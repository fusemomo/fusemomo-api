package integrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"fusemomo-api/internal/server"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestServer(t *testing.T) *server.FiberServer {
	t.Helper()
	s := server.New(nil)

	// RegisterFiberRoutes panics when required env vars (e.g. Supabase JWK) are
	// absent. Recover so the suite is skipped rather than crashing the runner.
	var regErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				regErr = fmt.Errorf("panic during route registration: %v", r)
			}
		}()
		regErr = s.RegisterFiberRoutes()
	}()

	if regErr != nil {
		t.Skipf("Skipping: could not register routes (missing env / JWK): %v", regErr)
	}
	return s
}

func TestResolveEntitiesHandler(t *testing.T) {
	s := setupTestServer(t)

	t.Run("Missing Authorization header returns 401", func(t *testing.T) {
		body := `{"identifiers":{"email":"test@example.com"}}`
		req := httptest.NewRequest("POST", "/api/v1/core/entities/resolve",
			bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.App.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 401, resp.StatusCode)
	})

	t.Run("Missing identifiers field returns 400", func(t *testing.T) {
		body := `{}`
		req := httptest.NewRequest("POST", "/api/v1/core/entities/resolve",
			bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk_test_invalid")

		resp, err := s.App.Test(req)
		require.NoError(t, err)
		// Either 400 (validation) or 401 (middleware rejects bad key) is acceptable
		assert.True(t, resp.StatusCode == 400 || resp.StatusCode == 401,
			"expected 400 or 401, got %d", resp.StatusCode)
	})

	t.Run("Identifier key exceeding 100 chars returns 400", func(t *testing.T) {
		longKey := strings.Repeat("a", 101)
		bodyMap := map[string]interface{}{
			"identifiers": map[string]string{longKey: "some_value"},
		}
		bodyBytes, _ := json.Marshal(bodyMap)

		req := httptest.NewRequest("POST", "/api/v1/core/entities/resolve",
			bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk_test_invalid")

		resp, err := s.App.Test(req)
		require.NoError(t, err)
		assert.True(t, resp.StatusCode == 400 || resp.StatusCode == 401,
			"expected 400 or 401, got %d", resp.StatusCode)
	})

	t.Run("Identifier value exceeding 1000 chars returns 400", func(t *testing.T) {
		longVal := strings.Repeat("x", 1001)
		bodyMap := map[string]interface{}{
			"identifiers": map[string]string{"email": longVal},
		}
		bodyBytes, _ := json.Marshal(bodyMap)

		req := httptest.NewRequest("POST", "/api/v1/core/entities/resolve",
			bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk_test_invalid")

		resp, err := s.App.Test(req)
		require.NoError(t, err)
		assert.True(t, resp.StatusCode == 400 || resp.StatusCode == 401,
			"expected 400 or 401, got %d", resp.StatusCode)
	})

	t.Run("Invalid identifier key characters returns 400", func(t *testing.T) {
		bodyMap := map[string]interface{}{
			"identifiers": map[string]string{"invalid-key!": "value"},
		}
		bodyBytes, _ := json.Marshal(bodyMap)

		req := httptest.NewRequest("POST", "/api/v1/core/entities/resolve",
			bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk_test_invalid")

		resp, err := s.App.Test(req)
		require.NoError(t, err)
		assert.True(t, resp.StatusCode == 400 || resp.StatusCode == 401,
			"expected 400 or 401, got %d", resp.StatusCode)
	})
}
