package quota

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"model-velo/internal/config"
	"model-velo/internal/postgres"
	"model-velo/internal/routing"
	"model-velo/internal/usage"
)

var (
	ErrExceeded        = errors.New("tenant quota exceeded")
	ErrCostUnknown     = errors.New("request cost cannot be reserved")
	ErrVersionConflict = errors.New("quota policy version conflict")
	ErrInvalidPolicy   = errors.New("invalid quota policy")
	ErrTenantNotFound  = errors.New("quota tenant not found")
)

type Quoter interface {
	Quote(usage.Event) usage.CostResult
}

type PolicyInput struct {
	TenantID      string                      `json:"tenant_id"`
	GatewayModel  string                      `json:"gateway_model"`
	Period        postgres.QuotaPeriod        `json:"period"`
	RequestLimit  *int64                      `json:"request_limit,omitempty"`
	TokenLimit    *int64                      `json:"token_limit,omitempty"`
	BudgetUSD     *string                     `json:"budget_usd,omitempty"`
	OveragePolicy postgres.QuotaOveragePolicy `json:"overage_policy"`
	Enabled       bool                        `json:"enabled"`
}

type PolicyView struct {
	ID            string                      `json:"id"`
	TenantID      string                      `json:"tenant_id"`
	GatewayModel  string                      `json:"gateway_model"`
	Period        postgres.QuotaPeriod        `json:"period"`
	RequestLimit  *int64                      `json:"request_limit,omitempty"`
	TokenLimit    *int64                      `json:"token_limit,omitempty"`
	BudgetUSD     *string                     `json:"budget_usd,omitempty"`
	OveragePolicy postgres.QuotaOveragePolicy `json:"overage_policy"`
	Enabled       bool                        `json:"enabled"`
	Version       uint64                      `json:"version"`
	CreatedAt     time.Time                   `json:"created_at"`
	UpdatedAt     time.Time                   `json:"updated_at"`
}

