package routing

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"model-velo/internal/provider"
)

var (
	ErrNoRoute               = errors.New("no route is configured for the requested model")
	ErrCapabilityUnavailable = errors.New("no route supports the requested capabilities")
	identifierPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

type Provider struct {
	ID                string
	Type              string
	BaseURL           string
	Models            []string
	ModelCapabilities map[string][]provider.Capability
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
	Providers []Provider
	Rules     []Rule
}

type Candidate struct {
	ProviderID    string
	UpstreamModel string
	Priority      int
}

type Plan struct {
	RequestedModel string
	Candidates     []Candidate
}

func (p Plan) Primary() (Candidate, bool) {
	if len(p.Candidates) == 0 {
		return Candidate{}, false
	}
	return p.Candidates[0], true
}

type Router struct {
	providers      map[string]routeProvider
	exact          map[string][]Target
	defaultTargets []Target
}

type routeProvider struct {
	id           string
	models       []string
	capabilities map[string]map[provider.Capability]struct{}
}

func New(definition Definition) (*Router, error) {
	providers, err := validateProviders(definition.Providers)
	if err != nil {
		return nil, err
	}

	router := &Router{
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

func (r *Router) Plan(requestedModel string, required []provider.Capability) (Plan, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
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
	modelAvailable := false
	for priority, target := range targets {
		provider := r.providers[target.ProviderID]
		upstreamModel := target.UpstreamModel
		if upstreamModel == "" {
			upstreamModel = requestedModel
		}
		if !providerSupports(provider, upstreamModel) {
			continue
		}
		modelAvailable = true
		if !providerSupportsCapabilities(provider, upstreamModel, required) {
			continue
		}
		candidates = append(candidates, Candidate{
			ProviderID:    provider.id,
			UpstreamModel: upstreamModel,
			Priority:      priority,
		})
	}
	if len(candidates) == 0 {
		if modelAvailable {
			return Plan{}, ErrCapabilityUnavailable
		}
		return Plan{}, ErrNoRoute
	}

	return Plan{
		RequestedModel: requestedModel,
		Candidates:     candidates,
	}, nil
}

func (r *Router) Models() []string {
	if r == nil {
		return nil
	}
	models := make([]string, 0, len(r.exact))
	for model := range r.exact {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func validateProviders(configured []Provider) (map[string]routeProvider, error) {
	if len(configured) == 0 {
		return nil, errors.New("routing config must contain at least one provider")
	}

	providers := make(map[string]routeProvider, len(configured))
	for index, configuredProvider := range configured {
		configuredProvider.ID = strings.TrimSpace(configuredProvider.ID)
		configuredProvider.Type = strings.TrimSpace(configuredProvider.Type)
		configuredProvider.BaseURL = strings.TrimSpace(configuredProvider.BaseURL)
		if !identifierPattern.MatchString(configuredProvider.ID) {
			return nil, fmt.Errorf("provider %d has an invalid ID", index)
		}
		if !provider.SupportedProtocol(configuredProvider.Type) {
			return nil, fmt.Errorf("provider %q has unsupported type %q", configuredProvider.ID, configuredProvider.Type)
		}
		if _, exists := providers[configuredProvider.ID]; exists {
			return nil, fmt.Errorf("routing config contains duplicate provider %q", configuredProvider.ID)
		}

		models, err := normalizeModels(configuredProvider.Models)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", configuredProvider.ID, err)
		}
		capabilities, err := normalizeCapabilities(
			models,
			configuredProvider.Type,
			configuredProvider.ModelCapabilities,
		)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", configuredProvider.ID, err)
		}
		providers[configuredProvider.ID] = routeProvider{
			id:           configuredProvider.ID,
			models:       models,
			capabilities: capabilities,
		}
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

func validateTargets(configured []Target, routeModel string, providers map[string]routeProvider) ([]Target, error) {
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

func providerSupports(provider routeProvider, model string) bool {
	for _, supportedModel := range provider.models {
		if supportedModel == "*" || supportedModel == model {
			return true
		}
	}
	return false
}

func normalizeCapabilities(
	models []string,
	protocol string,
	configured map[string][]provider.Capability,
) (map[string]map[provider.Capability]struct{}, error) {
	modelSet := make(map[string]struct{}, len(models))
	wildcard := false
	for _, model := range models {
		modelSet[model] = struct{}{}
		wildcard = wildcard || model == "*"
	}

	capabilities := make(map[string]map[provider.Capability]struct{}, len(models)+len(configured))
	for configuredModel, values := range configured {
		model := strings.TrimSpace(configuredModel)
		if model == "" {
			return nil, errors.New("model capabilities contain an empty model")
		}
		if _, ok := modelSet[model]; !ok && !wildcard {
			return nil, fmt.Errorf("model capabilities reference unsupported model %q", model)
		}
		if _, duplicate := capabilities[model]; duplicate {
			return nil, fmt.Errorf("model capabilities contain duplicate model %q", model)
		}
		set, err := normalizeCapabilitySet(protocol, values)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", model, err)
		}
		capabilities[model] = set
	}
	for _, model := range models {
		if capabilities[model] == nil {
			capabilities[model] = map[provider.Capability]struct{}{provider.CapabilityText: {}}
		}
	}
	return capabilities, nil
}

func normalizeCapabilitySet(
	protocol string,
	configured []provider.Capability,
) (map[provider.Capability]struct{}, error) {
	if len(configured) == 0 {
		return nil, errors.New("capabilities must not be empty")
	}
	set := make(map[provider.Capability]struct{}, len(configured))
	for _, capability := range configured {
		capability = provider.Capability(strings.ToLower(strings.TrimSpace(string(capability))))
		switch capability {
		case provider.CapabilityText,
			provider.CapabilityImage,
			provider.CapabilityAudio,
			provider.CapabilityFile,
			provider.CapabilityTools,
			provider.CapabilityStructured,
			provider.CapabilityEmbedding:
			if !provider.ProtocolSupportsCapability(protocol, capability) {
				return nil, fmt.Errorf("protocol %q cannot carry capability %q", protocol, capability)
			}
			if _, duplicate := set[capability]; duplicate {
				return nil, fmt.Errorf("capabilities contain duplicate %q", capability)
			}
			set[capability] = struct{}{}
		default:
			return nil, fmt.Errorf("unsupported capability %q", capability)
		}
	}
	if _, text := set[provider.CapabilityText]; !text {
		if _, embedding := set[provider.CapabilityEmbedding]; !embedding {
			return nil, errors.New("model capabilities must include text or embedding")
		}
	}
	return set, nil
}

func providerSupportsCapabilities(
	providerConfig routeProvider,
	model string,
	required []provider.Capability,
) bool {
	available := providerConfig.capabilities[model]
	if available == nil {
		available = providerConfig.capabilities["*"]
	}
	for _, capability := range required {
		if _, ok := available[capability]; !ok {
			return false
		}
	}
	return true
}
