package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"model-velo/internal/usage"
)

type PlatformUsageReader interface {
	PlatformList(
		context.Context,
		string,
		usage.ListParams,
	) (usage.PlatformPage, error)
	PlatformSummary(
		context.Context,
		string,
		usage.SummaryParams,
	) (usage.Summary, error)
	PlatformSeries(
		context.Context,
		string,
		usage.SeriesParams,
	) ([]usage.SeriesPoint, error)
}

type adminUsageHandler struct {
	reader PlatformUsageReader
}

func (handler adminUsageHandler) list(c *gin.Context) {
	if !knownQueryFields(
		c,
		"tenant_id", "start", "end", "model", "provider", "api_key_id",
		"request_id", "status", "cache_status", "stream", "limit", "cursor",
	) {
		writeAdminUsageQueryError(c)
		return
	}
	tenantID, filter, ok := adminUsageFilter(c)
	if !ok {
		return
	}
	limit, err := optionalQueryInt(c, "limit")
	if err != nil {
		writeAdminUsageQueryError(c)
		return
	}
	cursor, ok := singleQuery(c, "cursor")
	if !ok || len(cursor) > 512 {
		writeAdminUsageQueryError(c)
		return
	}
	page, err := handler.reader.PlatformList(
		c.Request.Context(),
		tenantID,
		usage.ListParams{Filter: filter, Limit: limit, Cursor: cursor},
	)
	if err != nil {
		writeAdminUsageReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

func (handler adminUsageHandler) summary(c *gin.Context) {
	if !knownQueryFields(
		c,
		"tenant_id", "start", "end", "model", "provider", "api_key_id",
		"request_id", "status", "cache_status", "stream", "group_by",
	) {
		writeAdminUsageQueryError(c)
		return
	}
	tenantID, filter, ok := adminUsageFilter(c)
	if !ok {
		return
	}
	groupBy, ok := singleQuery(c, "group_by")
	if !ok || len(groupBy) > 32 {
		writeAdminUsageQueryError(c)
		return
	}
	summary, err := handler.reader.PlatformSummary(
		c.Request.Context(),
		tenantID,
		usage.SummaryParams{Filter: filter, GroupBy: groupBy},
	)
	if err != nil {
		writeAdminUsageReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

func (handler adminUsageHandler) series(c *gin.Context) {
	if !knownQueryFields(
		c,
		"tenant_id", "start", "end", "model", "provider", "api_key_id",
		"request_id", "status", "cache_status", "stream", "interval", "timezone",
	) {
		writeAdminUsageQueryError(c)
		return
	}
	tenantID, filter, ok := adminUsageFilter(c)
	if !ok {
		return
	}
	interval, intervalOK := singleQuery(c, "interval")
	timezone, timezoneOK := singleQuery(c, "timezone")
	if !intervalOK || !timezoneOK || len(interval) > 16 || len(timezone) > 100 {
		writeAdminUsageQueryError(c)
		return
	}
	points, err := handler.reader.PlatformSeries(
		c.Request.Context(),
		tenantID,
		usage.SeriesParams{
			Filter: filter, Interval: interval, Timezone: timezone,
		},
	)
	if err != nil {
		writeAdminUsageReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": points})
}

func adminUsageFilter(c *gin.Context) (string, usage.QueryFilter, bool) {
	tenantID, ok := singleQuery(c, "tenant_id")
	if !ok || len(tenantID) > 64 {
		writeAdminUsageQueryError(c)
		return "", usage.QueryFilter{}, false
	}
	filter, err := usageFilterFromRequest(c)
	if err != nil {
		writeAdminUsageQueryError(c)
		return "", usage.QueryFilter{}, false
	}
	return tenantID, filter, true
}

func writeAdminUsageReadError(c *gin.Context, err error) {
	if errors.Is(err, usage.ErrInvalidQuery) {
		writeAdminUsageQueryError(c)
		return
	}
	writeAdminError(
		c,
		http.StatusServiceUnavailable,
		"usage_unavailable",
		"usage data is temporarily unavailable",
	)
}

func writeAdminUsageQueryError(c *gin.Context) {
	writeAdminError(
		c,
		http.StatusBadRequest,
		"invalid_usage_query",
		"usage query parameters are invalid",
	)
}
