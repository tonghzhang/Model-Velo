package reliability

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrUnknownBreakerProvider = errors.New("provider circuit breaker is not configured")

type BreakerRegistry struct {
	breakers map[string]*Breaker
	ids      []string
}

func NewBreakerRegistry(providerIDs []string, config BreakerConfig) (*BreakerRegistry, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	configs := make(map[string]BreakerConfig, len(providerIDs))
	for _, providerID := range providerIDs {
		configs[strings.TrimSpace(providerID)] = config
	}
	return NewBreakerRegistryWithConfigs(providerIDs, configs)
}

func NewBreakerRegistryWithConfigs(
	providerIDs []string,
	configs map[string]BreakerConfig,
) (*BreakerRegistry, error) {
	registry := &BreakerRegistry{breakers: make(map[string]*Breaker, len(providerIDs))}
	for index, configuredID := range providerIDs {
		providerID := strings.TrimSpace(configuredID)
		if providerID == "" {
			return nil, fmt.Errorf("breaker provider ID at index %d is empty", index)
		}
		if _, exists := registry.breakers[providerID]; exists {
			return nil, fmt.Errorf("breaker provider ID %q is duplicated", providerID)
		}
		config, ok := configs[providerID]
		if !ok {
			return nil, fmt.Errorf("breaker config for provider %q is missing", providerID)
		}
		breaker, err := NewBreaker(providerID, config)
		if err != nil {
			return nil, err
		}
		registry.breakers[providerID] = breaker
		registry.ids = append(registry.ids, providerID)
	}
	if len(registry.breakers) == 0 {
		return nil, errors.New("breaker registry requires at least one provider")
	}
	sort.Strings(registry.ids)
	return registry, nil
}

func NewBreakerRegistryFromBreakers(configured ...*Breaker) (*BreakerRegistry, error) {
	registry := &BreakerRegistry{breakers: make(map[string]*Breaker, len(configured))}
	for index, breaker := range configured {
		if breaker == nil {
			return nil, fmt.Errorf("breaker at index %d is nil", index)
		}
		providerID := breaker.Snapshot().ProviderID
		if _, exists := registry.breakers[providerID]; exists {
			return nil, fmt.Errorf("breaker provider ID %q is duplicated", providerID)
		}
		registry.breakers[providerID] = breaker
		registry.ids = append(registry.ids, providerID)
	}
	if len(registry.breakers) == 0 {
		return nil, errors.New("breaker registry requires at least one provider")
	}
	sort.Strings(registry.ids)
	return registry, nil
}

func (registry *BreakerRegistry) Allow(providerID string) (*Permit, *Failure) {
	providerID = strings.TrimSpace(providerID)
	if registry == nil || registry.breakers[providerID] == nil {
		return nil, &Failure{
			Category:   CategoryLocalValidation,
			ProviderID: providerID,
			Cause:      ErrUnknownBreakerProvider,
		}
	}
	return registry.breakers[providerID].Allow()
}

func (registry *BreakerRegistry) Snapshot(providerID string) (BreakerSnapshot, bool) {
	if registry == nil {
		return BreakerSnapshot{}, false
	}
	breaker := registry.breakers[strings.TrimSpace(providerID)]
	if breaker == nil {
		return BreakerSnapshot{}, false
	}
	return breaker.Snapshot(), true
}

func (registry *BreakerRegistry) Snapshots() []BreakerSnapshot {
	if registry == nil {
		return nil
	}
	snapshots := make([]BreakerSnapshot, 0, len(registry.ids))
	for _, providerID := range registry.ids {
		snapshots = append(snapshots, registry.breakers[providerID].Snapshot())
	}
	return snapshots
}
