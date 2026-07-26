package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	environmentEnv                 = "MODEL_VELO_ENVIRONMENT"
	rateLimitRequestsEnv           = "MODEL_VELO_RATE_LIMIT_REQUESTS"
	rateLimitWindowEnv             = "MODEL_VELO_RATE_LIMIT_WINDOW"
	rateLimitFailureEnv            = "MODEL_VELO_RATE_LIMIT_FAILURE_POLICY"
	defaultRateLimitRequests int64 = 6_000
	maximumRateLimitRequests int64 = 1_000_000
)

const (
	defaultRateLimitWindow = time.Minute
	minimumRateLimitWindow = time.Second
	maximumRateLimitWindow = 24 * time.Hour
)

var environmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

type RateLimit struct {
	Environment   string
	MaxRequests   int64
	Window        time.Duration
	FailurePolicy RateLimitFailurePolicy
}

type RateLimitFailurePolicy string

const (
	RateLimitFailOpen   RateLimitFailurePolicy = "fail-open"
	RateLimitFailClosed RateLimitFailurePolicy = "fail-closed"
)

func LoadRateLimit() (RateLimit, error) {
	environment := strings.ToLower(strings.TrimSpace(os.Getenv(environmentEnv)))
	if !environmentPattern.MatchString(environment) {
		return RateLimit{}, fmt.Errorf(
			"%s must contain 1 to 32 lowercase letters, digits, underscores, or hyphens",
			environmentEnv,
		)
	}

	maxRequests, err := loadInt64(rateLimitRequestsEnv, defaultRateLimitRequests, false, 64)
	if err != nil {
		return RateLimit{}, err
	}
	if maxRequests > maximumRateLimitRequests {
		return RateLimit{}, fmt.Errorf("%s must not exceed %d", rateLimitRequestsEnv, maximumRateLimitRequests)
	}

	window, err := loadPositiveDuration(rateLimitWindowEnv, defaultRateLimitWindow)
	if err != nil {
		return RateLimit{}, err
	}
	if window < minimumRateLimitWindow || window > maximumRateLimitWindow {
		return RateLimit{}, fmt.Errorf("%s must be between %s and %s", rateLimitWindowEnv, minimumRateLimitWindow, maximumRateLimitWindow)
	}

	failurePolicy, err := loadRateLimitFailurePolicy()
	if err != nil {
		return RateLimit{}, err
	}

	return RateLimit{
		Environment:   environment,
		MaxRequests:   maxRequests,
		Window:        window,
		FailurePolicy: failurePolicy,
	}, nil
}

func loadRateLimitFailurePolicy() (RateLimitFailurePolicy, error) {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(rateLimitFailureEnv)))
	if value == "" {
		return RateLimitFailClosed, nil
	}

	policy := RateLimitFailurePolicy(value)
	switch policy {
	case RateLimitFailOpen, RateLimitFailClosed:
		return policy, nil
	default:
		return "", fmt.Errorf("%s must be fail-open or fail-closed", rateLimitFailureEnv)
	}
}
