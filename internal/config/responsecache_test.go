package config

import (
	"testing"
	"time"
)

func TestLoadResponseCache(t *testing.T) {
	tests := []struct {
		name         string
		ttl          string
		routeVersion string
		wantTTL      time.Duration
		wantRoute    string
		wantErr      bool
	}{
		{name: "defaults", wantTTL: 5 * time.Minute, wantRoute: "routes-v1"},
		{name: "configured", ttl: "30s", routeVersion: "routes-2026-07", wantTTL: 30 * time.Second, wantRoute: "routes-2026-07"},
		{name: "disabled", ttl: "off", wantRoute: "routes-v1"},
		{name: "ttl too short", ttl: "500ms", wantErr: true},
		{name: "ttl too long", ttl: "25h", wantErr: true},
		{name: "invalid route version", routeVersion: "route/version", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(responseCacheTTLEnv, test.ttl)
			t.Setenv(responseCacheRouteVersionEnv, test.routeVersion)

			got, err := LoadResponseCache()
			if test.wantErr {
				if err == nil {
					t.Fatal("LoadResponseCache() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadResponseCache() error = %v", err)
			}
			if got.TTL != test.wantTTL || got.RouteVersion != test.wantRoute {
				t.Fatalf("LoadResponseCache() = %#v, want TTL=%s route=%q", got, test.wantTTL, test.wantRoute)
			}
		})
	}
}
