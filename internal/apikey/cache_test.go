package apikey

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"model-velo/internal/config"
	"model-velo/internal/postgres"
)

func TestAuthenticationCacheLayersAndFailureSafety(t *testing.T) {
	pepper := bytes.Repeat([]byte("p"), 32)
	token := mustTestToken(t, pepper)
	source := newFakeSnapshotSource(testSnapshot(token))
	shared := newFakeSharedAuthCache()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	manager := newTestCachedManager(source, shared, pepper, &now)

	identity, err := manager.Authenticate(context.Background(), token.plaintext)
	if err != nil {
		t.Fatalf("Authenticate(database fallback) error = %v", err)
	}
	if source.Loads() != 1 || shared.Loads() != 1 || shared.Stores() != 1 {
		t.Fatalf(
			"initial loads source/Redis/store = %d/%d/%d, want 1/1/1",
			source.Loads(),
			shared.Loads(),
			shared.Stores(),
		)
	}
	source.ResetCounts()
	shared.ResetCounts()
	if _, err := manager.Authenticate(
		context.Background(),
		token.plaintext,
	); err != nil {
		t.Fatalf("Authenticate(L1 hit) error = %v", err)
	}
	if source.Loads() != 0 || shared.Loads() != 0 {
		t.Fatalf(
			"L1 hit source/Redis loads = %d/%d, want 0/0",
			source.Loads(),
			shared.Loads(),
		)
	}

	second := newTestCachedManager(source, shared, pepper, &now)
	if _, err := second.Authenticate(
		context.Background(),
		token.plaintext,
	); err != nil {
		t.Fatalf("Authenticate(L2 hit) error = %v", err)
	}
	if source.Loads() != 0 || shared.Loads() != 1 {
		t.Fatalf(
			"L2 hit source/Redis loads = %d/%d, want 0/1",
			source.Loads(),
			shared.Loads(),
		)
	}
	if err := second.AuthorizeModel(
		context.Background(),
		identity,
		"model-a",
	); err != nil {
		t.Fatalf("AuthorizeModel(cached grant) error = %v", err)
	}
	if !errors.Is(
		second.AuthorizeModel(
			context.Background(),
			identity,
			"model-denied",
		),
		ErrModelNotAllowed,
	) {
		t.Fatal("AuthorizeModel() allowed an uncached model grant")
	}

	t.Run("concurrent miss is collapsed", func(t *testing.T) {
		source := newFakeSnapshotSource(testSnapshot(token))
		source.delay = 20 * time.Millisecond
		shared := newFakeSharedAuthCache()
		manager := newTestCachedManager(source, shared, pepper, &now)
		const callers = 64
		var wait sync.WaitGroup
		errorsFound := make(chan error, callers)
		for range callers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, err := manager.Authenticate(
					context.Background(),
					token.plaintext,
				)
				errorsFound <- err
			}()
		}
		wait.Wait()
		close(errorsFound)
		for err := range errorsFound {
			if err != nil {
				t.Fatalf("Authenticate(concurrent miss) error = %v", err)
			}
		}
		if source.Loads() != 1 || shared.Loads() != 1 {
			t.Fatalf(
				"concurrent source/Redis loads = %d/%d, want 1/1",
				source.Loads(),
				shared.Loads(),
			)
		}
	})

	t.Run("Redis error falls back and database error fails closed", func(t *testing.T) {
		source := newFakeSnapshotSource(testSnapshot(token))
		shared := newFakeSharedAuthCache()
		shared.readErr = errors.New("Redis unavailable")
		manager := newTestCachedManager(source, shared, pepper, &now)
		if _, err := manager.Authenticate(
			context.Background(),
			token.plaintext,
		); err != nil {
			t.Fatalf("Authenticate(Redis unavailable) error = %v", err)
		}
		source.err = errors.New("PostgreSQL unavailable")
		manager.credentials.l1.DeleteKey(token.prefix)
		manager = newTestCachedManager(
			source,
			newFakeSharedAuthCache(),
			pepper,
			&now,
		)
		if _, err := manager.Authenticate(
			context.Background(),
			token.plaintext,
		); err == nil {
			t.Fatal("Authenticate(PostgreSQL unavailable) error = nil")
		}

		source = newFakeSnapshotSource(testSnapshot(token))
		shared = newFakeSharedAuthCache()
		shared.writeErr = errors.New("Redis write unavailable")
		manager = newTestCachedManager(source, shared, pepper, &now)
		if _, err := manager.Authenticate(
			context.Background(),
			token.plaintext,
		); err != nil {
			t.Fatalf("Authenticate(Redis write unavailable) error = %v", err)
		}
		source.ResetCounts()
		shared.ResetCounts()
		if _, err := manager.Authenticate(
			context.Background(),
			token.plaintext,
		); err != nil {
			t.Fatalf("Authenticate(L1 after Redis write error) error = %v", err)
		}
		if source.Loads() != 0 || shared.Loads() != 0 {
			t.Fatal("Redis write error prevented safe L1 degradation")
		}
	})

	t.Run("bad payload and both TTLs reload the next layer", func(t *testing.T) {
		source := newFakeSnapshotSource(testSnapshot(token))
		shared := newFakeSharedAuthCache()
		manager := newTestCachedManager(source, shared, pepper, &now)
		cacheKey := manager.credentials.cacheKey(token.lookupDigest)
		shared.SetRaw(cacheKey, []byte(`{"version":999}`))
		if _, err := manager.Authenticate(
			context.Background(),
			token.plaintext,
		); err != nil {
			t.Fatalf("Authenticate(bad L2) error = %v", err)
		}
		if shared.Deletes() == 0 || source.Loads() != 1 {
			t.Fatalf(
				"bad L2 deletes/source loads = %d/%d, want >0/1",
				shared.Deletes(),
				source.Loads(),
			)
		}
		now = now.Add(16 * time.Second)
		if _, err := manager.Authenticate(
			context.Background(),
			token.plaintext,
		); err != nil {
			t.Fatalf("Authenticate(expired L1) error = %v", err)
		}
		if source.Loads() != 1 || shared.Loads() != 2 {
			t.Fatalf(
				"expired L1 source/Redis loads = %d/%d, want 1/2",
				source.Loads(),
				shared.Loads(),
			)
		}
		now = now.Add(15 * time.Second)
		if _, err := manager.Authenticate(
			context.Background(),
			token.plaintext,
		); err != nil {
			t.Fatalf("Authenticate(expired L2) error = %v", err)
		}
		if source.Loads() != 2 {
			t.Fatalf("expired L2 source loads = %d, want 2", source.Loads())
		}
	})

	t.Run("payload contains no plaintext secret or pepper", func(t *testing.T) {
		source := newFakeSnapshotSource(testSnapshot(token))
		shared := newFakeSharedAuthCache()
		manager := newTestCachedManager(source, shared, pepper, &now)
		if _, err := manager.Authenticate(
			context.Background(),
			token.plaintext,
		); err != nil {
			t.Fatal(err)
		}
		payload := shared.LastPayload()
		parsed, _ := parseToken(token.plaintext)
		if bytes.Contains(payload, []byte(token.plaintext)) ||
			bytes.Contains(payload, []byte(parsed.secret)) ||
			bytes.Contains(payload, pepper) {
			t.Fatal("shared cache payload contains plaintext API key or pepper")
		}
	})
}

