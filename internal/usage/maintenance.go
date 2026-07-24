package usage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"model-velo/internal/postgres"
)

type RepriceParams struct {
	TenantID    string
	Start       time.Time
	End         time.Time
	ProviderID  string
	Model       string
	MissingOnly bool
	Limit       int
	Cursor      string
}

type RepriceResult struct {
	Matched    int64
	Priced     int64
	Unknown    int64
	NextCursor string
}

func (store *Store) DeleteExpired(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() || limit < 1 || limit > 10_000 {
		return 0, errors.New("usage cleanup parameters are invalid")
	}
	var eventIDs []string
	if err := store.database.WithContext(ctx).
		Model(&postgres.UsageEvent{}).
		Where("ended_at < ?", before.UTC()).
		Order("ended_at ASC").
		Limit(limit).
		Pluck("event_id", &eventIDs).Error; err != nil {
		return 0, err
	}
	if len(eventIDs) == 0 {
		return 0, nil
	}
	result := store.database.WithContext(ctx).
		Where("event_id IN ?", eventIDs).
		Delete(&postgres.UsageEvent{})
	return result.RowsAffected, result.Error
}

func (store *Store) DeleteExpiredUntilIdle(
	ctx context.Context,
	before time.Time,
	batchSize int,
) (int64, error) {
	var total int64
	for {
		deleted, err := store.DeleteExpired(ctx, before, batchSize)
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted < int64(batchSize) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

func (store *Store) Reprice(ctx context.Context, params RepriceParams) (RepriceResult, error) {
	params.TenantID = strings.TrimSpace(params.TenantID)
	params.ProviderID = strings.TrimSpace(params.ProviderID)
	params.Model = strings.TrimSpace(params.Model)
	if params.Limit == 0 {
		params.Limit = 1_000
	}
	switch {
	case params.Limit < 1 || params.Limit > 10_000:
		return RepriceResult{}, errors.New("usage reprice limit must be between 1 and 10000")
	case params.Start.IsZero() || params.End.IsZero() || !params.End.After(params.Start):
		return RepriceResult{}, errors.New("usage reprice time range is invalid")
	case params.End.Sub(params.Start) > 10*365*24*time.Hour:
		return RepriceResult{}, errors.New("usage reprice time range exceeds ten years")
	case len(params.TenantID) > 128 || len(params.ProviderID) > 100 || len(params.Model) > 200:
		return RepriceResult{}, errors.New("usage reprice filter is too long")
	case len(params.Cursor) > 512:
		return RepriceResult{}, errors.New("usage reprice cursor is invalid")
	}

	query := store.database.WithContext(ctx).
		Where("started_at >= ? AND started_at < ?", params.Start.UTC(), params.End.UTC()).
		Order("started_at ASC").
		Order("event_id ASC").
		Limit(params.Limit + 1)
	if strings.TrimSpace(params.Cursor) != "" {
		cursor, err := decodeListCursor(strings.TrimSpace(params.Cursor))
		if err != nil {
			return RepriceResult{}, errors.New("usage reprice cursor is invalid")
		}
		startedAt := time.Unix(0, cursor.StartedAt).UTC()
		query = query.Where(
			"(started_at > ?) OR (started_at = ? AND event_id > ?)",
			startedAt,
			startedAt,
			cursor.EventID,
		)
	}
	if params.TenantID != "" {
		query = query.Where("tenant_id = ?", params.TenantID)
	}
	if params.ProviderID != "" {
		query = query.Where("provider_id = ?", params.ProviderID)
	}
	if params.Model != "" {
		query = query.Where("requested_model = ?", params.Model)
	}
	if params.MissingOnly {
		query = query.Where("total_cost_nano_usd IS NULL")
	}

	var rows []postgres.UsageEvent
	if err := query.Find(&rows).Error; err != nil {
		return RepriceResult{}, err
	}
	hasMore := len(rows) > params.Limit
	if hasMore {
		rows = rows[:params.Limit]
	}
	result := RepriceResult{Matched: int64(len(rows))}
	if hasMore && len(rows) > 0 {
		result.NextCursor = encodeListCursor(rows[len(rows)-1])
	}
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		for _, row := range rows {
			cost := store.pricing.Quote(eventFromRow(row))
			if cost.Snapshot == nil {
				result.Unknown++
				if err := transaction.Model(&postgres.UsageEvent{}).
					Where("event_id = ?", row.EventID).
					Update("cost_caveat", cost.Caveat).Error; err != nil {
					return err
				}
				continue
			}
			updates := costUpdates(cost.Snapshot)
			if err := transaction.Model(&postgres.UsageEvent{}).
				Where("event_id = ?", row.EventID).
				Updates(updates).Error; err != nil {
				return err
			}
			result.Priced++
		}
		return nil
	})
	if err != nil {
		return RepriceResult{}, fmt.Errorf("reprice usage records: %w", err)
	}
	return result, nil
}

func costUpdates(cost *CostSnapshot) map[string]any {
	return map[string]any{
		"input_cost_nano_usd":  cloneInt64(cost.InputNanoUSD),
		"output_cost_nano_usd": cloneInt64(cost.OutputNanoUSD),
		"total_cost_nano_usd":  cost.TotalNanoUSD,
		"cost_currency":        cost.Currency,
		"cost_source":          cost.Source,
		"pricing_version":      cost.PricingVersion,
		"cost_caveat":          cost.Caveat,
	}
}

func eventFromRow(row postgres.UsageEvent) Event {
	event := Event{
		SchemaVersion:  int(row.SchemaVersion),
		EventID:        row.EventID,
		RequestID:      row.RequestID,
		TenantID:       row.TenantID,
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
		FinishReason:   row.FinishReason,
		Status:         Status(row.Status),
		ErrorCategory:  row.ErrorCategory,
		ErrorCode:      row.ErrorCode,
		StartedAt:      row.StartedAt,
		EndedAt:        row.EndedAt,
		LatencyMS:      row.LatencyMS,
		FirstTokenMS:   cloneInt64(row.FirstTokenMS),
	}
	if row.TotalTokens == nil && row.InputTokens == nil && row.OutputTokens == nil {
		return event
	}
	event.Usage = &TokenUsage{
		Input:  int64Value(row.InputTokens),
		Output: int64Value(row.OutputTokens),
		Total:  int64Value(row.TotalTokens),
	}
	if anyInt64(row.InputText, row.InputAudio, row.InputImage, row.CachedRead, row.CachedWrite) {
		event.Usage.InputDetails = &InputTokenDetails{
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
		event.Usage.OutputDetails = &OutputTokenDetails{
			Text:               int64Value(row.OutputText),
			Audio:              int64Value(row.OutputAudio),
			Reasoning:          int64Value(row.Reasoning),
			AcceptedPrediction: int64Value(row.AcceptedPrediction),
			RejectedPrediction: int64Value(row.RejectedPrediction),
		}
	}
	if raw := strings.TrimSpace(row.RawUsage); raw != "" {
		if parsed, _ := parseTokenUsage([]byte(raw)); parsed != nil {
			event.Usage.Raw = parsed.Raw
			event.Usage.ReportedCost = parsed.ReportedCost
		}
	}
	return event
}
