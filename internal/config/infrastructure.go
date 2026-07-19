package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	postgresDSNEnv            = "MODEL_VELO_POSTGRES_DSN"
	postgresMaxOpenConnsEnv   = "MODEL_VELO_POSTGRES_MAX_OPEN_CONNS"
	postgresMaxIdleConnsEnv   = "MODEL_VELO_POSTGRES_MAX_IDLE_CONNS"
	postgresConnectTimeoutEnv = "MODEL_VELO_POSTGRES_CONNECT_TIMEOUT"
	postgresMaxLifetimeEnv    = "MODEL_VELO_POSTGRES_MAX_CONN_LIFETIME"
	postgresMaxIdleTimeEnv    = "MODEL_VELO_POSTGRES_MAX_CONN_IDLE_TIME"
	redisAddressEnv           = "MODEL_VELO_REDIS_ADDR"
	redisPasswordEnv          = "MODEL_VELO_REDIS_PASSWORD"
	redisDBEnv                = "MODEL_VELO_REDIS_DB"
	redisDialTimeoutEnv       = "MODEL_VELO_REDIS_DIAL_TIMEOUT"
	redisReadTimeoutEnv       = "MODEL_VELO_REDIS_READ_TIMEOUT"
	redisWriteTimeoutEnv      = "MODEL_VELO_REDIS_WRITE_TIMEOUT"
	redisPoolSizeEnv          = "MODEL_VELO_REDIS_POOL_SIZE"
	redisMinIdleConnsEnv      = "MODEL_VELO_REDIS_MIN_IDLE_CONNS"
	redisPoolTimeoutEnv       = "MODEL_VELO_REDIS_POOL_TIMEOUT"
	redisStartupPolicyEnv     = "MODEL_VELO_REDIS_STARTUP_POLICY"
	defaultPostgresMaxOpen    = 10
	defaultPostgresMaxIdle    = 2
	defaultRedisDB            = 0
	defaultRedisPoolSize      = 20
	defaultRedisMinIdleConns  = 2
)

const (
	defaultPostgresConnectTimeout = 5 * time.Second
	defaultPostgresMaxLifetime    = 30 * time.Minute
	defaultPostgresMaxIdleTime    = 5 * time.Minute
	defaultRedisDialTimeout       = 5 * time.Second
	defaultRedisReadTimeout       = 2 * time.Second
	defaultRedisWriteTimeout      = 2 * time.Second
	defaultRedisPoolTimeout       = 2 * time.Second
)

type Infrastructure struct {
	Postgres Postgres
	Redis    Redis
}

type Postgres struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnectTimeout  time.Duration
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

type Redis struct {
	Address       string
	Password      string
	DB            int
	DialTimeout   time.Duration
	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
	PoolSize      int
	MinIdleConns  int
	PoolTimeout   time.Duration
	StartupPolicy RedisStartupPolicy
}

type RedisStartupPolicy string

const (
	RedisStartupRequired RedisStartupPolicy = "required"
	RedisStartupOptional RedisStartupPolicy = "optional"
)

func LoadInfrastructure() (Infrastructure, error) {
	postgres, err := LoadPostgres()
	if err != nil {
		return Infrastructure{}, fmt.Errorf("configure PostgreSQL: %w", err)
	}

	redis, err := LoadRedis()
	if err != nil {
		return Infrastructure{}, fmt.Errorf("configure Redis: %w", err)
	}

	return Infrastructure{
		Postgres: postgres,
		Redis:    redis,
	}, nil
}

func LoadPostgres() (Postgres, error) {
	dsn, err := loadPostgresDSN()
	if err != nil {
		return Postgres{}, err
	}

	maxOpenConns, err := loadInt(postgresMaxOpenConnsEnv, defaultPostgresMaxOpen, false)
	if err != nil {
		return Postgres{}, err
	}
	maxIdleConns, err := loadInt(postgresMaxIdleConnsEnv, defaultPostgresMaxIdle, true)
	if err != nil {
		return Postgres{}, err
	}
	if maxIdleConns > maxOpenConns {
		return Postgres{}, fmt.Errorf("%s must not exceed %s", postgresMaxIdleConnsEnv, postgresMaxOpenConnsEnv)
	}

	connectTimeout, err := loadPositiveDuration(postgresConnectTimeoutEnv, defaultPostgresConnectTimeout)
	if err != nil {
		return Postgres{}, err
	}
	maxLifetime, err := loadPositiveDuration(postgresMaxLifetimeEnv, defaultPostgresMaxLifetime)
	if err != nil {
		return Postgres{}, err
	}
	maxIdleTime, err := loadPositiveDuration(postgresMaxIdleTimeEnv, defaultPostgresMaxIdleTime)
	if err != nil {
		return Postgres{}, err
	}

	return Postgres{
		DSN:             dsn,
		MaxOpenConns:    maxOpenConns,
		MaxIdleConns:    maxIdleConns,
		ConnectTimeout:  connectTimeout,
		MaxConnLifetime: maxLifetime,
		MaxConnIdleTime: maxIdleTime,
	}, nil
}

