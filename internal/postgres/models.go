package postgres

import "time"

type TenantStatus string

const (
	TenantActive   TenantStatus = "active"
	TenantDisabled TenantStatus = "disabled"
)

type APIKeyStatus string

const (
	APIKeyActive   APIKeyStatus = "active"
	APIKeyDisabled APIKeyStatus = "disabled"
	APIKeyRevoked  APIKeyStatus = "revoked"
)

type Tenant struct {
	ID          string       `gorm:"type:uuid;primaryKey"`
	Slug        string       `gorm:"size:80;not null;uniqueIndex:tenants_slug_unique;check:tenants_slug_format_check,slug = LOWER(slug) AND slug ~ '^[a-z0-9][a-z0-9_-]{1,78}[a-z0-9]$'"`
	DisplayName string       `gorm:"size:160;not null;check:tenants_display_name_check,LENGTH(BTRIM(display_name)) BETWEEN 1 AND 160"`
	Status      TenantStatus `gorm:"type:varchar(16);not null;default:active;index:tenants_status_idx;check:tenants_status_check,status IN ('active','disabled')"`
	Version     uint64       `gorm:"not null;default:1"`
	CreatedAt   time.Time    `gorm:"not null"`
	UpdatedAt   time.Time    `gorm:"not null;check:tenants_timestamps_check,updated_at >= created_at"`
}

func (Tenant) TableName() string {
	return "tenants"
}

