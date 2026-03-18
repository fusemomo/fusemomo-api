package server

import (
	"time"

	v1 "fusemomo-api/internal/handlers/v1"
	"fusemomo-api/internal/middlewares"

	"github.com/gofiber/fiber/v3/middleware/limiter"
)

// RegisterFiberRoutes registers all application routes and global middleware.
// Returns an error if any middleware cannot be initialised (e.g. invalid JWK).
func (s *FiberServer) RegisterFiberRoutes() error {

	h := v1.NewV1Handler(s.DB)

	s.App.Use(middlewares.CORS())
	s.App.Use(middlewares.RequestIDMiddleware())
	s.App.Use(middlewares.LoggingMiddleware())
	// s.App.Use(middlewares.SupabaseJWTMiddleware(s.DB))

	s.App.Get("/ping", h.PingPongHandler)
	s.App.Get("/health", h.DbHealthHandler)

	s.registerAPIv1Routes(h)

	return nil
}

func (s *FiberServer) registerAPIv1Routes(h *v1.Handler) {
	apiV1 := s.App.Group("/v1")

	rl := s.RateLimiter // convenience alias

	// Dashboard routes require user authentication via Supabase JWT
	app := apiV1.Group(
		"/app",
		middlewares.SupabaseJWTMiddleware(s.DB),
		middlewares.RequireRole("user", "admin"),
		rl.Middleware(),
	)

	// admin routes (AdminBypass in config allows these through without counting)
	admin := apiV1.Group(
		"/admin",
		middlewares.SupabaseJWTMiddleware(s.DB),
		middlewares.RequireRole("admin"),
	)

	core := apiV1.Group(
		"/core",
		middlewares.APIKeyMiddleware(s.DB),
		rl.Middleware(),
	)

	// auth routes
	auth := apiV1.Group("/auth", limiter.New(limiter.Config{
		Max:        5,
		Expiration: 1 * time.Minute,
	}))
	auth.Get("/login/:provider", h.LoginWithProvider)

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
	app.Get("/recommendations", h.GetRecommendationsHandler)
	app.Get("/recommendations/stats", h.GetRecommendationStatsHandler)

	app.Get("/analytics/summary", h.GetAnalyticsSummaryHandler)
	app.Get("/analytics/success-rate-timeseries", h.GetSuccessRateTimeSeriesHandler)
	app.Get("/analytics/performance-by-action-type", h.GetPerformanceByActionTypeHandler)
	app.Get("/analytics/activity-heatmap", h.GetActivityHeatmapHandler)
	app.Get("/analytics/api-distribution", h.GetApiDistributionHandler)
	app.Get("/analytics/top-entities", h.GetTopEntitiesHandler)
	app.Get("/analytics/recommendation-impact", h.GetRecommendationImpactHandler)
	app.Get("/graph", h.GetEntityGraphHandler)

	admin.Get("/tenants", h.GetAdminTenantsHandler)
	admin.Patch("/tenants/:id/plan", h.UpdateAdminTenantPlanHandler)
	admin.Get("/usage/global", h.GetGlobalTenantUsagesHandler)
	admin.Delete("/tenants/:id", h.DeleteTenantHandler)

	core.Post("/entities/resolve", h.ResolveEntitiesHandler)
	core.Post("/entities/:id/link", h.LinkEntityManuallyHandler)
	core.Get("/entities", h.GetAllEntitiesHandler)
	core.Get("/entities/:id", h.GetEntityHandler)
	core.Delete("/entities/:id", h.DeleteEntityHandler)
	core.Post("/interactions/log", h.LogInteractionHandler)
	core.Post("/interactions/batch", h.LogBatchInteractionsHandler)
	core.Post("/recommends", h.RecommendsActionsHandler)
	core.Patch("/recommends/:id/outcomes", h.RecommendsActionsOutcomesHandler)

}
