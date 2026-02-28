package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fusemomo-api/internal/models/payload"
	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// errEntityNotFound is a sentinel used to distinguish 404 from 5xx inside errgroups.
var errEntityNotFound = errors.New("entity_not_found")

// allowedSorts maps user-supplied sort keys to safe, qualified SQL column expressions.
var allowedSorts = map[string]string{
	"created_at":              "e.created_at",
	"updated_at":              "e.updated_at",
	"last_interaction_at":     "e.last_interaction_at",
	"total_interactions":      "e.total_interactions",
	"successful_interactions": "e.successful_interactions",
	"behavioral_score":        "e.behavioral_score",
}

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

	ctx := c.Context()
	var total int
	var entities []payload.EntityResponse

	g, gCtx := errgroup.WithContext(ctx)

	// 3. Get Total Count Concurrently
	g.Go(func() error {
		countQuery := fmt.Sprintf("SELECT COUNT(e.id) FROM entities e %s", whereClause)
		if err := h.DB.QueryRow(gCtx, countQuery, args...).Scan(&total); err != nil {
			return fmt.Errorf("count_query: %w", err)
		}
		return nil
	})

	// 4. Fetch Entities Concurrently
	g.Go(func() error {
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
		rows, err := h.DB.Query(gCtx, query, fetchArgs...)
		if err != nil {
			return fmt.Errorf("fetch_query: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var e payload.EntityResponse
			var displayName, entityType, preferredAction *string

			if err := rows.Scan(
				&e.ID, &e.TenantID, &displayName, &entityType, &e.TotalInteractions,
				&e.SuccessfulInteractions, &e.LastInteractionAt, &preferredAction,
				&e.BehavioralScore, &e.Metadata, &e.CreatedAt, &e.UpdatedAt,
			); err != nil {
				return fmt.Errorf("scan_row: %w", err)
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
		return nil
	})

	if err := g.Wait(); err != nil {
		return utils.InternalServerError("Failed to fetch entities", err.Error())
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

	var resp payload.EntityDetailResponse
	var identifiers []payload.EntityIdentifier
	var interactions []payload.InteractionSummary

	// ── Step 1: Fetch core entity row first ──────────────────────────────
	// We gate on entity existence before spawning any further DB work so that
	// a 404 path never wastes two extra connection-pool checkouts.
	{
		var displayName, entityType, preferredAction *string
		entityQuery := `
			SELECT id, tenant_id, display_name, entity_type, total_interactions,
			       successful_interactions, last_interaction_at, preferred_action_type,
			       behavioral_score, metadata, created_at, updated_at
			FROM entities
			WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
		`
		if err := h.DB.QueryRow(ctx, entityQuery, entityID, tenantID).Scan(
			&resp.ID, &resp.TenantID, &displayName, &entityType, &resp.TotalInteractions,
			&resp.SuccessfulInteractions, &resp.LastInteractionAt, &preferredAction,
			&resp.BehavioralScore, &resp.Metadata, &resp.CreatedAt, &resp.UpdatedAt,
		); err != nil {
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
	}

	// ── Step 2: Concurrently fetch identifiers + recent interactions ──────
	// Entity is confirmed to exist — safe to issue both sub-queries in parallel.
	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		idQuery := `
			SELECT id, source, identifier_type, identifier_value, confidence, link_strategy, verified_at
			FROM entity_identifiers
			WHERE entity_id = $1 AND tenant_id = $2
		`
		idRows, err := h.DB.Query(gCtx, idQuery, entityID, tenantID)
		if err != nil {
			return fmt.Errorf("identifiers_query: %w", err)
		}
		defer idRows.Close()

		for idRows.Next() {
			var ei payload.EntityIdentifier
			if err := idRows.Scan(&ei.ID, &ei.Source, &ei.IdentifierType, &ei.IdentifierValue, &ei.Confidence, &ei.LinkStrategy, &ei.VerifiedAt); err != nil {
				return fmt.Errorf("identifiers_scan: %w", err)
			}
			identifiers = append(identifiers, ei)
		}
		return nil
	})

	g.Go(func() error {
		interactionQuery := `
			SELECT id, api, action_type, outcome, occurred_at
			FROM interactions
			WHERE entity_id = $1 AND tenant_id = $2
			ORDER BY occurred_at DESC
			LIMIT 20
		`
		intRows, err := h.DB.Query(gCtx, interactionQuery, entityID, tenantID)
		if err != nil {
			return fmt.Errorf("interactions_query: %w", err)
		}
		defer intRows.Close()

		for intRows.Next() {
			var is payload.InteractionSummary
			if err := intRows.Scan(&is.ID, &is.API, &is.ActionType, &is.Outcome, &is.OccurredAt); err != nil {
				return fmt.Errorf("interactions_scan: %w", err)
			}
			interactions = append(interactions, is)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return utils.InternalServerError("Failed to fetch entity details", err.Error())
	}

	if identifiers == nil {
		identifiers = make([]payload.EntityIdentifier, 0)
	}
	if interactions == nil {
		interactions = make([]payload.InteractionSummary, 0)
	}

	resp.Identifiers = identifiers
	resp.Interactions = interactions

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

	ctx := c.Context()

	// Call the Postgres function for GDPR anonymization
	query := `SELECT fn_anonymize_entity($1, $2)`
	_, err = h.DB.Exec(ctx, query, tenantID, entityID)

	if err != nil {
		// PostgreSQL RAISE EXCEPTION 'Entity % not found for tenant %'
		if strings.Contains(err.Error(), "not found") {
			return utils.NotFound("Entity not found")
		}
		return utils.InternalServerError("Failed to anonymize entity", fmt.Errorf("anonymize_error: %w", err).Error())
	}

	return c.JSON(payload.EntityDeleteResponse{
		EntityID:   entityID.String(),
		Anonymized: true,
		ErasedAt:   time.Now().UTC(),
	})
}

// LogInteractionHandler godoc
// @Summary      Log a single interaction
// @Description  Appends a behavioral event to the L2 Behavioral Graph. Supports idempotent logging via external_ref. Entity must exist and belong to the calling tenant.
// @Tags         Core
// @Security     ApiKeyAuth
// @Accept       json
// @Produce      json
// @Param        request body payload.InteractionLogRequest true "Interaction payload"
// @Success      201 {object} payload.InteractionLogResponse
// @Failure      400 {object} utils.APIError
// @Failure      401 {object} utils.APIError
// @Failure      404 {object} utils.APIError
// @Failure      500 {object} utils.APIError
// @Router       /v1/core/interactions/log [post]
func (h *Handler) LogInteractionHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Missing tenant context")
	}
	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	// ── 1. Parse & validate ───────────────────────────────────────────────
	var req payload.InteractionLogRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request payload", err.Error())
	}
	if err := utils.Validator.Struct(&req); err != nil {
		return utils.BadRequest("Validation failed", err.Error())
	}

	// Metadata 50KB limit
	metaSize, err := req.MetadataByteSize()
	if err != nil {
		return utils.BadRequest("Field 'metadata' contains invalid JSON", err.Error())
	}
	if metaSize > 50*1024 {
		return utils.BadRequest("Field 'metadata' exceeds maximum size of 50KB")
	}

	// occurred_at validation
	now := time.Now().UTC()
	occurredAt := now
	if req.OccurredAt != nil {
		ts := req.OccurredAt.UTC()
		if ts.After(now) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error":          "validation_error",
				"message":        "Field 'occurred_at' cannot be in the future",
				"field":          "occurred_at",
				"provided_value": ts.Format(time.RFC3339),
			})
		}
		if ts.Before(now.AddDate(-1, 0, 0)) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error":          "validation_error",
				"message":        "Field 'occurred_at' cannot be more than 1 year in the past",
				"field":          "occurred_at",
				"provided_value": ts.Format(time.RFC3339),
			})
		}
		occurredAt = ts
	}

	ctx := c.Context()

	// ── 2. Entity existence check ─────────────────────────────────────────
	var entityExists bool
	if err := h.DB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM entities
			WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
		)
	`, req.EntityID, tenantID).Scan(&entityExists); err != nil {
		return utils.InternalServerError("Failed to verify entity", err.Error())
	}
	if !entityExists {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error":     "not_found",
			"message":   "Entity not found or does not belong to your account",
			"entity_id": req.EntityID,
		})
	}

	// ── 3. Deduplication via external_ref ────────────────────────────────
	if req.ExternalRef != nil && *req.ExternalRef != "" {
		var existingID string
		var existingCreatedAt time.Time
		dupErr := h.DB.QueryRow(ctx, `
			SELECT id, created_at FROM interactions
			WHERE tenant_id = $1 AND api = $2 AND external_ref = $3
			LIMIT 1
		`, tenantID, req.API, *req.ExternalRef).Scan(&existingID, &existingCreatedAt)
		if dupErr == nil {
			// Already logged — return existing record idempotently (200, not 201)
			return c.Status(fiber.StatusOK).JSON(payload.InteractionLogResponse{
				InteractionID: existingID,
				EntityID:      req.EntityID,
				LoggedAt:      existingCreatedAt,
			})
		}
		// dupErr == pgx.ErrNoRows means not a duplicate — continue
		if !strings.Contains(dupErr.Error(), "no rows") {
			return utils.InternalServerError("Failed to check for duplicate interaction", dupErr.Error())
		}
	}

	// ── 4. Insert + increment usage in a transaction ──────────────────────
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		return utils.InternalServerError("Failed to start transaction", err.Error())
	}
	defer tx.Rollback(ctx)

	var interactionID string
	var loggedAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO interactions
		  (tenant_id, entity_id, api, action_type, action, outcome,
		   intent, agent_id, external_ref, metadata, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at
	`,
		tenantID, req.EntityID, req.API, req.ActionType, req.Action,
		req.Outcome, req.Intent, req.AgentID, req.ExternalRef,
		req.Metadata, occurredAt,
	).Scan(&interactionID, &loggedAt)
	if err != nil {
		return utils.InternalServerError("Failed to log interaction", err.Error())
	}

	_, err = tx.Exec(ctx, `SELECT fn_increment_usage($1, 'interaction')`, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to increment usage", err.Error())
	}

	if err := tx.Commit(ctx); err != nil {
		return utils.InternalServerError("Failed to commit transaction", err.Error())
	}

	return c.Status(fiber.StatusCreated).JSON(payload.InteractionLogResponse{
		InteractionID: interactionID,
		EntityID:      req.EntityID,
		LoggedAt:      loggedAt,
	})
}

