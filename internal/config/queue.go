package config

import (
	"fmt"

	"model-velo/internal/reliability"
)

const (
	queueMaxInFlightEnv = "MODEL_VELO_QUEUE_MAX_IN_FLIGHT"
	queueMaxWaitingEnv  = "MODEL_VELO_QUEUE_MAX_WAITING"
	queueWaitTimeoutEnv = "MODEL_VELO_QUEUE_WAIT_TIMEOUT"
)

func LoadProviderQueue() (reliability.QueueConfig, error) {
	defaults := reliability.DefaultQueueConfig()
	maxInFlight, err := loadInt(queueMaxInFlightEnv, defaults.MaxInFlight, false)
	if err != nil {
		return reliability.QueueConfig{}, err
	}
	maxWaiting, err := loadInt(queueMaxWaitingEnv, defaults.MaxWaiting, true)
	if err != nil {
		return reliability.QueueConfig{}, err
	}
	waitTimeout, err := loadPositiveDuration(queueWaitTimeoutEnv, defaults.WaitTimeout)
	if err != nil {
		return reliability.QueueConfig{}, err
	}

	configured := reliability.QueueConfig{
		MaxInFlight: maxInFlight,
		MaxWaiting:  maxWaiting,
		WaitTimeout: waitTimeout,
	}
	if err := configured.Validate(); err != nil {
		return reliability.QueueConfig{}, fmt.Errorf("invalid provider queue configuration: %w", err)
	}
	return configured, nil
}
