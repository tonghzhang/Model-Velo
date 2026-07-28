package observability

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"model-velo/internal/usage"
)

func TestMetricsAreBoundedAndProtected(t *testing.T) {
	metrics := NewMetrics()
	if err := metrics.RegisterUsageWorker(fakeUsageWorker{}); err != nil {
		t.Fatal(err)
	}
	redisClient := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() {
		_ = redisClient.Close()
	})
	if err := metrics.RegisterDependencies(&sql.DB{}, redisClient); err != nil {
		t.Fatal(err)
	}
	finish := metrics.BeginRequest()
	finish("/v1/test", http.MethodPost, http.StatusOK, false, 20*time.Millisecond)
	metrics.HTTPError("/v1/test", http.StatusServiceUnavailable, "gateway_overloaded")
	metrics.RequestStage("authentication", "accepted", "", time.Millisecond)
	metrics.Authentication("accepted")
	metrics.RateLimit("allowed")
	metrics.Cache("lookup", "hit")
	metrics.ProviderAttempt("provider-a", "success", "", time.Millisecond, false)
	metrics.Fallbacks(1, "success")
	metrics.UsageDelivery("finalize", "published")
	metrics.Quota("allowed")

	const token = "0123456789abcdef0123456789abcdef"
	handler := metrics.Handler(token)
	for name, expectation := range map[string]struct {
		values []string
		status int
	}{
		"missing":   {status: http.StatusUnauthorized},
		"wrong":     {values: []string{"Bearer wrong"}, status: http.StatusUnauthorized},
		"duplicate": {values: []string{"Bearer " + token, "Bearer " + token}, status: http.StatusUnauthorized},
		"accepted":  {values: []string{"Bearer " + token}, status: http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			for _, value := range expectation.values {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != expectation.status {
				t.Fatalf("status = %d, want %d", response.Code, expectation.status)
			}
			if name != "accepted" {
				return
			}
			payload := response.Body.String()
			for _, metricName := range []string{
				"model_velo_http_requests_total",
				"model_velo_http_errors_total",
				"model_velo_request_stage_duration_seconds",
				"model_velo_provider_attempts_total",
				"model_velo_authentication_total",
				"model_velo_usage_delivery_total",
				"model_velo_usage_worker_pending",
				"model_velo_quota_decisions_total",
				"model_velo_postgres_connections",
				"model_velo_redis_pool_connections",
				"go_goroutines",
			} {
				if !strings.Contains(payload, metricName) {
					t.Errorf("scrape does not contain %s", metricName)
				}
			}
			for _, forbidden := range []string{
				"request_id=", "tenant_id=", "api_key=",
			} {
				if strings.Contains(payload, forbidden) {
					t.Errorf("scrape contains high-cardinality label %q", forbidden)
				}
			}
		})
	}
}

type fakeUsageWorker struct{}

func (fakeUsageWorker) Stats() usage.WorkerStats {
	return usage.WorkerStats{Read: 2, Stored: 1, Duplicates: 1}
}

func (fakeUsageWorker) Pending(context.Context) (int64, error) {
	return 3, nil
}
