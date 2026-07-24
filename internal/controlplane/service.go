package controlplane

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"model-velo/internal/config"
	"model-velo/internal/gateway"
	"model-velo/internal/postgres"
	"model-velo/internal/usage"
)

var (
	ErrVersionConflict = errors.New("control-plane version conflict")
	ErrNotConfigured   = errors.New("control-plane resource is not configured")
)

const runtimeCipherAAD = "model-velo/runtime-config/v1"

type PricingSink interface {
	ReplacePricing(*usage.PricingCatalog) error
}

type AuditMeta struct {
	PrincipalID string
	RequestID   string
	RemoteIP    string
}

type RuntimeView struct {
	Version  uint64          `json:"version"`
	Document RuntimeDocument `json:"document"`
}

type PricingView struct {
	Version uint64              `json:"version"`
	Prices  []config.UsagePrice `json:"prices"`
}

type AuditPage struct {
	Items      []AuditRecord `json:"items"`
	NextCursor uint64        `json:"next_cursor,omitempty"`
}

type AuditRecord struct {
	ID           uint64          `json:"id"`
	PrincipalID  string          `json:"principal_id"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id,omitempty"`
	RequestID    string          `json:"request_id"`
	RemoteIP     string          `json:"remote_ip,omitempty"`
	Before       json.RawMessage `json:"before,omitempty"`
	After        json.RawMessage `json:"after,omitempty"`
	Outcome      string          `json:"outcome"`
	CreatedAt    time.Time       `json:"created_at"`
}

type Service struct {
	database *gorm.DB
	runtime  *gateway.Manager
	pricing  PricingSink
	builder  Builder
	aead     cipher.AEAD

	mu             sync.RWMutex
	runtimeView    RuntimeView
	runtimePresent bool
	pricingView    PricingView
	pricingPresent bool
}

func NewService(
	database *gorm.DB,
	runtime *gateway.Manager,
	pricing PricingSink,
	builder Builder,
	masterKey []byte,
) (*Service, error) {
	if database == nil || runtime == nil || pricing == nil {
		return nil, errors.New("control plane dependencies are incomplete")
	}
	if len(masterKey) != 32 {
		return nil, errors.New("control plane master key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(append([]byte(nil), masterKey...))
	if err != nil {
		return nil, errors.New("initialize control plane cipher")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize control plane AEAD")
	}
	return &Service{
		database: database,
		runtime:  runtime,
		pricing:  pricing,
		builder:  builder,
		aead:     aead,
	}, nil
}

// Load activates persisted control-plane state. An absent managed resource
// leaves the environment bootstrap configuration in place.
func (service *Service) Load(ctx context.Context) error {
	if err := service.reloadRuntime(ctx); err != nil &&
		!errors.Is(err, ErrNotConfigured) {
		return err
	}
	if err := service.reloadPricing(ctx); err != nil &&
		!errors.Is(err, ErrNotConfigured) {
		return err
	}
	return nil
}

func (service *Service) Watch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := service.reloadRuntime(ctx); err != nil &&
				!errors.Is(err, ErrNotConfigured) {
				slog.Error("control-plane runtime refresh failed", "error", err)
			}
			if err := service.reloadPricing(ctx); err != nil &&
				!errors.Is(err, ErrNotConfigured) {
				slog.Error("control-plane pricing refresh failed", "error", err)
			}
		}
	}
}

func (service *Service) Runtime() (RuntimeView, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if !service.runtimePresent {
		return RuntimeView{}, ErrNotConfigured
	}
	return cloneRuntimeView(service.runtimeView), nil
}

func (service *Service) Pricing() (PricingView, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if !service.pricingPresent {
		return PricingView{}, ErrNotConfigured
	}
	return clonePricingView(service.pricingView), nil
}

