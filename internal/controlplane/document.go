package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"model-velo/internal/config"
	"model-velo/internal/gateway"
	"model-velo/internal/provider"
	"model-velo/internal/reliability"
	"model-velo/internal/routing"
)

const RuntimeSchemaVersion = 1

type RuntimeDocument struct {
	SchemaVersion int            `json:"schema_version"`
	Providers     []ProviderSpec `json:"providers"`
	Routes        []RouteSpec    `json:"routes"`
}

type ProviderSpec struct {
	ID                string                           `json:"id"`
	Protocol          string                           `json:"protocol"`
	BaseURL           string                           `json:"base_url"`
	Models            []string                         `json:"models"`
	ModelCapabilities map[string][]provider.Capability `json:"model_capabilities,omitempty"`
	Keys              []ProviderKeySpec                `json:"keys,omitempty"`
	Runtime           ProviderRuntimeSpec              `json:"runtime"`
}

type ProviderKeySpec struct {
	ID     string `json:"id"`
	Secret string `json:"secret,omitempty"`
}

type ProviderRuntimeSpec struct {
	Breaker BreakerSpec `json:"breaker"`
	Queue   QueueSpec   `json:"queue"`
	Retry   RetrySpec   `json:"retry"`
	HTTP    HTTPSpec    `json:"http"`
}

type BreakerSpec struct {
	FailureThreshold  *int   `json:"failure_threshold,omitempty"`
	OpenDuration      string `json:"open_duration,omitempty"`
	HalfOpenMaxProbes *int   `json:"half_open_max_probes,omitempty"`
}

type QueueSpec struct {
	MaxInFlight *int   `json:"max_in_flight,omitempty"`
	MaxWaiting  *int   `json:"max_waiting,omitempty"`
	WaitTimeout string `json:"wait_timeout,omitempty"`
}

type RetrySpec struct {
	MaxAttempts       *int     `json:"max_attempts,omitempty"`
	InitialBackoff    string   `json:"initial_backoff,omitempty"`
	MaxBackoff        string   `json:"max_backoff,omitempty"`
	BackoffMultiplier *float64 `json:"backoff_multiplier,omitempty"`
	JitterRatio       *float64 `json:"jitter_ratio,omitempty"`
	RequestTimeout    string   `json:"request_timeout,omitempty"`
	AttemptTimeout    string   `json:"attempt_timeout,omitempty"`
}

type HTTPSpec struct {
	MaxIdleConnections        *int `json:"max_idle_connections,omitempty"`
	MaxIdleConnectionsPerHost *int `json:"max_idle_connections_per_host,omitempty"`
	MaxConnectionsPerHost     *int `json:"max_connections_per_host,omitempty"`
}

type RouteSpec struct {
	Model      string          `json:"model"`
	Candidates []CandidateSpec `json:"candidates"`
}

type CandidateSpec struct {
	Provider      string `json:"provider"`
	UpstreamModel string `json:"upstream_model,omitempty"`
}

type Builder struct {
	Defaults           config.ProviderDefaults
	EnforceStreamUsage bool
}

func (builder Builder) Build(
	document RuntimeDocument,
) (*gateway.Snapshot, error) {
	return builder.build(document, nil)
}

