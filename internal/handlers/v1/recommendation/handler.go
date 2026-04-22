package recommendation

import (
	"strings"
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
	db      *pgxpool.Pool
	service *Service
}

func NewHandler(db *pgxpool.Pool) *Handler {
	store := NewStore(db)
	service := NewService(store, config.Envs)
	return &Handler{db: db, service: service}
}

// RecommendHandler godoc
// @Summary      Get L3 ranked recommendations for an entity
// @Description  Scores all (api, action_type) pairs for an entity + intent using behavioral history.
//
//	Returns a primary recommendation and the full ranked opportunity set.
//	Builder+ plan required. Returns 200 with empty opportunity_set when data is insufficient — never 404 for missing data.
//
// @Tags         Core
// @Security     ApiKeyAuth
// @Accept       json
// @Produce      json
// @Param        entity_id path  string            true  "Entity UUID"
// @Param        request   body  payload.RecommendRequest  false "Recommendation parameters"
// @Success      200 {object} models.RecommendResponse
// @Failure      400 {object} utils.APIError
// @Failure      401 {object} utils.APIError
// @Failure      402 {object} utils.APIError
// @Failure      404 {object} utils.APIError
// @Failure      500 {object} utils.APIError
// @Router       /v1/core/entities/{entity_id}/recommend [post]
func (h *Handler) RecommendHandler(c fiber.Ctx) error {
	tenantID, err := utils.TenantIDFromCtx(c)
	if err != nil {
		return err
	}

	entityIDParam := c.Params("entity_id")
	entityID, err := uuid.Parse(entityIDParam)
	if err != nil {
		return utils.BadRequest("Invalid entity_id — must be a valid UUID", err.Error())
	}

	var req payload.RecommendRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request body", err.Error())
	}

	// Validate optional bounds.
	if req.LookbackDays < 0 || req.LookbackDays > 365 {
		return utils.BadRequest("lookback_days must be between 1 and 365")
	}
	if req.MinSuccessCount < 0 || req.MinSuccessCount > 100 {
		return utils.BadRequest("min_success_count must be between 0 and 100")
	}

	ctx := c.Context()

	// Plan gate — L3 requires builder or enterprise.
	// utils.PlanFromCtx reads from c.Locals (set by auth middleware) — no extra DB call.
	plan := utils.PlanFromCtx(c)
	if utils.RequirePlan(c, utils.PlanBuilder) {
		return nil
	}

	// Resolve plan-based default lookback when caller omits the field.
	if req.LookbackDays == 0 {
		switch plan {
		case utils.PlanEnterprise:
			req.LookbackDays = 730
		default: // builder
			req.LookbackDays = 90
		}
	}

	// Entity existence check — returns 404 when entity unknown or not owned by tenant.
	var exists bool
	if err = h.db.QueryRow(c.Context(),
		`SELECT EXISTS(SELECT 1 FROM entities WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL)`,
		entityID.String(), tenantID.String(),
	).Scan(&exists); err != nil {
		return utils.InternalServerError("Failed to verify entity", err.Error())
	}
	if !exists {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error":     "not_found",
			"message":   "Entity not found or does not belong to your account",
			"entity_id": entityID,
		})
	}

	resp, err := h.service.Recommend(ctx, models.RecommendRequest{
		TenantID:        tenantID,
		EntityID:        entityID,
		Intent:          req.Intent,
		LookbackDays:    req.LookbackDays,
		MinSuccessCount: req.MinSuccessCount,
		AgentID:         req.AgentID,
	})
	if err != nil {
		return utils.InternalServerError("Failed to generate recommendation", err.Error())
	}

	return c.JSON(resp)
}

