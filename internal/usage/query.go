package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"model-velo/internal/postgres"
)

var ErrInvalidQuery = errors.New("invalid usage query")

const (
	defaultQueryWindow = 30 * 24 * time.Hour
	maximumQueryWindow = 366 * 24 * time.Hour
	defaultPageLimit   = 50
	maximumPageLimit   = 200
	maximumGroups      = 1_000
)

type QueryFilter struct {
	Start       time.Time
	End         time.Time
	Model       string
	ProviderID  string
	APIKeyID    string
	RequestID   string
	Status      Status
	CacheStatus string
	Stream      *bool
}

type ListParams struct {
	Filter     QueryFilter
	Limit      int
	Cursor     string
	IncludeRaw bool
}

type SummaryParams struct {
	Filter  QueryFilter
	GroupBy string
}

type SeriesParams struct {
	Filter   QueryFilter
	Interval string
	Timezone string
}

type TokenView struct {
	Input         int64               `json:"input"`
	Output        int64               `json:"output"`
	Total         int64               `json:"total"`
	InputDetails  *InputTokenDetails  `json:"input_details,omitempty"`
	OutputDetails *OutputTokenDetails `json:"output_details,omitempty"`
	Raw           json.RawMessage     `json:"raw,omitempty"`
}

type CostView struct {
	InputNanoUSD   *int64 `json:"input_nano_usd,omitempty"`
	OutputNanoUSD  *int64 `json:"output_nano_usd,omitempty"`
	TotalNanoUSD   int64  `json:"total_nano_usd"`
	TotalUSD       string `json:"total_usd"`
	Currency       string `json:"currency"`
	Source         string `json:"source"`
	PricingVersion string `json:"pricing_version,omitempty"`
	Caveat         string `json:"caveat,omitempty"`
}

type Record struct {
	SchemaVersion  int         `json:"schema_version"`
	EventID        string      `json:"event_id"`
	RequestID      string      `json:"request_id"`
	APIKeyID       string      `json:"api_key_id,omitempty"`
	RequestedModel string      `json:"requested_model"`
	ProviderID     string      `json:"provider_id,omitempty"`
	UpstreamModel  string      `json:"upstream_model,omitempty"`
	CacheStatus    string      `json:"cache_status"`
	Stream         bool        `json:"stream"`
	Attempts       int         `json:"attempts"`
	Retries        int         `json:"retries"`
	Fallbacks      int         `json:"fallbacks"`
	Usage          *TokenView  `json:"usage,omitempty"`
	UsageSource    UsageSource `json:"usage_source"`
	UsageCaveat    string      `json:"usage_caveat,omitempty"`
	Cost           *CostView   `json:"cost,omitempty"`
	CostCaveat     string      `json:"cost_caveat,omitempty"`
	FinishReason   string      `json:"finish_reason,omitempty"`
	Status         Status      `json:"status"`
	ErrorCategory  string      `json:"error_category,omitempty"`
	ErrorCode      string      `json:"error_code,omitempty"`
	StartedAt      time.Time   `json:"started_at"`
	EndedAt        time.Time   `json:"ended_at"`
	LatencyMS      int64       `json:"latency_ms"`
	FirstTokenMS   *int64      `json:"first_token_ms,omitempty"`
}

