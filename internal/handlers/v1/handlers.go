package v1

import (
	"github.com/gofiber/fiber/v3"
)

type Handler struct {
	// Add dependencies here, e.g., DB *db.Database
}

func NewV1Handler() *Handler {
	return &Handler{}
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
