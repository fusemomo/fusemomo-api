package v1

import (
	"fmt"
	"fusemomo-api/internal/utils"
	"log"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
)

// tenantPlan returns the plan tier string for a given tenant UUID.
func (h *Handler) tenantPlan(ctx fiber.Ctx, tenantID interface{}) (string, error) {
	var plan string
	err := h.DB.QueryRow(ctx.Context(), `SELECT plan::text FROM tenants WHERE id = $1`, tenantID).Scan(&plan)
	return plan, err
}

// GetAnalyticsSummaryHandler godoc
// @Summary Analytics summary
// @Description High-level summary statistics for the analytics dashboard
// @Tags Analytics
// @Produce json
// @Param period       query string  true  "Time range: 7d | 30d | 90d | all"
// @Param compare_to_previous query bool false "Include previous-period comparison (default true)"
// @Success 200 {object} fiber.Map
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /app/analytics/summary [get]
func (h *Handler) GetAnalyticsSummaryHandler(c fiber.Ctx) error {
	start := time.Now()

	tenantID, err := tenantIDFromCtx(c)
	if err != nil {
		return err
	}

	period := c.Query("period")
	if !utils.ValidAnalyticsPeriods[period] {
		return utils.BadRequest("period must be one of: 7d, 30d, 90d, all")
	}

	compareToPrevious := c.Query("compare_to_previous", "true") != "false"
	if period == "all" {
		compareToPrevious = false
	}

	periodStart, periodEnd := utils.AnalyticsPeriodRange(period)

	//  Plan check
	plan, err := h.tenantPlan(c, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to fetch tenant plan", err.Error())
	}
	isBuilder := plan == "builder" || plan == "enterprise"

	//  Current period
	type periodMetrics struct {
		total          int64
		successful     int64
		activeEntities int64
		mostUsedAction string
		mostUsedCount  int64
	}

	var cur periodMetrics
	err = h.DB.QueryRow(c.Context(), `
		SELECT
			COUNT(*)                                               AS total,
			COUNT(*) FILTER (WHERE outcome = 'success')           AS successful,
			COUNT(DISTINCT entity_id)                             AS active_entities,
			COALESCE(
				(SELECT action_type FROM interactions
				 WHERE tenant_id = $1 AND occurred_at BETWEEN $2 AND $3
				 GROUP BY action_type ORDER BY COUNT(*) DESC LIMIT 1),
			''),
			COALESCE(
				(SELECT COUNT(*) FROM interactions
				 WHERE tenant_id = $1 AND occurred_at BETWEEN $2 AND $3
				 GROUP BY action_type ORDER BY COUNT(*) DESC LIMIT 1),
			0)
		FROM interactions
		WHERE tenant_id = $1
		  AND occurred_at BETWEEN $2 AND $3
	`, tenantID, periodStart, periodEnd).Scan(
		&cur.total, &cur.successful, &cur.activeEntities,
		&cur.mostUsedAction, &cur.mostUsedCount,
	)
	if err != nil {
		log.Printf("[GetAnalyticsSummaryHandler] current period query error: %v", err)
		return utils.InternalServerError("Failed to fetch analytics summary", err.Error())
	}

	var curSuccessRate float64
	if cur.total > 0 {
		curSuccessRate = utils.Round1(float64(cur.successful) / float64(cur.total) * 100)
	}

	//  Builder+ recommendation metrics
	var recAccuracy, avgConfidence float64
	if isBuilder {
		h.DB.QueryRow(c.Context(), `
			SELECT
				COALESCE(
					ROUND(
						COUNT(*) FILTER (WHERE i.outcome = 'success')::NUMERIC /
						NULLIF(COUNT(*), 0) * 100, 1
					), 0),
				COALESCE(ROUND(AVG(r.confidence_score)::NUMERIC, 3), 0)
			FROM recommendations r
			JOIN interactions i ON i.id = r.outcome_interaction_id
			WHERE r.tenant_id = $1
			  AND r.created_at BETWEEN $2 AND $3
			  AND r.was_followed = true
		`, tenantID, periodStart, periodEnd).Scan(&recAccuracy, &avgConfidence) //nolint:errcheck
	}

	//  Previous period
	type prevPeriodData struct {
		successRate    float64
		total          int64
		activeEntities int64
	}
	var prev prevPeriodData
	if compareToPrevious {
		prevStart, prevEnd := utils.PreviousPeriodRange(period)
		h.DB.QueryRow(c.Context(), `
			SELECT
				COALESCE(
					ROUND(
						COUNT(*) FILTER (WHERE outcome = 'success')::NUMERIC /
						NULLIF(COUNT(*), 0) * 100, 1
					), 0),
				COUNT(*),
				COUNT(DISTINCT entity_id)
			FROM interactions
			WHERE tenant_id = $1
			  AND occurred_at BETWEEN $2 AND $3
		`, tenantID, prevStart, prevEnd).Scan(
			&prev.successRate, &prev.total, &prev.activeEntities,
		) //nolint:errcheck
	}

	//  Build response
	srChange := utils.Round1(curSuccessRate - prev.successRate)
	intChange := cur.total - prev.total
	entChange := cur.activeEntities - prev.activeEntities

	summary := fiber.Map{
		"success_rate": fiber.Map{
			"value":  curSuccessRate,
			"change": srChange,
			"trend":  utils.Trend(srChange),
		},
		"total_interactions": fiber.Map{
			"value":  cur.total,
			"change": intChange,
			"trend":  utils.Trend(float64(intChange)),
		},
		"active_entities": fiber.Map{
			"value":  cur.activeEntities,
			"change": entChange,
			"trend":  utils.Trend(float64(entChange)),
		},
		"most_used_action_type": fiber.Map{
			"action_type": cur.mostUsedAction,
			"count":       cur.mostUsedCount,
		},
		"recommendation_accuracy": fiber.Map{
			"value":     recAccuracy,
			"change":    0.0,
			"trend":     "neutral",
			"available": isBuilder,
		},
		"avg_confidence_score": fiber.Map{
			"value":     avgConfidence,
			"change":    0.0,
			"trend":     "neutral",
			"available": isBuilder,
		},
	}

	resp := fiber.Map{
		"period":       period,
		"period_start": periodStart.Format(time.RFC3339),
		"period_end":   periodEnd.Format(time.RFC3339),
		"summary":      summary,
	}
	if compareToPrevious {
		prevStart, prevEnd := utils.PreviousPeriodRange(period)
		resp["previous_period"] = fiber.Map{
			"period_start":       prevStart.Format(time.RFC3339),
			"period_end":         prevEnd.Format(time.RFC3339),
			"success_rate":       prev.successRate,
			"total_interactions": prev.total,
			"active_entities":    prev.activeEntities,
		}
	}

	log.Printf("[GetAnalyticsSummaryHandler] tenant=%s period=%s execution_ms=%d",
		tenantID, period, time.Since(start).Milliseconds())

	return c.JSON(resp)
}

