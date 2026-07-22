package httpapi_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"model-velo/internal/apikey"
	"model-velo/internal/config"
	"model-velo/internal/httpapi"
	"model-velo/internal/ratelimit"
	"model-velo/internal/responsecache"
)

const (
	redisTestAddressEnv  = "MODEL_VELO_REDIS_TEST_ADDR"
	redisTestPasswordEnv = "MODEL_VELO_REDIS_TEST_PASSWORD"
	redisTestDBEnv       = "MODEL_VELO_REDIS_TEST_DB"
)

func TestRedisRateLimitHTTPFlow(t *testing.T) {
	redisClient := openRedisIntegrationClient(t)
	environment := redisTestEnvironment(t)
	cleanupRedisRateLimitKeys(t, redisClient, environment)

	limiter := newRedisIntegrationLimiter(t, redisClient, environment, 2, time.Second)
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(upstream.Close)

	providerClient := newTestCompatibleAdapter(t, upstream.URL)
	router := newSingleProviderTestRouter(t, providerClient, testAccessController{}, limiter, testResponseCache{}, nil, nil, nil)

	first := performChatRequest(router)
	assertRateLimitResponse(t, first, http.StatusOK, "2", "1")
	second := performChatRequest(router)
	assertRateLimitResponse(t, second, http.StatusOK, "2", "0")
	denied := performChatRequest(router)
	assertRateLimitResponse(t, denied, http.StatusTooManyRequests, "2", "0")
	if code := responseErrorCode(t, denied); code != "rate_limit_exceeded" {
		t.Errorf("error code = %q, want rate_limit_exceeded", code)
	}
	retryAfter, err := strconv.ParseInt(denied.Header().Get("Retry-After"), 10, 64)
	if err != nil || retryAfter < 1 {
		t.Errorf("Retry-After = %q, want positive integer seconds", denied.Header().Get("Retry-After"))
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream calls after rejection = %d, want 2", upstreamCalls.Load())
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		time.Sleep(25 * time.Millisecond)
		recovered := performChatRequest(router)
		if recovered.Code == http.StatusOK {
			assertRateLimitResponse(t, recovered, http.StatusOK, "2", "1")
			break
		}
		if recovered.Code != http.StatusTooManyRequests {
			t.Fatalf("window recovery status = %d; body = %s", recovered.Code, recovered.Body.String())
		}
		if time.Now().After(deadline) {
			t.Fatal("rate limit window did not recover within 3s")
		}
	}
	if upstreamCalls.Load() != 3 {
		t.Fatalf("upstream calls after recovery = %d, want 3", upstreamCalls.Load())
	}
}

func TestRedisRateLimitTenantAndModelIsolation(t *testing.T) {
	redisClient := openRedisIntegrationClient(t)
	environment := redisTestEnvironment(t)
	cleanupRedisRateLimitKeys(t, redisClient, environment)
	limiter := newRedisIntegrationLimiter(t, redisClient, environment, 1, 5*time.Second)

	assertAllowed := func(tenantID, model string, want bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		decision, err := limiter.Allow(ctx, tenantID, model)
		if err != nil {
			t.Fatalf("Allow(%q, %q) error = %v", tenantID, model, err)
		}
		if decision.Allowed != want {
			t.Fatalf("Allow(%q, %q).Allowed = %t, want %t", tenantID, model, decision.Allowed, want)
		}
	}

	assertAllowed("tenant-a", "model-a", true)
	assertAllowed("tenant-a", "model-a", false)
	assertAllowed("tenant-b", "model-a", true)
	assertAllowed("tenant-a", "model-b", true)
}

