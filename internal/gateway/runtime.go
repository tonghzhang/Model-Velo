package gateway

import (
	"errors"
	"sync/atomic"

	"model-velo/internal/provider"
	"model-velo/internal/reliability"
	"model-velo/internal/routing"
)

// Snapshot is one immutable, internally consistent gateway configuration.
// A request keeps the same snapshot for its entire lifetime.
type Snapshot struct {
	CacheNamespace string
	Routes         *routing.Router
	Chat           *reliability.Orchestrator
	Embeddings     *reliability.Orchestrator
	Breakers       *reliability.BreakerRegistry
	Queues         *reliability.QueueRegistry
	Keys           *reliability.ProviderKeyRegistry
	ProviderStates map[string]ProviderStateIdentity
}

// ProviderStateIdentity records only opaque configuration fingerprints used
// to decide whether a later runtime snapshot may reuse mutable provider state.
type ProviderStateIdentity struct {
	Breaker string
	Queue   string
	Keys    string
}

// Source exposes the currently active runtime snapshot.
type Source interface {
	Current() *Snapshot
}

// Manager atomically publishes fully built snapshots.
type Manager struct {
	current atomic.Pointer[Snapshot]
}

func NewManager(initial *Snapshot) (*Manager, error) {
	if err := validate(initial); err != nil {
		return nil, err
	}
	manager := &Manager{}
	manager.current.Store(initial)
	return manager, nil
}

func (manager *Manager) Current() *Snapshot {
	if manager == nil {
		return nil
	}
	return manager.current.Load()
}

func (manager *Manager) Replace(next *Snapshot) error {
	if manager == nil {
		return errors.New("gateway runtime manager is nil")
	}
	if err := validate(next); err != nil {
		return err
	}
	manager.current.Store(next)
	return nil
}

// NewSnapshot wires a complete routing and reliability graph. Callers should
// finish all validation before publishing the returned snapshot.
func NewSnapshot(
	adapters *provider.AdapterRegistry,
	routes *routing.Router,
	breakers *reliability.BreakerRegistry,
	queues *reliability.QueueRegistry,
	keys *reliability.ProviderKeyRegistry,
	retries reliability.RetryPolicies,
) (*Snapshot, error) {
	if adapters == nil || routes == nil || breakers == nil || queues == nil || retries == nil {
		return nil, errors.New("gateway runtime dependencies are incomplete")
	}
	chat, err := newOrchestrator(adapters, breakers, queues, keys, retries)
	if err != nil {
		return nil, err
	}
	embeddingAdapters, err := adapters.EmbeddingRegistry()
	if err != nil {
		return nil, err
	}
	var embeddings *reliability.Orchestrator
	if embeddingAdapters != nil {
		embeddings, err = newOrchestrator(
			embeddingAdapters, breakers, queues, keys, retries,
		)
		if err != nil {
			return nil, err
		}
	}
	return &Snapshot{
		Routes:     routes,
		Chat:       chat,
		Embeddings: embeddings,
		Breakers:   breakers,
		Queues:     queues,
		Keys:       keys,
	}, nil
}

func newOrchestrator(
	adapters *provider.AdapterRegistry,
	breakers *reliability.BreakerRegistry,
	queues *reliability.QueueRegistry,
	keys *reliability.ProviderKeyRegistry,
	retries reliability.RetryPolicies,
) (*reliability.Orchestrator, error) {
	attempts, err := reliability.NewAttemptExecutor(
		adapters, breakers, queues, keys, retries,
	)
	if err != nil {
		return nil, err
	}
	return reliability.NewOrchestrator(attempts, retries)
}

func validate(snapshot *Snapshot) error {
	if snapshot == nil || snapshot.Routes == nil || snapshot.Chat == nil {
		return errors.New("gateway runtime snapshot is incomplete")
	}
	return nil
}
