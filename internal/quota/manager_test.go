package quota

import (
	"context"
	"errors"
	"testing"
	"time"

	"model-velo/internal/postgres"
)

func TestReserveSkipsDatabaseWithoutEnabledPolicy(t *testing.T) {
	manager := &Manager{policies: newPolicyIndex()}
	decision, err := manager.Reserve(context.Background(), ReserveInput{
		GroupID: "request-1", TenantID: "tenant-1",
		GatewayModel: "model-a",
	})
	if err != nil {
		t.Fatalf("Reserve(no policy) error = %v", err)
	}
	if decision.ReservationID != "" ||
		decision.AppliedPolicies != 0 {
		t.Fatalf("Reserve(no policy) decision = %#v", decision)
	}

	manager.policies.Put(postgres.TenantQuotaPolicy{
		ID: "policy-disabled", TenantID: "tenant-1",
		GatewayModel: "*", Enabled: false,
	})
	if manager.HasPolicy("tenant-1", "model-a") {
		t.Fatal("disabled quota policy was added to the hot-path index")
	}
	manager.policies.Put(postgres.TenantQuotaPolicy{
		ID: "policy-enabled", TenantID: "tenant-1",
		GatewayModel: "*", Enabled: true,
	})
	if !manager.HasPolicy("tenant-1", "model-a") {
		t.Fatal("enabled wildcard quota policy was absent from the index")
	}
}

func TestPolicyWindowsAndLimits(t *testing.T) {
	requestLimit := int64(2)
	tokenLimit := int64(100)
	budget := "1.25"
	policy, err := normalizePolicy(PolicyInput{
		TenantID:     "00000000-0000-4000-8000-000000000001",
		GatewayModel: "*", Period: postgres.QuotaPeriodMonth,
		RequestLimit: &requestLimit, TokenLimit: &tokenLimit,
		BudgetUSD: &budget, OveragePolicy: postgres.QuotaOverageDeny,
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.BudgetNanoUSD == nil || *policy.BudgetNanoUSD != 1_250_000_000 {
		t.Fatalf("budget nanoUSD = %v", policy.BudgetNanoUSD)
	}
	window := postgres.QuotaWindow{
		RequestsSettled: 1, TokensSettled: 40,
		TokensReserved: 20, CostSettled: 1_000_000_000,
	}
	if !exceeds(policy, window, 1, 41, 300_000_000) {
		t.Fatal("combined settled and reserved usage should exceed policy")
	}
	if exceeds(policy, window, 0, 10, 100_000_000) {
		t.Fatal("usage within every policy dimension was rejected")
	}

	at := time.Date(2026, 7, 24, 12, 34, 56, 0, time.FixedZone("local", 8*3600))
	if got := periodStart(at, postgres.QuotaPeriodMonth); !got.Equal(
		time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	) {
		t.Fatalf("monthly window start = %s", got)
	}

	zero := int64(0)
	_, err = normalizePolicy(PolicyInput{
		TenantID: policy.TenantID, GatewayModel: "*",
		Period: postgres.QuotaPeriodDay, RequestLimit: &zero,
		OveragePolicy: postgres.QuotaOverageDeny,
	})
	if !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("zero limit error = %v", err)
	}
}
