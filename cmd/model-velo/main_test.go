package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"model-velo/internal/apikey"
	"model-velo/internal/ratelimit"
	"model-velo/internal/responsecache"
)

func TestNewHTTPServerAddress(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "default", env: "", want: ":8080"},
		{name: "blank", env: "   ", want: ":8080"},
		{name: "configured", env: " 127.0.0.1:9090 ", want: "127.0.0.1:9090"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(httpAddressEnv, test.env)

			server := newConfiguredHTTPServer(t)
			if server.Addr != test.want {
				t.Fatalf("server address = %q, want %q", server.Addr, test.want)
			}
		})
	}
}

func TestNewHTTPServerDefaults(t *testing.T) {
	t.Setenv(httpAddressEnv, "")
	server := newConfiguredHTTPServer(t)

	if server.Handler == nil {
		t.Fatal("server handler is nil")
	}
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %s, want %s", server.ReadHeaderTimeout, 5*time.Second)
	}
	if server.ReadTimeout != 15*time.Second {
		t.Errorf("ReadTimeout = %s, want %s", server.ReadTimeout, 15*time.Second)
	}
	if server.WriteTimeout != 60*time.Second {
		t.Errorf("WriteTimeout = %s, want %s", server.WriteTimeout, 60*time.Second)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %s, want %s", server.IdleTimeout, 60*time.Second)
	}
}

