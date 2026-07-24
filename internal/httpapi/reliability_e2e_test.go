package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"model-velo/internal/httpapi"
	"model-velo/internal/provider"
	"model-velo/internal/ratelimit"
	"model-velo/internal/reliability"
	"model-velo/internal/routing"
)

func TestStage3KeyRecoveryAndFallback(t *testing.T) {
	for _, test := range []struct {
		name              string
		status            int
		wantRejectedState reliability.ProviderKeyState
	}{
		{name: "401 disables the rejected key", status: http.StatusUnauthorized, wantRejectedState: reliability.ProviderKeyDisabled},
		{name: "403 excludes the key only for this request", status: http.StatusForbidden, wantRejectedState: reliability.ProviderKeyAvailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var order []int
			adapter := &stage3Adapter{complete: func(_ context.Context, _ provider.ChatInput, apiKey string) ([]byte, error) {
				order = append(order, stage3KeyNumber(apiKey))
				if len(order) == 1 {
					return nil, &provider.HTTPError{StatusCode: test.status}
				}
				return stage3SuccessBody, nil
			}}
			harness := newStage3Harness(t, []string{"primary"}, map[string]provider.Adapter{
				"primary": adapter,
			}, []reliability.ProviderKeySet{{
				ProviderID: "primary",
				Keys: []reliability.ProviderKey{
					{ID: "key-one", Secret: "stage3-secret-one"},
					{ID: "key-two", Secret: "stage3-secret-two"},
				},
			}}, stage3RetryConfig(3))

			response := httptest.NewRecorder()
			harness.handler.ServeHTTP(response, validChatRequest())
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if len(order) != 2 || order[0] != 1 || order[1] != 2 {
				t.Fatalf("key order = %v, want [1 2]", order)
			}

			states := stage3KeyStates(harness.keys)
			if states["key-one"] != test.wantRejectedState || states["key-two"] != reliability.ProviderKeyAvailable {
				t.Fatalf("key states = %#v", states)
			}
		})
	}

	t.Run("429 cools the rejected key and switches immediately", func(t *testing.T) {
		var order []int
		adapter := &stage3Adapter{complete: func(_ context.Context, _ provider.ChatInput, apiKey string) ([]byte, error) {
			order = append(order, stage3KeyNumber(apiKey))
			if len(order) == 1 {
				return nil, &provider.HTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: "60"}
			}
			return stage3SuccessBody, nil
		}}
		harness := newStage3Harness(t, []string{"primary"}, map[string]provider.Adapter{
			"primary": adapter,
		}, []reliability.ProviderKeySet{{
			ProviderID: "primary",
			Keys: []reliability.ProviderKey{
				{ID: "key-one", Secret: "stage3-secret-one"},
				{ID: "key-two", Secret: "stage3-secret-two"},
			},
		}}, stage3RetryConfig(3))

		response := httptest.NewRecorder()
		harness.handler.ServeHTTP(response, validChatRequest())
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if len(order) != 2 || order[0] != 1 || order[1] != 2 {
			t.Fatalf("key order = %v, want [1 2]", order)
		}
		if states := stage3KeyStates(harness.keys); states["key-one"] != reliability.ProviderKeyCooling {
			t.Fatalf("key states = %#v", states)
		}
	})

	t.Run("exhausted primary keys fall back to the next provider", func(t *testing.T) {
		primary := &stage3Adapter{complete: func(context.Context, provider.ChatInput, string) ([]byte, error) {
			return nil, &provider.HTTPError{StatusCode: http.StatusUnauthorized}
		}}
		secondary := &stage3Adapter{complete: func(context.Context, provider.ChatInput, string) ([]byte, error) {
			return stage3SuccessBody, nil
		}}
		harness := newStage3Harness(t, []string{"primary", "secondary"}, map[string]provider.Adapter{
			"primary":   primary,
			"secondary": secondary,
		}, []reliability.ProviderKeySet{
			{ProviderID: "primary", Keys: []reliability.ProviderKey{{ID: "primary-key", Secret: "stage3-secret-one"}}},
			{ProviderID: "secondary", Keys: []reliability.ProviderKey{{ID: "secondary-key", Secret: "stage3-secret-two"}}},
		}, stage3RetryConfig(3))

		response := httptest.NewRecorder()
		harness.handler.ServeHTTP(response, validChatRequest())
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if primary.calls.Load() != 1 || secondary.calls.Load() != 1 {
			t.Fatalf("provider calls = primary:%d secondary:%d", primary.calls.Load(), secondary.calls.Load())
		}
		if states := stage3KeyStates(harness.keys); states["primary-key"] != reliability.ProviderKeyDisabled {
			t.Fatalf("key states = %#v", states)
		}
	})
}

