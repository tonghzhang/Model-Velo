package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	adminKeyPepperEnv       = "MODEL_VELO_ADMIN_KEY_PEPPER"
	controlMasterKeyEnv     = "MODEL_VELO_CONTROL_MASTER_KEY"
	controlRefreshEnv       = "MODEL_VELO_CONTROL_REFRESH_INTERVAL"
	defaultControlRefresh   = 5 * time.Second
	minimumAdminPepperBytes = 32
)

type ControlPlane struct {
	AdminPepper     []byte
	MasterKey       []byte
	RefreshInterval time.Duration
}

func LoadAdminKeySecurity() ([]byte, error) {
	pepper := os.Getenv(adminKeyPepperEnv)
	if len([]byte(pepper)) < minimumAdminPepperBytes {
		return nil, fmt.Errorf(
			"%s must contain at least %d bytes",
			adminKeyPepperEnv, minimumAdminPepperBytes,
		)
	}
	return []byte(pepper), nil
}

func LoadControlPlane() (ControlPlane, error) {
	pepper, err := LoadAdminKeySecurity()
	if err != nil {
		return ControlPlane{}, err
	}
	encodedKey := strings.TrimSpace(os.Getenv(controlMasterKeyEnv))
	if encodedKey == "" {
		return ControlPlane{}, fmt.Errorf("%s is required", controlMasterKeyEnv)
	}
	masterKey, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(masterKey) != 32 {
		return ControlPlane{}, fmt.Errorf(
			"%s must be base64 for exactly 32 bytes", controlMasterKeyEnv,
		)
	}
	refresh, err := loadPositiveDuration(controlRefreshEnv, defaultControlRefresh)
	if err != nil {
		return ControlPlane{}, err
	}
	return ControlPlane{
		AdminPepper:     append([]byte(nil), pepper...),
		MasterKey:       append([]byte(nil), masterKey...),
		RefreshInterval: refresh,
	}, nil
}
