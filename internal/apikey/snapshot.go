package apikey

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"model-velo/internal/postgres"
)

const authSnapshotVersion = 1

var errSnapshotNotFound = errors.New("authentication snapshot not found")

type authSnapshot struct {
	Version       int                   `json:"version"`
	APIKeyID      string                `json:"api_key_id"`
	TenantID      string                `json:"tenant_id"`
	KeyPrefix     string                `json:"key_prefix"`
	LookupDigest  []byte                `json:"lookup_digest"`
	KeyHash       []byte                `json:"key_hash"`
	HashVersion   int16                 `json:"hash_version"`
	KeyStatus     postgres.APIKeyStatus `json:"key_status"`
	KeyExpiresAt  *time.Time            `json:"key_expires_at,omitempty"`
	LastUsedAt    *time.Time            `json:"last_used_at,omitempty"`
	TenantStatus  postgres.TenantStatus `json:"tenant_status"`
	TenantVersion uint64                `json:"tenant_version"`
	AllowedModels []string              `json:"allowed_models"`
	GeneratedAt   time.Time             `json:"generated_at"`
	CachedUntil   time.Time             `json:"cached_until"`
}

func (snapshot authSnapshot) identity() Identity {
	return Identity{
		TenantID:  snapshot.TenantID,
		APIKeyID:  snapshot.APIKeyID,
		KeyPrefix: snapshot.KeyPrefix,
		authorization: &authorizationSnapshot{
			models: append([]string(nil), snapshot.AllowedModels...),
		},
	}
}

func (snapshot *authSnapshot) validate(
	expectedDigest []byte,
	now time.Time,
	requireCacheLifetime bool,
) error {
	if snapshot.Version != authSnapshotVersion ||
		strings.TrimSpace(snapshot.APIKeyID) == "" ||
		strings.TrimSpace(snapshot.TenantID) == "" ||
		strings.TrimSpace(snapshot.KeyPrefix) == "" ||
		len(snapshot.KeyHash) != 32 ||
		snapshot.HashVersion != hashVersion ||
		snapshot.TenantVersion == 0 ||
		snapshot.GeneratedAt.IsZero() ||
		!bytes.Equal(snapshot.LookupDigest, expectedDigest) ||
		!bytes.Equal(digestPrefix(snapshot.KeyPrefix), expectedDigest) {
		return errors.New("authentication snapshot is invalid")
	}
	switch snapshot.KeyStatus {
	case postgres.APIKeyActive, postgres.APIKeyDisabled, postgres.APIKeyRevoked:
	default:
		return errors.New("authentication snapshot key status is invalid")
	}
	switch snapshot.TenantStatus {
	case postgres.TenantActive, postgres.TenantDisabled:
	default:
		return errors.New("authentication snapshot tenant status is invalid")
	}
	models, err := normalizeModels(snapshot.AllowedModels)
	if err != nil {
		return errors.New("authentication snapshot model grants are invalid")
	}
	snapshot.AllowedModels = models
	if requireCacheLifetime && (snapshot.CachedUntil.IsZero() ||
		!snapshot.CachedUntil.After(now)) {
		return errors.New("authentication snapshot is expired")
	}
	return nil
}

type snapshotSource interface {
	Load(
		context.Context,
		[]byte,
		time.Time,
	) (authSnapshot, int, error)
	Touch(context.Context, string, time.Time) (int, error)
}

type postgresSnapshotSource struct {
	database *gorm.DB
}

func (source postgresSnapshotSource) Load(
	ctx context.Context,
	lookupDigest []byte,
	now time.Time,
) (authSnapshot, int, error) {
	var row struct {
		APIKeyID      string
		TenantID      string
		KeyPrefix     string
		LookupDigest  []byte
		KeyHash       []byte
		HashVersion   int16
		KeyStatus     postgres.APIKeyStatus
		KeyExpiresAt  *time.Time
		LastUsedAt    *time.Time
		TenantStatus  postgres.TenantStatus
		TenantVersion uint64
	}
	err := source.database.WithContext(ctx).
		Table("api_keys AS keys").
		Select(
			"keys.id AS api_key_id, keys.tenant_id, keys.key_prefix, "+
				"keys.lookup_digest, keys.key_hash, keys.hash_version, "+
				"keys.status AS key_status, keys.expires_at AS key_expires_at, "+
				"keys.last_used_at, tenants.status AS tenant_status, "+
				"tenants.version AS tenant_version",
		).
		Joins("JOIN tenants ON tenants.id = keys.tenant_id").
		Where("keys.lookup_digest = ?", lookupDigest).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return authSnapshot{}, 1, errSnapshotNotFound
	}
	if err != nil {
		return authSnapshot{}, 1, errors.New("read API key authentication snapshot")
	}

	var models []string
	err = source.database.WithContext(ctx).
		Model(&postgres.TenantModelGrant{}).
		Where("tenant_id = ?", row.TenantID).
		Order("gateway_model ASC").
		Pluck("gateway_model", &models).Error
	if err != nil {
		return authSnapshot{}, 2, errors.New("read API key model grants")
	}
	snapshot := authSnapshot{
		Version:       authSnapshotVersion,
		APIKeyID:      row.APIKeyID,
		TenantID:      row.TenantID,
		KeyPrefix:     row.KeyPrefix,
		LookupDigest:  append([]byte(nil), row.LookupDigest...),
		KeyHash:       append([]byte(nil), row.KeyHash...),
		HashVersion:   row.HashVersion,
		KeyStatus:     row.KeyStatus,
		KeyExpiresAt:  cloneTime(row.KeyExpiresAt),
		LastUsedAt:    cloneTime(row.LastUsedAt),
		TenantStatus:  row.TenantStatus,
		TenantVersion: row.TenantVersion,
		AllowedModels: models,
		GeneratedAt:   now.UTC(),
	}
	if err := snapshot.validate(lookupDigest, now, false); err != nil {
		return authSnapshot{}, 2, err
	}
	return snapshot, 2, nil
}

func (source postgresSnapshotSource) Touch(
	ctx context.Context,
	keyID string,
	now time.Time,
) (int, error) {
	err := source.database.WithContext(ctx).
		Session(&gorm.Session{SkipDefaultTransaction: true}).
		Model(&postgres.APIKey{}).
		Where(
			"id = ? AND (last_used_at IS NULL OR last_used_at < ?)",
			keyID,
			now.Add(-5*time.Minute),
		).
		Update("last_used_at", now.UTC()).Error
	if err != nil {
		return 1, errors.New("update API key last used time")
	}
	return 1, nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}
