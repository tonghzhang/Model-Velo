package usage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"model-velo/internal/postgres"
)

type Store struct {
	database *gorm.DB
	pricing  *PricingCatalog
	now      func() time.Time
}

func NewStore(database *gorm.DB, pricing *PricingCatalog) (*Store, error) {
	if database == nil {
		return nil, errors.New("usage store requires PostgreSQL")
	}
	if pricing == nil {
		return nil, errors.New("usage store requires a pricing catalog")
	}
	return &Store{database: database, pricing: pricing, now: time.Now}, nil
}

func (store *Store) Put(ctx context.Context, entryID string, event Event) (bool, error) {
	if err := event.Validate(); err != nil {
		return false, err
	}
	record := store.usageRecord(entryID, event, store.now().UTC())
	result := store.database.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "event_id"}},
			DoNothing: true,
		}).
		Create(&record)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 0, nil
}

func (store *Store) usageRecord(entryID string, event Event, processedAt time.Time) postgres.UsageEvent {
	record := postgres.UsageEvent{
		EventID:        event.EventID,
		RedisEntryID:   entryID,
		SchemaVersion:  int16(event.SchemaVersion),
		RequestID:      event.RequestID,
		TenantID:       event.TenantID,
		APIKeyID:       event.APIKeyID,
		RequestedModel: event.RequestedModel,
		ProviderID:     event.ProviderID,
		UpstreamModel:  event.UpstreamModel,
		CacheStatus:    event.CacheStatus,
		Stream:         event.Stream,
		Attempts:       event.Attempts,
		Retries:        event.Retries,
		Fallbacks:      event.Fallbacks,
		UsageSource:    string(event.UsageSource),
		UsageCaveat:    event.UsageCaveat,
		FinishReason:   event.FinishReason,
		Status:         string(event.Status),
		ErrorCategory:  event.ErrorCategory,
		ErrorCode:      event.ErrorCode,
		StartedAt:      event.StartedAt,
		EndedAt:        event.EndedAt,
		LatencyMS:      event.LatencyMS,
		FirstTokenMS:   cloneInt64(event.FirstTokenMS),
		ProcessedAt:    processedAt,
	}
	if event.Usage != nil {
		input := event.Usage.Input
		output := event.Usage.Output
		total := event.Usage.Total
		record.InputTokens = &input
		record.OutputTokens = &output
		record.TotalTokens = &total
		record.RawUsage = string(event.Usage.Raw)
		if details := event.Usage.InputDetails; details != nil {
			record.InputText = int64Pointer(details.Text)
			record.InputAudio = int64Pointer(details.Audio)
			record.InputImage = int64Pointer(details.Image)
			record.CachedRead = int64Pointer(details.CachedRead)
			record.CachedWrite = int64Pointer(details.CachedWrite)
		}
		if details := event.Usage.OutputDetails; details != nil {
			record.OutputText = int64Pointer(details.Text)
			record.OutputAudio = int64Pointer(details.Audio)
			record.Reasoning = int64Pointer(details.Reasoning)
			record.AcceptedPrediction = int64Pointer(details.AcceptedPrediction)
			record.RejectedPrediction = int64Pointer(details.RejectedPrediction)
		}
	}
	cost := store.pricing.Quote(event)
	record.CostCaveat = cost.Caveat
	if cost.Snapshot != nil {
		record.InputCostNanoUSD = cloneInt64(cost.Snapshot.InputNanoUSD)
		record.OutputCostNanoUSD = cloneInt64(cost.Snapshot.OutputNanoUSD)
		record.TotalCostNanoUSD = int64Pointer(cost.Snapshot.TotalNanoUSD)
		record.CostCurrency = cost.Snapshot.Currency
		record.CostSource = cost.Snapshot.Source
		record.PricingVersion = cost.Snapshot.PricingVersion
		record.CostCaveat = cost.Snapshot.Caveat
	}
	return record
}

func int64Pointer(value int64) *int64 {
	return &value
}
