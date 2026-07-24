package controlplane

import (
	"testing"
	"time"

	"model-velo/internal/config"
	"model-velo/internal/provider"
	"model-velo/internal/reliability"
)

func TestRuntimeBuildAndSecretRedaction(t *testing.T) {
	document := RuntimeDocument{
		SchemaVersion: RuntimeSchemaVersion,
		Providers: []ProviderSpec{{
			ID: "primary", Protocol: provider.ProtocolOpenAICompatible,
			BaseURL: "https://example.com", Models: []string{"gateway-model"},
			ModelCapabilities: map[string][]provider.Capability{
				"gateway-model": {
					provider.CapabilityText, provider.CapabilityTools,
				},
			},
			Keys: []ProviderKeySpec{{ID: "key-1", Secret: "provider-secret"}},
		}},
		Routes: []RouteSpec{{
			Model:      "gateway-model",
			Candidates: []CandidateSpec{{Provider: "primary"}},
		}},
	}
	builder := Builder{
		Defaults: config.ProviderDefaults{
			Breaker: reliability.DefaultBreakerConfig(),
			Queue:   reliability.DefaultQueueConfig(),
			Retry:   reliability.DefaultRetryConfig(),
			HTTP:    provider.DefaultHTTPConfig(),
		},
		EnforceStreamUsage: true,
	}
	snapshot, err := builder.Build(document)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Chat == nil || snapshot.Routes == nil ||
		len(snapshot.Breakers.Snapshots()) != 1 {
		t.Fatal("runtime snapshot is incomplete")
	}
	public := redactRuntime(document)
	if public.Providers[0].Keys[0].Secret != "" ||
		document.Providers[0].Keys[0].Secret != "provider-secret" {
		t.Fatal("runtime redaction mutated or exposed the provider secret")
	}
	merged, err := mergeRuntimeSecrets(public, document)
	if err != nil || merged.Providers[0].Keys[0].Secret != "provider-secret" {
		t.Fatalf("secret preservation failed: %v", err)
	}

	zero := 0
	jitter := float64(0)
	document.Providers[0].Runtime.Queue.MaxWaiting = &zero
	document.Providers[0].Runtime.Retry.JitterRatio = &jitter
	document.Providers[0].Runtime.Queue.WaitTimeout = (10 * time.Millisecond).String()
	if _, err := builder.Build(document); err != nil {
		t.Fatalf("valid zero queue/jitter overrides were rejected: %v", err)
	}
}

func TestRuntimeBuildReusesCompatibleProviderState(t *testing.T) {
	document := RuntimeDocument{
		SchemaVersion: RuntimeSchemaVersion,
		Providers: []ProviderSpec{{
			ID:       "primary",
			Protocol: provider.ProtocolOpenAICompatible,
			BaseURL:  "https://example.com",
			Models:   []string{"gateway-model"},
			Keys: []ProviderKeySpec{{
				ID: "key-1", Secret: "provider-secret",
			}},
		}},
		Routes: []RouteSpec{{
			Model:      "gateway-model",
			Candidates: []CandidateSpec{{Provider: "primary"}},
		}},
	}
	builder := Builder{
		Defaults: config.ProviderDefaults{
			Breaker: reliability.DefaultBreakerConfig(),
			Queue:   reliability.DefaultQueueConfig(),
			Retry:   reliability.DefaultRetryConfig(),
			HTTP:    provider.DefaultHTTPConfig(),
		},
	}
	first, err := builder.Build(document)
	if err != nil {
		t.Fatal(err)
	}

	lease, failure := first.Queues.Acquire(t.Context(), "primary")
	if failure != nil {
		t.Fatalf("Acquire() failure = %v", failure)
	}
	key, failure := first.Keys.Select("primary")
	if failure != nil {
		t.Fatalf("Select() failure = %v", failure)
	}
	key.Complete(&reliability.Failure{
		Category: reliability.CategoryKeyUnauthorized,
	})
	for range reliability.DefaultBreakerConfig().FailureThreshold {
		permit, breakerFailure := first.Breakers.Allow("primary")
		if breakerFailure != nil {
			t.Fatalf("Allow() failure = %v", breakerFailure)
		}
		permit.Complete(&reliability.Failure{
			Category: reliability.CategoryNetwork,
		})
	}

	document.Routes[0].Candidates[0].UpstreamModel = "gateway-model"
	next, err := builder.build(document, first)
	if err != nil {
		t.Fatal(err)
	}
	queue, _ := next.Queues.Snapshot("primary")
	if queue.Active != 1 {
		t.Fatalf("reloaded queue active = %d, want 1", queue.Active)
	}
	breaker, _ := next.Breakers.Snapshot("primary")
	if breaker.State != reliability.StateOpen {
		t.Fatalf("reloaded breaker state = %s, want %s", breaker.State, reliability.StateOpen)
	}
	keys := next.Keys.Snapshots()
	if len(keys) != 1 || keys[0].State != reliability.ProviderKeyDisabled {
		t.Fatalf("reloaded key state = %#v, want disabled", keys)
	}
	lease.Release()
	queue, _ = next.Queues.Snapshot("primary")
	if queue.Active != 0 {
		t.Fatalf("released queue active = %d, want 0", queue.Active)
	}

	document.Providers[0].Keys[0].Secret = "replacement-secret"
	withNewKey, err := builder.build(document, next)
	if err != nil {
		t.Fatal(err)
	}
	keys = withNewKey.Keys.Snapshots()
	if len(keys) != 1 || keys[0].State != reliability.ProviderKeyAvailable {
		t.Fatalf("replacement key state = %#v, want available", keys)
	}

	document.Providers[0].BaseURL = "https://replacement.example.com"
	withNewEndpoint, err := builder.build(document, withNewKey)
	if err != nil {
		t.Fatal(err)
	}
	breaker, _ = withNewEndpoint.Breakers.Snapshot("primary")
	if breaker.State != reliability.StateClosed {
		t.Fatalf("replacement endpoint breaker = %s, want %s", breaker.State, reliability.StateClosed)
	}
}
