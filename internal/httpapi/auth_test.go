package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"model-velo/internal/apikey"
)

func TestBearerToken(t *testing.T) {
	tests := []struct {
		name        string
		values      []string
		wantToken   string
		wantErrCode string
	}{
		{name: "missing", wantErrCode: "missing_api_key"},
		{name: "valid", values: []string{"Bearer mvl_locator_secret"}, wantToken: "mvl_locator_secret"},
		{name: "case insensitive scheme", values: []string{"bearer mvl_locator_secret"}, wantToken: "mvl_locator_secret"},
		{name: "duplicate headers", values: []string{"Bearer first", "Bearer second"}, wantErrCode: "invalid_authorization"},
		{name: "wrong scheme", values: []string{"Basic credential"}, wantErrCode: "invalid_authorization"},
		{name: "empty token", values: []string{"Bearer "}, wantErrCode: "invalid_authorization"},
		{name: "leading whitespace", values: []string{" Bearer token"}, wantErrCode: "invalid_authorization"},
		{name: "trailing whitespace", values: []string{"Bearer token "}, wantErrCode: "invalid_authorization"},
		{name: "double separator", values: []string{"Bearer  token"}, wantErrCode: "invalid_authorization"},
		{name: "tab separator", values: []string{"Bearer\ttoken"}, wantErrCode: "invalid_authorization"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, value := range test.values {
				request.Header.Add("Authorization", value)
			}

			token, tokenErr := bearerToken(request)
			if token != test.wantToken {
				t.Errorf("bearerToken() token = %q, want %q", token, test.wantToken)
			}
			if test.wantErrCode == "" {
				if tokenErr != nil {
					t.Fatalf("bearerToken() error = %+v", tokenErr)
				}
				return
			}
			if tokenErr == nil || tokenErr.code != test.wantErrCode {
				t.Fatalf("bearerToken() error code = %v, want %q", tokenErr, test.wantErrCode)
			}
		})
	}
}

func TestAuthenticationMiddlewareStopsFailuresAndSetsIdentity(t *testing.T) {
	tests := []struct {
		name             string
		header           string
		authenticateErr  error
		wantStatus       int
		wantCode         string
		wantAuthenticate int
	}{
		{name: "missing header", wantStatus: http.StatusUnauthorized, wantCode: "missing_api_key"},
		{name: "invalid credential", header: "Bearer token", authenticateErr: apikey.ErrInvalidCredential, wantStatus: http.StatusUnauthorized, wantCode: "invalid_api_key", wantAuthenticate: 1},
		{name: "disabled key", header: "Bearer token", authenticateErr: apikey.ErrKeyInactive, wantStatus: http.StatusUnauthorized, wantCode: "invalid_api_key", wantAuthenticate: 1},
		{name: "revoked key", header: "Bearer token", authenticateErr: apikey.ErrKeyRevoked, wantStatus: http.StatusUnauthorized, wantCode: "invalid_api_key", wantAuthenticate: 1},
		{name: "expired key", header: "Bearer token", authenticateErr: apikey.ErrKeyExpired, wantStatus: http.StatusUnauthorized, wantCode: "invalid_api_key", wantAuthenticate: 1},
		{name: "disabled tenant", header: "Bearer token", authenticateErr: apikey.ErrTenantInactive, wantStatus: http.StatusUnauthorized, wantCode: "invalid_api_key", wantAuthenticate: 1},
		{name: "storage failure", header: "Bearer token", authenticateErr: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable, wantCode: "authentication_unavailable", wantAuthenticate: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			access := &middlewareAccessController{authenticateErr: test.authenticateErr}
			downstreamCalls := 0
			router := gin.New()
			router.Use(authenticationMiddleware(access))
			router.GET("/", func(c *gin.Context) {
				downstreamCalls++
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if got := authenticationErrorCode(t, response); got != test.wantCode {
				t.Errorf("error code = %q, want %q", got, test.wantCode)
			}
			if access.authenticateCalls != test.wantAuthenticate {
				t.Errorf("Authenticate() calls = %d, want %d", access.authenticateCalls, test.wantAuthenticate)
			}
			if downstreamCalls != 0 {
				t.Errorf("downstream calls = %d, want 0", downstreamCalls)
			}
		})
	}

	t.Run("success sets Go and Gin identity", func(t *testing.T) {
		wantIdentity := apikey.Identity{TenantID: "tenant-auth", APIKeyID: "key-auth", KeyPrefix: "mvl_auth"}
		access := &middlewareAccessController{identity: wantIdentity}
		router := gin.New()
		router.Use(authenticationMiddleware(access))
		router.GET("/", func(c *gin.Context) {
			contextIdentity, ok := identityFromContext(c.Request.Context())
			if !ok || contextIdentity != wantIdentity {
				t.Errorf("Go Context identity = %+v, found %t; want %+v", contextIdentity, ok, wantIdentity)
			}
			ginValue, ok := c.Get(identityGinKey)
			ginIdentity, typeOK := ginValue.(apikey.Identity)
			if !ok || !typeOK || ginIdentity != wantIdentity {
				t.Errorf("Gin Context identity = %+v, found %t; want %+v", ginValue, ok && typeOK, wantIdentity)
			}
			c.Status(http.StatusNoContent)
		})
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Authorization", "Bearer valid-token")
		response := httptest.NewRecorder()

		router.ServeHTTP(response, request)

		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
		}
		if access.authenticateCalls != 1 {
			t.Errorf("Authenticate() calls = %d, want 1", access.authenticateCalls)
		}
	})
}

type middlewareAccessController struct {
	identity          apikey.Identity
	authenticateErr   error
	authenticateCalls int
}

func (access *middlewareAccessController) Authenticate(context.Context, string) (apikey.Identity, error) {
	access.authenticateCalls++
	return access.identity, access.authenticateErr
}

func (*middlewareAccessController) AuthorizeModel(
	context.Context,
	apikey.Identity,
	string,
) error {
	return nil
}

func authenticationErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response: %v; body = %s", err, response.Body.String())
	}
	return envelope.Error.Code
}
