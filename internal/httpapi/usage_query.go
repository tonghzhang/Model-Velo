package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"model-velo/internal/usage"
)

type UsageReader interface {
	List(context.Context, string, usage.ListParams) (usage.Page, error)
	Summary(context.Context, string, usage.SummaryParams) (usage.Summary, error)
	Series(context.Context, string, usage.SeriesParams) ([]usage.SeriesPoint, error)
}

type usageQueryHandler struct {
	reader UsageReader
}

func (handler usageQueryHandler) list(c *gin.Context) {
	if !knownQueryFields(c, "start", "end", "model", "provider", "api_key_id", "request_id", "status", "cache_status", "stream", "limit", "cursor", "include_raw") {
		writeUsageQueryError(c)
		return
	}
	filter, err := usageFilterFromRequest(c)
	if err != nil {
		writeUsageQueryError(c)
		return
	}
	limit, err := optionalQueryInt(c, "limit")
	if err != nil {
		writeUsageQueryError(c)
		return
	}
	includeRaw, err := optionalQueryBool(c, "include_raw")
	if err != nil {
		writeUsageQueryError(c)
		return
	}
	cursor, ok := singleQuery(c, "cursor")
	if !ok || len(cursor) > 512 {
		writeUsageQueryError(c)
		return
	}
	tenantID, ok := scopeUsageFilter(c, &filter)
	if !ok {
		return
	}
	page, err := handler.reader.List(c.Request.Context(), tenantID, usage.ListParams{
		Filter:     filter,
		Limit:      limit,
		Cursor:     cursor,
		IncludeRaw: includeRaw != nil && *includeRaw,
	})
	if err != nil {
		writeUsageReadError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, page)
}

func (handler usageQueryHandler) summary(c *gin.Context) {
	if !knownQueryFields(c, "start", "end", "model", "provider", "api_key_id", "request_id", "status", "cache_status", "stream", "group_by") {
		writeUsageQueryError(c)
		return
	}
	filter, err := usageFilterFromRequest(c)
	if err != nil {
		writeUsageQueryError(c)
		return
	}
	groupBy, ok := singleQuery(c, "group_by")
	if !ok || len(groupBy) > 32 {
		writeUsageQueryError(c)
		return
	}
	tenantID, ok := scopeUsageFilter(c, &filter)
	if !ok {
		return
	}
	summary, err := handler.reader.Summary(c.Request.Context(), tenantID, usage.SummaryParams{
		Filter:  filter,
		GroupBy: groupBy,
	})
	if err != nil {
		writeUsageReadError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, summary)
}

