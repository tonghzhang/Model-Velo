// NewManager()
//     ↓
// 创建 API Key 业务服务 Manager

// manager.BootstrapTenant()
//     ↓
// 创建 Tenant + 模型权限 + 第一把 API Key

// manager.CreateKey()
//     ↓
// 给已有 Tenant 创建新的 postgres.APIKey 记录

// manager.Authenticate()
//     ↓
// 查询 postgres.APIKey 记录并验证身份

// manager.Disable() / manager.Revoke()
//
//	↓
//
// 修改 postgres.APIKey 的状态
package apikey

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"model-velo/internal/postgres"
)

var (
	ErrInvalidInput        = errors.New("invalid API key input")
	ErrTenantAlreadyExists = errors.New("tenant already exists")
	ErrTenantNotFound      = errors.New("tenant not found")
	ErrKeyNotFound         = errors.New("API key not found")
	ErrInvalidCredential   = errors.New("invalid API key credential")
	ErrKeyInactive         = errors.New("API key is inactive")
	ErrKeyRevoked          = errors.New("API key is revoked")
	ErrKeyExpired          = errors.New("API key has expired")
	ErrTenantInactive      = errors.New("tenant is inactive")
	ErrModelNotAllowed     = errors.New("model is not allowed")
)

var tenantSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,78}[a-z0-9]$`)

type Manager struct {
	database *gorm.DB
	pepper   []byte
	now      func() time.Time
}

// BootstrapTenantInput 表示“创建一个新租户”需要提供的参数。
type BootstrapTenantInput struct {
	Slug        string     `json:"slug"`
	DisplayName string     `json:"display_name"`
	KeyLabel    string     `json:"key_label"`
	Models      []string   `json:"models"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// CreateKeyInput 表示给已有租户创建 API Key 时需要的参数。
type CreateKeyInput struct {
	TenantID  string     `json:"tenant_id"`
	Label     string     `json:"label"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// IssuedKey 表示创建 API Key 成功后返回给调用者的数据。
type IssuedKey struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	Prefix    string     `json:"key_prefix"`
	Plaintext string     `json:"api_key"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Identity 表示 API Key 认证成功后得到的身份信息。
//
// 后面的模型权限、限流、计费、日志等业务，
// 不需要继续传递完整 API Key，
// 只需要使用这个认证结果。
type Identity struct {
	// TenantID 表示当前请求属于哪个租户。
	TenantID string

	// APIKeyID 表示当前请求使用了哪一条 API Key 记录。
	APIKeyID string

	// KeyPrefix 用于日志或后台展示。
	//
	// 它不会暴露完整 secret。
	KeyPrefix string
}

func NewManager(database *gorm.DB, pepper []byte) (*Manager, error) {
	if database == nil {
		return nil, errors.New("API key manager requires a database")
	}
	if len(pepper) < 32 {
		return nil, errors.New("API key manager requires at least 32 pepper bytes")
	}

	return &Manager{
		database: database,
		pepper:   append([]byte(nil), pepper...),
		now:      time.Now,
	}, nil
}

func (manager *Manager) BootstrapTenant(ctx context.Context, input BootstrapTenantInput) (IssuedKey, error) {
	normalized, err := normalizeBootstrapInput(input, manager.now().UTC())
	if err != nil {
		return IssuedKey{}, err
	}

	tenantID, err := randomUUID()
	if err != nil {
		return IssuedKey{}, err
	}

	var issued IssuedKey
	err = manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		tenant := postgres.Tenant{
			ID:          tenantID,
			Slug:        normalized.Slug,
			DisplayName: normalized.DisplayName,
			Status:      postgres.TenantActive,
			Version:     1,
		}
		if err := transaction.Create(&tenant).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrTenantAlreadyExists
			}
			return errors.New("create tenant")
		}

		if len(normalized.Models) > 0 {
			grants := make([]postgres.TenantModelGrant, 0, len(normalized.Models))
			for _, model := range normalized.Models {
				grants = append(grants, postgres.TenantModelGrant{
					TenantID:     tenantID,
					GatewayModel: model,
				})
			}
			if err := transaction.Create(&grants).Error; err != nil {
				return errors.New("create tenant model grants")
			}
		}

		issued, err = manager.createKey(transaction, CreateKeyInput{
			TenantID:  tenantID,
			Label:     normalized.KeyLabel,
			ExpiresAt: normalized.ExpiresAt,
		})
		return err
	})
	if err != nil {
		return IssuedKey{}, err
	}

	return issued, nil
}