type Page struct {
	Data       []Record `json:"data"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type Totals struct {
	Requests            int64   `json:"requests"`
	SuccessfulRequests  int64   `json:"successful_requests"`
	FailedRequests      int64   `json:"failed_requests"`
	CacheHits           int64   `json:"cache_hits"`
	StreamedRequests    int64   `json:"streamed_requests"`
	InputTokens         int64   `json:"input_tokens"`
	UncachedInputTokens int64   `json:"uncached_input_tokens"`
	InputTextTokens     int64   `json:"input_text_tokens"`
	InputAudioTokens    int64   `json:"input_audio_tokens"`
	InputImageTokens    int64   `json:"input_image_tokens"`
	CachedReadTokens    int64   `json:"cached_read_tokens"`
	CachedWriteTokens   int64   `json:"cached_write_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	OutputTextTokens    int64   `json:"output_text_tokens"`
	OutputAudioTokens   int64   `json:"output_audio_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	AcceptedPrediction  int64   `json:"accepted_prediction_tokens"`
	RejectedPrediction  int64   `json:"rejected_prediction_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	BilledTokens        int64   `json:"billed_tokens"`
	CacheSavedTokens    int64   `json:"cache_saved_tokens"`
	KnownCostRequests   int64   `json:"known_cost_requests"`
	UnknownCostRequests int64   `json:"unknown_cost_requests"`
	TotalCostNanoUSD    int64   `json:"total_cost_nano_usd"`
	TotalCostUSD        string  `json:"total_cost_usd"`
	AverageLatencyMS    float64 `json:"average_latency_ms"`
	AverageFirstTokenMS float64 `json:"average_first_token_ms"`
	Attempts            int64   `json:"attempts"`
	Retries             int64   `json:"retries"`
	Fallbacks           int64   `json:"fallbacks"`
}

type Group struct {
	Value  string `json:"value"`
	Totals Totals `json:"totals"`
}

type Summary struct {
	Totals          Totals  `json:"totals"`
	Groups          []Group `json:"groups,omitempty"`
	GroupsTruncated bool    `json:"groups_truncated,omitempty"`
}

type SeriesPoint struct {
	Bucket string `json:"bucket"`
	Totals Totals `json:"totals"`
}

type listCursor struct {
	StartedAt int64  `json:"started_at"`
	EventID   string `json:"event_id"`
}

type aggregateRow struct {
	GroupValue          string
	Requests            int64
	SuccessfulRequests  int64
	FailedRequests      int64
	CacheHits           int64
	StreamedRequests    int64
	InputTokens         int64
	UncachedInputTokens int64
	InputTextTokens     int64
	InputAudioTokens    int64
	InputImageTokens    int64
	CachedReadTokens    int64
	CachedWriteTokens   int64
	OutputTokens        int64
	OutputTextTokens    int64
	OutputAudioTokens   int64
	ReasoningTokens     int64
	AcceptedPrediction  int64
	RejectedPrediction  int64
	TotalTokens         int64
	BilledTokens        int64
	CacheSavedTokens    int64
	KnownCostRequests   int64
	TotalCostNanoUSD    int64
	AverageLatencyMS    float64
	AverageFirstTokenMS float64
	Attempts            int64
	Retries             int64
	Fallbacks           int64
}

func NormalizeQueryFilter(filter QueryFilter, now time.Time) (QueryFilter, error) {
	now = now.UTC()
	if filter.End.IsZero() {
		filter.End = now
	} else {
		filter.End = filter.End.UTC()
	}
	if filter.Start.IsZero() {
		filter.Start = filter.End.Add(-defaultQueryWindow)
	} else {
		filter.Start = filter.Start.UTC()
	}
	filter.Model = strings.TrimSpace(filter.Model)
	filter.ProviderID = strings.TrimSpace(filter.ProviderID)
	filter.APIKeyID = strings.TrimSpace(filter.APIKeyID)
	filter.RequestID = strings.TrimSpace(filter.RequestID)
	filter.CacheStatus = strings.TrimSpace(filter.CacheStatus)
	switch {
	case !filter.End.After(filter.Start):
		return QueryFilter{}, fmt.Errorf("%w: end must be after start", ErrInvalidQuery)
	case filter.End.Sub(filter.Start) > maximumQueryWindow:
		return QueryFilter{}, fmt.Errorf("%w: time range exceeds 366 days", ErrInvalidQuery)
	case len(filter.Model) > 200 ||
		len(filter.ProviderID) > 100 ||
		len(filter.APIKeyID) > 64 ||
		len(filter.RequestID) > 128 ||
		len(filter.CacheStatus) > 16:
		return QueryFilter{}, fmt.Errorf("%w: filter is too long", ErrInvalidQuery)
	case filter.Status != "" && !validUsageStatus(filter.Status):
		return QueryFilter{}, fmt.Errorf("%w: status is unsupported", ErrInvalidQuery)
	}
	return filter, nil
}

