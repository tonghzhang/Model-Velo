package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"model-velo/internal/adminauth"
	"model-velo/internal/apikey"
	"model-velo/internal/config"
	"model-velo/internal/controlplane"
	"model-velo/internal/postgres"
	"model-velo/internal/quota"
)

const (
	maxAdminBodyBytes int64 = 2 << 20
	adminIdentityKey        = "model-velo.admin-identity"
)

type adminHandler struct {
	auth    *adminauth.Manager
	service *controlplane.Service
	quota   *quota.Manager
	tenants *apikey.Manager
}

func registerAdminRoutes(
	router *gin.Engine,
	auth *adminauth.Manager,
	service *controlplane.Service,
	quotaManager *quota.Manager,
	tenantManager *apikey.Manager,
	usageReader PlatformUsageReader,
) {
	handler := adminHandler{
		auth: auth, service: service, quota: quotaManager, tenants: tenantManager,
	}
	usageQueries := adminUsageHandler{reader: usageReader}
	group := router.Group("/admin/v1")
	group.Use(handler.authenticate)

	group.GET("/runtime", handler.require(adminauth.PermissionRuntimeRead), handler.getRuntime)
	group.PUT("/runtime", handler.require(adminauth.PermissionRuntimeWrite), handler.putRuntime)
	group.GET("/pricing", handler.require(adminauth.PermissionPricingRead), handler.getPricing)
	group.PUT("/pricing", handler.require(adminauth.PermissionPricingWrite), handler.putPricing)
	group.GET("/audit", handler.require(adminauth.PermissionAuditRead), handler.audit)
	group.GET("/principals", handler.require(adminauth.PermissionAdminRead), handler.principals)
	group.POST("/principals", handler.require(adminauth.PermissionAdminWrite), handler.createPrincipal)
	group.GET(
		"/usage/events",
		handler.require(adminauth.PermissionUsageRead),
		usageQueries.list,
	)
	group.GET(
		"/usage/summary",
		handler.require(adminauth.PermissionUsageRead),
		usageQueries.summary,
	)
	group.GET(
		"/usage/series",
		handler.require(adminauth.PermissionUsageRead),
		usageQueries.series,
	)
	group.PATCH(
		"/principals/:id",
		handler.require(adminauth.PermissionAdminWrite),
		handler.updatePrincipal,
	)
	if quotaManager != nil {
		group.GET("/quotas", handler.require(adminauth.PermissionQuotaRead), handler.quotas)
		group.GET(
			"/quota-windows", handler.require(adminauth.PermissionQuotaRead),
			handler.quotaWindows,
		)
		group.POST("/quotas", handler.require(adminauth.PermissionQuotaWrite), handler.createQuota)
		group.PUT(
			"/quotas/:id", handler.require(adminauth.PermissionQuotaWrite),
			handler.updateQuota,
		)
	}
	if tenantManager != nil {
		group.GET("/tenants", handler.require(adminauth.PermissionTenantRead), handler.tenantsList)
		group.POST("/tenants", handler.require(adminauth.PermissionTenantWrite), handler.createTenant)
		group.PUT(
			"/tenants/:id", handler.require(adminauth.PermissionTenantWrite),
			handler.updateTenant,
		)
		group.GET(
			"/tenants/:id/keys", handler.require(adminauth.PermissionTenantRead),
			handler.tenantKeys,
		)
		group.POST(
			"/tenants/:id/keys", handler.require(adminauth.PermissionTenantWrite),
			handler.createTenantKey,
		)
		group.PATCH(
			"/api-keys/:id", handler.require(adminauth.PermissionTenantWrite),
			handler.updateAPIKey,
		)
	}
}

