package integrations

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
)

func TestErrorHandler(t *testing.T) {
	// Initialize server with custom error handler
	// s := server.New() // Unused
	// aggregated error handler not yet wired in server.New(), so we override it for testing
	// or we can just test the handler function directly if we want unit tests.
	// But let's verify it as a fiber config.

	// Create a new app instance with the error handler for this test
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c fiber.Ctx, err error) error {
			return utils.ErrorHandler(c, err)
		},
	})

	app.Get("/bad-request", func(c fiber.Ctx) error {
		return utils.BadRequest("Bad Request Test")
	})

	app.Get("/unauthorized", func(c fiber.Ctx) error {
		return utils.Unauthorized("Unauthorized Test")
	})

	app.Get("/forbidden", func(c fiber.Ctx) error {
		return utils.Forbidden("Forbidden Test")
	})

	app.Get("/not-found", func(c fiber.Ctx) error {
		return utils.NotFound("Not Found Test")
	})

	app.Get("/panic", func(c fiber.Ctx) error {
		panic("panic")
	})

	t.Run("Bad Request", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/bad-request", nil)
		resp, err := app.Test(req)
		assert.NoError(t, err)
		assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)

		var body map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&body)
		errBytes, _ := json.Marshal(body["error"])
		var apiErr utils.APIError
		json.Unmarshal(errBytes, &apiErr)

		assert.Equal(t, fiber.StatusBadRequest, apiErr.Code)
		assert.Equal(t, "Bad Request Test", apiErr.Message)
	})

	t.Run("Unauthorized", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/unauthorized", nil)
		resp, err := app.Test(req)
		assert.NoError(t, err)
		assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
	})
}