// GetSuccessRateTimeSeriesHandler godoc
// @Summary Success rate time series
// @Description Time-series data of success rates for line chart visualization
// @Tags Analytics
// @Produce json
// @Param period                query string false "7d | 30d | 90d | all"
// @Param granularity           query string false "day | week | month (auto-selected if omitted)"
// @Param include_recommendations query bool false "Include recommendation lift data (Builder+ only)"
// @Success 200 {object} fiber.Map
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 402 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /app/analytics/success-rate-timeseries [get]
func (h *Handler) GetSuccessRateTimeSeriesHandler(c fiber.Ctx) error {
	tenantID, err := tenantIDFromCtx(c)
	if err != nil {
		return err
	}

	period := c.Query("period")
	if !utils.ValidAnalyticsPeriods[period] {
		return utils.BadRequest("period must be one of: 7d, 30d, 90d, all")
	}

	// Auto-select granularity
	granularity := c.Query("granularity")
	validGran := map[string]bool{"day": true, "week": true, "month": true}
	if granularity == "" {
		switch period {
		case "7d", "30d":
			granularity = "day"
		case "90d":
			granularity = "week"
		default:
			granularity = "month"
		}
	} else if !validGran[granularity] {
		return utils.BadRequest("granularity must be one of: day, week, month")
	}

	includeRec := c.Query("include_recommendations") == "true"

	// Plan check if recommendation comparison is requested
	if includeRec {
		plan, err := h.tenantPlan(c, tenantID)
		if err != nil {
			return utils.InternalServerError("Failed to check plan", err.Error())
		}
		if plan == "free" {
			return c.Status(fiber.StatusPaymentRequired).JSON(fiber.Map{
				"error": "Recommendation comparison requires Builder tier or above",
			})
		}
	}

	periodStart, periodEnd := utils.AnalyticsPeriodRange(period)

	// Main time-series query
	rows, err := h.DB.Query(c.Context(), fmt.Sprintf(`
		SELECT
			DATE_TRUNC('%s', occurred_at)::date                             AS bucket,
			COUNT(*)                                                         AS total,
			COUNT(*) FILTER (WHERE outcome = 'success')                     AS successful
		FROM interactions
		WHERE tenant_id = $1
		  AND occurred_at BETWEEN $2 AND $3
		GROUP BY bucket
		ORDER BY bucket ASC
	`, granularity), tenantID, periodStart, periodEnd)
	if err != nil {
		return utils.InternalServerError("Failed to fetch time series data", err.Error())
	}
	defer rows.Close()

	type tsPoint struct {
		Date        string   `json:"date"`
		SuccessRate float64  `json:"overall_success_rate"`
		Total       int64    `json:"total_interactions"`
		Successful  int64    `json:"successful_interactions"`
		WithRec     *float64 `json:"with_recommendations"`
		WithoutRec  *float64 `json:"without_recommendations"`
	}

	points := make([]tsPoint, 0, 64)
	for rows.Next() {
		var bucket time.Time
		var total, successful int64
		if err := rows.Scan(&bucket, &total, &successful); err != nil {
			return utils.InternalServerError("Failed to scan time series row", err.Error())
		}
		sr := 0.0
		if total > 0 {
			sr = utils.Round1(float64(successful) / float64(total) * 100)
		}
		points = append(points, tsPoint{
			Date:        bucket.Format("2006-01-02"),
			SuccessRate: sr,
			Total:       total,
			Successful:  successful,
		})
	}
	if err = rows.Err(); err != nil {
		return utils.InternalServerError("Error reading time series rows", err.Error())
	}

	// Recommendation lift data (Builder+)
	if includeRec {
		type recPoint struct {
			bucket    string
			withSR    float64
			withoutSR float64
		}
		recMap := make(map[string]recPoint)

		// With recommendations
		withRows, err := h.DB.Query(c.Context(), fmt.Sprintf(`
			SELECT
				DATE_TRUNC('%s', i.occurred_at)::date AS bucket,
				COALESCE(ROUND(COUNT(*) FILTER (WHERE i.outcome='success')::NUMERIC / NULLIF(COUNT(*),0) * 100, 1), 0)
			FROM interactions i
			JOIN recommendations r ON r.outcome_interaction_id = i.id
			WHERE i.tenant_id = $1
			  AND i.occurred_at BETWEEN $2 AND $3
			  AND r.was_followed = true
			GROUP BY bucket ORDER BY bucket ASC
		`, granularity), tenantID, periodStart, periodEnd)
		if err == nil {
			defer withRows.Close()
			for withRows.Next() {
				var b time.Time
				var sr float64
				if scanErr := withRows.Scan(&b, &sr); scanErr == nil {
					key := b.Format("2006-01-02")
					rp := recMap[key]
					rp.bucket = key
					rp.withSR = sr
					recMap[key] = rp
				}
			}
		}

		// Without recommendations
		withoutRows, err := h.DB.Query(c.Context(), fmt.Sprintf(`
			SELECT
				DATE_TRUNC('%s', occurred_at)::date AS bucket,
				COALESCE(ROUND(COUNT(*) FILTER (WHERE outcome='success')::NUMERIC / NULLIF(COUNT(*),0) * 100, 1), 0)
			FROM interactions
			WHERE tenant_id = $1
			  AND occurred_at BETWEEN $2 AND $3
			  AND id NOT IN (
				SELECT outcome_interaction_id FROM recommendations
				WHERE was_followed = true AND outcome_interaction_id IS NOT NULL
			  )
			GROUP BY bucket ORDER BY bucket ASC
		`, granularity), tenantID, periodStart, periodEnd)
		if err == nil {
			defer withoutRows.Close()
			for withoutRows.Next() {
				var b time.Time
				var sr float64
				if scanErr := withoutRows.Scan(&b, &sr); scanErr == nil {
					key := b.Format("2006-01-02")
					rp := recMap[key]
					rp.bucket = key
					rp.withoutSR = sr
					recMap[key] = rp
				}
			}
		}

		// Merge into points
		for i, pt := range points {
			if rp, ok := recMap[pt.Date]; ok {
				with := rp.withSR
				without := rp.withoutSR
				points[i].WithRec = &with
				points[i].WithoutRec = &without
			}
		}
	}

	return c.JSON(fiber.Map{
		"period":      period,
		"granularity": granularity,
		"data":        points,
	})
}