func (service *Service) UpdateRuntime(
	ctx context.Context,
	expectedVersion uint64,
	document RuntimeDocument,
	meta AuditMeta,
) (RuntimeView, error) {
	var nextView RuntimeView
	var snapshot *gateway.Snapshot
	err := service.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		currentRow, currentDocument, err := service.lockedRuntime(transaction)
		if err != nil && !errors.Is(err, ErrNotConfigured) {
			return err
		}
		currentVersion := uint64(0)
		if err == nil {
			currentVersion = currentRow.Version
		}
		if expectedVersion != currentVersion {
			return ErrVersionConflict
		}
		merged, err := mergeRuntimeSecrets(document, currentDocument)
		if err != nil {
			return err
		}
		snapshot, err = service.builder.build(merged, service.runtime.Current())
		if err != nil {
			return err
		}
		nextVersion := currentVersion + 1
		snapshot.CacheNamespace = managedCacheNamespace(nextVersion)
		plaintext, err := json.Marshal(merged)
		if err != nil {
			return errors.New("encode runtime document")
		}
		public := redactRuntime(merged)
		publicJSON, err := json.Marshal(public)
		if err != nil {
			return errors.New("encode public runtime document")
		}
		ciphertext, nonce, err := service.seal(plaintext)
		if err != nil {
			return err
		}
		if err := transaction.Model(&postgres.RuntimeConfigVersion{}).
			Where("active = ?", true).
			Update("active", false).Error; err != nil {
			return errors.New("deactivate runtime version")
		}
		rowID, err := randomID()
		if err != nil {
			return errors.New("generate runtime version ID")
		}
		row := postgres.RuntimeConfigVersion{
			ID: rowID, Version: nextVersion, PublicDocument: string(publicJSON),
			Ciphertext: ciphertext, Nonce: nonce, Active: true,
			CreatedBy: meta.PrincipalID, CreatedAt: time.Now().UTC(),
		}
		if err := transaction.Create(&row).Error; err != nil {
			return errors.New("persist runtime version")
		}
		nextView = RuntimeView{Version: nextVersion, Document: public}
		var before any
		if currentVersion != 0 {
			before = redactRuntime(currentDocument)
		}
		return appendAudit(transaction, meta, "runtime.update", "runtime_config",
			fmt.Sprint(nextVersion), before, public, "success")
	})
	if err != nil {
		return RuntimeView{}, err
	}
	if err := service.runtime.Replace(snapshot); err != nil {
		return RuntimeView{}, err
	}
	service.mu.Lock()
	service.runtimeView = nextView
	service.runtimePresent = true
	service.mu.Unlock()
	return cloneRuntimeView(nextView), nil
}

func (service *Service) UpdatePricing(
	ctx context.Context,
	expectedVersion uint64,
	prices []config.UsagePrice,
	meta AuditMeta,
) (PricingView, error) {
	catalog, err := usage.NewPricingCatalog(prices)
	if err != nil {
		return PricingView{}, err
	}
	document, err := json.Marshal(prices)
	if err != nil {
		return PricingView{}, errors.New("encode managed pricing")
	}
	var next PricingView
	err = service.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var current postgres.ManagedPricing
		queryErr := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&current, "id = ?", 1).Error
		currentVersion := uint64(0)
		var before any
		switch {
		case queryErr == nil:
			currentVersion = current.Version
			_ = json.Unmarshal([]byte(current.Document), &before)
		case errors.Is(queryErr, gorm.ErrRecordNotFound):
		default:
			return errors.New("read managed pricing")
		}
		if expectedVersion != currentVersion {
			return ErrVersionConflict
		}
		next = PricingView{
			Version: currentVersion + 1,
			Prices:  append([]config.UsagePrice(nil), prices...),
		}
		row := postgres.ManagedPricing{
			ID: 1, Version: next.Version, Document: string(document),
			UpdatedBy: meta.PrincipalID,
		}
		if queryErr == nil {
			row.CreatedAt = current.CreatedAt
		}
		if err := transaction.Save(&row).Error; err != nil {
			return errors.New("persist managed pricing")
		}
		return appendAudit(transaction, meta, "pricing.update", "pricing",
			fmt.Sprint(next.Version), before, prices, "success")
	})
	if err != nil {
		return PricingView{}, err
	}
	if err := service.pricing.ReplacePricing(catalog); err != nil {
		return PricingView{}, err
	}
	service.mu.Lock()
	service.pricingView = next
	service.pricingPresent = true
	service.mu.Unlock()
	return clonePricingView(next), nil
}

