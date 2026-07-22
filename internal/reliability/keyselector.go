package reliability

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultProviderKeyCooldown = 30 * time.Second
	maximumKeysPerProvider     = 1_000
)

var providerKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type ProviderKey struct {
	ID     string
	Secret string
}

func (key ProviderKey) String() string {
	return fmt.Sprintf("provider-key<id=%s>", strings.TrimSpace(key.ID))
}

func (key ProviderKey) GoString() string {
	return key.String()
}

type ProviderKeySet struct {
	ProviderID string
	Keys       []ProviderKey
}

func (set ProviderKeySet) String() string {
	return fmt.Sprintf("provider-key-set<provider=%s keys=%d>", strings.TrimSpace(set.ProviderID), len(set.Keys))
}

func (set ProviderKeySet) GoString() string {
	return set.String()
}

type ProviderKeyState string

const (
	ProviderKeyAvailable ProviderKeyState = "available"
	ProviderKeyCooling   ProviderKeyState = "cooldown"
	ProviderKeyDisabled  ProviderKeyState = "disabled"
)

type ProviderKeySnapshot struct {
	ProviderID    string
	KeyID         string
	State         ProviderKeyState
	CooldownUntil time.Time
}

type ProviderKeyRegistry struct {
	selectors map[string]*providerKeySelector
	ids       []string
	now       func() time.Time
}

type providerKeySelector struct {
	providerID string
	keys       []providerKeyEntry
	next       atomic.Uint64
	mu         sync.RWMutex
}

type providerKeyEntry struct {
	id            string
	secret        string
	disabled      bool
	cooldownUntil time.Time
	stateVersion  uint64
}

func NewProviderKeyRegistry(providerIDs []string, configured []ProviderKeySet) (*ProviderKeyRegistry, error) {
	return newProviderKeyRegistry(providerIDs, configured, time.Now)
}

func newProviderKeyRegistry(
	providerIDs []string,
	configured []ProviderKeySet,
	now func() time.Time,
) (*ProviderKeyRegistry, error) {
	if now == nil {
		return nil, errors.New("provider key clock is required")
	}

	expected := make(map[string]struct{}, len(providerIDs))
	for index, providerID := range providerIDs {
		providerID = strings.TrimSpace(providerID)
		if providerID == "" {
			return nil, fmt.Errorf("provider key registry provider ID at index %d is empty", index)
		}
		if _, exists := expected[providerID]; exists {
			return nil, fmt.Errorf("provider key registry provider ID %q is duplicated", providerID)
		}
		expected[providerID] = struct{}{}
	}
	if len(expected) == 0 {
		return nil, errors.New("provider key registry requires at least one provider")
	}

	registry := &ProviderKeyRegistry{
		selectors: make(map[string]*providerKeySelector, len(configured)),
		now:       now,
	}
	for index, set := range configured {
		providerID := strings.TrimSpace(set.ProviderID)
		if _, exists := expected[providerID]; !exists {
			return nil, fmt.Errorf("provider key set %d references unknown provider %q", index, providerID)
		}
		if _, exists := registry.selectors[providerID]; exists {
			return nil, fmt.Errorf("provider key set for provider %q is duplicated", providerID)
		}
		selector, err := newProviderKeySelector(providerID, set.Keys)
		if err != nil {
			return nil, err
		}
		registry.selectors[providerID] = selector
		registry.ids = append(registry.ids, providerID)
	}

	for providerID := range expected {
		if registry.selectors[providerID] == nil {
			return nil, fmt.Errorf("provider %q has no configured keys", providerID)
		}
	}
	sort.Strings(registry.ids)
	return registry, nil
}

func newProviderKeySelector(providerID string, configured []ProviderKey) (*providerKeySelector, error) {
	if len(configured) == 0 {
		return nil, fmt.Errorf("provider %q must contain at least one key", providerID)
	}
	if len(configured) > maximumKeysPerProvider {
		return nil, fmt.Errorf("provider %q contains more than %d keys", providerID, maximumKeysPerProvider)
	}

	selector := &providerKeySelector{
		providerID: providerID,
		keys:       make([]providerKeyEntry, 0, len(configured)),
	}
	seen := make(map[string]struct{}, len(configured))
	for index, key := range configured {
		keyID := strings.TrimSpace(key.ID)
		if !providerKeyIDPattern.MatchString(keyID) {
			return nil, fmt.Errorf("provider %q key at index %d has an invalid ID", providerID, index)
		}
		if _, exists := seen[keyID]; exists {
			return nil, fmt.Errorf("provider %q contains duplicate key ID %q", providerID, keyID)
		}
		secret := strings.TrimSpace(key.Secret)
		if secret == "" {
			return nil, fmt.Errorf("provider %q key %q has an empty secret", providerID, keyID)
		}
		seen[keyID] = struct{}{}
		selector.keys = append(selector.keys, providerKeyEntry{id: keyID, secret: secret})
	}
	return selector, nil
}

func (registry *ProviderKeyRegistry) Select(providerID string) (*ProviderKeySelection, *Failure) {
	return registry.selectKey(providerID, "", nil)
}

func (registry *ProviderKeyRegistry) SelectPreferred(providerID, keyID string) (*ProviderKeySelection, *Failure) {
	return registry.selectKey(providerID, strings.TrimSpace(keyID), nil)
}

