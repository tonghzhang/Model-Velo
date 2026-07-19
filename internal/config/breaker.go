package config

import (
	"fmt"

	"model-velo/internal/reliability"
)

const (
	breakerFailureThresholdEnv = "MODEL_VELO_BREAKER_FAILURE_THRESHOLD"
	breakerOpenDurationEnv     = "MODEL_VELO_BREAKER_OPEN_DURATION"
	breakerHalfOpenProbesEnv   = "MODEL_VELO_BREAKER_HALF_OPEN_PROBES"
)

func LoadCircuitBreaker() (reliability.BreakerConfig, error) {
	defaults := reliability.DefaultBreakerConfig()
	failureThreshold, err := loadInt(breakerFailureThresholdEnv, defaults.FailureThreshold, false)
	if err != nil {
		return reliability.BreakerConfig{}, err
	}
	openDuration, err := loadPositiveDuration(breakerOpenDurationEnv, defaults.OpenDuration)
	if err != nil {
		return reliability.BreakerConfig{}, err
	}
	halfOpenProbes, err := loadInt(breakerHalfOpenProbesEnv, defaults.HalfOpenMaxProbes, false)
	if err != nil {
		return reliability.BreakerConfig{}, err
	}

	configured := reliability.BreakerConfig{
		FailureThreshold:  failureThreshold,
		OpenDuration:      openDuration,
		HalfOpenMaxProbes: halfOpenProbes,
	}
	if err := configured.Validate(); err != nil {
		return reliability.BreakerConfig{}, fmt.Errorf("invalid circuit breaker configuration: %w", err)
	}
	return configured, nil
}