// GetPerformanceByActionTypeHandler godoc
// @Summary Performance by action type
// @Description Success rates broken down by action_type for bar chart
// @Tags Analytics
// @Produce json
// @Param period  query string false "7d | 30d | 90d | all"
// @Param sort_by query string false "success_rate | count | name (default: success_rate)"
// @Param limit   query int    false "1-50 (default: 10)"
// @Success 200 {object} fiber.Map
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /app/analytics/performance-by-action-type [get]
func (h *Handler) GetPerformanceByActionTypeHandler(c fiber.Ctx) error {
	tenantID, err := tenantIDFromCtx(c)
	if err != nil {
		return err
	}

	period := c.Query("period")
	if !utils.ValidAnalyticsPeriods[period] {
		return utils.BadRequest("period must be one of: 7d, 30d, 90d, all")
	}

	validSort := map[string]bool{"success_rate": true, "count": true, "name": true}
	sortBy := c.Query("sort_by", "success_rate")
	if !validSort[sortBy] {
		return utils.BadRequest("sort_by must be one of: success_rate, count, name")
	}

	limit, _ := strconv.Atoi(c.Query("limit", "10"))
	if limit < 1 || limit > 50 {
		limit = 10
	}

	periodStart, periodEnd := utils.AnalyticsPeriodRange(period)

	// Determine ORDER BY clause
	orderClause := "success_rate DESC"
	switch sortBy {
	case "count":
		orderClause = "total_count DESC"
	case "name":
		orderClause = "action_type ASC"
	}

	// Count total distinct action_types before limit
	var totalActionTypes int
	h.DB.QueryRow(c.Context(), `
		SELECT COUNT(DISTINCT action_type) FROM interactions
		WHERE tenant_id = $1 AND occurred_at BETWEEN $2 AND $3
	`, tenantID, periodStart, periodEnd).Scan(&totalActionTypes) //nolint:errcheck

	rows, err := h.DB.Query(c.Context(), fmt.Sprintf(`
		SELECT
			action_type,
			COUNT(*)                                                AS total_count,
			COUNT(*) FILTER (WHERE outcome = 'success')            AS successful_count,
			COUNT(*) FILTER (WHERE outcome = 'failed')             AS failed_count,
			COUNT(*) FILTER (WHERE outcome = 'pending')            AS pending_count,
			COUNT(*) FILTER (WHERE outcome = 'ignored')            AS ignored_count,
			COUNT(*) FILTER (WHERE outcome = 'unknown')            AS unknown_count,
			COALESCE(
				ROUND(
					COUNT(*) FILTER (WHERE outcome = 'success')::NUMERIC /
					NULLIF(COUNT(*), 0) * 100, 1
				), 0)                                              AS success_rate
		FROM interactions
		WHERE tenant_id = $1
		  AND occurred_at BETWEEN $2 AND $3
		GROUP BY action_type
		ORDER BY %s
		LIMIT $4
	`, orderClause), tenantID, periodStart, periodEnd, limit)
	if err != nil {
		return utils.InternalServerError("Failed to fetch performance by action type", err.Error())
	}
	defer rows.Close()

	type actionRow struct {
		ActionType      string  `json:"action_type"`
		SuccessRate     float64 `json:"success_rate"`
		TotalCount      int64   `json:"total_count"`
		SuccessfulCount int64   `json:"successful_count"`
		FailedCount     int64   `json:"failed_count"`
		PendingCount    int64   `json:"pending_count"`
		IgnoredCount    int64   `json:"ignored_count"`
		UnknownCount    int64   `json:"unknown_count"`
	}

	data := make([]actionRow, 0, limit)
	for rows.Next() {
		var r actionRow
		if err := rows.Scan(
			&r.ActionType, &r.TotalCount, &r.SuccessfulCount,
			&r.FailedCount, &r.PendingCount, &r.IgnoredCount,
			&r.UnknownCount, &r.SuccessRate,
		); err != nil {
			return utils.InternalServerError("Failed to scan action type row", err.Error())
		}
		data = append(data, r)
	}
	if err = rows.Err(); err != nil {
		return utils.InternalServerError("Error reading action type rows", err.Error())
	}

	return c.JSON(fiber.Map{
		"period":             period,
		"sort_by":            sortBy,
		"total_action_types": totalActionTypes,
		"data":               data,
	})
}