func (store *Store) List(ctx context.Context, tenantID string, params ListParams) (Page, error) {
	filter, err := NormalizeQueryFilter(params.Filter, store.now())
	if err != nil {
		return Page{}, err
	}
	limit := params.Limit
	if limit == 0 {
		limit = defaultPageLimit
	}
	if limit < 1 || limit > maximumPageLimit {
		return Page{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidQuery, maximumPageLimit)
	}

	query, err := store.filteredQuery(ctx, tenantID, filter)
	if err != nil {
		return Page{}, err
	}
	if params.Cursor != "" {
		cursor, err := decodeListCursor(params.Cursor)
		if err != nil {
			return Page{}, err
		}
		startedAt := time.Unix(0, cursor.StartedAt).UTC()
		query = query.Where(
			"(started_at < ?) OR (started_at = ? AND event_id < ?)",
			startedAt,
			startedAt,
			cursor.EventID,
		)
	}

	var rows []postgres.UsageEvent
	if err := query.
		Order("started_at DESC").
		Order("event_id DESC").
		Limit(limit + 1).
		Find(&rows).Error; err != nil {
		return Page{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := Page{Data: make([]Record, 0, len(rows))}
	for _, row := range rows {
		page.Data = append(page.Data, recordFromRow(row, params.IncludeRaw))
	}
	if hasMore && len(rows) > 0 {
		page.NextCursor = encodeListCursor(rows[len(rows)-1])
	}
	return page, nil
}

func (store *Store) Summary(ctx context.Context, tenantID string, params SummaryParams) (Summary, error) {
	filter, err := NormalizeQueryFilter(params.Filter, store.now())
	if err != nil {
		return Summary{}, err
	}
	query, err := store.filteredQuery(ctx, tenantID, filter)
	if err != nil {
		return Summary{}, err
	}
	var total aggregateRow
	if err := query.Select(aggregateSelect("")).Scan(&total).Error; err != nil {
		return Summary{}, err
	}
	summary := Summary{Totals: totalsFromAggregate(total)}
	if strings.TrimSpace(params.GroupBy) == "" {
		return summary, nil
	}
	column, err := groupColumn(params.GroupBy)
	if err != nil {
		return Summary{}, err
	}
	var rows []aggregateRow
	if err := query.
		Select(aggregateSelect(column)).
		Group(column).
		Order("group_value ASC").
		Limit(maximumGroups + 1).
		Scan(&rows).Error; err != nil {
		return Summary{}, err
	}
	if len(rows) > maximumGroups {
		rows = rows[:maximumGroups]
		summary.GroupsTruncated = true
	}
	summary.Groups = make([]Group, 0, len(rows))
	for _, row := range rows {
		value := row.GroupValue
		if value == "" {
			value = "unknown"
		}
		summary.Groups = append(summary.Groups, Group{
			Value:  value,
			Totals: totalsFromAggregate(row),
		})
	}
	return summary, nil
}

func (store *Store) Series(ctx context.Context, tenantID string, params SeriesParams) ([]SeriesPoint, error) {
	filter, err := NormalizeQueryFilter(params.Filter, store.now())
	if err != nil {
		return nil, err
	}
	expression, err := seriesExpression(params.Interval)
	if err != nil {
		return nil, err
	}
	timezone := strings.TrimSpace(params.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return nil, fmt.Errorf("%w: timezone is invalid", ErrInvalidQuery)
	}
	query, err := store.filteredQuery(ctx, tenantID, filter)
	if err != nil {
		return nil, err
	}
	selectSQL := fmt.Sprintf(
		"%s AS group_value, %s",
		expression,
		aggregateColumns,
	)
	var rows []aggregateRow
	if err := query.
		Select(selectSQL, timezone).
		Group("group_value").
		Order("group_value ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	points := make([]SeriesPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, SeriesPoint{
			Bucket: row.GroupValue,
			Totals: totalsFromAggregate(row),
		})
	}
	return points, nil
}

