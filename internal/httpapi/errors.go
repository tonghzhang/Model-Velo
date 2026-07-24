package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const apiErrorCodeKey = "model-velo.api-error-code"

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

func writeAPIError(c *gin.Context, status int, message, errorType string, param *string, code string) {
	c.Set(apiErrorCodeKey, code)
	if protocolKind(c) == protocolAnthropic {
		c.AbortWithStatusJSON(status, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    anthropicErrorType(status),
				"message": message,
			},
			"request_id": requestIDFromContext(c.Request.Context()),
		})
		return
	}
	c.AbortWithStatusJSON(status, errorEnvelope{
		Error: apiError{
			Message: message,
			Type:    errorType,
			Param:   param,
			Code:    code,
		},
	})
}

func anthropicErrorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func apiErrorCode(c *gin.Context) string {
	value, ok := c.Get(apiErrorCodeKey)
	if !ok {
		return ""
	}
	code, _ := value.(string)
	return code
}

func stringPointer(value string) *string {
	return &value
}
