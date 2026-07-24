package usage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"model-velo/internal/postgres"
)

const (
	defaultOutboxBatchSize       = 100
	defaultOutboxRepublishPeriod = 30 * time.Second
)

// PendingEvent is the durable minimum recorded before an authenticated request
// reaches a provider. It intentionally excludes prompts and provider secrets.
type PendingEvent struct {
	EventID        string
	RequestID      string
	TenantID       string
	APIKeyID       string
	RequestedModel string
	Stream         bool
	StartedAt      time.Time
}

// LifecycleEmitter records both the beginning and the final form of a request.
// Begin must succeed before the online request is allowed to call a provider.
type LifecycleEmitter interface {
	Emitter
	Begin(context.Context, PendingEvent) error
}

// DurableEmitter stores request lifecycle state in PostgreSQL before making a
// best-effort immediate delivery to Redis. The worker relays anything left in
// the outbox, so Redis downtime cannot silently discard a finalized event.
type DurableEmitter struct {
	database *gorm.DB
	redis    *RedisEmitter
	timeout  time.Duration
}

func NewDurableEmitter(
	database *gorm.DB,
	redis *RedisEmitter,
	timeout time.Duration,
) (*DurableEmitter, error) {
	switch {
	case database == nil:
		return nil, errors.New("durable usage emitter requires PostgreSQL")
	case redis == nil:
		return nil, errors.New("durable usage emitter requires Redis")
	case timeout <= 0:
		return nil, errors.New("durable usage emitter timeout must be positive")
	default:
		return &DurableEmitter{database: database, redis: redis, timeout: timeout}, nil
	}
}

func (emitter *DurableEmitter) Begin(ctx context.Context, pending PendingEvent) error {
	if err := validatePendingEvent(pending); err != nil {
		return err
	}
	writeContext, cancel := detachedTimeout(ctx, emitter.timeout)
	defer cancel()
	record := postgres.UsageOutbox{
		EventID:        pending.EventID,
		RequestID:      pending.RequestID,
		TenantID:       pending.TenantID,
		APIKeyID:       pending.APIKeyID,
		RequestedModel: pending.RequestedModel,
		Stream:         pending.Stream,
		State:          postgres.UsageOutboxPending,
		StartedAt:      pending.StartedAt.UTC(),
	}
	result := emitter.database.WithContext(writeContext).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "event_id"}}, DoNothing: true}).
		Create(&record)
	if result.Error != nil {
		return fmt.Errorf("record usage request lifecycle: %w", result.Error)
	}
	return nil
}

