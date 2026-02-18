package middlewares

import (
	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// RequireRole enforces role-based access control.
func RequireRole(allowedRoles ...string) fiber.Handler {
	return func(c fiber.Ctx) error {
		role, ok := c.Locals("role").(string)
		if !ok || role == "" {
			return utils.Unauthorized("Missing or invalid role context")
		}

		for _, allowedRole := range allowedRoles {
			if role == allowedRole {
				return c.Next()
			}
		}

		return utils.Forbidden("You do not have permission to access this resource")
	}
}
