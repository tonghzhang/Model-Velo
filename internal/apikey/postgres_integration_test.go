package apikey

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"model-velo/internal/config"
	dbstore "model-velo/internal/postgres"
	quotastore "model-velo/internal/quota"
	"model-velo/internal/usage"
)

const postgresTestDSNEnv = "MODEL_VELO_POSTGRES_TEST_DSN"

func TestPostgresAPIKeyLifecycle(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := database.SyncSchema(ctx); err != nil {
		t.Fatalf("first SyncSchema() error = %v", err)
	}
	if err := database.SyncSchema(ctx); err != nil {
		t.Fatalf("second SyncSchema() error = %v", err)
	}
	assertPostgresSchema(t, database.ORM())

	pepper := bytes.Repeat([]byte{0x6b}, 32)
	manager, err := NewManager(database.ORM(), pepper)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	issued, err := manager.BootstrapTenant(ctx, BootstrapTenantInput{
		Slug:        "integration-tenant",
		DisplayName: "Integration Tenant",
		KeyLabel:    "primary",
		Models:      []string{"model-a", "model-b"},
	})
	if err != nil {
		t.Fatalf("BootstrapTenant() error = %v", err)
	}

	pricing, err := usage.NewPricingCatalog(nil)
	if err != nil {
		t.Fatalf("NewPricingCatalog() error = %v", err)
	}
	quotaManager, err := quotastore.NewManager(
		database.ORM(),
		pricing,
		config.Quota{
			ReservationTTL:         15 * time.Minute,
			ReapInterval:           time.Minute,
			DefaultMaxOutputTokens: 4096,
		},
	)
	if err != nil {
		t.Fatalf("quota.NewManager() error = %v", err)
	}
	requestLimit := int64(10)
	quotaInput := quotastore.PolicyInput{
		TenantID:     "00000000-0000-4000-8000-000000000000",
		GatewayModel: "*", Period: dbstore.QuotaPeriodMonth,
		RequestLimit:  &requestLimit,
		OveragePolicy: dbstore.QuotaOverageDeny,
		Enabled:       true,
	}
	const quotaActorID = "00000000-0000-4000-8000-000000000099"
	_, err = quotaManager.CreatePolicy(ctx, quotaInput, quotaActorID)
	requireErrorIs(t, err, quotastore.ErrTenantNotFound)
	quotaInput.TenantID = issued.TenantID
	if _, err := quotaManager.CreatePolicy(
		ctx, quotaInput, quotaActorID,
	); err != nil {
		t.Fatalf("CreatePolicy(existing tenant) error = %v", err)
	}
	if issued.Plaintext == "" || issued.ID == "" || issued.TenantID == "" {
		t.Fatal("BootstrapTenant() returned incomplete key metadata")
	}
	baseTime := time.Now().UTC().Add(time.Second)
	manager.now = func() time.Time { return baseTime }

	identity, err := manager.Authenticate(ctx, issued.Plaintext)
	if err != nil {
		t.Fatalf("Authenticate(valid key) error = %v", err)
	}
	if identity.TenantID != issued.TenantID || identity.APIKeyID != issued.ID || identity.KeyPrefix != issued.Prefix {
		t.Errorf("Authenticate(valid key) identity = %+v, want tenant %q key %q prefix %q", identity, issued.TenantID, issued.ID, issued.Prefix)
	}
	if err := manager.AuthorizeModel(ctx, issued.TenantID, "model-a"); err != nil {
		t.Fatalf("AuthorizeModel(allowed) error = %v", err)
	}
	requireErrorIs(t, manager.AuthorizeModel(ctx, issued.TenantID, "model-denied"), ErrModelNotAllowed)

	unknown, err := generateToken(pepper)
	if err != nil {
		t.Fatalf("generate unknown token: %v", err)
	}
	_, err = manager.Authenticate(ctx, unknown.plaintext)
	requireErrorIs(t, err, ErrInvalidCredential)

	issuedParts, err := parseToken(issued.Plaintext)
	if err != nil {
		t.Fatalf("parse issued key: %v", err)
	}
	wrongSecretParts, err := parseToken(unknown.plaintext)
	if err != nil {
		t.Fatalf("parse unknown key: %v", err)
	}
	_, err = manager.Authenticate(ctx, issuedParts.prefix+"_"+wrongSecretParts.secret)
	requireErrorIs(t, err, ErrInvalidCredential)

	_, err = manager.BootstrapTenant(ctx, BootstrapTenantInput{
		Slug:        "integration-tenant",
		DisplayName: "Duplicate",
		KeyLabel:    "duplicate",
		Models:      []string{"model-a"},
	})
	requireErrorIs(t, err, ErrTenantAlreadyExists)

	_, err = manager.CreateKey(ctx, CreateKeyInput{TenantID: "00000000-0000-4000-8000-000000000000", Label: "missing tenant"})
	requireErrorIs(t, err, ErrTenantNotFound)

	disabledKey, err := manager.CreateKey(ctx, CreateKeyInput{TenantID: issued.TenantID, Label: "disabled"})
	if err != nil {
		t.Fatalf("CreateKey(disabled case) error = %v", err)
	}
	if err := manager.Disable(ctx, disabledKey.ID); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	_, err = manager.Authenticate(ctx, disabledKey.Plaintext)
	requireErrorIs(t, err, ErrKeyInactive)

	if err := manager.Revoke(ctx, issued.ID); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	_, err = manager.Authenticate(ctx, issued.Plaintext)
	requireErrorIs(t, err, ErrKeyRevoked)
	requireErrorIs(t, manager.Revoke(ctx, issued.ID), ErrKeyRevoked)
	requireErrorIs(t, manager.Disable(ctx, issued.ID), ErrKeyRevoked)

	expiresAt := baseTime.Add(time.Hour)
	expiringKey, err := manager.CreateKey(ctx, CreateKeyInput{
		TenantID:  issued.TenantID,
		Label:     "expiring",
		ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatalf("CreateKey(expiring) error = %v", err)
	}
	manager.now = func() time.Time { return expiresAt }
	_, err = manager.Authenticate(ctx, expiringKey.Plaintext)
	requireErrorIs(t, err, ErrKeyExpired)

	manager.now = func() time.Time { return baseTime }
	tenantDisabledKey, err := manager.CreateKey(ctx, CreateKeyInput{TenantID: issued.TenantID, Label: "tenant disabled"})
	if err != nil {
		t.Fatalf("CreateKey(tenant disabled case) error = %v", err)
	}
	if err := database.ORM().WithContext(ctx).
		Model(&dbstore.Tenant{}).
		Where("id = ?", issued.TenantID).
		Update("status", dbstore.TenantDisabled).Error; err != nil {
		t.Fatalf("disable tenant: %v", err)
	}
	_, err = manager.Authenticate(ctx, tenantDisabledKey.Plaintext)
	requireErrorIs(t, err, ErrTenantInactive)
}

func openPostgresIntegrationDatabase(t *testing.T) *dbstore.Database {
	t.Helper()

	dsn := strings.TrimSpace(os.Getenv(postgresTestDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run real PostgreSQL integration tests", postgresTestDSNEnv)
	}

	settings := postgresIntegrationSettings(dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin, err := dbstore.Open(ctx, settings)
	if err != nil {
		t.Fatalf("open PostgreSQL integration admin connection: %v", err)
	}

	schema := postgresTestSchema(t)
	quotedSchema := `"` + schema + `"`
	if err := admin.ORM().WithContext(ctx).Exec("CREATE SCHEMA " + quotedSchema).Error; err != nil {
		_ = admin.Close()
		t.Fatalf("create isolated PostgreSQL schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := admin.ORM().WithContext(cleanupContext).Exec("DROP SCHEMA " + quotedSchema + " CASCADE").Error; err != nil {
			t.Errorf("drop isolated PostgreSQL schema %s: %v", schema, err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL integration admin connection: %v", err)
		}
	})

	isolatedDSN, err := postgresDSNWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatalf("build isolated PostgreSQL DSN: %v", err)
	}
	settings.DSN = isolatedDSN
	database, err := dbstore.Open(ctx, settings)
	if err != nil {
		t.Fatalf("open isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close isolated PostgreSQL database: %v", err)
		}
	})
	return database
}