func (store *Store) filteredQuery(
	ctx context.Context,
	tenantID string,
	filter QueryFilter,
) (*gorm.DB, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("%w: tenant is required", ErrInvalidQuery)
	}
	query := store.database.WithContext(ctx).
		Model(&postgres.UsageEvent{}).
		Where("tenant_id = ?", tenantID).
		Where("started_at >= ? AND started_at < ?", filter.Start, filter.End)
	if filter.Model != "" {
		query = query.Where("requested_model = ?", filter.Model)
	}
	if filter.ProviderID != "" {
		query = query.Where("provider_id = ?", filter.ProviderID)
	}
	if filter.APIKeyID != "" {
		query = query.Where("api_key_id = ?", filter.APIKeyID)
	}
	if filter.RequestID != "" {
		query = query.Where("request_id = ?", filter.RequestID)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", string(filter.Status))
	}
	if filter.CacheStatus != "" {
		query = query.Where("cache_status = ?", filter.CacheStatus)
	}
	if filter.Stream != nil {
		query = query.Where("stream = ?", *filter.Stream)
	}
	return query, nil
}

const aggregateColumns = `
COUNT(*) AS requests,
COUNT(*) FILTER (WHERE status IN ('success','cache_hit','stream_completed')) AS successful_requests,
COUNT(*) FILTER (WHERE status IN ('failed','cancelled','stream_interrupted')) AS failed_requests,
COUNT(*) FILTER (WHERE status = 'cache_hit') AS cache_hits,
COUNT(*) FILTER (WHERE stream) AS streamed_requests,
COALESCE(SUM(input_tokens), 0) AS input_tokens,
COALESCE(SUM(GREATEST(COALESCE(input_tokens, 0) - COALESCE(cached_read, 0) - COALESCE(cached_write, 0), 0)), 0) AS uncached_input_tokens,
COALESCE(SUM(input_text), 0) AS input_text_tokens,
COALESCE(SUM(input_audio), 0) AS input_audio_tokens,
COALESCE(SUM(input_image), 0) AS input_image_tokens,
COALESCE(SUM(cached_read), 0) AS cached_read_tokens,
COALESCE(SUM(cached_write), 0) AS cached_write_tokens,
COALESCE(SUM(output_tokens), 0) AS output_tokens,
COALESCE(SUM(output_text), 0) AS output_text_tokens,
COALESCE(SUM(output_audio), 0) AS output_audio_tokens,
COALESCE(SUM(reasoning), 0) AS reasoning_tokens,
COALESCE(SUM(accepted_prediction), 0) AS accepted_prediction,
COALESCE(SUM(rejected_prediction), 0) AS rejected_prediction,
COALESCE(SUM(total_tokens), 0) AS total_tokens,
COALESCE(SUM(total_tokens) FILTER (WHERE status <> 'cache_hit'), 0) AS billed_tokens,
COALESCE(SUM(total_tokens) FILTER (WHERE status = 'cache_hit'), 0) AS cache_saved_tokens,
COUNT(total_cost_nano_usd) AS known_cost_requests,
COALESCE(SUM(total_cost_nano_usd), 0) AS total_cost_nano_usd,
COALESCE(AVG(latency_ms), 0) AS average_latency_ms,
COALESCE(AVG(first_token_ms), 0) AS average_first_token_ms,
COALESCE(SUM(attempts), 0) AS attempts,
COALESCE(SUM(retries), 0) AS retries,
COALESCE(SUM(fallbacks), 0) AS fallbacks`

