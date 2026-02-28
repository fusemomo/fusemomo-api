package v1

import (
	"fmt"
	"fusemomo-api/internal/models/payload"
	"fusemomo-api/internal/utils"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

// ResolveEntitiesHandler godoc
// @Summary      Resolve identifiers to a canonical entity
// @Description  L1 Identity Resolution. Accepts one or more identifier key-value pairs and returns (or creates) the canonical entity. Handles entity merges when multiple entities match different identifiers in the same request.
// @Tags         Core
// @Security     ApiKeyAuth
// @Accept       json
// @Produce      json
// @Param        request body payload.ResolveEntityRequest true "Resolution request"
// @Success      200 {object} payload.ResolveEntityResponse
// @Failure      400 {object} utils.APIError
// @Failure      401 {object} utils.APIError
// @Failure      429 {object} utils.APIError
// @Failure      500 {object} utils.APIError
// @Router       /v1/core/entities/resolve [post]
func (h *Handler) ResolveEntitiesHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Missing tenant context")
	}
	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	// ── 1. Parse & validate request ────────────────────────────────────────
	var req payload.ResolveEntityRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request payload", err.Error())
	}
	if len(req.Identifiers) == 0 {
		return &utils.APIError{
			Code:    fiber.StatusBadRequest,
			Message: "Field 'identifiers' is required and must contain at least one identifier",
		}
	}

	// Per-identifier field validation
	for k, v := range req.Identifiers {
		if len(k) > 100 {
			return &utils.APIError{
				Code:    fiber.StatusBadRequest,
				Message: fmt.Sprintf("Identifier key '%s' exceeds maximum length of 100 characters", k),
			}
		}
		if !isValidIdentifierKey(k) {
			return &utils.APIError{
				Code:    fiber.StatusBadRequest,
				Message: fmt.Sprintf("Identifier key '%s' contains invalid characters (alphanumeric + underscore only)", k),
			}
		}
		if len(v) > 1000 {
			return &utils.APIError{
				Code:    fiber.StatusBadRequest,
				Message: fmt.Sprintf("Identifier value for key '%s' exceeds maximum length of 1000 characters", k),
			}
		}
	}

	// Optional field length validation
	if req.EntityType != nil && len(*req.EntityType) > 100 {
		return utils.BadRequest("Field 'entity_type' exceeds maximum length of 100 characters")
	}
	if req.DisplayName != nil && len(*req.DisplayName) > 500 {
		return utils.BadRequest("Field 'display_name' exceeds maximum length of 500 characters")
	}
	metaSize, err := req.MetadataBytes()
	if err != nil {
		return utils.BadRequest("Field 'metadata' contains invalid JSON", err.Error())
	}
	if metaSize > 10*1024 {
		return utils.BadRequest("Field 'metadata' exceeds maximum size of 10KB")
	}

	ctx := c.Context()

	// ── 2. Rate limit check ─────────────────────────────────────────────────
	// Fetch the tenant's monthly resolution limit and current usage in one query.
	var monthlyLimit int
	var currentUsage int
	rateLimitQuery := `
		SELECT t.monthly_resolution_limit,
		       COALESCE(ul.resolution_count, 0)
		FROM tenants t
		LEFT JOIN usage_logs ul
		       ON ul.tenant_id = t.id
		      AND ul.period_year  = EXTRACT(YEAR  FROM NOW())::SMALLINT
		      AND ul.period_month = EXTRACT(MONTH FROM NOW())::SMALLINT
		WHERE t.id = $1 AND t.deleted_at IS NULL
	`
	if err := h.DB.QueryRow(ctx, rateLimitQuery, tenantID).Scan(&monthlyLimit, &currentUsage); err != nil {
		return utils.InternalServerError("Failed to check rate limit", err.Error())
	}
	// -1 means unlimited (enterprise)
	if monthlyLimit != -1 && currentUsage >= monthlyLimit {
		now := time.Now().UTC()
		// First day of next month at 00:00 UTC
		resetAt := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"error":       "rate_limit_exceeded",
			"message":     "You have exceeded your monthly entity resolution limit",
			"limit":       monthlyLimit,
			"current":     currentUsage,
			"reset_at":    resetAt.Format(time.RFC3339),
			"upgrade_url": "https://app.fusemomo.com/upgrade",
		})
	}

	// ── 3. Resolve within a transaction ────────────────────────────────────
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		return utils.InternalServerError("Failed to start transaction", err.Error())
	}
	defer tx.Rollback(ctx)

	// Build a dynamic OR clause: (source = $n AND identifier_value = $n+1) OR …
	type idPair struct{ source, value string }
	pairs := make([]idPair, 0, len(req.Identifiers))
	for src, val := range req.Identifiers {
		pairs = append(pairs, idPair{src, val})
	}

	orClauses := make([]string, 0, len(pairs))
	lookupArgs := []interface{}{tenantID}
	argPos := 2
	for _, p := range pairs {
		orClauses = append(orClauses,
			fmt.Sprintf("(source = $%d AND identifier_value = $%d)", argPos, argPos+1))
		lookupArgs = append(lookupArgs, p.source, p.value)
		argPos += 2
	}

	lookupSQL := fmt.Sprintf(`
		SELECT DISTINCT ei.entity_id
		FROM entity_identifiers ei
		JOIN entities e ON e.id = ei.entity_id
		WHERE ei.tenant_id = $1
		  AND (%s)
		  AND e.deleted_at IS NULL
	`, strings.Join(orClauses, " OR "))

	rows, err := tx.Query(ctx, lookupSQL, lookupArgs...)
	if err != nil {
		return utils.InternalServerError("Failed to resolve identifiers", err.Error())
	}

	var matchedEntityIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return utils.InternalServerError("Failed to scan entity ID", err.Error())
		}
		matchedEntityIDs = append(matchedEntityIDs, id)
	}
	rows.Close()

	var canonicalID uuid.UUID

	switch len(matchedEntityIDs) {

	// ── Case A: New entity ──────────────────────────────────────────────────
	case 0:
		insertEntitySQL := `
			INSERT INTO entities (tenant_id, display_name, entity_type, metadata)
			VALUES ($1, $2, $3, $4)
			RETURNING id
		`
		metaJSON := req.Metadata
		if metaJSON == nil {
			metaJSON = map[string]any{}
		}
		if err := tx.QueryRow(ctx, insertEntitySQL,
			tenantID, req.DisplayName, req.EntityType, metaJSON,
		).Scan(&canonicalID); err != nil {
			return utils.InternalServerError("Failed to create entity", err.Error())
		}
		// Insert all identifiers
		for _, p := range pairs {
			_, err := tx.Exec(ctx, `
				INSERT INTO entity_identifiers
				  (entity_id, tenant_id, source, identifier_type, identifier_value, confidence, link_strategy)
				VALUES ($1, $2, $3, $4, $5, 1.000, 'deterministic')
				ON CONFLICT (tenant_id, source, identifier_value) DO NOTHING
			`, canonicalID, tenantID, p.source, p.source, p.value)
			if err != nil {
				return utils.InternalServerError("Failed to insert identifier", err.Error())
			}
		}

	// ── Case B: Single match — link any new identifiers ────────────────────
	case 1:
		canonicalID = matchedEntityIDs[0]
		// Upsert any incoming identifiers not yet linked to this entity
		for _, p := range pairs {
			_, err := tx.Exec(ctx, `
				INSERT INTO entity_identifiers
				  (entity_id, tenant_id, source, identifier_type, identifier_value, confidence, link_strategy)
				VALUES ($1, $2, $3, $4, $5, 1.000, 'deterministic')
				ON CONFLICT (tenant_id, source, identifier_value) DO NOTHING
			`, canonicalID, tenantID, p.source, p.source, p.value)
			if err != nil {
				return utils.InternalServerError("Failed to link identifier", err.Error())
			}
		}
		// Merge metadata (new values win for overlapping keys)
		if req.Metadata != nil {
			_, err = tx.Exec(ctx, `
				UPDATE entities SET metadata = metadata || $1::jsonb, updated_at = NOW()
				WHERE id = $2 AND tenant_id = $3
			`, req.Metadata, canonicalID, tenantID)
			if err != nil {
				return utils.InternalServerError("Failed to merge metadata", err.Error())
			}
		}
		// Set display_name only if currently NULL
		if req.DisplayName != nil {
			_, err = tx.Exec(ctx, `
				UPDATE entities SET display_name = $1, updated_at = NOW()
				WHERE id = $2 AND tenant_id = $3 AND display_name IS NULL
			`, *req.DisplayName, canonicalID, tenantID)
			if err != nil {
				return utils.InternalServerError("Failed to update display_name", err.Error())
			}
		}

	// ── Case C: Multiple matches — merge ────────────────────────────────────
	default:
		// Pick the entity with the earliest created_at as canonical
		pickCanonicalSQL := `
			SELECT id FROM entities
			WHERE id = ANY($1) AND tenant_id = $2 AND deleted_at IS NULL
			ORDER BY created_at ASC
			LIMIT 1
		`
		if err := tx.QueryRow(ctx, pickCanonicalSQL, matchedEntityIDs, tenantID).Scan(&canonicalID); err != nil {
			return utils.InternalServerError("Failed to pick canonical entity", err.Error())
		}

		// Merge all other entities into the canonical one
		for _, mergedID := range matchedEntityIDs {
			if mergedID == canonicalID {
				continue
			}
			// Re-point identifiers
			_, err = tx.Exec(ctx, `
				UPDATE entity_identifiers
				SET entity_id = $1
				WHERE entity_id = $2 AND tenant_id = $3
			`, canonicalID, mergedID, tenantID)
			if err != nil {
				return utils.InternalServerError("Failed to relink identifiers during merge", err.Error())
			}
			// Merge metadata from the merged entity
			_, err = tx.Exec(ctx, `
				UPDATE entities SET metadata = (
					SELECT e2.metadata || e1.metadata FROM entities e1, entities e2
					WHERE e1.id = $1 AND e2.id = $2
				), updated_at = NOW()
				WHERE id = $1 AND tenant_id = $3
			`, canonicalID, mergedID, tenantID)
			if err != nil {
				return utils.InternalServerError("Failed to merge entity metadata", err.Error())
			}
			// Record the merge in entity_links
			_, err = tx.Exec(ctx, `
				INSERT INTO entity_links (tenant_id, canonical_entity_id, merged_entity_id, link_reason, link_strategy, confidence, triggered_by)
				VALUES ($1, $2, $3, 'Merged during identity resolution', 'deterministic', 1.000, 'api:resolve')
				ON CONFLICT (tenant_id, canonical_entity_id, merged_entity_id) DO NOTHING
			`, tenantID, canonicalID, mergedID)
			if err != nil {
				return utils.InternalServerError("Failed to record entity link", err.Error())
			}
			// Soft-delete the merged entity
			_, err = tx.Exec(ctx, `
				UPDATE entities SET deleted_at = NOW(), updated_at = NOW()
				WHERE id = $1 AND tenant_id = $2
			`, mergedID, tenantID)
			if err != nil {
				return utils.InternalServerError("Failed to soft-delete merged entity", err.Error())
			}
		}

		// Link any incoming identifiers not yet in the table
		for _, p := range pairs {
			_, err := tx.Exec(ctx, `
				INSERT INTO entity_identifiers
				  (entity_id, tenant_id, source, identifier_type, identifier_value, confidence, link_strategy)
				VALUES ($1, $2, $3, $4, $5, 1.000, 'deterministic')
				ON CONFLICT (tenant_id, source, identifier_value) DO NOTHING
			`, canonicalID, tenantID, p.source, p.source, p.value)
			if err != nil {
				return utils.InternalServerError("Failed to link identifier after merge", err.Error())
			}
		}
		// Merge incoming metadata on top
		if req.Metadata != nil {
			_, err = tx.Exec(ctx, `
				UPDATE entities SET metadata = metadata || $1::jsonb, updated_at = NOW()
				WHERE id = $2 AND tenant_id = $3
			`, req.Metadata, canonicalID, tenantID)
			if err != nil {
				return utils.InternalServerError("Failed to merge metadata after entity merge", err.Error())
			}
		}
	}

	// ── 4. Increment usage counter ──────────────────────────────────────────
	_, err = tx.Exec(ctx, `SELECT fn_increment_usage($1, 'resolution')`, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to increment usage", err.Error())
	}

	if err := tx.Commit(ctx); err != nil {
		return utils.InternalServerError("Failed to commit transaction", err.Error())
	}

	// ── 5. Fetch resolved entity and return ────────────────────────────────
	var resp payload.ResolveEntityResponse
	entityQuery := `
		SELECT id, display_name, entity_type, total_interactions, successful_interactions,
		       last_interaction_at, preferred_action_type, behavioral_score, metadata, created_at
		FROM entities
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
	`
	if err := h.DB.QueryRow(ctx, entityQuery, canonicalID, tenantID).Scan(
		&resp.EntityID, &resp.DisplayName, &resp.EntityType,
		&resp.TotalInteractions, &resp.SuccessfulInteractions,
		&resp.LastInteractionAt, &resp.PreferredActionType, &resp.BehavioralScore,
		&resp.Metadata, &resp.CreatedAt,
	); err != nil {
		return utils.InternalServerError("Failed to fetch resolved entity", err.Error())
	}

	resp.Identifiers = make([]payload.EntityIdentifier, 0)
	idRows, err := h.DB.Query(ctx, `
		SELECT id, source, identifier_type, identifier_value, confidence, link_strategy, verified_at
		FROM entity_identifiers
		WHERE entity_id = $1 AND tenant_id = $2
	`, canonicalID, tenantID)
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

	return c.JSON(resp)
}

