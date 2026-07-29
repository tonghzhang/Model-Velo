package apikey

import (
	"container/list"
	"sync"
	"time"
)

type l1Cache struct {
	mu         sync.Mutex
	maxEntries int
	ttl        time.Duration
	entries    map[string]*list.Element
	keys       map[string]map[string]struct{}
	tenants    map[string]map[string]struct{}
	order      *list.List
}

type l1Entry struct {
	cacheKey  string
	snapshot  authSnapshot
	expiresAt time.Time
}

func newL1Cache(maxEntries int, ttl time.Duration) *l1Cache {
	return &l1Cache{
		maxEntries: maxEntries,
		ttl:        ttl,
		entries:    make(map[string]*list.Element, maxEntries),
		keys:       make(map[string]map[string]struct{}),
		tenants:    make(map[string]map[string]struct{}),
		order:      list.New(),
	}
}

func (cache *l1Cache) Get(
	cacheKey string,
	now time.Time,
) (authSnapshot, string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	element, ok := cache.entries[cacheKey]
	if !ok {
		return authSnapshot{}, "miss"
	}
	entry := element.Value.(*l1Entry)
	if !entry.expiresAt.After(now) {
		cache.remove(element)
		return authSnapshot{}, "expired"
	}
	cache.order.MoveToFront(element)
	return entry.snapshot, "hit"
}

func (cache *l1Cache) Set(
	cacheKey string,
	snapshot authSnapshot,
	now time.Time,
) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	expiresAt := now.Add(cache.ttl)
	if !snapshot.CachedUntil.IsZero() &&
		snapshot.CachedUntil.Before(expiresAt) {
		expiresAt = snapshot.CachedUntil
	}
	if element, ok := cache.entries[cacheKey]; ok {
		cache.remove(element)
	}
	entry := &l1Entry{
		cacheKey: cacheKey, snapshot: snapshot, expiresAt: expiresAt,
	}
	element := cache.order.PushFront(entry)
	cache.entries[cacheKey] = element
	addCacheIndex(cache.keys, snapshot.APIKeyID, cacheKey)
	addCacheIndex(cache.tenants, snapshot.TenantID, cacheKey)

	if cache.order.Len() <= cache.maxEntries {
		return false
	}
	cache.remove(cache.order.Back())
	return true
}

func (cache *l1Cache) DeleteKey(keyID string) int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.deleteIndexed(cache.keys[keyID])
}

func (cache *l1Cache) DeleteTenant(tenantID string) int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.deleteIndexed(cache.tenants[tenantID])
}

func (cache *l1Cache) deleteIndexed(cacheKeys map[string]struct{}) int {
	removed := 0
	for cacheKey := range cacheKeys {
		element, ok := cache.entries[cacheKey]
		if !ok {
			continue
		}
		cache.remove(element)
		removed++
	}
	return removed
}

func (cache *l1Cache) remove(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*l1Entry)
	delete(cache.entries, entry.cacheKey)
	deleteCacheIndex(cache.keys, entry.snapshot.APIKeyID, entry.cacheKey)
	deleteCacheIndex(cache.tenants, entry.snapshot.TenantID, entry.cacheKey)
	cache.order.Remove(element)
}

func addCacheIndex(
	index map[string]map[string]struct{},
	id string,
	cacheKey string,
) {
	cacheKeys := index[id]
	if cacheKeys == nil {
		cacheKeys = make(map[string]struct{})
		index[id] = cacheKeys
	}
	cacheKeys[cacheKey] = struct{}{}
}

func deleteCacheIndex(
	index map[string]map[string]struct{},
	id string,
	cacheKey string,
) {
	cacheKeys := index[id]
	delete(cacheKeys, cacheKey)
	if len(cacheKeys) == 0 {
		delete(index, id)
	}
}
