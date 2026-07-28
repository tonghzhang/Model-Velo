package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"model-velo/internal/config"
	"model-velo/internal/postgres"
)

func TestCollectorFinalizesOnce(t *testing.T) {
	startedAt := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	collector, err := NewCollector(NewEventInput{
		RequestID:      "request-1",
		TenantID:       "00000000-0000-4000-8000-000000000001",
		APIKeyID:       "api-key-1",
		RequestedModel: "demo-model",
		Stream:         true,
		StartedAt:      startedAt,
	})
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	collector.SetCacheStatus("BYPASS")
	collector.SetRoute("primary", "upstream-model", 3, 1, 1)
	collector.ObserveResponse([]byte(
		`{"usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}`,
	))

	event, ok := collector.Finalize(Outcome{
		Status:  StatusSuccess,
		EndedAt: startedAt.Add(1500 * time.Millisecond),
	})
	if !ok {
		t.Fatal("first Finalize() = false")
	}
	if _, ok := collector.Finalize(Outcome{Status: StatusFailed}); ok {
		t.Fatal("second Finalize() = true")
	}
	if event.LatencyMS != 1500 ||
		event.CacheStatus != "bypass" ||
		event.Attempts != 3 ||
		event.Retries != 1 ||
		event.Fallbacks != 1 ||
		event.Usage == nil ||
		event.Usage.Total != 15 {
		t.Fatalf("event = %#v", event)
	}

	payload, err := event.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	decoded, err := Decode(payload)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded.EventID != event.EventID || decoded.Status != StatusSuccess {
		t.Fatalf("decoded event = %#v", decoded)
	}
	decoded.SchemaVersion++
	if _, err := decoded.Marshal(); err == nil {
		t.Fatal("Marshal() accepted an unknown schema version")
	}

	concurrent, err := NewCollector(NewEventInput{
		RequestID:      "request-concurrent",
		TenantID:       "00000000-0000-4000-8000-000000000001",
		APIKeyID:       "api-key-1",
		RequestedModel: "demo-model",
		StartedAt:      startedAt,
	})
	if err != nil {
		t.Fatalf("NewCollector(concurrent) error = %v", err)
	}
	var winners atomic.Int32
	var wait sync.WaitGroup
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, finalized := concurrent.Finalize(Outcome{
				Status:  StatusSuccess,
				EndedAt: startedAt.Add(time.Second),
			}); finalized {
				winners.Add(1)
			}
		}()
	}
	wait.Wait()
	if winners.Load() != 1 {
		t.Fatalf("concurrent finalize winners = %d, want 1", winners.Load())
	}
}

