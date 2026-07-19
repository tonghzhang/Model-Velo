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
	"model-velo/internal/responsecache"
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

	client, err := provider.NewClient(baseURL, "provider-test-key", timeout)
	if err != nil {
		t.Fatalf("provider.NewClient() error = %v", err)
	}

	return httpapi.NewRouter(client, testAccessController{}, testRateLimiter{
		decision: ratelimit.Decision{
			Allowed:     true,
			Limit:       60,
			Remaining:   59,
			ResetAtUnix: 1_800_000_000,
		},
	}, testResponseCache{})
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
