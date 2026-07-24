package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	quotaReservationTTLenv = "MODEL_VELO_QUOTA_RESERVATION_TTL"
	quotaReapIntervalEnv   = "MODEL_VELO_QUOTA_REAP_INTERVAL"
	quotaDefaultOutputEnv  = "MODEL_VELO_QUOTA_DEFAULT_MAX_OUTPUT_TOKENS"
	defaultQuotaTTL        = 15 * time.Minute
	defaultQuotaReap       = time.Minute
	defaultQuotaOutput     = 4096
)

type Quota struct {
	ReservationTTL         time.Duration
	ReapInterval           time.Duration
	DefaultMaxOutputTokens int64
}

func LoadQuota() (Quota, error) {
	ttl, err := loadPositiveDuration(quotaReservationTTLenv, defaultQuotaTTL)
	if err != nil {
		return Quota{}, err
	}
	if ttl < time.Minute || ttl > 24*time.Hour {
		return Quota{}, fmt.Errorf("%s must be between 1m and 24h", quotaReservationTTLenv)
	}
	reap, err := loadPositiveDuration(quotaReapIntervalEnv, defaultQuotaReap)
	if err != nil {
		return Quota{}, err
	}
	if reap > ttl {
		return Quota{}, fmt.Errorf("%s must not exceed reservation TTL", quotaReapIntervalEnv)
	}
	output := int64(defaultQuotaOutput)
	if raw := strings.TrimSpace(os.Getenv(quotaDefaultOutputEnv)); raw != "" {
		output, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || output < 1 || output > 1_000_000 {
			return Quota{}, fmt.Errorf("%s must be between 1 and 1000000", quotaDefaultOutputEnv)
		}
	}
	return Quota{
		ReservationTTL:         ttl,
		ReapInterval:           reap,
		DefaultMaxOutputTokens: output,
	}, nil
}