// FeedbackHandler godoc
//
// @Summary      Record recommendation outcome (feedback loop)
// @Description  Closes the feedback loop. Reports whether the agent followed the recommendation
//
//	and optionally links the resulting interaction. Builder+ required. Idempotent — last write wins.
//
// @Tags         Core
// @Security     ApiKeyAuth
// @Accept       json
// @Produce      json
// @Param        id      path  string          true  "Recommendation UUID"
// @Param        request body  payload.FeedbackRequest true  "Feedback payload"
// @Success      200 {object} payload.FeedbackResponse
// @Failure      400 {object} utils.APIError
// @Failure      401 {object} utils.APIError
// @Failure      402 {object} utils.APIError
// @Failure      404 {object} utils.APIError
// @Failure      500 {object} utils.APIError
// @Router       /v1/core/recommends/{id}/feedback [patch]
func (h *Handler) FeedbackHandler(c fiber.Ctx) error {
	tenantID, err := utils.TenantIDFromCtx(c)
	if err != nil {
		return err
	}

	recIDParam := c.Params("id")
	recID, err := uuid.Parse(recIDParam)
	if err != nil {
		return utils.BadRequest("Invalid recommendation id — must be a valid UUID", err.Error())
	}

	var req payload.FeedbackRequest
	if err := c.Bind().JSON(&req); err != nil {
		return utils.BadRequest("Invalid request body", err.Error())
	}
	if req.OutcomeInteractionID != nil {
		if _, err := uuid.Parse(*req.OutcomeInteractionID); err != nil {
			return utils.BadRequest("outcome_interaction_id must be a valid UUID", err.Error())
		}
	}

	ctx := c.Context()

	// Plan gate — reads plan from c.Locals (no extra DB call).
	if utils.RequirePlan(c, utils.PlanBuilder) {
		return nil
	}

	// Verify recommendation ownership — fetch entity_id at the same time.
	var recEntityIDStr string
	err = h.db.QueryRow(ctx,
		`SELECT entity_id::text FROM recommendations WHERE id = $1 AND tenant_id = $2`,
		recID.String(), tenantID.String(),
	).Scan(&recEntityIDStr)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error":             "not_found",
				"message":           "Recommendation not found or does not belong to your account",
				"recommendation_id": recIDParam,
			})
		}
		return utils.InternalServerError("Failed to fetch recommendation", err.Error())
	}

	// Validate outcome_interaction_id — must belong to the same tenant and entity.
	var interactionOutcome *string
	if req.OutcomeInteractionID != nil {
		var intEntityIDStr, outcome string
		err = h.db.QueryRow(ctx,
			`SELECT entity_id::text, outcome::text FROM interactions WHERE id = $1 AND tenant_id = $2`,
			*req.OutcomeInteractionID, tenantID.String(),
		).Scan(&intEntityIDStr, &outcome)
		if err != nil {
			if strings.Contains(err.Error(), "no rows") {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error":          "validation_error",
					"message":        "Interaction not found or does not belong to your account",
					"interaction_id": *req.OutcomeInteractionID,
				})
			}
			return utils.InternalServerError("Failed to verify interaction", err.Error())
		}
		if intEntityIDStr != recEntityIDStr {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error":                    "validation_error",
				"message":                  "Interaction does not belong to the same entity as the recommendation",
				"interaction_id":           *req.OutcomeInteractionID,
				"recommendation_entity_id": recEntityIDStr,
				"interaction_entity_id":    intEntityIDStr,
			})
		}
		interactionOutcome = &outcome
	}

	// Idempotent update — last write wins.
	updatedAt := time.Now().UTC()
	if _, err := h.db.Exec(ctx,
		`UPDATE recommendations SET was_followed = $1, outcome_interaction_id = $2 WHERE id = $3 AND tenant_id = $4`,
		req.WasFollowed, req.OutcomeInteractionID, recID.String(), tenantID.String(),
	); err != nil {
		return utils.InternalServerError("Failed to record feedback", err.Error())
	}

	return c.JSON(payload.FeedbackResponse{
		RecommendationID: recIDParam,
		WasFollowed:      req.WasFollowed,
		Outcome:          interactionOutcome,
		UpdatedAt:        updatedAt,
	})
}