// isValidIdentifierKey returns true when k consists only of alphanumeric characters and underscores.
func isValidIdentifierKey(k string) bool {
	for _, r := range k {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return len(k) > 0
}

// LinkEntityManuallyHandler godoc
// @Summary      Manually link identifiers to an entity
// @Description  Adds one or more new identifiers to an existing entity. Returns 409 if any identifier already belongs to a different entity, so the caller can decide whether to merge or reject.
// @Tags         Core
// @Security     ApiKeyAuth
// @Accept       json
// @Produce      json
// @Param        id   path     string                         true  "Entity UUID"
// @Param        request body payload.LinkIdentifiersRequest true  "Link request"
// @Success      200  {object} payload.LinkIdentifiersResponse
// @Failure      400  {object} utils.APIError
// @Failure      401  {object} utils.APIError
// @Failure      404  {object} utils.APIError
// @Failure      409  {object} utils.APIError
// @Failure      500  {object} utils.APIError
// @Router       /v1/core/entities/{id}/link [post]
func (h *Handler) LinkEntityManuallyHandler(c fiber.Ctx) error {
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
		return utils.BadRequest("Entity ID is required")
	}
	entityID, err := uuid.Parse(entityIDParam)
	if err != nil {
		return utils.BadRequest("Invalid Entity ID format", err.Error())
	}

	// ── 1. Parse & validate request ────────────────────────────────────────
	var req payload.LinkIdentifiersRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request payload", err.Error())
	}
	if len(req.Identifiers) == 0 {
		return &utils.APIError{
			Code:    fiber.StatusBadRequest,
			Message: "Field 'identifiers' is required and must contain at least one identifier",
		}
	}
	for k, v := range req.Identifiers {
		if len(k) > 100 || !isValidIdentifierKey(k) {
			return &utils.APIError{
				Code:    fiber.StatusBadRequest,
				Message: fmt.Sprintf("Identifier key '%s' is invalid (max 100 chars, alphanumeric + underscore)", k),
			}
		}
		if len(v) > 1000 {
			return &utils.APIError{
				Code:    fiber.StatusBadRequest,
				Message: fmt.Sprintf("Identifier value for key '%s' exceeds maximum length of 1000 characters", k),
			}
		}
	}

	// Validate link_strategy
	linkStrategy := "deterministic"
	if req.LinkStrategy != nil {
		switch *req.LinkStrategy {
		case "deterministic", "probabilistic":
			linkStrategy = *req.LinkStrategy
		default:
			return &utils.APIError{
				Code:    fiber.StatusBadRequest,
				Message: "Field 'link_strategy' must be 'deterministic' or 'probabilistic'",
			}
		}
	}

	// Validate confidence
	confidence := 1.0
	if req.Confidence != nil {
		if *req.Confidence < 0.0 || *req.Confidence > 1.0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error":          "validation_error",
				"message":        "Field 'confidence' must be between 0.0 and 1.0",
				"field":          "confidence",
				"provided_value": *req.Confidence,
			})
		}
		confidence = *req.Confidence
	}

	ctx := c.Context()

	// ── 2. Verify entity exists and belongs to tenant ──────────────────────
	var exists bool
	err = h.DB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM entities
			WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
		)
	`, entityID, tenantID).Scan(&exists)
	if err != nil {
		return utils.InternalServerError("Failed to check entity", err.Error())
	}
	if !exists {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error":     "not_found",
			"message":   "Entity not found or does not belong to your account",
			"entity_id": entityID.String(),
		})
	}

	// ── 3. Check for conflicts on OTHER entities ───────────────────────────
	inValues := make([]string, 0, len(req.Identifiers))
	for _, v := range req.Identifiers {
		inValues = append(inValues, v)
	}

	// Build $2, $3, ... placeholders
	placeholders := make([]string, len(inValues))
	conflictArgs := []interface{}{tenantID, entityID}
	for i, v := range inValues {
		placeholders[i] = fmt.Sprintf("$%d", i+3)
		conflictArgs = append(conflictArgs, v)
	}

	conflictSQL := fmt.Sprintf(`
		SELECT entity_id, identifier_value
		FROM entity_identifiers
		WHERE tenant_id = $1
		  AND entity_id != $2
		  AND identifier_value IN (%s)
	`, strings.Join(placeholders, ","))

	conflictRows, err := h.DB.Query(ctx, conflictSQL, conflictArgs...)
	if err != nil {
		return utils.InternalServerError("Failed to check identifier conflicts", err.Error())
	}
	defer conflictRows.Close()

	var conflicts []payload.IdentifierConflict
	for conflictRows.Next() {
		var existingEntityID uuid.UUID
		var identifierValue string
		if err := conflictRows.Scan(&existingEntityID, &identifierValue); err != nil {
			return utils.InternalServerError("Failed to scan conflict row", err.Error())
		}
		conflicts = append(conflicts, payload.IdentifierConflict{
			IdentifierValue:  identifierValue,
			ExistingEntityID: existingEntityID.String(),
			ActionRequired:   "merge_entities_or_reject",
		})
	}
	conflictRows.Close()

	if len(conflicts) > 0 {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":     "identifier_conflict",
			"message":   "One or more identifiers are already linked to a different entity",
			"conflicts": conflicts,
		})
	}

	// ── 4. Insert new identifiers (skip already-linked ones) ──────────────
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		return utils.InternalServerError("Failed to start transaction", err.Error())
	}
	defer tx.Rollback(ctx)

	linkedCount := 0
	for src, val := range req.Identifiers {
		tag, err := tx.Exec(ctx, `
			INSERT INTO entity_identifiers
			  (entity_id, tenant_id, source, identifier_type, identifier_value, confidence, link_strategy)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (tenant_id, source, identifier_value) DO NOTHING
		`, entityID, tenantID, src, src, val, confidence, linkStrategy)
		if err != nil {
			return utils.InternalServerError("Failed to insert identifier", err.Error())
		}
		linkedCount += int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return utils.InternalServerError("Failed to commit transaction", err.Error())
	}

	// ── 5. Fetch all identifiers and return ───────────────────────────────
	idRows, err := h.DB.Query(ctx, `
		SELECT id, source, identifier_type, identifier_value, confidence, link_strategy, verified_at
		FROM entity_identifiers
		WHERE entity_id = $1 AND tenant_id = $2
	`, entityID, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to fetch entity identifiers", err.Error())
	}
	defer idRows.Close()

	identifiers := make([]payload.EntityIdentifier, 0)
	for idRows.Next() {
		var ei payload.EntityIdentifier
		if err := idRows.Scan(&ei.ID, &ei.Source, &ei.IdentifierType, &ei.IdentifierValue, &ei.Confidence, &ei.LinkStrategy, &ei.VerifiedAt); err != nil {
			return utils.InternalServerError("Failed to scan identifier", err.Error())
		}
		identifiers = append(identifiers, ei)
	}

	return c.JSON(payload.LinkIdentifiersResponse{
		EntityID:    entityID.String(),
		Identifiers: identifiers,
		LinkedCount: linkedCount,
	})
}

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

// DeleteEntityHandler godoc
// @Summary Anonymize an entity (GDPR erasure)
// @Description Irreversibly redacts an entity's PII, identifiers, and metadata, leaving only anonymized behavioral records for analytics.
// @Tags Core
// @Security ApiKeyAuth
// @Produce json
// @Param id path string true "Entity ID"
// @Success 200 {object} payload.EntityDeleteResponse
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 404 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /v1/entities/{id} [delete]
func (h *Handler) DeleteEntityHandler(c fiber.Ctx) error {
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

	// Call the Postgres function for GDPR anonymization
	query := `SELECT fn_anonymize_entity($1, $2)`
	_, err = h.DB.Exec(c.Context(), query, tenantID, entityID)

	if err != nil {
		// PostgreSQL RAISE EXCEPTION 'Entity % not found for tenant %'
		if strings.Contains(err.Error(), "not found") {
			return utils.NotFound("Entity not found")
		}
		return utils.InternalServerError("Failed to anonymize entity", err.Error())
	}

	return c.JSON(payload.EntityDeleteResponse{
		EntityID:   entityID.String(),
		Anonymized: true,
		ErasedAt:   time.Now().UTC(),
	})
}

// LogInteractionHandler godoc
// @Summary Log a single interaction
// @Description Logs a behavioral event/interaction for a given entity and increments the tenant's interaction usage counter.
// @Tags Core
// @Security ApiKeyAuth
// @Accept json
// @Produce json
// @Param request body payload.InteractionLogRequest true "Interaction RequestPayload"
// @Success 200 {object} payload.InteractionLogResponse
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /v1/interactions/log [post]
func (h *Handler) LogInteractionHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Missing tenant context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	var req payload.InteractionLogRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request payload", err.Error())
	}

	if err := utils.Validator.Struct(&req); err != nil {
		return utils.BadRequest("Validation failed", err.Error())
	}

	occurredAt := time.Now().UTC()
	if req.OccurredAt != nil {
		occurredAt = *req.OccurredAt
	}

	ctx := c.Context()
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		return utils.InternalServerError("Failed to start transaction", err.Error())
	}
	defer tx.Rollback(ctx)

	// 1. Insert Interaction
	var interactionID string
	insertQuery := `
		INSERT INTO interactions (tenant_id, entity_id, api, action_type, action, outcome, intent, agent_id, external_ref, metadata, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id
	`
	err = tx.QueryRow(ctx, insertQuery,
		tenantID, req.EntityID, req.API, req.ActionType, req.Action,
		req.Outcome, req.Intent, req.AgentID, req.ExternalRef, req.Metadata, occurredAt,
	).Scan(&interactionID)

	if err != nil {
		return utils.InternalServerError("Failed to log interaction", err.Error())
	}

	// 2. Increment Usage Counter
	_, err = tx.Exec(ctx, `SELECT fn_increment_usage($1, 'interaction')`, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to increment usage", err.Error())
	}

	if err := tx.Commit(ctx); err != nil {
		return utils.InternalServerError("Failed to commit transaction", err.Error())
	}

	return c.JSON(payload.InteractionLogResponse{
		InteractionID: interactionID,
		LoggedAt:      time.Now().UTC(),
	})
}

// LogBatchInteractionsHandler godoc
// @Summary Log multiple interactions
// @Description Logs a batch of behavioral events using a multi-row insert and increments usage counters. Max 100 per request.
// @Tags Core
// @Security ApiKeyAuth
// @Accept json
// @Produce json
// @Param request body payload.BatchInteractionLogRequest true "Batch Interaction payload"
// @Success 200 {object} payload.BatchInteractionLogResponse
// @Failure 400 {object} utils.APIError
// @Failure 401 {object} utils.APIError
// @Failure 500 {object} utils.APIError
// @Router /v1/interactions/batch [post]
func (h *Handler) LogBatchInteractionsHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Missing tenant context")
	}

	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	var req payload.BatchInteractionLogRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request payload", err.Error())
	}

	if err := utils.Validator.Struct(&req); err != nil {
		return utils.BadRequest("Validation failed", err.Error())
	}

	if len(req.Interactions) == 0 {
		return utils.BadRequest("Empty interactions list", "")
	}

	ctx := c.Context()
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		return utils.InternalServerError("Failed to start transaction", err.Error())
	}
	defer tx.Rollback(ctx)

	// Build multi-row INSERT query
	var valueStrings []string
	var valueArgs []interface{}
	argID := 1

	for _, item := range req.Interactions {
		occurredAt := time.Now().UTC()
		if item.OccurredAt != nil {
			occurredAt = *item.OccurredAt
		}

		valueStrings = append(valueStrings, fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			argID, argID+1, argID+2, argID+3, argID+4, argID+5, argID+6, argID+7, argID+8, argID+9, argID+10))

		valueArgs = append(valueArgs,
			tenantID, item.EntityID, item.API, item.ActionType, item.Action,
			item.Outcome, item.Intent, item.AgentID, item.ExternalRef, item.Metadata, occurredAt,
		)
		argID += 11
	}

	insertQuery := fmt.Sprintf(`
		INSERT INTO interactions (tenant_id, entity_id, api, action_type, action, outcome, intent, agent_id, external_ref, metadata, occurred_at)
		VALUES %s
		RETURNING id
	`, strings.Join(valueStrings, ","))

	rows, err := tx.Query(ctx, insertQuery, valueArgs...)
	if err != nil {
		return utils.InternalServerError("Failed to log batch interactions", err.Error())
	}
	defer rows.Close()

	var insertedIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return utils.InternalServerError("Failed to scan returning ID", err.Error())
		}
		insertedIDs = append(insertedIDs, id)
	}

	// Increment usage for the batch size
	// Note: We run the increment N times rather than modifying the Postgres function.
	for i := 0; i < len(req.Interactions); i++ {
		_, err = tx.Exec(ctx, `SELECT fn_increment_usage($1, 'interaction')`, tenantID)
		if err != nil {
			return utils.InternalServerError("Failed to increment usage", err.Error())
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return utils.InternalServerError("Failed to commit transaction", err.Error())
	}

	firstID := ""
	lastID := ""
	if len(insertedIDs) > 0 {
		firstID = insertedIDs[0]
		lastID = insertedIDs[len(insertedIDs)-1]
	}

	return c.JSON(payload.BatchInteractionLogResponse{
		LoggedCount: len(req.Interactions),
		FirstID:     firstID,
		LastID:      lastID,
	})
}
