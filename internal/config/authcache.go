package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	authCacheEnabledEnv             = "MODEL_VELO_AUTH_CACHE_ENABLED"
	authCacheL1MaxEntriesEnv        = "MODEL_VELO_AUTH_CACHE_L1_MAX_ENTRIES"
	authCacheL1TTLEnv               = "MODEL_VELO_AUTH_CACHE_L1_TTL"
	authCacheL2TTLEnv               = "MODEL_VELO_AUTH_CACHE_L2_TTL"
	authCacheKeyPrefixEnv           = "MODEL_VELO_AUTH_CACHE_KEY_PREFIX"
	authCacheInvalidationChannelEnv = "MODEL_VELO_AUTH_CACHE_INVALIDATION_CHANNEL"

	defaultAuthCacheL1MaxEntries = 10_000
	defaultAuthCacheL1TTL        = 15 * time.Second
	defaultAuthCacheL2TTL        = 30 * time.Second
)

var authCacheNamespacePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9:_.-]{0,127}$`)

type AuthCache struct {
	Enabled             bool
	L1MaxEntries        int
	L1TTL               time.Duration
	L2TTL               time.Duration
	KeyPrefix           string
	InvalidationChannel string
}

func LoadAuthCache() (AuthCache, error) {
	enabled, err := loadOptionalBool(authCacheEnabledEnv, true)
	if err != nil {
		return AuthCache{}, err
	}
	maxEntries, err := loadInt(
		authCacheL1MaxEntriesEnv,
		defaultAuthCacheL1MaxEntries,
		false,
	)
	if err != nil || maxEntries > 1_000_000 {
		return AuthCache{}, fmt.Errorf(
			"%s must be between 1 and 1000000",
			authCacheL1MaxEntriesEnv,
		)
	}
	l1TTL, err := loadPositiveDuration(authCacheL1TTLEnv, defaultAuthCacheL1TTL)
	if err != nil || l1TTL < time.Second || l1TTL > 5*time.Minute {
		return AuthCache{}, fmt.Errorf(
			"%s must be between 1s and 5m",
			authCacheL1TTLEnv,
		)
	}
	l2TTL, err := loadPositiveDuration(authCacheL2TTLEnv, defaultAuthCacheL2TTL)
	if err != nil || l2TTL < l1TTL || l2TTL > 10*time.Minute {
		return AuthCache{}, fmt.Errorf(
			"%s must be between %s and 10m",
			authCacheL2TTLEnv,
			l1TTL,
		)
	}

	environment := strings.ToLower(strings.TrimSpace(os.Getenv(environmentEnv)))
	if !environmentPattern.MatchString(environment) {
		return AuthCache{}, fmt.Errorf(
			"%s must contain 1 to 32 lowercase letters, digits, underscores, or hyphens",
			environmentEnv,
		)
	}
	keyPrefix := strings.TrimSpace(os.Getenv(authCacheKeyPrefixEnv))
	if keyPrefix == "" {
		keyPrefix = "model-velo:" + environment + ":auth:v1"
	}
	if !authCacheNamespacePattern.MatchString(keyPrefix) {
		return AuthCache{}, fmt.Errorf(
			"%s must contain 1 to 128 safe namespace characters",
			authCacheKeyPrefixEnv,
		)
	}
	channel := strings.TrimSpace(os.Getenv(authCacheInvalidationChannelEnv))
	if channel == "" {
		channel = keyPrefix + ":invalidate"
	}
	if !authCacheNamespacePattern.MatchString(channel) {
		return AuthCache{}, fmt.Errorf(
			"%s must contain 1 to 128 safe namespace characters",
			authCacheInvalidationChannelEnv,
		)
	}
	if channel == keyPrefix {
		return AuthCache{}, fmt.Errorf(
			"%s must differ from %s",
			authCacheInvalidationChannelEnv,
			authCacheKeyPrefixEnv,
		)
	}

	return AuthCache{
		Enabled:             enabled,
		L1MaxEntries:        maxEntries,
		L1TTL:               l1TTL,
		L2TTL:               l2TTL,
		KeyPrefix:           keyPrefix,
		InvalidationChannel: channel,
	}, nil
}

func loadOptionalBool(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return value, nil
}
