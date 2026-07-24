package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"model-velo/internal/config"
)

const maximumDeadLetterPayload = 64 << 10

type Worker struct {
	client *goredis.Client
	store  *Store
	relay  *OutboxRelay
	config config.Usage
	stats  workerCounters
}

type WorkerStats struct {
	Read         int64
	Claimed      int64
	Stored       int64
	Duplicates   int64
	Failed       int64
	DeadLettered int64
	Cleaned      int64
	Relayed      int64
}

type workerCounters struct {
	read         atomic.Int64
	claimed      atomic.Int64
	stored       atomic.Int64
	duplicates   atomic.Int64
	failed       atomic.Int64
	deadLettered atomic.Int64
	cleaned      atomic.Int64
	relayed      atomic.Int64
}

func (worker *Worker) SetOutboxRelay(relay *OutboxRelay) {
	worker.relay = relay
}

func NewWorker(client *goredis.Client, store *Store, settings config.Usage) (*Worker, error) {
	switch {
	case client == nil:
		return nil, errors.New("usage worker requires Redis")
	case store == nil:
		return nil, errors.New("usage worker requires a store")
	case settings.StreamKey == "" || settings.DeadLetterKey == "":
		return nil, errors.New("usage worker requires stream keys")
	case settings.Group == "" || settings.Consumer == "":
		return nil, errors.New("usage worker requires group and consumer names")
	case settings.DeadLetterMaxLen <= 0 || settings.BatchSize <= 0 || settings.MaxDeliveries <= 0:
		return nil, errors.New("usage worker counts must be positive")
	case settings.ReadBlock <= 0 || settings.ClaimIdle <= 0 ||
		settings.RetryBackoff <= 0 || settings.WorkerTimeout <= 0 ||
		settings.MaintenanceInterval <= 0 || settings.PricingRefresh <= 0 ||
		settings.PendingTimeout < 5*time.Minute:
		return nil, errors.New("usage worker durations must be positive")
	case settings.RetentionDays < 0 || settings.RetentionDays > 3_650 ||
		settings.MaintenanceBatch <= 0 || settings.MaintenanceBatch > 10_000:
		return nil, errors.New("usage worker maintenance configuration is invalid")
	}
	return &Worker{client: client, store: store, config: settings}, nil
}

func (worker *Worker) Run(ctx context.Context) error {
	if err := worker.ensureGroup(ctx); err != nil {
		return err
	}

	claimStart := "0-0"
	nextMaintenance := time.Now()
	nextPricingRefresh := time.Now()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !time.Now().Before(nextMaintenance) {
			worker.maintain(ctx)
			nextMaintenance = time.Now().Add(worker.config.MaintenanceInterval)
		}
		if !time.Now().Before(nextPricingRefresh) {
			worker.refreshPricing(ctx)
			nextPricingRefresh = time.Now().Add(worker.config.PricingRefresh)
		}
		if worker.relay != nil {
			relayContext, cancelRelay := context.WithTimeout(
				context.WithoutCancel(ctx),
				worker.config.WorkerTimeout,
			)
			relayed, relayErr := worker.relay.Publish(relayContext)
			cancelRelay()
			if relayErr != nil {
				worker.stats.failed.Add(1)
				slog.Error("usage worker outbox relay failed", "error", relayErr)
			} else {
				worker.stats.relayed.Add(int64(relayed))
			}
		}
		claimed, nextClaimStart, err := worker.claim(ctx, claimStart)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("usage worker pending claim failed", "error", err)
			if !waitForRetry(ctx, worker.config.RetryBackoff) {
				return nil
			}
			continue
		}
		claimStart = nextClaimStart
		if len(claimed) > 0 {
			worker.stats.claimed.Add(int64(len(claimed)))
			worker.processBatch(ctx, claimed)
			continue
		}
		if claimStart != "0-0" {
			continue
		}

		messages, err := worker.read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, goredis.Nil) {
				continue
			}
			slog.Error("usage worker stream read failed", "error", err)
			if !waitForRetry(ctx, worker.config.RetryBackoff) {
				return nil
			}
			continue
		}
		worker.stats.read.Add(int64(len(messages)))
		worker.processBatch(ctx, messages)
	}
}

func (worker *Worker) refreshPricing(ctx context.Context) {
	refreshContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		worker.config.WorkerTimeout,
	)
	defer cancel()
	if _, err := worker.store.ReloadManagedPricing(refreshContext); err != nil {
		worker.stats.failed.Add(1)
		slog.Error("usage worker pricing refresh failed", "error", err)
	}
}

func (worker *Worker) Stats() WorkerStats {
	return WorkerStats{
		Read:         worker.stats.read.Load(),
		Claimed:      worker.stats.claimed.Load(),
		Stored:       worker.stats.stored.Load(),
		Duplicates:   worker.stats.duplicates.Load(),
		Failed:       worker.stats.failed.Load(),
		DeadLettered: worker.stats.deadLettered.Load(),
		Cleaned:      worker.stats.cleaned.Load(),
		Relayed:      worker.stats.relayed.Load(),
	}
}

