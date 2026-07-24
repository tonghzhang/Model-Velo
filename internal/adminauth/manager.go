package adminauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"model-velo/internal/postgres"
)

type Role string
type Permission string

const (
	RoleOwner    Role = "owner"
	RoleOperator Role = "operator"
	RoleBilling  Role = "billing"
	RoleAuditor  Role = "auditor"

	PermissionRuntimeRead  Permission = "runtime:read"
	PermissionRuntimeWrite Permission = "runtime:write"
	PermissionPricingRead  Permission = "pricing:read"
	PermissionPricingWrite Permission = "pricing:write"
	PermissionQuotaRead    Permission = "quota:read"
	PermissionQuotaWrite   Permission = "quota:write"
	PermissionAuditRead    Permission = "audit:read"
	PermissionAdminRead    Permission = "admin:read"
	PermissionAdminWrite   Permission = "admin:write"
	PermissionTenantRead   Permission = "tenant:read"
	PermissionTenantWrite  Permission = "tenant:write"
)

var (
	ErrInvalidCredential = errors.New("invalid admin credential")
	ErrInactive          = errors.New("admin principal is inactive")
	ErrForbidden         = errors.New("admin permission denied")
	ErrInvalidInput      = errors.New("invalid admin input")
	ErrLastOwner         = errors.New("at least one active owner is required")
	adminNamePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,99}$`)
)

var rolePermissions = map[Role]map[Permission]struct{}{
	RoleOwner: {
		PermissionRuntimeRead: {}, PermissionRuntimeWrite: {},
		PermissionPricingRead: {}, PermissionPricingWrite: {},
		PermissionQuotaRead: {}, PermissionQuotaWrite: {},
		PermissionAuditRead: {}, PermissionAdminRead: {},
		PermissionAdminWrite: {}, PermissionTenantRead: {},
		PermissionTenantWrite: {},
	},
	RoleOperator: {
		PermissionRuntimeRead: {}, PermissionRuntimeWrite: {},
		PermissionPricingRead: {}, PermissionQuotaRead: {},
		PermissionQuotaWrite: {}, PermissionTenantRead: {},
		PermissionTenantWrite: {},
	},
	RoleBilling: {
		PermissionPricingRead: {}, PermissionPricingWrite: {},
		PermissionQuotaRead: {}, PermissionQuotaWrite: {},
		PermissionAuditRead: {},
	},
	RoleAuditor: {
		PermissionRuntimeRead: {}, PermissionPricingRead: {},
		PermissionQuotaRead: {}, PermissionAuditRead: {},
		PermissionAdminRead: {}, PermissionTenantRead: {},
	},
}

type Identity struct {
	PrincipalID string
	Name        string
	KeyPrefix   string
	Roles       []Role
	permissions map[Permission]struct{}
}

func (identity Identity) Allows(permission Permission) bool {
	_, ok := identity.permissions[permission]
	return ok
}

type IssuedCredential struct {
	PrincipalID string `json:"principal_id"`
	Name        string `json:"name"`
	KeyPrefix   string `json:"key_prefix"`
	Plaintext   string `json:"admin_key"`
	Roles       []Role `json:"roles"`
}

type PrincipalView struct {
	ID         string                        `json:"id"`
	Name       string                        `json:"name"`
	KeyPrefix  string                        `json:"key_prefix"`
	Status     postgres.AdminPrincipalStatus `json:"status"`
	Roles      []Role                        `json:"roles"`
	LastUsedAt *time.Time                    `json:"last_used_at,omitempty"`
	CreatedAt  time.Time                     `json:"created_at"`
	UpdatedAt  time.Time                     `json:"updated_at"`
}

type MutationMeta struct {
	ActorID   string
	RequestID string
	RemoteIP  string
}

type Manager struct {
	database *gorm.DB
	pepper   []byte
	now      func() time.Time
}

func NewManager(database *gorm.DB, pepper []byte) (*Manager, error) {
	if database == nil {
		return nil, errors.New("admin auth requires a database")
	}
	if len(pepper) < 32 {
		return nil, errors.New("admin auth requires at least 32 pepper bytes")
	}
	return &Manager{
		database: database,
		pepper:   append([]byte(nil), pepper...),
		now:      time.Now,
	}, nil
}

func (manager *Manager) Bootstrap(
	ctx context.Context,
	name string,
) (IssuedCredential, error) {
	var count int64
	if err := manager.database.WithContext(ctx).
		Model(&postgres.AdminPrincipal{}).
		Count(&count).Error; err != nil {
		return IssuedCredential{}, errors.New("count admin principals")
	}
	if count != 0 {
		return IssuedCredential{}, errors.New("admin bootstrap is already complete")
	}
	return manager.Create(ctx, name, []Role{RoleOwner})
}

func (manager *Manager) Create(
	ctx context.Context,
	name string,
	roles []Role,
) (IssuedCredential, error) {
	return manager.create(ctx, name, roles, nil)
}

func (manager *Manager) CreateAudited(
	ctx context.Context,
	name string,
	roles []Role,
	meta MutationMeta,
) (IssuedCredential, error) {
	return manager.create(ctx, name, roles, &meta)
}

func (manager *Manager) create(
	ctx context.Context,
	name string,
	roles []Role,
	meta *MutationMeta,
) (IssuedCredential, error) {
	name = strings.TrimSpace(name)
	roles, err := normalizeRoles(roles)
	if !adminNamePattern.MatchString(name) || err != nil {
		return IssuedCredential{}, ErrInvalidInput
	}
	plaintext, prefix, err := newCredential()
	if err != nil {
		return IssuedCredential{}, errors.New("generate admin credential")
	}
	principalID, err := randomUUID()
	if err != nil {
		return IssuedCredential{}, errors.New("generate admin principal ID")
	}
	now := manager.now().UTC()
	principal := postgres.AdminPrincipal{
		ID:               principalID,
		Name:             name,
		KeyPrefix:        prefix,
		CredentialDigest: manager.digest(plaintext),
		Status:           postgres.AdminPrincipalActive,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	grants := make([]postgres.AdminRoleGrant, 0, len(roles))
	for _, role := range roles {
		grants = append(grants, postgres.AdminRoleGrant{
			PrincipalID: principalID,
			Role:        string(role),
			CreatedAt:   now,
		})
	}
	err = manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&principal).Error; err != nil {
			return errors.New("create admin principal")
		}
		if err := transaction.Create(&grants).Error; err != nil {
			return errors.New("create admin role grants")
		}
		if meta != nil {
			return writeMutationAudit(
				transaction, *meta, "admin.create", principalID,
				nil, map[string]any{
					"id": principalID, "name": name, "roles": roles,
				},
			)
		}
		return nil
	})
	if err != nil {
		return IssuedCredential{}, err
	}
	return IssuedCredential{
		PrincipalID: principalID,
		Name:        name,
		KeyPrefix:   prefix,
		Plaintext:   plaintext,
		Roles:       roles,
	}, nil
}

func (manager *Manager) Authenticate(
	ctx context.Context,
	plaintext string,
) (Identity, error) {
	plaintext = strings.TrimSpace(plaintext)
	if !strings.HasPrefix(plaintext, "mv_admin_") || len(plaintext) > 160 {
		return Identity{}, ErrInvalidCredential
	}
	digest := manager.digest(plaintext)
	var principal postgres.AdminPrincipal
	err := manager.database.WithContext(ctx).
		Where("credential_digest = ?", digest).
		First(&principal).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Identity{}, ErrInvalidCredential
		}
		return Identity{}, errors.New("read admin principal")
	}
	if subtle.ConstantTimeCompare(principal.CredentialDigest, digest) != 1 {
		return Identity{}, ErrInvalidCredential
	}
	if principal.Status != postgres.AdminPrincipalActive {
		return Identity{}, ErrInactive
	}
	roles, err := manager.roles(ctx, principal.ID)
	if err != nil {
		return Identity{}, err
	}
	permissions := make(map[Permission]struct{})
	for _, role := range roles {
		for permission := range rolePermissions[role] {
			permissions[permission] = struct{}{}
		}
	}
	now := manager.now().UTC()
	if principal.LastUsedAt == nil || now.Sub(*principal.LastUsedAt) >= 5*time.Minute {
		_ = manager.database.WithContext(ctx).
			Model(&postgres.AdminPrincipal{}).
			Where("id = ?", principal.ID).
			Update("last_used_at", now).Error
	}
	return Identity{
		PrincipalID: principal.ID,
		Name:        principal.Name,
		KeyPrefix:   principal.KeyPrefix,
		Roles:       roles,
		permissions: permissions,
	}, nil
}

func (manager *Manager) List(ctx context.Context) ([]PrincipalView, error) {
	var principals []postgres.AdminPrincipal
	if err := manager.database.WithContext(ctx).
		Order("created_at ASC").
		Limit(1000).
		Find(&principals).Error; err != nil {
		return nil, errors.New("list admin principals")
	}
	principalIDs := make([]string, 0, len(principals))
	for _, principal := range principals {
		principalIDs = append(principalIDs, principal.ID)
	}
	var grants []postgres.AdminRoleGrant
	if len(principalIDs) > 0 {
		if err := manager.database.WithContext(ctx).
			Where("principal_id IN ?", principalIDs).
			Order("principal_id ASC, role ASC").
			Find(&grants).Error; err != nil {
			return nil, errors.New("list admin role grants")
		}
	}
	rolesByPrincipal := make(map[string][]Role, len(principals))
	for _, grant := range grants {
		role := Role(grant.Role)
		if _, ok := rolePermissions[role]; ok {
			rolesByPrincipal[grant.PrincipalID] = append(
				rolesByPrincipal[grant.PrincipalID],
				role,
			)
		}
	}
	views := make([]PrincipalView, 0, len(principals))
	for _, principal := range principals {
		roles := rolesByPrincipal[principal.ID]
		if len(roles) == 0 {
			return nil, ErrForbidden
		}
		views = append(views, PrincipalView{
			ID: principal.ID, Name: principal.Name, KeyPrefix: principal.KeyPrefix,
			Status: principal.Status, Roles: roles, LastUsedAt: principal.LastUsedAt,
			CreatedAt: principal.CreatedAt, UpdatedAt: principal.UpdatedAt,
		})
	}
	return views, nil
}

func (manager *Manager) Update(
	ctx context.Context,
	principalID string,
	status postgres.AdminPrincipalStatus,
	roles []Role,
) error {
	return manager.update(ctx, principalID, status, roles, nil)
}

func (manager *Manager) UpdateAudited(
	ctx context.Context,
	principalID string,
	status postgres.AdminPrincipalStatus,
	roles []Role,
	meta MutationMeta,
) error {
	return manager.update(ctx, principalID, status, roles, &meta)
}

func (manager *Manager) update(
	ctx context.Context,
	principalID string,
	status postgres.AdminPrincipalStatus,
	roles []Role,
	meta *MutationMeta,
) error {
	principalID = strings.TrimSpace(principalID)
	roles, err := normalizeRoles(roles)
	if principalID == "" || err != nil ||
		(status != postgres.AdminPrincipalActive &&
			status != postgres.AdminPrincipalDisabled) {
		return ErrInvalidInput
	}
	return manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var principal postgres.AdminPrincipal
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&principal, "id = ?", principalID).Error; err != nil {
			return err
		}
		var currentOwnerCount int64
		if err := transaction.Model(&postgres.AdminRoleGrant{}).
			Where("principal_id = ? AND role = ?", principalID, RoleOwner).
			Count(&currentOwnerCount).Error; err != nil {
			return errors.New("read current admin roles")
		}
		var currentGrants []postgres.AdminRoleGrant
		if err := transaction.Where("principal_id = ?", principalID).
			Order("role ASC").Find(&currentGrants).Error; err != nil {
			return errors.New("read current admin role grants")
		}
		keepsOwner := status == postgres.AdminPrincipalActive &&
			roleIncluded(roles, RoleOwner)
		if currentOwnerCount > 0 && !keepsOwner {
			var otherOwners int64
			if err := transaction.Table("admin_principals AS principals").
				Joins("JOIN admin_role_grants AS grants ON grants.principal_id = principals.id").
				Where(
					"principals.status = ? AND principals.id <> ? AND grants.role = ?",
					postgres.AdminPrincipalActive, principalID, RoleOwner,
				).
				Count(&otherOwners).Error; err != nil {
				return errors.New("count active owners")
			}
			if otherOwners == 0 {
				return ErrLastOwner
			}
		}
		result := transaction.Model(&principal).
			Updates(map[string]any{"status": status, "updated_at": manager.now().UTC()})
		if result.Error != nil {
			return errors.New("update admin principal")
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := transaction.Where("principal_id = ?", principalID).
			Delete(&postgres.AdminRoleGrant{}).Error; err != nil {
			return errors.New("replace admin role grants")
		}
		now := manager.now().UTC()
		grants := make([]postgres.AdminRoleGrant, 0, len(roles))
		for _, role := range roles {
			grants = append(grants, postgres.AdminRoleGrant{
				PrincipalID: principalID, Role: string(role), CreatedAt: now,
			})
		}
		if err := transaction.Create(&grants).Error; err != nil {
			return err
		}
		if meta != nil {
			beforeRoles := make([]Role, 0, len(currentGrants))
			for _, grant := range currentGrants {
				beforeRoles = append(beforeRoles, Role(grant.Role))
			}
			return writeMutationAudit(
				transaction, *meta, "admin.update", principalID,
				map[string]any{
					"id": principalID, "status": principal.Status,
					"roles": beforeRoles,
				},
				map[string]any{
					"id": principalID, "status": status, "roles": roles,
				},
			)
		}
		return nil
	})
}

func roleIncluded(roles []Role, expected Role) bool {
	for _, role := range roles {
		if role == expected {
			return true
		}
	}
	return false
}

func (manager *Manager) roles(ctx context.Context, principalID string) ([]Role, error) {
	var rows []postgres.AdminRoleGrant
	if err := manager.database.WithContext(ctx).
		Where("principal_id = ?", principalID).
		Order("role ASC").
		Find(&rows).Error; err != nil {
		return nil, errors.New("read admin role grants")
	}
	roles := make([]Role, 0, len(rows))
	for _, row := range rows {
		role := Role(row.Role)
		if _, ok := rolePermissions[role]; ok {
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		return nil, ErrForbidden
	}
	return roles, nil
}

func (manager *Manager) digest(plaintext string) []byte {
	mac := hmac.New(sha256.New, manager.pepper)
	_, _ = mac.Write([]byte(plaintext))
	return mac.Sum(nil)
}

func normalizeRoles(roles []Role) ([]Role, error) {
	seen := make(map[Role]struct{}, len(roles))
	result := make([]Role, 0, len(roles))
	for _, role := range roles {
		role = Role(strings.ToLower(strings.TrimSpace(string(role))))
		if _, ok := rolePermissions[role]; !ok {
			return nil, fmt.Errorf("%w: unknown role", ErrInvalidInput)
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		result = append(result, role)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: at least one role is required", ErrInvalidInput)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left] < result[right]
	})
	return result, nil
}

func newCredential() (plaintext string, prefix string, err error) {
	prefixBytes := make([]byte, 4)
	secret := make([]byte, 32)
	if _, err = rand.Read(prefixBytes); err != nil {
		return "", "", err
	}
	if _, err = rand.Read(secret); err != nil {
		return "", "", err
	}
	prefix = hex.EncodeToString(prefixBytes)
	plaintext = "mv_admin_" + prefix + "_" +
		base64.RawURLEncoding.EncodeToString(secret)
	return plaintext, prefix, nil
}

func randomUUID() (string, error) {
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

func writeMutationAudit(
	transaction *gorm.DB,
	meta MutationMeta,
	action string,
	resourceID string,
	before any,
	after any,
) error {
	if strings.TrimSpace(meta.ActorID) == "" ||
		strings.TrimSpace(meta.RequestID) == "" {
		return errors.New("admin audit metadata is incomplete")
	}
	beforeJSON, err := auditJSON(before)
	if err != nil {
		return err
	}
	afterJSON, err := auditJSON(after)
	if err != nil {
		return err
	}
	return transaction.Create(&postgres.AuditLog{
		PrincipalID: meta.ActorID, Action: action,
		ResourceType: "admin_principal", ResourceID: resourceID,
		RequestID: meta.RequestID, RemoteIP: meta.RemoteIP,
		BeforeJSON: beforeJSON, AfterJSON: afterJSON,
		Outcome: "success", CreatedAt: time.Now().UTC(),
	}).Error
}

func auditJSON(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	result := string(encoded)
	return &result, nil
}