func (service *Service) Audit(
	ctx context.Context,
	beforeID uint64,
	limit int,
) (AuditPage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	query := service.database.WithContext(ctx).Order("id DESC").Limit(limit + 1)
	if beforeID != 0 {
		query = query.Where("id < ?", beforeID)
	}
	var rows []postgres.AuditLog
	if err := query.Find(&rows).Error; err != nil {
		return AuditPage{}, errors.New("read audit log")
	}
	page := AuditPage{Items: make([]AuditRecord, 0, min(len(rows), limit))}
	if len(rows) > limit {
		page.NextCursor = rows[limit-1].ID
	}
	for _, row := range rows[:min(len(rows), limit)] {
		record := AuditRecord{
			ID: row.ID, PrincipalID: row.PrincipalID, Action: row.Action,
			ResourceType: row.ResourceType, ResourceID: row.ResourceID,
			RequestID: row.RequestID, RemoteIP: row.RemoteIP,
			Outcome: row.Outcome, CreatedAt: row.CreatedAt,
		}
		if row.BeforeJSON != nil {
			record.Before = json.RawMessage(*row.BeforeJSON)
		}
		if row.AfterJSON != nil {
			record.After = json.RawMessage(*row.AfterJSON)
		}
		page.Items = append(page.Items, record)
	}
	return page, nil
}

func (service *Service) lockedRuntime(
	transaction *gorm.DB,
) (postgres.RuntimeConfigVersion, RuntimeDocument, error) {
	var row postgres.RuntimeConfigVersion
	err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("active = ?", true).
		Order("version DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return postgres.RuntimeConfigVersion{}, RuntimeDocument{}, ErrNotConfigured
	}
	if err != nil {
		return postgres.RuntimeConfigVersion{}, RuntimeDocument{}, errors.New("read runtime version")
	}
	document, err := service.decrypt(row)
	return row, document, err
}

func (service *Service) reloadRuntime(ctx context.Context) error {
	var row postgres.RuntimeConfigVersion
	err := service.database.WithContext(ctx).
		Where("active = ?", true).
		Order("version DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotConfigured
	}
	if err != nil {
		return errors.New("read active runtime")
	}
	service.mu.RLock()
	currentVersion := service.runtimeView.Version
	service.mu.RUnlock()
	if currentVersion == row.Version {
		return nil
	}
	document, err := service.decrypt(row)
	if err != nil {
		return err
	}
	snapshot, err := service.builder.build(document, service.runtime.Current())
	if err != nil {
		return fmt.Errorf("build persisted runtime: %w", err)
	}
	snapshot.CacheNamespace = managedCacheNamespace(row.Version)
	var public RuntimeDocument
	if err := json.Unmarshal([]byte(row.PublicDocument), &public); err != nil {
		return errors.New("decode public runtime document")
	}
	if err := service.runtime.Replace(snapshot); err != nil {
		return err
	}
	service.mu.Lock()
	service.runtimeView = RuntimeView{Version: row.Version, Document: public}
	service.runtimePresent = true
	service.mu.Unlock()
	return nil
}

func (service *Service) reloadPricing(ctx context.Context) error {
	var row postgres.ManagedPricing
	err := service.database.WithContext(ctx).First(&row, "id = ?", 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotConfigured
	}
	if err != nil {
		return errors.New("read managed pricing")
	}
	service.mu.RLock()
	currentVersion := service.pricingView.Version
	service.mu.RUnlock()
	if currentVersion == row.Version {
		return nil
	}
	var prices []config.UsagePrice
	if err := json.Unmarshal([]byte(row.Document), &prices); err != nil {
		return errors.New("decode managed pricing")
	}
	catalog, err := usage.NewPricingCatalog(prices)
	if err != nil {
		return fmt.Errorf("validate managed pricing: %w", err)
	}
	if err := service.pricing.ReplacePricing(catalog); err != nil {
		return err
	}
	service.mu.Lock()
	service.pricingView = PricingView{Version: row.Version, Prices: prices}
	service.pricingPresent = true
	service.mu.Unlock()
	return nil
}

