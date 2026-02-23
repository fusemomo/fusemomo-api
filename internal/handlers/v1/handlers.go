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
		Key:       fullKey,
		KeyPrefix: keyPrefix,
		Name:      apiKey.Name,
		ExpiresAt: apiKey.ExpiresAt,
		CreatedAt: createdAt,
	}

	return c.Status(fiber.StatusCreated).JSON(resp)
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
