package provider

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

var ErrUnknownProvider = errors.New("provider adapter is not configured")

type AdapterConfig struct {
	ProviderID string
	Protocol   string
	BaseURL    string
	HTTP       HTTPConfig
}

type AdapterRegistry struct {
	adapters map[string]Adapter
	ids      []string
}

func NewAdapterRegistry(configured []AdapterConfig) (*AdapterRegistry, error) {
	adapters := make(map[string]Adapter, len(configured))
	for index, config := range configured {
		providerID := strings.TrimSpace(config.ProviderID)
		if providerID == "" {
			return nil, fmt.Errorf("provider adapter ID at index %d is empty", index)
		}
		if _, exists := adapters[providerID]; exists {
			return nil, fmt.Errorf("provider adapter ID %q is duplicated", providerID)
		}
		adapter, err := NewAdapter(config)
		if err != nil {
			return nil, fmt.Errorf("configure provider adapter %q: %w", providerID, err)
		}
		adapters[providerID] = adapter
	}
	return NewAdapterRegistryFromAdapters(adapters)
}

func NewAdapterRegistryFromAdapters(configured map[string]Adapter) (*AdapterRegistry, error) {
	registry := &AdapterRegistry{adapters: make(map[string]Adapter, len(configured))}
	for configuredID, adapter := range configured {
		providerID := strings.TrimSpace(configuredID)
		if providerID == "" {
			return nil, errors.New("provider adapter ID is empty")
		}
		if nilAdapter(adapter) {
			return nil, fmt.Errorf("provider adapter %q is nil", providerID)
		}
		if _, exists := registry.adapters[providerID]; exists {
			return nil, fmt.Errorf("provider adapter ID %q is duplicated", providerID)
		}
		registry.adapters[providerID] = adapter
		registry.ids = append(registry.ids, providerID)
	}
	if len(registry.adapters) == 0 {
		return nil, errors.New("provider adapter registry requires at least one provider")
	}
	sort.Strings(registry.ids)
	return registry, nil
}

func nilAdapter(adapter Adapter) bool {
	if adapter == nil {
		return true
	}
	value := reflect.ValueOf(adapter)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func (registry *AdapterRegistry) Adapter(providerID string) (Adapter, bool) {
	if registry == nil {
		return nil, false
	}
	adapter := registry.adapters[strings.TrimSpace(providerID)]
	return adapter, adapter != nil
}

func (registry *AdapterRegistry) ProviderIDs() []string {
	if registry == nil {
		return nil
	}
	return append([]string(nil), registry.ids...)
}

func (registry *AdapterRegistry) KeyedProviderIDs() []string {
	if registry == nil {
		return nil
	}
	providerIDs := make([]string, 0, len(registry.ids))
	for _, providerID := range registry.ids {
		if registry.adapters[providerID].Authentication() == AuthenticationAPIKey {
			providerIDs = append(providerIDs, providerID)
		}
	}
	return providerIDs
}
