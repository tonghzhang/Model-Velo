package observability

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"

	"model-velo/internal/config"
)

func ConfigureTracing(
	ctx context.Context,
	settings config.Observability,
) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	if settings.OTELEndpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	parsed, err := url.Parse(settings.OTELEndpoint)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("OpenTelemetry endpoint must be an absolute URL")
	}
	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(parsed.Host),
	}
	if parsed.Path != "" && parsed.Path != "/" {
		options = append(options, otlptracehttp.WithURLPath(parsed.Path))
	}
	if settings.OTELInsecure || parsed.Scheme == "http" {
		options = append(options, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	serviceResource, err := resource.New(
		ctx,
		resource.WithAttributes(semconv.ServiceName(settings.ServiceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("create trace resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(serviceResource),
		sdktrace.WithSampler(sdktrace.ParentBased(
			sdktrace.TraceIDRatioBased(settings.OTELSampleRatio),
		)),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