func TestRedisRateLimitAcrossInstancesIsAtomic(t *testing.T) {
	firstClient := openRedisIntegrationClient(t)
	secondClient := openRedisIntegrationClient(t)
	environment := redisTestEnvironment(t)
	cleanupRedisRateLimitKeys(t, firstClient, environment)

	const (
		quota       = int64(25)
		competitors = 200
	)
	firstLimiter := newRedisIntegrationLimiter(t, firstClient, environment, quota, 5*time.Second)
	secondLimiter := newRedisIntegrationLimiter(t, secondClient, environment, quota, 5*time.Second)

	start := make(chan struct{})
	errorsFound := make(chan error, competitors)
	var allowed atomic.Int64
	var waitGroup sync.WaitGroup
	for index := 0; index < competitors; index++ {
		limiter := firstLimiter
		if index%2 == 1 {
			limiter = secondLimiter
		}
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			decision, err := limiter.Allow(ctx, "shared-tenant", "shared-model")
			if err != nil {
				errorsFound <- err
				return
			}
			if decision.Allowed {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errorsFound)

	for err := range errorsFound {
		t.Errorf("concurrent Allow() error = %v", err)
	}
	if got := allowed.Load(); got != quota {
		t.Fatalf("allowed requests = %d, want exactly quota %d", got, quota)
	}
}

func TestRedisResponseCacheHTTPFlow(t *testing.T) {
	redisClient := openRedisIntegrationClient(t)
	environment := redisTestEnvironment(t)
	cleanupRedisResponseCacheKeys(t, redisClient, environment)

	cache, err := responsecache.New(redisClient, environment, "integration-v1", time.Second)
	if err != nil {
		t.Fatalf("responsecache.New() error = %v", err)
	}

	var upstreamCalls atomic.Int32
	var failNext atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if failNext.Swap(false) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"temporary"}}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":"upstream-%d","choices":[{"message":{"role":"assistant","content":"ok"}}]}`, call)
	}))
	t.Cleanup(upstream.Close)

	providerClient := newTestCompatibleAdapter(t, upstream.URL)
	newRouter := func(tenantID string, responseCache httpapi.ResponseCache) http.Handler {
		return newSingleProviderTestRouter(
			t,
			providerClient,
			testAccessController{identity: apikey.Identity{TenantID: tenantID, APIKeyID: "cache-test-key"}},
			testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
			responseCache,
			nil,
			nil,
			nil,
		)
	}
	router := newRouter("tenant-cache-a", cache)

	original := `{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"temperature":0.2}`
	reordered := `{"temperature":0.2,"messages":[{"content":"hello","role":"user"}],"model":"demo-model"}`
	first := performChatRequestBody(router, original)
	assertCacheResponse(t, first, http.StatusOK, "MISS")
	firstBody := first.Body.String()
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls after first miss = %d, want 1", upstreamCalls.Load())
	}

	hit := performChatRequestBody(router, reordered)
	assertCacheResponse(t, hit, http.StatusOK, "HIT")
	if hit.Body.String() != firstBody {
		t.Errorf("cache hit body = %s, want %s", hit.Body.String(), firstBody)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls after hit = %d, want 1", upstreamCalls.Load())
	}

	parameterMiss := performChatRequestBody(router, strings.Replace(original, "0.2", "0.3", 1))
	assertCacheResponse(t, parameterMiss, http.StatusOK, "MISS")
	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream calls after parameter change = %d, want 2", upstreamCalls.Load())
	}

	tenantMiss := performChatRequestBody(newRouter("tenant-cache-b", cache), original)
	assertCacheResponse(t, tenantMiss, http.StatusOK, "MISS")
	if upstreamCalls.Load() != 3 {
		t.Fatalf("upstream calls after tenant change = %d, want 3", upstreamCalls.Load())
	}

	time.Sleep(1200 * time.Millisecond)
	expired := performChatRequestBody(router, original)
	assertCacheResponse(t, expired, http.StatusOK, "MISS")
	if upstreamCalls.Load() != 4 {
		t.Fatalf("upstream calls after TTL expiry = %d, want 4", upstreamCalls.Load())
	}

	errorRequest := strings.Replace(original, "0.2", "0.9", 1)
	failNext.Store(true)
	failed := performChatRequestBody(router, errorRequest)
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("failed upstream status = %d, want %d; body = %s", failed.Code, http.StatusBadGateway, failed.Body.String())
	}
	recovered := performChatRequestBody(router, errorRequest)
	assertCacheResponse(t, recovered, http.StatusOK, "MISS")
	if upstreamCalls.Load() != 6 {
		t.Fatalf("upstream calls after non-cached error = %d, want 6", upstreamCalls.Load())
	}

	closedRedisClient := goredis.NewClient(redisClient.Options())
	closedCache, err := responsecache.New(closedRedisClient, environment, "integration-v1", time.Second)
	if err != nil {
		t.Fatalf("responsecache.New(closed client) error = %v", err)
	}
	if err := closedRedisClient.Close(); err != nil {
		t.Fatalf("close failure-injection Redis client: %v", err)
	}
	bypassed := performChatRequestBody(
		newRouter("tenant-cache-a", closedCache),
		strings.Replace(original, "0.2", "0.8", 1),
	)
	assertCacheResponse(t, bypassed, http.StatusOK, "BYPASS")
	if upstreamCalls.Load() != 7 {
		t.Fatalf("upstream calls after Redis failure = %d, want 7", upstreamCalls.Load())
	}
}