type APIKey struct {
	ID           string       `gorm:"type:uuid;primaryKey"`
	TenantID     string       `gorm:"type:uuid;not null;index:api_keys_tenant_status_idx,priority:1"`
	Tenant       Tenant       `gorm:"foreignKey:TenantID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	Label        string       `gorm:"size:100;not null;check:api_keys_label_check,LENGTH(BTRIM(label)) BETWEEN 1 AND 100"`
	KeyPrefix    string       `gorm:"size:20;not null;check:api_keys_prefix_check,LENGTH(BTRIM(key_prefix)) BETWEEN 4 AND 20"`
	LookupDigest []byte       `gorm:"type:bytea;not null;uniqueIndex:api_keys_lookup_digest_unique;check:api_keys_lookup_digest_check,OCTET_LENGTH(lookup_digest) >= 16"`
	KeyHash      []byte       `gorm:"type:bytea;not null;check:api_keys_hash_check,OCTET_LENGTH(key_hash) >= 32"`
	HashVersion  int16        `gorm:"not null;check:api_keys_hash_version_check,hash_version > 0"`
	Status       APIKeyStatus `gorm:"type:varchar(16);not null;default:active;index:api_keys_tenant_status_idx,priority:2;check:api_keys_status_check,status IN ('active','disabled','revoked')"`
	ExpiresAt    *time.Time   `gorm:"index:api_keys_active_expiry_idx,where:status = 'active' AND expires_at IS NOT NULL;check:api_keys_expiry_check,expires_at IS NULL OR expires_at > created_at"`
	LastUsedAt   *time.Time
	RevokedAt    *time.Time `gorm:"check:api_keys_revocation_check,(status = 'revoked' AND revoked_at IS NOT NULL) OR (status <> 'revoked' AND revoked_at IS NULL)"`
	CreatedAt    time.Time  `gorm:"not null"`
	UpdatedAt    time.Time  `gorm:"not null;check:api_keys_timestamps_check,updated_at >= created_at"`
}

func (APIKey) TableName() string {
	return "api_keys"
}

type TenantModelGrant struct {
	TenantID     string    `gorm:"type:uuid;primaryKey"`
	Tenant       Tenant    `gorm:"foreignKey:TenantID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:CASCADE"`
	GatewayModel string    `gorm:"size:200;primaryKey;index:tenant_model_grants_model_idx;check:tenant_model_grants_model_check,LENGTH(BTRIM(gateway_model)) BETWEEN 1 AND 200 AND gateway_model = BTRIM(gateway_model)"`
	CreatedAt    time.Time `gorm:"not null"`
}

func (TenantModelGrant) TableName() string {
	return "tenant_model_grants"
}

type UsageEvent struct {
	EventID       string `gorm:"size:64;primaryKey"`
	RedisEntryID  string `gorm:"size:64;not null;index:usage_events_redis_entry_idx"`
	SchemaVersion int16  `gorm:"not null;check:usage_events_schema_version_check,schema_version > 0"`
	RequestID     string `gorm:"size:128;not null;index:usage_events_request_idx"`
	TenantID      string `gorm:"type:uuid;not null;index:usage_events_tenant_started_idx,priority:1;index:usage_events_tenant_model_started_idx,priority:1;index:usage_events_tenant_provider_started_idx,priority:1"`
	APIKeyID      string `gorm:"size:64;index:usage_events_api_key_started_idx,priority:1"`

	RequestedModel string `gorm:"size:200;not null;index:usage_events_tenant_model_started_idx,priority:2"`
	ProviderID     string `gorm:"size:100;index:usage_events_provider_started_idx,priority:1;index:usage_events_tenant_provider_started_idx,priority:2"`
	UpstreamModel  string `gorm:"size:200"`
	CacheStatus    string `gorm:"size:16;not null"`
	Stream         bool   `gorm:"not null"`
	Attempts       int    `gorm:"not null"`
	Retries        int    `gorm:"not null"`
	Fallbacks      int    `gorm:"not null"`

	UsageSource        string `gorm:"size:24;not null;default:unknown;check:usage_events_usage_source_check,usage_source IN ('unknown','provider','cache_replay')"`
	UsageCaveat        string `gorm:"size:256"`
	InputTokens        *int64
	OutputTokens       *int64
	TotalTokens        *int64
	InputText          *int64
	InputAudio         *int64
	InputImage         *int64
	CachedRead         *int64
	CachedWrite        *int64
	OutputText         *int64
	OutputAudio        *int64
	Reasoning          *int64
	AcceptedPrediction *int64
	RejectedPrediction *int64
	RawUsage           string `gorm:"type:text"`

	InputCostNanoUSD  *int64
	OutputCostNanoUSD *int64
	TotalCostNanoUSD  *int64 `gorm:"index:usage_events_cost_idx"`
	CostCurrency      string `gorm:"size:3"`
	CostSource        string `gorm:"size:32"`
	PricingVersion    string `gorm:"size:100"`
	CostCaveat        string `gorm:"size:256"`

	FinishReason  string    `gorm:"size:64"`
	Status        string    `gorm:"size:32;not null;index:usage_events_status_ended_idx,priority:1;check:usage_events_status_check,status IN ('success','cache_hit','failed','cancelled','stream_completed','stream_interrupted')"`
	ErrorCategory string    `gorm:"size:64"`
	ErrorCode     string    `gorm:"size:100"`
	StartedAt     time.Time `gorm:"not null;index:usage_events_tenant_started_idx,priority:2;index:usage_events_provider_started_idx,priority:2;index:usage_events_tenant_model_started_idx,priority:3;index:usage_events_tenant_provider_started_idx,priority:3;index:usage_events_api_key_started_idx,priority:2"`
	EndedAt       time.Time `gorm:"not null;index:usage_events_status_ended_idx,priority:2"`
	LatencyMS     int64     `gorm:"not null"`
	FirstTokenMS  *int64
	ProcessedAt   time.Time `gorm:"not null"`
}

func (UsageEvent) TableName() string {
	return "usage_events"
}

type UsageOutboxState string

const (
	UsageOutboxPending   UsageOutboxState = "pending"
	UsageOutboxReady     UsageOutboxState = "ready"
	UsageOutboxPublished UsageOutboxState = "published"
)

// UsageOutbox is the durable hand-off between the online request and Redis.
// Payload remains empty while a request is in flight. A stale pending record
// is evidence that the process stopped before it could finalize the request.
type UsageOutbox struct {
	EventID        string           `gorm:"size:64;primaryKey"`
	RequestID      string           `gorm:"size:128;not null;index:usage_outbox_request_idx"`
	TenantID       string           `gorm:"type:uuid;not null;index:usage_outbox_tenant_started_idx,priority:1"`
	APIKeyID       string           `gorm:"size:64;not null"`
	RequestedModel string           `gorm:"size:200;not null"`
	Stream         bool             `gorm:"not null"`
	State          UsageOutboxState `gorm:"type:varchar(16);not null;index:usage_outbox_state_updated_idx,priority:1;index:usage_outbox_state_published_idx,priority:1;check:usage_outbox_state_check,state IN ('pending','ready','published')"`
	Payload        *string          `gorm:"type:jsonb"`
	StartedAt      time.Time        `gorm:"not null;index:usage_outbox_tenant_started_idx,priority:2"`
	CreatedAt      time.Time        `gorm:"not null"`
	UpdatedAt      time.Time        `gorm:"not null;index:usage_outbox_state_updated_idx,priority:2"`
	PublishedAt    *time.Time       `gorm:"index:usage_outbox_state_published_idx,priority:2"`
}

func (UsageOutbox) TableName() string {
	return "usage_outbox"
}

type AdminPrincipalStatus string

const (
	AdminPrincipalActive   AdminPrincipalStatus = "active"
	AdminPrincipalDisabled AdminPrincipalStatus = "disabled"
)

type AdminPrincipal struct {
	ID               string               `gorm:"type:uuid;primaryKey"`
	Name             string               `gorm:"size:100;not null;uniqueIndex:admin_principals_name_unique"`
	KeyPrefix        string               `gorm:"size:24;not null"`
	CredentialDigest []byte               `gorm:"type:bytea;not null;uniqueIndex:admin_principals_digest_unique"`
	Status           AdminPrincipalStatus `gorm:"type:varchar(16);not null;index:admin_principals_status_idx;check:admin_principals_status_check,status IN ('active','disabled')"`
	LastUsedAt       *time.Time
	CreatedAt        time.Time `gorm:"not null"`
	UpdatedAt        time.Time `gorm:"not null"`
}

func (AdminPrincipal) TableName() string {
	return "admin_principals"
}

type AdminRoleGrant struct {
	PrincipalID string         `gorm:"type:uuid;primaryKey"`
	Principal   AdminPrincipal `gorm:"foreignKey:PrincipalID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:CASCADE"`
	Role        string         `gorm:"size:24;primaryKey;index:admin_role_grants_role_idx"`
	CreatedAt   time.Time      `gorm:"not null"`
}

func (AdminRoleGrant) TableName() string {
	return "admin_role_grants"
}

// RuntimeConfigVersion contains one encrypted, immutable control-plane
// document. Only the row marked Active may be used to build a runtime.
type RuntimeConfigVersion struct {
	ID             string `gorm:"type:uuid;primaryKey"`
	Version        uint64 `gorm:"not null;uniqueIndex:runtime_config_versions_version_unique"`
	PublicDocument string `gorm:"type:jsonb;not null"`
	Ciphertext     []byte `gorm:"type:bytea;not null"`
	Nonce          []byte `gorm:"type:bytea;not null"`
	Active         bool   `gorm:"not null;index:runtime_config_versions_active_idx;uniqueIndex:runtime_config_versions_one_active,where:active = true"`
	CreatedBy      string `gorm:"type:uuid;not null"`
	CreatedAt      time.Time
}

func (RuntimeConfigVersion) TableName() string {
	return "runtime_config_versions"
}

type ManagedPricing struct {
	ID        uint8  `gorm:"primaryKey;check:managed_pricing_singleton_check,id = 1"`
	Version   uint64 `gorm:"not null"`
	Document  string `gorm:"type:jsonb;not null"`
	UpdatedBy string `gorm:"type:uuid;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (ManagedPricing) TableName() string {
	return "managed_pricing"
}

