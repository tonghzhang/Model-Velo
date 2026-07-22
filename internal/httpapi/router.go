package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"model-velo/internal/provider"
	"model-velo/internal/reliability"
	"model-velo/internal/routing"
)

type healthResponse struct {
	Status string `json:"status"`
}

func NewRouter(
	adapters *provider.AdapterRegistry,
	access AccessController,
	limiter RateLimiter,
	cache ResponseCache,
	routes *routing.Router,
	breakers *reliability.BreakerRegistry,
	queues *reliability.QueueRegistry,
	providerKeys *reliability.ProviderKeyRegistry,
	retry reliability.RetryPolicies,
) *gin.Engine {
	if adapters == nil {
		panic("httpapi: provider adapter registry is nil")
	}
	if access == nil {
		panic("httpapi: access controller is nil")
	}
	if limiter == nil {
		panic("httpapi: rate limiter is nil")
	}
	if cache == nil {
		panic("httpapi: response cache is nil")
	}
	if routes == nil {
		panic("httpapi: routing is nil")
	}
	if breakers == nil {
		panic("httpapi: circuit breaker registry is nil")
	}
	if queues == nil {
		panic("httpapi: provider queue registry is nil")
	}
	if retry == nil {
		panic("httpapi: retry policy is nil")
	}
	attempts, err := reliability.NewAttemptExecutor(adapters, breakers, queues, providerKeys, retry)
	if err != nil {
		panic("httpapi: create attempt executor: " + err.Error())
	}
	orchestrator, err := reliability.NewOrchestrator(attempts, retry)
	if err != nil {
		panic("httpapi: create fallback orchestrator: " + err.Error())
	}

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(requestIDMiddleware())
	router.GET("/healthz", health)

	protected := router.Group("/v1")
	protected.Use(authenticationMiddleware(access))
	protected.POST("/chat/completions", chatHandler{
		access:       access,
		limiter:      limiter,
		cache:        cache,
		routes:       routes,
		orchestrator: orchestrator,
	}.complete)

	return router
}

func health(c *gin.Context) {
	c.JSON(http.StatusOK, healthResponse{Status: "ok"})
}
