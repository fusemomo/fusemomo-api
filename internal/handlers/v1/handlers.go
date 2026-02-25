package v1

import (
	"fmt"
	"time"

	"fusemomo-api/internal/config"
	"fusemomo-api/internal/models"
	"fusemomo-api/internal/models/payload"
	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
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

// CreateAPIkeysForAgentsHandler godoc
// @Summary Create API keys
// @Description Create API keys for AI agent to access Fusemomo
// @Tags API_key
// @Accept  json
// @Produce json
// @Param   req body payload.CreateAPIKeyRequest true "API Key Request"
// @Success      201  {object}  payload.CreateAPIKeyResponse
// @Failure      400  {object}  utils.APIError
// @Failure      401  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /app/key/create [post]
func (h *Handler) CreateAPIkeysForAgentsHandler(c fiber.Ctx) error {
	var req payload.CreateAPIKeyRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request payload", err.Error())
	}

	if err := utils.Validator.Struct(&req); err != nil {
		return utils.BadRequest("Validation failed", err.Error())
	}

	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	rawKey, err := utils.GenerateSecureRandomString(32)
	if err != nil {
		return utils.InternalServerError("Failed to generate secure API key")
	}

	keyPrefix := "fm_live_"
	fullKey := keyPrefix + rawKey

	keyHash := utils.HashSHA256(fullKey)

	var expiresAt *time.Time
	switch req.ExpiresIn {
	case "30":
		t := time.Now().AddDate(0, 0, 30)
		expiresAt = &t
	case "90":
		t := time.Now().AddDate(0, 0, 90)
		expiresAt = &t
	case "forever":
		expiresAt = nil
	default:
		return utils.BadRequest("Invalid expiration period", "Supported values: '30', '90', 'forever'")
	}

	apiKey := models.APIKey{
		TenantID:  tenantID,
		Name:      req.Name,
		KeyPrefix: keyPrefix,
		KeyHash:   keyHash,
		Status:    models.APIKeyStatusActive,
		ExpiresAt: expiresAt,
	}

	query := `
		INSERT INTO api_keys (tenant_id, name, key_prefix, key_hash, status, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at
	`

	var id uuid.UUID
	var createdAt time.Time

	err = h.DB.QueryRow(c.Context(), query, apiKey.TenantID, apiKey.Name, apiKey.KeyPrefix, apiKey.KeyHash, apiKey.Status, apiKey.ExpiresAt).Scan(&id, &createdAt)
	if err != nil {
		return utils.InternalServerError("Failed to save API key", err.Error())
	}

	resp := payload.CreateAPIKeyResponse{
		ID:        id.String(),
		Key:       fullKey,
		KeyPrefix: keyPrefix,
		Name:      apiKey.Name,
		ExpiresAt: apiKey.ExpiresAt,
		CreatedAt: createdAt,
	}

	return c.Status(fiber.StatusCreated).JSON(resp)
}

// GetAPIKeysHandler godoc
// @Summary List API keys
// @Description Fetch all active API keys for the authenticated tenant
// @Tags API_key
// @Produce json
// @Success      200  {array}   payload.APIKeyInfo
// @Failure      401  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /app/key [get]
func (h *Handler) GetAPIKeysHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	query := `
		SELECT id, name, key_prefix, status, last_used_at, expires_at, created_at 
		FROM api_keys 
		WHERE tenant_id = $1 AND status = 'active'
		ORDER BY created_at DESC
	`

	rows, err := h.DB.Query(c.Context(), query, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to fetch API keys", err.Error())
	}
	defer rows.Close()

	var apiKeys []payload.APIKeyInfo
	for rows.Next() {
		var k payload.APIKeyInfo
		err := rows.Scan(&k.ID, &k.Name, &k.KeyPrefix, &k.Status, &k.LastUsedAt, &k.ExpiresAt, &k.CreatedAt)
		if err != nil {
			return utils.InternalServerError("Failed to scan API key", err.Error())
		}
		apiKeys = append(apiKeys, k)
	}

	if apiKeys == nil {
		apiKeys = []payload.APIKeyInfo{}
	}

	return c.JSON(apiKeys)
}