// AuditLog is append-only from the application's point of view. It contains
// redacted request snapshots and never stores API or provider secrets.
type AuditLog struct {
	ID           uint64    `json:"id" gorm:"primaryKey;autoIncrement"`
	PrincipalID  string    `json:"principal_id" gorm:"type:uuid;not null;index:audit_logs_principal_created_idx,priority:1"`
	Action       string    `json:"action" gorm:"size:80;not null;index:audit_logs_action_created_idx,priority:1"`
	ResourceType string    `json:"resource_type" gorm:"size:40;not null"`
	ResourceID   string    `json:"resource_id,omitempty" gorm:"size:128"`
	RequestID    string    `json:"request_id" gorm:"size:128;not null"`
	RemoteIP     string    `json:"remote_ip,omitempty" gorm:"size:64"`
	BeforeJSON   *string   `json:"before,omitempty" gorm:"type:jsonb"`
	AfterJSON    *string   `json:"after,omitempty" gorm:"type:jsonb"`
	Outcome      string    `json:"outcome" gorm:"size:16;not null"`
	CreatedAt    time.Time `json:"created_at"`
}

func (AuditLog) TableName() string {
	return "audit_logs"
}

type QuotaPeriod string
type QuotaOveragePolicy string
type QuotaReservationState string

const (
	QuotaPeriodMinute QuotaPeriod = "minute"
	QuotaPeriodHour   QuotaPeriod = "hour"
	QuotaPeriodDay    QuotaPeriod = "day"
	QuotaPeriodMonth  QuotaPeriod = "month"

	QuotaOverageDeny  QuotaOveragePolicy = "deny"
	QuotaOverageAllow QuotaOveragePolicy = "allow"
	QuotaOverageAlert QuotaOveragePolicy = "alert"

	QuotaReservationActive    QuotaReservationState = "active"
	QuotaReservationSettled   QuotaReservationState = "settled"
	QuotaReservationEstimated QuotaReservationState = "estimated"
)