// LogBatchInteractionsHandler godoc
// @Summary      Log multiple interactions
// @Description  Atomically logs up to 100 interaction events. All items are validated before any insert. Duplicate external_refs are skipped (idempotent), not rejected. Returns 201 Created.
// @Tags         Core
// @Security     ApiKeyAuth
// @Accept       json
// @Produce      json
// @Param        request body payload.BatchInteractionLogRequest true "Batch interaction payload"
// @Success      201 {object} payload.BatchInteractionLogResponse
// @Failure      400 {object} utils.APIError
// @Failure      401 {object} utils.APIError
// @Failure      404 {object} utils.APIError
// @Failure      500 {object} utils.APIError
// @Router       /v1/core/interactions/batch [post]
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

	// Spec-exact batch size error
	if len(req.Interactions) > 100 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":          "validation_error",
			"message":        "Batch size exceeds maximum of 100 interactions",
			"provided_count": len(req.Interactions),
			"max_count":      100,
		})
	}
	if len(req.Interactions) == 0 {
		return utils.BadRequest("Field 'interactions' must contain at least one item")
	}

	ctx := c.Context()
	now := time.Now().UTC()

	// ── Phase 1: Per-item pre-validation (all must pass before any insert) ──
	type resolvedItem struct {
		item       payload.InteractionLogRequest
		occurredAt time.Time
	}
	resolved := make([]resolvedItem, 0, len(req.Interactions))

	for i, item := range req.Interactions {
		// Struct-level validation
		if err := utils.Validator.Struct(&item); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error":   "validation_error",
				"message": fmt.Sprintf("Validation failed for interaction at index %d", i),
				"index":   i,
				"reason":  err.Error(),
			})
		}

		// Metadata 50KB check
		metaSize, err := item.MetadataByteSize()
		if err != nil || metaSize > 50*1024 {
			reason := "Field 'metadata' exceeds maximum size of 50KB"
			if err != nil {
				reason = "Field 'metadata' contains invalid JSON"
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error":   "validation_error",
				"message": fmt.Sprintf("Validation failed for interaction at index %d", i),
				"index":   i,
				"field":   "metadata",
				"reason":  reason,
			})
		}

		// occurred_at range validation
		occurredAt := now
		if item.OccurredAt != nil {
			ts := item.OccurredAt.UTC()
			if ts.After(now) {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error":          "validation_error",
					"message":        fmt.Sprintf("Validation failed for interaction at index %d", i),
					"index":          i,
					"field":          "occurred_at",
					"reason":         "Field 'occurred_at' cannot be in the future",
					"provided_value": ts.Format(time.RFC3339),
				})
			}
			if ts.Before(now.AddDate(-1, 0, 0)) {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error":          "validation_error",
					"message":        fmt.Sprintf("Validation failed for interaction at index %d", i),
					"index":          i,
					"field":          "occurred_at",
					"reason":         "Field 'occurred_at' cannot be more than 1 year in the past",
					"provided_value": ts.Format(time.RFC3339),
				})
			}
			occurredAt = ts
		}
		resolved = append(resolved, resolvedItem{item, occurredAt})
	}

	// ── Phase 2: Bulk entity existence check ────────────────────────────────
	// Collect unique entity IDs to validate in one query
	entityIDSet := make(map[string]struct{})
	for _, r := range resolved {
		entityIDSet[r.item.EntityID] = struct{}{}
	}
	uniqueEntityIDs := make([]string, 0, len(entityIDSet))
	for id := range entityIDSet {
		uniqueEntityIDs = append(uniqueEntityIDs, id)
	}

	existingEntitiesRows, err := h.DB.Query(ctx, `
		SELECT id::text FROM entities
		WHERE id = ANY($1::uuid[]) AND tenant_id = $2 AND deleted_at IS NULL
	`, uniqueEntityIDs, tenantID)
	if err != nil {
		return utils.InternalServerError("Failed to verify entities", err.Error())
	}
	existingEntities := make(map[string]struct{})
	for existingEntitiesRows.Next() {
		var id string
		if err := existingEntitiesRows.Scan(&id); err != nil {
			existingEntitiesRows.Close()
			return utils.InternalServerError("Failed to scan entity ID", err.Error())
		}
		existingEntities[id] = struct{}{}
	}
	existingEntitiesRows.Close()

	for i, r := range resolved {
		if _, ok := existingEntities[r.item.EntityID]; !ok {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error":     "not_found",
				"message":   fmt.Sprintf("Entity at index %d not found or does not belong to your account", i),
				"index":     i,
				"entity_id": r.item.EntityID,
			})
		}
	}

	// ── Phase 3: external_ref deduplication (per-item, skip not fail) ───────
	type insertItem struct {
		resolved   resolvedItem
		existingID string // non-empty if already exists
	}
	items := make([]insertItem, len(resolved))
	for i, r := range resolved {
		items[i] = insertItem{resolved: r}
		if r.item.ExternalRef != nil && *r.item.ExternalRef != "" {
			var existingID string
			var existingCreatedAt time.Time
			dupErr := h.DB.QueryRow(ctx, `
				SELECT id, created_at FROM interactions
				WHERE tenant_id = $1 AND api = $2 AND external_ref = $3
				LIMIT 1
			`, tenantID, r.item.API, *r.item.ExternalRef).Scan(&existingID, &existingCreatedAt)
			if dupErr == nil {
				items[i].existingID = existingID
			} else if !strings.Contains(dupErr.Error(), "no rows") {
				return utils.InternalServerError("Failed to check for duplicate interaction", dupErr.Error())
			}
		}
	}

	// ── Phase 4: Single-transaction multi-row INSERT (non-duplicates only) ──
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		return utils.InternalServerError("Failed to start transaction", err.Error())
	}
	defer tx.Rollback(ctx)

	// Map index in resolved list → returned ID (duplicate or newly inserted)
	resultIDs := make([]string, len(items))

	// Prefill duplicates
	for i, it := range items {
		if it.existingID != "" {
			resultIDs[i] = it.existingID
		}
	}

	// Build INSERT only for non-duplicate items
	var valueStrings []string
	var valueArgs []interface{}
	var newItemIndexes []int // track which resolved[] positions map to this INSERT
	argID := 1

	for i, it := range items {
		if it.existingID != "" {
			continue // skip duplicates
		}
		r := it.resolved
		valueStrings = append(valueStrings, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			argID, argID+1, argID+2, argID+3, argID+4,
			argID+5, argID+6, argID+7, argID+8, argID+9, argID+10,
		))
		valueArgs = append(valueArgs,
			tenantID, r.item.EntityID, r.item.API, r.item.ActionType, r.item.Action,
			r.item.Outcome, r.item.Intent, r.item.AgentID, r.item.ExternalRef,
			r.item.Metadata, r.occurredAt,
		)
		newItemIndexes = append(newItemIndexes, i)
		argID += 11
	}

	newlyInserted := 0
	if len(valueStrings) > 0 {
		insertQuery := fmt.Sprintf(`
			INSERT INTO interactions
			  (tenant_id, entity_id, api, action_type, action, outcome,
			   intent, agent_id, external_ref, metadata, occurred_at)
			VALUES %s
			RETURNING id
		`, strings.Join(valueStrings, ","))

		insRows, err := tx.Query(ctx, insertQuery, valueArgs...)
		if err != nil {
			return utils.InternalServerError("Failed to log batch interactions", err.Error())
		}
		scanIdx := 0
		for insRows.Next() {
			var id string
			if err := insRows.Scan(&id); err != nil {
				insRows.Close()
				return utils.InternalServerError("Failed to scan returning ID", err.Error())
			}
			resultIDs[newItemIndexes[scanIdx]] = id
			scanIdx++
			newlyInserted++
		}
		insRows.Close()
	}

	// Increment usage only for newly inserted rows
	for i := 0; i < newlyInserted; i++ {
		_, err = tx.Exec(ctx, `SELECT fn_increment_usage($1, 'interaction')`, tenantID)
		if err != nil {
			return utils.InternalServerError("Failed to increment usage", err.Error())
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return utils.InternalServerError("Failed to commit transaction", err.Error())
	}

	firstID, lastID := "", ""
	if len(resultIDs) > 0 {
		firstID = resultIDs[0]
		lastID = resultIDs[len(resultIDs)-1]
	}

	return c.Status(fiber.StatusCreated).JSON(payload.BatchInteractionLogResponse{
		LoggedCount: len(resultIDs),
		FirstID:     firstID,
		LastID:      lastID,
		LoggedAt:    time.Now().UTC(),
	})
}