func TestLoadShutdownTimeout(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", env: "", want: 10 * time.Second},
		{name: "configured", env: " 3s ", want: 3 * time.Second},
		{name: "invalid", env: "invalid", wantErr: true},
		{name: "non-positive", env: "0s", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(shutdownTimeoutEnv, test.env)

			got, err := loadShutdownTimeout()
			if test.wantErr {
				if err == nil {
					t.Fatalf("loadShutdownTimeout() = %s, nil; want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadShutdownTimeout() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("loadShutdownTimeout() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestLoadStartupConfig(t *testing.T) {
	setValidInfrastructureEnv(t)
	setValidRoutingEnv(t)
	t.Setenv(shutdownTimeoutEnv, "4s")

	got, err := loadStartupConfig()
	if err != nil {
		t.Fatalf("loadStartupConfig() error = %v", err)
	}
	if got.shutdownTimeout != 4*time.Second {
		t.Errorf("shutdown timeout = %s, want 4s", got.shutdownTimeout)
	}
	if got.infrastructure.Postgres.MaxOpenConns != 10 {
		t.Errorf("Postgres.MaxOpenConns = %d, want 10", got.infrastructure.Postgres.MaxOpenConns)
	}
	if got.infrastructure.Redis.Address != "localhost:6379" {
		t.Errorf("Redis.Address = %q, want localhost:6379", got.infrastructure.Redis.Address)
	}
	if len(got.apiKeySecurity.Pepper) < 32 {
		t.Errorf("API key pepper length = %d, want at least 32", len(got.apiKeySecurity.Pepper))
	}
	if got.rateLimit.Environment != "test" || got.rateLimit.MaxRequests != 60 {
		t.Errorf("rate limit config = %#v", got.rateLimit)
	}
	if got.responseCache.TTL != 5*time.Minute || got.responseCache.RouteVersion != "routes-v1" {
		t.Errorf("response cache config = %#v", got.responseCache)
	}
}

func TestLoadStartupConfigRequiresRouting(t *testing.T) {
	setValidInfrastructureEnv(t)
	setValidRoutingEnv(t)
	t.Setenv("MODEL_VELO_ROUTING_JSON", "")

	_, err := loadStartupConfig()
	if err == nil || !strings.Contains(err.Error(), "MODEL_VELO_ROUTING_JSON is required") {
		t.Fatalf("loadStartupConfig() error = %v, want missing routing error", err)
	}
}

func TestLoadStartupConfigRejectsInfrastructureBeforeListening(t *testing.T) {
	const secret = "must-not-leak"

	setValidInfrastructureEnv(t)
	t.Setenv("MODEL_VELO_POSTGRES_DSN", "postgres://model_velo:"+secret+"@localhost:5432/model_velo")
	t.Setenv("MODEL_VELO_REDIS_STARTUP_POLICY", "invalid")

	_, err := loadStartupConfig()
	if err == nil {
		t.Fatal("loadStartupConfig() error = nil, want error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("loadStartupConfig() error leaked DSN password: %v", err)
	}
}

func newConfiguredHTTPServer(t *testing.T) *http.Server {
	t.Helper()
	setValidInfrastructureEnv(t)
	setValidRoutingEnv(t)

	startup, err := loadStartupConfig()
	if err != nil {
		t.Fatalf("loadStartupConfig() error = %v", err)
	}
	server, err := newHTTPServer(
		mainTestAccessController{},
		mainTestRateLimiter{},
		mainTestResponseCache{},
		startup.routing,
		startup.adapters,
		startup.breakers,
		startup.queues,
		startup.providerKeys,
		startup.retry,
	)
	if err != nil {
		t.Fatalf("newHTTPServer() error = %v", err)
	}
	return server
}

func setValidRoutingEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MODEL_VELO_ROUTING_JSON", validRoutingJSON)
	t.Setenv("MODEL_VELO_PROVIDER_KEYS_JSON", validProviderKeysJSON)
}

const validRoutingJSON = `{"providers":[{"id":"upstream","vendor":"custom","type":"openai-compatible","base_url":"https://example.com","models":["*"]}],"routes":[{"model":"*","candidates":[{"provider":"upstream"}]}]}`

const validProviderKeysJSON = `{"providers":[{"provider_id":"upstream","keys":[{"id":"default","secret":"provider-test-key"}]}]}`

func setValidInfrastructureEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MODEL_VELO_POSTGRES_DSN", "postgres://model_velo:postgres-test-key@localhost:5432/model_velo?sslmode=disable")
	t.Setenv("MODEL_VELO_POSTGRES_MAX_OPEN_CONNS", "")
	t.Setenv("MODEL_VELO_POSTGRES_MAX_IDLE_CONNS", "")
	t.Setenv("MODEL_VELO_POSTGRES_CONNECT_TIMEOUT", "")
	t.Setenv("MODEL_VELO_POSTGRES_MAX_CONN_LIFETIME", "")
	t.Setenv("MODEL_VELO_POSTGRES_MAX_CONN_IDLE_TIME", "")
	t.Setenv("MODEL_VELO_API_KEY_PEPPER", "test-only-model-velo-api-key-pepper-32-bytes")
	t.Setenv("MODEL_VELO_REDIS_ADDR", "localhost:6379")
	t.Setenv("MODEL_VELO_REDIS_PASSWORD", "redis-test-key")
	t.Setenv("MODEL_VELO_REDIS_DB", "")
	t.Setenv("MODEL_VELO_REDIS_DIAL_TIMEOUT", "")
	t.Setenv("MODEL_VELO_REDIS_READ_TIMEOUT", "")
	t.Setenv("MODEL_VELO_REDIS_WRITE_TIMEOUT", "")
	t.Setenv("MODEL_VELO_REDIS_POOL_SIZE", "")
	t.Setenv("MODEL_VELO_REDIS_MIN_IDLE_CONNS", "")
	t.Setenv("MODEL_VELO_REDIS_POOL_TIMEOUT", "")
	t.Setenv("MODEL_VELO_REDIS_STARTUP_POLICY", "")
	t.Setenv("MODEL_VELO_ENVIRONMENT", "test")
	t.Setenv("MODEL_VELO_RATE_LIMIT_REQUESTS", "")
	t.Setenv("MODEL_VELO_RATE_LIMIT_WINDOW", "")
	t.Setenv("MODEL_VELO_RATE_LIMIT_FAILURE_POLICY", "")
	t.Setenv("MODEL_VELO_CACHE_TTL", "")
	t.Setenv("MODEL_VELO_CACHE_ROUTE_VERSION", "")
}

type mainTestAccessController struct{}

func (mainTestAccessController) Authenticate(context.Context, string) (apikey.Identity, error) {
	return apikey.Identity{TenantID: "tenant-test-id"}, nil
}

func (mainTestAccessController) AuthorizeModel(context.Context, string, string) error {
	return nil
}

type mainTestRateLimiter struct{}

func (mainTestRateLimiter) Allow(context.Context, string, string) (ratelimit.Decision, error) {
	return ratelimit.Decision{Allowed: true}, nil
}

type mainTestResponseCache struct{}

func (mainTestResponseCache) Lookup(context.Context, string, string, []byte) (responsecache.Result, error) {
	return responsecache.Result{Status: responsecache.StatusMiss}, nil
}

func (mainTestResponseCache) Store(context.Context, string, string, []byte, []byte) error {
	return nil
}