type TenantQuotaPolicy struct {
	ID            string      `gorm:"type:uuid;primaryKey"`
	TenantID      string      `gorm:"type:uuid;not null;index:quota_policies_match_idx,priority:1"`
	Tenant        Tenant      `gorm:"foreignKey:TenantID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	GatewayModel  string      `gorm:"size:200;not null;index:quota_policies_match_idx,priority:2"`
	Period        QuotaPeriod `gorm:"type:varchar(16);not null;check:quota_policies_period_check,period IN ('minute','hour','day','month')"`
	RequestLimit  *int64
	TokenLimit    *int64
	BudgetNanoUSD *int64
	OveragePolicy QuotaOveragePolicy `gorm:"type:varchar(16);not null;check:quota_policies_overage_check,overage_policy IN ('deny','allow','alert')"`
	Enabled       bool               `gorm:"not null;index:quota_policies_match_idx,priority:3"`
	Version       uint64             `gorm:"not null"`
	CreatedBy     string             `gorm:"type:uuid;not null"`
	UpdatedBy     string             `gorm:"type:uuid;not null"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (TenantQuotaPolicy) TableName() string {
	return "tenant_quota_policies"
}

type QuotaWindow struct {
	PolicyID         string    `gorm:"type:uuid;primaryKey"`
	WindowStart      time.Time `gorm:"primaryKey"`
	RequestsSettled  int64     `gorm:"not null"`
	TokensSettled    int64     `gorm:"not null"`
	CostSettled      int64     `gorm:"not null"`
	RequestsReserved int64     `gorm:"not null"`
	TokensReserved   int64     `gorm:"not null"`
	CostReserved     int64     `gorm:"not null"`
	UpdatedAt        time.Time
}

func (QuotaWindow) TableName() string {
	return "quota_windows"
}

type QuotaReservation struct {
	ID               string    `gorm:"type:uuid;primaryKey"`
	GroupID          string    `gorm:"size:64;not null;uniqueIndex:quota_reservations_group_policy_unique,priority:1;index:quota_reservations_group_idx"`
	PolicyID         string    `gorm:"type:uuid;not null;uniqueIndex:quota_reservations_group_policy_unique,priority:2"`
	WindowStart      time.Time `gorm:"not null"`
	RequestsReserved int64     `gorm:"not null"`
	TokensReserved   int64     `gorm:"not null"`
	CostReserved     int64     `gorm:"not null"`
	RequestsActual   *int64
	TokensActual     *int64
	CostActual       *int64
	State            QuotaReservationState `gorm:"type:varchar(16);not null;index:quota_reservations_state_expiry_idx,priority:1;check:quota_reservations_state_check,state IN ('active','settled','estimated')"`
	Exceeded         bool                  `gorm:"not null"`
	ExpiresAt        time.Time             `gorm:"not null;index:quota_reservations_state_expiry_idx,priority:2"`
	CreatedAt        time.Time
	SettledAt        *time.Time
}

func (QuotaReservation) TableName() string {
	return "quota_reservations"
}