// GetActivityHeatmapHandler godoc
// @Summary Activity heatmap
// @Description Daily interaction counts for calendar heatmap (max 90d)
// @Tags Analytics
// @Produce json
// @Param period query string true "7d | 30d | 90d (no 'all')"
// @Success 200 {object} fiber.Map
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /app/analytics/activity-heatmap [get]
func (h *Handler) GetActivityHeatmapHandler(c fiber.Ctx) error {
	tenantID, err := tenantIDFromCtx(c)
	if err != nil {
		return err
	}

	period := c.Query("period")
	validHeatmapPeriods := map[string]bool{"7d": true, "30d": true, "90d": true}
	if !validHeatmapPeriods[period] {
		return utils.BadRequest("period must be one of: 7d, 30d, 90d (heatmap limited to 90 days)")
	}

	periodStart, periodEnd := utils.AnalyticsPeriodRange(period)

	// Use generate_series to fill gaps (no missing dates)
	rows, err := h.DB.Query(c.Context(), `
		SELECT
			d.day::date,
			COALESCE(COUNT(i.id), 0) AS interaction_count
		FROM generate_series($2::date, $3::date, '1 day'::interval) AS d(day)
		LEFT JOIN interactions i
			ON i.tenant_id = $1
			AND DATE_TRUNC('day', i.occurred_at) = d.day
		GROUP BY d.day
		ORDER BY d.day ASC
	`, tenantID, periodStart.Format("2006-01-02"), periodEnd.Format("2006-01-02"))
	if err != nil {
		return utils.InternalServerError("Failed to fetch activity heatmap", err.Error())
	}
	defer rows.Close()

	intensityOf := func(n int64) string {
		switch {
		case n == 0:
			return "none"
		case n <= 10:
			return "low"
		case n <= 50:
			return "medium"
		case n <= 100:
			return "high"
		default:
			return "very_high"
		}
	}

	type heatPoint struct {
		Date             string `json:"date"`
		InteractionCount int64  `json:"interaction_count"`
		Intensity        string `json:"intensity"`
	}

	data := make([]heatPoint, 0, 90)
	for rows.Next() {
		var day time.Time
		var count int64
		if err := rows.Scan(&day, &count); err != nil {
			return utils.InternalServerError("Failed to scan heatmap row", err.Error())
		}
		data = append(data, heatPoint{
			Date:             day.Format("2006-01-02"),
			InteractionCount: count,
			Intensity:        intensityOf(count),
		})
	}
	if err = rows.Err(); err != nil {
		return utils.InternalServerError("Error reading heatmap rows", err.Error())
	}

	return c.JSON(fiber.Map{
		"period":       period,
		"period_start": periodStart.Format("2006-01-02"),
		"period_end":   periodEnd.Format("2006-01-02"),
		"data":         data,
		"scale": fiber.Map{
			"none":      [2]interface{}{0, 0},
			"low":       [2]interface{}{1, 10},
			"medium":    [2]interface{}{11, 50},
			"high":      [2]interface{}{51, 100},
			"very_high": [2]interface{}{101, nil},
		},
	})
}

