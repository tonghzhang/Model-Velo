package routing

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const ProviderTypeOpenAICompatible = "openai-compatible"

var (
	ErrNoRoute        = errors.New("no route is configured for the requested model")
	identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	versionPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type Provider struct {
	ID            string
	Type          string
	Models        []string
	ConfigVersion string
}

type Target struct {
	ProviderID    string
	UpstreamModel string
}

type Rule struct {
	Model      string
	Candidates []Target
}

type Definition struct {
	Version   string
	Providers []Provider
	Rules     []Rule
}

type Candidate struct {
	ProviderID    string
	ProviderType  string
	UpstreamModel string
	Priority      int
}

type Plan struct {
	TenantID       string
	RequestedModel string
	ConfigVersion  string
	Candidates     []Candidate
}

func (p Plan) Primary() (Candidate, bool) {
	if len(p.Candidates) == 0 {
		return Candidate{}, false
	}
	return p.Candidates[0], true
}

type Router struct {
	version        string
	providers      map[string]Provider
	exact          map[string][]Target
	defaultTargets []Target
}

func New(definition Definition) (*Router, error) {
	version := strings.TrimSpace(definition.Version)
	if !versionPattern.MatchString(version) {
		return nil, errors.New("routing config version must be a 1 to 64 character identifier")
	}

	providers, err := validateProviders(definition.Providers, version)
	if err != nil {
		return nil, err
	}

	router := &Router{
		version:   version,
		providers: providers,
		exact:     make(map[string][]Target),
	}
	for index, rule := range definition.Rules {
		model := strings.TrimSpace(rule.Model)
		if model == "" {
			return nil, fmt.Errorf("route %d has an empty model", index)
		}
		targets, err := validateTargets(rule.Candidates, model, providers)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", model, err)
		}
		if model == "*" {
			if router.defaultTargets != nil {
				return nil, errors.New("routing config contains more than one default route")
			}
			router.defaultTargets = targets
			continue
		}
		if _, exists := router.exact[model]; exists {
			return nil, fmt.Errorf("routing config contains duplicate route %q", model)
		}
		router.exact[model] = targets
	}
	if len(router.exact) == 0 && len(router.defaultTargets) == 0 {
		return nil, errors.New("routing config must contain at least one route")
	}

	return router, nil
}

func SingleProviderDefinition(providerID, version string) Definition {
	return Definition{
		Version: version,
		Providers: []Provider{{
			ID:            providerID,
			Type:          ProviderTypeOpenAICompatible,
			Models:        []string{"*"},
			ConfigVersion: version,
		}},
		Rules: []Rule{{
			Model:      "*",
			Candidates: []Target{{ProviderID: providerID}},
		}},
	}
}

func (r *Router) Plan(tenantID, requestedModel string) (Plan, error) {
	tenantID = strings.TrimSpace(tenantID)
	requestedModel = strings.TrimSpace(requestedModel)
	if tenantID == "" || requestedModel == "" {
		return Plan{}, ErrNoRoute
	}

	targets := r.exact[requestedModel]
	if len(targets) == 0 {
		targets = r.defaultTargets
	}
	if len(targets) == 0 {
		return Plan{}, ErrNoRoute
	}

	candidates := make([]Candidate, 0, len(targets))
	for priority, target := range targets {
		provider := r.providers[target.ProviderID]
		upstreamModel := target.UpstreamModel
		if upstreamModel == "" {
			upstreamModel = requestedModel
		}
		if !providerSupports(provider, upstreamModel) {
			continue
		}
		candidates = append(candidates, Candidate{
			ProviderID:    provider.ID,
			ProviderType:  provider.Type,
			UpstreamModel: upstreamModel,
			Priority:      priority,
		})
	}
	if len(candidates) == 0 {
		return Plan{}, ErrNoRoute
	}

	return Plan{
		TenantID:       tenantID,
		RequestedModel: requestedModel,
		ConfigVersion:  r.version,
		Candidates:     candidates,
	}, nil
}

func validateProviders(configured []Provider, version string) (map[string]Provider, error) {
	if len(configured) == 0 {
		return nil, errors.New("routing config must contain at least one provider")
	}

	providers := make(map[string]Provider, len(configured))
	for index, provider := range configured {
		provider.ID = strings.TrimSpace(provider.ID)
		provider.Type = strings.TrimSpace(provider.Type)
		provider.ConfigVersion = strings.TrimSpace(provider.ConfigVersion)
		if !identifierPattern.MatchString(provider.ID) {
			return nil, fmt.Errorf("provider %d has an invalid ID", index)
		}
		if provider.Type != ProviderTypeOpenAICompatible {
			return nil, fmt.Errorf("provider %q has unsupported type %q", provider.ID, provider.Type)
		}
		if provider.ConfigVersion != version {
			return nil, fmt.Errorf("provider %q config version does not match routing version", provider.ID)
		}
		if _, exists := providers[provider.ID]; exists {
			return nil, fmt.Errorf("routing config contains duplicate provider %q", provider.ID)
		}

		models, err := normalizeModels(provider.Models)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", provider.ID, err)
		}
		provider.Models = models
		providers[provider.ID] = provider
	}
	return providers, nil
}

func normalizeModels(configured []string) ([]string, error) {
	if len(configured) == 0 {
		return nil, errors.New("models must not be empty")
	}

	seen := make(map[string]struct{}, len(configured))
	models := make([]string, 0, len(configured))
	for _, configuredModel := range configured {
		model := strings.TrimSpace(configuredModel)
		if model == "" {
			return nil, errors.New("models must not contain an empty value")
		}
		if _, exists := seen[model]; exists {
			return nil, fmt.Errorf("models contain duplicate %q", model)
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	sort.Strings(models)
	return models, nil
}

func validateTargets(configured []Target, routeModel string, providers map[string]Provider) ([]Target, error) {
	if len(configured) == 0 {
		return nil, errors.New("candidates must not be empty")
	}

	seen := make(map[string]struct{}, len(configured))
	targets := make([]Target, 0, len(configured))
	for index, target := range configured {
		target.ProviderID = strings.TrimSpace(target.ProviderID)
		target.UpstreamModel = strings.TrimSpace(target.UpstreamModel)
		provider, exists := providers[target.ProviderID]
		if !exists {
			return nil, fmt.Errorf("candidate %d references unknown provider %q", index, target.ProviderID)
		}

		resolvedModel := target.UpstreamModel
		if resolvedModel == "" {
			resolvedModel = routeModel
		}
		if routeModel == "*" && target.UpstreamModel == "" {
			resolvedModel = "*"
		}
		if !providerSupports(provider, resolvedModel) {
			return nil, fmt.Errorf("candidate %d maps to unsupported model %q", index, resolvedModel)
		}

		identity := target.ProviderID + "\x00" + target.UpstreamModel
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return nil, errors.New("candidates contain no usable target")
	}
	return targets, nil
}

func providerSupports(provider Provider, model string) bool {
	for _, supportedModel := range provider.Models {
		if supportedModel == "*" || supportedModel == model {
			return true
		}
	}
	return false
}