func (registry *ProviderKeyRegistry) selectKey(
	providerID string,
	preferredKeyID string,
	excluded map[string]struct{},
) (*ProviderKeySelection, *Failure) {
	providerID = strings.TrimSpace(providerID)
	selector := registry.selectors[providerID]
	if selector == nil {
		return nil, &Failure{Category: CategoryKeyExhausted, ProviderID: providerID}
	}

	now := registry.now()
	selector.mu.RLock()
	defer selector.mu.RUnlock()
	if preferredKeyID != "" {
		for index := range selector.keys {
			key := &selector.keys[index]
			_, skip := excluded[key.id]
			if key.id == preferredKeyID && !skip && !key.disabled && !now.Before(key.cooldownUntil) {
				return newProviderKeySelection(selector, index, registry.now), nil
			}
		}
	}

	start := selector.next.Add(1) - 1
	var earliestCooldown time.Time
	for offset := 0; offset < len(selector.keys); offset++ {
		index := int((start + uint64(offset)) % uint64(len(selector.keys)))
		key := &selector.keys[index]
		if _, skip := excluded[key.id]; skip {
			continue
		}
		if key.disabled {
			continue
		}
		if now.Before(key.cooldownUntil) {
			if earliestCooldown.IsZero() || key.cooldownUntil.Before(earliestCooldown) {
				earliestCooldown = key.cooldownUntil
			}
			continue
		}
		return newProviderKeySelection(selector, index, registry.now), nil
	}

	failure := &Failure{Category: CategoryKeyExhausted, ProviderID: providerID}
	if !earliestCooldown.IsZero() {
		failure.RetryAfter = earliestCooldown.Sub(now)
		failure.RetryAfterSet = true
	}
	return nil, failure
}

func newProviderKeySelection(selector *providerKeySelector, index int, now func() time.Time) *ProviderKeySelection {
	return &ProviderKeySelection{
		selector:     selector,
		index:        index,
		keyID:        selector.keys[index].id,
		stateVersion: selector.keys[index].stateVersion,
		now:          now,
	}
}

func (registry *ProviderKeyRegistry) Snapshots() []ProviderKeySnapshot {
	now := registry.now()
	snapshots := make([]ProviderKeySnapshot, 0)
	for _, providerID := range registry.ids {
		selector := registry.selectors[providerID]
		selector.mu.RLock()
		for _, key := range selector.keys {
			state := ProviderKeyAvailable
			cooldownUntil := time.Time{}
			switch {
			case key.disabled:
				state = ProviderKeyDisabled
			case now.Before(key.cooldownUntil):
				state = ProviderKeyCooling
				cooldownUntil = key.cooldownUntil
			}
			snapshots = append(snapshots, ProviderKeySnapshot{
				ProviderID:    providerID,
				KeyID:         key.id,
				State:         state,
				CooldownUntil: cooldownUntil,
			})
		}
		selector.mu.RUnlock()
	}
	return snapshots
}

func (registry *ProviderKeyRegistry) String() string {
	if registry == nil {
		return "provider-key-registry<nil>"
	}
	return fmt.Sprintf("provider-key-registry<providers=%d>", len(registry.selectors))
}

func (registry *ProviderKeyRegistry) GoString() string {
	return registry.String()
}

type ProviderKeySelection struct {
	selector     *providerKeySelector
	index        int
	keyID        string
	stateVersion uint64
	now          func() time.Time
	completed    atomic.Bool
}

func (selection *ProviderKeySelection) ProviderID() string {
	if selection == nil || selection.selector == nil {
		return ""
	}
	return selection.selector.providerID
}

func (selection *ProviderKeySelection) KeyID() string {
	if selection == nil {
		return ""
	}
	return selection.keyID
}

func (selection *ProviderKeySelection) Secret() string {
	if selection == nil || selection.selector == nil || selection.index < 0 || selection.index >= len(selection.selector.keys) {
		return ""
	}
	return selection.selector.keys[selection.index].secret
}

func (selection *ProviderKeySelection) Complete(failure *Failure) bool {
	if selection == nil || selection.selector == nil || !selection.completed.CompareAndSwap(false, true) {
		return false
	}

	selector := selection.selector
	selector.mu.Lock()
	defer selector.mu.Unlock()
	key := &selector.keys[selection.index]
	switch {
	case failure == nil:
		// A success may only clear the state observed when this key was selected.
		// Otherwise an older in-flight request could erase a newer 429 cooldown.
		if !key.disabled && key.stateVersion == selection.stateVersion {
			key.cooldownUntil = time.Time{}
		}
	case failure.Category == CategoryKeyUnauthorized:
		key.disabled = true
		key.cooldownUntil = time.Time{}
		key.stateVersion++
	case failure.Category == CategoryUpstreamRateLimit && !key.disabled:
		cooldown := defaultProviderKeyCooldown
		if failure.RetryAfterSet {
			cooldown = failure.RetryAfter
		}
		if cooldown > maximumRetryAfter {
			cooldown = maximumRetryAfter
		}
		cooldownUntil := selection.now().Add(cooldown)
		if cooldownUntil.After(key.cooldownUntil) {
			key.cooldownUntil = cooldownUntil
		}
		key.stateVersion++
	}
	return true
}

func (selection *ProviderKeySelection) String() string {
	if selection == nil {
		return "provider-key-selection<nil>"
	}
	return fmt.Sprintf("provider-key-selection<provider=%s key=%s>", selection.ProviderID(), selection.keyID)
}

func (selection *ProviderKeySelection) GoString() string {
	return selection.String()
}
