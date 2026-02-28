package server

import (
	v1 "fusemomo-api/internal/handlers/v1"
	"fusemomo-api/internal/middlewares"
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
	apiV1 := s.App.Group("/api/v1")

	// Dashboard routes require user authentication via Supabase JWT
	app := apiV1.Group(
		"/app",
		middlewares.SupabaseJWTMiddleware(s.DB),
		middlewares.RequireRole("user", "admin"),
	)
	dashboard := apiV1.Group(
		"/dashboard",
		middlewares.SupabaseJWTMiddleware(s.DB),
		middlewares.RequireRole("user", "admin"),
	)

	// admin routes
	admin := apiV1.Group(
		"/admin",
		middlewares.SupabaseJWTMiddleware(s.DB),
		middlewares.RequireRole("admin"),
	)

	core := apiV1.Group(
		"/core",
		middlewares.APIKeyMiddleware(s.DB),
	)

	// auth routes
	auth := apiV1.Group("/auth")
	auth.Get("/login/:provider", h.LoginWithProvider)

	apiKey := app.Group("/key")
	apiKey.Get("", h.GetAPIKeysHandler)
	apiKey.Get("/all", h.ListAllAPIKeysHandler)
	apiKey.Post("/create", h.CreateAPIkeysForAgentsHandler)
	apiKey.Delete("/:id", h.DeleteAPIkeysForAgentsHandler)
	apiKey.Post("/sync-expired", h.SyncExpiredAPIKeysHandler)
	apiKey.Delete("/:id", h.RevokeAPIKeyHandler)

	dashboard.Get("/usage", h.GetMonthlyUsageHandler)
	dashboard.Get("/usage/history", h.GetHistoricalUsageHandler)

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

}
