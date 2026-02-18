package middlewares

import (
	"fmt"

	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// planHierarchy defines the ordered tier levels. Higher value = higher tier.
var planHierarchy = map[string]int{
	"free":       1,
	"builder":    2,
	"enterprise": 3,
}

// RequirePlan enforces a minimum plan tier for an endpoint.
func RequirePlan(minPlan string) fiber.Handler {
	minLevel, ok := planHierarchy[minPlan]
	if !ok {
		panic(fmt.Sprintf("invalid plan tier %q: must be one of free, builder, enterprise", minPlan))
	}

	return func(c fiber.Ctx) error {
		currentPlan, ok := c.Locals("plan").(string)
		if !ok || currentPlan == "" {
			return utils.Unauthorized("Missing or invalid plan context")
		}

		currentLevel, known := planHierarchy[currentPlan]
		if !known {
			return utils.InternalServerError(fmt.Sprintf("Unknown tenant plan %q", currentPlan))
		}

		if currentLevel < minLevel {
			return c.Status(402).JSON(fiber.Map{
				"error":         "plan_upgrade_required",
				"message":       fmt.Sprintf("This feature requires %s plan or higher", minPlan),
				"current_plan":  currentPlan,
				"required_plan": minPlan,
				"upgrade_url":   "https://app.fusemomo.com/upgrade",
			})
		}

		return c.Next()
	}
}