func TestStage3RetryExhaustionReleasesResources(t *testing.T) {
	for _, test := range []struct {
		name      string
		complete  func(call int32) ([]byte, error)
		wantCalls int32
		wantCode  int
	}{
		{
			name: "network failure retries and then succeeds",
			complete: func(call int32) ([]byte, error) {
				if call == 1 {
					return nil, errors.New("test network failure")
				}
				return stage3SuccessBody, nil
			},
			wantCalls: 2,
			wantCode:  http.StatusOK,
		},
		{
			name: "network failure stops at the attempt limit",
			complete: func(int32) ([]byte, error) {
				return nil, errors.New("test network failure")
			},
			wantCalls: 3,
			wantCode:  http.StatusBadGateway,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := &stage3Adapter{}
			adapter.complete = func(context.Context, provider.ChatInput, string) ([]byte, error) {
				return test.complete(adapter.calls.Load())
			}
			harness := newStage3Harness(t, []string{"primary"}, map[string]provider.Adapter{
				"primary": adapter,
			}, []reliability.ProviderKeySet{{
				ProviderID: "primary",
				Keys:       []reliability.ProviderKey{{ID: "primary-key", Secret: "stage3-secret-one"}},
			}}, stage3RetryConfig(3))

			response := httptest.NewRecorder()
			harness.handler.ServeHTTP(response, validChatRequest())
			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantCode, response.Body.String())
			}
			if adapter.calls.Load() != test.wantCalls {
				t.Fatalf("adapter calls = %d, want %d", adapter.calls.Load(), test.wantCalls)
			}
			stage3AssertNoQueueOccupancy(t, harness.queues)
		})
	}
}

