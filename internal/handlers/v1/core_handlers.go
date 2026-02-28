package v1

import (
	"fmt"
	"fusemomo-api/internal/models/payload"
	"fusemomo-api/internal/utils"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

// GetAllEntitiesHandler godoc
// @Summary List all entities
// @Description Returns a paginated list of entities matching the filters
// @Tags Core
// @Security ApiKeyAuth
// @Produce json
// @Param limit query int false "Results per page (max 100)" default(50)
// @Param offset query int false "Pagination offset" default(0)
// @Param entity_type query string false "Filter by entity type"
// @Param source query string false "Filter by API source"
// @Param min_score query number false "Filter by behavioral score"
// @Param sort query string false "Sort field (default: last_interaction_at)"
// @Param order query string false "Sort order (asc/desc)" default(desc)
// @Success 200 {object} payload.EntitiesListResponse
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /v1/entities [get]
func (h *Handler) GetAllEntitiesHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Missing tenant context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	// 1. Extract and validate parameters
	limit := 50
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed >= 1 && parsed <= 100 {
			limit = parsed
		} else if parsed > 100 {
			limit = 100
		}
	}

	offset := 0
	if o := c.Query("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	entityType := c.Query("entity_type")
	source := c.Query("source")
	minScoreStr := c.Query("min_score")
	sortField := c.Query("sort", "last_interaction_at")
	sortOrder := strings.ToLower(c.Query("order", "desc"))

	var minScore float64
	var hasMinScore bool
	if minScoreStr != "" {
		if parsed, err := strconv.ParseFloat(minScoreStr, 64); err == nil {
			minScore = parsed
			hasMinScore = true
		}
	}

	// Validate sort field to prevent SQL injection
	allowedSorts := map[string]string{
		"created_at":              "e.created_at",
		"updated_at":              "e.updated_at",
		"last_interaction_at":     "e.last_interaction_at",
		"total_interactions":      "e.total_interactions",
		"successful_interactions": "e.successful_interactions",
		"behavioral_score":        "e.behavioral_score",
	}

	dbSortField, ok := allowedSorts[sortField]
	if !ok {
		dbSortField = "e.last_interaction_at"
	}

	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "desc"
	}

	// 2. Build Query
	whereClauses := []string{"e.tenant_id = $1", "e.deleted_at IS NULL"}
	args := []interface{}{tenantID}
	argID := 2

	if entityType != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("e.entity_type = $%d", argID))
		args = append(args, entityType)
		argID++
	}

	if hasMinScore {
		whereClauses = append(whereClauses, fmt.Sprintf("e.behavioral_score >= $%d", argID))
		args = append(args, minScore)
		argID++
	}

	if source != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("EXISTS (SELECT 1 FROM entity_identifiers ei WHERE ei.entity_id = e.id AND ei.source = $%d)", argID))
		args = append(args, source)
		argID++
	}

	whereClause := "WHERE " + strings.Join(whereClauses, " AND ")

	// 3. Get Total Count
	countQuery := fmt.Sprintf("SELECT COUNT(e.id) FROM entities e %s", whereClause)
	var total int
	if err := h.DB.QueryRow(c.Context(), countQuery, args...).Scan(&total); err != nil {
		return utils.InternalServerError("Failed to get total count", err.Error())
	}

	// 4. Fetch Entities
	// NULLS LAST handles the case where last_interaction_at is null
	query := fmt.Sprintf(`
		SELECT e.id, e.tenant_id, e.display_name, e.entity_type, e.total_interactions, 
		       e.successful_interactions, e.last_interaction_at, e.preferred_action_type, 
		       e.behavioral_score, e.metadata, e.created_at, e.updated_at
		FROM entities e
		%s
		ORDER BY %s %s NULLS LAST
		LIMIT $%d OFFSET $%d
	`, whereClause, dbSortField, sortOrder, argID, argID+1)

	fetchArgs := append(args, limit, offset)

	rows, err := h.DB.Query(c.Context(), query, fetchArgs...)
	if err != nil {
		return utils.InternalServerError("Failed to query entities", err.Error())
	}
	defer rows.Close()

	var entities []payload.EntityResponse

	for rows.Next() {
		var e payload.EntityResponse
		// Create pointers for nullable types
		var displayName, entityType, preferredAction *string

		if err := rows.Scan(
			&e.ID, &e.TenantID, &displayName, &entityType, &e.TotalInteractions,
			&e.SuccessfulInteractions, &e.LastInteractionAt, &preferredAction,
			&e.BehavioralScore, &e.Metadata, &e.CreatedAt, &e.UpdatedAt,
		); err != nil {
			return utils.InternalServerError("Failed to scan entity", err.Error())
		}

		if displayName != nil {
			e.DisplayName = *displayName
		}
		if entityType != nil {
			e.EntityType = *entityType
		}
		if preferredAction != nil {
			e.PreferredActionType = *preferredAction
		}

		entities = append(entities, e)
	}

	if entities == nil {
		entities = []payload.EntityResponse{}
	}

	return c.JSON(payload.EntitiesListResponse{
		Entities: entities,
		Total:    total,
		Limit:    limit,
		Offset:   offset,
	})
}