func TestAuthenticationInvalidationAcrossInstances(t *testing.T) {
	pepper := bytes.Repeat([]byte("q"), 32)
	token := mustTestToken(t, pepper)
	source := newFakeSnapshotSource(testSnapshot(token))
	shared := newFakeSharedAuthCache()
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	instanceA := newTestCachedManager(source, shared, pepper, &now)
	instanceB := newTestCachedManager(source, shared, pepper, &now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instanceA.StartInvalidationListener(ctx)
	instanceB.StartInvalidationListener(ctx)
	shared.WaitForListeners(t, 2)

	identity, err := instanceA.Authenticate(ctx, token.plaintext)
	if err != nil {
		t.Fatal(err)
	}
	revoked := testSnapshot(token)
	revoked.KeyStatus = postgres.APIKeyRevoked
	source.Set(revoked)
	instanceB.credentials.InvalidateKey(ctx, revoked.APIKeyID)
	waitForCondition(t, func() bool {
		_, err := instanceA.Authenticate(ctx, token.plaintext)
		return errors.Is(err, ErrKeyRevoked)
	})

	activeWithoutGrants := revoked
	activeWithoutGrants.KeyStatus = postgres.APIKeyActive
	activeWithoutGrants.AllowedModels = nil
	source.Set(activeWithoutGrants)
	instanceB.credentials.InvalidateTenant(ctx, revoked.TenantID)
	var refreshed Identity
	waitForCondition(t, func() bool {
		var authenticateErr error
		refreshed, authenticateErr = instanceA.Authenticate(
			ctx,
			token.plaintext,
		)
		return authenticateErr == nil
	})
	if !errors.Is(
		instanceA.AuthorizeModel(ctx, refreshed, "model-a"),
		ErrModelNotAllowed,
	) {
		t.Fatal("tenant grant invalidation did not reject the removed model")
	}

	disabledTenant := activeWithoutGrants
	disabledTenant.TenantStatus = postgres.TenantDisabled
	source.Set(disabledTenant)
	instanceB.credentials.InvalidateTenant(ctx, disabledTenant.TenantID)
	waitForCondition(t, func() bool {
		_, err := instanceA.Authenticate(ctx, token.plaintext)
		return errors.Is(err, ErrTenantInactive)
	})
	if identity.APIKeyID == "" {
		t.Fatal("initial identity was incomplete")
	}
}

func TestAuthenticationL1IsBounded(t *testing.T) {
	pepper := bytes.Repeat([]byte("r"), 32)
	first := mustTestToken(t, pepper)
	second := mustTestToken(t, pepper)
	source := newFakeSnapshotSource(
		testSnapshot(first),
		testSnapshot(second),
	)
	shared := newFakeSharedAuthCache()
	now := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	settings := testCacheSettings()
	settings.L1MaxEntries = 1
	manager := newTestManagerWithSettings(
		source,
		shared,
		pepper,
		&now,
		settings,
	)
	for _, plaintext := range []string{
		first.plaintext,
		second.plaintext,
		first.plaintext,
	} {
		if _, err := manager.Authenticate(
			context.Background(),
			plaintext,
		); err != nil {
			t.Fatal(err)
		}
	}
	if shared.Loads() != 3 {
		t.Fatalf("L1 max=1 Redis loads = %d, want 3", shared.Loads())
	}
}

func TestAuthenticationInvalidationBlocksStalePromotion(t *testing.T) {
	pepper := bytes.Repeat([]byte("s"), 32)
	token := mustTestToken(t, pepper)
	source := newFakeSnapshotSource(testSnapshot(token))
	shared := newFakeSharedAuthCache()
	now := time.Date(2026, 7, 28, 14, 30, 0, 0, time.UTC)
	manager := newTestCachedManager(source, shared, pepper, &now)
	load := &cacheLoad{
		snapshot:   testSnapshot(token),
		source:     "postgres",
		generation: manager.credentials.generation.Load(),
	}
	manager.credentials.InvalidateKey(
		context.Background(),
		load.snapshot.APIKeyID,
	)
	shared.ResetCounts()
	manager.credentials.Promote(
		context.Background(),
		manager.credentials.cacheKey(token.lookupDigest),
		load,
	)
	if shared.Stores() != 0 {
		t.Fatal("an invalidated in-flight snapshot was written to Redis")
	}
	if _, result := manager.credentials.l1.Get(
		manager.credentials.cacheKey(token.lookupDigest),
		now,
	); result != "miss" {
		t.Fatalf("stale L1 promotion result = %q, want miss", result)
	}
}

// BenchmarkAuthenticationCachePaths isolates gateway cache and HMAC overhead.
// The fake Redis and snapshot source make command/query counts deterministic;
// their ns/op values do not represent networked Redis or PostgreSQL latency.
func BenchmarkAuthenticationCachePaths(b *testing.B) {
	pepper := bytes.Repeat([]byte("b"), 32)
	token, err := generateToken(pepper)
	if err != nil {
		b.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)

	b.Run("l1_hit", func(b *testing.B) {
		source := newFakeSnapshotSource(testSnapshot(token))
		shared := newFakeSharedAuthCache()
		manager := newTestCachedManager(source, shared, pepper, &now)
		_, _ = manager.Authenticate(context.Background(), token.plaintext)
		source.ResetCounts()
		shared.ResetCounts()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := manager.Authenticate(
				context.Background(),
				token.plaintext,
			); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		reportBenchmarkCounts(b, source, shared, 100)
	})

	b.Run("l2_hit", func(b *testing.B) {
		source := newFakeSnapshotSource(testSnapshot(token))
		shared := newFakeSharedAuthCache()
		manager := newTestCachedManager(source, shared, pepper, &now)
		_, _ = manager.Authenticate(context.Background(), token.plaintext)
		source.ResetCounts()
		shared.ResetCounts()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			manager.credentials.l1.DeleteKey(testSnapshot(token).APIKeyID)
			if _, err := manager.Authenticate(
				context.Background(),
				token.plaintext,
			); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		reportBenchmarkCounts(b, source, shared, 100)
	})

	b.Run("postgres_fallback", func(b *testing.B) {
		source := newFakeSnapshotSource(testSnapshot(token))
		shared := newFakeSharedAuthCache()
		manager := newTestCachedManager(source, shared, pepper, &now)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			manager.credentials.l1.DeleteKey(testSnapshot(token).APIKeyID)
			shared.Clear()
			if _, err := manager.Authenticate(
				context.Background(),
				token.plaintext,
			); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		reportBenchmarkCounts(b, source, shared, 0)
	})

	b.Run("cache_disabled", func(b *testing.B) {
		source := newFakeSnapshotSource(testSnapshot(token))
		manager := &Manager{
			pepper: append([]byte(nil), pepper...),
			now:    func() time.Time { return now },
		}
		manager.credentials = newCredentialStore(
			source,
			config.AuthCache{},
			nil,
			nil,
		)
		manager.credentials.now = manager.now
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := manager.Authenticate(
				context.Background(),
				token.plaintext,
			); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		reportBenchmarkCounts(
			b,
			source,
			newFakeSharedAuthCache(),
			0,
		)
	})
}

func reportBenchmarkCounts(
	b *testing.B,
	source *fakeSnapshotSource,
	shared *fakeSharedAuthCache,
	hitRate float64,
) {
	denominator := float64(b.N)
	b.ReportMetric(float64(source.Queries())/denominator, "pg_queries/op")
	b.ReportMetric(float64(shared.Commands())/denominator, "redis_cmds/op")
	b.ReportMetric(hitRate, "cache_hit_pct")
}

func newTestCachedManager(
	source *fakeSnapshotSource,
	shared *fakeSharedAuthCache,
	pepper []byte,
	now *time.Time,
) *Manager {
	return newTestManagerWithSettings(
		source,
		shared,
		pepper,
		now,
		testCacheSettings(),
	)
}

func newTestManagerWithSettings(
	source snapshotSource,
	shared sharedAuthCache,
	pepper []byte,
	now *time.Time,
	settings config.AuthCache,
) *Manager {
	manager := &Manager{
		pepper: append([]byte(nil), pepper...),
		now:    func() time.Time { return *now },
	}
	manager.credentials = newCredentialStore(
		source,
		settings,
		shared,
		nil,
	)
	manager.credentials.now = manager.now
	return manager
}

func testCacheSettings() config.AuthCache {
	return config.AuthCache{
		Enabled:             true,
		L1MaxEntries:        100,
		L1TTL:               15 * time.Second,
		L2TTL:               30 * time.Second,
		KeyPrefix:           "model-velo:test:auth:v1",
		InvalidationChannel: "model-velo:test:auth:v1:invalidate",
	}
}

func testSnapshot(token generatedToken) authSnapshot {
	return authSnapshot{
		Version:       authSnapshotVersion,
		APIKeyID:      "00000000-0000-4000-8000-000000000011",
		TenantID:      "00000000-0000-4000-8000-000000000022",
		KeyPrefix:     token.prefix,
		LookupDigest:  append([]byte(nil), token.lookupDigest...),
		KeyHash:       append([]byte(nil), token.keyHash...),
		HashVersion:   token.hashVersion,
		KeyStatus:     postgres.APIKeyActive,
		TenantStatus:  postgres.TenantActive,
		TenantVersion: 1,
		AllowedModels: []string{"model-a"},
	}
}

func mustTestToken(t *testing.T, pepper []byte) generatedToken {
	t.Helper()
	token, err := generateToken(pepper)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

type fakeSnapshotSource struct {
	mu        sync.Mutex
	snapshots map[string]authSnapshot
	loads     int
	touches   int
	err       error
	delay     time.Duration
}

func newFakeSnapshotSource(
	snapshots ...authSnapshot,
) *fakeSnapshotSource {
	source := &fakeSnapshotSource{
		snapshots: make(map[string]authSnapshot, len(snapshots)),
	}
	for _, snapshot := range snapshots {
		source.Set(snapshot)
	}
	return source
}

func (source *fakeSnapshotSource) Load(
	ctx context.Context,
	digest []byte,
	now time.Time,
) (authSnapshot, int, error) {
	if source.delay > 0 {
		timer := time.NewTimer(source.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return authSnapshot{}, 0, ctx.Err()
		case <-timer.C:
		}
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	source.loads++
	if source.err != nil {
		return authSnapshot{}, 1, source.err
	}
	snapshot, ok := source.snapshots[hex.EncodeToString(digest)]
	if !ok {
		return authSnapshot{}, 1, errSnapshotNotFound
	}
	snapshot.GeneratedAt = now
	snapshot.CachedUntil = time.Time{}
	return snapshot, 2, nil
}

func (source *fakeSnapshotSource) Touch(
	_ context.Context,
	keyID string,
	now time.Time,
) (int, error) {
	source.mu.Lock()
	source.touches++
	for digest, snapshot := range source.snapshots {
		if snapshot.APIKeyID != keyID {
			continue
		}
		snapshot.LastUsedAt = cloneTime(&now)
		source.snapshots[digest] = snapshot
	}
	source.mu.Unlock()
	return 1, nil
}

func (source *fakeSnapshotSource) Set(snapshot authSnapshot) {
	source.mu.Lock()
	if source.snapshots == nil {
		source.snapshots = make(map[string]authSnapshot)
	}
	source.snapshots[hex.EncodeToString(snapshot.LookupDigest)] = snapshot
	source.mu.Unlock()
}

func (source *fakeSnapshotSource) Loads() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.loads
}

func (source *fakeSnapshotSource) Queries() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.loads*2 + source.touches
}

func (source *fakeSnapshotSource) ResetCounts() {
	source.mu.Lock()
	source.loads = 0
	source.touches = 0
	source.mu.Unlock()
}

type fakeSharedAuthCache struct {
	mu          sync.Mutex
	values      map[string][]byte
	keyIndex    map[string]string
	tenantIndex map[string]map[string]struct{}
	listeners   []chan []byte
	loads       int
	stores      int
	deletes     int
	publishes   int
	readErr     error
	writeErr    error
	lastPayload []byte
}

func newFakeSharedAuthCache() *fakeSharedAuthCache {
	return &fakeSharedAuthCache{
		values:      make(map[string][]byte),
		keyIndex:    make(map[string]string),
		tenantIndex: make(map[string]map[string]struct{}),
	}
}

func (cache *fakeSharedAuthCache) Load(
	_ context.Context,
	key string,
) ([]byte, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.loads++
	if cache.readErr != nil {
		return nil, cache.readErr
	}
	value, ok := cache.values[key]
	if !ok {
		return nil, errSharedCacheMiss
	}
	return append([]byte(nil), value...), nil
}

func (cache *fakeSharedAuthCache) Store(
	_ context.Context,
	cacheKey string,
	keyID string,
	tenantID string,
	payload []byte,
	_ time.Duration,
) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.stores++
	if cache.writeErr != nil {
		return cache.writeErr
	}
	cache.values[cacheKey] = append([]byte(nil), payload...)
	cache.keyIndex[keyID] = cacheKey
	keys := cache.tenantIndex[tenantID]
	if keys == nil {
		keys = make(map[string]struct{})
		cache.tenantIndex[tenantID] = keys
	}
	keys[cacheKey] = struct{}{}
	cache.lastPayload = append([]byte(nil), payload...)
	return nil
}