func TestStage3CancellationStopsFallbackAndReleasesResources(t *testing.T) {
	secondaryEntered := make(chan struct{})
	var enteredOnce sync.Once
	primary := &stage3Adapter{complete: func(context.Context, provider.ChatInput, string) ([]byte, error) {
		return nil, &provider.HTTPError{StatusCode: http.StatusServiceUnavailable}
	}}
	secondary := &stage3Adapter{complete: func(ctx context.Context, _ provider.ChatInput, _ string) ([]byte, error) {
		enteredOnce.Do(func() { close(secondaryEntered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	tertiary := &stage3Adapter{complete: func(context.Context, provider.ChatInput, string) ([]byte, error) {
		return stage3SuccessBody, nil
	}}
	harness := newStage3Harness(t, []string{"primary", "secondary", "tertiary"}, map[string]provider.Adapter{
		"primary":   primary,
		"secondary": secondary,
		"tertiary":  tertiary,
	}, []reliability.ProviderKeySet{
		{ProviderID: "primary", Keys: []reliability.ProviderKey{{ID: "primary-key", Secret: "stage3-secret-one"}}},
		{ProviderID: "secondary", Keys: []reliability.ProviderKey{{ID: "secondary-key", Secret: "stage3-secret-two"}}},
		{ProviderID: "tertiary", Keys: []reliability.ProviderKey{{ID: "tertiary-key", Secret: "stage3-secret-three"}}},
	}, stage3RetryConfig(1))

	ctx, cancel := context.WithCancel(context.Background())
	request := validChatRequest().WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		harness.handler.ServeHTTP(response, request)
	}()

	select {
	case <-secondaryEntered:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("secondary provider was not reached")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled request did not stop")
	}

	if primary.calls.Load() != 1 || secondary.calls.Load() != 1 || tertiary.calls.Load() != 0 {
		t.Fatalf("provider calls = primary:%d secondary:%d tertiary:%d", primary.calls.Load(), secondary.calls.Load(), tertiary.calls.Load())
	}
	stage3AssertNoQueueOccupancy(t, harness.queues)
	for _, snapshot := range harness.breakers.Snapshots() {
		if snapshot.ProviderID != "primary" && (snapshot.State != reliability.StateClosed || snapshot.ConsecutiveFailures != 0) {
			t.Fatalf("breaker retained canceled request state: %#v", snapshot)
		}
	}
}

type stage3Harness struct {
	handler  http.Handler
	breakers *reliability.BreakerRegistry
	queues   *reliability.QueueRegistry
	keys     *reliability.ProviderKeyRegistry
}

func newStage3Harness(
	t *testing.T,
	providerIDs []string,
	configuredAdapters map[string]provider.Adapter,
	keySets []reliability.ProviderKeySet,
	retryConfig reliability.RetryConfig,
) stage3Harness {
	t.Helper()

	providers := make([]routing.Provider, 0, len(providerIDs))
	targets := make([]routing.Target, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		providers = append(providers, routing.Provider{
			ID:     providerID,
			Type:   provider.ProtocolOpenAICompatible,
			Models: []string{"demo-model"},
		})
		targets = append(targets, routing.Target{ProviderID: providerID})
	}
	routes, err := routing.New(routing.Definition{
		Providers: providers,
		Rules: []routing.Rule{{
			Model:      "demo-model",
			Candidates: targets,
		}},
	})
	if err != nil {
		t.Fatalf("routing.New() error = %v", err)
	}
	adapters, err := provider.NewAdapterRegistryFromAdapters(configuredAdapters)
	if err != nil {
		t.Fatalf("provider.NewAdapterRegistryFromAdapters() error = %v", err)
	}
	breakers, err := reliability.NewBreakerRegistry(providerIDs, reliability.BreakerConfig{
		FailureThreshold:  100,
		OpenDuration:      time.Second,
		HalfOpenMaxProbes: 1,
	})
	if err != nil {
		t.Fatalf("reliability.NewBreakerRegistry() error = %v", err)
	}
	queues, err := reliability.NewQueueRegistry(providerIDs, reliability.QueueConfig{
		MaxInFlight: 2,
		MaxWaiting:  8,
		WaitTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("reliability.NewQueueRegistry() error = %v", err)
	}
	keys, err := reliability.NewProviderKeyRegistry(providerIDs, keySets)
	if err != nil {
		t.Fatalf("reliability.NewProviderKeyRegistry() error = %v", err)
	}
	retry, err := reliability.NewRetryPolicy(retryConfig)
	if err != nil {
		t.Fatalf("reliability.NewRetryPolicy() error = %v", err)
	}

	return stage3Harness{
		handler: httpapi.NewRouter(
			adapters,
			testAccessController{},
			testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
			testResponseCache{},
			routes,
			breakers,
			queues,
			keys,
			retry,
			&recordingUsageEmitter{},
			emptyUsageReader{},
		),
		breakers: breakers,
		queues:   queues,
		keys:     keys,
	}
}

func stage3RetryConfig(maxAttempts int) reliability.RetryConfig {
	return reliability.RetryConfig{
		MaxAttempts:       maxAttempts,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2,
		JitterRatio:       0,
		RequestTimeout:    2 * time.Second,
		AttemptTimeout:    time.Second,
	}
}

type stage3Adapter struct {
	calls    atomic.Int32
	complete func(context.Context, provider.ChatInput, string) ([]byte, error)
}

func (*stage3Adapter) Authentication() provider.Authentication {
	return provider.AuthenticationAPIKey
}

func (adapter *stage3Adapter) Complete(ctx context.Context, input provider.ChatInput, apiKey string) ([]byte, error) {
	adapter.calls.Add(1)
	return adapter.complete(ctx, input, apiKey)
}

func stage3KeyNumber(apiKey string) int {
	switch apiKey {
	case "stage3-secret-one":
		return 1
	case "stage3-secret-two":
		return 2
	default:
		return 0
	}
}

func stage3KeyStates(registry *reliability.ProviderKeyRegistry) map[string]reliability.ProviderKeyState {
	states := make(map[string]reliability.ProviderKeyState)
	for _, snapshot := range registry.Snapshots() {
		states[snapshot.KeyID] = snapshot.State
	}
	return states
}

func stage3AssertNoQueueOccupancy(t *testing.T, registry *reliability.QueueRegistry) {
	t.Helper()
	for _, snapshot := range registry.Snapshots() {
		if snapshot.Active != 0 || snapshot.Waiting != 0 {
			t.Fatalf("queue retained request state: %#v", snapshot)
		}
	}
}

var stage3SuccessBody = []byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
