package config

import (
	"errors"
	"math"
	"os"
	"strconv"
	"strings"

	"model-velo/internal/reliability"
)

const (
	retryMaxAttemptsEnv       = "MODEL_VELO_RETRY_MAX_ATTEMPTS"
	retryInitialBackoffEnv    = "MODEL_VELO_RETRY_INITIAL_BACKOFF"
	retryMaxBackoffEnv        = "MODEL_VELO_RETRY_MAX_BACKOFF"
	retryBackoffMultiplierEnv = "MODEL_VELO_RETRY_BACKOFF_MULTIPLIER"
	retryJitterRatioEnv       = "MODEL_VELO_RETRY_JITTER_RATIO"
	requestTimeoutEnv         = "MODEL_VELO_REQUEST_TIMEOUT"
	attemptTimeoutEnv         = "MODEL_VELO_ATTEMPT_TIMEOUT"
)

func LoadRetry() (reliability.RetryConfig, error) {
	defaults := reliability.DefaultRetryConfig()
	maxAttempts, err := loadInt(retryMaxAttemptsEnv, defaults.MaxAttempts, false)
	if err != nil {
		return reliability.RetryConfig{}, err
	}
	initialBackoff, err := loadPositiveDuration(retryInitialBackoffEnv, defaults.InitialBackoff)
	if err != nil {
		return reliability.RetryConfig{}, err
	}
	maxBackoff, err := loadPositiveDuration(retryMaxBackoffEnv, defaults.MaxBackoff)
	if err != nil {
		return reliability.RetryConfig{}, err
	}
	backoffMultiplier, err := loadNonNegativeFloat(retryBackoffMultiplierEnv, defaults.BackoffMultiplier)
	if err != nil {
		return reliability.RetryConfig{}, err
	}
	jitterRatio, err := loadNonNegativeFloat(retryJitterRatioEnv, defaults.JitterRatio)
	if err != nil {
		return reliability.RetryConfig{}, err
	}
	requestTimeout, err := loadPositiveDuration(requestTimeoutEnv, defaults.RequestTimeout)
	if err != nil {
		return reliability.RetryConfig{}, err
	}
	attemptTimeout, err := loadPositiveDuration(attemptTimeoutEnv, defaults.AttemptTimeout)
	if err != nil {
		return reliability.RetryConfig{}, err
	}

	configured := reliability.RetryConfig{
		MaxAttempts:       maxAttempts,
		InitialBackoff:    initialBackoff,
		MaxBackoff:        maxBackoff,
		BackoffMultiplier: backoffMultiplier,
		JitterRatio:       jitterRatio,
		RequestTimeout:    requestTimeout,
		AttemptTimeout:    attemptTimeout,
	}
	if err := configured.Validate(); err != nil {
		return reliability.RetryConfig{}, errors.New("invalid retry configuration: " + err.Error())
	}
	return configured, nil
}

func loadNonNegativeFloat(environmentVariable string, defaultValue float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(environmentVariable))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, errors.New(environmentVariable + " must be a finite non-negative number")
	}
	return value, nil
}
