package reliability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"model-velo/internal/provider"
	"model-velo/internal/routing"
)

func TestStage3ConfigurationBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		validate func() error
	}{
		{
			name: "breaker requires a positive threshold",
			validate: func() error {
				config := DefaultBreakerConfig()
				config.FailureThreshold = 0
				return config.Validate()
			},
		},
		{
			name: "queue requires execution capacity",
			validate: func() error {
				config := DefaultQueueConfig()
				config.MaxInFlight = 0
				return config.Validate()
			},
		},
		{
			name: "queue rejects negative waiting capacity",
			validate: func() error {
				config := DefaultQueueConfig()
				config.MaxWaiting = -1
				return config.Validate()
			},
		},
		{
			name: "retry requires at least one attempt",
			validate: func() error {
				config := DefaultRetryConfig()
				config.MaxAttempts = 0
				return config.Validate()
			},
		},
		{
			name: "attempt timeout cannot exceed request budget",
			validate: func() error {
				config := DefaultRetryConfig()
				config.AttemptTimeout = config.RequestTimeout + time.Second
				return config.Validate()
			},
		},
		{
			name: "jitter is a bounded ratio",
			validate: func() error {
				config := DefaultRetryConfig()
				config.JitterRatio = 1.1
				return config.Validate()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}

	if err := DefaultBreakerConfig().Validate(); err != nil {
		t.Fatalf("default breaker config: %v", err)
	}
	if err := DefaultQueueConfig().Validate(); err != nil {
		t.Fatalf("default queue config: %v", err)
	}
	if err := DefaultRetryConfig().Validate(); err != nil {
		t.Fatalf("default retry config: %v", err)
	}
}

func TestStage3RetryPolicy(t *testing.T) {
	t.Run("uses a bounded deterministic backoff", func(t *testing.T) {
		config := DefaultRetryConfig()
		config.InitialBackoff = 100 * time.Millisecond
		config.MaxBackoff = 250 * time.Millisecond
		config.BackoffMultiplier = 2
		config.JitterRatio = 0.5
		policy, err := NewRetryPolicy(config)
		if err != nil {
			t.Fatalf("NewRetryPolicy() error = %v", err)
		}
		policy.random = func() float64 { return 0.5 }
		failure := &Failure{Category: CategoryNetwork}

		want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond}
		for index, expected := range want {
			if got := policy.Backoff(failure, index+1); got != expected {
				t.Fatalf("Backoff(attempt=%d) = %s, want %s", index+1, got, expected)
			}
		}
		if got := policy.Backoff(&Failure{Category: CategoryKeyForbidden}, 1); got != 0 {
			t.Fatalf("key switch backoff = %s, want 0", got)
		}
	})

	t.Run("stops waiting on cancellation or an insufficient budget", func(t *testing.T) {
		policy, err := NewRetryPolicy(DefaultRetryConfig())
		if err != nil {
			t.Fatalf("NewRetryPolicy() error = %v", err)
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if policy.Wait(canceled, time.Second) {
			t.Fatal("Wait() accepted a canceled context")
		}

		short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer stop()
		if policy.Wait(short, time.Second) {
			t.Fatal("Wait() accepted a delay beyond the request budget")
		}
		if short.Err() != nil {
			t.Fatalf("Wait() consumed the remaining budget: %v", short.Err())
		}
	})

	t.Run("parses HTTP-date Retry-After against a controlled clock", func(t *testing.T) {
		now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
		future := now.Add(90 * time.Second).Format(http.TimeFormat)
		if got, ok := parseRetryAfter(future, now); !ok || got != 90*time.Second {
			t.Fatalf("parseRetryAfter(future) = %s, %v", got, ok)
		}
		past := now.Add(-time.Minute).Format(http.TimeFormat)
		if got, ok := parseRetryAfter(past, now); !ok || got != 0 {
			t.Fatalf("parseRetryAfter(past) = %s, %v", got, ok)
		}
		farFuture := now.Add(48 * time.Hour).Format(http.TimeFormat)
		if got, ok := parseRetryAfter(farFuture, now); !ok || got != maximumRetryAfter {
			t.Fatalf("parseRetryAfter(far future) = %s, %v", got, ok)
		}
	})
}

func TestStage3ConcurrentState(t *testing.T) {
	t.Run("limits concurrent half-open probes", func(t *testing.T) {
		now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
		breaker, err := NewBreakerWithClock("provider-a", BreakerConfig{
			FailureThreshold:  1,
			OpenDuration:      time.Second,
			HalfOpenMaxProbes: 4,
		}, func() time.Time { return now })
		if err != nil {
			t.Fatalf("NewBreakerWithClock() error = %v", err)
		}
		permit, failure := breaker.Allow()
		if failure != nil || !permit.Complete(&Failure{Category: CategoryNetwork}) {
			t.Fatalf("open breaker = %#v, %#v", permit, failure)
		}
		now = now.Add(time.Second)

		const contenders = 64
		start := make(chan struct{})
		permits := make(chan *Permit, contenders)
		var rejected atomic.Int64
		var workers sync.WaitGroup
		for range contenders {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				probe, denied := breaker.Allow()
				if denied != nil {
					rejected.Add(1)
					return
				}
				permits <- probe
			}()
		}
		close(start)
		workers.Wait()
		close(permits)

		var admitted []*Permit
		for probe := range permits {
			admitted = append(admitted, probe)
		}
		if len(admitted) != 4 || rejected.Load() != contenders-4 {
			t.Fatalf("half-open admissions = %d, rejected = %d", len(admitted), rejected.Load())
		}
		for _, probe := range admitted {
			if !probe.Complete(nil) {
				t.Fatal("probe completion was rejected")
			}
		}
		if snapshot := breaker.Snapshot(); snapshot.State != StateClosed || snapshot.HalfOpenInFlight != 0 {
			t.Fatalf("breaker snapshot = %#v", snapshot)
		}
	})

	t.Run("keeps queue occupancy within capacity", func(t *testing.T) {
		queue, err := NewProviderQueue("provider-a", QueueConfig{
			MaxInFlight: 8,
			MaxWaiting:  64,
			WaitTimeout: 2 * time.Second,
		})
		if err != nil {
			t.Fatalf("NewProviderQueue() error = %v", err)
		}

		const workers = 64
		const iterations = 25
		start := make(chan struct{})
		var maximumActive atomic.Int64
		var failures atomic.Int64
		var group sync.WaitGroup
		for range workers {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				for range iterations {
					lease, failure := queue.Acquire(context.Background())
					if failure != nil {
						failures.Add(1)
						return
					}
					active := queue.Snapshot().Active
					for current := maximumActive.Load(); active > current && !maximumActive.CompareAndSwap(current, active); current = maximumActive.Load() {
					}
					lease.Release()
				}
			}()
		}
		close(start)
		group.Wait()

		if failures.Load() != 0 {
			t.Fatalf("queue acquisition failures = %d", failures.Load())
		}
		if maximumActive.Load() > 8 {
			t.Fatalf("maximum active = %d, want <= 8", maximumActive.Load())
		}
		if snapshot := queue.Snapshot(); snapshot.Active != 0 || snapshot.Waiting != 0 {
			t.Fatalf("final queue snapshot = %#v", snapshot)
		}
	})

	t.Run("rotates keys safely under concurrent selection", func(t *testing.T) {
		const keyCount = 16
		const selectionsPerKey = 100
		configured := make([]ProviderKey, 0, keyCount)
		indexes := make(map[string]int, keyCount)
		for index := range keyCount {
			id := "key-" + strconv.Itoa(index)
			configured = append(configured, ProviderKey{ID: id, Secret: "test-secret-" + strconv.Itoa(index)})
			indexes[id] = index
		}
		registry, err := NewProviderKeyRegistry(
			[]string{"provider-a"},
			[]ProviderKeySet{{ProviderID: "provider-a", Keys: configured}},
		)
		if err != nil {
			t.Fatalf("NewProviderKeyRegistry() error = %v", err)
		}

		counts := make([]atomic.Int64, keyCount)
		var failures atomic.Int64
		var group sync.WaitGroup
		for range keyCount * selectionsPerKey {
			group.Add(1)
			go func() {
				defer group.Done()
				selection, failure := registry.Select("provider-a")
				if failure != nil || selection == nil {
					failures.Add(1)
					return
				}
				index, ok := indexes[selection.KeyID()]
				if !ok || selection.Secret() == "" || !selection.Complete(nil) {
					failures.Add(1)
					return
				}
				counts[index].Add(1)
			}()
		}
		group.Wait()

		if failures.Load() != 0 {
			t.Fatalf("key selection failures = %d", failures.Load())
		}
		for index := range counts {
			if got := counts[index].Load(); got != selectionsPerKey {
				t.Fatalf("key %d selections = %d, want %d", index, got, selectionsPerKey)
			}
		}
	})
}