func (manager *Manager) CreateKey(ctx context.Context, input CreateKeyInput) (IssuedKey, error) {
	normalized, err := normalizeCreateKeyInput(input, manager.now().UTC())
	if err != nil {
		return IssuedKey{}, err
	}

	var issued IssuedKey
	err = manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var tenant postgres.Tenant
		if err := transaction.Select("id", "status").First(&tenant, "id = ?", normalized.TenantID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTenantNotFound
			}
			return errors.New("read tenant")
		}
		if tenant.Status != postgres.TenantActive {
			return ErrTenantInactive
		}

		issued, err = manager.createKey(transaction, normalized)
		return err
	})
	if err != nil {
		return IssuedKey{}, err
	}

	return issued, nil
}

func (manager *Manager) Authenticate(ctx context.Context, plaintext string) (Identity, error) {
	token, err := parseToken(plaintext)
	if err != nil {
		manager.consumeDummyHash()
		return Identity{}, ErrInvalidCredential
	}

	var key postgres.APIKey
	err = manager.database.WithContext(ctx).
		Preload("Tenant").
		Where("lookup_digest = ?", digestPrefix(token.prefix)).
		First(&key).Error
	if err != nil {
		manager.consumeDummyHash()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Identity{}, ErrInvalidCredential
		}
		return Identity{}, errors.New("read API key")
	}
	if !verifyToken(token, key.KeyHash, key.HashVersion, manager.pepper) {
		return Identity{}, ErrInvalidCredential
	}
	if key.Status == postgres.APIKeyRevoked {
		return Identity{}, ErrKeyRevoked
	}
	if key.Status != postgres.APIKeyActive {
		return Identity{}, ErrKeyInactive
	}
	if key.Tenant.Status != postgres.TenantActive {
		return Identity{}, ErrTenantInactive
	}
	if expirationReached(key.ExpiresAt, manager.now().UTC()) {
		return Identity{}, ErrKeyExpired
	}
	now := manager.now().UTC()
	if key.LastUsedAt == nil || now.Sub(*key.LastUsedAt) >= 5*time.Minute {
		_ = manager.database.WithContext(ctx).
			Model(&postgres.APIKey{}).
			Where("id = ?", key.ID).
			Update("last_used_at", now).Error
	}

	return Identity{
		TenantID:  key.TenantID,
		APIKeyID:  key.ID,
		KeyPrefix: key.KeyPrefix,
	}, nil
}

func (manager *Manager) AuthorizeModel(ctx context.Context, tenantID, model string) error {
	tenantID = strings.TrimSpace(tenantID)
	model = strings.TrimSpace(model)
	if tenantID == "" || model == "" {
		return ErrModelNotAllowed
	}

	var count int64
	err := manager.database.WithContext(ctx).
		Model(&postgres.TenantModelGrant{}).
		Where(
			"tenant_id = ? AND gateway_model IN ?",
			tenantID, []string{model, "*"},
		).
		Count(&count).Error
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("read tenant model grant")
	}
	if count == 0 {
		return ErrModelNotAllowed
	}
	return nil
}

func (manager *Manager) AuthorizedModels(
	ctx context.Context,
	tenantID string,
) ([]string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrModelNotAllowed
	}
	var models []string
	if err := manager.database.WithContext(ctx).
		Model(&postgres.TenantModelGrant{}).
		Where("tenant_id = ?", tenantID).
		Order("gateway_model ASC").
		Pluck("gateway_model", &models).Error; err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("read tenant model grants")
	}
	return models, nil
}

func (manager *Manager) Revoke(ctx context.Context, keyID string) error {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return fmt.Errorf("%w: key ID is required", ErrInvalidInput)
	}

	now := manager.now().UTC()
	result := manager.database.WithContext(ctx).
		Model(&postgres.APIKey{}).
		Where("id = ? AND status <> ?", keyID, postgres.APIKeyRevoked).
		Updates(map[string]any{
			"status":     postgres.APIKeyRevoked,
			"revoked_at": now,
			"updated_at": now,
		})
	if result.Error != nil {
		return errors.New("revoke API key")
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := manager.database.WithContext(ctx).
			Model(&postgres.APIKey{}).
			Where("id = ?", keyID).
			Count(&count).Error; err != nil {
			return errors.New("check API key status")
		}
		if count > 0 {
			return ErrKeyRevoked
		}
		return ErrKeyNotFound
	}
	return nil
}