func (cache *fakeSharedAuthCache) DeleteValue(
	_ context.Context,
	key string,
) error {
	cache.mu.Lock()
	delete(cache.values, key)
	cache.deletes++
	cache.mu.Unlock()
	return nil
}

func (cache *fakeSharedAuthCache) InvalidateKey(
	_ context.Context,
	keyID string,
) error {
	cache.mu.Lock()
	if key := cache.keyIndex[keyID]; key != "" {
		delete(cache.values, key)
	}
	delete(cache.keyIndex, keyID)
	cache.deletes++
	cache.mu.Unlock()
	return nil
}

func (cache *fakeSharedAuthCache) InvalidateTenant(
	_ context.Context,
	tenantID string,
) error {
	cache.mu.Lock()
	for key := range cache.tenantIndex[tenantID] {
		delete(cache.values, key)
	}
	delete(cache.tenantIndex, tenantID)
	cache.deletes++
	cache.mu.Unlock()
	return nil
}

func (cache *fakeSharedAuthCache) Publish(
	_ context.Context,
	_ string,
	payload []byte,
) error {
	cache.mu.Lock()
	listeners := append([]chan []byte(nil), cache.listeners...)
	cache.publishes++
	cache.mu.Unlock()
	for _, listener := range listeners {
		listener <- append([]byte(nil), payload...)
	}
	return nil
}

