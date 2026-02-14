package server

import (
	v1 "fusemomo-api/internal/handlers/v1"
	"fusemomo-api/internal/middlewares"
)

func (s *FiberServer) RegisterFiberRoutes() {
	h := v1.NewV1Handler()

	s.App.Use(middlewares.CORS())
	s.App.Use(middlewares.RequestIDMiddleware())
	s.App.Use(middlewares.LoggingMiddleware())

	s.App.Get("/ping", h.PingPongHandler)
	// s.App.Get("/health", h.DBHealthCheckHandler)

	s.registerAPIv1Routes()

}

func (s *FiberServer) registerAPIv1Routes() {
	// apiV1 := s.Group("/api/v1")

}
