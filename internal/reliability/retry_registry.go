package reliability

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type RetryRegistry struct {
	policies       map[string]*RetryPolicy
	providerIDs    []string
	requestTimeout time.Duration
}

func NewRetryRegistry(providerIDs []string, configs map[string]RetryConfig) (*RetryRegistry, error) {
	registry := &RetryRegistry{policies: make(map[string]*RetryPolicy, len(providerIDs))}
	for index, configuredID := range providerIDs {
		providerID := strings.TrimSpace(configuredID)
		if providerID == "" {
			return nil, fmt.Errorf("retry provider ID at index %d is empty", index)
		}
		if _, exists := registry.policies[providerID]; exists {
			return nil, fmt.Errorf("retry provider ID %q is duplicated", providerID)
		}
		config, ok := configs[providerID]
		if !ok {
			return nil, fmt.Errorf("retry config for provider %q is missing", providerID)
		}
		if registry.requestTimeout == 0 {
			registry.requestTimeout = config.RequestTimeout
		} else if config.RequestTimeout != registry.requestTimeout {
			return nil, errors.New("provider retry configs must share one request timeout")
		}
		policy, err := NewRetryPolicy(config)
		if err != nil {
			return nil, fmt.Errorf("retry config for provider %q: %w", providerID, err)
		}
		registry.policies[providerID] = policy
		registry.providerIDs = append(registry.providerIDs, providerID)
	}
	if len(registry.policies) == 0 {
		return nil, errors.New("retry registry requires at least one provider")
	}
	sort.Strings(registry.providerIDs)
	return registry, nil
}

func (registry *RetryRegistry) ForProvider(providerID string) *RetryPolicy {
	if registry == nil {
		return nil
	}
	return registry.policies[strings.TrimSpace(providerID)]
}

func (registry *RetryRegistry) RequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, registry.requestTimeout)
}

func (registry *RetryRegistry) RequestTimeout() time.Duration {
	if registry == nil {
		return 0
	}
	return registry.requestTimeout
}
