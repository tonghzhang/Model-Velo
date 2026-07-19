package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	requestIDHeader    = "X-Request-ID"
	requestIDGinKey    = "model-velo.request_id"
	maxRequestIDLength = 128
)

type requestIDContextKey struct{}

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader(requestIDHeader))
		if !validRequestID(requestID) {
			generated, err := generateRequestID()
			if err != nil {
				writeAPIError(
					c,
					http.StatusInternalServerError,
					"request ID could not be generated",
					"server_error",
					nil,
					"request_id_generation_failed",
				)
				return
			}
			requestID = generated
		}

		c.Set(requestIDGinKey, requestID)
		c.Request = c.Request.WithContext(
			context.WithValue(c.Request.Context(), requestIDContextKey{}, requestID),
		)
		c.Header(requestIDHeader, requestID)
		c.Next()
	}
}

func generateRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(value[:]), nil
}

func validRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDLength {
		return false
	}

	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}

	return true
}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}