func (manager *Manager) Disable(ctx context.Context, keyID string) error {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return fmt.Errorf("%w: key ID is required", ErrInvalidInput)
	}

	result := manager.database.WithContext(ctx).
		Model(&postgres.APIKey{}).
		Where("id = ? AND status <> ?", keyID, postgres.APIKeyRevoked).
		Updates(map[string]any{
			"status":     postgres.APIKeyDisabled,
			"revoked_at": nil,
			"updated_at": manager.now().UTC(),
		})
	if result.Error != nil {
		return errors.New("disable API key")
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := manager.database.WithContext(ctx).
			Model(&postgres.APIKey{}).
			Where("id = ?", keyID).
			Count(&count).Error; err != nil {
			return errors.New("check API key status")
		}
		if count > 0 {
			return ErrKeyRevoked
		}
		return ErrKeyNotFound
	}
	return nil
}

func (manager *Manager) createKey(transaction *gorm.DB, input CreateKeyInput) (IssuedKey, error) {
	token, err := generateToken(manager.pepper)
	if err != nil {
		return IssuedKey{}, err
	}

	keyID, err := randomUUID()
	if err != nil {
		return IssuedKey{}, err
	}

	record := postgres.APIKey{
		ID:           keyID,
		TenantID:     input.TenantID,
		Label:        input.Label,
		KeyPrefix:    token.prefix,
		LookupDigest: token.lookupDigest,
		KeyHash:      token.keyHash,
		HashVersion:  token.hashVersion,
		Status:       postgres.APIKeyActive,
		ExpiresAt:    input.ExpiresAt,
	}
	if err := transaction.Create(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return IssuedKey{}, errors.New("generate unique API key")
		}
		return IssuedKey{}, errors.New("store API key")
	}

	return IssuedKey{
		ID:        record.ID,
		TenantID:  record.TenantID,
		Prefix:    record.KeyPrefix,
		Plaintext: token.plaintext,
		ExpiresAt: record.ExpiresAt,
	}, nil
}

func (manager *Manager) consumeDummyHash() {
	_ = hashSecret(strings.Repeat("x", 43), manager.pepper)
}

func normalizeBootstrapInput(input BootstrapTenantInput, now time.Time) (BootstrapTenantInput, error) {
	input.Slug = strings.TrimSpace(input.Slug)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.KeyLabel = strings.TrimSpace(input.KeyLabel)
	if !tenantSlugPattern.MatchString(input.Slug) {
		return BootstrapTenantInput{}, fmt.Errorf("%w: tenant slug must be 3-80 lowercase letters, digits, _ or -", ErrInvalidInput)
	}
	if input.DisplayName == "" || utf8.RuneCountInString(input.DisplayName) > 160 {
		return BootstrapTenantInput{}, fmt.Errorf("%w: display name must contain 1-160 characters", ErrInvalidInput)
	}

	keyInput, err := normalizeCreateKeyInput(CreateKeyInput{
		TenantID:  "bootstrap",
		Label:     input.KeyLabel,
		ExpiresAt: input.ExpiresAt,
	}, now)
	if err != nil {
		return BootstrapTenantInput{}, err
	}
	input.KeyLabel = keyInput.Label
	input.ExpiresAt = keyInput.ExpiresAt

	models, err := normalizeModels(input.Models)
	if err != nil {
		return BootstrapTenantInput{}, err
	}
	if len(models) == 0 {
		return BootstrapTenantInput{}, fmt.Errorf("%w: at least one model grant is required", ErrInvalidInput)
	}
	input.Models = models

	return input, nil
}

func normalizeCreateKeyInput(input CreateKeyInput, now time.Time) (CreateKeyInput, error) {
	input.TenantID = strings.TrimSpace(input.TenantID)
	input.Label = strings.TrimSpace(input.Label)
	if input.TenantID == "" {
		return CreateKeyInput{}, fmt.Errorf("%w: tenant ID is required", ErrInvalidInput)
	}
	if input.Label == "" || utf8.RuneCountInString(input.Label) > 100 {
		return CreateKeyInput{}, fmt.Errorf("%w: key label must contain 1-100 characters", ErrInvalidInput)
	}
	if input.ExpiresAt != nil {
		expiresAt := input.ExpiresAt.UTC()
		if expirationReached(&expiresAt, now) {
			return CreateKeyInput{}, fmt.Errorf("%w: expiration must be in the future", ErrInvalidInput)
		}
		input.ExpiresAt = &expiresAt
	}
	return input, nil
}

func expirationReached(expiresAt *time.Time, now time.Time) bool {
	return expiresAt != nil && !expiresAt.After(now)
}

func normalizeModels(models []string) ([]string, error) {
	unique := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || utf8.RuneCountInString(model) > 200 {
			return nil, fmt.Errorf("%w: model names must contain 1-200 characters", ErrInvalidInput)
		}
		unique[model] = struct{}{}
	}

	result := make([]string, 0, len(unique))
	for model := range unique {
		result = append(result, model)
	}
	sort.Strings(result)
	return result, nil
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("generate UUID")
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80

	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded), nil
}