func (worker *Worker) maintain(ctx context.Context) {
	if worker.config.RetentionDays == 0 {
		return
	}
	maintenanceContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		worker.config.WorkerTimeout,
	)
	defer cancel()
	before := time.Now().UTC().AddDate(0, 0, -worker.config.RetentionDays)
	deleted, err := worker.store.DeleteExpiredUntilIdle(
		maintenanceContext,
		before,
		worker.config.MaintenanceBatch,
	)
	if err != nil {
		worker.stats.failed.Add(1)
		slog.Error("usage worker retention cleanup failed", "error", err)
		return
	}
	worker.stats.cleaned.Add(deleted)
}

func (worker *Worker) Pending(ctx context.Context) (int64, error) {
	pending, err := worker.client.XPending(
		ctx,
		worker.config.StreamKey,
		worker.config.Group,
	).Result()
	if err != nil {
		return 0, err
	}
	return pending.Count, nil
}

func (worker *Worker) ensureGroup(ctx context.Context) error {
	err := worker.client.XGroupCreateMkStream(
		ctx,
		worker.config.StreamKey,
		worker.config.Group,
		"0",
	).Err()
	if err == nil || strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return fmt.Errorf("create usage consumer group: %w", err)
}

func (worker *Worker) claim(
	ctx context.Context,
	start string,
) ([]goredis.XMessage, string, error) {
	messages, next, err := worker.client.XAutoClaim(ctx, &goredis.XAutoClaimArgs{
		Stream:   worker.config.StreamKey,
		Group:    worker.config.Group,
		Consumer: worker.config.Consumer,
		MinIdle:  worker.config.ClaimIdle,
		Start:    start,
		Count:    worker.config.BatchSize,
	}).Result()
	return messages, next, err
}

func (worker *Worker) read(ctx context.Context) ([]goredis.XMessage, error) {
	streams, err := worker.client.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    worker.config.Group,
		Consumer: worker.config.Consumer,
		Streams:  []string{worker.config.StreamKey, ">"},
		Count:    worker.config.BatchSize,
		Block:    worker.config.ReadBlock,
	}).Result()
	if err != nil {
		return nil, err
	}
	if len(streams) == 0 {
		return nil, nil
	}
	return streams[0].Messages, nil
}

func (worker *Worker) processBatch(ctx context.Context, messages []goredis.XMessage) {
	batchContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		worker.config.WorkerTimeout,
	)
	defer cancel()

	for _, message := range messages {
		if err := worker.processMessage(batchContext, message); err != nil {
			worker.stats.failed.Add(1)
			slog.Error(
				"usage worker entry failed",
				"entry_id", message.ID,
				"error", err,
			)
		}
		if batchContext.Err() != nil {
			return
		}
	}
}

func (worker *Worker) processMessage(ctx context.Context, message goredis.XMessage) error {
	payload, ok := streamString(message.Values["payload"])
	if !ok {
		return worker.handlePoison(ctx, message, "missing_payload")
	}
	event, err := Decode([]byte(payload))
	if err != nil {
		return worker.handlePoison(ctx, message, "invalid_event")
	}

	duplicate, err := worker.store.Put(ctx, message.ID, event)
	if err != nil {
		return err
	}
	if duplicate {
		worker.stats.duplicates.Add(1)
	} else {
		worker.stats.stored.Add(1)
	}
	_, err = worker.client.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
		pipe.XAck(ctx, worker.config.StreamKey, worker.config.Group, message.ID)
		pipe.XDel(ctx, worker.config.StreamKey, message.ID)
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (worker *Worker) handlePoison(
	ctx context.Context,
	message goredis.XMessage,
	reason string,
) error {
	deliveries, err := worker.deliveryCount(ctx, message.ID)
	if err != nil {
		return err
	}
	if deliveries < worker.config.MaxDeliveries {
		return errors.New("poison usage event is awaiting redelivery")
	}

	payload, _ := streamString(message.Values["payload"])
	if len(payload) > maximumDeadLetterPayload {
		payload = payload[:maximumDeadLetterPayload]
	}
	_, err = worker.client.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
		pipe.XAdd(ctx, &goredis.XAddArgs{
			Stream: worker.config.DeadLetterKey,
			MaxLen: worker.config.DeadLetterMaxLen,
			Approx: true,
			Values: map[string]any{
				"source_entry_id": message.ID,
				"reason":          reason,
				"payload":         payload,
			},
		})
		pipe.XAck(ctx, worker.config.StreamKey, worker.config.Group, message.ID)
		pipe.XDel(ctx, worker.config.StreamKey, message.ID)
		return nil
	})
	if err == nil {
		worker.stats.deadLettered.Add(1)
	}
	return err
}

func (worker *Worker) deliveryCount(ctx context.Context, entryID string) (int64, error) {
	pending, err := worker.client.XPendingExt(ctx, &goredis.XPendingExtArgs{
		Stream: worker.config.StreamKey,
		Group:  worker.config.Group,
		Start:  entryID,
		End:    entryID,
		Count:  1,
	}).Result()
	if err != nil {
		return 0, err
	}
	if len(pending) != 1 || pending[0].ID != entryID {
		return 0, errors.New("usage pending entry is unavailable")
	}
	return pending[0].RetryCount, nil
}

func streamString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