// GetApiDistributionHandler godoc
// @Summary API call distribution
// @Description Percentage distribution of API usage for pie/donut chart
// @Tags Analytics
// @Produce json
// @Param period query string true "7d | 30d | 90d | all"
// @Success 200 {object} fiber.Map
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /app/analytics/api-distribution [get]
func (h *Handler) GetApiDistributionHandler(c fiber.Ctx) error {
	tenantID, err := tenantIDFromCtx(c)
	if err != nil {
		return err
	}

	period := c.Query("period")
	if !utils.ValidAnalyticsPeriods[period] {
		return utils.BadRequest("period must be one of: 7d, 30d, 90d, all")
	}

	periodStart, periodEnd := utils.AnalyticsPeriodRange(period)

	rows, err := h.DB.Query(c.Context(), `
		SELECT
			api,
			COUNT(*)                                                            AS count,
			COALESCE(
				ROUND(
					COUNT(*) FILTER (WHERE outcome = 'success')::NUMERIC /
					NULLIF(COUNT(*), 0) * 100, 1
				), 0)                                                           AS success_rate
		FROM interactions
		WHERE tenant_id = $1
		  AND occurred_at BETWEEN $2 AND $3
		GROUP BY api
		ORDER BY count DESC
	`, tenantID, periodStart, periodEnd)
	if err != nil {
		return utils.InternalServerError("Failed to fetch API distribution", err.Error())
	}
	defer rows.Close()

	type apiRow struct {
		API         string  `json:"api"`
		Count       int64   `json:"count"`
		Percentage  float64 `json:"percentage"`
		SuccessRate float64 `json:"success_rate"`
		Color       string  `json:"color"`
	}

	var totalInteractions int64
	rawRows := make([]apiRow, 0, 20)
	for rows.Next() {
		var r apiRow
		if err := rows.Scan(&r.API, &r.Count, &r.SuccessRate); err != nil {
			return utils.InternalServerError("Failed to scan API row", err.Error())
		}
		totalInteractions += r.Count
		rawRows = append(rawRows, r)
	}
	if err = rows.Err(); err != nil {
		return utils.InternalServerError("Error reading API distribution rows", err.Error())
	}

	data := make([]apiRow, 0, len(rawRows))
	for i, r := range rawRows {
		if totalInteractions > 0 {
			r.Percentage = utils.Round1(float64(r.Count) / float64(totalInteractions) * 100)
		}
		if i < len(utils.AnalyticsPredefinedColors) {
			r.Color = utils.AnalyticsPredefinedColors[i]
		} else {
			r.Color = "#94a3b8"
		}
		data = append(data, r)
	}

	return c.JSON(fiber.Map{
		"period":             period,
		"total_interactions": totalInteractions,
		"data":               data,
	})
}

