package quota

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"model-velo/internal/postgres"
)

type policyIndex struct {
	mu       sync.RWMutex
	policies map[string]postgres.TenantQuotaPolicy
	matches  map[string]map[string]int
}

func newPolicyIndex() *policyIndex {
	return &policyIndex{
		policies: make(map[string]postgres.TenantQuotaPolicy),
		matches:  make(map[string]map[string]int),
	}
}

func (index *policyIndex) Replace(
	policies []postgres.TenantQuotaPolicy,
) {
	next := newPolicyIndex()
	for _, policy := range policies {
		next.put(policy)
	}
	index.mu.Lock()
	index.policies = next.policies
	index.matches = next.matches
	index.mu.Unlock()
}

func (index *policyIndex) Put(policy postgres.TenantQuotaPolicy) {
	index.mu.Lock()
	defer index.mu.Unlock()
	index.remove(policy.ID)
	index.put(policy)
}

func (index *policyIndex) Has(tenantID, model string) bool {
	index.mu.RLock()
	defer index.mu.RUnlock()
	models := index.matches[tenantID]
	return models[model] > 0 || models["*"] > 0
}

func (index *policyIndex) put(policy postgres.TenantQuotaPolicy) {
	index.policies[policy.ID] = policy
	if !policy.Enabled {
		return
	}
	models := index.matches[policy.TenantID]
	if models == nil {
		models = make(map[string]int)
		index.matches[policy.TenantID] = models
	}
	models[policy.GatewayModel]++
}

func (index *policyIndex) remove(policyID string) {
	current, ok := index.policies[policyID]
	if !ok {
		return
	}
	delete(index.policies, policyID)
	if !current.Enabled {
		return
	}
	models := index.matches[current.TenantID]
	models[current.GatewayModel]--
	if models[current.GatewayModel] <= 0 {
		delete(models, current.GatewayModel)
	}
	if len(models) == 0 {
		delete(index.matches, current.TenantID)
	}
}

func (manager *Manager) LoadPolicyIndex(ctx context.Context) error {
	var policies []postgres.TenantQuotaPolicy
	if err := manager.database.WithContext(ctx).
		Where("enabled = ?", true).
		Find(&policies).Error; err != nil {
		return errors.New("load quota policy index")
	}
	manager.policies.Replace(policies)
	return nil
}

func (manager *Manager) RunPolicyIndexRefresh(
	ctx context.Context,
	interval time.Duration,
) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = manager.LoadPolicyIndex(ctx)
		}
	}
}

func (manager *Manager) HasPolicy(tenantID, model string) bool {
	if manager == nil || manager.policies == nil {
		return false
	}
	return manager.policies.Has(
		strings.TrimSpace(tenantID),
		strings.TrimSpace(model),
	)
}
