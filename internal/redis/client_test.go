package redis

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"model-velo/internal/config"
)

const (
	redisTestAddressEnv  = "MODEL_VELO_REDIS_TEST_ADDR"
	redisTestPasswordEnv = "MODEL_VELO_REDIS_TEST_PASSWORD"
	redisTestDBEnv       = "MODEL_VELO_REDIS_TEST_DB"
)

func TestRedisClientOpenPingAndClose(t *testing.T) {
	settings := liveRedisSettings(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := Open(ctx, settings)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !client.AvailableAtStartup() {
		t.Fatal("AvailableAtStartup() = false, want true")
	}
	if err := client.Native().Ping(ctx).Err(); err != nil {
		t.Fatalf("Ping() after Open error = %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := client.Native().Ping(context.Background()).Err(); err == nil {
		t.Fatal("Ping() after Close error = nil, want closed client error")
	}
}

func TestRedisClientUnavailableStartupPolicies(t *testing.T) {
	tests := []struct {
		name       string
		policy     config.RedisStartupPolicy
		wantError  bool
		wantClient bool
	}{
		{name: "required", policy: config.RedisStartupRequired, wantError: true},
		{name: "optional", policy: config.RedisStartupOptional, wantClient: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := redisSettings("127.0.0.1:0", "unused", 0)
			settings.StartupPolicy = test.policy
			settings.DialTimeout = 100 * time.Millisecond

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			client, err := Open(ctx, settings)
			if test.wantError {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("Open() error = %v, want ErrUnavailable", err)
				}
				if client != nil {
					t.Fatal("Open() client is non-nil on required startup failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			if (client != nil) != test.wantClient {
				t.Fatalf("Open() client presence = %t, want %t", client != nil, test.wantClient)
			}
			if client.AvailableAtStartup() {
				t.Fatal("AvailableAtStartup() = true after optional startup failure")
			}
			if err := client.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		})
	}
}

func liveRedisSettings(t *testing.T) config.Redis {
	t.Helper()

	address := strings.TrimSpace(os.Getenv(redisTestAddressEnv))
	if address == "" {
		t.Skipf("set %s to run real Redis client integration tests", redisTestAddressEnv)
	}
	db := 0
	if rawDB := strings.TrimSpace(os.Getenv(redisTestDBEnv)); rawDB != "" {
		parsed, err := strconv.Atoi(rawDB)
		if err != nil || parsed < 0 {
			t.Fatalf("%s must be a non-negative integer", redisTestDBEnv)
		}
		db = parsed
	}
	return redisSettings(address, os.Getenv(redisTestPasswordEnv), db)
}

func redisSettings(address, password string, db int) config.Redis {
	return config.Redis{
		Address:       address,
		Password:      password,
		DB:            db,
		DialTimeout:   time.Second,
		ReadTimeout:   time.Second,
		WriteTimeout:  time.Second,
		PoolSize:      4,
		MinIdleConns:  1,
		PoolTimeout:   time.Second,
		StartupPolicy: config.RedisStartupRequired,
	}
}