func postgresIntegrationSettings(dsn string) config.Postgres {
	return config.Postgres{
		DSN:             dsn,
		MaxOpenConns:    4,
		MaxIdleConns:    1,
		ConnectTimeout:  3 * time.Second,
		MaxConnLifetime: time.Minute,
		MaxConnIdleTime: time.Minute,
	}
}

func postgresTestSchema(t *testing.T) string {
	t.Helper()
	randomBytes := make([]byte, 6)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatalf("generate PostgreSQL integration schema: %v", err)
	}
	return "model_velo_it_" + hex.EncodeToString(randomBytes)
}

func postgresDSNWithSearchPath(dsn, schema string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", errors.New("invalid PostgreSQL test DSN")
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("DSN must use postgres or postgresql scheme")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("application_name", "model-velo-integration")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func assertPostgresSchema(t *testing.T, database *gorm.DB) {
	t.Helper()
	migrator := database.Migrator()
	for _, model := range []any{
		&dbstore.Tenant{},
		&dbstore.APIKey{},
		&dbstore.TenantModelGrant{},
		&dbstore.UsageEvent{},
		&dbstore.UsageOutbox{},
		&dbstore.AdminPrincipal{},
		&dbstore.AdminRoleGrant{},
		&dbstore.RuntimeConfigVersion{},
		&dbstore.ManagedPricing{},
		&dbstore.AuditLog{},
		&dbstore.TenantQuotaPolicy{},
		&dbstore.QuotaWindow{},
		&dbstore.QuotaReservation{},
	} {
		if !migrator.HasTable(model) {
			t.Errorf("AutoMigrate did not create table for %T", model)
		}
	}
	for _, index := range []struct {
		model any
		name  string
	}{
		{&dbstore.Tenant{}, "tenants_slug_unique"},
		{&dbstore.APIKey{}, "api_keys_lookup_digest_unique"},
		{&dbstore.APIKey{}, "api_keys_tenant_status_idx"},
		{&dbstore.TenantModelGrant{}, "tenant_model_grants_model_idx"},
		{&dbstore.UsageEvent{}, "usage_events_tenant_started_idx"},
		{&dbstore.UsageEvent{}, "usage_events_request_idx"},
		{&dbstore.UsageEvent{}, "usage_events_provider_started_idx"},
		{&dbstore.UsageEvent{}, "usage_events_status_ended_idx"},
		{&dbstore.UsageOutbox{}, "usage_outbox_state_published_idx"},
		{&dbstore.AdminPrincipal{}, "admin_principals_digest_unique"},
		{&dbstore.RuntimeConfigVersion{}, "runtime_config_versions_one_active"},
		{&dbstore.AuditLog{}, "audit_logs_action_created_idx"},
		{&dbstore.TenantQuotaPolicy{}, "quota_policies_match_idx"},
		{&dbstore.QuotaReservation{}, "quota_reservations_state_expiry_idx"},
	} {
		if !migrator.HasIndex(index.model, index.name) {
			t.Errorf("AutoMigrate did not create index %s", index.name)
		}
	}
	for _, constraint := range []struct {
		model any
		name  string
	}{
		{&dbstore.Tenant{}, "tenants_status_check"},
		{&dbstore.APIKey{}, "api_keys_status_check"},
		{&dbstore.APIKey{}, "Tenant"},
		{&dbstore.TenantModelGrant{}, "Tenant"},
		{&dbstore.UsageOutbox{}, "usage_outbox_state_check"},
		{&dbstore.AdminPrincipal{}, "admin_principals_status_check"},
		{&dbstore.TenantQuotaPolicy{}, "quota_policies_period_check"},
		{&dbstore.TenantQuotaPolicy{}, "quota_policies_overage_check"},
		{&dbstore.TenantQuotaPolicy{}, "Tenant"},
		{&dbstore.QuotaReservation{}, "quota_reservations_state_check"},
	} {
		if !migrator.HasConstraint(constraint.model, constraint.name) {
			t.Errorf("AutoMigrate did not create constraint %s for %T", constraint.name, constraint.model)
		}
	}
}

func requireErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want errors.Is(_, %v)", err, target)
	}
}
