package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadInfrastructureDefaults(t *testing.T) {
	setRequiredInfrastructureEnv(t)

	got, err := LoadInfrastructure()
	if err != nil {
		t.Fatalf("LoadInfrastructure() error = %v", err)
	}

	if got.Postgres.DSN != "postgres://model_velo:postgres-secret@localhost:5432/model_velo?sslmode=disable" {
		t.Errorf("Postgres.DSN = %q", got.Postgres.DSN)
	}
	if got.Postgres.MaxOpenConns != 10 || got.Postgres.MaxIdleConns != 2 {
		t.Errorf("Postgres pool = %d/%d, want 10/2", got.Postgres.MaxOpenConns, got.Postgres.MaxIdleConns)
	}
	if got.Postgres.ConnectTimeout != 5*time.Second {
		t.Errorf("Postgres.ConnectTimeout = %s, want 5s", got.Postgres.ConnectTimeout)
	}
	if got.Postgres.MaxConnLifetime != 30*time.Minute {
		t.Errorf("Postgres.MaxConnLifetime = %s, want 30m", got.Postgres.MaxConnLifetime)
	}
	if got.Postgres.MaxConnIdleTime != 5*time.Minute {
		t.Errorf("Postgres.MaxConnIdleTime = %s, want 5m", got.Postgres.MaxConnIdleTime)
	}

	if got.Redis.Address != "localhost:6379" || got.Redis.Password != "redis-secret" || got.Redis.DB != 0 {
		t.Errorf("Redis identity config = %#v", got.Redis)
	}
	if got.Redis.DialTimeout != 5*time.Second || got.Redis.ReadTimeout != 2*time.Second || got.Redis.WriteTimeout != 2*time.Second {
		t.Errorf("Redis timeouts = %s/%s/%s", got.Redis.DialTimeout, got.Redis.ReadTimeout, got.Redis.WriteTimeout)
	}
	if got.Redis.PoolSize != 20 || got.Redis.MinIdleConns != 2 || got.Redis.PoolTimeout != 2*time.Second {
		t.Errorf("Redis pool = %d/%d/%s, want 20/2/2s", got.Redis.PoolSize, got.Redis.MinIdleConns, got.Redis.PoolTimeout)
	}
	if got.Redis.StartupPolicy != RedisStartupRequired {
		t.Errorf("Redis.StartupPolicy = %q, want %q", got.Redis.StartupPolicy, RedisStartupRequired)
	}
}

func TestLoadInfrastructureConfigured(t *testing.T) {
	setRequiredInfrastructureEnv(t)
	t.Setenv(postgresMaxOpenConnsEnv, "24")
	t.Setenv(postgresMaxIdleConnsEnv, "3")
	t.Setenv(postgresConnectTimeoutEnv, "8s")
	t.Setenv(postgresMaxLifetimeEnv, "45m")
	t.Setenv(postgresMaxIdleTimeEnv, "7m")
	t.Setenv(redisAddressEnv, "127.0.0.1:6380")
	t.Setenv(redisPasswordEnv, " redis-secret-with-spaces ")
	t.Setenv(redisDBEnv, "4")
	t.Setenv(redisDialTimeoutEnv, "7s")
	t.Setenv(redisReadTimeoutEnv, "3s")
	t.Setenv(redisWriteTimeoutEnv, "4s")
	t.Setenv(redisPoolSizeEnv, "32")
	t.Setenv(redisMinIdleConnsEnv, "4")
	t.Setenv(redisPoolTimeoutEnv, "6s")
	t.Setenv(redisStartupPolicyEnv, " OPTIONAL ")

	got, err := LoadInfrastructure()
	if err != nil {
		t.Fatalf("LoadInfrastructure() error = %v", err)
	}

	if got.Postgres.MaxOpenConns != 24 || got.Postgres.MaxIdleConns != 3 {
		t.Errorf("Postgres pool = %d/%d, want 24/3", got.Postgres.MaxOpenConns, got.Postgres.MaxIdleConns)
	}
	if got.Postgres.ConnectTimeout != 8*time.Second ||
		got.Postgres.MaxConnLifetime != 45*time.Minute ||
		got.Postgres.MaxConnIdleTime != 7*time.Minute {
		t.Errorf(
			"Postgres durations = %s/%s/%s",
			got.Postgres.ConnectTimeout,
			got.Postgres.MaxConnLifetime,
			got.Postgres.MaxConnIdleTime,
		)
	}
	if got.Redis.Address != "127.0.0.1:6380" || got.Redis.DB != 4 {
		t.Errorf("Redis address/DB = %s/%d, want 127.0.0.1:6380/4", got.Redis.Address, got.Redis.DB)
	}
	if got.Redis.Password != " redis-secret-with-spaces " {
		t.Errorf("Redis.Password was unexpectedly normalized")
	}
	if got.Redis.PoolSize != 32 || got.Redis.MinIdleConns != 4 || got.Redis.PoolTimeout != 6*time.Second {
		t.Errorf("Redis pool = %d/%d/%s, want 32/4/6s", got.Redis.PoolSize, got.Redis.MinIdleConns, got.Redis.PoolTimeout)
	}
	if got.Redis.StartupPolicy != RedisStartupOptional {
		t.Errorf("Redis.StartupPolicy = %q, want %q", got.Redis.StartupPolicy, RedisStartupOptional)
	}
}

