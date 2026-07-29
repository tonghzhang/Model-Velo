package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"model-velo/internal/observability"
	"model-velo/internal/quota"
	"model-velo/internal/reliability"
	"model-velo/internal/usage"
)

type usageSession struct {
	collector     *usage.Collector
	emitter       usage.Emitter
	event         usage.Event
	ready         bool
	eventID       string
	quota         *quota.Manager
	reservationID string
	metrics       *observability.Metrics
}

func newUsageSession(
	ctx context.Context,
	emitter usage.Emitter,
	tenantID string,
	apiKeyID string,
	model string,
	stream bool,
	metrics *observability.Metrics,
) (*usageSession, error) {
	startedAt := time.Now()
	stageResult := "error"
	defer func() {
		metrics.RequestStage(
			"usage_begin", stageResult, "", time.Since(startedAt),
		)
	}()

	collector, err := usage.NewCollector(usage.NewEventInput{
		RequestID:      requestIDFromContext(ctx),
		TenantID:       tenantID,
		APIKeyID:       apiKeyID,
		RequestedModel: model,
		Stream:         stream,
	})
	if err != nil {
		return nil, fmt.Errorf("create usage collector: %w", err)
	}
	pending, pendingOK := collector.Pending()
	if !pendingOK {
		return nil, errors.New("usage lifecycle could not be prepared")
	}
	if lifecycle, ok := emitter.(usage.LifecycleEmitter); ok {
		if err := lifecycle.Begin(ctx, pending); err != nil {
			metrics.UsageDelivery("lifecycle_begin", "error")
			return nil, fmt.Errorf("begin usage lifecycle: %w", err)
		}
		metrics.UsageDelivery("lifecycle_begin", "success")
	}
	stageResult = "success"
	return &usageSession{
		collector: collector, emitter: emitter, eventID: pending.EventID,
		metrics: metrics,
	}, nil
}

func (session *usageSession) finish(c *gin.Context) {
	if session == nil {
		return
	}
	if !session.ready {
		status := usage.StatusFailed
		category := "gateway"
		code := apiErrorCode(c)
		if c.Request.Context().Err() != nil {
			status = usage.StatusCanceled
			category = "client"
			if code == "" {
				code = "client_canceled"
			}
		}
		if code == "" {
			code = "request_incomplete"
		}
		session.finalize(usage.Outcome{
			Status:        status,
			ErrorCategory: category,
			ErrorCode:     code,
		})
	}
	if !session.ready {
		return
	}
	if session.quota != nil && session.reservationID != "" {
		settleContext, cancel := context.WithTimeout(
			context.WithoutCancel(c.Request.Context()), 2*time.Second,
		)
		startedAt := time.Now()
		err := session.quota.Settle(
			settleContext, session.reservationID, session.event,
		)
		result := "success"
		if err != nil {
			result = "error"
			slog.Error(
				"quota settlement failed",
				"request_id", session.event.RequestID,
				"event_id", session.event.EventID,
				"error", err,
			)
		}
		session.metrics.RequestStage(
			"quota_settle", result, "", time.Since(startedAt),
		)
		cancel()
	}
	startedAt := time.Now()
	entryID, err := session.emitter.Emit(c.Request.Context(), session.event)
	result := "queued"
	if entryID != "" {
		result = "published"
	}
	if err != nil {
		result = "deferred"
	}
	session.metrics.RequestStage(
		"usage_finalize", result, "", time.Since(startedAt),
	)
	if err != nil {
		session.metrics.UsageDelivery("finalize", "deferred")
		slog.Warn(
			"usage finalization deferred",
			"request_id", session.event.RequestID,
			"event_id", session.event.EventID,
			"error", err,
		)
		return
	}
	session.metrics.UsageDelivery("finalize", result)
}

func (session *usageSession) attachQuota(
	manager *quota.Manager,
	reservationID string,
) {
	if session == nil {
		return
	}
	session.quota = manager
	session.reservationID = strings.TrimSpace(reservationID)
}

func (session *usageSession) finalize(outcome usage.Outcome) {
	if session == nil || session.ready {
		return
	}
	event, ok := session.collector.Finalize(outcome)
	if !ok {
		return
	}
	session.event = event
	session.ready = true
}

func (session *usageSession) setCacheStatus(status string) {
	if session != nil {
		session.collector.SetCacheStatus(status)
	}
}

func (session *usageSession) observe(payload []byte) {
	if session != nil {
		session.collector.ObserveResponse(payload)
	}
}

func (session *usageSession) observeStream(payload []byte) {
	if session != nil {
		session.collector.ObserveStreamResponse(payload, time.Now())
	}
}

func (session *usageSession) recordExecution(result reliability.ExecutionResult) {
	if session == nil {
		return
	}
	session.collector.SetRoute(
		result.ProviderID,
		result.UpstreamModel,
		result.Attempts,
		retriesFromTrail(result.Trail),
		result.Fallbacks,
	)
}

func (session *usageSession) recordStream(stream *reliability.PreparedStream) {
	if session == nil || stream == nil {
		return
	}
	session.collector.SetRoute(
		stream.ProviderID,
		stream.UpstreamModel,
		stream.Attempts,
		retriesFromTrail(stream.Trail),
		stream.Fallbacks,
	)
}

func (session *usageSession) recordFailure(failure *reliability.Failure) {
	if session == nil || failure == nil {
		return
	}
	upstreamModel := ""
	if len(failure.Trail) > 0 {
		upstreamModel = failure.Trail[len(failure.Trail)-1].UpstreamModel
	}
	session.collector.SetRoute(
		failure.ProviderID,
		upstreamModel,
		failure.TotalAttempts,
		retriesFromTrail(failure.Trail),
		failure.Fallbacks,
	)
}

func retriesFromTrail(trail []reliability.AttemptRecord) int {
	retries := 0
	for _, attempt := range trail {
		if attempt.Attempt > 1 {
			retries++
		}
	}
	return retries
}