func loadPostgresDSN() (string, error) {
	dsn := strings.TrimSpace(os.Getenv(postgresDSNEnv))
	if dsn == "" {
		return "", fmt.Errorf("%s is required", postgresDSNEnv)
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("%s must be a valid PostgreSQL URL", postgresDSNEnv)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("%s must use postgres or postgresql scheme", postgresDSNEnv)
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("%s must include a host", postgresDSNEnv)
	}
	if parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return "", fmt.Errorf("%s must include a user", postgresDSNEnv)
	}
	if strings.Trim(parsed.Path, "/") == "" {
		return "", fmt.Errorf("%s must include a database name", postgresDSNEnv)
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("%s must not include a fragment", postgresDSNEnv)
	}

	return dsn, nil
}

func LoadRedis() (Redis, error) {
	address, err := loadRedisAddress()
	if err != nil {
		return Redis{}, err
	}

	password := os.Getenv(redisPasswordEnv)
	if strings.TrimSpace(password) == "" {
		return Redis{}, fmt.Errorf("%s is required", redisPasswordEnv)
	}

	db, err := loadInt(redisDBEnv, defaultRedisDB, true)
	if err != nil {
		return Redis{}, err
	}
	dialTimeout, err := loadPositiveDuration(redisDialTimeoutEnv, defaultRedisDialTimeout)
	if err != nil {
		return Redis{}, err
	}
	readTimeout, err := loadPositiveDuration(redisReadTimeoutEnv, defaultRedisReadTimeout)
	if err != nil {
		return Redis{}, err
	}
	writeTimeout, err := loadPositiveDuration(redisWriteTimeoutEnv, defaultRedisWriteTimeout)
	if err != nil {
		return Redis{}, err
	}
	poolSize, err := loadInt(redisPoolSizeEnv, defaultRedisPoolSize, false)
	if err != nil {
		return Redis{}, err
	}
	minIdleConns, err := loadInt(redisMinIdleConnsEnv, defaultRedisMinIdleConns, true)
	if err != nil {
		return Redis{}, err
	}
	if minIdleConns > poolSize {
		return Redis{}, fmt.Errorf("%s must not exceed %s", redisMinIdleConnsEnv, redisPoolSizeEnv)
	}
	poolTimeout, err := loadPositiveDuration(redisPoolTimeoutEnv, defaultRedisPoolTimeout)
	if err != nil {
		return Redis{}, err
	}
	startupPolicy, err := loadRedisStartupPolicy()
	if err != nil {
		return Redis{}, err
	}

	return Redis{
		Address:       address,
		Password:      password,
		DB:            db,
		DialTimeout:   dialTimeout,
		ReadTimeout:   readTimeout,
		WriteTimeout:  writeTimeout,
		PoolSize:      poolSize,
		MinIdleConns:  minIdleConns,
		PoolTimeout:   poolTimeout,
		StartupPolicy: startupPolicy,
	}, nil
}

func loadRedisAddress() (string, error) {
	address := strings.TrimSpace(os.Getenv(redisAddressEnv))
	if address == "" {
		return "", fmt.Errorf("%s is required", redisAddressEnv)
	}

	host, rawPort, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("%s must use host:port format", redisAddressEnv)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("%s must include a port from 1 to 65535", redisAddressEnv)
	}

	return address, nil
}

func loadRedisStartupPolicy() (RedisStartupPolicy, error) {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(redisStartupPolicyEnv)))
	if value == "" {
		return RedisStartupRequired, nil
	}

	policy := RedisStartupPolicy(value)
	switch policy {
	case RedisStartupRequired, RedisStartupOptional:
		return policy, nil
	default:
		return "", fmt.Errorf("%s must be required or optional", redisStartupPolicyEnv)
	}
}

func loadPositiveDuration(environmentVariable string, defaultValue time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(environmentVariable))
	if value == "" {
		return defaultValue, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", environmentVariable)
	}

	return duration, nil
}

func loadInt(environmentVariable string, defaultValue int, allowZero bool) (int, error) {
	value, err := loadInt64(environmentVariable, int64(defaultValue), allowZero, 0)
	return int(value), err
}

func loadInt64(environmentVariable string, defaultValue int64, allowZero bool, bitSize int) (int64, error) {
	rawValue := strings.TrimSpace(os.Getenv(environmentVariable))
	if rawValue == "" {
		return defaultValue, nil
	}

	value, err := strconv.ParseInt(rawValue, 10, bitSize)
	if err != nil || value < 0 || (!allowZero && value == 0) {
		requirement := "a positive integer"
		if allowZero {
			requirement = "a non-negative integer"
		}
		return 0, errors.New(environmentVariable + " must be " + requirement)
	}

	return value, nil
}