// GetEntityHandler godoc
// @Summary Get a specific entity
// @Description Returns the canonical entity details along with its graph identifiers and recent behavior log
// @Tags Core
// @Security ApiKeyAuth
// @Produce json
// @Param id path string true "Entity ID"
// @Success 200 {object} payload.EntityDetailResponse
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 404 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /v1/entities/{id} [get]
func (h *Handler) GetEntityHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Missing tenant context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	entityIDParam := c.Params("id")
	if entityIDParam == "" {
		return utils.BadRequest("Entity ID is required", "")
	}

	entityID, err := uuid.Parse(entityIDParam)
	if err != nil {
		return utils.BadRequest("Invalid Entity ID format", err.Error())
	}

	ctx := c.Context()

	// 1. Fetch Core Entity Data
	var resp payload.EntityDetailResponse
	var displayName, entityType, preferredAction *string

	entityQuery := `
		SELECT id, tenant_id, display_name, entity_type, total_interactions, 
		       successful_interactions, last_interaction_at, preferred_action_type, 
		       behavioral_score, metadata, created_at, updated_at
		FROM entities
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
	`
	err = h.DB.QueryRow(ctx, entityQuery, entityID, tenantID).Scan(
		&resp.ID, &resp.TenantID, &displayName, &entityType, &resp.TotalInteractions,
		&resp.SuccessfulInteractions, &resp.LastInteractionAt, &preferredAction,
		&resp.BehavioralScore, &resp.Metadata, &resp.CreatedAt, &resp.UpdatedAt,
	)

	if err != nil {
		// pgx v5 returns pgx.ErrNoRows which gets generalized; we'll rely on string matching if we don't import pgx
		if strings.Contains(err.Error(), "no rows") {
			return utils.NotFound("Entity not found")
		}
		return utils.InternalServerError("Failed to fetch entity", err.Error())
	}

	if displayName != nil {
		resp.DisplayName = *displayName
	}
	if entityType != nil {
		resp.EntityType = *entityType
	}
	if preferredAction != nil {
		resp.PreferredActionType = *preferredAction
	}

	// 2. Fetch Entity Identifiers
	resp.Identifiers = make([]payload.EntityIdentifier, 0)
	idQuery := `
		SELECT id, source, identifier_type, identifier_value, confidence, link_strategy, verified_at
		FROM entity_identifiers
		WHERE entity_id = $1 AND tenant_id = $2
	`
	idRows, err := h.DB.Query(ctx, idQuery, entityID, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to fetch entity identifiers", err.Error())
	}
	defer idRows.Close()

	for idRows.Next() {
		var ei payload.EntityIdentifier
		if err := idRows.Scan(&ei.ID, &ei.Source, &ei.IdentifierType, &ei.IdentifierValue, &ei.Confidence, &ei.LinkStrategy, &ei.VerifiedAt); err != nil {
			return utils.InternalServerError("Failed to scan identifier", err.Error())
		}
		resp.Identifiers = append(resp.Identifiers, ei)
	}

	// 3. Fetch Recent Interactions (Limit 20)
	resp.Interactions = make([]payload.InteractionSummary, 0)
	interactionQuery := `
		SELECT id, api, action_type, outcome, occurred_at
		FROM interactions
		WHERE entity_id = $1 AND tenant_id = $2
		ORDER BY occurred_at DESC
		LIMIT 20
	`
	intRows, err := h.DB.Query(ctx, interactionQuery, entityID, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to fetch recent interactions", err.Error())
	}
	defer intRows.Close()

	for intRows.Next() {
		var is payload.InteractionSummary
		if err := intRows.Scan(&is.ID, &is.API, &is.ActionType, &is.Outcome, &is.OccurredAt); err != nil {
			return utils.InternalServerError("Failed to scan interaction", err.Error())
		}
		resp.Interactions = append(resp.Interactions, is)
	}

	return c.JSON(resp)
}

func (h *Handler) DeleteEntityHandler(c fiber.Ctx) error {
	return nil
}
