package server

import (
	"time"

	v1 "fusemomo-api/internal/handlers/v1"
	"fusemomo-api/internal/handlers/v1/recommendation"
	"fusemomo-api/internal/middlewares"

	"github.com/gofiber/fiber/v3/middleware/limiter"
)

// RegisterFiberRoutes registers all application routes and global middleware.
func (s *FiberServer) RegisterFiberRoutes() error {

	h := v1.NewV1Handler(s.DB)
	v1.SessionStore = s.SessionStore

	s.App.Use(middlewares.CORS())
	s.App.Use(middlewares.RequestIDMiddleware())
	s.App.Use(middlewares.LoggingMiddleware())

	s.App.Get("/ping", h.PingPongHandler)
	s.App.Get("/health", h.DbHealthHandler)

	s.registerAPIv1Routes(h)

	return nil
}

func (s *FiberServer) registerAPIv1Routes(h *v1.Handler) {
	apiV1 := s.App.Group("/v1")

	rh := recommendation.NewHandler(s.DB)
	rl := s.RateLimiter

	//  Auth routes
	authLimiter := limiter.New(limiter.Config{
		Max:        30,
		Expiration: 1 * time.Minute,
	})
	auth := apiV1.Group("/auth", authLimiter)
	auth.Get("/login/:provider", h.LoginWithProvider)
	auth.Post("/session", h.CreateSessionHandler)
	auth.Delete("/session", h.DeleteSessionHandler)

	//  fusemomo app
	app := apiV1.Group(
		"/app",
		middlewares.SessionCookieMiddleware(s.SessionStore),
		middlewares.RequireRole("user", "admin"),
		rl.Middleware(),
	)

	//  Admin routes
	admin := apiV1.Group(
		"/admin",
		middlewares.SessionCookieMiddleware(s.SessionStore),
		middlewares.RequireRole("admin"),
	)

	core := apiV1.Group(
		"/core",
		middlewares.APIKeyMiddleware(s.DB),
		rl.Middleware(),
	)

	// App routes
	apiKey := app.Group("/key")
	apiKey.Get("", h.GetAPIKeysHandler)
	apiKey.Get("/all", h.ListAllAPIKeysHandler)
	apiKey.Post("/create", h.CreateAPIkeysForAgentsHandler)
	apiKey.Delete("/:id", h.DeleteAPIkeysForAgentsHandler)
	apiKey.Post("/sync-expired", h.SyncExpiredAPIKeysHandler)
	apiKey.Post("/:id/revoke", h.RevokeAPIKeyHandler)

	app.Delete("", h.DeleteTenantProfileHandler)
	app.Get("/profile", h.GetProfileHandler)
	app.Patch("/profile", h.UpdateTenantProfileHandler)
	app.Get("/usage", h.GetMonthlyUsageHandler)
	app.Get("/usage/history", h.GetHistoricalUsageHandler)

	app.Get("/analytics/summary", h.GetAnalyticsSummaryHandler)
	app.Get("/analytics/success-rate-timeseries", h.GetSuccessRateTimeSeriesHandler)
	app.Get("/analytics/performance-by-action-type", h.GetPerformanceByActionTypeHandler)
	app.Get("/analytics/activity-heatmap", h.GetActivityHeatmapHandler)
	app.Get("/analytics/api-distribution", h.GetApiDistributionHandler)
	app.Get("/analytics/top-entities", h.GetTopEntitiesHandler)
	app.Get("/analytics/recommendation-impact", h.GetRecommendationImpactHandler)
	app.Get("/graph", h.GetEntityGraphHandler)

	// Entity routes for the dashboard
	app.Get("/entities", h.GetAllEntitiesHandler)
	app.Get("/entities/:id", h.GetEntityHandler)
	app.Delete("/entities/:id", h.DeleteEntityHandler)
	app.Post("/entities/resolve", h.ResolveEntitiesHandler)

	// Admin routes
	admin.Get("/tenants", h.GetAdminTenantsHandler)
	admin.Patch("/tenants/:id/plan", h.UpdateAdminTenantPlanHandler)
	admin.Get("/usage/global", h.GetGlobalTenantUsagesHandler)
	admin.Delete("/tenants/:id", h.DeleteTenantHandler)

	// Core (agent-facing, API key auth)
	core.Post("/entities/resolve", h.ResolveEntitiesHandler)
	core.Post("/entities/:id/link", h.LinkEntityManuallyHandler)
	core.Get("/entities", h.GetAllEntitiesHandler)
	core.Get("/entities/:id", h.GetEntityHandler)
	core.Delete("/entities/:id", h.DeleteEntityHandler)
	core.Post("/interactions/log", h.LogInteractionHandler)
	core.Post("/interactions/batch", h.LogBatchInteractionsHandler)
	core.Post("/entities/:entity_id/recommend", rh.RecommendHandler)
	core.Patch("/recommends/:id/feedback", rh.FeedbackHandler)
}
