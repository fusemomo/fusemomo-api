package middlewares

import (
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
)

func CORS() fiber.Handler {
	return cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:5200", "http://127.0.0.1:5200", "http://localhost:8000", "http://127.0.0.1:8000"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders:     []string{"Accept", "Authorization", "Content-Type", "X-Requested-Id"},
		AllowCredentials: true,
		MaxAge:           86400,
	})
}