func TestStage3ProviderKeyBoundary(t *testing.T) {
	tests := []struct {
		name      string
		providers []string
		sets      []ProviderKeySet
	}{
		{
			name:      "missing provider key set",
			providers: []string{"provider-a"},
		},
		{
			name:      "unknown provider key set",
			providers: []string{"provider-a"},
			sets:      []ProviderKeySet{{ProviderID: "provider-b", Keys: []ProviderKey{{ID: "key-a", Secret: "secret-a"}}}},
		},
		{
			name:      "empty provider secret",
			providers: []string{"provider-a"},
			sets:      []ProviderKeySet{{ProviderID: "provider-a", Keys: []ProviderKey{{ID: "key-a"}}}},
		},
		{
			name:      "duplicate key ID",
			providers: []string{"provider-a"},
			sets: []ProviderKeySet{{ProviderID: "provider-a", Keys: []ProviderKey{
				{ID: "key-a", Secret: "secret-a"},
				{ID: "key-a", Secret: "secret-b"},
			}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewProviderKeyRegistry(test.providers, test.sets); err == nil {
				t.Fatal("NewProviderKeyRegistry() error = nil")
			}
		})
	}

	key := ProviderKey{ID: "key-a", Secret: "secret-must-not-appear"}
	set := ProviderKeySet{ProviderID: "provider-a", Keys: []ProviderKey{key}}
	for _, formatted := range []string{fmt.Sprint(key), fmt.Sprintf("%#v", key), fmt.Sprint(set), fmt.Sprintf("%#v", set)} {
		if strings.Contains(formatted, key.Secret) {
			t.Fatal("safe formatting exposed a provider key secret")
		}
	}
}

func TestAttemptExecutorRequiresKeysForAPIKeyAdapters(t *testing.T) {
	keyedAdapters, err := provider.NewAdapterRegistryFromAdapters(map[string]provider.Adapter{
		"provider-a": reliabilityTestAdapter{authentication: provider.AuthenticationAPIKey},
	})
	if err != nil {
		t.Fatalf("NewAdapterRegistryFromAdapters() error = %v", err)
	}
	breakers, err := NewBreakerRegistry([]string{"provider-a"}, DefaultBreakerConfig())
	if err != nil {
		t.Fatalf("NewBreakerRegistry() error = %v", err)
	}
	queues, err := NewQueueRegistry([]string{"provider-a"}, DefaultQueueConfig())
	if err != nil {
		t.Fatalf("NewQueueRegistry() error = %v", err)
	}
	retry, err := NewRetryPolicy(DefaultRetryConfig())
	if err != nil {
		t.Fatalf("NewRetryPolicy() error = %v", err)
	}

	if _, err := NewAttemptExecutor(keyedAdapters, breakers, queues, nil, retry); err == nil || !strings.Contains(err.Error(), "provider key registry") {
		t.Fatalf("NewAttemptExecutor() error = %v", err)
	}

	keylessAdapters, err := provider.NewAdapterRegistryFromAdapters(map[string]provider.Adapter{
		"provider-a": reliabilityTestAdapter{authentication: provider.AuthenticationNone},
	})
	if err != nil {
		t.Fatalf("NewAdapterRegistryFromAdapters() error = %v", err)
	}
	if _, err := NewAttemptExecutor(keylessAdapters, breakers, queues, nil, retry); err != nil {
		t.Fatalf("keyless NewAttemptExecutor() error = %v", err)
	}
}

func TestReliabilityTraceHierarchyAndRedaction(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	previousTracer := reliabilityTracer
	reliabilityTracer = tracerProvider.Tracer(reliabilityTracerName)
	t.Cleanup(func() {
		reliabilityTracer = previousTracer
		_ = tracerProvider.Shutdown(context.Background())
	})

	const (
		primarySecret   = "primary-trace-secret"
		secondarySecret = "secondary-trace-secret"
		privatePrompt   = "do not export this prompt"
	)
	adapters, err := provider.NewAdapterRegistryFromAdapters(
		map[string]provider.Adapter{
			"primary": traceTestAdapter{
				secret: primarySecret,
				err:    errors.New("primary network failure"),
			},
			"secondary": traceTestAdapter{
				secret: secondarySecret,
				body:   []byte(`{"id":"chatcmpl-trace"}`),
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	providerIDs := []string{"primary", "secondary"}
	breakers, err := NewBreakerRegistry(providerIDs, DefaultBreakerConfig())
	if err != nil {
		t.Fatal(err)
	}
	queues, err := NewQueueRegistry(providerIDs, DefaultQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := NewProviderKeyRegistry(providerIDs, []ProviderKeySet{
		{
			ProviderID: "primary",
			Keys:       []ProviderKey{{ID: "primary-key", Secret: primarySecret}},
		},
		{
			ProviderID: "secondary",
			Keys:       []ProviderKey{{ID: "secondary-key", Secret: secondarySecret}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	retryConfig := DefaultRetryConfig()
	retryConfig.MaxAttempts = 2
	retryConfig.InitialBackoff = minimumRetryBackoff
	retryConfig.MaxBackoff = minimumRetryBackoff
	retryConfig.JitterRatio = 0
	retryConfig.RequestTimeout = time.Second
	retryConfig.AttemptTimeout = 500 * time.Millisecond
	retry, err := NewRetryPolicy(retryConfig)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := NewAttemptExecutor(
		adapters, breakers, queues, keys, retry,
	)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator, err := NewOrchestrator(attempts, retry)
	if err != nil {
		t.Fatal(err)
	}

	ctx, parent := tracerProvider.Tracer("test").Start(
		context.Background(),
		"http.request",
	)
	result, failure := orchestrator.Execute(ctx, ExecutionInput{
		RequestID: "Bearer " + primarySecret,
		Request: provider.ChatRequest{
			Model: "gateway-model",
			Messages: []provider.ChatMessage{{
				Role:    "user",
				Content: []byte(strconv.Quote(privatePrompt)),
			}},
		},
		Plan: routing.Plan{
			RequestedModel: "gateway-model",
			Candidates: []routing.Candidate{
				{
					ProviderID:    "primary",
					UpstreamModel: "primary-model",
					Priority:      0,
				},
				{
					ProviderID:    "secondary",
					UpstreamModel: "secondary-model",
					Priority:      1,
				},
			},
		},
	})
	parent.End()
	if failure != nil {
		t.Fatalf("Execute() failure = %v", failure)
	}
	if result.ProviderID != "secondary" || result.Attempts != 3 || result.Fallbacks != 1 {
		t.Fatalf("Execute() result = %#v", result)
	}

	spans := exporter.GetSpans()
	var root tracetest.SpanStub
	attemptSpans := make(map[string]tracetest.SpanStub)
	queueSpans := make([]tracetest.SpanStub, 0, 3)
	var exported strings.Builder
	for _, span := range spans {
		fmt.Fprintln(&exported, span.Name, span.Status.Description)
		for _, field := range span.Attributes {
			fmt.Fprintln(&exported, field.Key, field.Value.Emit())
		}
		for _, event := range span.Events {
			fmt.Fprintln(&exported, event.Name)
			for _, field := range event.Attributes {
				fmt.Fprintln(&exported, field.Key, field.Value.Emit())
			}
		}
		switch span.Name {
		case "http.request":
			root = span
		case "gateway.provider.attempt":
			attemptSpans[span.SpanContext.SpanID().String()] = span
		case "gateway.queue.wait":
			queueSpans = append(queueSpans, span)
		}
	}
	if !root.SpanContext.IsValid() {
		t.Fatal("root span was not exported")
	}
	if len(attemptSpans) != 3 || len(queueSpans) != 3 {
		t.Fatalf(
			"exported attempts=%d queues=%d, want 3 each",
			len(attemptSpans),
			len(queueSpans),
		)
	}
	for _, attempt := range attemptSpans {
		if attempt.Parent.SpanID() != root.SpanContext.SpanID() {
			t.Fatalf(
				"attempt parent = %s, want root %s",
				attempt.Parent.SpanID(),
				root.SpanContext.SpanID(),
			)
		}
	}
	for _, queue := range queueSpans {
		if _, ok := attemptSpans[queue.Parent.SpanID().String()]; !ok {
			t.Fatalf("queue parent %s is not an attempt span", queue.Parent.SpanID())
		}
	}
	traceText := exported.String()
	for _, eventName := range []string{"gateway.retry", "gateway.fallback"} {
		if !strings.Contains(traceText, eventName) {
			t.Fatalf("trace does not contain %q event", eventName)
		}
	}
	for _, sensitive := range []string{
		primarySecret,
		secondarySecret,
		privatePrompt,
		"Authorization",
		"Bearer",
	} {
		if strings.Contains(traceText, sensitive) {
			t.Fatalf("trace exported sensitive value %q", sensitive)
		}
	}
}

type reliabilityTestAdapter struct {
	authentication provider.Authentication
}

func (adapter reliabilityTestAdapter) Authentication() provider.Authentication {
	return adapter.authentication
}

func (reliabilityTestAdapter) Complete(context.Context, provider.ChatInput, string) ([]byte, error) {
	return nil, errors.New("not used")
}

type traceTestAdapter struct {
	secret string
	body   []byte
	err    error
}

func (traceTestAdapter) Authentication() provider.Authentication {
	return provider.AuthenticationAPIKey
}

func (adapter traceTestAdapter) Complete(
	_ context.Context,
	_ provider.ChatInput,
	apiKey string,
) ([]byte, error) {
	if apiKey != adapter.secret {
		return nil, errors.New("unexpected provider key")
	}
	return adapter.body, adapter.err
}
