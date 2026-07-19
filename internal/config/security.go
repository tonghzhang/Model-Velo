package config

import (
	"fmt"
	"os"
	"strings"
)

const (
	apiKeyPepperEnv    = "MODEL_VELO_API_KEY_PEPPER"
	minimumPepperBytes = 32
)

type APIKeySecurity struct {
	Pepper []byte
}

func LoadAPIKeySecurity() (APIKeySecurity, error) {
	pepper := os.Getenv(apiKeyPepperEnv)
	if strings.TrimSpace(pepper) == "" {
		return APIKeySecurity{}, fmt.Errorf("%s is required", apiKeyPepperEnv)
	}
	if len([]byte(pepper)) < minimumPepperBytes {
		return APIKeySecurity{}, fmt.Errorf("%s must contain at least %d bytes", apiKeyPepperEnv, minimumPepperBytes)
	}

	return APIKeySecurity{Pepper: []byte(pepper)}, nil
}
