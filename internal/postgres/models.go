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