func (service *Service) decrypt(
	row postgres.RuntimeConfigVersion,
) (RuntimeDocument, error) {
	plaintext, err := service.aead.Open(
		nil, row.Nonce, row.Ciphertext, []byte(runtimeCipherAAD),
	)
	if err != nil {
		return RuntimeDocument{}, errors.New("decrypt runtime document")
	}
	var document RuntimeDocument
	decoder := json.NewDecoder(strings.NewReader(string(plaintext)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return RuntimeDocument{}, errors.New("decode runtime document")
	}
	return document, nil
}

func (service *Service) seal(plaintext []byte) ([]byte, []byte, error) {
	nonce := make([]byte, service.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, errors.New("generate runtime encryption nonce")
	}
	return service.aead.Seal(
		nil, nonce, plaintext, []byte(runtimeCipherAAD),
	), nonce, nil
}

func mergeRuntimeSecrets(
	next RuntimeDocument,
	current RuntimeDocument,
) (RuntimeDocument, error) {
	existing := make(map[string]string)
	for _, configuredProvider := range current.Providers {
		for _, key := range configuredProvider.Keys {
			existing[configuredProvider.ID+"\x00"+key.ID] = key.Secret
		}
	}
	for providerIndex := range next.Providers {
		configuredProvider := &next.Providers[providerIndex]
		for keyIndex := range configuredProvider.Keys {
			key := &configuredProvider.Keys[keyIndex]
			key.ID = strings.TrimSpace(key.ID)
			if key.Secret == "" {
				key.Secret = existing[configuredProvider.ID+"\x00"+key.ID]
			}
			if key.Secret == "" {
				return RuntimeDocument{}, fmt.Errorf(
					"provider %q key %q requires a secret",
					configuredProvider.ID, key.ID,
				)
			}
		}
	}
	return next, nil
}

func redactRuntime(document RuntimeDocument) RuntimeDocument {
	cloned := cloneRuntimeView(RuntimeView{Document: document}).Document
	for providerIndex := range cloned.Providers {
		for keyIndex := range cloned.Providers[providerIndex].Keys {
			cloned.Providers[providerIndex].Keys[keyIndex].Secret = ""
		}
	}
	return cloned
}

func appendAudit(
	database *gorm.DB,
	meta AuditMeta,
	action, resourceType, resourceID string,
	before, after any,
	outcome string,
) error {
	beforeJSON, err := nullableJSON(before)
	if err != nil {
		return err
	}
	afterJSON, err := nullableJSON(after)
	if err != nil {
		return err
	}
	row := postgres.AuditLog{
		PrincipalID: strings.TrimSpace(meta.PrincipalID),
		Action:      strings.TrimSpace(action), ResourceType: strings.TrimSpace(resourceType),
		ResourceID: strings.TrimSpace(resourceID),
		RequestID:  strings.TrimSpace(meta.RequestID), RemoteIP: strings.TrimSpace(meta.RemoteIP),
		BeforeJSON: beforeJSON, AfterJSON: afterJSON,
		Outcome: strings.TrimSpace(outcome), CreatedAt: time.Now().UTC(),
	}
	if row.PrincipalID == "" || row.Action == "" || row.ResourceType == "" ||
		row.RequestID == "" || row.Outcome == "" {
		return errors.New("audit metadata is incomplete")
	}
	if err := database.Create(&row).Error; err != nil {
		return errors.New("append audit log")
	}
	return nil
}

func nullableJSON(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("encode audit snapshot")
	}
	result := string(encoded)
	return &result, nil
}

func cloneRuntimeView(view RuntimeView) RuntimeView {
	encoded, _ := json.Marshal(view)
	var cloned RuntimeView
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}

func clonePricingView(view PricingView) PricingView {
	cloned := view
	cloned.Prices = append([]config.UsagePrice(nil), view.Prices...)
	return cloned
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
}

func managedCacheNamespace(version uint64) string {
	return fmt.Sprintf("managed-runtime-v%d", version)
}