func (builder Builder) build(
	document RuntimeDocument,
	previous *gateway.Snapshot,
) (*gateway.Snapshot, error) {
	if document.SchemaVersion != RuntimeSchemaVersion {
		return nil, fmt.Errorf(
			"runtime schema_version must be %d", RuntimeSchemaVersion,
		)
	}
	if len(document.Providers) == 0 || len(document.Providers) > 100 {
		return nil, errors.New("runtime providers must contain between 1 and 100 entries")
	}
	if len(document.Routes) == 0 || len(document.Routes) > 10_000 {
		return nil, errors.New("runtime routes must contain between 1 and 10000 entries")
	}
	definition := routing.Definition{}
	adapterConfigs := make([]provider.AdapterConfig, 0, len(document.Providers))
	providerIDs := make([]string, 0, len(document.Providers))
	breakerConfigs := make(map[string]reliability.BreakerConfig, len(document.Providers))
	queueConfigs := make(map[string]reliability.QueueConfig, len(document.Providers))
	retryConfigs := make(map[string]reliability.RetryConfig, len(document.Providers))
	keySets := make([]reliability.ProviderKeySet, 0, len(document.Providers))
	stateIdentities := make(
		map[string]gateway.ProviderStateIdentity,
		len(document.Providers),
	)

	for index, candidate := range document.Providers {
		providerID := strings.TrimSpace(candidate.ID)
		runtime, err := builder.runtime(candidate.Runtime)
		if err != nil {
			return nil, fmt.Errorf("provider %d runtime: %w", index, err)
		}
		definition.Providers = append(definition.Providers, routing.Provider{
			ID:                providerID,
			Type:              strings.ToLower(strings.TrimSpace(candidate.Protocol)),
			BaseURL:           strings.TrimSpace(candidate.BaseURL),
			Models:            append([]string(nil), candidate.Models...),
			ModelCapabilities: candidate.ModelCapabilities,
		})
		adapterConfigs = append(adapterConfigs, provider.AdapterConfig{
			ProviderID:         providerID,
			Protocol:           candidate.Protocol,
			BaseURL:            candidate.BaseURL,
			HTTP:               runtime.HTTP,
			DisableStreamUsage: !builder.EnforceStreamUsage,
		})
		providerIDs = append(providerIDs, providerID)
		breakerConfigs[providerID] = runtime.Breaker
		queueConfigs[providerID] = runtime.Queue
		retryConfigs[providerID] = runtime.Retry
		if len(candidate.Keys) > 0 {
			keys := make([]reliability.ProviderKey, 0, len(candidate.Keys))
			for _, configuredKey := range candidate.Keys {
				keys = append(keys, reliability.ProviderKey{
					ID: strings.TrimSpace(configuredKey.ID), Secret: configuredKey.Secret,
				})
			}
			keySets = append(keySets, reliability.ProviderKeySet{
				ProviderID: providerID, Keys: keys,
			})
		}
		stateIdentities[providerID] = providerStateIdentity(candidate, runtime)
	}
	for _, configuredRoute := range document.Routes {
		rule := routing.Rule{Model: configuredRoute.Model}
		for _, candidate := range configuredRoute.Candidates {
			rule.Candidates = append(rule.Candidates, routing.Target{
				ProviderID: candidate.Provider, UpstreamModel: candidate.UpstreamModel,
			})
		}
		definition.Rules = append(definition.Rules, rule)
	}
	routes, err := routing.New(definition)
	if err != nil {
		return nil, fmt.Errorf("validate routes: %w", err)
	}
	adapters, err := provider.NewAdapterRegistry(adapterConfigs)
	if err != nil {
		return nil, fmt.Errorf("configure adapters: %w", err)
	}
	breakers, err := reliability.NewBreakerRegistryWithConfigs(providerIDs, breakerConfigs)
	if err != nil {
		return nil, fmt.Errorf("configure breakers: %w", err)
	}
	queues, err := reliability.NewQueueRegistryWithConfigs(providerIDs, queueConfigs)
	if err != nil {
		return nil, fmt.Errorf("configure queues: %w", err)
	}
	retries, err := reliability.NewRetryRegistry(providerIDs, retryConfigs)
	if err != nil {
		return nil, fmt.Errorf("configure retries: %w", err)
	}
	var keys *reliability.ProviderKeyRegistry
	keyedProviderIDs := adapters.KeyedProviderIDs()
	if len(keyedProviderIDs) > 0 {
		keys, err = reliability.NewProviderKeyRegistry(keyedProviderIDs, keySets)
		if err != nil {
			return nil, fmt.Errorf("configure provider keys: %w", err)
		}
	}
	reuseProviderStates(
		stateIdentities,
		previous,
		breakers,
		queues,
		keys,
	)
	snapshot, err := gateway.NewSnapshot(
		adapters, routes, breakers, queues, keys, retries,
	)
	if err != nil {
		return nil, err
	}
	snapshot.ProviderStates = stateIdentities
	return snapshot, nil
}

func reuseProviderStates(
	current map[string]gateway.ProviderStateIdentity,
	previous *gateway.Snapshot,
	breakers *reliability.BreakerRegistry,
	queues *reliability.QueueRegistry,
	keys *reliability.ProviderKeyRegistry,
) {
	if previous == nil {
		return
	}
	for providerID, identity := range current {
		existing, ok := previous.ProviderStates[providerID]
		if !ok {
			continue
		}
		if identity.Breaker == existing.Breaker {
			breakers.ReuseProvider(providerID, previous.Breakers)
		}
		if identity.Queue == existing.Queue {
			queues.ReuseProvider(providerID, previous.Queues)
		}
		if identity.Keys != "" && identity.Keys == existing.Keys {
			keys.ReuseProvider(providerID, previous.Keys)
		}
	}
}

