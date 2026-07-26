package config

import (
	"testing"
	"time"
)

func TestLoadRateLimitDefaults(t *testing.T) {
	t.Setenv(environmentEnv, " Development ")
	t.Setenv(rateLimitRequestsEnv, "")
	t.Setenv(rateLimitWindowEnv, "")
	t.Setenv(rateLimitFailureEnv, "")

	got, err := LoadRateLimit()
	if err != nil {
		t.Fatalf("LoadRateLimit() error = %v", err)
	}
	if got.Environment != "development" {
		t.Errorf("Environment = %q, want development", got.Environment)
	}
	if got.MaxRequests != 6_000 || got.Window != time.Minute {
		t.Errorf("quota = %d/%s, want 6000/1m", got.MaxRequests, got.Window)
	}
	if got.FailurePolicy != RateLimitFailClosed {
		t.Errorf("FailurePolicy = %q, want %q", got.FailurePolicy, RateLimitFailClosed)
	}
}

func TestLoadRateLimitConfigured(t *testing.T) {
	t.Setenv(environmentEnv, "prod-cn")
	t.Setenv(rateLimitRequestsEnv, "250")
	t.Setenv(rateLimitWindowEnv, "30s")
	t.Setenv(rateLimitFailureEnv, " FAIL-OPEN ")

	got, err := LoadRateLimit()
	if err != nil {
		t.Fatalf("LoadRateLimit() error = %v", err)
	}
	if got.Environment != "prod-cn" || got.MaxRequests != 250 || got.Window != 30*time.Second {
		t.Errorf("RateLimit = %#v", got)
	}
	if got.FailurePolicy != RateLimitFailOpen {
		t.Errorf("FailurePolicy = %q, want %q", got.FailurePolicy, RateLimitFailOpen)
	}
}

func TestLoadRateLimitRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "missing environment", env: environmentEnv, value: ""},
		{name: "unsafe environment", env: environmentEnv, value: "prod:blue"},
		{name: "zero requests", env: rateLimitRequestsEnv, value: "0"},
		{name: "too many requests", env: rateLimitRequestsEnv, value: "1000001"},
		{name: "window below minimum", env: rateLimitWindowEnv, value: "500ms"},
		{name: "window above maximum", env: rateLimitWindowEnv, value: "25h"},
		{name: "unknown failure policy", env: rateLimitFailureEnv, value: "optional"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(environmentEnv, "test")
			t.Setenv(rateLimitRequestsEnv, "60")
			t.Setenv(rateLimitWindowEnv, "1m")
			t.Setenv(rateLimitFailureEnv, "fail-closed")
			t.Setenv(test.env, test.value)

			if _, err := LoadRateLimit(); err == nil {
				t.Fatal("LoadRateLimit() error = nil, want error")
			}
		})
	}
}
