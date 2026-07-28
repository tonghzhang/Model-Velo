package apikey

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"model-velo/internal/config"
)

const (
	authCacheLoadTimeout    = 5 * time.Second
	authCacheWriteTimeout   = 2 * time.Second
	invalidationRetryPeriod = time.Second
	invalidationVersion     = 1
)

type CacheObserver interface {
	AuthCacheLookup(layer, result string, duration time.Duration)
	AuthCacheEvent(event, result string)
	AuthPostgresFallback(result string)
	AuthDatabaseQueries(count int)
	ModelAuthorization(result string)
}

type credentialStore struct {
	enabled  bool
	settings config.AuthCache
	l1       *l1Cache
	shared   sharedAuthCache
	source   snapshotSource
	observer CacheObserver
	loads    singleflight.Group
	now      func() time.Time
	start    sync.Once

	invalidationMu sync.RWMutex
	generation     atomic.Uint64
}

type cacheLoad struct {
	snapshot    authSnapshot
	source      string
	queries     int
	err         error
	generation  uint64
	promoteOnce sync.Once
	touchOnce   sync.Once
}

type invalidationEvent struct {
	Version int    `json:"version"`
	EventID string `json:"event_id"`
	Scope   string `json:"scope"`
	ID      string `json:"id"`
}

func newCredentialStore(
	source snapshotSource,
	settings config.AuthCache,
	shared sharedAuthCache,
	observer CacheObserver,
) *credentialStore {
	store := &credentialStore{
		enabled: settings.Enabled, settings: settings,
		shared: shared, source: source, observer: observer, now: time.Now,
	}
	if settings.Enabled {
		store.l1 = newL1Cache(settings.L1MaxEntries, settings.L1TTL)
	}
	return store
}

func newRedisCredentialStore(
	source snapshotSource,
	client *goredis.Client,
	settings config.AuthCache,
	observer CacheObserver,
) (*credentialStore, error) {
	if client == nil {
		return nil, errors.New("authentication cache requires Redis")
	}
	if !settings.Enabled || settings.L1MaxEntries <= 0 ||
		settings.L1TTL <= 0 || settings.L2TTL < settings.L1TTL ||
		settings.KeyPrefix == "" || settings.InvalidationChannel == "" {
		return nil, errors.New("authentication cache settings are invalid")
	}
	return newCredentialStore(
		source,
		settings,
		redisAuthCache{client: client, prefix: settings.KeyPrefix},
		observer,
	), nil
}

func (store *credentialStore) Lookup(
	ctx context.Context,
	lookupDigest []byte,
) (*cacheLoad, bool) {
	cacheKey := store.cacheKey(lookupDigest)
	if !store.enabled {
		snapshot, queries, err := store.source.Load(
			ctx, lookupDigest, store.now().UTC(),
		)
		result := "success"
		if errors.Is(err, errSnapshotNotFound) {
			result = "not_found"
		} else if err != nil {
			result = "error"
		}
		store.observePostgresFallback(result)
		return &cacheLoad{
			snapshot: snapshot, source: "postgres",
			queries: queries, err: err,
		}, true
	}

	startedAt := time.Now()
	if snapshot, result := store.l1.Get(cacheKey, store.now().UTC()); result == "hit" {
		store.observeLookup("l1", result, time.Since(startedAt))
		return &cacheLoad{snapshot: snapshot, source: "l1"}, false
	} else {
		store.observeLookup("l1", result, time.Since(startedAt))
	}

	var leader atomic.Bool
	resultChannel := store.loads.DoChan(cacheKey, func() (any, error) {
		leader.Store(true)
		loadContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			authCacheLoadTimeout,
		)
		defer cancel()
		return store.load(loadContext, cacheKey, lookupDigest), nil
	})
	select {
	case result := <-resultChannel:
		return result.Val.(*cacheLoad), leader.Load()
	case <-ctx.Done():
		return &cacheLoad{err: ctx.Err()}, false
	}
}

