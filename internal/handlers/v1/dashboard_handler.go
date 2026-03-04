package v1

import (
	"fmt"
	"fusemomo-api/internal/models/payload"
	"fusemomo-api/internal/utils"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

// GetProfileHandler godoc
// @Summary Get tenant profile
// @Description Fetch the currently authenticated tenant profile details
// @Tags Dashboard
// @Produce json
// @Success      200  {object}  payload.ProfileResponse
// @Failure      401  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /dashboard/profile [get]
func (h *Handler) GetProfileHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	return h.getCurrentProfile(c, tenantID)
}

// DeleteTenantProfileHandler godoc
// @Summary Delete tenant profile (Soft Delete)
// @Description Marks the currently authenticated tenant profile as deleted. This is irreversible via the UI.
// @Tags Dashboard
// @Produce json
// @Success      200  {object}  utils.APIResponse
// @Failure      401  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /dashboard [delete]
func (h *Handler) DeleteTenantProfileHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	query := `
		UPDATE tenants 
		SET deleted_at = NOW(), updated_at = NOW() 
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := h.DB.Exec(c.Context(), query, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to delete tenant profile", err.Error())
	}

	if result.RowsAffected() == 0 {
		return utils.NotFound("Tenant profile not found or already deleted")
	}

	return c.JSON(fiber.Map{
		"message":   "Profile successfully deleted",
		"tenant_id": tenantID,
	})
}

// UpdateTenantProfileHandler godoc
// @Summary Update tenant profile
// @Description Partially update tenant profile fields (name, avatar_url)
// @Tags Dashboard
// @Accept  json
// @Produce json
// @Param   request  body      payload.UpdateProfileRequest  true  "Update Profile Request"
// @Success      200  {object}  payload.ProfileResponse
// @Failure      400  {object}  utils.APIError
// @Failure      401  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /dashboard/profile [patch]
func (h *Handler) UpdateTenantProfileHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	var req payload.UpdateProfileRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request body", err.Error())
	}

	if err := utils.Validator.Struct(&req); err != nil {
		return utils.BadRequest("Validation failed", err.Error())
	}

	// Dynamic query building for partial updates
	query := "UPDATE tenants SET updated_at = NOW()"
	args := []interface{}{tenantID}
	argIdx := 2

	if req.Name != nil {
		query += fmt.Sprintf(", name = $%d", argIdx)
		args = append(args, *req.Name)
		argIdx++
	}

	if req.AvatarURL != nil {
		query += fmt.Sprintf(", avatar_url = $%d", argIdx)
		args = append(args, *req.AvatarURL)
		argIdx++
	}

	// If no fields provided, just return the current profile
	if argIdx == 2 {
		return h.getCurrentProfile(c, tenantID)
	}

	query += " WHERE id = $1 AND deleted_at IS NULL RETURNING id, name, email, avatar_url, plan, role, created_at, updated_at"

	var resp payload.ProfileResponse
	err = h.DB.QueryRow(c.Context(), query, args...).Scan(
		&resp.ID, &resp.Name, &resp.Email, &resp.AvatarURL, &resp.Plan, &resp.Role, &resp.CreatedAt, &resp.UpdatedAt,
	)

	if err != nil {
		if err.Error() == "no rows in result set" {
			return utils.NotFound("Tenant profile not found or already deleted")
		}
		return utils.InternalServerError("Failed to update tenant profile", err.Error())
	}

	return c.JSON(resp)
}

// Helper to get current profile
func (h *Handler) getCurrentProfile(c fiber.Ctx, tenantID uuid.UUID) error {
	// 2. Database Fetch
	var resp payload.ProfileResponse
	query := "SELECT id, name, email, avatar_url, plan, role, created_at, updated_at FROM tenants WHERE id = $1 AND deleted_at IS NULL"
	err := h.DB.QueryRow(c.Context(), query, tenantID).Scan(
		&resp.ID, &resp.Name, &resp.Email, &resp.AvatarURL, &resp.Plan, &resp.Role, &resp.CreatedAt, &resp.UpdatedAt,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return utils.NotFound("Tenant profile not found")
		}
		return utils.InternalServerError("Failed to fetch tenant profile", err.Error())
	}

	c.Set("Cache-Control", "private, max-age=300") // 5 mins
	return c.JSON(resp)
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

// GetHistoricalUsageHandler godoc
// @Summary Get historical usage data (last 12 months)
// @Description Fetch time-series usage data for analytics dashboard (builder & enterprise only)
// @Tags Dashboard
// @Produce json
// @Success      200  {object}  payload.UsageHistoryResponse
// @Failure      401  {object}  utils.APIError
// @Failure      403  {object}  utils.APIError
// @Failure      500  {object}  utils.APIError
// @Router       /dashboard/usage/history [get]
func (h *Handler) GetHistoricalUsageHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Tenant ID not found in context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	// 1. Verify Plan Tier (Must be builder or enterprise)
	var plan string
	err = h.DB.QueryRow(c.Context(), "SELECT plan FROM tenants WHERE id = $1", tenantID).Scan(&plan)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return utils.Unauthorized("Tenant not found")
		}
		return utils.InternalServerError("Failed to verify tenant plan", err.Error())
	}

	if plan != "builder" && plan != "enterprise" {
		return utils.Forbidden("Insufficient plan tier. 'builder' or 'enterprise' required.")
	}

	// 2. Determine the time range (Last 12 months including current month)
	now := time.Now().UTC()

	// Create a map to securely track and pre-fill 12 months with 0s
	// This ensures even if a month has no db logs, it appears in the sequence
	historyMap := make(map[string]*payload.UsageMonth)
	var orderedPeriods []string

	for i := 11; i >= 0; i-- {
		// Calculate the month historically. e.g. AddDate(0, -i, 0)
		historyDate := now.AddDate(0, -i, 0)
		period := fmt.Sprintf("%04d-%02d", historyDate.Year(), historyDate.Month())

		orderedPeriods = append(orderedPeriods, period)
		historyMap[period] = &payload.UsageMonth{
			Period:       period,
			Resolutions:  0,
			Interactions: 0,
		}
	}

	// Calculate the threshold boundaries to put in the WHERE clause
	startBound := now.AddDate(0, -11, 0)
	startYear := startBound.Year()
	startMonth := int(startBound.Month())

	// 3. Query the Database for the last 12 months records
	// We want everything where (year > startYear) OR (year == startYear AND month >= startMonth)
	query := `
		SELECT period_year, period_month, resolution_count, interaction_count
		FROM usage_logs
		WHERE tenant_id = $1
		  AND (
		      period_year > $2 
		      OR (period_year = $2 AND period_month >= $3)
		  )
		ORDER BY period_year ASC, period_month ASC
	`

	rows, err := h.DB.Query(c.Context(), query, tenantID, startYear, startMonth)
	if err != nil {
		return utils.InternalServerError("Failed to fetch historical usage data", err.Error())
	}
	defer rows.Close()

	for rows.Next() {
		var year, month, resCount, intCount int
		if err := rows.Scan(&year, &month, &resCount, &intCount); err != nil {
			return utils.InternalServerError("Failed to scan usage log", err.Error())
		}

		periodStr := fmt.Sprintf("%04d-%02d", year, month)

		// If the db record matches one of our expected 12 months, update the counts
		if entry, exists := historyMap[periodStr]; exists {
			entry.Resolutions = resCount
			entry.Interactions = intCount
		}
	}

	// 4. Construct the ordered response
	var resp payload.UsageHistoryResponse
	resp.Months = make([]payload.UsageMonth, 0, 12)

	for _, period := range orderedPeriods {
		resp.Months = append(resp.Months, *historyMap[period])
	}

	return c.JSON(resp)
}
