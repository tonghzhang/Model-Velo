package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const maximumHTTPConnections = 10_000

type HTTPConfig struct {
	MaxIdleConnections        int
	MaxIdleConnectionsPerHost int
	MaxConnectionsPerHost     int
}

func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		MaxIdleConnections:        100,
		MaxIdleConnectionsPerHost: 20,
		MaxConnectionsPerHost:     20,
	}
}

func (config HTTPConfig) Validate() error {
	if config.MaxIdleConnections < 1 || config.MaxIdleConnections > maximumHTTPConnections {
		return fmt.Errorf("HTTP max idle connections must be between 1 and %d", maximumHTTPConnections)
	}
	if config.MaxIdleConnectionsPerHost < 1 || config.MaxIdleConnectionsPerHost > config.MaxIdleConnections {
		return errors.New("HTTP max idle connections per host must be positive and no greater than max idle connections")
	}
	if config.MaxConnectionsPerHost < 1 || config.MaxConnectionsPerHost > maximumHTTPConnections {
		return fmt.Errorf("HTTP max connections per host must be between 1 and %d", maximumHTTPConnections)
	}
	if config.MaxIdleConnectionsPerHost > config.MaxConnectionsPerHost {
		return errors.New("HTTP max idle connections per host must not exceed max connections per host")
	}
	return nil
}

type jsonTransport struct {
	client           *http.Client
	maxResponseBytes int64
}

func fixedEndpoint(rawBaseURL, suffix string) (string, error) {
	parsed, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	suffix = strings.Trim(suffix, "/")
	fullSuffix := "/" + suffix
	if !strings.HasSuffix(basePath, fullSuffix) {
		firstSegment, remaining, _ := strings.Cut(suffix, "/")
		if remaining != "" && strings.HasSuffix(basePath, "/"+firstSegment) {
			basePath += "/" + remaining
		} else {
			basePath += fullSuffix
		}
	}
	parsed.Path = basePath
	parsed.RawPath = ""
	return parsed.String(), nil
}

func modelEndpoint(rawBaseURL, prefix, model, action string) (string, error) {
	parsed, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return "", err
	}
	model = strings.TrimSpace(strings.TrimPrefix(model, prefix+"/"))
	if model == "" || strings.ContainsAny(model, "/?#") {
		return "", ErrInvalidRequest
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	parsed.Path = basePath + "/" + prefix + "/" + url.PathEscape(model) + action
	parsed.RawPath = ""
	return parsed.String(), nil
}

func newJSONTransport(config HTTPConfig) *jsonTransport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = config.MaxIdleConnections
	transport.MaxIdleConnsPerHost = config.MaxIdleConnectionsPerHost
	transport.MaxConnsPerHost = config.MaxConnectionsPerHost
	return &jsonTransport{
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxResponseBytes: maxResponseBodyBytes,
	}
}

func (transport *jsonTransport) post(
	ctx context.Context,
	endpoint string,
	requestID string,
	body []byte,
	headers http.Header,
) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("Content-Type", "application/json")
	if requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}

	response, err := transport.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call upstream: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, transport.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upstream response: %w", err)
	}
	if int64(len(responseBody)) > transport.maxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &HTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: response.Header.Get("Retry-After"),
			Code:       responseErrorCode(responseBody),
		}
	}

	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") || !json.Valid(responseBody) {
		return nil, ErrInvalidResponse
	}
	return responseBody, nil
}

func responseErrorCode(body []byte) string {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	if nested, ok := envelope["error"]; ok {
		var details map[string]json.RawMessage
		if json.Unmarshal(nested, &details) == nil {
			if code := firstErrorCode(details); code != "" {
				return code
			}
		}
	}
	return firstErrorCode(envelope)
}

func firstErrorCode(fields map[string]json.RawMessage) string {
	for _, name := range []string{"code", "type", "status"} {
		var value string
		if json.Unmarshal(fields[name], &value) == nil && strings.TrimSpace(value) != "" {
			return normalizeErrorCode(value)
		}
		var number json.Number
		if json.Unmarshal(fields[name], &number) == nil && number.String() != "" {
			return normalizeErrorCode(number.String())
		}
	}
	return ""
}

func normalizeErrorCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	var normalized strings.Builder
	lastWasSeparator := false
	for _, character := range code {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			normalized.WriteRune(character)
			lastWasSeparator = false
		case normalized.Len() > 0 && !lastWasSeparator:
			normalized.WriteByte('_')
			lastWasSeparator = true
		}
		if normalized.Len() >= 128 {
			break
		}
	}
	return strings.Trim(normalized.String(), "_")
}
