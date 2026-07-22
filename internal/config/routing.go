package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"model-velo/internal/provider"
	"model-velo/internal/reliability"
	"model-velo/internal/routing"
)

const (
	routingJSONEnv          = "MODEL_VELO_ROUTING_JSON"
	maximumRoutingJSONBytes = 64 << 10
)

type routingJSON struct {
	Providers []routingProviderJSON `json:"providers"`
	Routes    []routingRuleJSON     `json:"routes"`
}

type routingProviderJSON struct {
	ID                string                           `json:"id"`
	Type              string                           `json:"type"`
	Vendor            string                           `json:"vendor"`
	BaseURL           string                           `json:"base_url"`
	Models            []string                         `json:"models"`
	ModelCapabilities map[string][]provider.Capability `json:"model_capabilities"`
	Runtime           providerRuntimeJSON              `json:"runtime"`
}

type providerRuntimeJSON struct {
	Breaker breakerOverrideJSON `json:"breaker"`
	Queue   queueOverrideJSON   `json:"queue"`
	Retry   retryOverrideJSON   `json:"retry"`
	HTTP    httpOverrideJSON    `json:"http"`
}

type breakerOverrideJSON struct {
	FailureThreshold  *int   `json:"failure_threshold"`
	OpenDuration      string `json:"open_duration"`
	HalfOpenMaxProbes *int   `json:"half_open_max_probes"`
}

type queueOverrideJSON struct {
	MaxInFlight *int   `json:"max_in_flight"`
	MaxWaiting  *int   `json:"max_waiting"`
	WaitTimeout string `json:"wait_timeout"`
}

type retryOverrideJSON struct {
	MaxAttempts       *int     `json:"max_attempts"`
	InitialBackoff    string   `json:"initial_backoff"`
	MaxBackoff        string   `json:"max_backoff"`
	BackoffMultiplier *float64 `json:"backoff_multiplier"`
	JitterRatio       *float64 `json:"jitter_ratio"`
	AttemptTimeout    string   `json:"attempt_timeout"`
}

type httpOverrideJSON struct {
	MaxIdleConnections        *int `json:"max_idle_connections"`
	MaxIdleConnectionsPerHost *int `json:"max_idle_connections_per_host"`
	MaxConnectionsPerHost     *int `json:"max_connections_per_host"`
}

type ProviderDefaults struct {
	Breaker reliability.BreakerConfig
	Queue   reliability.QueueConfig
	Retry   reliability.RetryConfig
	HTTP    provider.HTTPConfig
}

type ProviderRuntime struct {
	Breaker reliability.BreakerConfig
	Queue   reliability.QueueConfig
	Retry   reliability.RetryConfig
	HTTP    provider.HTTPConfig
}

type Routing struct {
	Definition routing.Definition
	Providers  map[string]ProviderRuntime
}

type routingRuleJSON struct {
	Model      string                 `json:"model"`
	Candidates []routingCandidateJSON `json:"candidates"`
}

type routingCandidateJSON struct {
	Provider      string `json:"provider"`
	UpstreamModel string `json:"upstream_model"`
}

func LoadRouting(defaults ProviderDefaults) (Routing, error) {
	if err := validateProviderDefaults(defaults); err != nil {
		return Routing{}, err
	}
	raw := strings.TrimSpace(os.Getenv(routingJSONEnv))
	if raw == "" {
		return Routing{}, fmt.Errorf("%s is required", routingJSONEnv)
	}
	if len(raw) > maximumRoutingJSONBytes {
		return Routing{}, fmt.Errorf("%s exceeds %d bytes", routingJSONEnv, maximumRoutingJSONBytes)
	}

	var document routingJSON
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return Routing{}, fmt.Errorf("%s must be a valid routing object: %w", routingJSONEnv, err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return Routing{}, fmt.Errorf("%s must contain one routing object: %w", routingJSONEnv, err)
	}
	definition := routing.Definition{}
	providerRuntime := make(map[string]ProviderRuntime, len(document.Providers))
	for index, configuredProvider := range document.Providers {
		preset, err := provider.Resolve(
			configuredProvider.Vendor,
			configuredProvider.Type,
			configuredProvider.BaseURL,
		)
		if err != nil {
			return Routing{}, fmt.Errorf("%s provider %d: %w", routingJSONEnv, index, err)
		}
		providerID := strings.TrimSpace(configuredProvider.ID)
		runtime, err := resolveProviderRuntime(providerID, configuredProvider.Runtime, defaults)
		if err != nil {
			return Routing{}, fmt.Errorf("%s provider %d runtime: %w", routingJSONEnv, index, err)
		}
		definition.Providers = append(definition.Providers, routing.Provider{
			ID:                providerID,
			Type:              preset.Protocol,
			BaseURL:           preset.BaseURL,
			Models:            configuredProvider.Models,
			ModelCapabilities: configuredProvider.ModelCapabilities,
		})
		providerRuntime[providerID] = runtime
	}
	for _, configuredRoute := range document.Routes {
		rule := routing.Rule{Model: configuredRoute.Model}
		for _, configuredCandidate := range configuredRoute.Candidates {
			rule.Candidates = append(rule.Candidates, routing.Target{
				ProviderID:    configuredCandidate.Provider,
				UpstreamModel: configuredCandidate.UpstreamModel,
			})
		}
		definition.Rules = append(definition.Rules, rule)
	}

	return Routing{Definition: definition, Providers: providerRuntime}, nil
}

