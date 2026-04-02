package v1

import (
	"fmt"
	"fusemomo-api/internal/models/payload"
	"fusemomo-api/internal/utils"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

// GetAdminTenantsHandler godoc
// @Summary List all tenants (admin view)
// @Description Returns all tenant records with current month usage stats and pagination support
// @Tags Admin
// @Produce json
// @Param plan query string false "Filter by plan tier (free, builder, enterprise)"
// @Param limit query int false "Pagination limit" default(50)
// @Param offset query int false "Pagination offset" default(0)
// @Success 200 {object} payload.AdminTenantsResponse
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 403 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Security     SessionAuth
// @Router /v1/admin/tenants [get]
func (h *Handler) GetAdminTenantsHandler(c fiber.Ctx) error {
	// Query parameters
	planFilter := c.Query("plan")

	limit := 50
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil {
			limit = parsed
		}
	}

	offset := 0
	if o := c.Query("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil {
			offset = parsed
		}
	}

	if limit < 1 || limit > 100 {
		return utils.BadRequest("limit must be between 1 and 100")
	}
	if offset < 0 {
		return utils.BadRequest("offset cannot be negative")
	}

	now := time.Now().UTC()
	currentYear := now.Year()
	currentMonth := int(now.Month())

	// 1. Build the query dynamically based on the plan filter
	whereClause := ""
	var args []interface{}
	args = append(args, currentYear, currentMonth)

	if planFilter != "" {
		if planFilter != "free" && planFilter != "builder" && planFilter != "enterprise" {
			return utils.BadRequest("Invalid plan filter. Use free, builder, or enterprise")
		}
		whereClause = "WHERE t.plan = $3"
		args = append(args, planFilter)
	}

	// 2. Query total count first (requires a separate query since we paginate)
	var countQuery string
	if whereClause == "" {
		countQuery = `SELECT COUNT(id) FROM tenants`
		// No args needed for count query if no plan filter
	} else {
		countQuery = fmt.Sprintf(`SELECT COUNT(id) FROM tenants t %s`, whereClause)
	}

	var totalCount int
	var err error
	if whereClause == "" {
		err = h.DB.QueryRow(c.Context(), countQuery).Scan(&totalCount)
	} else {
		err = h.DB.QueryRow(c.Context(), countQuery, planFilter).Scan(&totalCount)
	}

	if err != nil {
		return utils.InternalServerError("Failed to get total tenants count", err.Error())
	}

	// 3. Query tenants with usage LEFT JOIN
	var query string

	if planFilter == "" {
		query = `
			SELECT 
				t.id, t.name, t.email, t.plan, t.created_at, 
				u.resolution_count, u.interaction_count
			FROM tenants t
			LEFT JOIN usage_logs u 
				ON t.id = u.tenant_id AND u.period_year = $1 AND u.period_month = $2
			ORDER BY t.created_at DESC
			LIMIT $3 OFFSET $4
		`
		args = append(args, limit, offset)
	} else {
		query = `
			SELECT 
				t.id, t.name, t.email, t.plan, t.created_at, 
				u.resolution_count, u.interaction_count
			FROM tenants t
			LEFT JOIN usage_logs u 
				ON t.id = u.tenant_id AND u.period_year = $1 AND u.period_month = $2
			WHERE t.plan = $3
			ORDER BY t.created_at DESC
			LIMIT $4 OFFSET $5
		`
		args = append(args, limit, offset) // at this point args already has the planFilter at index 2
	}

	rows, err := h.DB.Query(c.Context(), query, args...)
	if err != nil {
		return utils.InternalServerError("Failed to fetch tenants", err.Error())
	}
	defer rows.Close()

	var tenants []payload.AdminTenantInfo

	for rows.Next() {
		var tenant payload.AdminTenantInfo
		var resCount, intCount *int

		err := rows.Scan(
			&tenant.ID,
			&tenant.Name,
			&tenant.Email,
			&tenant.Plan,
			&tenant.CreatedAt,
			&resCount,
			&intCount,
		)
		if err != nil {
			return utils.InternalServerError("Failed to scan tenant data", err.Error())
		}

		if resCount != nil {
			tenant.Usage.Resolutions = *resCount
		}
		if intCount != nil {
			tenant.Usage.Interactions = *intCount
		}

		tenants = append(tenants, tenant)
	}

	if tenants == nil {
		tenants = []payload.AdminTenantInfo{}
	}

	return c.JSON(payload.AdminTenantsResponse{
		Tenants: tenants,
		Total:   totalCount,
	})
}

