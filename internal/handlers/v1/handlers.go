package v1

import (
	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	DB *pgxpool.Pool
}

func NewV1Handler(db *pgxpool.Pool) *Handler {
	return &Handler{
		DB: db,
	}
}

// DbHealthHandler godoc
// @Summary      Check database health
// @Description  Check if the database connection is alive.
// @Tags         Health
// @Produce      json
// @Success      200  {object}  map[string]string
// @Failure      503  {object}  utils.APIError
// @Router       /health/db [get]
func (h *Handler) DbHealthHandler(c fiber.Ctx) error {
	if h.DB == nil {
		return utils.NewAPIError(fiber.StatusServiceUnavailable, "Database is unreachable", "Database pool is not initialized")
	}
	err := h.DB.Ping(c.Context())
	if err != nil {
		return utils.NewAPIError(fiber.StatusServiceUnavailable, "Database is unreachable", err.Error())
	}

	return c.JSON(fiber.Map{
		"status":  "up",
		"message": "Database connection is healthy",
	})
}

// PingPongHandler godoc
// @Summary      Ping the server
// @Description  Returns a simple pong message to verify the server is running.
// @Tags         Health
// @Produce      json
// @Success      200  {object}  map[string]string
// @Router       /ping [get]
func (h *Handler) PingPongHandler(c fiber.Ctx) error {
	resp := fiber.Map{
		"message": "pong",
	}
	return c.JSON(resp)
}
