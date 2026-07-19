package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"model-velo/internal/apikey"
	"model-velo/internal/ratelimit"
)

const identityGinKey = "model-velo.identity"

type identityContextKey struct{}

type AccessController interface {
	Authenticate(ctx context.Context, plaintext string) (apikey.Identity, error)
	AuthorizeModel(ctx context.Context, tenantID, model string) error
}

type RateLimiter interface {
	Allow(ctx context.Context, tenantID, model string) (ratelimit.Decision, error)
}

func authenticationMiddleware(access AccessController) gin.HandlerFunc {
	return func(c *gin.Context) {
		plaintext, err := bearerToken(c.Request)
		if err != nil {
			c.Header("WWW-Authenticate", "Bearer")
			writeAPIError(
				c,
				http.StatusUnauthorized,
				err.message,
				"authentication_error",
				nil,
				err.code,
			)
			return
		}

		identity, authenticateErr := access.Authenticate(c.Request.Context(), plaintext)
		if authenticateErr != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			if errors.Is(authenticateErr, apikey.ErrInvalidCredential) ||
				errors.Is(authenticateErr, apikey.ErrKeyInactive) ||
				errors.Is(authenticateErr, apikey.ErrKeyRevoked) ||
				errors.Is(authenticateErr, apikey.ErrKeyExpired) ||
				errors.Is(authenticateErr, apikey.ErrTenantInactive) {
				c.Header("WWW-Authenticate", "Bearer")
				writeAPIError(
					c,
					http.StatusUnauthorized,
					"API key is invalid or inactive",
					"authentication_error",
					nil,
					"invalid_api_key",
				)
				return
			}

			writeAPIError(
				c,
				http.StatusServiceUnavailable,
				"authentication service is unavailable",
				"server_error",
				nil,
				"authentication_unavailable",
			)
			return
		}

		c.Set(identityGinKey, identity)
		c.Request = c.Request.WithContext(
			context.WithValue(c.Request.Context(), identityContextKey{}, identity),
		)
		c.Next()
	}
}

type bearerTokenError struct {
	message string
	code    string
}

func bearerToken(request *http.Request) (string, *bearerTokenError) {
	values := request.Header.Values("Authorization")
	if len(values) == 0 {
		return "", &bearerTokenError{
			message: "Authorization header with Bearer API key is required",
			code:    "missing_api_key",
		}
	}
	if len(values) != 1 {
		return "", &bearerTokenError{
			message: "Authorization header is invalid",
			code:    "invalid_authorization",
		}
	}

	value := values[0]
	if value != strings.TrimSpace(value) {
		return "", &bearerTokenError{
			message: "Authorization header is invalid",
			code:    "invalid_authorization",
		}
	}

	scheme, plaintext, found := strings.Cut(value, " ")
	if !found ||
		!strings.EqualFold(scheme, "Bearer") ||
		plaintext == "" ||
		strings.ContainsAny(plaintext, " \t\r\n") {
		return "", &bearerTokenError{
			message: "Authorization header must use Bearer authentication",
			code:    "invalid_authorization",
		}
	}

	return plaintext, nil
}

func identityFromContext(ctx context.Context) (apikey.Identity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(apikey.Identity)
	return identity, ok
}