func (store *credentialStore) load(
	ctx context.Context,
	cacheKey string,
	lookupDigest []byte,
) *cacheLoad {
	generation := store.generation.Load()
	if snapshot, result := store.l1.Get(cacheKey, store.now().UTC()); result == "hit" {
		return &cacheLoad{
			snapshot: snapshot, source: "l1", generation: generation,
		}
	}

	startedAt := time.Now()
	payload, err := store.shared.Load(ctx, cacheKey)
	switch {
	case err == nil:
		var snapshot authSnapshot
		decodeErr := json.Unmarshal(payload, &snapshot)
		if decodeErr == nil {
			decodeErr = snapshot.validate(
				lookupDigest,
				store.now().UTC(),
				true,
			)
		}
		if decodeErr == nil {
			store.observeLookup("l2", "hit", time.Since(startedAt))
			store.invalidationMu.RLock()
			if store.generation.Load() == generation {
				store.setL1(cacheKey, snapshot)
			}
			store.invalidationMu.RUnlock()
			return &cacheLoad{
				snapshot: snapshot, source: "l2", generation: generation,
			}
		}
		store.observeLookup("l2", "error", time.Since(startedAt))
		store.observeEvent("invalid_payload", "deleted")
		deleteContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			authCacheWriteTimeout,
		)
		if deleteErr := store.shared.DeleteValue(
			deleteContext,
			cacheKey,
		); deleteErr != nil {
			store.observeEvent("delete", "error")
		}
		cancel()
	case errors.Is(err, errSharedCacheMiss):
		store.observeLookup("l2", "miss", time.Since(startedAt))
	default:
		store.observeLookup("l2", "error", time.Since(startedAt))
	}

	snapshot, queries, sourceErr := store.source.Load(
		ctx,
		lookupDigest,
		store.now().UTC(),
	)
	fallbackResult := "success"
	if errors.Is(sourceErr, errSnapshotNotFound) {
		fallbackResult = "not_found"
	} else if sourceErr != nil {
		fallbackResult = "error"
	}
	store.observePostgresFallback(fallbackResult)
	return &cacheLoad{
		snapshot: snapshot, source: "postgres",
		queries: queries, err: sourceErr, generation: generation,
	}
}

func (store *credentialStore) Promote(
	ctx context.Context,
	cacheKey string,
	load *cacheLoad,
) {
	if !store.enabled || load == nil || load.err != nil ||
		load.source != "postgres" {
		return
	}
	store.invalidationMu.RLock()
	defer store.invalidationMu.RUnlock()
	if store.generation.Load() != load.generation {
		return
	}
	load.promoteOnce.Do(func() {
		now := store.now().UTC()
		snapshot := load.snapshot
		snapshot.CachedUntil = now.Add(store.settings.L2TTL)
		payload, err := json.Marshal(snapshot)
		if err == nil {
			writeContext, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				authCacheWriteTimeout,
			)
			err = store.shared.Store(
				writeContext,
				cacheKey,
				snapshot.APIKeyID,
				snapshot.TenantID,
				payload,
				store.settings.L2TTL,
			)
			cancel()
		}
		if err != nil {
			store.observeEvent("write", "error")
		} else {
			store.observeEvent("write", "success")
		}
		store.setL1(cacheKey, snapshot)
	})
}

func (store *credentialStore) Touch(
	ctx context.Context,
	load *cacheLoad,
	now time.Time,
) int {
	if load == nil || load.source != "postgres" ||
		load.snapshot.APIKeyID == "" {
		return 0
	}
	performed := false
	queries := 0
	load.touchOnce.Do(func() {
		performed = true
		touchContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			authCacheWriteTimeout,
		)
		defer cancel()
		queries, _ = store.source.Touch(
			touchContext,
			load.snapshot.APIKeyID,
			now,
		)
	})
	if !performed {
		return 0
	}
	return queries
}

func (store *credentialStore) Start(ctx context.Context) {
	if !store.enabled || store.shared == nil {
		return
	}
	store.start.Do(func() {
		go store.listen(ctx)
	})
}

func (store *credentialStore) InvalidateKey(
	ctx context.Context,
	keyID string,
) {
	if !store.enabled {
		return
	}
	store.invalidationMu.Lock()
	defer store.invalidationMu.Unlock()
	store.generation.Add(1)
	store.l1.DeleteKey(keyID)
	store.invalidate(ctx, "key", keyID)
}