func aggregateSelect(group string) string {
	if group == "" {
		return aggregateColumns
	}
	return group + " AS group_value, " + aggregateColumns
}

func groupColumn(group string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(group)) {
	case "model":
		return "requested_model", nil
	case "provider":
		return "provider_id", nil
	case "status":
		return "status", nil
	case "cache":
		return "cache_status", nil
	case "api_key":
		return "api_key_id", nil
	default:
		return "", fmt.Errorf("%w: group_by is unsupported", ErrInvalidQuery)
	}
}

func seriesExpression(interval string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "", "day":
		return "to_char(date_trunc('day', started_at AT TIME ZONE ?), 'YYYY-MM-DD')", nil
	case "hour":
		return `to_char(date_trunc('hour', started_at AT TIME ZONE ?), 'YYYY-MM-DD"T"HH24:00')`, nil
	case "week":
		return `to_char(date_trunc('week', started_at AT TIME ZONE ?), 'IYYY-"W"IW')`, nil
	case "month":
		return "to_char(date_trunc('month', started_at AT TIME ZONE ?), 'YYYY-MM')", nil
	case "year":
		return "to_char(date_trunc('year', started_at AT TIME ZONE ?), 'YYYY')", nil
	default:
		return "", fmt.Errorf("%w: interval is unsupported", ErrInvalidQuery)
	}
}

func totalsFromAggregate(row aggregateRow) Totals {
	return Totals{
		Requests:            row.Requests,
		SuccessfulRequests:  row.SuccessfulRequests,
		FailedRequests:      row.FailedRequests,
		CacheHits:           row.CacheHits,
		StreamedRequests:    row.StreamedRequests,
		InputTokens:         row.InputTokens,
		UncachedInputTokens: row.UncachedInputTokens,
		InputTextTokens:     row.InputTextTokens,
		InputAudioTokens:    row.InputAudioTokens,
		InputImageTokens:    row.InputImageTokens,
		CachedReadTokens:    row.CachedReadTokens,
		CachedWriteTokens:   row.CachedWriteTokens,
		OutputTokens:        row.OutputTokens,
		OutputTextTokens:    row.OutputTextTokens,
		OutputAudioTokens:   row.OutputAudioTokens,
		ReasoningTokens:     row.ReasoningTokens,
		AcceptedPrediction:  row.AcceptedPrediction,
		RejectedPrediction:  row.RejectedPrediction,
		TotalTokens:         row.TotalTokens,
		BilledTokens:        row.BilledTokens,
		CacheSavedTokens:    row.CacheSavedTokens,
		KnownCostRequests:   row.KnownCostRequests,
		UnknownCostRequests: row.Requests - row.KnownCostRequests,
		TotalCostNanoUSD:    row.TotalCostNanoUSD,
		TotalCostUSD:        formatUSD(row.TotalCostNanoUSD),
		AverageLatencyMS:    row.AverageLatencyMS,
		AverageFirstTokenMS: row.AverageFirstTokenMS,
		Attempts:            row.Attempts,
		Retries:             row.Retries,
		Fallbacks:           row.Fallbacks,
	}
}

