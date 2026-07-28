package usage

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"model-velo/internal/config"
	"model-velo/internal/postgres"
)

type Store struct {
	database *gorm.DB
	pricing  atomic.Pointer[PricingCatalog]
	now      func() time.Time
}

type storeEntry struct {
	entryID string
	event   Event
}

func NewStore(database *gorm.DB, pricing *PricingCatalog) (*Store, error) {
	if database == nil {
		return nil, errors.New("usage store requires PostgreSQL")
	}
	if pricing == nil {
		return nil, errors.New("usage store requires a pricing catalog")
	}
	store := &Store{database: database, now: time.Now}
	store.pricing.Store(pricing)
	return store, nil
}

// ReplacePricing atomically publishes an already validated immutable catalog.
// Requests already being persisted finish with either the old or the new
// complete catalog; they never observe a partially updated price list.
func (store *Store) ReplacePricing(pricing *PricingCatalog) error {
	if store == nil || pricing == nil {
		return errors.New("usage pricing catalog is required")
	}
	store.pricing.Store(pricing)
	return nil
}

func (store *Store) Quote(event Event) CostResult {
	if store == nil {
		return CostResult{Caveat: "pricing_store_unavailable"}
	}
	pricing := store.pricing.Load()
	if pricing == nil {
		return CostResult{Caveat: "pricing_not_configured"}
	}
	return pricing.Quote(event)
}

// ReloadManagedPricing refreshes pricing written by the control plane. A
// missing managed document intentionally keeps the environment catalog.
func (store *Store) ReloadManagedPricing(ctx context.Context) (bool, error) {
	var row postgres.ManagedPricing
	err := store.database.WithContext(ctx).First(&row, "id = ?", 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("read managed pricing")
	}
	var prices []config.UsagePrice
	if err := json.Unmarshal([]byte(row.Document), &prices); err != nil {
		return false, errors.New("decode managed pricing")
	}
	catalog, err := NewPricingCatalog(prices)
	if err != nil {
		return false, err
	}
	store.pricing.Store(catalog)
	return true, nil
}

func (store *Store) Put(ctx context.Context, entryID string, event Event) (bool, error) {
	stored, duplicates, err := store.putBatch(
		ctx,
		[]storeEntry{{entryID: entryID, event: event}},
	)
	if err != nil {
		return false, err
	}
	return stored == 0 && duplicates == 1, nil
}

func (store *Store) putBatch(
	ctx context.Context,
	entries []storeEntry,
) (int64, int64, error) {
	if len(entries) == 0 {
		return 0, 0, nil
	}
	processedAt := store.now().UTC()
	records := make([]postgres.UsageEvent, 0, len(entries))
	eventIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := entry.event.Validate(); err != nil {
			return 0, 0, err
		}
		records = append(records, store.usageRecord(
			entry.entryID,
			entry.event,
			processedAt,
		))
		eventIDs = append(eventIDs, entry.event.EventID)
	}

	var stored int64
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "event_id"}},
				DoNothing: true,
			}).
			Create(&records)
		if result.Error != nil {
			return result.Error
		}
		stored = result.RowsAffected
		if err := transaction.
			Where("event_id IN ?", eventIDs).
			Delete(&postgres.UsageOutbox{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return stored, int64(len(entries)) - stored, nil
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
	pricing := store.pricing.Load()
	cost := pricing.Quote(event)
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
