package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"model-velo/internal/apikey"
	"model-velo/internal/gateway"
	"model-velo/internal/routing"
)

type modelHandler struct {
	runtime gateway.Source
	access  AccessController
}

type authorizedModelLister interface {
	AuthorizedModels(context.Context, string) ([]string, error)
}

func (handler modelHandler) list(c *gin.Context) {
	active := handler.runtime.Current()
	if active == nil {
		writeAPIError(
			c, http.StatusServiceUnavailable, "gateway runtime is unavailable",
			"server_error", nil, "runtime_unavailable",
		)
		return
	}
	models := active.Routes.Models()
	identity, ok := identityFromContext(c.Request.Context())
	if !ok {
		writeIdentityUnavailable(c)
		return
	}
	allowed, err := handler.allowedModels(c.Request.Context(), identity.TenantID, models)
	if err != nil {
		writeAPIError(
			c, http.StatusServiceUnavailable, "model catalog authorization is unavailable",
			"server_error", nil, "model_catalog_unavailable",
		)
		return
	}
	items := make([]gin.H, 0, len(models))
	for _, model := range allowed {
		items = append(items, modelObject(model))
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": items})
}

func (handler modelHandler) get(c *gin.Context) {
	model := strings.TrimSpace(c.Param("model"))
	identity, ok := identityFromContext(c.Request.Context())
	if !ok {
		writeIdentityUnavailable(c)
		return
	}
	if err := handler.access.AuthorizeModel(
		c.Request.Context(), identity.TenantID, model,
	); err != nil {
		if errors.Is(err, apikey.ErrModelNotAllowed) {
			writeAPIError(
				c, http.StatusNotFound, "model was not found",
				"invalid_request_error", stringPointer("model"), "model_not_found",
			)
			return
		}
		writeAPIError(
			c, http.StatusServiceUnavailable, "model catalog authorization is unavailable",
			"server_error", nil, "model_catalog_unavailable",
		)
		return
	}
	active := handler.runtime.Current()
	if active == nil {
		writeAPIError(
			c, http.StatusServiceUnavailable, "gateway runtime is unavailable",
			"server_error", nil, "runtime_unavailable",
		)
		return
	}
	if _, err := active.Routes.Plan(model, nil); err != nil {
		if errors.Is(err, routing.ErrNoRoute) {
			writeAPIError(
				c, http.StatusNotFound, "model was not found",
				"invalid_request_error", stringPointer("model"), "model_not_found",
			)
			return
		}
		writeAPIError(
			c, http.StatusServiceUnavailable, "model catalog is unavailable",
			"server_error", nil, "model_catalog_unavailable",
		)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, modelObject(model))
}

func (handler modelHandler) allowedModels(
	ctx context.Context,
	tenantID string,
	configured []string,
) ([]string, error) {
	if lister, ok := handler.access.(authorizedModelLister); ok {
		granted, err := lister.AuthorizedModels(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		allowed := make(map[string]struct{}, len(granted))
		for _, model := range granted {
			allowed[model] = struct{}{}
		}
		if _, all := allowed["*"]; all {
			return append([]string(nil), configured...), nil
		}
		result := make([]string, 0, len(configured))
		for _, model := range configured {
			if _, ok := allowed[model]; ok {
				result = append(result, model)
			}
		}
		return result, nil
	}
	result := make([]string, 0, len(configured))
	for _, model := range configured {
		if err := handler.access.AuthorizeModel(ctx, tenantID, model); err == nil {
			result = append(result, model)
		} else if !errors.Is(err, apikey.ErrModelNotAllowed) {
			return nil, err
		}
	}
	return result, nil
}

func modelObject(model string) gin.H {
	return gin.H{
		"id": model, "object": "model",
		"created": 0, "owned_by": "model-velo",
	}
}
