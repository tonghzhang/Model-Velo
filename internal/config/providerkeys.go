package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"model-velo/internal/reliability"
)

const (
	providerKeysJSONEnv          = "MODEL_VELO_PROVIDER_KEYS_JSON"
	maximumProviderKeysJSONBytes = 64 << 10
)

type providerKeysJSON struct {
	Providers []providerKeySetJSON `json:"providers"`
}

type providerKeySetJSON struct {
	ProviderID string            `json:"provider_id"`
	Keys       []providerKeyJSON `json:"keys"`
}

type providerKeyJSON struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

func LoadProviderKeys() ([]reliability.ProviderKeySet, error) {
	raw := strings.TrimSpace(os.Getenv(providerKeysJSONEnv))
	if raw == "" {
		return nil, fmt.Errorf("%s is required", providerKeysJSONEnv)
	}
	if len(raw) > maximumProviderKeysJSONBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", providerKeysJSONEnv, maximumProviderKeysJSONBytes)
	}

	var document providerKeysJSON
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%s must be a valid provider key object: %w", providerKeysJSONEnv, err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return nil, fmt.Errorf("%s must contain one provider key object: %w", providerKeysJSONEnv, err)
	}

	configured := make([]reliability.ProviderKeySet, 0, len(document.Providers))
	for _, provider := range document.Providers {
		set := reliability.ProviderKeySet{ProviderID: provider.ProviderID}
		for _, key := range provider.Keys {
			set.Keys = append(set.Keys, reliability.ProviderKey{ID: key.ID, Secret: key.Secret})
		}
		configured = append(configured, set)
	}
	return configured, nil
}