func validateProviderDefaults(defaults ProviderDefaults) error {
	if err := defaults.Breaker.Validate(); err != nil {
		return fmt.Errorf("invalid default circuit breaker configuration: %w", err)
	}
	if err := defaults.Queue.Validate(); err != nil {
		return fmt.Errorf("invalid default provider queue configuration: %w", err)
	}
	if err := defaults.Retry.Validate(); err != nil {
		return fmt.Errorf("invalid default retry configuration: %w", err)
	}
	if err := defaults.HTTP.Validate(); err != nil {
		return fmt.Errorf("invalid default provider HTTP configuration: %w", err)
	}
	return nil
}

func resolveProviderRuntime(
	providerID string,
	override providerRuntimeJSON,
	defaults ProviderDefaults,
) (ProviderRuntime, error) {
	configured := ProviderRuntime{
		Breaker: defaults.Breaker,
		Queue:   defaults.Queue,
		Retry:   defaults.Retry,
		HTTP:    defaults.HTTP,
	}
	if override.Breaker.FailureThreshold != nil {
		configured.Breaker.FailureThreshold = *override.Breaker.FailureThreshold
	}
	if override.Breaker.HalfOpenMaxProbes != nil {
		configured.Breaker.HalfOpenMaxProbes = *override.Breaker.HalfOpenMaxProbes
	}
	if override.Queue.MaxInFlight != nil {
		configured.Queue.MaxInFlight = *override.Queue.MaxInFlight
	}
	if override.Queue.MaxWaiting != nil {
		configured.Queue.MaxWaiting = *override.Queue.MaxWaiting
	}
	// The queue is the concurrency owner. Unless explicitly overridden below,
	// the upstream connection pool follows its resolved in-flight limit.
	configured.HTTP.MaxConnectionsPerHost = configured.Queue.MaxInFlight
	configured.HTTP.MaxIdleConnectionsPerHost = configured.Queue.MaxInFlight
	if configured.HTTP.MaxIdleConnections < configured.Queue.MaxInFlight {
		configured.HTTP.MaxIdleConnections = configured.Queue.MaxInFlight
	}
	if override.Retry.MaxAttempts != nil {
		configured.Retry.MaxAttempts = *override.Retry.MaxAttempts
	}
	if override.Retry.BackoffMultiplier != nil {
		configured.Retry.BackoffMultiplier = *override.Retry.BackoffMultiplier
	}
	if override.Retry.JitterRatio != nil {
		configured.Retry.JitterRatio = *override.Retry.JitterRatio
	}
	if override.HTTP.MaxIdleConnections != nil {
		configured.HTTP.MaxIdleConnections = *override.HTTP.MaxIdleConnections
	}
	if override.HTTP.MaxIdleConnectionsPerHost != nil {
		configured.HTTP.MaxIdleConnectionsPerHost = *override.HTTP.MaxIdleConnectionsPerHost
	}
	if override.HTTP.MaxConnectionsPerHost != nil {
		configured.HTTP.MaxConnectionsPerHost = *override.HTTP.MaxConnectionsPerHost
		if override.HTTP.MaxIdleConnectionsPerHost == nil && configured.HTTP.MaxIdleConnectionsPerHost > configured.HTTP.MaxConnectionsPerHost {
			configured.HTTP.MaxIdleConnectionsPerHost = configured.HTTP.MaxConnectionsPerHost
		}
	}

	var err error
	configured.Breaker.OpenDuration, err = durationOverride(providerID+" breaker open_duration", override.Breaker.OpenDuration, configured.Breaker.OpenDuration)
	if err != nil {
		return ProviderRuntime{}, err
	}
	configured.Queue.WaitTimeout, err = durationOverride(providerID+" queue wait_timeout", override.Queue.WaitTimeout, configured.Queue.WaitTimeout)
	if err != nil {
		return ProviderRuntime{}, err
	}
	configured.Retry.InitialBackoff, err = durationOverride(providerID+" retry initial_backoff", override.Retry.InitialBackoff, configured.Retry.InitialBackoff)
	if err != nil {
		return ProviderRuntime{}, err
	}
	configured.Retry.MaxBackoff, err = durationOverride(providerID+" retry max_backoff", override.Retry.MaxBackoff, configured.Retry.MaxBackoff)
	if err != nil {
		return ProviderRuntime{}, err
	}
	configured.Retry.AttemptTimeout, err = durationOverride(providerID+" retry attempt_timeout", override.Retry.AttemptTimeout, configured.Retry.AttemptTimeout)
	if err != nil {
		return ProviderRuntime{}, err
	}

	if err := configured.Breaker.Validate(); err != nil {
		return ProviderRuntime{}, err
	}
	if err := configured.Queue.Validate(); err != nil {
		return ProviderRuntime{}, err
	}
	if err := configured.Retry.Validate(); err != nil {
		return ProviderRuntime{}, err
	}
	if err := configured.HTTP.Validate(); err != nil {
		return ProviderRuntime{}, err
	}
	return configured, nil
}

func durationOverride(name, raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("unexpected trailing JSON value")
}