func (handler usageQueryHandler) series(c *gin.Context) {
	if !knownQueryFields(c, "start", "end", "model", "provider", "api_key_id", "request_id", "status", "cache_status", "stream", "interval", "timezone") {
		writeUsageQueryError(c)
		return
	}
	filter, err := usageFilterFromRequest(c)
	if err != nil {
		writeUsageQueryError(c)
		return
	}
	interval, intervalOK := singleQuery(c, "interval")
	timezone, timezoneOK := singleQuery(c, "timezone")
	if !intervalOK || !timezoneOK || len(interval) > 16 || len(timezone) > 100 {
		writeUsageQueryError(c)
		return
	}
	tenantID, ok := scopeUsageFilter(c, &filter)
	if !ok {
		return
	}
	points, err := handler.reader.Series(c.Request.Context(), tenantID, usage.SeriesParams{
		Filter:   filter,
		Interval: interval,
		Timezone: timezone,
	})
	if err != nil {
		writeUsageReadError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": points})
}

func scopeUsageFilter(c *gin.Context, filter *usage.QueryFilter) (string, bool) {
	identity, ok := identityFromContext(c.Request.Context())
	if !ok || strings.TrimSpace(identity.TenantID) == "" ||
		strings.TrimSpace(identity.APIKeyID) == "" {
		writeIdentityUnavailable(c)
		return "", false
	}
	apiKeyID := strings.TrimSpace(identity.APIKeyID)
	if filter.APIKeyID != "" && filter.APIKeyID != apiKeyID {
		writeUsageQueryError(c)
		return "", false
	}
	filter.APIKeyID = apiKeyID
	return strings.TrimSpace(identity.TenantID), true
}

func usageFilterFromRequest(c *gin.Context) (usage.QueryFilter, error) {
	start, err := optionalQueryTime(c, "start")
	if err != nil {
		return usage.QueryFilter{}, err
	}
	end, err := optionalQueryTime(c, "end")
	if err != nil {
		return usage.QueryFilter{}, err
	}
	stream, err := optionalQueryBool(c, "stream")
	if err != nil {
		return usage.QueryFilter{}, err
	}
	model, modelOK := singleQuery(c, "model")
	providerID, providerOK := singleQuery(c, "provider")
	apiKeyID, apiKeyOK := singleQuery(c, "api_key_id")
	requestID, requestIDOK := singleQuery(c, "request_id")
	status, statusOK := singleQuery(c, "status")
	cacheStatus, cacheOK := singleQuery(c, "cache_status")
	if !modelOK || !providerOK || !apiKeyOK || !requestIDOK || !statusOK || !cacheOK {
		return usage.QueryFilter{}, usage.ErrInvalidQuery
	}
	return usage.QueryFilter{
		Start:       start,
		End:         end,
		Model:       model,
		ProviderID:  providerID,
		APIKeyID:    apiKeyID,
		RequestID:   requestID,
		Status:      usage.Status(status),
		CacheStatus: cacheStatus,
		Stream:      stream,
	}, nil
}

func optionalQueryTime(c *gin.Context, name string) (time.Time, error) {
	raw, ok := singleQuery(c, name)
	if !ok {
		return time.Time{}, usage.ErrInvalidQuery
	}
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, usage.ErrInvalidQuery
	}
	return parsed, nil
}

func optionalQueryBool(c *gin.Context, name string) (*bool, error) {
	raw, ok := singleQuery(c, name)
	if !ok {
		return nil, usage.ErrInvalidQuery
	}
	if raw == "" {
		return nil, nil
	}
	if raw != "true" && raw != "false" {
		return nil, usage.ErrInvalidQuery
	}
	value, _ := strconv.ParseBool(raw)
	return &value, nil
}

func optionalQueryInt(c *gin.Context, name string) (int, error) {
	raw, ok := singleQuery(c, name)
	if !ok {
		return 0, usage.ErrInvalidQuery
	}
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, usage.ErrInvalidQuery
	}
	return value, nil
}

func singleQuery(c *gin.Context, name string) (string, bool) {
	values, exists := c.Request.URL.Query()[name]
	if !exists {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

func knownQueryFields(c *gin.Context, allowed ...string) bool {
	known := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		known[name] = struct{}{}
	}
	for name := range c.Request.URL.Query() {
		if _, ok := known[name]; !ok {
			return false
		}
	}
	return true
}

func writeUsageReadError(c *gin.Context, err error) {
	if errors.Is(err, usage.ErrInvalidQuery) {
		writeUsageQueryError(c)
		return
	}
	writeAPIError(
		c,
		http.StatusServiceUnavailable,
		"usage data is temporarily unavailable",
		"server_error",
		nil,
		"usage_unavailable",
	)
}

func writeUsageQueryError(c *gin.Context) {
	writeAPIError(
		c,
		http.StatusBadRequest,
		"usage query parameters are invalid",
		"invalid_request_error",
		nil,
		"invalid_usage_query",
	)
}

func writeIdentityUnavailable(c *gin.Context) {
	writeAPIError(
		c,
		http.StatusInternalServerError,
		"authenticated identity is unavailable",
		"server_error",
		nil,
		"identity_unavailable",
	)
}
