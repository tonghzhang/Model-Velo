package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"model-velo/internal/httpapi"
	"model-velo/internal/provider"
	"model-velo/internal/ratelimit"
	"model-velo/internal/reliability"
	"model-velo/internal/routing"
)

func BenchmarkChatCompletions(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		_, _ = io.Copy(io.Discard, request.Body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(
			writer,
			`{"id":"chatcmpl-bench","object":"chat.completion","created":1,`+
				`"model":"bench-model","choices":[{"index":0,"message":`+
				`{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}`,
		)
	}))
	b.Cleanup(upstream.Close)

	router := benchmarkRouter(b, upstream.URL)
	body := `{"model":"demo-model","messages":[{"role":"user","content":"hello"}]}`
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		request := httptest.NewRequest(
			http.MethodPost, "/v1/chat/completions", strings.NewReader(body),
		)
		request.Header.Set("Authorization", "Bearer benchmark-key")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			b.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	}
}

func benchmarkRouter(b *testing.B, baseURL string) http.Handler {
	b.Helper()
	gin.SetMode(gin.ReleaseMode)
	adapters, err := provider.NewAdapterRegistry([]provider.AdapterConfig{{
		ProviderID: "upstream", Protocol: provider.ProtocolOpenAICompatible,
		BaseURL: baseURL, HTTP: provider.DefaultHTTPConfig(),
	}})
	if err != nil {
		b.Fatal(err)
	}
	routes, err := routing.New(routing.Definition{
		Providers: []routing.Provider{{
			ID: "upstream", Type: provider.ProtocolOpenAICompatible,
			BaseURL: baseURL, Models: []string{"*"},
		}},
		Rules: []routing.Rule{{
			Model: "*", Candidates: []routing.Target{{ProviderID: "upstream"}},
		}},
	})
	if err != nil {
		b.Fatal(err)
	}
	breakers, err := reliability.NewBreakerRegistry(
		[]string{"upstream"}, reliability.DefaultBreakerConfig(),
	)
	if err != nil {
		b.Fatal(err)
	}
	queues, err := reliability.NewQueueRegistry(
		[]string{"upstream"}, reliability.DefaultQueueConfig(),
	)
	if err != nil {
		b.Fatal(err)
	}
	keys, err := reliability.NewProviderKeyRegistry(
		[]string{"upstream"}, []reliability.ProviderKeySet{{
			ProviderID: "upstream",
			Keys:       []reliability.ProviderKey{{ID: "bench", Secret: "bench-secret"}},
		}},
	)
	if err != nil {
		b.Fatal(err)
	}
	retryConfig := reliability.DefaultRetryConfig()
	retryConfig.MaxAttempts = 1
	retryConfig.RequestTimeout = 5 * time.Second
	retryConfig.AttemptTimeout = 5 * time.Second
	retries, err := reliability.NewRetryRegistry(
		[]string{"upstream"},
		map[string]reliability.RetryConfig{"upstream": retryConfig},
	)
	if err != nil {
		b.Fatal(err)
	}
	return httpapi.NewRouter(
		adapters,
		testAccessController{},
		testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
		testResponseCache{},
		routes,
		breakers,
		queues,
		keys,
		retries,
		&recordingUsageEmitter{},
		&recordingUsageReader{},
		httpapi.WithRequestLogger(
			slog.New(slog.NewTextHandler(io.Discard, nil)),
		),
	)
}
