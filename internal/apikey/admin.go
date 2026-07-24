package apikey

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"model-velo/internal/postgres"
)

var ErrTenantVersionConflict = errors.New("tenant version conflict")

type MutationMeta struct {
	ActorID   string
	RequestID string
	RemoteIP  string
}

type TenantUpdateInput struct {
	DisplayName string                `json:"display_name"`
	Status      postgres.TenantStatus `json:"status"`
	Models      []string              `json:"models"`
}

type TenantView struct {
	ID          string                `json:"id"`
	Slug        string                `json:"slug"`
	DisplayName string                `json:"display_name"`
	Status      postgres.TenantStatus `json:"status"`
	Models      []string              `json:"models"`
	Version     uint64                `json:"version"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
}

type KeyView struct {
	ID         string                `json:"id"`
	TenantID   string                `json:"tenant_id"`
	Label      string                `json:"label"`
	KeyPrefix  string                `json:"key_prefix"`
	Status     postgres.APIKeyStatus `json:"status"`
	ExpiresAt  *time.Time            `json:"expires_at,omitempty"`
	LastUsedAt *time.Time            `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time            `json:"revoked_at,omitempty"`
	CreatedAt  time.Time             `json:"created_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
}

func (manager *Manager) CreateTenantAudited(
	ctx context.Context,
	input BootstrapTenantInput,
	meta MutationMeta,
) (TenantView, IssuedKey, error) {
	normalized, err := normalizeBootstrapInput(input, manager.now().UTC())
	if err != nil {
		return TenantView{}, IssuedKey{}, err
	}
	tenantID, err := randomUUID()
	if err != nil {
		return TenantView{}, IssuedKey{}, err
	}
	now := manager.now().UTC()
	tenant := postgres.Tenant{
		ID: tenantID, Slug: normalized.Slug,
		DisplayName: normalized.DisplayName, Status: postgres.TenantActive,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	var issued IssuedKey
	err = manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&tenant).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrTenantAlreadyExists
			}
			return errors.New("create tenant")
		}
		if err := replaceModelGrants(transaction, tenant.ID, normalized.Models, now); err != nil {
			return err
		}
		issued, err = manager.createKey(transaction, CreateKeyInput{
			TenantID: tenant.ID, Label: normalized.KeyLabel,
			ExpiresAt: normalized.ExpiresAt,
		})
		if err != nil {
			return err
		}
		view := tenantView(tenant, normalized.Models)
		return writeAdminAudit(
			transaction, meta, "tenant.create", "tenant", tenant.ID,
			nil, view,
		)
	})
	if err != nil {
		return TenantView{}, IssuedKey{}, err
	}
	return tenantView(tenant, normalized.Models), issued, nil
}

func (manager *Manager) ListTenants(ctx context.Context) ([]TenantView, error) {
	var tenants []postgres.Tenant
	if err := manager.database.WithContext(ctx).
		Order("created_at ASC, id ASC").
		Limit(1000).
		Find(&tenants).Error; err != nil {
		return nil, errors.New("list tenants")
	}
	tenantIDs := make([]string, 0, len(tenants))
	for _, tenant := range tenants {
		tenantIDs = append(tenantIDs, tenant.ID)
	}
	var grants []postgres.TenantModelGrant
	if len(tenantIDs) > 0 {
		if err := manager.database.WithContext(ctx).
			Where("tenant_id IN ?", tenantIDs).
			Order("tenant_id ASC, gateway_model ASC").
			Find(&grants).Error; err != nil {
			return nil, errors.New("list tenant model grants")
		}
	}
	modelsByTenant := make(map[string][]string, len(tenants))
	for _, grant := range grants {
		modelsByTenant[grant.TenantID] = append(
			modelsByTenant[grant.TenantID],
			grant.GatewayModel,
		)
	}
	views := make([]TenantView, 0, len(tenants))
	for _, tenant := range tenants {
		views = append(views, tenantView(tenant, modelsByTenant[tenant.ID]))
	}
	return views, nil
}

func (manager *Manager) UpdateTenantAudited(
	ctx context.Context,
	tenantID string,
	expectedVersion uint64,
	input TenantUpdateInput,
	meta MutationMeta,
) (TenantView, error) {
	tenantID = strings.TrimSpace(tenantID)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	models, err := normalizeModels(input.Models)
	if tenantID == "" || input.DisplayName == "" ||
		utf8.RuneCountInString(input.DisplayName) > 160 ||
		(input.Status != postgres.TenantActive &&
			input.Status != postgres.TenantDisabled) ||
		err != nil || len(models) == 0 {
		return TenantView{}, ErrInvalidInput
	}
	var updated postgres.Tenant
	err = manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var current postgres.Tenant
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&current, "id = ?", tenantID).Error; err != nil {
			return err
		}
		if current.Version != expectedVersion {
			return ErrTenantVersionConflict
		}
		beforeModels, err := tenantModels(transaction, current.ID)
		if err != nil {
			return err
		}
		updated = current
		updated.DisplayName = input.DisplayName
		updated.Status = input.Status
		updated.Version++
		updated.UpdatedAt = manager.now().UTC()
		if err := transaction.Save(&updated).Error; err != nil {
			return errors.New("update tenant")
		}
		if err := transaction.Where("tenant_id = ?", tenantID).
			Delete(&postgres.TenantModelGrant{}).Error; err != nil {
			return errors.New("replace tenant model grants")
		}
		if err := replaceModelGrants(transaction, tenantID, models, updated.UpdatedAt); err != nil {
			return err
		}
		return writeAdminAudit(
			transaction, meta, "tenant.update", "tenant", tenantID,
			tenantView(current, beforeModels), tenantView(updated, models),
		)
	})
	if err != nil {
		return TenantView{}, err
	}
	return tenantView(updated, models), nil
}

func (manager *Manager) CreateKeyAudited(
	ctx context.Context,
	input CreateKeyInput,
	meta MutationMeta,
) (IssuedKey, error) {
	normalized, err := normalizeCreateKeyInput(input, manager.now().UTC())
	if err != nil {
		return IssuedKey{}, err
	}
	var issued IssuedKey
	err = manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var tenant postgres.Tenant
		if err := transaction.Clauses(clause.Locking{Strength: "SHARE"}).
			First(&tenant, "id = ?", normalized.TenantID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTenantNotFound
			}
			return errors.New("read tenant")
		}
		if tenant.Status != postgres.TenantActive {
			return ErrTenantInactive
		}
		issued, err = manager.createKey(transaction, normalized)
		if err != nil {
			return err
		}
		return writeAdminAudit(
			transaction, meta, "api_key.create", "api_key", issued.ID,
			nil, map[string]any{
				"id": issued.ID, "tenant_id": issued.TenantID,
				"key_prefix": issued.Prefix, "expires_at": issued.ExpiresAt,
			},
		)
	})
	return issued, err
}

func (manager *Manager) ListKeys(
	ctx context.Context,
	tenantID string,
) ([]KeyView, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrInvalidInput
	}
	var tenantCount int64
	if err := manager.database.WithContext(ctx).
		Model(&postgres.Tenant{}).
		Where("id = ?", tenantID).
		Count(&tenantCount).Error; err != nil {
		return nil, errors.New("check tenant")
	}
	if tenantCount == 0 {
		return nil, ErrTenantNotFound
	}
	var rows []postgres.APIKey
	if err := manager.database.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at ASC, id ASC").
		Limit(1000).
		Find(&rows).Error; err != nil {
		return nil, errors.New("list API keys")
	}
	result := make([]KeyView, 0, len(rows))
	for _, row := range rows {
		result = append(result, keyView(row))
	}
	return result, nil
}

func (manager *Manager) UpdateKeyStatusAudited(
	ctx context.Context,
	keyID string,
	status postgres.APIKeyStatus,
	meta MutationMeta,
) (KeyView, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" || (status != postgres.APIKeyActive &&
		status != postgres.APIKeyDisabled &&
		status != postgres.APIKeyRevoked) {
		return KeyView{}, ErrInvalidInput
	}
	var updated postgres.APIKey
	err := manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var current postgres.APIKey
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&current, "id = ?", keyID).Error; err != nil {
			return err
		}
		if current.Status == postgres.APIKeyRevoked && status != postgres.APIKeyRevoked {
			return ErrKeyRevoked
		}
		if status == postgres.APIKeyActive &&
			expirationReached(current.ExpiresAt, manager.now().UTC()) {
			return ErrKeyExpired
		}
		updated = current
		updated.Status = status
		updated.UpdatedAt = manager.now().UTC()
		if status == postgres.APIKeyRevoked {
			updated.RevokedAt = &updated.UpdatedAt
		} else {
			updated.RevokedAt = nil
		}
		if err := transaction.Save(&updated).Error; err != nil {
			return errors.New("update API key")
		}
		return writeAdminAudit(
			transaction, meta, "api_key.status.update", "api_key", keyID,
			keyView(current), keyView(updated),
		)
	})
	return keyView(updated), err
}

func replaceModelGrants(
	transaction *gorm.DB,
	tenantID string,
	models []string,
	now time.Time,
) error {
	grants := make([]postgres.TenantModelGrant, 0, len(models))
	for _, model := range models {
		grants = append(grants, postgres.TenantModelGrant{
			TenantID: tenantID, GatewayModel: model, CreatedAt: now,
		})
	}
	if err := transaction.Create(&grants).Error; err != nil {
		return errors.New("create tenant model grants")
	}
	return nil
}

func tenantModels(database *gorm.DB, tenantID string) ([]string, error) {
	var models []string
	if err := database.Model(&postgres.TenantModelGrant{}).
		Where("tenant_id = ?", tenantID).
		Order("gateway_model ASC").
		Pluck("gateway_model", &models).Error; err != nil {
		return nil, errors.New("read tenant model grants")
	}
	return models, nil
}

func tenantView(tenant postgres.Tenant, models []string) TenantView {
	return TenantView{
		ID: tenant.ID, Slug: tenant.Slug, DisplayName: tenant.DisplayName,
		Status: tenant.Status, Models: append([]string(nil), models...),
		Version: tenant.Version, CreatedAt: tenant.CreatedAt, UpdatedAt: tenant.UpdatedAt,
	}
}

func keyView(key postgres.APIKey) KeyView {
	return KeyView{
		ID: key.ID, TenantID: key.TenantID, Label: key.Label,
		KeyPrefix: key.KeyPrefix, Status: key.Status,
		ExpiresAt: key.ExpiresAt, LastUsedAt: key.LastUsedAt,
		RevokedAt: key.RevokedAt, CreatedAt: key.CreatedAt, UpdatedAt: key.UpdatedAt,
	}
}

func writeAdminAudit(
	transaction *gorm.DB,
	meta MutationMeta,
	action string,
	resourceType string,
	resourceID string,
	before any,
	after any,
) error {
	if strings.TrimSpace(meta.ActorID) == "" ||
		strings.TrimSpace(meta.RequestID) == "" {
		return errors.New("API key audit metadata is incomplete")
	}
	beforeJSON, err := adminAuditJSON(before)
	if err != nil {
		return err
	}
	afterJSON, err := adminAuditJSON(after)
	if err != nil {
		return err
	}
	if err := transaction.Create(&postgres.AuditLog{
		PrincipalID: meta.ActorID, Action: action,
		ResourceType: resourceType, ResourceID: resourceID,
		RequestID: meta.RequestID, RemoteIP: meta.RemoteIP,
		BeforeJSON: beforeJSON, AfterJSON: afterJSON,
		Outcome: "success", CreatedAt: time.Now().UTC(),
	}).Error; err != nil {
		return errors.New("append API key audit log")
	}
	return nil
}

func adminAuditJSON(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("encode API key audit snapshot")
	}
	result := string(encoded)
	return &result, nil
}