// GetTopEntitiesHandler godoc
// @Summary Top performing entities
// @Description Entities ranked by success rate or interaction count
// @Tags Analytics
// @Produce json
// @Param period           query string false "7d | 30d | 90d | all"
// @Param sort_by          query string false "success_rate | interaction_count (default: success_rate)"
// @Param min_interactions query int    false "Minimum interactions to qualify (default: 10)"
// @Param limit            query int    false "1-50 (default: 10)"
// @Success 200 {object} fiber.Map
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /app/analytics/top-entities [get]
func (h *Handler) GetTopEntitiesHandler(c fiber.Ctx) error {
	tenantID, err := tenantIDFromCtx(c)
	if err != nil {
		return err
	}

	period := c.Query("period")
	if !utils.ValidAnalyticsPeriods[period] {
		return utils.BadRequest("period must be one of: 7d, 30d, 90d, all")
	}

	validSort := map[string]bool{"success_rate": true, "interaction_count": true}
	sortBy := c.Query("sort_by", "success_rate")
	if !validSort[sortBy] {
		return utils.BadRequest("sort_by must be one of: success_rate, interaction_count")
	}

	minInteractions, _ := strconv.Atoi(c.Query("min_interactions", "10"))
	if minInteractions < 1 || minInteractions > 100 {
		minInteractions = 10
	}

	limit, _ := strconv.Atoi(c.Query("limit", "10"))
	if limit < 1 || limit > 50 {
		limit = 10
	}

	periodStart, periodEnd := utils.AnalyticsPeriodRange(period)

	orderClause := "success_rate DESC"
	if sortBy == "interaction_count" {
		orderClause = "total_interactions DESC"
	}

	// Count qualifying entities before applying limit
	var totalQualifying int
	h.DB.QueryRow(c.Context(), `
		SELECT COUNT(*) FROM (
			SELECT entity_id FROM interactions
			WHERE tenant_id = $1 AND occurred_at BETWEEN $2 AND $3
			GROUP BY entity_id HAVING COUNT(*) >= $4
		) sub
	`, tenantID, periodStart, periodEnd, minInteractions).Scan(&totalQualifying) //nolint:errcheck

	rows, err := h.DB.Query(c.Context(), fmt.Sprintf(`
		SELECT
			e.id,
			COALESCE(e.display_name, ''),
			COALESCE(e.entity_type, ''),
			COALESCE(e.preferred_action_type, ''),
			COALESCE(e.behavioral_score, 0),
			COUNT(i.id)                                                       AS total_interactions,
			COUNT(i.id) FILTER (WHERE i.outcome = 'success')                 AS successful_interactions,
			COUNT(i.id) FILTER (WHERE i.outcome = 'failed')                  AS failed_interactions,
			COALESCE(
				ROUND(
					COUNT(i.id) FILTER (WHERE i.outcome = 'success')::NUMERIC /
					NULLIF(COUNT(i.id), 0) * 100, 1
				), 0)                                                         AS success_rate
		FROM interactions i
		JOIN entities e ON e.id = i.entity_id
		WHERE i.tenant_id = $1
		  AND i.occurred_at BETWEEN $2 AND $3
		GROUP BY e.id
		HAVING COUNT(i.id) >= $4
		ORDER BY %s
		LIMIT $5
	`, orderClause), tenantID, periodStart, periodEnd, minInteractions, limit)
	if err != nil {
		return utils.InternalServerError("Failed to fetch top entities", err.Error())
	}
	defer rows.Close()

	type entityRow struct {
		EntityID               string  `json:"entity_id"`
		DisplayName            string  `json:"display_name"`
		EntityType             string  `json:"entity_type"`
		PreferredActionType    string  `json:"preferred_action_type"`
		BehavioralScore        float64 `json:"behavioral_score"`
		TotalInteractions      int64   `json:"total_interactions"`
		SuccessfulInteractions int64   `json:"successful_interactions"`
		FailedInteractions     int64   `json:"failed_interactions"`
		SuccessRate            float64 `json:"success_rate"`
	}

	data := make([]entityRow, 0, limit)
	for rows.Next() {
		var r entityRow
		var bs float64
		if err := rows.Scan(
			&r.EntityID, &r.DisplayName, &r.EntityType,
			&r.PreferredActionType, &bs,
			&r.TotalInteractions, &r.SuccessfulInteractions,
			&r.FailedInteractions, &r.SuccessRate,
		); err != nil {
			return utils.InternalServerError("Failed to scan entity row", err.Error())
		}
		r.BehavioralScore = utils.Round3(bs)
		data = append(data, r)
	}
	if err = rows.Err(); err != nil {
		return utils.InternalServerError("Error reading top entity rows", err.Error())
	}

	return c.JSON(fiber.Map{
		"period":                    period,
		"sort_by":                   sortBy,
		"min_interactions":          minInteractions,
		"total_qualifying_entities": totalQualifying,
		"data":                      data,
	})
}

