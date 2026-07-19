package ratelimit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"model-velo/internal/config"
)

func TestLimiterFailurePolicy(t *testing.T) {
	tests := []struct {
		name       string
		policy     config.RateLimitFailurePolicy
		wantBypass bool
		wantErr    error
	}{
		{name: "fail open", policy: config.RateLimitFailOpen, wantBypass: true},
		{name: "fail closed", policy: config.RateLimitFailClosed, wantErr: ErrUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := unavailableRedisClient()
			t.Cleanup(func() { _ = client.Close() })

			limiter := newTestLimiter(t, client, test.policy)
			decision, err := limiter.Allow(context.Background(), "tenant-1", "demo-model")
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Allow() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Allow() error = %v", err)
			}
			if !decision.Allowed || decision.Bypassed != test.wantBypass {
				t.Fatalf("Allow() decision = %#v", decision)
			}
		})
	}
}

func TestLimiterPropagatesCanceledContext(t *testing.T) {
	client := unavailableRedisClient()
	t.Cleanup(func() { _ = client.Close() })
	limiter := newTestLimiter(t, client, config.RateLimitFailOpen)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	decision, err := limiter.Allow(ctx, "tenant-1", "demo-model")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Allow() error = %v, want context.Canceled", err)
	}
	if decision.Allowed {
		t.Fatalf("Allow() decision = %#v, want rejected cancellation", decision)
	}
}

func TestLimiterKeyIsIsolatedAndContainsNoInputs(t *testing.T) {
	client := unavailableRedisClient()
	t.Cleanup(func() { _ = client.Close() })
	limiter := newTestLimiter(t, client, config.RateLimitFailClosed)

	first, err := limiter.key("tenant-secret-value", "model/private")
	if err != nil {
		t.Fatalf("key() error = %v", err)
	}
	second, err := limiter.key("tenant-secret-value", "model/other")
	if err != nil {
		t.Fatalf("key() error = %v", err)
	}
	if first == second {
		t.Fatal("different models produced the same key")
	}
	third, err := limiter.key("another-tenant", "model/private")
	if err != nil {
		t.Fatalf("key() error = %v", err)
	}
	if first == third {
		t.Fatal("different tenants produced the same key")
	}
	if !strings.Contains(first, ":test:") {
		t.Errorf("key = %q, want environment namespace", first)
	}
	if strings.Contains(first, "tenant-secret-value") || strings.Contains(first, "model/private") || strings.Contains(first, "mvl_") {
		t.Errorf("key leaked an unhashed input: %q", first)
	}
}

func TestParseDecision(t *testing.T) {
	decision, err := parseDecision([]any{int64(0), int64(0), int64(1_800_000_000), int64(12)}, 60)
	if err != nil {
		t.Fatalf("parseDecision() error = %v", err)
	}
	if decision.Allowed || decision.Limit != 60 || decision.Remaining != 0 || decision.RetryAfterSeconds != 12 {
		t.Fatalf("parseDecision() = %#v", decision)
	}
}

func newTestLimiter(t *testing.T, client *goredis.Client, policy config.RateLimitFailurePolicy) *Limiter {
	t.Helper()

	limiter, err := New(client, config.RateLimit{
		Environment:   "test",
		MaxRequests:   60,
		Window:        time.Minute,
		FailurePolicy: policy,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return limiter
}

func unavailableRedisClient() *goredis.Client {
	client := goredis.NewClient(&goredis.Options{Addr: "unused:6379"})
	client.AddHook(failingRedisHook{})
	return client
}

type failingRedisHook struct{}

func (failingRedisHook) DialHook(next goredis.DialHook) goredis.DialHook {
	return next
}

func (failingRedisHook) ProcessHook(goredis.ProcessHook) goredis.ProcessHook {
	return func(context.Context, goredis.Cmder) error {
		return errors.New("forced Redis command failure")
	}
}

func (failingRedisHook) ProcessPipelineHook(goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(context.Context, []goredis.Cmder) error {
		return errors.New("forced Redis pipeline failure")
	}
}