func openRedisIntegrationClient(t *testing.T) *goredis.Client {
	t.Helper()

	address := strings.TrimSpace(os.Getenv(redisTestAddressEnv))
	if address == "" {
		t.Skipf("set %s to run real Redis integration tests", redisTestAddressEnv)
	}
	db := 0
	if rawDB := strings.TrimSpace(os.Getenv(redisTestDBEnv)); rawDB != "" {
		parsed, err := strconv.Atoi(rawDB)
		if err != nil || parsed < 0 {
			t.Fatalf("%s must be a non-negative integer", redisTestDBEnv)
		}
		db = parsed
	}

	client := goredis.NewClient(&goredis.Options{
		Addr:                  address,
		Password:              os.Getenv(redisTestPasswordEnv),
		DB:                    db,
		DialTimeout:           time.Second,
		ReadTimeout:           time.Second,
		WriteTimeout:          time.Second,
		PoolTimeout:           time.Second,
		ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close Redis integration client: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping Redis integration server: %v", err)
	}
	return client
}

func newRedisIntegrationLimiter(
	t *testing.T,
	client *goredis.Client,
	environment string,
	maxRequests int64,
	window time.Duration,
) *ratelimit.Limiter {
	t.Helper()

	limiter, err := ratelimit.New(client, config.RateLimit{
		Environment:   environment,
		MaxRequests:   maxRequests,
		Window:        window,
		FailurePolicy: config.RateLimitFailClosed,
	})
	if err != nil {
		t.Fatalf("ratelimit.New() error = %v", err)
	}
	return limiter
}

func redisTestEnvironment(t *testing.T) string {
	t.Helper()

	randomBytes := make([]byte, 6)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatalf("generate Redis test namespace: %v", err)
	}
	return "it-" + hex.EncodeToString(randomBytes)
}

func cleanupRedisRateLimitKeys(t *testing.T, client *goredis.Client, environment string) {
	t.Helper()

	pattern := fmt.Sprintf("model-velo:rate-limit:v1:%s:*", environment)
	cleanupRedisKeys(t, client, pattern)
}

func cleanupRedisResponseCacheKeys(t *testing.T, client *goredis.Client, environment string) {
	t.Helper()

	pattern := fmt.Sprintf("model-velo:response-cache:v1:%s:*", environment)
	cleanupRedisKeys(t, client, pattern)
}

func cleanupRedisKeys(t *testing.T, client *goredis.Client, pattern string) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		var cursor uint64
		for {
			keys, nextCursor, err := client.Scan(ctx, cursor, pattern, 100).Result()
			if err != nil {
				t.Errorf("scan Redis integration keys: %v", err)
				return
			}
			if len(keys) > 0 {
				if err := client.Del(ctx, keys...).Err(); err != nil {
					t.Errorf("delete Redis integration keys: %v", err)
					return
				}
			}
			cursor = nextCursor
			if cursor == 0 {
				return
			}
		}
	})
}

func performChatRequest(router http.Handler) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, validChatRequest())
	return response
}

func performChatRequestBody(router http.Handler, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer model-velo-integration-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertCacheResponse(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantCacheStatus string) {
	t.Helper()

	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, wantStatus, response.Body.String())
	}
	if got := response.Header().Get("X-Model-Velo-Cache"); got != wantCacheStatus {
		t.Errorf("X-Model-Velo-Cache = %q, want %q", got, wantCacheStatus)
	}
}

func assertRateLimitResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantLimit string,
	wantRemaining string,
) {
	t.Helper()

	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, wantStatus, response.Body.String())
	}
	if got := response.Header().Get("X-RateLimit-Limit"); got != wantLimit {
		t.Errorf("X-RateLimit-Limit = %q, want %q", got, wantLimit)
	}
	if got := response.Header().Get("X-RateLimit-Remaining"); got != wantRemaining {
		t.Errorf("X-RateLimit-Remaining = %q, want %q", got, wantRemaining)
	}
	resetAt, err := strconv.ParseInt(response.Header().Get("X-RateLimit-Reset"), 10, 64)
	if err != nil || resetAt <= time.Now().Unix() {
		t.Errorf("X-RateLimit-Reset = %q, want a future Unix timestamp", response.Header().Get("X-RateLimit-Reset"))
	}
}
