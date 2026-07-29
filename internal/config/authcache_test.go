package config

import (
	"testing"
	"time"
)

func TestLoadAuthCacheDefaultsAndValidation(t *testing.T) {
	t.Setenv(environmentEnv, "test")
	for _, name := range []string{
		authCacheEnabledEnv,
		authCacheL1MaxEntriesEnv,
		authCacheL1TTLEnv,
		authCacheL2TTLEnv,
		authCacheKeyPrefixEnv,
		authCacheInvalidationChannelEnv,
	} {
		t.Setenv(name, "")
	}
	settings, err := LoadAuthCache()
	if err != nil {
		t.Fatalf("LoadAuthCache() error = %v", err)
	}
	if !settings.Enabled ||
		settings.L1MaxEntries != defaultAuthCacheL1MaxEntries ||
		settings.L1TTL != 15*time.Second ||
		settings.L2TTL != 30*time.Second ||
		settings.KeyPrefix != "model-velo:test:auth:v1" ||
		settings.InvalidationChannel != "model-velo:test:auth:v1:invalidate" {
		t.Fatalf("LoadAuthCache() = %#v", settings)
	}

	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "bad enabled", env: authCacheEnabledEnv, value: "sometimes"},
		{name: "zero entries", env: authCacheL1MaxEntriesEnv, value: "0"},
		{name: "too many entries", env: authCacheL1MaxEntriesEnv, value: "1000001"},
		{name: "short L1", env: authCacheL1TTLEnv, value: "500ms"},
		{name: "L2 shorter than L1", env: authCacheL2TTLEnv, value: "5s"},
		{name: "unsafe prefix", env: authCacheKeyPrefixEnv, value: "auth cache"},
		{name: "same channel", env: authCacheInvalidationChannelEnv, value: "custom"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(environmentEnv, "test")
			t.Setenv(authCacheEnabledEnv, "")
			t.Setenv(authCacheL1MaxEntriesEnv, "")
			t.Setenv(authCacheL1TTLEnv, "")
			t.Setenv(authCacheL2TTLEnv, "")
			t.Setenv(authCacheKeyPrefixEnv, "")
			t.Setenv(authCacheInvalidationChannelEnv, "")
			if test.name == "same channel" {
				t.Setenv(authCacheKeyPrefixEnv, "custom")
			}
			t.Setenv(test.env, test.value)
			if _, err := LoadAuthCache(); err == nil {
				t.Fatal("LoadAuthCache() error = nil, want error")
			}
		})
	}
}
