package reliability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const reliabilityTracerName = "model-velo/reliability"

var reliabilityTracer = otel.Tracer(reliabilityTracerName)

func startAttemptSpan(
	ctx context.Context,
	input AttemptInput,
	attempt int,
	stream bool,
) (context.Context, trace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	return reliabilityTracer.Start(
		ctx,
		"gateway.provider.attempt",
		trace.WithAttributes(
			attribute.String("gateway.provider.id", input.Candidate.ProviderID),
			attribute.String("gateway.upstream.model", input.Candidate.UpstreamModel),
			attribute.Int("gateway.candidate", input.Candidate.Priority),
			attribute.Int("gateway.attempt", attempt),
			attribute.Bool("gateway.stream", stream),
		),
	)
}

func finishAttemptSpan(
	span trace.Span,
	failure *Failure,
	attempted bool,
) {
	if span == nil {
		return
	}
	span.SetAttributes(attribute.Bool("gateway.attempt.executed", attempted))
	if failure == nil {
		span.SetStatus(codes.Ok, "")
		span.End()
		return
	}
	span.SetAttributes(
		attribute.String("gateway.failure.category", string(failure.Category)),
	)
	if failure.KeyID != "" {
		span.SetAttributes(attribute.String("gateway.key.id", failure.KeyID))
	}
	if failure.StatusCode != 0 {
		span.SetAttributes(attribute.Int("gateway.upstream.status_code", failure.StatusCode))
	}
	if failure.Queue != "" {
		span.SetAttributes(attribute.String("gateway.queue.reason", string(failure.Queue)))
	}
	span.SetStatus(codes.Error, string(failure.Category))
	span.End()
}

func setAttemptKeyID(span trace.Span, keyID string) {
	if span != nil && keyID != "" {
		span.SetAttributes(attribute.String("gateway.key.id", keyID))
	}
}

func traceQueueAcquire(
	ctx context.Context,
	queues *QueueRegistry,
	providerID string,
) (*QueueLease, *Failure) {
	if ctx == nil {
		ctx = context.Background()
	}
	_, span := reliabilityTracer.Start(
		ctx,
		"gateway.queue.wait",
		trace.WithAttributes(
			attribute.String("gateway.provider.id", providerID),
		),
	)
	startedAt := time.Now()
	lease, failure := queues.Acquire(ctx, providerID)
	duration := time.Since(startedAt)
	result := "acquired"
	if failure == nil {
		span.SetAttributes(attribute.String("gateway.queue.result", "acquired"))
		span.SetStatus(codes.Ok, "")
	} else {
		result = string(failure.Queue)
		if result == "" {
			result = string(failure.Category)
		}
		span.SetAttributes(
			attribute.String("gateway.queue.result", string(failure.Queue)),
			attribute.String("gateway.failure.category", string(failure.Category)),
		)
		span.SetStatus(codes.Error, string(failure.Category))
	}
	observeQueueWait(ctx, providerID, result, duration)
	span.End()
	return lease, failure
}

func addRetryEvent(
	ctx context.Context,
	providerID string,
	candidate int,
	attempt int,
	failure *Failure,
	backoff time.Duration,
) {
	if ctx == nil || failure == nil {
		return
	}
	trace.SpanFromContext(ctx).AddEvent(
		"gateway.retry",
		trace.WithAttributes(
			attribute.String("gateway.provider.id", providerID),
			attribute.Int("gateway.candidate", candidate),
			attribute.Int("gateway.attempt", attempt),
			attribute.String("gateway.failure.category", string(failure.Category)),
			attribute.Int64("gateway.retry.backoff_ms", backoff.Milliseconds()),
			attribute.Bool("gateway.retry.switch_key", SignalsFor(failure).SwitchKey),
		),
	)
}

func addFallbackEvent(
	ctx context.Context,
	fromProvider string,
	toProvider string,
	fallback int,
	failure *Failure,
) {
	if ctx == nil || failure == nil {
		return
	}
	trace.SpanFromContext(ctx).AddEvent(
		"gateway.fallback",
		trace.WithAttributes(
			attribute.String("gateway.fallback.from_provider", fromProvider),
			attribute.String("gateway.fallback.to_provider", toProvider),
			attribute.Int("gateway.fallback.number", fallback),
			attribute.String("gateway.failure.category", string(failure.Category)),
		),
	)
}
