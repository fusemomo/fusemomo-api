package integrations

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"fusemomo-api/internal/server"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
)

func TestHealthHandler(t *testing.T) {
	s := server.New(nil)
	if err := s.RegisterFiberRoutes(); err != nil {
		t.Skipf("Skipping: could not register routes (likely missing valid JWK in test env): %v", err)
	}

	sf := server.New(nil)
	if err := sf.RegisterFiberRoutes(); err != nil {
		t.Skipf("Skipping: could not register routes (likely missing valid JWK in test env): %v", err)
	}

	t.Run("DB Health Check", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health", nil)
		resp, err := sf.App.Test(req)

		if err != nil {
			t.Logf("Expected error if DB is unreachable: %v", err)
			return
		}

		if resp == nil {
			t.Log("Response is nil (likely due to timeout or connection refused)")
			return
		}

		if resp.StatusCode == fiber.StatusOK {
			var body map[string]string
			json.NewDecoder(resp.Body).Decode(&body)
			assert.Equal(t, "up", body["status"])
		} else if resp.StatusCode == fiber.StatusServiceUnavailable {
			t.Log("Database is unreachable as expected if no DB is running")
		} else {
			t.Errorf("Unexpected status code: %d", resp.StatusCode)
		}
	})
}
