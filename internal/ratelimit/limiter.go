package ratelimit

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"model-velo/internal/config"
)

var ErrUnavailable = errors.New("rate limiter is unavailable")

const fixedWindowScriptSource = `
local limit = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local current = redis.call("GET", KEYS[1])
local used = 0
local allowed = 0

if not current then
  redis.call("SET", KEYS[1], 1, "PX", window_ms)
  used = 1
  allowed = 1
else
  used = tonumber(current)
  if used < limit then
    used = redis.call("INCR", KEYS[1])
    allowed = 1
  end
end

local ttl_ms = redis.call("PTTL", KEYS[1])
if ttl_ms < 1 then
  redis.call("PEXPIRE", KEYS[1], window_ms)
  ttl_ms = window_ms
end

local remaining = limit - used
if remaining < 0 then
  remaining = 0
end

local redis_time = redis.call("TIME")
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local reset_at = math.ceil((now_ms + ttl_ms) / 1000)
local retry_after = 0
if allowed == 0 then
  retry_after = math.max(1, math.ceil(ttl_ms / 1000))
end

return {allowed, remaining, reset_at, retry_after}
`

var fixedWindowScript = goredis.NewScript(fixedWindowScriptSource)

type Decision struct {
	Allowed           bool
	Bypassed          bool
	Limit             int64
	Remaining         int64
	ResetAtUnix       int64
	RetryAfterSeconds int64
}

type Limiter struct {
	client        *goredis.Client
	environment   string
	maxRequests   int64
	window        time.Duration
	failurePolicy config.RateLimitFailurePolicy
}

func New(client *goredis.Client, settings config.RateLimit) (*Limiter, error) {
	if client == nil {
		return nil, errors.New("rate limiter Redis client is nil")
	}
	if strings.TrimSpace(settings.Environment) == "" || settings.MaxRequests <= 0 || settings.Window <= 0 {
		return nil, errors.New("rate limiter settings are invalid")
	}
	if settings.FailurePolicy != config.RateLimitFailOpen && settings.FailurePolicy != config.RateLimitFailClosed {
		return nil, errors.New("rate limiter failure policy is invalid")
	}

	return &Limiter{
		client:        client,
		environment:   settings.Environment,
		maxRequests:   settings.MaxRequests,
		window:        settings.Window,
		failurePolicy: settings.FailurePolicy,
	}, nil
}

func (limiter *Limiter) Allow(ctx context.Context, tenantID, model string) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}

	key, err := limiter.key(tenantID, model)
	if err != nil {
		return Decision{}, err
	}

	values, err := fixedWindowScript.Run(
		ctx,
		limiter.client,
		[]string{key},
		limiter.maxRequests,
		limiter.window.Milliseconds(),
	).Slice()
	if err != nil {
		if ctx.Err() != nil {
			return Decision{}, ctx.Err()
		}
		if limiter.failurePolicy == config.RateLimitFailOpen {
			return Decision{Allowed: true, Bypassed: true}, nil
		}
		return Decision{}, fmt.Errorf("%w: Redis command failed: %v", ErrUnavailable, err)
	}

	decision, err := parseDecision(values, limiter.maxRequests)
	if err != nil {
		if limiter.failurePolicy == config.RateLimitFailOpen {
			return Decision{Allowed: true, Bypassed: true}, nil
		}
		return Decision{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return decision, nil
}

func (limiter *Limiter) key(tenantID, model string) (string, error) {
	tenantID = strings.TrimSpace(tenantID)
	model = strings.TrimSpace(model)
	if tenantID == "" || model == "" {
		return "", errors.New("rate limit tenant and model are required")
	}

	tenantDigest := sha256.Sum256([]byte(tenantID))
	modelDigest := sha256.Sum256([]byte(model))
	return fmt.Sprintf(
		"model-velo:rate-limit:v1:%s:tenant:%x:model:%x",
		limiter.environment,
		tenantDigest,
		modelDigest,
	), nil
}

func parseDecision(values []any, limit int64) (Decision, error) {
	if len(values) != 4 {
		return Decision{}, fmt.Errorf("rate limit script returned %d values", len(values))
	}

	allowed, ok := values[0].(int64)
	if !ok || (allowed != 0 && allowed != 1) {
		return Decision{}, errors.New("rate limit script returned an invalid allowed value")
	}
	remaining, ok := values[1].(int64)
	if !ok || remaining < 0 || remaining > limit {
		return Decision{}, errors.New("rate limit script returned an invalid remaining value")
	}
	resetAt, ok := values[2].(int64)
	if !ok || resetAt <= 0 {
		return Decision{}, errors.New("rate limit script returned an invalid reset value")
	}
	retryAfter, ok := values[3].(int64)
	if !ok || retryAfter < 0 {
		return Decision{}, errors.New("rate limit script returned an invalid retry value")
	}
	if allowed == 0 && retryAfter == 0 {
		return Decision{}, errors.New("rate limit script omitted retry delay for a rejected request")
	}

	return Decision{
		Allowed:           allowed == 1,
		Limit:             limit,
		Remaining:         remaining,
		ResetAtUnix:       resetAt,
		RetryAfterSeconds: retryAfter,
	}, nil
}