// ListAllAPIKeysHandler godoc
// @Summary List all API keys
// @Description Fetch all API keys (active, expired, revoked) for the authenticated tenant
// @Tags API_key
// @Produce json
// @Success      200  {array}   payload.APIKeyInfo
// @Failure      401  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /app/key/all [get]
func (h *Handler) ListAllAPIKeysHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	query := `
		SELECT id, name, key_prefix, status, last_used_at, expires_at, created_at 
		FROM api_keys 
		WHERE tenant_id = $1
		ORDER BY created_at DESC
	`

	rows, err := h.DB.Query(c.Context(), query, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to fetch API keys", err.Error())
	}
	defer rows.Close()

	var apiKeys []payload.APIKeyInfo
	for rows.Next() {
		var k payload.APIKeyInfo
		err := rows.Scan(&k.ID, &k.Name, &k.KeyPrefix, &k.Status, &k.LastUsedAt, &k.ExpiresAt, &k.CreatedAt)
		if err != nil {
			return utils.InternalServerError("Failed to scan API key", err.Error())
		}
		apiKeys = append(apiKeys, k)
	}

	if apiKeys == nil {
		apiKeys = []payload.APIKeyInfo{}
	}

	return c.JSON(apiKeys)
}

// DeleteAPIkeysForAgentsHandler godoc
// @Summary Delete API keys
// @Description Delete an existing API key for an AI agent
// @Tags API_key
// @Produce json
// @Param   id path string true "API Key ID"
// @Success      204  "No Content"
// @Failure      400  {object}  utils.APIError
// @Failure      401  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /app/key/{id} [delete]
func (h *Handler) DeleteAPIkeysForAgentsHandler(c fiber.Ctx) error {
	keyID := c.Params("id")
	if keyID == "" {
		return utils.BadRequest("API Key ID is required", "")
	}

	// Validate UUID format
	if _, err := uuid.Parse(keyID); err != nil {
		return utils.BadRequest("Invalid API Key ID format", err.Error())
	}

	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	query := `DELETE FROM api_keys WHERE id = $1 AND tenant_id = $2`
	result, err := h.DB.Exec(c.Context(), query, keyID, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to delete API key", err.Error())
	}

	if result.RowsAffected() == 0 {
		return utils.NotFound("API key not found or does not belong to your tenant")
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// RevokeAPIKeyHandler godoc
// @Summary Revoke an API key
// @Description Update the status of an API key to 'revoked'
// @Tags API_key
// @Produce json
// @Param   id path string true "API Key ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  utils.APIError
// @Failure      401  {object}  utils.APIError
// @Failure      404  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /dashboard/api-keys/{id} [delete]
func (h *Handler) RevokeAPIKeyHandler(c fiber.Ctx) error {
	keyID := c.Params("id")
	if keyID == "" {
		return utils.BadRequest("API Key ID is required", "")
	}

	// Validate UUID format
	if _, err := uuid.Parse(keyID); err != nil {
		return utils.BadRequest("Invalid API Key ID format", err.Error())
	}

	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	query := `
		UPDATE api_keys 
		SET status = 'revoked', updated_at = NOW() 
		WHERE id = $1 AND tenant_id = $2 AND status = 'active'
	`

	result, err := h.DB.Exec(c.Context(), query, keyID, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to revoke API key", err.Error())
	}

	if result.RowsAffected() == 0 {
		return utils.NotFound("Active API key not found or does not belong to your tenant")
	}

	return c.JSON(fiber.Map{
		"revoked":    true,
		"api_key_id": keyID,
	})
}

// SyncExpiredAPIKeysHandler godoc
// @Summary Sync expired API keys
// @Description Update the status of all expired API keys for the authenticated tenant
// @Tags API_key
// @Produce json
// @Success      200  {object}  map[string]interface{}
// @Failure      401  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /app/key/sync-expired [post]
func (h *Handler) SyncExpiredAPIKeysHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	query := `
		UPDATE api_keys 
		SET status = 'expired', updated_at = NOW() 
		WHERE tenant_id = $1 AND status = 'active' AND expires_at < NOW()
	`

	result, err := h.DB.Exec(c.Context(), query, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to sync expired API keys", err.Error())
	}

	return c.JSON(fiber.Map{
		"message":       "Expired API keys synchronized successfully",
		"updated_count": result.RowsAffected(),
	})
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

// LoginWithProvider redirects the user to Supabase's OAuth authorize endpoint
// @Summary      Social Login
// @Description  Redirects to Supabase OAuth for Google or GitHub
// @Tags         Auth
// @Param        provider path string true "OAuth Provider (google or github)"
// @Param        redirect_to query string false "Redirect URL after login"
// @Success      307 "Redirects to Supabase"
// @Failure      400 {object} utils.APIError
// @Router       /auth/login/{provider} [get]
func (h *Handler) LoginWithProvider(c fiber.Ctx) error {
	provider := c.Params("provider")
	if provider != "google" && provider != "github" {
		return utils.BadRequest("Invalid provider. Supported: google, github")
	}

	redirectTo := c.Query("redirect_to")
	supabaseURL := config.Envs.SUPABASE_URL
	if supabaseURL == "" {
		return utils.InternalServerError("Supabase URL not configured")
	}

	authorizeURL := fmt.Sprintf("%s/auth/v1/authorize?provider=%s", supabaseURL, provider)
	if redirectTo != "" {
		authorizeURL += "&redirect_to=" + redirectTo
	}

	return c.Redirect().Status(fiber.StatusTemporaryRedirect).To(authorizeURL)
}

// GetMonthlyUsageHandler godoc
// @Summary Get current month's usage stats
// @Description Fetch current month's usage logs and tenant limits
// @Tags Dashboard
// @Produce json
// @Success      200  {object}  payload.UsageResponse
// @Failure      401  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /dashboard/usage [get]
func (h *Handler) GetMonthlyUsageHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	now := time.Now().UTC()
	currentYear := now.Year()
	currentMonth := int(now.Month())

	query := `
		SELECT 
			u.resolution_count, 
			t.monthly_resolution_limit, 
			u.interaction_count, 
			t.monthly_interaction_limit, 
			u.recommendation_count, 
			t.plan
		FROM tenants t
		LEFT JOIN usage_logs u 
			ON t.id = u.tenant_id 
			AND u.period_year = $1 
			AND u.period_month = $2
		WHERE t.id = $3
	`

	var resp payload.UsageResponse
	resp.Period = fmt.Sprintf("%04d-%02d", currentYear, currentMonth)

	var (
		resCount *int
		resLimit int
		intCount *int
		intLimit int
		recCount *int
		plan     string
	)

	err = h.DB.QueryRow(c.Context(), query, currentYear, currentMonth, tenantID).Scan(
		&resCount, &resLimit, &intCount, &intLimit, &recCount, &plan,
	)

	if err != nil {
		if err.Error() == "no rows in result set" {
			return utils.Unauthorized("Tenant not found")
		}
		return utils.InternalServerError("Failed to fetch usage stats", err.Error())
	}

	if resCount != nil {
		resp.ResolutionCount = *resCount
	}
	if intCount != nil {
		resp.InteractionCount = *intCount
	}
	if recCount != nil {
		resp.RecommendationCount = *recCount
	}

	resp.ResolutionLimit = resLimit
	resp.InteractionLimit = intLimit
	resp.Plan = plan

	if resp.ResolutionLimit > 0 {
		usageRatio := float64(resp.ResolutionCount) / float64(resp.ResolutionLimit)
		resp.PercentageUsed = float64(int(usageRatio*1000)) / 10.0
	} else {
		resp.PercentageUsed = 0.0
	}

	return c.JSON(resp)
}