func TestStreamingDetailsPricingAndSchemaCompatibility(t *testing.T) {
	startedAt := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	collector, err := NewCollector(NewEventInput{
		RequestID:      "request-detailed",
		TenantID:       "00000000-0000-4000-8000-000000000001",
		APIKeyID:       "api-key-detailed",
		RequestedModel: "gateway-model",
		Stream:         true,
		StartedAt:      startedAt,
	})
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	collector.SetRoute("primary", "upstream-model", 2, 1, 0)
	collector.ObserveStreamResponse(
		[]byte(`{"choices":[{"delta":{"content":"hello"}}]}`),
		startedAt.Add(125*time.Millisecond),
	)
	collector.ObserveStreamResponse(
		[]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100,"prompt_tokens_details":{"cached_tokens":200},"completion_tokens_details":{"reasoning_tokens":40}}}`),
		startedAt.Add(time.Second),
	)
	event, ok := collector.Finalize(Outcome{
		Status:  StatusStreamCompleted,
		EndedAt: startedAt.Add(2 * time.Second),
	})
	if !ok {
		t.Fatal("Finalize() = false")
	}
	if event.FirstTokenMS == nil ||
		*event.FirstTokenMS != 125 ||
		event.FinishReason != "stop" ||
		event.UsageSource != UsageSourceProvider ||
		event.Usage == nil ||
		event.Usage.InputDetails == nil ||
		event.Usage.InputDetails.CachedRead != 200 ||
		event.Usage.OutputDetails == nil ||
		event.Usage.OutputDetails.Reasoning != 40 ||
		!json.Valid(event.Usage.Raw) {
		t.Fatalf("detailed event = %#v", event)
	}

	catalog, err := NewPricingCatalog([]config.UsagePrice{{
		ProviderID:                   "primary",
		Model:                        "upstream-model",
		Version:                      "price-2026-07",
		InputUSDPerMillion:           "2.5",
		OutputUSDPerMillion:          "10",
		CachedReadUSDPerMillion:      "0.5",
		ReasoningOutputUSDPerMillion: "15",
	}})
	if err != nil {
		t.Fatalf("NewPricingCatalog() error = %v", err)
	}
	cost := catalog.Quote(event)
	if cost.Snapshot == nil ||
		cost.Snapshot.TotalNanoUSD != 3_300_000 ||
		cost.Snapshot.Source != CostSourceCatalog ||
		cost.Snapshot.PricingVersion != "price-2026-07" ||
		!strings.Contains(cost.Snapshot.Caveat, "cost_excludes_unreported_failed_attempts") {
		t.Fatalf("quoted cost = %#v", cost)
	}

	unknownCatalog, err := NewPricingCatalog(nil)
	if err != nil {
		t.Fatalf("NewPricingCatalog(empty) error = %v", err)
	}
	unknownCost := unknownCatalog.Quote(event)
	if unknownCost.Snapshot != nil || !strings.Contains(unknownCost.Caveat, "pricing_not_found") {
		t.Fatalf("unknown cost = %#v, want nil snapshot", unknownCost)
	}

	cacheEvent := event
	cacheEvent.Status = StatusCacheHit
	cacheEvent.Stream = false
	cacheEvent.CacheStatus = "hit"
	cacheEvent.FirstTokenMS = nil
	cacheEvent.UsageSource = UsageSourceCacheReplay
	cacheCost := catalog.Quote(cacheEvent)
	if cacheCost.Snapshot == nil ||
		cacheCost.Snapshot.TotalNanoUSD != 0 ||
		cacheCost.Snapshot.Source != CostSourceCache {
		t.Fatalf("cache cost = %#v", cacheCost)
	}

	legacy := event
	legacy.SchemaVersion = 1
	legacy.APIKeyID = ""
	legacy.UsageSource = ""
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("json.Marshal(legacy) error = %v", err)
	}
	decoded, err := Decode(payload)
	if err != nil {
		t.Fatalf("Decode(schema v1) error = %v", err)
	}
	if decoded.UsageSource != UsageSourceProvider {
		t.Fatalf("schema v1 usage source = %q", decoded.UsageSource)
	}
}

func TestPricingRejectsOverlapsAndPreservesReportedCost(t *testing.T) {
	_, err := NewPricingCatalog([]config.UsagePrice{
		{
			ProviderID:          "primary",
			Model:               "model",
			Version:             "first",
			EffectiveFrom:       "2026-01-01T00:00:00Z",
			EffectiveUntil:      "2026-08-01T00:00:00Z",
			InputUSDPerMillion:  "1",
			OutputUSDPerMillion: "2",
		},
		{
			ProviderID:          "primary",
			Model:               "model",
			Version:             "second",
			EffectiveFrom:       "2026-07-01T00:00:00Z",
			EffectiveUntil:      "2027-01-01T00:00:00Z",
			InputUSDPerMillion:  "1",
			OutputUSDPerMillion: "2",
		},
	})
	if err == nil {
		t.Fatal("NewPricingCatalog() accepted overlapping windows")
	}

	parsed, caveat := parseTokenUsage([]byte(
		`{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"cost":{"input_cost":"0.000001","output_cost":0.000002,"currency":"usd"}}`,
	))
	if caveat != "" ||
		parsed == nil ||
		parsed.ReportedCost == nil ||
		parsed.ReportedCost.TotalNanoUSD != 3_000 {
		t.Fatalf("reported cost usage=%#v caveat=%q", parsed, caveat)
	}
}

func TestRedisEmitterRejectsBadEventsAndHonorsTimeout(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{
		Addr: "timeout.test:6379",
		Dialer: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		ContextTimeoutEnabled: true,
	})
	defer client.Close()
	emitter, err := NewRedisEmitter(client, "usage:test", 25*time.Millisecond)
	if err != nil {
		t.Fatalf("NewRedisEmitter() error = %v", err)
	}
	if _, err := emitter.Emit(context.Background(), Event{}); err == nil {
		t.Fatal("Emit() accepted an invalid event")
	}

	event := integrationEvent(t, "request-timeout")
	startedAt := time.Now()
	if _, err := emitter.Emit(context.Background(), event); err == nil {
		t.Fatal("Emit() timeout error = nil")
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("Emit() elapsed = %s, want bounded timeout", elapsed)
	}
}

func TestUsageRedisPostgresPipeline(t *testing.T) {
	database := openUsageIntegrationDatabase(t)
	client := openUsageIntegrationRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := database.SyncSchema(ctx); err != nil {
		t.Fatalf("SyncSchema() error = %v", err)
	}
	if err := database.SyncSchema(ctx); err != nil {
		t.Fatalf("second SyncSchema() error = %v", err)
	}
	if !database.ORM().Migrator().HasTable(&postgres.UsageEvent{}) {
		t.Fatal("AutoMigrate did not create usage_events")
	}
	for _, index := range []string{
		"usage_events_tenant_started_idx",
		"usage_events_provider_started_idx",
		"usage_events_status_ended_idx",
		"usage_events_tenant_model_started_idx",
		"usage_events_tenant_provider_started_idx",
		"usage_events_api_key_started_idx",
		"usage_events_cost_idx",
	} {
		if !database.ORM().Migrator().HasIndex(&postgres.UsageEvent{}, index) {
			t.Errorf("AutoMigrate did not create %s", index)
		}
	}

	suffix := randomUsageSuffix(t)
	settings := config.Usage{
		StreamKey:           "model-velo:usage:test:" + suffix,
		DeadLetterKey:       "model-velo:usage:test:" + suffix + ":dead-letter",
		DeadLetterMaxLen:    100,
		EmitTimeout:         time.Second,
		Group:               "usage-test-group",
		Consumer:            "usage-test-consumer",
		BatchSize:           10,
		ReadBlock:           20 * time.Millisecond,
		ClaimIdle:           20 * time.Millisecond,
		MaxDeliveries:       1,
		RetryBackoff:        10 * time.Millisecond,
		WorkerTimeout:       2 * time.Second,
		RetentionDays:       90,
		MaintenanceInterval: time.Hour,
		MaintenanceBatch:    100,
		PricingRefresh:      time.Minute,
		PendingTimeout:      15 * time.Minute,
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		_ = client.Del(cleanupContext, settings.StreamKey, settings.DeadLetterKey).Err()
	})

	emitter, err := NewRedisEmitter(
		client,
		settings.StreamKey,
		settings.EmitTimeout,
	)
	if err != nil {
		t.Fatalf("NewRedisEmitter() error = %v", err)
	}
	pricing, err := NewPricingCatalog([]config.UsagePrice{{
		ProviderID:          "upstream",
		Model:               "demo-model",
		Version:             "integration-v1",
		InputUSDPerMillion:  "1",
		OutputUSDPerMillion: "2",
	}})
	if err != nil {
		t.Fatalf("NewPricingCatalog() error = %v", err)
	}
	store, err := NewStore(database.ORM(), pricing)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	worker, err := NewWorker(client, store, settings)
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	if err := worker.ensureGroup(ctx); err != nil {
		t.Fatalf("ensureGroup() error = %v", err)
	}

	first := integrationEvent(t, "request-first")
	if _, err := emitter.Emit(ctx, first); err != nil {
		t.Fatalf("Emit(first) error = %v", err)
	}
	rawEntries, err := client.XRange(ctx, settings.StreamKey, "-", "+").Result()
	if err != nil || len(rawEntries) != 1 {
		t.Fatalf("XRange(first) entries = %d, error = %v", len(rawEntries), err)
	}
	rawPayload, ok := streamString(rawEntries[0].Values["payload"])
	if !ok {
		t.Fatal("first Redis entry has no string payload")
	}
	decoded, err := Decode([]byte(rawPayload))
	if err != nil || decoded.EventID != first.EventID {
		t.Fatalf("Decode(Redis payload) event = %#v, error = %v", decoded, err)
	}
	if _, err := emitter.Emit(ctx, first); err != nil {
		t.Fatalf("Emit(duplicate) error = %v", err)
	}

	firstRunContext, stopFirstRun := context.WithCancel(ctx)
	firstRunDone := make(chan error, 1)
	go func() {
		firstRunDone <- worker.Run(firstRunContext)
	}()
	waitForUsageCondition(t, func() bool {
		var count int64
		if database.ORM().Model(&postgres.UsageEvent{}).
			Where("event_id = ?", first.EventID).
			Count(&count).Error != nil {
			return false
		}
		return count == 1 && worker.Stats().Duplicates == 1
	})
	waitForUsageCondition(t, func() bool {
		length, err := client.XLen(ctx, settings.StreamKey).Result()
		return err == nil && length == 0
	})
	otherKeyEvent := integrationEvent(t, "request-other-key")
	otherKeyEvent.APIKeyID = "api-key-other"
	if duplicate, err := store.Put(ctx, "direct-other-key", otherKeyEvent); err != nil || duplicate {
		t.Fatalf("Put(other key event) duplicate=%t error=%v", duplicate, err)
	}
	otherTenantEvent := integrationEvent(t, "request-other-tenant")
	otherTenantEvent.TenantID = "00000000-0000-4000-8000-000000000099"
	otherTenantEvent.APIKeyID = "api-key-other-tenant"
	if duplicate, err := store.Put(
		ctx,
		"direct-other-tenant",
		otherTenantEvent,
	); err != nil || duplicate {
		t.Fatalf("Put(other tenant event) duplicate=%t error=%v", duplicate, err)
	}
	window := QueryFilter{
		Start:    time.Now().UTC().Add(-time.Hour),
		End:      time.Now().UTC().Add(time.Hour),
		APIKeyID: first.APIKeyID,
	}
	page, err := store.List(ctx, first.TenantID, ListParams{Filter: window})
	if err != nil || len(page.Data) != 1 ||
		page.Data[0].Cost == nil ||
		page.Data[0].Cost.TotalNanoUSD != 4_000 {
		t.Fatalf("List() page = %#v, error = %v", page, err)
	}
	otherTenant, err := store.List(
		ctx,
		"00000000-0000-4000-8000-000000000099",
		ListParams{Filter: window},
	)
	if err != nil || len(otherTenant.Data) != 0 {
		t.Fatalf("List(other tenant) page = %#v, error = %v", otherTenant, err)
	}
	platformWindow := window
	platformWindow.APIKeyID = ""
	platformPage, err := store.PlatformList(
		ctx,
		"",
		ListParams{Filter: platformWindow},
	)
	if err != nil ||
		len(platformPage.Data) != 3 ||
		platformPage.Data[0].TenantID == "" {
		t.Fatalf("PlatformList() page = %#v, error = %v", platformPage, err)
	}
	tenantSummary, err := store.PlatformSummary(
		ctx,
		first.TenantID,
		SummaryParams{Filter: platformWindow},
	)
	if err != nil || tenantSummary.Totals.Requests != 2 {
		t.Fatalf(
			"PlatformSummary(tenant) = %#v, error = %v",
			tenantSummary,
			err,
		)
	}
	platformSummary, err := store.PlatformSummary(
		ctx,
		"",
		SummaryParams{Filter: platformWindow, GroupBy: "tenant"},
	)
	if err != nil ||
		platformSummary.Totals.Requests != 3 ||
		len(platformSummary.Groups) != 2 {
		t.Fatalf(
			"PlatformSummary() = %#v, error = %v",
			platformSummary,
			err,
		)
	}
	platformSeries, err := store.PlatformSeries(ctx, "", SeriesParams{
		Filter:   platformWindow,
		Interval: "hour",
		Timezone: "UTC",
	})
	if err != nil ||
		len(platformSeries) != 1 ||
		platformSeries[0].Totals.Requests != 3 {
		t.Fatalf("PlatformSeries() = %#v, error = %v", platformSeries, err)
	}
	summary, err := store.Summary(ctx, first.TenantID, SummaryParams{Filter: window})
	if err != nil ||
		summary.Totals.Requests != 1 ||
		summary.Totals.KnownCostRequests != 1 ||
		summary.Totals.TotalCostNanoUSD != 4_000 ||
		summary.Totals.UncachedInputTokens != 1 ||
		summary.Totals.CachedReadTokens != 1 ||
		summary.Totals.ReasoningTokens != 1 {
		t.Fatalf("Summary() = %#v, error = %v", summary, err)
	}
	grouped, err := store.Summary(
		ctx,
		first.TenantID,
		SummaryParams{Filter: window, GroupBy: "model"},
	)
	if err != nil ||
		len(grouped.Groups) != 1 ||
		grouped.Groups[0].Value != "demo-model" ||
		grouped.Groups[0].Totals.Requests != 1 {
		t.Fatalf("Summary(grouped) = %#v, error = %v", grouped, err)
	}
	series, err := store.Series(ctx, first.TenantID, SeriesParams{
		Filter:   window,
		Interval: "hour",
		Timezone: "UTC",
	})
	if err != nil ||
		len(series) != 1 ||
		series[0].Totals.Requests != 1 ||
		series[0].Totals.TotalCostNanoUSD != 4_000 {
		t.Fatalf("Series() = %#v, error = %v", series, err)
	}
	stopFirstRun()
	if err := <-firstRunDone; err != nil {
		t.Fatalf("first Worker.Run() error = %v", err)
	}

	durable, err := NewDurableEmitter(database.ORM(), time.Second)
	if err != nil {
		t.Fatalf("NewDurableEmitter() error = %v", err)
	}
	relayEvent := integrationEvent(t, "request-outbox-republish")
	if err := durable.Begin(ctx, PendingEvent{
		EventID: relayEvent.EventID, RequestID: relayEvent.RequestID,
		TenantID: relayEvent.TenantID, APIKeyID: relayEvent.APIKeyID,
		RequestedModel: relayEvent.RequestedModel,
		StartedAt:      relayEvent.StartedAt,
	}); err != nil {
		t.Fatalf("DurableEmitter.Begin() error = %v", err)
	}
	relayEntryID, err := durable.Emit(ctx, relayEvent)
	if err != nil {
		t.Fatalf("DurableEmitter.Emit() error = %v", err)
	}
	if relayEntryID != "" {
		t.Fatalf("DurableEmitter.Emit() entry ID = %q, want asynchronous relay", relayEntryID)
	}
	relay, err := NewOutboxRelay(
		database.ORM(),
		emitter,
		settings.Group,
		settings.BatchSize,
	)
	if err != nil {
		t.Fatalf("NewOutboxRelay() error = %v", err)
	}
	relay.republishAfter = 0
	if published, err := relay.Publish(ctx); err != nil || published != 1 {
		t.Fatalf("OutboxRelay.Publish() published=%d error=%v", published, err)
	}
	if length, err := client.XLen(ctx, settings.StreamKey).Result(); err != nil || length != 1 {
		t.Fatalf("relayed stream length=%d error=%v", length, err)
	}
	if published, err := relay.Publish(ctx); err != nil || published != 0 {
		t.Fatalf("OutboxRelay.Publish(backlogged) published=%d error=%v", published, err)
	}
	messages, err := client.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    settings.Group,
		Consumer: settings.Consumer,
		Streams:  []string{settings.StreamKey, ">"},
		Count:    1,
	}).Result()
	if err != nil || len(messages) != 1 || len(messages[0].Messages) != 1 {
		t.Fatalf("XReadGroup(relayed) streams=%d error=%v", len(messages), err)
	}
	lostEntryID := messages[0].Messages[0].ID
	if _, err := client.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
		pipe.XAck(ctx, settings.StreamKey, settings.Group, lostEntryID)
		pipe.XDel(ctx, settings.StreamKey, lostEntryID)
		return nil
	}); err != nil {
		t.Fatalf("remove relayed event before storage: %v", err)
	}
	if published, err := relay.Publish(ctx); err != nil || published != 1 {
		t.Fatalf("OutboxRelay.Publish(lost) published=%d error=%v", published, err)
	}
	if duplicate, err := store.Put(ctx, "direct-relay", relayEvent); err != nil || duplicate {
		t.Fatalf("Put(relayed event) duplicate=%t error=%v", duplicate, err)
	}
	if err := client.Del(ctx, settings.StreamKey).Err(); err != nil {
		t.Fatalf("clear republished stream event: %v", err)
	}

	repriceEvent := integrationEvent(t, "request-reprice")
	repriceEvent.ProviderID = "reprice-provider"
	repriceEvent.RequestedModel = "reprice-model"
	repriceEvent.UpstreamModel = "reprice-model"
	if duplicate, err := store.Put(ctx, "direct-reprice", repriceEvent); err != nil || duplicate {
		t.Fatalf("Put(reprice event) duplicate=%t error=%v", duplicate, err)
	}
	unpricedEvent := integrationEvent(t, "request-unpriced")
	unpricedEvent.ProviderID = repriceEvent.ProviderID
	unpricedEvent.RequestedModel = repriceEvent.RequestedModel
	unpricedEvent.UpstreamModel = repriceEvent.UpstreamModel
	unpricedEvent.StartedAt = repriceEvent.StartedAt.Add(-2 * time.Second)
	unpricedEvent.EndedAt = unpricedEvent.StartedAt.Add(time.Millisecond)
	unpricedEvent.LatencyMS = 1
	if duplicate, err := store.Put(ctx, "direct-unpriced", unpricedEvent); err != nil || duplicate {
		t.Fatalf("Put(unpriced event) duplicate=%t error=%v", duplicate, err)
	}
	repriceCatalog, err := NewPricingCatalog([]config.UsagePrice{{
		ProviderID:          "reprice-provider",
		Model:               "reprice-model",
		Version:             "reprice-v1",
		EffectiveFrom:       repriceEvent.StartedAt.Add(-time.Second).Format(time.RFC3339Nano),
		InputUSDPerMillion:  "3",
		OutputUSDPerMillion: "4",
	}})
	if err != nil {
		t.Fatalf("NewPricingCatalog(reprice) error = %v", err)
	}
	originalCatalog := store.pricing.Load()
	if err := store.ReplacePricing(repriceCatalog); err != nil {
		t.Fatalf("ReplacePricing() error = %v", err)
	}
	firstReprice, err := store.Reprice(ctx, RepriceParams{
		Start:       unpricedEvent.StartedAt.Add(-time.Second),
		End:         repriceEvent.EndedAt.Add(time.Second),
		ProviderID:  repriceEvent.ProviderID,
		Model:       repriceEvent.RequestedModel,
		MissingOnly: true,
		Limit:       1,
	})
	if err != nil ||
		firstReprice.Priced != 0 ||
		firstReprice.Unknown != 1 ||
		firstReprice.NextCursor == "" {
		t.Fatalf("Reprice(first page) = %#v, error = %v", firstReprice, err)
	}
	repriceResult, err := store.Reprice(ctx, RepriceParams{
		Start:       unpricedEvent.StartedAt.Add(-time.Second),
		End:         repriceEvent.EndedAt.Add(time.Second),
		ProviderID:  repriceEvent.ProviderID,
		Model:       repriceEvent.RequestedModel,
		MissingOnly: true,
		Limit:       1,
		Cursor:      firstReprice.NextCursor,
	})
	if err := store.ReplacePricing(originalCatalog); err != nil {
		t.Fatalf("restore pricing error = %v", err)
	}
	if err != nil ||
		repriceResult.Priced != 1 ||
		repriceResult.Unknown != 0 {
		t.Fatalf("Reprice() = %#v, error = %v", repriceResult, err)
	}
	var repriced postgres.UsageEvent
	if err := database.ORM().
		Where("event_id = ?", repriceEvent.EventID).
		First(&repriced).Error; err != nil ||
		repriced.TotalCostNanoUSD == nil ||
		*repriced.TotalCostNanoUSD != 10_000 {
		t.Fatalf("repriced row = %#v, error = %v", repriced, err)
	}

	expired := integrationEvent(t, "request-expired")
	expired.StartedAt = time.Now().UTC().AddDate(0, 0, -100)
	expired.EndedAt = expired.StartedAt.Add(time.Second)
	expired.LatencyMS = 1_000
	if duplicate, err := store.Put(ctx, "direct-expired", expired); err != nil || duplicate {
		t.Fatalf("Put(expired event) duplicate=%t error=%v", duplicate, err)
	}
	deleted, err := store.DeleteExpiredUntilIdle(
		ctx,
		time.Now().UTC().AddDate(0, 0, -90),
		1,
	)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteExpiredUntilIdle() deleted=%d error=%v", deleted, err)
	}

	second := integrationEvent(t, "request-recovered")
	secondEntryID, err := emitter.Emit(ctx, second)
	if err != nil {
		t.Fatalf("Emit(second) error = %v", err)
	}
	if err := worker.ensureGroup(ctx); err != nil {
		t.Fatalf("recreate consumer group after stream cleanup: %v", err)
	}
	streams, err := client.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    settings.Group,
		Consumer: "stopped-consumer",
		Streams:  []string{settings.StreamKey, ">"},
		Count:    1,
		Block:    time.Second,
	}).Result()
	if err != nil {
		t.Fatalf("XReadGroup(stopped consumer) error = %v", err)
	}
	if len(streams) != 1 ||
		len(streams[0].Messages) != 1 ||
		streams[0].Messages[0].ID != secondEntryID {
		t.Fatalf("stopped consumer messages = %#v", streams)
	}
	time.Sleep(settings.ClaimIdle + 10*time.Millisecond)

	recoverySettings := settings
	recoverySettings.Consumer = "recovery-consumer"
	recoveryWorker, err := NewWorker(client, store, recoverySettings)
	if err != nil {
		t.Fatalf("NewWorker(recovery) error = %v", err)
	}
	recoveryContext, stopRecovery := context.WithCancel(ctx)
	recoveryDone := make(chan error, 1)
	go func() {
		recoveryDone <- recoveryWorker.Run(recoveryContext)
	}()
	waitForUsageCondition(t, func() bool {
		var count int64
		if database.ORM().Model(&postgres.UsageEvent{}).
			Where("event_id = ?", second.EventID).
			Count(&count).Error != nil {
			return false
		}
		return count == 1 && recoveryWorker.Stats().Claimed >= 1
	})

	if _, err := client.XAdd(ctx, &goredis.XAddArgs{
		Stream: settings.StreamKey,
		Values: map[string]any{"payload": "{"},
	}).Result(); err != nil {
		t.Fatalf("XAdd(poison) error = %v", err)
	}
	waitForUsageCondition(t, func() bool {
		length, err := client.XLen(ctx, settings.DeadLetterKey).Result()
		return err == nil && length == 1 && recoveryWorker.Stats().DeadLettered == 1
	})
	stopRecovery()
	if err := <-recoveryDone; err != nil {
		t.Fatalf("recovery Worker.Run() error = %v", err)
	}
}

func integrationEvent(t *testing.T, requestID string) Event {
	t.Helper()
	collector, err := NewCollector(NewEventInput{
		RequestID:      requestID,
		TenantID:       "00000000-0000-4000-8000-000000000001",
		APIKeyID:       "api-key-integration",
		RequestedModel: "demo-model",
	})
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	collector.SetRoute("upstream", "demo-model", 1, 0, 0)
	collector.ObserveResponse([]byte(
		`{"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":1}}}`,
	))
	event, ok := collector.Finalize(Outcome{Status: StatusSuccess})
	if !ok {
		t.Fatal("Finalize() = false")
	}
	return event
}

func openUsageIntegrationDatabase(t *testing.T) *postgres.Database {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("MODEL_VELO_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("set MODEL_VELO_POSTGRES_TEST_DSN and Redis test variables to run usage integration")
	}
	if strings.TrimSpace(os.Getenv("MODEL_VELO_REDIS_TEST_ADDR")) == "" {
		t.Skip("set MODEL_VELO_REDIS_TEST_ADDR and PostgreSQL test DSN to run usage integration")
	}

	settings := config.Postgres{
		DSN:             dsn,
		MaxOpenConns:    4,
		MaxIdleConns:    1,
		ConnectTimeout:  3 * time.Second,
		MaxConnLifetime: time.Minute,
		MaxConnIdleTime: time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin, err := postgres.Open(ctx, settings)
	if err != nil {
		t.Fatalf("open PostgreSQL integration admin connection: %v", err)
	}

	schema := "model_velo_usage_it_" + randomUsageSuffix(t)
	quotedSchema := `"` + schema + `"`
	if err := admin.ORM().WithContext(ctx).Exec("CREATE SCHEMA " + quotedSchema).Error; err != nil {
		_ = admin.Close()
		t.Fatalf("create PostgreSQL integration schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := admin.ORM().WithContext(cleanupContext).
			Exec("DROP SCHEMA " + quotedSchema + " CASCADE").Error; err != nil {
			t.Errorf("drop PostgreSQL integration schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL integration admin connection: %v", err)
		}
	})

	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("MODEL_VELO_POSTGRES_TEST_DSN must be a PostgreSQL URL")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("application_name", "model-velo-usage-integration")
	parsed.RawQuery = query.Encode()
	settings.DSN = parsed.String()

	database, err := postgres.Open(ctx, settings)
	if err != nil {
		t.Fatalf("open isolated PostgreSQL integration database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close isolated PostgreSQL integration database: %v", err)
		}
	})
	return database
}

func openUsageIntegrationRedis(t *testing.T) *goredis.Client {
	t.Helper()
	address := strings.TrimSpace(os.Getenv("MODEL_VELO_REDIS_TEST_ADDR"))
	if address == "" {
		t.Skip("set MODEL_VELO_REDIS_TEST_ADDR to run usage integration")
	}
	database := 0
	if raw := strings.TrimSpace(os.Getenv("MODEL_VELO_REDIS_TEST_DB")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			t.Fatal("MODEL_VELO_REDIS_TEST_DB must be a non-negative integer")
		}
		database = parsed
	}
	client := goredis.NewClient(&goredis.Options{
		Addr:                  address,
		Password:              os.Getenv("MODEL_VELO_REDIS_TEST_PASSWORD"),
		DB:                    database,
		ContextTimeoutEnabled: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("ping Redis integration server: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close Redis integration client: %v", err)
		}
	})
	return client
}

func randomUsageSuffix(t *testing.T) string {
	t.Helper()
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("generate usage test suffix: %v", err)
	}
	return hex.EncodeToString(random)
}

func waitForUsageCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("usage integration condition was not met")
}

func TestNewWorkerRejectsInvalidConfiguration(t *testing.T) {
	_, err := NewWorker(nil, nil, config.Usage{})
	if err == nil || !strings.Contains(err.Error(), "requires Redis") {
		t.Fatalf("NewWorker() error = %v", err)
	}
}