func (handler adminHandler) authenticate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	scheme, credential, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") ||
		strings.TrimSpace(credential) == "" {
		writeAdminError(c, http.StatusUnauthorized, "admin_auth_required", "admin bearer key is required")
		c.Abort()
		return
	}
	identity, err := handler.auth.Authenticate(c.Request.Context(), credential)
	if err != nil {
		status := http.StatusUnauthorized
		code := "invalid_admin_key"
		if !errors.Is(err, adminauth.ErrInvalidCredential) &&
			!errors.Is(err, adminauth.ErrInactive) {
			status = http.StatusServiceUnavailable
			code = "admin_auth_unavailable"
		}
		writeAdminError(c, status, code, "admin authentication failed")
		c.Abort()
		return
	}
	c.Set(adminIdentityKey, identity)
	c.Next()
}

func (handler adminHandler) require(permission adminauth.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := adminIdentity(c)
		if !ok || !identity.Allows(permission) {
			writeAdminError(c, http.StatusForbidden, "admin_permission_denied", "admin permission denied")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (handler adminHandler) getRuntime(c *gin.Context) {
	view, err := handler.service.Runtime()
	if errors.Is(err, controlplane.ErrNotConfigured) {
		writeAdminError(c, http.StatusNotFound, "runtime_not_managed", "no managed runtime is active")
		return
	}
	if err != nil {
		writeAdminError(c, http.StatusInternalServerError, "runtime_read_failed", "runtime could not be read")
		return
	}
	writeVersion(c, view.Version)
	c.JSON(http.StatusOK, view)
}

func (handler adminHandler) putRuntime(c *gin.Context) {
	expected, ok := expectedVersion(c)
	if !ok {
		return
	}
	var document controlplane.RuntimeDocument
	if !decodeAdminJSON(c, &document) {
		return
	}
	view, err := handler.service.UpdateRuntime(
		c.Request.Context(), expected, document, auditMeta(c),
	)
	if err != nil {
		writeControlPlaneError(c, err, "runtime_update_failed")
		return
	}
	writeVersion(c, view.Version)
	c.JSON(http.StatusOK, view)
}

func (handler adminHandler) getPricing(c *gin.Context) {
	view, err := handler.service.Pricing()
	if errors.Is(err, controlplane.ErrNotConfigured) {
		writeAdminError(c, http.StatusNotFound, "pricing_not_managed", "no managed pricing is active")
		return
	}
	if err != nil {
		writeAdminError(c, http.StatusInternalServerError, "pricing_read_failed", "pricing could not be read")
		return
	}
	writeVersion(c, view.Version)
	c.JSON(http.StatusOK, view)
}

func (handler adminHandler) putPricing(c *gin.Context) {
	expected, ok := expectedVersion(c)
	if !ok {
		return
	}
	var input struct {
		Prices []config.UsagePrice `json:"prices"`
	}
	if !decodeAdminJSON(c, &input) {
		return
	}
	view, err := handler.service.UpdatePricing(
		c.Request.Context(), expected, input.Prices, auditMeta(c),
	)
	if err != nil {
		writeControlPlaneError(c, err, "pricing_update_failed")
		return
	}
	writeVersion(c, view.Version)
	c.JSON(http.StatusOK, view)
}

func (handler adminHandler) audit(c *gin.Context) {
	cursor, err := parseUintQuery(c.Query("before_id"))
	if err != nil {
		writeAdminError(c, http.StatusBadRequest, "invalid_cursor", "before_id must be an unsigned integer")
		return
	}
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 1 || value > 200 {
			writeAdminError(c, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 200")
			return
		}
		limit = value
	}
	page, err := handler.service.Audit(c.Request.Context(), cursor, limit)
	if err != nil {
		writeAdminError(c, http.StatusInternalServerError, "audit_read_failed", "audit log could not be read")
		return
	}
	c.JSON(http.StatusOK, page)
}

func (handler adminHandler) principals(c *gin.Context) {
	principals, err := handler.auth.List(c.Request.Context())
	if err != nil {
		writeAdminError(c, http.StatusInternalServerError, "principals_read_failed", "admin principals could not be read")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": principals})
}

func (handler adminHandler) createPrincipal(c *gin.Context) {
	var input struct {
		Name  string           `json:"name"`
		Roles []adminauth.Role `json:"roles"`
	}
	if !decodeAdminJSON(c, &input) {
		return
	}
	meta := auditMeta(c)
	issued, err := handler.auth.CreateAudited(
		c.Request.Context(), input.Name, input.Roles,
		adminauth.MutationMeta{
			ActorID: meta.PrincipalID, RequestID: meta.RequestID,
			RemoteIP: meta.RemoteIP,
		},
	)
	if err != nil {
		writeAdminError(c, http.StatusBadRequest, "invalid_principal", "admin principal could not be created")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, issued)
}

func (handler adminHandler) updatePrincipal(c *gin.Context) {
	var input struct {
		Status postgres.AdminPrincipalStatus `json:"status"`
		Roles  []adminauth.Role              `json:"roles"`
	}
	if !decodeAdminJSON(c, &input) {
		return
	}
	principalID := strings.TrimSpace(c.Param("id"))
	meta := auditMeta(c)
	if err := handler.auth.UpdateAudited(
		c.Request.Context(), principalID, input.Status, input.Roles,
		adminauth.MutationMeta{
			ActorID: meta.PrincipalID, RequestID: meta.RequestID,
			RemoteIP: meta.RemoteIP,
		},
	); err != nil {
		status := http.StatusBadRequest
		code := "invalid_principal_update"
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
			code = "principal_not_found"
		}
		if errors.Is(err, adminauth.ErrLastOwner) {
			status = http.StatusConflict
			code = "last_owner_required"
		}
		writeAdminError(c, status, code, "admin principal could not be updated")
		return
	}
	c.Status(http.StatusNoContent)
}

func (handler adminHandler) quotas(c *gin.Context) {
	policies, err := handler.quota.ListPolicies(
		c.Request.Context(), strings.TrimSpace(c.Query("tenant_id")),
	)
	if err != nil {
		writeAdminError(c, http.StatusInternalServerError, "quotas_read_failed", "quota policies could not be read")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": policies})
}

func (handler adminHandler) quotaWindows(c *gin.Context) {
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			writeAdminError(
				c, http.StatusBadRequest, "invalid_limit",
				"limit must be between 1 and 500",
			)
			return
		}
		limit = value
	}
	windows, err := handler.quota.ListWindows(
		c.Request.Context(), strings.TrimSpace(c.Query("tenant_id")), limit,
	)
	if err != nil {
		writeAdminError(
			c, http.StatusInternalServerError,
			"quota_windows_read_failed", "quota windows could not be read",
		)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": windows})
}

func (handler adminHandler) createQuota(c *gin.Context) {
	var input quota.PolicyInput
	if !decodeAdminJSON(c, &input) {
		return
	}
	identity, _ := adminIdentity(c)
	policy, err := handler.quota.CreatePolicyAudited(
		c.Request.Context(), input, quota.MutationMeta{
			ActorID:   identity.PrincipalID,
			RequestID: requestIDFromContext(c.Request.Context()),
			RemoteIP:  c.ClientIP(),
		},
	)
	if err != nil {
		if errors.Is(err, quota.ErrTenantNotFound) {
			writeAdminError(
				c, http.StatusNotFound,
				"tenant_not_found", "quota tenant was not found",
			)
			return
		}
		writeAdminError(c, http.StatusBadRequest, "invalid_quota_policy", "quota policy could not be created")
		return
	}
	writeVersion(c, policy.Version)
	c.JSON(http.StatusCreated, policy)
}

func (handler adminHandler) updateQuota(c *gin.Context) {
	expected, ok := expectedVersion(c)
	if !ok {
		return
	}
	var input quota.PolicyInput
	if !decodeAdminJSON(c, &input) {
		return
	}
	identity, _ := adminIdentity(c)
	policy, err := handler.quota.UpdatePolicyAudited(
		c.Request.Context(), c.Param("id"), expected, input, quota.MutationMeta{
			ActorID:   identity.PrincipalID,
			RequestID: requestIDFromContext(c.Request.Context()),
			RemoteIP:  c.ClientIP(),
		},
	)
	if err != nil {
		switch {
		case errors.Is(err, quota.ErrVersionConflict):
			writeAdminError(c, http.StatusConflict, "version_conflict", "quota policy changed; read it and retry")
		case errors.Is(err, gorm.ErrRecordNotFound):
			writeAdminError(c, http.StatusNotFound, "quota_not_found", "quota policy was not found")
		case errors.Is(err, quota.ErrTenantNotFound):
			writeAdminError(c, http.StatusNotFound, "tenant_not_found", "quota tenant was not found")
		default:
			writeAdminError(c, http.StatusBadRequest, "invalid_quota_policy", "quota policy could not be updated")
		}
		return
	}
	writeVersion(c, policy.Version)
	c.JSON(http.StatusOK, policy)
}

func (handler adminHandler) tenantsList(c *gin.Context) {
	tenants, err := handler.tenants.ListTenants(c.Request.Context())
	if err != nil {
		writeAdminError(
			c, http.StatusInternalServerError,
			"tenants_read_failed", "tenants could not be read",
		)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": tenants})
}

func (handler adminHandler) createTenant(c *gin.Context) {
	var input apikey.BootstrapTenantInput
	if !decodeAdminJSON(c, &input) {
		return
	}
	tenant, issued, err := handler.tenants.CreateTenantAudited(
		c.Request.Context(), input, apiKeyMutationMeta(c),
	)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_tenant"
		if errors.Is(err, apikey.ErrTenantAlreadyExists) {
			status = http.StatusConflict
			code = "tenant_already_exists"
		}
		writeAdminError(c, status, code, "tenant could not be created")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{"tenant": tenant, "key": issued})
}

func (handler adminHandler) updateTenant(c *gin.Context) {
	expected, ok := expectedVersion(c)
	if !ok {
		return
	}
	var input apikey.TenantUpdateInput
	if !decodeAdminJSON(c, &input) {
		return
	}
	tenant, err := handler.tenants.UpdateTenantAudited(
		c.Request.Context(), c.Param("id"), expected, input,
		apiKeyMutationMeta(c),
	)
	if err != nil {
		switch {
		case errors.Is(err, apikey.ErrTenantVersionConflict):
			writeAdminError(
				c, http.StatusConflict, "version_conflict",
				"tenant changed; read it and retry",
			)
		case errors.Is(err, gorm.ErrRecordNotFound):
			writeAdminError(
				c, http.StatusNotFound, "tenant_not_found",
				"tenant was not found",
			)
		default:
			writeAdminError(
				c, http.StatusBadRequest, "invalid_tenant",
				"tenant could not be updated",
			)
		}
		return
	}
	writeVersion(c, tenant.Version)
	c.JSON(http.StatusOK, tenant)
}

func (handler adminHandler) tenantKeys(c *gin.Context) {
	keys, err := handler.tenants.ListKeys(c.Request.Context(), c.Param("id"))
	if err != nil {
		status := http.StatusInternalServerError
		code := "api_keys_read_failed"
		switch {
		case errors.Is(err, apikey.ErrInvalidInput):
			status = http.StatusBadRequest
			code = "invalid_tenant"
		case errors.Is(err, apikey.ErrTenantNotFound):
			status = http.StatusNotFound
			code = "tenant_not_found"
		}
		writeAdminError(c, status, code, "API keys could not be read")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": keys})
}

func (handler adminHandler) createTenantKey(c *gin.Context) {
	var input struct {
		Label     string     `json:"label"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
	if !decodeAdminJSON(c, &input) {
		return
	}
	issued, err := handler.tenants.CreateKeyAudited(
		c.Request.Context(),
		apikey.CreateKeyInput{
			TenantID: c.Param("id"), Label: input.Label, ExpiresAt: input.ExpiresAt,
		},
		apiKeyMutationMeta(c),
	)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_api_key"
		if errors.Is(err, apikey.ErrTenantNotFound) {
			status = http.StatusNotFound
			code = "tenant_not_found"
		}
		if errors.Is(err, apikey.ErrTenantInactive) {
			status = http.StatusConflict
			code = "tenant_inactive"
		}
		writeAdminError(c, status, code, "API key could not be created")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, issued)
}

func (handler adminHandler) updateAPIKey(c *gin.Context) {
	var input struct {
		Status postgres.APIKeyStatus `json:"status"`
	}
	if !decodeAdminJSON(c, &input) {
		return
	}
	key, err := handler.tenants.UpdateKeyStatusAudited(
		c.Request.Context(), c.Param("id"), input.Status,
		apiKeyMutationMeta(c),
	)
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			writeAdminError(
				c, http.StatusNotFound, "api_key_not_found",
				"API key was not found",
			)
		case errors.Is(err, apikey.ErrKeyRevoked):
			writeAdminError(
				c, http.StatusConflict, "api_key_revoked",
				"a revoked API key cannot be reactivated",
			)
		case errors.Is(err, apikey.ErrKeyExpired):
			writeAdminError(
				c, http.StatusConflict, "api_key_expired",
				"an expired API key cannot be activated",
			)
		default:
			writeAdminError(
				c, http.StatusBadRequest, "invalid_api_key_status",
				"API key status could not be updated",
			)
		}
		return
	}
	c.JSON(http.StatusOK, key)
}

func apiKeyMutationMeta(c *gin.Context) apikey.MutationMeta {
	meta := auditMeta(c)
	return apikey.MutationMeta{
		ActorID: meta.PrincipalID, RequestID: meta.RequestID,
		RemoteIP: meta.RemoteIP,
	}
}

func decodeAdminJSON(c *gin.Context, target any) bool {
	if !hasJSONContentType(c.GetHeader("Content-Type")) {
		writeAdminError(c, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAdminBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var sizeError *http.MaxBytesError
		status := http.StatusBadRequest
		code := "invalid_json"
		message := "request body must be one JSON object"
		if errors.As(err, &sizeError) {
			status = http.StatusRequestEntityTooLarge
			code = "request_too_large"
			message = "request body exceeds the size limit"
		}
		writeAdminError(c, status, code, message)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeAdminError(c, http.StatusBadRequest, "invalid_json", "request body must contain one JSON object")
		return false
	}
	return true
}

func expectedVersion(c *gin.Context) (uint64, bool) {
	raw := strings.TrimSpace(c.GetHeader("If-Match"))
	if raw == "" {
		writeAdminError(c, http.StatusPreconditionRequired, "if_match_required", "If-Match version is required")
		return 0, false
	}
	raw = strings.Trim(raw, `"`)
	version, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		writeAdminError(c, http.StatusBadRequest, "invalid_if_match", "If-Match must contain a numeric version")
		return 0, false
	}
	return version, true
}

func writeVersion(c *gin.Context, version uint64) {
	c.Header("ETag", `"`+strconv.FormatUint(version, 10)+`"`)
	c.Header("Cache-Control", "no-store")
}

func writeControlPlaneError(c *gin.Context, err error, fallbackCode string) {
	switch {
	case errors.Is(err, controlplane.ErrVersionConflict):
		writeAdminError(c, http.StatusConflict, "version_conflict", "managed resource changed; read it and retry")
	default:
		writeAdminError(c, http.StatusBadRequest, fallbackCode, err.Error())
	}
}

func writeAdminError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"code": code, "message": message,
			"request_id": requestIDFromContext(c.Request.Context()),
		},
	})
}

func adminIdentity(c *gin.Context) (adminauth.Identity, bool) {
	value, ok := c.Get(adminIdentityKey)
	if !ok {
		return adminauth.Identity{}, false
	}
	identity, ok := value.(adminauth.Identity)
	return identity, ok
}

func auditMeta(c *gin.Context) controlplane.AuditMeta {
	identity, _ := adminIdentity(c)
	return controlplane.AuditMeta{
		PrincipalID: identity.PrincipalID,
		RequestID:   requestIDFromContext(c.Request.Context()),
		RemoteIP:    c.ClientIP(),
	}
}

func parseUintQuery(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseUint(raw, 10, 64)
}