// GetRecommendationImpactHandler godoc
// @Summary Recommendation ROI impact
// @Description Compares outcomes with vs. without recommendations (Builder+ only)
// @Tags Analytics
// @Produce json
// @Param period query string true "7d | 30d | 90d | all"
// @Success 200 {object} fiber.Map
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /app/analytics/recommendation-impact [get]
func (h *Handler) GetRecommendationImpactHandler(c fiber.Ctx) error {
	tenantID, err := tenantIDFromCtx(c)
	if err != nil {
		return err
	}

	period := c.Query("period")
	if !utils.ValidAnalyticsPeriods[period] {
		return utils.BadRequest("period must be one of: 7d, 30d, 90d, all")
	}

	// Plan check — return 200 with available=false for Free tier (better UX)
	plan, err := h.tenantPlan(c, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to check tenant plan", err.Error())
	}
	if plan == "free" {
		return c.JSON(fiber.Map{
			"period":      period,
			"available":   false,
			"reason":      "Recommendation analytics available on Builder tier and above",
			"upgrade_url": "/pricing",
		})
	}

	periodStart, periodEnd := utils.AnalyticsPeriodRange(period)

	// Quick count check — need at least 10 recommendations
	const minRecommendations = 10
	var recCount int64
	h.DB.QueryRow(c.Context(), `
		SELECT COUNT(*) FROM recommendations
		WHERE tenant_id = $1 AND created_at BETWEEN $2 AND $3
	`, tenantID, periodStart, periodEnd).Scan(&recCount) //nolint:errcheck

	if recCount < minRecommendations {
		return c.JSON(fiber.Map{
			"period":                period,
			"available":             true,
			"has_sufficient_data":   false,
			"reason":                "Need at least 10 recommendations to show impact analysis",
			"recommendations_count": recCount,
			"required_count":        minRecommendations,
		})
	}

	//  With recommendations
	type impactMetrics struct {
		successRate   float64
		total         int64
		successful    int64
		failed        int64
		pending       int64
		avgConfidence float64
	}

	var withRec impactMetrics
	err = h.DB.QueryRow(c.Context(), `
		SELECT
			COALESCE(ROUND(COUNT(*) FILTER (WHERE i.outcome='success')::NUMERIC / NULLIF(COUNT(*),0) * 100, 1), 0),
			COUNT(*),
			COUNT(*) FILTER (WHERE i.outcome = 'success'),
			COUNT(*) FILTER (WHERE i.outcome = 'failed'),
			COUNT(*) FILTER (WHERE i.outcome = 'pending'),
			COALESCE(ROUND(AVG(r.confidence_score)::NUMERIC, 3), 0)
		FROM interactions i
		JOIN recommendations r ON r.outcome_interaction_id = i.id
		WHERE i.tenant_id = $1
		  AND i.occurred_at BETWEEN $2 AND $3
		  AND r.was_followed = true
	`, tenantID, periodStart, periodEnd).Scan(
		&withRec.successRate, &withRec.total, &withRec.successful,
		&withRec.failed, &withRec.pending, &withRec.avgConfidence,
	)
	if err != nil {
		return utils.InternalServerError("Failed to fetch recommendation impact (with)", err.Error())
	}

	//  Without recommendations
	var withoutRec impactMetrics
	err = h.DB.QueryRow(c.Context(), `
		SELECT
			COALESCE(ROUND(COUNT(*) FILTER (WHERE outcome='success')::NUMERIC / NULLIF(COUNT(*),0) * 100, 1), 0),
			COUNT(*),
			COUNT(*) FILTER (WHERE outcome = 'success'),
			COUNT(*) FILTER (WHERE outcome = 'failed'),
			COUNT(*) FILTER (WHERE outcome = 'pending')
		FROM interactions
		WHERE tenant_id = $1
		  AND occurred_at BETWEEN $2 AND $3
		  AND id NOT IN (
			SELECT COALESCE(outcome_interaction_id, '00000000-0000-0000-0000-000000000000')
			FROM recommendations
			WHERE was_followed = true AND outcome_interaction_id IS NOT NULL
		  )
	`, tenantID, periodStart, periodEnd).Scan(
		&withoutRec.successRate, &withoutRec.total, &withoutRec.successful,
		&withoutRec.failed, &withoutRec.pending,
	)
	if err != nil {
		return utils.InternalServerError("Failed to fetch recommendation impact (without)", err.Error())
	}

	//  Impact calculations
	ppImprovement := utils.Round1(withRec.successRate - withoutRec.successRate)
	multiplier := 0.0
	if withoutRec.successRate > 0 {
		multiplier = utils.Round2(withRec.successRate / withoutRec.successRate)
	}
	additionalSuccesses := int64(0)
	if withRec.total > 0 && withoutRec.successRate > 0 {
		additionalSuccesses = withRec.successful - int64(float64(withRec.total)*withoutRec.successRate/100)
	}
	if additionalSuccesses < 0 {
		additionalSuccesses = 0
	}

	roiMsg := fmt.Sprintf("Following recommendations led to %d additional successes", additionalSuccesses)

	return c.JSON(fiber.Map{
		"period":              period,
		"available":           true,
		"has_sufficient_data": true,
		"with_recommendations": fiber.Map{
			"success_rate":                 withRec.successRate,
			"total_count":                  withRec.total,
			"successful_count":             withRec.successful,
			"failed_count":                 withRec.failed,
			"pending_count":                withRec.pending,
			"avg_confidence_when_followed": utils.Round3(withRec.avgConfidence),
		},
		"without_recommendations": fiber.Map{
			"success_rate":     withoutRec.successRate,
			"total_count":      withoutRec.total,
			"successful_count": withoutRec.successful,
			"failed_count":     withoutRec.failed,
			"pending_count":    withoutRec.pending,
		},
		"impact": fiber.Map{
			"percentage_point_improvement": ppImprovement,
			"multiplier":                   multiplier,
			"additional_successes":         additionalSuccesses,
			"roi_message":                  roiMsg,
		},
	})
}
