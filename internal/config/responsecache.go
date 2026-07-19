package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	responseCacheTTLEnv          = "MODEL_VELO_CACHE_TTL"
	responseCacheRouteVersionEnv = "MODEL_VELO_CACHE_ROUTE_VERSION"
	defaultCacheRouteVersion     = "single-provider-v1"
)

const (
	defaultResponseCacheTTL = 5 * time.Minute
	minimumResponseCacheTTL = time.Second
	maximumResponseCacheTTL = 24 * time.Hour
)

var cacheRouteVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type ResponseCache struct {
	TTL          time.Duration
	RouteVersion string
}

func LoadResponseCache() (ResponseCache, error) {
	ttl, err := loadResponseCacheTTL()
	if err != nil {
		return ResponseCache{}, err
	}

	routeVersion := strings.TrimSpace(os.Getenv(responseCacheRouteVersionEnv))
	if routeVersion == "" {
		routeVersion = defaultCacheRouteVersion
	}
	if !cacheRouteVersionPattern.MatchString(routeVersion) {
		return ResponseCache{}, fmt.Errorf(
			"%s must contain 1 to 64 letters, digits, dots, underscores, or hyphens",
			responseCacheRouteVersionEnv,
		)
	}

	return ResponseCache{TTL: ttl, RouteVersion: routeVersion}, nil
}

func loadResponseCacheTTL() (time.Duration, error) {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(responseCacheTTLEnv)))
	if value == "" {
		return defaultResponseCacheTTL, nil
	}
	if value == "0" || value == "off" {
		return 0, nil
	}

	ttl, err := time.ParseDuration(value)
	if err != nil || ttl < minimumResponseCacheTTL || ttl > maximumResponseCacheTTL {
		return 0, fmt.Errorf(
			"%s must be off, 0, or a duration between %s and %s",
			responseCacheTTLEnv,
			minimumResponseCacheTTL,
			maximumResponseCacheTTL,
		)
	}
	return ttl, nil
}
