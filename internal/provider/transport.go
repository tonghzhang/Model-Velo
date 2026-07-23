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
	"strings"
)

const (
	maxResponseBodyBytes   int64 = 8 << 20
	maximumHTTPConnections       = 10_000
)

// HTTPConfig 控制每个 Provider 独立 HTTP Client 的连接池上限。
type HTTPConfig struct {
	MaxIdleConnections        int
	MaxIdleConnectionsPerHost int
	MaxConnectionsPerHost     int
}

// DefaultHTTPConfig 提供适合有界 Provider Queue 的保守连接池默认值。
func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		MaxIdleConnections:        100,
		MaxIdleConnectionsPerHost: 20,
		MaxConnectionsPerHost:     20,
	}
}

// Validate 保证空闲连接配置不会超过总连接上限。
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

// jsonTransport 只负责一次 JSON HTTP 往返，不在这里实现 Retry 或 Fallback。
type jsonTransport struct {
	client           *http.Client
	maxResponseBytes int64
}

// newJSONTransport 克隆标准 Transport，避免不同 Provider 共享可变连接池配置。
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

// post 传播调用方 context，限制响应体大小，并把非 2xx 转成可分类的 HTTPError。
func (transport *jsonTransport) post(
	ctx context.Context,
	endpoint string,
	requestID string,
	body []byte,
	headers http.Header,
) ([]byte, error) {
	request, err := newJSONPostRequest(ctx, endpoint, requestID, body, headers)
	if err != nil {
		return nil, err
	}

	response, err := transport.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call upstream: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := transport.readBody(response.Body)
	if err != nil {
		return nil, err
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

// postStream 只校验建流响应并移交 Body 所有权；调用方必须关闭返回值。
func (transport *jsonTransport) postStream(
	ctx context.Context,
	endpoint string,
	requestID string,
	body []byte,
	headers http.Header,
) (io.ReadCloser, error) {
	request, err := newJSONPostRequest(ctx, endpoint, requestID, body, headers)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "text/event-stream")

	response, err := transport.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call upstream: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		responseBody, readErr := transport.readBody(response.Body)
		if readErr != nil {
			return nil, readErr
		}
		return nil, &HTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: response.Header.Get("Retry-After"),
			Code:       responseErrorCode(responseBody),
		}
	}

	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		response.Body.Close()
		return nil, ErrInvalidStream
	}
	return response.Body, nil
}

func newJSONPostRequest(
	ctx context.Context,
	endpoint string,
	requestID string,
	body []byte,
	headers http.Header,
) (*http.Request, error) {
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
	return request, nil
}

func (transport *jsonTransport) readBody(body io.Reader) ([]byte, error) {
	responseBody, err := io.ReadAll(io.LimitReader(body, transport.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upstream response: %w", err)
	}
	if int64(len(responseBody)) > transport.maxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	return responseBody, nil
}

// responseErrorCode 兼容常见的嵌套和顶层错误结构，只提取策略判断需要的代码。
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

// normalizeErrorCode 生成有界、稳定且不包含原始错误文本的分类值。
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