func providerStateIdentity(
	spec ProviderSpec,
	runtime config.ProviderRuntime,
) gateway.ProviderStateIdentity {
	breaker := runtime.Breaker
	queue := runtime.Queue
	keyParts := make([]string, 0, len(spec.Keys)*2)
	for _, key := range spec.Keys {
		keyParts = append(
			keyParts,
			strings.TrimSpace(key.ID),
			strings.TrimSpace(key.Secret),
		)
	}
	identity := gateway.ProviderStateIdentity{
		Breaker: stateFingerprint(
			strings.ToLower(strings.TrimSpace(spec.Protocol)),
			strings.TrimSpace(spec.BaseURL),
			strconv.Itoa(breaker.FailureThreshold),
			strconv.FormatInt(int64(breaker.OpenDuration), 10),
			strconv.Itoa(breaker.HalfOpenMaxProbes),
		),
		Queue: stateFingerprint(
			strconv.Itoa(queue.MaxInFlight),
			strconv.Itoa(queue.MaxWaiting),
			strconv.FormatInt(int64(queue.WaitTimeout), 10),
		),
	}
	if len(keyParts) > 0 {
		identity.Keys = stateFingerprint(keyParts...)
	}
	return identity
}

func stateFingerprint(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		digest.Write([]byte(strconv.Itoa(len(part))))
		digest.Write([]byte{':'})
		digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func (builder Builder) runtime(
	spec ProviderRuntimeSpec,
) (config.ProviderRuntime, error) {
	configured := config.ProviderRuntime{
		Breaker: builder.Defaults.Breaker,
		Queue:   builder.Defaults.Queue,
		Retry:   builder.Defaults.Retry,
		HTTP:    builder.Defaults.HTTP,
	}
	if spec.Breaker.FailureThreshold != nil {
		configured.Breaker.FailureThreshold = *spec.Breaker.FailureThreshold
	}
	if spec.Breaker.HalfOpenMaxProbes != nil {
		configured.Breaker.HalfOpenMaxProbes = *spec.Breaker.HalfOpenMaxProbes
	}
	if spec.Queue.MaxInFlight != nil {
		configured.Queue.MaxInFlight = *spec.Queue.MaxInFlight
	}
	if spec.Queue.MaxWaiting != nil {
		configured.Queue.MaxWaiting = *spec.Queue.MaxWaiting
	}
	if spec.Retry.MaxAttempts != nil {
		configured.Retry.MaxAttempts = *spec.Retry.MaxAttempts
	}
	if spec.Retry.BackoffMultiplier != nil {
		configured.Retry.BackoffMultiplier = *spec.Retry.BackoffMultiplier
	}
	if spec.Retry.JitterRatio != nil {
		configured.Retry.JitterRatio = *spec.Retry.JitterRatio
	}
	if spec.HTTP.MaxIdleConnections != nil {
		configured.HTTP.MaxIdleConnections = *spec.HTTP.MaxIdleConnections
	}
	if spec.HTTP.MaxIdleConnectionsPerHost != nil {
		configured.HTTP.MaxIdleConnectionsPerHost = *spec.HTTP.MaxIdleConnectionsPerHost
	}
	if spec.HTTP.MaxConnectionsPerHost != nil {
		configured.HTTP.MaxConnectionsPerHost = *spec.HTTP.MaxConnectionsPerHost
	}
	durationFields := []struct {
		name     string
		raw      string
		fallback time.Duration
		target   *time.Duration
	}{
		{"breaker open_duration", spec.Breaker.OpenDuration, configured.Breaker.OpenDuration, &configured.Breaker.OpenDuration},
		{"queue wait_timeout", spec.Queue.WaitTimeout, configured.Queue.WaitTimeout, &configured.Queue.WaitTimeout},
		{"retry initial_backoff", spec.Retry.InitialBackoff, configured.Retry.InitialBackoff, &configured.Retry.InitialBackoff},
		{"retry max_backoff", spec.Retry.MaxBackoff, configured.Retry.MaxBackoff, &configured.Retry.MaxBackoff},
		{"retry request_timeout", spec.Retry.RequestTimeout, configured.Retry.RequestTimeout, &configured.Retry.RequestTimeout},
		{"retry attempt_timeout", spec.Retry.AttemptTimeout, configured.Retry.AttemptTimeout, &configured.Retry.AttemptTimeout},
	}
	for _, field := range durationFields {
		value, err := optionalDuration(field.raw, field.fallback)
		if err != nil {
			return config.ProviderRuntime{}, fmt.Errorf("%s: %w", field.name, err)
		}
		*field.target = value
	}
	if err := configured.Breaker.Validate(); err != nil {
		return config.ProviderRuntime{}, err
	}
	if err := configured.Queue.Validate(); err != nil {
		return config.ProviderRuntime{}, err
	}
	if err := configured.Retry.Validate(); err != nil {
		return config.ProviderRuntime{}, err
	}
	if err := configured.HTTP.Validate(); err != nil {
		return config.ProviderRuntime{}, err
	}
	return configured, nil
}

func optionalDuration(raw string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, errors.New("must be a positive duration")
	}
	return value, nil
}
