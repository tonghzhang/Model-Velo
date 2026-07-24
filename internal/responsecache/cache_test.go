package responsecache

import (
	"context"
	"strings"
	"testing"
)

func TestCacheKeyCanonicalizationAndIsolation(t *testing.T) {
	cache := &Cache{environment: "test", routeVersion: "routes-v1"}
	firstBody := []byte(`{"model":"demo","messages":[{"role":"user","content":"private prompt"}],"temperature":1}`)
	reorderedBody := []byte(`{"temperature":1,"messages":[{"content":"private prompt","role":"user"}],"model":"demo"}`)

	first, cacheable, err := cache.key("tenant-a", "demo", firstBody)
	if err != nil || !cacheable {
		t.Fatalf("key() = %q, %t, %v", first, cacheable, err)
	}
	reordered, cacheable, err := cache.key("tenant-a", "demo", reorderedBody)
	if err != nil || !cacheable {
		t.Fatalf("reordered key() = %q, %t, %v", reordered, cacheable, err)
	}
	if first != reordered {
		t.Fatal("equivalent JSON field order produced different cache keys")
	}
	const wantKey = "model-velo:response-cache:v1:test:tenant:80a707af7dc77ee1228f9127180f3964835e5beb4c4ab0d812f0fe7593579b3a:model:2a97516c354b68848cdbd8f54a226a0a55b21ed138e207ad6c5cbb9c00aa5aea:route:b0df7c40cdf5330693d50652272b98f3c4b192126f15761e0c195ed9386d753a:request:9302423210db4b2c26b69805b85f6f28ffe8ced962c8a7013edcad806d44fb4c"
	if first != wantKey {
		t.Fatalf("key() = %q, want stable vector %q", first, wantKey)
	}
	if strings.Contains(first, "private prompt") || strings.Contains(first, "tenant-a") {
		t.Fatalf("cache key leaked raw input: %q", first)
	}

	differentTenant, _, _ := cache.key("tenant-b", "demo", firstBody)
	differentModel, _, _ := cache.key("tenant-a", "other-model", firstBody)
	differentNumber, _, _ := cache.key("tenant-a", "demo", []byte(`{"model":"demo","messages":[],"temperature":1.0}`))
	if first == differentTenant || first == differentModel || first == differentNumber {
		t.Fatal("distinct tenant, model, or numeric request representation reused a cache key")
	}

	otherRoute := &Cache{environment: "test", routeVersion: "routes-v2"}
	differentRoute, _, _ := otherRoute.key("tenant-a", "demo", firstBody)
	if first == differentRoute {
		t.Fatal("different route version reused a cache key")
	}
}

func TestCacheKeyUsesRequestPinnedRouteVersion(t *testing.T) {
	cache := &Cache{environment: "test", routeVersion: "bootstrap"}
	body := []byte(`{"model":"demo","messages":[]}`)

	bootstrap, _, err := cache.key("tenant-a", "demo", body)
	if err != nil {
		t.Fatal(err)
	}
	pinned, _, err := cache.keyForRouteVersion(
		routeVersionFromContext(
			WithRouteVersion(context.Background(), "managed-runtime-v2"),
			cache.routeVersion,
		),
		"tenant-a",
		"demo",
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap == pinned {
		t.Fatal("managed runtime reused the bootstrap cache namespace")
	}
}

func TestCacheKeyBypassesDuplicateJSONKeys(t *testing.T) {
	cache := &Cache{environment: "test", routeVersion: "routes-v1"}
	key, cacheable, err := cache.key("tenant-a", "demo", []byte(`{"model":"demo","model":"other"}`))
	if err != nil {
		t.Fatalf("key() error = %v", err)
	}
	if key != "" || cacheable {
		t.Fatalf("key() = %q, %t; want bypass", key, cacheable)
	}
}
