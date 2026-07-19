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

func NewRouter(client *provider.Client, access AccessController, limiter RateLimiter, cache ResponseCache) *gin.Engine {
	routes, err := routing.New(routing.SingleProviderDefinition("upstream", "single-provider-v1"))
	if err != nil {
		panic("httpapi: create default routing: " + err.Error())
	}
	breaker, err := reliability.NewBreaker("upstream", reliability.DefaultBreakerConfig())
	if err != nil {
		panic("httpapi: create default circuit breaker: " + err.Error())
	}
	return NewRouterWithBreaker(client, access, limiter, cache, routes, breaker)
}

func NewRouterWithRouting(
	client *provider.Client,
	access AccessController,
	limiter RateLimiter,
	cache ResponseCache,
	routes *routing.Router,
) *gin.Engine {
	breaker, err := reliability.NewBreaker("upstream", reliability.DefaultBreakerConfig())
	if err != nil {
		panic("httpapi: create default circuit breaker: " + err.Error())
	}
	return NewRouterWithBreaker(client, access, limiter, cache, routes, breaker)
}

func NewRouterWithBreaker(
	client *provider.Client,
	access AccessController,
	limiter RateLimiter,
	cache ResponseCache,
	routes *routing.Router,
	breaker *reliability.Breaker,
) *gin.Engine {
	if breaker == nil {
		panic("httpapi: circuit breaker is nil")
	}
	queues, err := reliability.NewQueueRegistry([]string{breaker.Snapshot().ProviderID}, reliability.DefaultQueueConfig())
	if err != nil {
		panic("httpapi: create default provider queue: " + err.Error())
	}
	return NewRouterWithReliability(client, access, limiter, cache, routes, breaker, queues)
}

func NewRouterWithReliability(
	client *provider.Client,
	access AccessController,
	limiter RateLimiter,
	cache ResponseCache,
	routes *routing.Router,
	breaker *reliability.Breaker,
	queues *reliability.QueueRegistry,
) *gin.Engine {
	if client == nil {
		panic("httpapi: provider client is nil")
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
	if breaker == nil {
		panic("httpapi: circuit breaker is nil")
	}
	if queues == nil {
		panic("httpapi: provider queue registry is nil")
	}

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(requestIDMiddleware())
	router.GET("/healthz", health)

	protected := router.Group("/v1")
	protected.Use(authenticationMiddleware(access))
	protected.POST("/chat/completions", chatHandler{
		client:  client,
		access:  access,
		limiter: limiter,
		cache:   cache,
		routes:  routes,
		breaker: breaker,
		queues:  queues,
	}.complete)

	return router
}

func health(c *gin.Context) {
	c.JSON(http.StatusOK, healthResponse{Status: "ok"})
}