type WindowView struct {
	PolicyID         string    `json:"policy_id"`
	TenantID         string    `json:"tenant_id"`
	GatewayModel     string    `json:"gateway_model"`
	Period           string    `json:"period"`
	WindowStart      time.Time `json:"window_start"`
	RequestsSettled  int64     `json:"requests_settled"`
	RequestsReserved int64     `json:"requests_reserved"`
	TokensSettled    int64     `json:"tokens_settled"`
	TokensReserved   int64     `json:"tokens_reserved"`
	CostSettledUSD   string    `json:"cost_settled_usd"`
	CostReservedUSD  string    `json:"cost_reserved_usd"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type MutationMeta struct {
	ActorID   string
	RequestID string
	RemoteIP  string
}

type ReserveInput struct {
	GroupID               string
	TenantID              string
	GatewayModel          string
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
	Plan                  routing.Plan
}

type Decision struct {
	ReservationID   string
	Exceeded        bool
	Alerts          []string
	AppliedPolicies int
}

type Manager struct {
	database *gorm.DB
	pricing  Quoter
	settings config.Quota
	now      func() time.Time
	policies *policyIndex
}

func NewManager(
	database *gorm.DB,
	pricing Quoter,
	settings config.Quota,
) (*Manager, error) {
	if database == nil || pricing == nil {
		return nil, errors.New("quota manager dependencies are incomplete")
	}
	if settings.ReservationTTL <= 0 || settings.ReapInterval <= 0 ||
		settings.DefaultMaxOutputTokens <= 0 {
		return nil, errors.New("quota manager settings are invalid")
	}
	return &Manager{
		database: database, pricing: pricing, settings: settings,
		now: time.Now, policies: newPolicyIndex(),
	}, nil
}

func (manager *Manager) DefaultMaxOutputTokens() int64 {
	if manager == nil {
		return 0
	}
	return manager.settings.DefaultMaxOutputTokens
}

func (manager *Manager) CreatePolicy(
	ctx context.Context,
	input PolicyInput,
	actorID string,
) (PolicyView, error) {
	return manager.createPolicy(ctx, input, actorID, nil)
}

func (manager *Manager) CreatePolicyAudited(
	ctx context.Context,
	input PolicyInput,
	meta MutationMeta,
) (PolicyView, error) {
	return manager.createPolicy(ctx, input, meta.ActorID, &meta)
}

func (manager *Manager) createPolicy(
	ctx context.Context,
	input PolicyInput,
	actorID string,
	meta *MutationMeta,
) (PolicyView, error) {
	policy, err := normalizePolicy(input)
	if err != nil {
		return PolicyView{}, err
	}
	policy.ID, err = randomUUID()
	if err != nil {
		return PolicyView{}, errors.New("generate quota policy ID")
	}
	now := manager.now().UTC()
	policy.Version = 1
	policy.CreatedBy = strings.TrimSpace(actorID)
	policy.UpdatedBy = strings.TrimSpace(actorID)
	policy.CreatedAt = now
	policy.UpdatedAt = now
	if policy.CreatedBy == "" {
		return PolicyView{}, ErrInvalidPolicy
	}
	view := policyView(policy)
	err = manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := ensureTenant(transaction, policy.TenantID); err != nil {
			return err
		}
		if err := transaction.Create(&policy).Error; err != nil {
			return errors.New("create quota policy")
		}
		if meta != nil {
			return writeQuotaAudit(transaction, *meta, "quota.create", policy.ID, nil, view)
		}
		return nil
	})
	if err != nil {
		return PolicyView{}, err
	}
	manager.policies.Put(policy)
	return view, nil
}

func (manager *Manager) UpdatePolicy(
	ctx context.Context,
	policyID string,
	expectedVersion uint64,
	input PolicyInput,
	actorID string,
) (PolicyView, error) {
	return manager.updatePolicy(ctx, policyID, expectedVersion, input, actorID, nil)
}

func (manager *Manager) UpdatePolicyAudited(
	ctx context.Context,
	policyID string,
	expectedVersion uint64,
	input PolicyInput,
	meta MutationMeta,
) (PolicyView, error) {
	return manager.updatePolicy(
		ctx, policyID, expectedVersion, input, meta.ActorID, &meta,
	)
}

func (manager *Manager) updatePolicy(
	ctx context.Context,
	policyID string,
	expectedVersion uint64,
	input PolicyInput,
	actorID string,
	meta *MutationMeta,
) (PolicyView, error) {
	normalized, err := normalizePolicy(input)
	if err != nil {
		return PolicyView{}, err
	}
	var updated postgres.TenantQuotaPolicy
	err = manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var current postgres.TenantQuotaPolicy
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&current, "id = ?", strings.TrimSpace(policyID)).Error; err != nil {
			return err
		}
		if current.Version != expectedVersion {
			return ErrVersionConflict
		}
		if err := ensureTenant(transaction, normalized.TenantID); err != nil {
			return err
		}
		normalized.ID = current.ID
		normalized.Version = current.Version + 1
		normalized.CreatedBy = current.CreatedBy
		normalized.CreatedAt = current.CreatedAt
		normalized.UpdatedBy = strings.TrimSpace(actorID)
		normalized.UpdatedAt = manager.now().UTC()
		if normalized.UpdatedBy == "" {
			return ErrInvalidPolicy
		}
		if err := transaction.Save(&normalized).Error; err != nil {
			return errors.New("update quota policy")
		}
		updated = normalized
		if meta != nil {
			return writeQuotaAudit(
				transaction, *meta, "quota.update", current.ID,
				policyView(current), policyView(normalized),
			)
		}
		return nil
	})
	if err != nil {
		return PolicyView{}, err
	}
	manager.policies.Put(updated)
	return policyView(updated), nil
}

func ensureTenant(transaction *gorm.DB, tenantID string) error {
	var tenant postgres.Tenant
	err := transaction.Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id").
		First(&tenant, "id = ?", tenantID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrTenantNotFound
	}
	if err != nil {
		return errors.New("check quota tenant")
	}
	return nil
}

func (manager *Manager) ListPolicies(
	ctx context.Context,
	tenantID string,
) ([]PolicyView, error) {
	query := manager.database.WithContext(ctx).
		Order("tenant_id ASC, gateway_model ASC, period ASC, id ASC")
	if strings.TrimSpace(tenantID) != "" {
		query = query.Where("tenant_id = ?", strings.TrimSpace(tenantID))
	}
	var rows []postgres.TenantQuotaPolicy
	if err := query.Find(&rows).Error; err != nil {
		return nil, errors.New("list quota policies")
	}
	result := make([]PolicyView, 0, len(rows))
	for _, row := range rows {
		result = append(result, policyView(row))
	}
	return result, nil
}

func (manager *Manager) ListWindows(
	ctx context.Context,
	tenantID string,
	limit int,
) ([]WindowView, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := manager.database.WithContext(ctx).
		Table("quota_windows AS windows").
		Select(
			"windows.policy_id, policies.tenant_id, policies.gateway_model, " +
				"policies.period, windows.window_start, windows.requests_settled, " +
				"windows.requests_reserved, windows.tokens_settled, " +
				"windows.tokens_reserved, windows.cost_settled, " +
				"windows.cost_reserved, windows.updated_at",
		).
		Joins(
			"JOIN tenant_quota_policies AS policies ON policies.id = windows.policy_id",
		).
		Order("windows.window_start DESC, windows.policy_id ASC").
		Limit(limit)
	tenantID = strings.TrimSpace(tenantID)
	if tenantID != "" {
		query = query.Where("policies.tenant_id = ?", tenantID)
	}
	var rows []struct {
		PolicyID         string
		TenantID         string
		GatewayModel     string
		Period           string
		WindowStart      time.Time
		RequestsSettled  int64
		RequestsReserved int64
		TokensSettled    int64
		TokensReserved   int64
		CostSettled      int64
		CostReserved     int64
		UpdatedAt        time.Time
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, errors.New("list quota windows")
	}
	result := make([]WindowView, 0, len(rows))
	for _, row := range rows {
		result = append(result, WindowView{
			PolicyID: row.PolicyID, TenantID: row.TenantID,
			GatewayModel: row.GatewayModel, Period: row.Period,
			WindowStart:      row.WindowStart,
			RequestsSettled:  row.RequestsSettled,
			RequestsReserved: row.RequestsReserved,
			TokensSettled:    row.TokensSettled,
			TokensReserved:   row.TokensReserved,
			CostSettledUSD:   formatUSD(row.CostSettled),
			CostReservedUSD:  formatUSD(row.CostReserved),
			UpdatedAt:        row.UpdatedAt,
		})
	}
	return result, nil
}

func (manager *Manager) Reserve(
	ctx context.Context,
	input ReserveInput,
) (Decision, error) {
	input.GroupID = strings.TrimSpace(input.GroupID)
	input.TenantID = strings.TrimSpace(input.TenantID)
	input.GatewayModel = strings.TrimSpace(input.GatewayModel)
	if input.GroupID == "" || input.TenantID == "" || input.GatewayModel == "" ||
		input.EstimatedInputTokens < 0 || input.EstimatedOutputTokens < 0 {
		return Decision{}, errors.New("quota reservation input is invalid")
	}
	if !manager.HasPolicy(input.TenantID, input.GatewayModel) {
		return Decision{}, nil
	}
	totalTokens, ok := add(input.EstimatedInputTokens, input.EstimatedOutputTokens)
	if !ok {
		return Decision{}, errors.New("quota token estimate overflow")
	}
	now := manager.now().UTC()
	estimatedCost, costKnown := manager.estimateCost(input, now)
	decision := Decision{ReservationID: input.GroupID}
	err := manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var existing int64
		if err := transaction.Model(&postgres.QuotaReservation{}).
			Where("group_id = ?", input.GroupID).Count(&existing).Error; err != nil {
			return errors.New("check quota reservation")
		}
		if existing > 0 {
			return nil
		}
		var policies []postgres.TenantQuotaPolicy
		if err := transaction.Clauses(clause.Locking{Strength: "SHARE"}).
			Where(
				"tenant_id = ? AND enabled = ? AND gateway_model IN ?",
				input.TenantID, true, []string{"*", input.GatewayModel},
			).
			Order("id ASC").
			Find(&policies).Error; err != nil {
			return errors.New("read quota policies")
		}
		for _, policy := range policies {
			decision.AppliedPolicies++
			windowStart := periodStart(now, policy.Period)
			window, err := lockWindow(transaction, policy.ID, windowStart, now)
			if err != nil {
				return err
			}
			costReservation := int64(0)
			if policy.BudgetNanoUSD != nil {
				if !costKnown {
					if policy.OveragePolicy == postgres.QuotaOverageDeny {
						return ErrCostUnknown
					}
					decision.Alerts = append(decision.Alerts, policy.ID+":cost_unknown")
				} else {
					costReservation = estimatedCost
				}
			}
			exceeded := exceeds(policy, window, 1, totalTokens, costReservation)
			if exceeded {
				decision.Exceeded = true
				switch policy.OveragePolicy {
				case postgres.QuotaOverageDeny:
					return ErrExceeded
				case postgres.QuotaOverageAlert:
					decision.Alerts = append(decision.Alerts, policy.ID+":exceeded")
				}
			}
			window.RequestsReserved++
			window.TokensReserved, ok = add(window.TokensReserved, totalTokens)
			if !ok {
				return errors.New("quota reserved token counter overflow")
			}
			window.CostReserved, ok = add(window.CostReserved, costReservation)
			if !ok {
				return errors.New("quota reserved cost counter overflow")
			}
			window.UpdatedAt = now
			if err := transaction.Save(&window).Error; err != nil {
				return errors.New("update quota window")
			}
			reservationID, err := randomUUID()
			if err != nil {
				return errors.New("generate quota reservation ID")
			}
			reservation := postgres.QuotaReservation{
				ID: reservationID, GroupID: input.GroupID, PolicyID: policy.ID,
				WindowStart:      windowStart,
				RequestsReserved: 1, TokensReserved: totalTokens,
				CostReserved: costReservation, State: postgres.QuotaReservationActive,
				Exceeded: exceeded, ExpiresAt: now.Add(manager.settings.ReservationTTL),
				CreatedAt: now,
			}
			if err := transaction.Create(&reservation).Error; err != nil {
				return errors.New("create quota reservation")
			}
		}
		return nil
	})
	return decision, err
}

func (manager *Manager) Settle(
	ctx context.Context,
	groupID string,
	event usage.Event,
) error {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil
	}
	now := manager.now().UTC()
	return manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var reservations []postgres.QuotaReservation
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("group_id = ?", groupID).
			Order("policy_id ASC").
			Find(&reservations).Error; err != nil {
			return errors.New("read quota reservations")
		}
		for _, reservation := range reservations {
			if reservation.State == postgres.QuotaReservationSettled {
				continue
			}
			window, err := lockWindow(
				transaction, reservation.PolicyID, reservation.WindowStart, now,
			)
			if err != nil {
				return err
			}
			requestsActual, tokensActual, costActual := manager.actual(event, reservation)
			if reservation.State == postgres.QuotaReservationActive {
				window.RequestsReserved -= reservation.RequestsReserved
				window.TokensReserved -= reservation.TokensReserved
				window.CostReserved -= reservation.CostReserved
			} else {
				window.RequestsSettled -= reservation.RequestsReserved
				window.TokensSettled -= reservation.TokensReserved
				window.CostSettled -= reservation.CostReserved
			}
			window.RequestsSettled += requestsActual
			window.TokensSettled += tokensActual
			window.CostSettled += costActual
			window.UpdatedAt = now
			if err := transaction.Save(&window).Error; err != nil {
				return errors.New("settle quota window")
			}
			reservation.State = postgres.QuotaReservationSettled
			reservation.RequestsActual = &requestsActual
			reservation.TokensActual = &tokensActual
			reservation.CostActual = &costActual
			reservation.SettledAt = &now
			if err := transaction.Save(&reservation).Error; err != nil {
				return errors.New("settle quota reservation")
			}
		}
		return nil
	})
}

func (manager *Manager) ReapExpired(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	now := manager.now().UTC()
	processed := 0
	err := manager.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var reservations []postgres.QuotaReservation
		if err := transaction.Clauses(clause.Locking{
			Strength: "UPDATE", Options: "SKIP LOCKED",
		}).
			Where("state = ? AND expires_at <= ?", postgres.QuotaReservationActive, now).
			Order("expires_at ASC").
			Limit(limit).
			Find(&reservations).Error; err != nil {
			return errors.New("read expired quota reservations")
		}
		for _, reservation := range reservations {
			window, err := lockWindow(
				transaction, reservation.PolicyID, reservation.WindowStart, now,
			)
			if err != nil {
				return err
			}
			window.RequestsReserved -= reservation.RequestsReserved
			window.TokensReserved -= reservation.TokensReserved
			window.CostReserved -= reservation.CostReserved
			window.RequestsSettled += reservation.RequestsReserved
			window.TokensSettled += reservation.TokensReserved
			window.CostSettled += reservation.CostReserved
			window.UpdatedAt = now
			if err := transaction.Save(&window).Error; err != nil {
				return errors.New("estimate expired quota window")
			}
			requests := reservation.RequestsReserved
			tokens := reservation.TokensReserved
			cost := reservation.CostReserved
			reservation.RequestsActual = &requests
			reservation.TokensActual = &tokens
			reservation.CostActual = &cost
			reservation.State = postgres.QuotaReservationEstimated
			reservation.SettledAt = &now
			if err := transaction.Save(&reservation).Error; err != nil {
				return errors.New("estimate expired quota reservation")
			}
			processed++
		}
		return nil
	})
	return processed, err
}

func (manager *Manager) RunReaper(ctx context.Context) {
	ticker := time.NewTicker(manager.settings.ReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				count, err := manager.ReapExpired(ctx, 100)
				if err != nil || count < 100 {
					break
				}
			}
		}
	}
}

func (manager *Manager) estimateCost(input ReserveInput, now time.Time) (int64, bool) {
	maximum := int64(0)
	known := len(input.Plan.Candidates) > 0
	for _, candidate := range input.Plan.Candidates {
		event := usage.Event{
			RequestedModel: input.GatewayModel,
			ProviderID:     candidate.ProviderID, UpstreamModel: candidate.UpstreamModel,
			Status: usage.StatusSuccess, StartedAt: now,
			Usage: &usage.TokenUsage{
				Input:  input.EstimatedInputTokens,
				Output: input.EstimatedOutputTokens,
				Total:  input.EstimatedInputTokens + input.EstimatedOutputTokens,
			},
		}
		quote := manager.pricing.Quote(event)
		if quote.Snapshot == nil {
			known = false
			continue
		}
		if quote.Snapshot.TotalNanoUSD > maximum {
			maximum = quote.Snapshot.TotalNanoUSD
		}
	}
	return maximum, known
}

func (manager *Manager) actual(
	event usage.Event,
	reservation postgres.QuotaReservation,
) (int64, int64, int64) {
	requests := int64(1)
	tokens := int64(0)
	if event.Usage != nil {
		tokens = event.Usage.Total
	} else if event.Attempts > 0 {
		tokens = reservation.TokensReserved
	}
	cost := int64(0)
	quote := manager.pricing.Quote(event)
	if quote.Snapshot != nil {
		cost = quote.Snapshot.TotalNanoUSD
	} else if event.Attempts > 0 {
		cost = reservation.CostReserved
	}
	return requests, tokens, cost
}

func lockWindow(
	transaction *gorm.DB,
	policyID string,
	start time.Time,
	now time.Time,
) (postgres.QuotaWindow, error) {
	seed := postgres.QuotaWindow{
		PolicyID: policyID, WindowStart: start, UpdatedAt: now,
	}
	if err := transaction.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&seed).Error; err != nil {
		return postgres.QuotaWindow{}, errors.New("create quota window")
	}
	var window postgres.QuotaWindow
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&window, "policy_id = ? AND window_start = ?", policyID, start).Error; err != nil {
		return postgres.QuotaWindow{}, errors.New("lock quota window")
	}
	return window, nil
}

func exceeds(
	policy postgres.TenantQuotaPolicy,
	window postgres.QuotaWindow,
	requests, tokens, cost int64,
) bool {
	return exceedsLimit(
		policy.RequestLimit, window.RequestsSettled, window.RequestsReserved, requests,
	) || exceedsLimit(
		policy.TokenLimit, window.TokensSettled, window.TokensReserved, tokens,
	) || exceedsLimit(
		policy.BudgetNanoUSD, window.CostSettled, window.CostReserved, cost,
	)
}

func exceedsLimit(limit *int64, settled, reserved, incoming int64) bool {
	if limit == nil {
		return false
	}
	total, ok := add(settled, reserved)
	if !ok {
		return true
	}
	total, ok = add(total, incoming)
	return !ok || total > *limit
}

func normalizePolicy(input PolicyInput) (postgres.TenantQuotaPolicy, error) {
	policy := postgres.TenantQuotaPolicy{
		TenantID:     strings.TrimSpace(input.TenantID),
		GatewayModel: strings.TrimSpace(input.GatewayModel),
		Period:       input.Period, RequestLimit: clone(input.RequestLimit),
		TokenLimit: clone(input.TokenLimit), OveragePolicy: input.OveragePolicy,
		Enabled: input.Enabled,
	}
	if policy.GatewayModel == "" {
		policy.GatewayModel = "*"
	}
	switch policy.Period {
	case postgres.QuotaPeriodMinute, postgres.QuotaPeriodHour,
		postgres.QuotaPeriodDay, postgres.QuotaPeriodMonth:
	default:
		return postgres.TenantQuotaPolicy{}, ErrInvalidPolicy
	}
	switch policy.OveragePolicy {
	case postgres.QuotaOverageDeny, postgres.QuotaOverageAllow,
		postgres.QuotaOverageAlert:
	default:
		return postgres.TenantQuotaPolicy{}, ErrInvalidPolicy
	}
	if policy.TenantID == "" || len(policy.GatewayModel) > 200 ||
		!validLimit(policy.RequestLimit) || !validLimit(policy.TokenLimit) {
		return postgres.TenantQuotaPolicy{}, ErrInvalidPolicy
	}
	if input.BudgetUSD != nil {
		budget, err := parseUSD(*input.BudgetUSD)
		if err != nil {
			return postgres.TenantQuotaPolicy{}, ErrInvalidPolicy
		}
		policy.BudgetNanoUSD = &budget
	}
	if policy.RequestLimit == nil && policy.TokenLimit == nil &&
		policy.BudgetNanoUSD == nil {
		return postgres.TenantQuotaPolicy{}, ErrInvalidPolicy
	}
	return policy, nil
}

func policyView(policy postgres.TenantQuotaPolicy) PolicyView {
	var budget *string
	if policy.BudgetNanoUSD != nil {
		value := formatUSD(*policy.BudgetNanoUSD)
		budget = &value
	}
	return PolicyView{
		ID: policy.ID, TenantID: policy.TenantID, GatewayModel: policy.GatewayModel,
		Period: policy.Period, RequestLimit: clone(policy.RequestLimit),
		TokenLimit: clone(policy.TokenLimit), BudgetUSD: budget,
		OveragePolicy: policy.OveragePolicy, Enabled: policy.Enabled,
		Version: policy.Version, CreatedAt: policy.CreatedAt, UpdatedAt: policy.UpdatedAt,
	}
}

func periodStart(at time.Time, period postgres.QuotaPeriod) time.Time {
	at = at.UTC()
	switch period {
	case postgres.QuotaPeriodMinute:
		return at.Truncate(time.Minute)
	case postgres.QuotaPeriodHour:
		return at.Truncate(time.Hour)
	case postgres.QuotaPeriodDay:
		return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
}

func validLimit(value *int64) bool {
	return value == nil || (*value > 0 && *value <= 1_000_000_000_000_000)
}

func clone(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func add(left, right int64) (int64, bool) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, false
	}
	if right < 0 && left < math.MinInt64-right {
		return 0, false
	}
	return left + right, true
}

func parseUSD(raw string) (int64, error) {
	value, ok := new(big.Rat).SetString(strings.TrimSpace(raw))
	if !ok || value.Sign() < 0 {
		return 0, errors.New("invalid USD amount")
	}
	scaled := new(big.Rat).Mul(value, big.NewRat(1_000_000_000, 1))
	numerator := new(big.Int).Set(scaled.Num())
	denominator := new(big.Int).Set(scaled.Denom())
	numerator.Add(numerator, new(big.Int).Quo(denominator, big.NewInt(2)))
	numerator.Quo(numerator, denominator)
	if !numerator.IsInt64() {
		return 0, errors.New("USD amount is too large")
	}
	return numerator.Int64(), nil
}

func formatUSD(nano int64) string {
	value := new(big.Rat).SetFrac(big.NewInt(nano), big.NewInt(1_000_000_000))
	return value.FloatString(9)
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

func writeQuotaAudit(
	transaction *gorm.DB,
	meta MutationMeta,
	action string,
	resourceID string,
	before any,
	after any,
) error {
	if strings.TrimSpace(meta.ActorID) == "" ||
		strings.TrimSpace(meta.RequestID) == "" {
		return errors.New("quota audit metadata is incomplete")
	}
	beforeJSON, err := quotaAuditJSON(before)
	if err != nil {
		return err
	}
	afterJSON, err := quotaAuditJSON(after)
	if err != nil {
		return err
	}
	if err := transaction.Create(&postgres.AuditLog{
		PrincipalID: meta.ActorID, Action: action,
		ResourceType: "quota_policy", ResourceID: resourceID,
		RequestID: meta.RequestID, RemoteIP: meta.RemoteIP,
		BeforeJSON: beforeJSON, AfterJSON: afterJSON,
		Outcome: "success", CreatedAt: time.Now().UTC(),
	}).Error; err != nil {
		return errors.New("append quota audit log")
	}
	return nil
}

func quotaAuditJSON(value any) (*string, error) {
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