func (emitter *DurableEmitter) Emit(ctx context.Context, event Event) (string, error) {
	payload, err := event.Marshal()
	if err != nil {
		return "", err
	}
	writeContext, cancel := detachedTimeout(ctx, emitter.timeout)
	defer cancel()
	result := emitter.database.WithContext(writeContext).
		Model(&postgres.UsageOutbox{}).
		Where("event_id = ?", event.EventID).
		Updates(map[string]any{
			"payload":      string(payload),
			"state":        postgres.UsageOutboxReady,
			"published_at": nil,
		})
	if result.Error != nil {
		return "", fmt.Errorf("finalize usage outbox event: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return "", errors.New("usage outbox lifecycle record is missing")
	}

	entryID, publishErr := emitter.redis.Emit(ctx, event)
	if publishErr != nil {
		return "", publishErr
	}
	emitter.markPublished(event.EventID)
	return entryID, nil
}

func (emitter *DurableEmitter) markPublished(eventID string) {
	ctx, cancel := context.WithTimeout(context.Background(), emitter.timeout)
	defer cancel()
	now := time.Now().UTC()
	_ = emitter.database.WithContext(ctx).
		Model(&postgres.UsageOutbox{}).
		Where("event_id = ? AND state = ?", eventID, postgres.UsageOutboxReady).
		Updates(map[string]any{
			"state":        postgres.UsageOutboxPublished,
			"published_at": now,
		}).Error
}

// OutboxRelay republishes ready records and safely republishes published
// records that have not yet been removed by the idempotent usage store.
type OutboxRelay struct {
	database       *gorm.DB
	emitter        *RedisEmitter
	batchSize      int
	timeout        time.Duration
	pendingTimeout time.Duration
	republishAfter time.Duration
}

func NewOutboxRelay(
	database *gorm.DB,
	emitter *RedisEmitter,
	timeout time.Duration,
) (*OutboxRelay, error) {
	switch {
	case database == nil:
		return nil, errors.New("usage outbox relay requires PostgreSQL")
	case emitter == nil:
		return nil, errors.New("usage outbox relay requires Redis")
	case timeout <= 0:
		return nil, errors.New("usage outbox relay timeout must be positive")
	default:
		return &OutboxRelay{
			database:       database,
			emitter:        emitter,
			batchSize:      defaultOutboxBatchSize,
			timeout:        timeout,
			pendingTimeout: 15 * time.Minute,
			republishAfter: defaultOutboxRepublishPeriod,
		}, nil
	}
}

func (relay *OutboxRelay) SetPendingTimeout(timeout time.Duration) error {
	if timeout < 5*time.Minute {
		return errors.New("usage pending timeout must be at least five minutes")
	}
	relay.pendingTimeout = timeout
	return nil
}

func (relay *OutboxRelay) Publish(ctx context.Context) (int, error) {
	if _, err := relay.recoverPending(ctx); err != nil {
		return 0, err
	}
	republishBefore := time.Now().UTC().Add(-relay.republishAfter)
	var records []postgres.UsageOutbox
	if err := relay.database.WithContext(ctx).
		Where(
			"state = ? OR (state = ? AND (published_at IS NULL OR published_at <= ?))",
			postgres.UsageOutboxReady,
			postgres.UsageOutboxPublished,
			republishBefore,
		).
		Order("updated_at ASC").
		Limit(relay.batchSize).
		Find(&records).Error; err != nil {
		return 0, fmt.Errorf("read usage outbox: %w", err)
	}

	published := 0
	for _, record := range records {
		if record.Payload == nil {
			return published, fmt.Errorf("usage outbox event %s has no payload", record.EventID)
		}
		event, err := Decode([]byte(*record.Payload))
		if err != nil {
			return published, fmt.Errorf("decode usage outbox event %s: %w", record.EventID, err)
		}
		if _, err := relay.emitter.Emit(ctx, event); err != nil {
			return published, err
		}
		now := time.Now().UTC()
		if err := relay.database.WithContext(ctx).
			Model(&postgres.UsageOutbox{}).
			Where("event_id = ?", record.EventID).
			Updates(map[string]any{
				"state":        postgres.UsageOutboxPublished,
				"published_at": now,
			}).Error; err != nil {
			return published, fmt.Errorf("mark usage outbox published: %w", err)
		}
		published++
	}
	return published, nil
}

func (relay *OutboxRelay) recoverPending(ctx context.Context) (int, error) {
	cutoff := time.Now().UTC().Add(-relay.pendingTimeout)
	var records []postgres.UsageOutbox
	if err := relay.database.WithContext(ctx).
		Where("state = ? AND updated_at <= ?", postgres.UsageOutboxPending, cutoff).
		Order("updated_at ASC").
		Limit(relay.batchSize).
		Find(&records).Error; err != nil {
		return 0, fmt.Errorf("read stale usage lifecycles: %w", err)
	}
	recovered := 0
	for _, record := range records {
		status := StatusFailed
		if record.Stream {
			status = StatusStreamInterrupted
		}
		endedAt := time.Now().UTC()
		event := Event{
			SchemaVersion: SchemaVersion,
			EventID:       record.EventID, RequestID: record.RequestID,
			TenantID: record.TenantID, APIKeyID: record.APIKeyID,
			RequestedModel: record.RequestedModel,
			CacheStatus:    "bypass", Stream: record.Stream,
			UsageSource: UsageSourceUnknown,
			UsageCaveat: "process_ended_before_usage_finalization",
			Status:      status, ErrorCategory: "gateway",
			ErrorCode: "request_lifecycle_interrupted",
			StartedAt: record.StartedAt, EndedAt: endedAt,
			LatencyMS: endedAt.Sub(record.StartedAt).Milliseconds(),
		}
		payload, err := event.Marshal()
		if err != nil {
			return recovered, fmt.Errorf("encode recovered usage event: %w", err)
		}
		result := relay.database.WithContext(ctx).
			Model(&postgres.UsageOutbox{}).
			Where(
				"event_id = ? AND state = ?",
				record.EventID, postgres.UsageOutboxPending,
			).
			Updates(map[string]any{
				"payload": string(payload), "state": postgres.UsageOutboxReady,
			})
		if result.Error != nil {
			return recovered, fmt.Errorf("recover usage lifecycle: %w", result.Error)
		}
		recovered += int(result.RowsAffected)
	}
	return recovered, nil
}

func validatePendingEvent(event PendingEvent) error {
	switch {
	case !validRequiredText(event.EventID, 64):
		return errors.New("pending usage event ID is invalid")
	case !validRequiredText(event.RequestID, 128):
		return errors.New("pending usage request ID is invalid")
	case !validRequiredText(event.TenantID, 128):
		return errors.New("pending usage tenant ID is invalid")
	case !validRequiredText(event.APIKeyID, 64):
		return errors.New("pending usage API key ID is invalid")
	case !validRequiredText(event.RequestedModel, 200):
		return errors.New("pending usage model is invalid")
	case event.StartedAt.IsZero():
		return errors.New("pending usage start time is required")
	default:
		return nil
	}
}

func detachedTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}
