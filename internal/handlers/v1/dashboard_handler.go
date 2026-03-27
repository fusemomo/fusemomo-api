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

/*
// GetRecommendationsHandler godoc
// @Summary List recommendations
// @Description Fetch paginated recommendations for the authenticated tenant
// @Tags Dashboard
// @Produce json
// @Param page query int false "Page number (default: 1)"
// @Param limit query int false "Page size (default: 50, max: 100)"
// @Param status query string false "Filter: all | followed | not_followed"
// @Param date_from query string false "YYYY-MM-DD (default: 30 days ago)"
// @Param date_to query string false "YYYY-MM-DD (default: today)"
// @Param sort query string false "Sort field: created_at | confidence_score"
// @Param order query string false "Sort order: asc | desc"
// @Success 200 {object} payload.GetRecommendationsResponse
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /dashboard/recommendations [get]
func (h *Handler) GetRecommendationsHandler(c fiber.Ctx) error {
	tenantID, err := tenantIDFromCtx(c)
	if err != nil {
		return err
	}

	// ── Parse & validate query params ─
	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	offset := (page - 1) * limit

	statusFilter := c.Query("status", "")

	sortField := c.Query("sort", "created_at")
	if sortField != "created_at" && sortField != "confidence_score" {
		sortField = "created_at"
	}
	sortOrder := c.Query("order", "desc")
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "desc"
	}

	// Validate and default date range (YYYY-MM-DD format enforced).
	now := time.Now().UTC()
	dateFrom, dateTo, err := parseDateRange(
		c.Query("date_from", ""),
		c.Query("date_to", ""),
		now,
	)
	if err != nil {
		return utils.BadRequest("Invalid date format: use YYYY-MM-DD", err.Error())
	}

	ctx := c.Context()

	// ── Build parameterised WHERE clause
	// Base args: $1=tenant_id  $2=dateFrom  $3=dateTo
	baseArgs := []any{tenantID, dateFrom + " 00:00:00", dateTo + " 23:59:59"}
	argIdx := 4 // next positional arg index

	statusClause := ""
	switch statusFilter {
	case "followed":
		statusClause = fmt.Sprintf(" AND r.was_followed = $%d", argIdx)
		baseArgs = append(baseArgs, true)
		argIdx++
	case "not_followed":
		statusClause = fmt.Sprintf(" AND r.was_followed = $%d", argIdx)
		baseArgs = append(baseArgs, false)
		argIdx++
	}

	// ── Count total (shared args, no extra params)
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM recommendations r
		JOIN entities e ON e.id = r.entity_id
		WHERE r.tenant_id = $1
		  AND r.created_at >= $2
		  AND r.created_at <= $3
		%s
	`, statusClause)

	var total int64
	if err := h.DB.QueryRow(ctx, countQuery, baseArgs...).Scan(&total); err != nil {
		return utils.InternalServerError("Failed to count recommendations", err.Error())
	}

	// ── Fetch page — copy baseArgs to avoid aliasing from the append above ─
	listArgs := make([]any, len(baseArgs), len(baseArgs)+2)
	copy(listArgs, baseArgs)
	listArgs = append(listArgs, limit, offset)

	listQuery := fmt.Sprintf(`
		SELECT
			r.id,
			r.entity_id,
			COALESCE(e.display_name, 'Unknown') AS entity_display_name,
			r.intent,
			r.recommended_action_type,
			r.confidence_score,
			r.was_followed,
			i.outcome::text,
			r.agent_id,
			r.created_at
		FROM recommendations r
		JOIN entities e ON e.id = r.entity_id
		LEFT JOIN interactions i ON i.id = r.outcome_interaction_id
		WHERE r.tenant_id = $1
		  AND r.created_at >= $2
		  AND r.created_at <= $3
		%s
		ORDER BY r.%s %s
		LIMIT $%d OFFSET $%d
	`, statusClause, sortField, sortOrder, argIdx, argIdx+1)

	rows, err := h.DB.Query(ctx, listQuery, listArgs...)
	if err != nil {
		return utils.InternalServerError("Failed to fetch recommendations", err.Error())
	}
	defer rows.Close()

	recs := make([]payload.DashboardRecommendationRow, 0, 50)
	for rows.Next() {
		var rec payload.DashboardRecommendationRow
		if err := rows.Scan(
			&rec.ID,
			&rec.EntityID,
			&rec.EntityDisplayName,
			&rec.Intent,
			&rec.RecommendedActionType,
			&rec.ConfidenceScore,
			&rec.WasFollowed,
			&rec.Outcome,
			&rec.AgentID,
			&rec.CreatedAt,
		); err != nil {
			return utils.InternalServerError("Failed to scan recommendation row", err.Error())
		}
		recs = append(recs, rec)
	}
	// Always check rows.Err() — a mid-stream network failure would not surface otherwise.
	if err := rows.Err(); err != nil {
		return utils.InternalServerError("Error reading recommendation rows", err.Error())
	}

	return c.JSON(payload.GetRecommendationsResponse{
		Recommendations: recs,
		Total:           total,
		Limit:           limit,
		Offset:          offset,
		Page:            page,
	})
}

// GetRecommendationStatsHandler godoc
// @Summary Recommendation aggregate stats
// @Description Get aggregate metrics for recommendation metrics cards
// @Tags Dashboard
// @Produce json
// @Param date_from query string false "YYYY-MM-DD (default: 30 days ago)"
// @Param date_to query string false "YYYY-MM-DD (default: today)"
// @Success 200 {object} payload.GetRecommendationStatsResponse
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /dashboard/recommendations/stats [get]
func (h *Handler) GetRecommendationStatsHandler(c fiber.Ctx) error {
	tenantID, err := tenantIDFromCtx(c)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	dateFrom, dateTo, err := parseDateRange(
		c.Query("date_from", ""),
		c.Query("date_to", ""),
		now,
	)
	if err != nil {
		return utils.BadRequest("Invalid date format: use YYYY-MM-DD", err.Error())
	}

	ctx := c.Context()
	tsFrom := dateFrom + " 00:00:00"
	tTo := dateTo + " 23:59:59"

	// ── Run the three independent aggregate queries concurrently ─
	var (
		totalServed, totalFollowed int64
		followThroughRate          float64
		successWhenFollowed        float64
		baselineSuccessRate        float64
	)

	g, gCtx := errgroup.WithContext(ctx)

	// 1. Total served & follow-through rate
	g.Go(func() error {
		return h.DB.QueryRow(gCtx, `
			SELECT
				COUNT(*),
				COUNT(*) FILTER (WHERE was_followed = true),
				COALESCE(
					COUNT(*) FILTER (WHERE was_followed = true)::float / NULLIF(COUNT(*), 0),
					0
				)
			FROM recommendations
			WHERE tenant_id = $1
			  AND created_at >= $2
			  AND created_at <= $3
		`, tenantID, tsFrom, tTo).Scan(&totalServed, &totalFollowed, &followThroughRate)
	})

	// 2. Success rate when the recommendation was followed
	g.Go(func() error {
		return h.DB.QueryRow(gCtx, `
			SELECT
				COALESCE(
					COUNT(*) FILTER (WHERE i.outcome = 'success')::float /
					NULLIF(COUNT(*) FILTER (WHERE r.was_followed = true), 0),
					0
				)
			FROM recommendations r
			LEFT JOIN interactions i ON i.id = r.outcome_interaction_id
			WHERE r.tenant_id = $1
			  AND r.was_followed = true
			  AND r.created_at >= $2
			  AND r.created_at <= $3
		`, tenantID, tsFrom, tTo).Scan(&successWhenFollowed)
	})

	// 3. Tenant-wide baseline success rate (for delta comparison)
	g.Go(func() error {
		return h.DB.QueryRow(gCtx, `
			SELECT
				COALESCE(
					COUNT(*) FILTER (WHERE outcome = 'success')::float / NULLIF(COUNT(*), 0),
					0
				)
			FROM interactions
			WHERE tenant_id = $1
		`, tenantID).Scan(&baselineSuccessRate)
	})

	if err := g.Wait(); err != nil {
		return utils.InternalServerError("Failed to fetch recommendation stats", err.Error())
	}

	improvementVsBaseline := math.Round((successWhenFollowed-baselineSuccessRate)*1000) / 10 // percentage points

	// ── Trend metrics (best-effort: errors are non-fatal)
	today := now.Format("2006-01-02")
	thisWeekStart := now.AddDate(0, 0, -7).Format("2006-01-02")
	prevWeekStart := now.AddDate(0, 0, -14).Format("2006-01-02")
	prevWeekEnd := now.AddDate(0, 0, -7).Format("2006-01-02")

	var servedToday int
	var thisWeekFT, prevWeekFT float64

	tg, tCtx := errgroup.WithContext(ctx)
	tg.Go(func() error {
		return h.DB.QueryRow(tCtx,
			`SELECT COUNT(*) FROM recommendations WHERE tenant_id=$1 AND created_at >= $2`,
			tenantID, today+" 00:00:00").Scan(&servedToday)
	})
	tg.Go(func() error {
		return h.DB.QueryRow(tCtx,
			`SELECT COALESCE(COUNT(*) FILTER (WHERE was_followed=true)::float / NULLIF(COUNT(*),0), 0)
			  FROM recommendations WHERE tenant_id=$1 AND created_at >= $2 AND created_at <= $3`,
			tenantID, thisWeekStart+" 00:00:00", today+" 23:59:59").Scan(&thisWeekFT)
	})
	tg.Go(func() error {
		return h.DB.QueryRow(tCtx,
			`SELECT COALESCE(COUNT(*) FILTER (WHERE was_followed=true)::float / NULLIF(COUNT(*),0), 0)
			  FROM recommendations WHERE tenant_id=$1 AND created_at >= $2 AND created_at <= $3`,
			tenantID, prevWeekStart+" 00:00:00", prevWeekEnd+" 23:59:59").Scan(&prevWeekFT)
	})
	// Trends are best-effort: a trend query error degrades gracefully (returns 0 values).
	_ = tg.Wait()

	return c.JSON(payload.GetRecommendationStatsResponse{
		TotalServed:           totalServed,
		TotalFollowed:         totalFollowed,
		FollowThroughRate:     followThroughRate,
		SuccessWhenFollowed:   successWhenFollowed,
		BaselineSuccessRate:   baselineSuccessRate,
		ImprovementVsBaseline: improvementVsBaseline,
		Trends: payload.RecommendationTrends{
			ServedToday:             servedToday,
			FollowThroughChangeWeek: math.Round((thisWeekFT-prevWeekFT)*1000) / 10,
			SuccessChangeWeek:       0, // extend in a future iteration
		},
	})
}
*/

// ── Shared handler helpers ─

// tenantIDFromCtx extracts and parses the tenant UUID set by SupabaseJWTMiddleware.
func tenantIDFromCtx(c fiber.Ctx) (uuid.UUID, error) {
	v := c.Locals("tenant_id")
	if v == nil {
		return uuid.Nil, utils.Unauthorized("Tenant ID not found in context")
	}
	id, err := uuid.Parse(fmt.Sprintf("%v", v))
	if err != nil {
		return uuid.Nil, utils.Unauthorized("Invalid tenant ID format")
	}
	return id, nil
}

// parseDateRange validates optional YYYY-MM-DD query params and returns UTC defaults.
func parseDateRange(from, to string, now time.Time) (string, string, error) {
	const layout = "2006-01-02"
	if from != "" {
		if _, err := time.Parse(layout, from); err != nil {
			return "", "", fmt.Errorf("date_from %q: %w", from, err)
		}
	} else {
		from = now.AddDate(0, 0, -30).Format(layout)
	}
	if to != "" {
		if _, err := time.Parse(layout, to); err != nil {
			return "", "", fmt.Errorf("date_to %q: %w", to, err)
		}
	} else {
		to = now.Format(layout)
	}
	return from, to, nil
}
