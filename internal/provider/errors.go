package provider

import (
	"errors"
	"fmt"
)

var (
	// 这些哨兵错误由 reliability 包转换为稳定的 Retry、Fallback 和 HTTP 决策。
	ErrUnknownProvider       = errors.New("provider adapter is not configured")
	ErrInvalidRequest        = errors.New("upstream request could not be prepared")
	ErrUnsupportedCapability = errors.New("provider does not support a requested capability")
	ErrUnsupportedResponse   = errors.New("provider returned a capability the gateway cannot represent")
	ErrInvalidResponse       = errors.New("upstream response is not valid JSON")
	ErrInvalidStream         = errors.New("upstream response is not a valid event stream")
	ErrResponseTooLarge      = errors.New("upstream response exceeds the size limit")
)

// HTTPError 只保留可靠性策略需要的上游错误元数据，不携带可能含敏感信息的原始响应体。
type HTTPError struct {
	StatusCode int
	RetryAfter string
	Code       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("upstream returned HTTP status %d", e.StatusCode)
}
