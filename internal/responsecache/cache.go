package responsecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type routeVersionContextKey struct{}

type Status string

const (
	StatusHit    Status = "HIT"
	StatusMiss   Status = "MISS"
	StatusBypass Status = "BYPASS"
)

type Result struct {
	Status Status
	Body   []byte
}

type Cache struct {
	client       *goredis.Client
	environment  string
	routeVersion string
	ttl          time.Duration
}

func New(client *goredis.Client, environment, routeVersion string, ttl time.Duration) (*Cache, error) {
	if client == nil {
		return nil, errors.New("response cache Redis client is nil")
	}
	environment = strings.TrimSpace(environment)
	routeVersion = strings.TrimSpace(routeVersion)
	if environment == "" || routeVersion == "" || ttl < 0 {
		return nil, errors.New("response cache settings are invalid")
	}

	return &Cache{
		client:       client,
		environment:  environment,
		routeVersion: routeVersion,
		ttl:          ttl,
	}, nil
}

func (cache *Cache) Lookup(ctx context.Context, tenantID, model string, requestBody []byte) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if cache.ttl == 0 {
		return Result{Status: StatusBypass}, nil
	}

	key, cacheable, err := cache.keyForRouteVersion(
		routeVersionFromContext(ctx, cache.routeVersion),
		tenantID,
		model,
		requestBody,
	)
	if err != nil {
		return Result{}, err
	}
	if !cacheable {
		return Result{Status: StatusBypass}, nil
	}

	body, err := cache.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return Result{Status: StatusMiss}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("read response cache: %w", err)
	}
	if !json.Valid(body) {
		return Result{}, errors.New("read response cache: cached response is invalid JSON")
	}
	return Result{Status: StatusHit, Body: body}, nil
}

func (cache *Cache) Store(
	ctx context.Context,
	tenantID, model string,
	requestBody, responseBody []byte,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cache.ttl == 0 || !json.Valid(responseBody) {
		return nil
	}

	key, cacheable, err := cache.keyForRouteVersion(
		routeVersionFromContext(ctx, cache.routeVersion),
		tenantID,
		model,
		requestBody,
	)
	if err != nil {
		return err
	}
	if !cacheable {
		return nil
	}
	if err := cache.client.Set(ctx, key, responseBody, cache.ttl).Err(); err != nil {
		return fmt.Errorf("write response cache: %w", err)
	}
	return nil
}

func (cache *Cache) key(tenantID, model string, requestBody []byte) (string, bool, error) {
	return cache.keyForRouteVersion(cache.routeVersion, tenantID, model, requestBody)
}

func (cache *Cache) keyForRouteVersion(
	routeVersion string,
	tenantID string,
	model string,
	requestBody []byte,
) (string, bool, error) {
	tenantID = strings.TrimSpace(tenantID)
	model = strings.TrimSpace(model)
	routeVersion = strings.TrimSpace(routeVersion)
	if tenantID == "" || model == "" || routeVersion == "" {
		return "", false, errors.New("response cache tenant and model are required")
	}

	canonicalRequest, ok := canonicalizeJSON(requestBody)
	if !ok {
		return "", false, nil
	}
	tenantDigest := sha256.Sum256([]byte(tenantID))
	modelDigest := sha256.Sum256([]byte(model))
	routeDigest := sha256.Sum256([]byte(routeVersion))
	requestDigest := sha256.Sum256(canonicalRequest)
	return fmt.Sprintf(
		"model-velo:response-cache:v1:%s:tenant:%x:model:%x:route:%x:request:%x",
		cache.environment,
		tenantDigest,
		modelDigest,
		routeDigest,
		requestDigest,
	), true, nil
}

// WithRouteVersion pins cache reads and writes to the runtime snapshot used by
// one request. A hot route change therefore cannot mix old responses into the
// new cache namespace.
func WithRouteVersion(ctx context.Context, routeVersion string) context.Context {
	routeVersion = strings.TrimSpace(routeVersion)
	if ctx == nil || routeVersion == "" {
		return ctx
	}
	return context.WithValue(ctx, routeVersionContextKey{}, routeVersion)
}

func routeVersionFromContext(ctx context.Context, fallback string) string {
	if ctx == nil {
		return fallback
	}
	value, _ := ctx.Value(routeVersionContextKey{}).(string)
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func canonicalizeJSON(source []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	value, err := readJSONValue(decoder)
	if err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}

	canonical, err := json.Marshal(value)
	return canonical, err == nil
}

func readJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}

	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON object key is not a string")
			}
			if _, exists := object[key]; exists {
				return nil, errors.New("JSON object contains duplicate keys")
			}
			value, err := readJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return nil, errors.New("JSON object is not closed")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := readJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return nil, errors.New("JSON array is not closed")
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}