func TestLoadInfrastructureRejectsInvalidPostgresConfig(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "missing DSN", env: postgresDSNEnv, value: ""},
		{name: "invalid DSN scheme", env: postgresDSNEnv, value: "mysql://user:secret@localhost/model_velo"},
		{name: "DSN without host", env: postgresDSNEnv, value: "postgres://user:secret@/model_velo"},
		{name: "DSN without user", env: postgresDSNEnv, value: "postgres://localhost/model_velo"},
		{name: "DSN without database", env: postgresDSNEnv, value: "postgres://user:secret@localhost"},
		{name: "zero max open connections", env: postgresMaxOpenConnsEnv, value: "0"},
		{name: "negative max idle connections", env: postgresMaxIdleConnsEnv, value: "-1"},
		{name: "invalid connect timeout", env: postgresConnectTimeoutEnv, value: "later"},
		{name: "zero max lifetime", env: postgresMaxLifetimeEnv, value: "0s"},
		{name: "invalid max idle time", env: postgresMaxIdleTimeEnv, value: "idle"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setRequiredInfrastructureEnv(t)
			t.Setenv(test.env, test.value)

			_, err := LoadInfrastructure()
			if err == nil {
				t.Fatal("LoadInfrastructure() error = nil, want error")
			}
		})
	}
}

func TestLoadInfrastructureRejectsMaxIdleAboveMaxOpen(t *testing.T) {
	setRequiredInfrastructureEnv(t)
	t.Setenv(postgresMaxOpenConnsEnv, "2")
	t.Setenv(postgresMaxIdleConnsEnv, "3")

	_, err := LoadInfrastructure()
	if err == nil {
		t.Fatal("LoadInfrastructure() error = nil, want error")
	}
}

func TestLoadInfrastructureRejectsInvalidRedisConfig(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "missing address", env: redisAddressEnv, value: ""},
		{name: "address without port", env: redisAddressEnv, value: "localhost"},
		{name: "address without host", env: redisAddressEnv, value: ":6379"},
		{name: "port out of range", env: redisAddressEnv, value: "localhost:70000"},
		{name: "missing password", env: redisPasswordEnv, value: ""},
		{name: "negative DB", env: redisDBEnv, value: "-1"},
		{name: "invalid dial timeout", env: redisDialTimeoutEnv, value: "soon"},
		{name: "zero read timeout", env: redisReadTimeoutEnv, value: "0s"},
		{name: "negative write timeout", env: redisWriteTimeoutEnv, value: "-1s"},
		{name: "zero pool size", env: redisPoolSizeEnv, value: "0"},
		{name: "negative minimum idle connections", env: redisMinIdleConnsEnv, value: "-1"},
		{name: "invalid pool timeout", env: redisPoolTimeoutEnv, value: "wait"},
		{name: "unsupported startup policy", env: redisStartupPolicyEnv, value: "fail-open"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setRequiredInfrastructureEnv(t)
			t.Setenv(test.env, test.value)

			_, err := LoadInfrastructure()
			if err == nil {
				t.Fatal("LoadInfrastructure() error = nil, want error")
			}
		})
	}
}

func TestLoadInfrastructureErrorsDoNotLeakCredentials(t *testing.T) {
	const (
		postgresSecret = "postgres-super-secret"
		redisSecret    = "redis-super-secret"
	)

	setRequiredInfrastructureEnv(t)
	t.Setenv(postgresDSNEnv, "postgres://model_velo:"+postgresSecret+"@localhost:5432/model_velo")
	t.Setenv(redisPasswordEnv, redisSecret)
	t.Setenv(redisStartupPolicyEnv, "unknown")

	_, err := LoadInfrastructure()
	if err == nil {
		t.Fatal("LoadInfrastructure() error = nil, want error")
	}
	if strings.Contains(err.Error(), postgresSecret) || strings.Contains(err.Error(), redisSecret) {
		t.Fatalf("LoadInfrastructure() error leaked credentials: %v", err)
	}
}

func setRequiredInfrastructureEnv(t *testing.T) {
	t.Helper()

	t.Setenv(postgresDSNEnv, "postgres://model_velo:postgres-secret@localhost:5432/model_velo?sslmode=disable")
	t.Setenv(postgresMaxOpenConnsEnv, "")
	t.Setenv(postgresMaxIdleConnsEnv, "")
	t.Setenv(postgresConnectTimeoutEnv, "")
	t.Setenv(postgresMaxLifetimeEnv, "")
	t.Setenv(postgresMaxIdleTimeEnv, "")
	t.Setenv(redisAddressEnv, "localhost:6379")
	t.Setenv(redisPasswordEnv, "redis-secret")
	t.Setenv(redisDBEnv, "")
	t.Setenv(redisDialTimeoutEnv, "")
	t.Setenv(redisReadTimeoutEnv, "")
	t.Setenv(redisWriteTimeoutEnv, "")
	t.Setenv(redisPoolSizeEnv, "")
	t.Setenv(redisMinIdleConnsEnv, "")
	t.Setenv(redisPoolTimeoutEnv, "")
	t.Setenv(redisStartupPolicyEnv, "")
}
