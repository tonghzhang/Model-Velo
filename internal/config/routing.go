package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"model-velo/internal/routing"
)

const (
	routingJSONEnv          = "MODEL_VELO_ROUTING_JSON"
	currentProviderID       = "upstream"
	maximumRoutingJSONBytes = 64 << 10
)

type routingJSON struct {
	Providers []routingProviderJSON `json:"providers"`
	Routes    []routingRuleJSON     `json:"routes"`
}

type routingProviderJSON struct {
	ID     string   `json:"id"`
	Type   string   `json:"type"`
	Models []string `json:"models"`
}

type routingRuleJSON struct {
	Model      string                 `json:"model"`
	Candidates []routingCandidateJSON `json:"candidates"`
}

type routingCandidateJSON struct {
	Provider      string `json:"provider"`
	UpstreamModel string `json:"upstream_model"`
}

func LoadRouting(routeVersion string) (routing.Definition, error) {
	raw := strings.TrimSpace(os.Getenv(routingJSONEnv))
	if raw == "" {
		return routing.SingleProviderDefinition(currentProviderID, routeVersion), nil
	}
	if len(raw) > maximumRoutingJSONBytes {
		return routing.Definition{}, fmt.Errorf("%s exceeds %d bytes", routingJSONEnv, maximumRoutingJSONBytes)
	}

	var document routingJSON
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return routing.Definition{}, fmt.Errorf("%s must be a valid routing object: %w", routingJSONEnv, err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return routing.Definition{}, fmt.Errorf("%s must contain one routing object: %w", routingJSONEnv, err)
	}
	if len(document.Providers) != 1 {
		return routing.Definition{}, fmt.Errorf("%s currently requires exactly one provider", routingJSONEnv)
	}

	definition := routing.Definition{Version: routeVersion}
	for _, configuredProvider := range document.Providers {
		definition.Providers = append(definition.Providers, routing.Provider{
			ID:            configuredProvider.ID,
			Type:          configuredProvider.Type,
			Models:        configuredProvider.Models,
			ConfigVersion: routeVersion,
		})
	}
	for _, configuredRoute := range document.Routes {
		rule := routing.Rule{Model: configuredRoute.Model}
		for _, configuredCandidate := range configuredRoute.Candidates {
			rule.Candidates = append(rule.Candidates, routing.Target{
				ProviderID:    configuredCandidate.Provider,
				UpstreamModel: configuredCandidate.UpstreamModel,
			})
		}
		definition.Rules = append(definition.Rules, rule)
	}

	return definition, nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("unexpected trailing JSON value")
}
