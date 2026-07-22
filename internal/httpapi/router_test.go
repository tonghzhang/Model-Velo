package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"model-velo/internal/apikey"
	"model-velo/internal/httpapi"
	"model-velo/internal/provider"
	"model-velo/internal/ratelimit"
	"model-velo/internal/reliability"
	"model-velo/internal/responsecache"
	"model-velo/internal/routing"
)

func TestHealthz(t *testing.T) {
	router := newTestRouter(t, "https://example.com", time.Second)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}

	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json; charset=utf-8")
	}

	if got := response.Body.String(); got != `{"status":"ok"}` {
		t.Errorf("body = %q, want %q", got, `{"status":"ok"}`)
	}
}

func newTestRouter(t *testing.T, baseURL string, timeout time.Duration) http.Handler {
	t.Helper()

	client := newTestCompatibleAdapter(t, baseURL)

	return newSingleProviderTestRouterWithRetry(t, client, testAccessController{}, testRateLimiter{
		decision: ratelimit.Decision{
			Allowed:     true,
			Limit:       60,
			Remaining:   59,
			ResetAtUnix: 1_800_000_000,
		},
	}, testResponseCache{}, nil, nil, nil, singleAttemptRetryPolicy(t, timeout))
}

func newSingleProviderTestRouter(
	t *testing.T,
	adapter provider.Adapter,
	access httpapi.AccessController,
	limiter httpapi.RateLimiter,
	cache httpapi.ResponseCache,
	routes *routing.Router,
	breaker *reliability.Breaker,
	queues *reliability.QueueRegistry,
) http.Handler {
	t.Helper()
	return newSingleProviderTestRouterWithRetry(
		t,
		adapter,
		access,
		limiter,
		cache,
		routes,
		breaker,
		queues,
		singleAttemptRetryPolicy(t, time.Second),
	)
}

func newSingleProviderTestRouterWithRetry(
	t *testing.T,
	adapter provider.Adapter,
	access httpapi.AccessController,
	limiter httpapi.RateLimiter,
	cache httpapi.ResponseCache,
	routes *routing.Router,
	breaker *reliability.Breaker,
	queues *reliability.QueueRegistry,
	retry *reliability.RetryPolicy,
) http.Handler {
	t.Helper()

	const providerID = "upstream"
	var err error
	if routes == nil {
		routes, err = routing.New(singleProviderTestDefinition(providerID))
		if err != nil {
			t.Fatalf("routing.New() error = %v", err)
		}
	}
	if breaker == nil {
		breaker, err = reliability.NewBreaker(providerID, reliability.DefaultBreakerConfig())
		if err != nil {
			t.Fatalf("reliability.NewBreaker() error = %v", err)
		}
	}
	if queues == nil {
		queues, err = reliability.NewQueueRegistry([]string{providerID}, reliability.DefaultQueueConfig())
		if err != nil {
			t.Fatalf("reliability.NewQueueRegistry() error = %v", err)
		}
	}

	adapters, err := provider.NewAdapterRegistryFromAdapters(map[string]provider.Adapter{providerID: adapter})
	if err != nil {
		t.Fatalf("provider.NewAdapterRegistryFromAdapters() error = %v", err)
	}
	breakers, err := reliability.NewBreakerRegistryFromBreakers(breaker)
	if err != nil {
		t.Fatalf("reliability.NewBreakerRegistryFromBreakers() error = %v", err)
	}
	var providerKeys *reliability.ProviderKeyRegistry
	if adapter.Authentication() == provider.AuthenticationAPIKey {
		providerKeys, err = reliability.NewProviderKeyRegistry(
			[]string{providerID},
			[]reliability.ProviderKeySet{{
				ProviderID: providerID,
				Keys: []reliability.ProviderKey{{
					ID:     "test-key",
					Secret: "provider-test-key",
				}},
			}},
		)
		if err != nil {
			t.Fatalf("reliability.NewProviderKeyRegistry() error = %v", err)
		}
	}

	return httpapi.NewRouter(
		adapters,
		access,
		limiter,
		cache,
		routes,
		breakers,
		queues,
		providerKeys,
		retry,
	)
}

func singleAttemptRetryPolicy(t *testing.T, attemptTimeout time.Duration) *reliability.RetryPolicy {
	t.Helper()
	config := reliability.DefaultRetryConfig()
	config.MaxAttempts = 1
	config.AttemptTimeout = attemptTimeout
	if config.RequestTimeout < attemptTimeout {
		config.RequestTimeout = attemptTimeout
	}
	policy, err := reliability.NewRetryPolicy(config)
	if err != nil {
		t.Fatalf("reliability.NewRetryPolicy() error = %v", err)
	}
	return policy
}

func newTestCompatibleAdapter(t *testing.T, baseURL string) provider.Adapter {
	t.Helper()

	adapter, err := provider.NewAdapter(provider.AdapterConfig{
		Protocol: provider.ProtocolOpenAICompatible,
		BaseURL:  baseURL,
	})
	if err != nil {
		t.Fatalf("provider.NewAdapter() error = %v", err)
	}
	return adapter
}

func singleProviderTestDefinition(providerID string) routing.Definition {
	return routing.Definition{
		Providers: []routing.Provider{{
			ID:     providerID,
			Type:   provider.ProtocolOpenAICompatible,
			Models: []string{"*"},
		}},
		Rules: []routing.Rule{{
			Model:      "*",
			Candidates: []routing.Target{{ProviderID: providerID}},
		}},
	}
}

type testAccessController struct {
	identity        apikey.Identity
	authenticateErr error
	authorizeErr    error
	onAuthenticate  func()
	onAuthorize     func(tenantID, model string)
}

func (access testAccessController) Authenticate(context.Context, string) (apikey.Identity, error) {
	if access.onAuthenticate != nil {
		access.onAuthenticate()
	}
	if access.authenticateErr != nil {
		return apikey.Identity{}, access.authenticateErr
	}
	if access.identity.TenantID != "" {
		return access.identity, nil
	}
	return apikey.Identity{TenantID: "tenant-test-id", APIKeyID: "api-key-test-id", KeyPrefix: "mvl_test"}, nil
}

func (access testAccessController) AuthorizeModel(_ context.Context, tenantID, model string) error {
	if access.onAuthorize != nil {
		access.onAuthorize(tenantID, model)
	}
	return access.authorizeErr
}

type testRateLimiter struct {
	decision ratelimit.Decision
	err      error
	onAllow  func(tenantID, model string)
}

func (limiter testRateLimiter) Allow(_ context.Context, tenantID, model string) (ratelimit.Decision, error) {
	if limiter.onAllow != nil {
		limiter.onAllow(tenantID, model)
	}
	return limiter.decision, limiter.err
}

type testResponseCache struct {
	result    responsecache.Result
	lookupErr error
	storeErr  error
	onLookup  func(tenantID, model string, requestBody []byte)
	onStore   func(tenantID, model string, requestBody, responseBody []byte)
}

func (cache testResponseCache) Lookup(
	_ context.Context,
	tenantID, model string,
	requestBody []byte,
) (responsecache.Result, error) {
	if cache.onLookup != nil {
		cache.onLookup(tenantID, model, requestBody)
	}
	if cache.result.Status == "" {
		cache.result.Status = responsecache.StatusMiss
	}
	return cache.result, cache.lookupErr
}

func (cache testResponseCache) Store(
	_ context.Context,
	tenantID, model string,
	requestBody, responseBody []byte,
) error {
	if cache.onStore != nil {
		cache.onStore(tenantID, model, requestBody, responseBody)
	}
	return cache.storeErr
}