func (store *credentialStore) InvalidateTenant(
	ctx context.Context,
	tenantID string,
) {
	if !store.enabled {
		return
	}
	store.invalidationMu.Lock()
	defer store.invalidationMu.Unlock()
	store.generation.Add(1)
	store.l1.DeleteTenant(tenantID)
	store.invalidate(ctx, "tenant", tenantID)
}

func (store *credentialStore) invalidate(
	ctx context.Context,
	scope string,
	id string,
) {
	writeContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		authCacheWriteTimeout,
	)
	defer cancel()
	var err error
	switch scope {
	case "key":
		err = store.shared.InvalidateKey(writeContext, id)
	case "tenant":
		err = store.shared.InvalidateTenant(writeContext, id)
	}
	if err != nil {
		store.observeEvent("invalidation", "error")
	} else {
		store.observeEvent("invalidation", "success")
	}
	eventID, eventErr := randomUUID()
	if eventErr != nil {
		store.observeEvent("publish", "error")
		return
	}
	payload, eventErr := json.Marshal(invalidationEvent{
		Version: invalidationVersion,
		EventID: eventID,
		Scope:   scope,
		ID:      id,
	})
	if eventErr != nil ||
		store.shared.Publish(
			writeContext,
			store.settings.InvalidationChannel,
			payload,
		) != nil {
		store.observeEvent("publish", "error")
		return
	}
	store.observeEvent("publish", "success")
}

func (store *credentialStore) listen(ctx context.Context) {
	for ctx.Err() == nil {
		err := store.shared.Listen(
			ctx,
			store.settings.InvalidationChannel,
			store.handleInvalidation,
		)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			store.observeEvent("subscription", "error")
		}
		timer := time.NewTimer(invalidationRetryPeriod)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (store *credentialStore) handleInvalidation(payload []byte) {
	var event invalidationEvent
	if json.Unmarshal(payload, &event) != nil ||
		event.Version != invalidationVersion ||
		event.EventID == "" || event.ID == "" {
		store.observeEvent("invalidation_message", "error")
		return
	}
	switch event.Scope {
	case "key":
		store.invalidationMu.Lock()
		store.generation.Add(1)
		store.l1.DeleteKey(event.ID)
		store.deleteSharedAfterEvent("key", event.ID)
		store.invalidationMu.Unlock()
	case "tenant":
		store.invalidationMu.Lock()
		store.generation.Add(1)
		store.l1.DeleteTenant(event.ID)
		store.deleteSharedAfterEvent("tenant", event.ID)
		store.invalidationMu.Unlock()
	default:
		store.observeEvent("invalidation_message", "error")
		return
	}
	store.observeEvent("invalidation_message", "success")
}

func (store *credentialStore) deleteSharedAfterEvent(
	scope string,
	id string,
) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		authCacheWriteTimeout,
	)
	defer cancel()
	var err error
	if scope == "key" {
		err = store.shared.InvalidateKey(ctx, id)
	} else {
		err = store.shared.InvalidateTenant(ctx, id)
	}
	if err != nil {
		store.observeEvent("invalidation_recheck", "error")
	} else {
		store.observeEvent("invalidation_recheck", "success")
	}
}

func (store *credentialStore) setL1(
	cacheKey string,
	snapshot authSnapshot,
) {
	if store.l1.Set(cacheKey, snapshot, store.now().UTC()) {
		store.observeEvent("eviction", "success")
	}
}

func (store *credentialStore) cacheKey(lookupDigest []byte) string {
	return store.settings.KeyPrefix + ":snapshot:" +
		hex.EncodeToString(lookupDigest)
}

func (store *credentialStore) observeLookup(
	layer string,
	result string,
	duration time.Duration,
) {
	if store.observer != nil {
		store.observer.AuthCacheLookup(layer, result, duration)
	}
}

func (store *credentialStore) observeEvent(event, result string) {
	if store.observer != nil {
		store.observer.AuthCacheEvent(event, result)
	}
}

func (store *credentialStore) observePostgresFallback(result string) {
	if store.observer != nil {
		store.observer.AuthPostgresFallback(result)
	}
}