func recordFromRow(row postgres.UsageEvent, includeRaw bool) Record {
	record := Record{
		SchemaVersion:  int(row.SchemaVersion),
		EventID:        row.EventID,
		RequestID:      row.RequestID,
		APIKeyID:       row.APIKeyID,
		RequestedModel: row.RequestedModel,
		ProviderID:     row.ProviderID,
		UpstreamModel:  row.UpstreamModel,
		CacheStatus:    row.CacheStatus,
		Stream:         row.Stream,
		Attempts:       row.Attempts,
		Retries:        row.Retries,
		Fallbacks:      row.Fallbacks,
		UsageSource:    UsageSource(row.UsageSource),
		UsageCaveat:    row.UsageCaveat,
		CostCaveat:     row.CostCaveat,
		FinishReason:   row.FinishReason,
		Status:         Status(row.Status),
		ErrorCategory:  row.ErrorCategory,
		ErrorCode:      row.ErrorCode,
		StartedAt:      row.StartedAt.UTC(),
		EndedAt:        row.EndedAt.UTC(),
		LatencyMS:      row.LatencyMS,
		FirstTokenMS:   cloneInt64(row.FirstTokenMS),
	}
	if row.InputTokens != nil || row.OutputTokens != nil || row.TotalTokens != nil {
		record.Usage = &TokenView{
			Input:  int64Value(row.InputTokens),
			Output: int64Value(row.OutputTokens),
			Total:  int64Value(row.TotalTokens),
		}
		if anyInt64(
			row.InputText,
			row.InputAudio,
			row.InputImage,
			row.CachedRead,
			row.CachedWrite,
		) {
			record.Usage.InputDetails = &InputTokenDetails{
				Text:        int64Value(row.InputText),
				Audio:       int64Value(row.InputAudio),
				Image:       int64Value(row.InputImage),
				CachedRead:  int64Value(row.CachedRead),
				CachedWrite: int64Value(row.CachedWrite),
			}
		}
		if anyInt64(
			row.OutputText,
			row.OutputAudio,
			row.Reasoning,
			row.AcceptedPrediction,
			row.RejectedPrediction,
		) {
			record.Usage.OutputDetails = &OutputTokenDetails{
				Text:               int64Value(row.OutputText),
				Audio:              int64Value(row.OutputAudio),
				Reasoning:          int64Value(row.Reasoning),
				AcceptedPrediction: int64Value(row.AcceptedPrediction),
				RejectedPrediction: int64Value(row.RejectedPrediction),
			}
		}
		if includeRaw && json.Valid([]byte(row.RawUsage)) {
			record.Usage.Raw = json.RawMessage(row.RawUsage)
		}
	}
	if row.TotalCostNanoUSD != nil {
		record.Cost = &CostView{
			InputNanoUSD:   cloneInt64(row.InputCostNanoUSD),
			OutputNanoUSD:  cloneInt64(row.OutputCostNanoUSD),
			TotalNanoUSD:   *row.TotalCostNanoUSD,
			TotalUSD:       formatUSD(*row.TotalCostNanoUSD),
			Currency:       row.CostCurrency,
			Source:         row.CostSource,
			PricingVersion: row.PricingVersion,
			Caveat:         row.CostCaveat,
		}
	}
	return record
}

func anyInt64(values ...*int64) bool {
	for _, value := range values {
		if value != nil {
			return true
		}
	}
	return false
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func formatUSD(nanoUSD int64) string {
	whole := nanoUSD / 1_000_000_000
	fraction := nanoUSD % 1_000_000_000
	if fraction == 0 {
		return strconv.FormatInt(whole, 10)
	}
	formatted := fmt.Sprintf("%s.%09d", strconv.FormatInt(whole, 10), fraction)
	return strings.TrimRight(formatted, "0")
}

func encodeListCursor(row postgres.UsageEvent) string {
	payload, _ := json.Marshal(listCursor{
		StartedAt: row.StartedAt.UTC().UnixNano(),
		EventID:   row.EventID,
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeListCursor(encoded string) (listCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) > 256 {
		return listCursor{}, fmt.Errorf("%w: cursor is invalid", ErrInvalidQuery)
	}
	var cursor listCursor
	if json.Unmarshal(payload, &cursor) != nil ||
		cursor.StartedAt <= 0 ||
		!validRequiredText(cursor.EventID, 64) {
		return listCursor{}, fmt.Errorf("%w: cursor is invalid", ErrInvalidQuery)
	}
	return cursor, nil
}