func (cache *fakeSharedAuthCache) Listen(
	ctx context.Context,
	_ string,
	handle func([]byte),
) error {
	events := make(chan []byte, 16)
	cache.mu.Lock()
	cache.listeners = append(cache.listeners, events)
	cache.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-events:
			handle(event)
		}
	}
}

func (cache *fakeSharedAuthCache) SetRaw(key string, payload []byte) {
	cache.mu.Lock()
	cache.values[key] = append([]byte(nil), payload...)
	cache.mu.Unlock()
}

func (cache *fakeSharedAuthCache) Clear() {
	cache.mu.Lock()
	cache.values = make(map[string][]byte)
	cache.keyIndex = make(map[string]string)
	cache.tenantIndex = make(map[string]map[string]struct{})
	cache.mu.Unlock()
}

func (cache *fakeSharedAuthCache) WaitForListeners(
	t *testing.T,
	want int,
) {
	t.Helper()
	waitForCondition(t, func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return len(cache.listeners) >= want
	})
}

func (cache *fakeSharedAuthCache) Loads() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.loads
}

func (cache *fakeSharedAuthCache) Stores() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.stores
}

func (cache *fakeSharedAuthCache) Deletes() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.deletes
}

func (cache *fakeSharedAuthCache) Commands() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.loads + cache.stores*4
}

func (cache *fakeSharedAuthCache) LastPayload() []byte {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return append([]byte(nil), cache.lastPayload...)
}

func (cache *fakeSharedAuthCache) ResetCounts() {
	cache.mu.Lock()
	cache.loads = 0
	cache.stores = 0
	cache.deletes = 0
	cache.publishes = 0
	cache.mu.Unlock()
}

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
