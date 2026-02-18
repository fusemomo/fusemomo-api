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

	// s.registerAPIv1Routes(h)

	return nil
}