// RecommendsActionsHandler godoc
// @Summary      Get next best action recommendation (L3)
// @Description  Scores all action types for an entity + intent using behavioral history, returns the highest-confidence recommendation. Free tier returns 402. Returns 200 with null recommendation when data is insufficient.
// @Tags         Core
// @Security     ApiKeyAuth
// @Accept       json
// @Produce      json
// @Param        request body payload.RecommendRequest true "Recommendation request"
// @Success      200 {object} payload.RecommendResponse
// @Failure      400 {object} utils.APIError
// @Failure      401 {object} utils.APIError
// @Failure      402 {object} utils.APIError
// @Failure      404 {object} utils.APIError
// @Failure      500 {object} utils.APIError
// @Router       /v1/core/recommends [post]
func (h *Handler) RecommendsActionsHandler(c fiber.Ctx) error {
	tenantIDStr := c.Locals("tenant_id")
	if tenantIDStr == nil {
		return utils.Unauthorized("Missing tenant context")
	}
	tenantID, err := uuid.Parse(fmt.Sprintf("%v", tenantIDStr))
	if err != nil {
		return utils.Unauthorized("Invalid tenant ID format")
	}

	// ── 1. Parse & validate request ────────────────────────────────────────
	var req payload.RecommendRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request payload", err.Error())
	}
	if err := utils.Validator.Struct(&req); err != nil {
		return utils.BadRequest("Validation failed", err.Error())
	}
	if req.LookbackDays < 0 || req.LookbackDays > 730 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":          "validation_error",
			"message":        "Field 'lookback_days' must be between 1 and 730",
			"field":          "lookback_days",
			"provided_value": req.LookbackDays,
		})
	}
	if req.MinSampleSize < 0 || req.MinSampleSize > 100 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":          "validation_error",
			"message":        "Field 'min_sample_size' must be between 1 and 100",
			"field":          "min_sample_size",
			"provided_value": req.MinSampleSize,
		})
	}
	minSampleSize := req.MinSampleSize
	if minSampleSize == 0 {
		minSampleSize = 2
	}

	ctx := c.Context()

	// ── 2. Fetch tenant plan + gate free tier ──────────────────────────────
	var plan string
	if err := h.DB.QueryRow(ctx, `
		SELECT plan::text FROM tenants WHERE id = $1 AND deleted_at IS NULL
	`, tenantID).Scan(&plan); err != nil {
		return utils.InternalServerError("Failed to fetch tenant plan", err.Error())
	}
	if plan == "free" {
		return c.Status(fiber.StatusPaymentRequired).JSON(fiber.Map{
			"error":         "plan_upgrade_required",
			"message":       "Recommendations are a Builder feature. Upgrade to access behavioral intelligence.",
			"current_plan":  "free",
			"required_plan": "builder",
			"feature":       "recommendations",
			"upgrade_url":   "https://app.fusemomo.com/upgrade",
		})
	}

	// ── 3. Resolve lookback window ─────────────────────────────────────────
	lookbackDays := req.LookbackDays
	if lookbackDays == 0 {
		switch plan {
		case "enterprise":
			lookbackDays = 730
		default: // builder
			lookbackDays = 90
		}
	}

	// ── 4. Entity existence check ─────────────────────────────────────────
	var entityExists bool
	if err := h.DB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM entities WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
		)
	`, req.EntityID, tenantID).Scan(&entityExists); err != nil {
		return utils.InternalServerError("Failed to verify entity", err.Error())
	}
	if !entityExists {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error":     "not_found",
			"message":   "Entity not found or does not belong to your account",
			"entity_id": req.EntityID,
		})
	}

	// ── 5. Call fn_score_action_types ─────────────────────────────────────
	// We pass intent as a nullable string so the PG function's NULL check works.
	rows, err := h.DB.Query(ctx, `
		SELECT action_type, total_count, success_count, success_rate,
		       last_success_at, last_occurred_at
		FROM fn_score_action_types($1, $2, $3)
	`, req.EntityID, req.Intent, lookbackDays)
	if err != nil {
		return utils.InternalServerError("Failed to score action types", err.Error())
	}
	defer rows.Close()

	var scored []payload.ScoredActionType
	var totalSampleSize int64
	for rows.Next() {
		var s payload.ScoredActionType
		if err := rows.Scan(
			&s.ActionType, &s.TotalCount, &s.SuccessCount,
			&s.SuccessRate, &s.LastSuccessAt, &s.LastOccurredAt,
		); err != nil {
			return utils.InternalServerError("Failed to scan scoring result", err.Error())
		}
		totalSampleSize += s.TotalCount
		scored = append(scored, s)
	}
	rows.Close()

	// ── 6. Build scoring breakdown map (all action types, unfiltered) ─────
	breakdown := make(map[string]float64, len(scored))
	for _, s := range scored {
		breakdown[s.ActionType] = s.SuccessRate
	}

	// ── 7. Filter by min_sample_size and pick top recommendation ─────────
	var top *payload.ScoredActionType
	for i := range scored {
		if scored[i].TotalCount >= int64(minSampleSize) {
			top = &scored[i]
			break // fn_score_action_types already returns ordered: success_rate DESC, success_count DESC
		}
	}

	// ── 8. Handle insufficient data — return 200 with null recommendation ─
	if top == nil {
		return c.JSON(payload.RecommendResponse{
			RecommendationID:      nil,
			EntityID:              req.EntityID,
			Intent:                req.Intent,
			RecommendedActionType: nil,
			Confidence:            nil,
			ScoringBreakdown:      breakdown,
			Reason: fmt.Sprintf(
				"Insufficient interaction history for this entity and intent. Need at least %d interactions.",
				minSampleSize,
			),
			SampleSize:   totalSampleSize,
			LookbackDays: lookbackDays,
		})
	}

	// ── 9. Log recommendation + increment usage in a transaction ──────────
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		return utils.InternalServerError("Failed to start transaction", err.Error())
	}
	defer tx.Rollback(ctx)

	// Marshal breakdown for JSONB column
	breakdownJSON, err := json.Marshal(breakdown)
	if err != nil {
		return utils.InternalServerError("Failed to serialize scoring breakdown", err.Error())
	}

	var recommendationID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO recommendations
		  (tenant_id, entity_id, intent, recommended_action_type, confidence_score, scoring_breakdown)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`,
		tenantID, req.EntityID, req.Intent,
		top.ActionType, top.SuccessRate, breakdownJSON,
	).Scan(&recommendationID); err != nil {
		return utils.InternalServerError("Failed to log recommendation", err.Error())
	}

	if _, err := tx.Exec(ctx, `SELECT fn_increment_usage($1, 'recommendation')`, tenantID); err != nil {
		return utils.InternalServerError("Failed to increment usage", err.Error())
	}

	if err := tx.Commit(ctx); err != nil {
		return utils.InternalServerError("Failed to commit transaction", err.Error())
	}

	// ── 10. Build human-readable reason ───────────────────────────────────
	reason := fmt.Sprintf(
		"%s succeeded %d of %d times in the last %d days for %s",
		top.ActionType, top.SuccessCount, top.TotalCount, lookbackDays, req.Intent,
	)

	confidence := top.SuccessRate
	recIDStr := recommendationID
	actionType := top.ActionType

	return c.JSON(payload.RecommendResponse{
		RecommendationID:      &recIDStr,
		EntityID:              req.EntityID,
		Intent:                req.Intent,
		RecommendedActionType: &actionType,
		Confidence:            &confidence,
		ScoringBreakdown:      breakdown,
		Reason:                reason,
		SampleSize:            totalSampleSize,
		LookbackDays:          lookbackDays,
	})
}

func (h *Handler) RecommendsActionsOutcomesHandler(c fiber.Ctx) error {
	return nil
}
