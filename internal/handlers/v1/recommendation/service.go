package recommendation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"fusemomo-api/internal/config"
	"fusemomo-api/internal/models"

	"github.com/google/uuid"
)

// Service orchestrates the L3 recommendation flow:
//  1. Fetch raw interaction stats from the DB.
//  2. Fetch historical feedback weights (best-effort).
//  3. Score and rank (api, action_type) pairs.
//  4. Build the response.
//  5. Persist asynchronously — never blocks the caller.
type Service struct {
	store  Store
	scorer *Scorer
	cfg    config.Config
}

// NewService constructs a Service wired with the given store and config.
func NewService(store Store, cfg config.Config) *Service {
	return &Service{
		store:  store,
		scorer: NewScorer(cfg),
		cfg:    cfg,
	}
}

// Recommend generates a ranked recommendation for the given entity and intent.
// It always returns a non-nil *Response — callers should check DataSufficient
// to determine whether the response contains actionable data.
func (s *Service) Recommend(ctx context.Context, req models.RecommendRequest) (*models.RecommendResponse, error) {
	// Apply service-level defaults.
	if req.LookbackDays == 0 {
		req.LookbackDays = 90
	}
	if req.MinSuccessCount == 0 {
		req.MinSuccessCount = 1
	}

	slog.InfoContext(ctx, "recommendation.generate",
		"tenant_id", req.TenantID,
		"entity_id", req.EntityID,
		"intent", req.Intent,
		"lookback_days", req.LookbackDays,
	)

	// 1. Fetch bucketed interaction stats.
	rows, err := s.store.FetchInteractionStats(ctx, FetchStatsParams{
		TenantID:        req.TenantID,
		EntityID:        req.EntityID,
		Intent:          req.Intent,
		LookbackDays:    req.LookbackDays,
		MinSuccessCount: req.MinSuccessCount,
	})
	if err != nil {
		return nil, fmt.Errorf("recommendation: fetch stats: %w", err)
	}

	// 2. Fetch feedback weights — best-effort, don't fail the request if empty.
	feedback, _ := s.store.FetchFeedbackWeights(ctx, req.TenantID, req.EntityID)

	// 3. Score and rank.
	opportunities := s.scorer.Score(rows, feedback, req.MinSuccessCount)

	// 4. Build response.
	resp := &models.RecommendResponse{
		RecommendationID: uuid.New(),
		EntityID:         req.EntityID,
		Intent:           req.Intent,
		OpportunitySet:   opportunities,
		LookbackDays:     req.LookbackDays,
		GeneratedAt:      time.Now().UTC(),
		DataSufficient:   false,
	}

	var primaryAPI, primaryActionType string
	if len(opportunities) > 0 {
		resp.Primary = &opportunities[0]
		resp.ConfidenceScore = opportunities[0].CompositeScore
		resp.DataSufficient = opportunities[0].TotalCount >= int64(s.cfg.MIN_INTERACTIONS_CONFIDENCE)
		primaryAPI = opportunities[0].API
		primaryActionType = opportunities[0].ActionType
	}

	slog.InfoContext(ctx, "recommendation.scored",
		"entity_id", req.EntityID,
		"opportunity_count", len(opportunities),
		"primary_api", primaryAPI,
		"primary_action_type", primaryActionType,
		"confidence_score", resp.ConfidenceScore,
		"data_sufficient", resp.DataSufficient,
	)

	// 5. Persist asynchronously — persistence failure must NOT affect the agent response.
	go s.persistAsync(req, resp, primaryAPI, primaryActionType)

	return resp, nil
}

// persistAsync writes the recommendation to the DB in a background goroutine.
// Uses a fresh context with a hard timeout to avoid leaking goroutines.
func (s *Service) persistAsync(
	req models.RecommendRequest,
	resp *models.RecommendResponse,
	primaryAPI, primaryActionType string,
) {
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := s.store.PersistRecommendation(persistCtx, PersistParams{
		RecommendationID:  resp.RecommendationID,
		TenantID:          req.TenantID,
		EntityID:          req.EntityID,
		Intent:            req.Intent,
		PrimaryAPI:        primaryAPI,
		PrimaryActionType: primaryActionType,
		ConfidenceScore:   resp.ConfidenceScore,
		OpportunitySet:    resp.OpportunitySet,
		ScoringBreakdown:  buildScoringBreakdown(req, resp),
		AgentID:           req.AgentID,
		LookbackDays:      req.LookbackDays,
		MinSuccessCount:   req.MinSuccessCount,
	})
	if err != nil {
		slog.Error("recommendation.persist_failed",
			"entity_id", req.EntityID,
			"err", err,
		)
	}
}

// buildScoringBreakdown produces the audit map persisted in the scoring_breakdown column.
func buildScoringBreakdown(req models.RecommendRequest, resp *models.RecommendResponse) map[string]any {
	return map[string]any{
		"lookback_days":     req.LookbackDays,
		"min_success_count": req.MinSuccessCount,
		"intent_scoped":     req.Intent != "",
		"opportunity_count": len(resp.OpportunitySet),
		"data_sufficient":   resp.DataSufficient,
	}
}
