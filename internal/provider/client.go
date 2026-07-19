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
	"time"
)

const maxResponseBodyBytes int64 = 8 << 20

var (
	ErrInvalidRequest   = errors.New("upstream request could not be prepared")
	ErrInvalidResponse  = errors.New("upstream response is not valid JSON")
	ErrResponseTooLarge = errors.New("upstream response exceeds the size limit")
)

type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("upstream returned HTTP status %d", e.StatusCode)
}

type Client struct {
	endpoint         string
	apiKey           string
	httpClient       *http.Client
	maxResponseBytes int64
}

func NewClient(baseURL, apiKey string, timeout time.Duration) (*Client, error) {
	endpoint, err := chatCompletionsEndpoint(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("upstream API key is required")
	}
	if timeout <= 0 {
		return nil, errors.New("upstream timeout must be positive")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()

	return &Client{
		endpoint: endpoint,
		apiKey:   strings.TrimSpace(apiKey),
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxResponseBytes: maxResponseBodyBytes,
	}, nil
}

func (c *Client) Chat(ctx context.Context, requestID string, requestBody []byte) ([]byte, error) {
	return c.sendChat(ctx, requestID, requestBody)
}

func (c *Client) ChatModel(
	ctx context.Context,
	requestID string,
	requestBody []byte,
	upstreamModel string,
) ([]byte, error) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return nil, ErrInvalidRequest
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(requestBody, &payload); err != nil || payload == nil {
		return nil, ErrInvalidRequest
	}
	encodedModel, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	payload["model"] = encodedModel
	mappedBody, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrInvalidRequest
	}

	return c.sendChat(ctx, requestID, mappedBody)
}

func (c *Client) sendChat(ctx context.Context, requestID string, requestBody []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.endpoint,
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	if requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call upstream: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upstream response: %w", err)
	}
	if int64(len(responseBody)) > c.maxResponseBytes {
		return nil, ErrResponseTooLarge
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &HTTPError{StatusCode: response.StatusCode}
	}

	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || !json.Valid(responseBody) {
		return nil, ErrInvalidResponse
	}

	return responseBody, nil
}

func chatCompletionsEndpoint(rawBaseURL string) (string, error) {
	baseURL := strings.TrimSpace(rawBaseURL)
	if baseURL == "" {
		return "", errors.New("upstream base URL is required")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", errors.New("upstream base URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("upstream base URL must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("upstream base URL must include a host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("upstream base URL must not contain credentials, query, or fragment")
	}

	basePath := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(basePath, "/v1/chat/completions"):
		parsed.Path = basePath
	case strings.HasSuffix(basePath, "/v1"):
		parsed.Path = basePath + "/chat/completions"
	default:
		parsed.Path = basePath + "/v1/chat/completions"
	}
	parsed.RawPath = ""

	return parsed.String(), nil
}