// UpdateAdminTenantPlanHandler godoc
// @Summary Manually change a tenant's plan
// @Description Updates a tenant's plan tier and limits directly in the database (admin override)
// @Tags Admin
// @Accept json
// @Produce json
// @Param id path string true "Tenant ID"
// @Param request body payload.UpdateTenantPlanRequest true "Plan Update Request"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 404 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Security     SessionAuth
// @Router /v1/admin/tenants/{id}/plan [patch]
func (h *Handler) UpdateAdminTenantPlanHandler(c fiber.Ctx) error {
	tenantIDParam := c.Params("id")
	if tenantIDParam == "" {
		return utils.BadRequest("Tenant ID is required", "")
	}

	if _, err := uuid.Parse(tenantIDParam); err != nil {
		return utils.BadRequest("Invalid Tenant ID format", err.Error())
	}

	var req payload.UpdateTenantPlanRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request payload", err.Error())
	}

	if err := utils.Validator.Struct(&req); err != nil {
		return utils.BadRequest("Validation failed", err.Error())
	}

	query := `
		UPDATE tenants
		SET plan = $1, monthly_resolution_limit = $2, updated_at = NOW()
		WHERE id = $3
	`

	result, err := h.DB.Exec(c.Context(), query, req.Plan, req.MonthlyResolutionLimit, tenantIDParam)
	if err != nil {
		return utils.InternalServerError("Failed to update tenant plan", err.Error())
	}

	if result.RowsAffected() == 0 {
		return utils.NotFound("Tenant not found")
	}

	return c.JSON(fiber.Map{
		"updated":   true,
		"tenant_id": tenantIDParam,
		"new_plan":  req.Plan,
	})
}

// GetGlobalTenantUsagesHandler godoc
// @Summary Global usage stats
// @Description Aggregates usage metrics across all tenants for platform monitoring
// @Tags Admin
// @Produce json
// @Success 200 {object} payload.GlobalUsageResponse
// @Failure 401 {object} utils.APIError
// @Failure 403 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Security     SessionAuth
// @Router /v1/admin/usage/global [get]
func (h *Handler) GetGlobalTenantUsagesHandler(c fiber.Ctx) error {
	var resp payload.GlobalUsageResponse
	resp.ByPlan = make(map[string]int)

	ctx := c.Context()
	now := time.Now().UTC()
	currentYear := now.Year()
	currentMonth := int(now.Month())

	// 1. Total Tenants
	if err := h.DB.QueryRow(ctx, "SELECT COUNT(id) FROM tenants").Scan(&resp.TotalTenants); err != nil {
		return utils.InternalServerError("Failed to count total tenants", err.Error())
	}

	// 2. Total Entities
	if err := h.DB.QueryRow(ctx, "SELECT COUNT(id) FROM entities").Scan(&resp.TotalEntities); err != nil {
		return utils.InternalServerError("Failed to count total entities", err.Error())
	}

	// 3. Total Interactions (All time)
	var totalInteractions *int
	if err := h.DB.QueryRow(ctx, "SELECT SUM(interaction_count) FROM usage_logs").Scan(&totalInteractions); err != nil {
		return utils.InternalServerError("Failed to sum interactions", err.Error())
	}
	if totalInteractions != nil {
		resp.TotalInteractions = *totalInteractions
	}

	// 4. Total Resolutions (This Month)
	var totalResolutions *int
	if err := h.DB.QueryRow(ctx, "SELECT SUM(resolution_count) FROM usage_logs WHERE period_year = $1 AND period_month = $2", currentYear, currentMonth).Scan(&totalResolutions); err != nil {
		return utils.InternalServerError("Failed to sum daily resolutions", err.Error())
	}
	if totalResolutions != nil {
		resp.TotalResolutionsThisMonth = *totalResolutions
	}

	// 5. By Plan Grouping
	rows, err := h.DB.Query(ctx, "SELECT plan, COUNT(id) FROM tenants GROUP BY plan")
	if err != nil {
		return utils.InternalServerError("Failed to group tenants by plan", err.Error())
	}
	defer rows.Close()

	for rows.Next() {
		var plan string
		var count int
		if err := rows.Scan(&plan, &count); err != nil {
			return utils.InternalServerError("Failed to scan plan groups", err.Error())
		}
		resp.ByPlan[plan] = count
	}

	return c.JSON(resp)
}

// DeleteTenantHandler godoc
// @Summary Delete a tenant
// @Description Hard delete a tenant and all its associated data (admin only)
// @Tags Admin
// @Param id path string true "Tenant ID"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 403 {object} utils.APIError
// @Failure 404 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Security     SessionAuth
// @Router /v1/admin/tenants/{id} [delete]
func (h *Handler) DeleteTenantHandler(c fiber.Ctx) error {
	tenantID := c.Params("id")
	if tenantID == "" {
		return utils.BadRequest("Tenant ID is required", "")
	}

	if _, err := uuid.Parse(tenantID); err != nil {
		return utils.BadRequest("Invalid Tenant ID format", err.Error())
	}

	query := `
		DELETE FROM tenants
		WHERE id = $1
	`

	result, err := h.DB.Exec(c.Context(), query, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to delete tenant", err.Error())
	}

	if result.RowsAffected() == 0 {
		return utils.NotFound("Tenant not found")
	}

	return c.JSON(fiber.Map{
		"deleted":   true,
		"tenant_id": tenantID,
	})
}
