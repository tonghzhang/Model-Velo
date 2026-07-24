package httpapi

import "github.com/gin-gonic/gin"

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
	c.AbortWithStatusJSON(status, errorEnvelope{
		Error: apiError{
			Message: message,
			Type:    errorType,
			Param:   param,
			Code:    code,
		},
	})
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
