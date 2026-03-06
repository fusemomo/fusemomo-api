package payload

import "time"

// RecommendRequest is the request body for POST /v1/recommends.
type RecommendRequest struct {
	EntityID      string `json:"entity_id"   validate:"required,uuid"`
	Intent        string `json:"intent"       validate:"required,max=255"`
	LookbackDays  int    `json:"lookback_days"`   // 0 = use plan default; max 730
	MinSampleSize int    `json:"min_sample_size"` // 0 = use default of 2; max 100
}

// ScoredActionType holds a single row from fn_score_action_types.
type ScoredActionType struct {
	ActionType     string     `json:"action_type"`
	TotalCount     int64      `json:"total_count"`
	SuccessCount   int64      `json:"success_count"`
	SuccessRate    float64    `json:"success_rate"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
	LastOccurredAt *time.Time `json:"last_occurred_at"`
}

// RecommendOutcomeRequest is the body for PATCH /v1/recommends/:id/outcomes.
type RecommendOutcomeRequest struct {
	WasFollowed          bool    `json:"was_followed"`
	OutcomeInteractionID *string `json:"outcome_interaction_id"` // optional UUID
}

// RecommendOutcomeResponse is the success body for PATCH /v1/recommends/:id/outcomes.
type RecommendOutcomeResponse struct {
	RecommendationID string    `json:"recommendation_id"`
	WasFollowed      bool      `json:"was_followed"`
	Outcome          *string   `json:"outcome"` // nil when no interaction linked
	UpdatedAt        time.Time `json:"updated_at"`
}

// RecommendResponse is the success body for POST /v1/recommends.
type RecommendResponse struct {
	// RecommendationID is nil when there is insufficient data.
	RecommendationID *string `json:"recommendation_id"`
	EntityID         string  `json:"entity_id"`
	Intent           string  `json:"intent"`
	// RecommendedActionType is nil when there is insufficient data.
	RecommendedActionType *string            `json:"recommended_action_type"`
	Confidence            *float64           `json:"confidence"`
	ScoringBreakdown      map[string]float64 `json:"scoring_breakdown"`
	Reason                string             `json:"reason"`
	SampleSize            int64              `json:"sample_size"`
	LookbackDays          int                `json:"lookback_days"`
}

// ─── Dashboard Recommendation List ──

// DashboardRecommendationRow is one row in the dashboard recommendation list.
type DashboardRecommendationRow struct {
	ID                    string    `json:"id"`
	EntityID              string    `json:"entity_id"`
	EntityDisplayName     string    `json:"entity_display_name"`
	Intent                string    `json:"intent"`
	RecommendedActionType string    `json:"recommended_action_type"`
	ConfidenceScore       float64   `json:"confidence_score"`
	WasFollowed           *bool     `json:"was_followed"`
	Outcome               *string   `json:"outcome"`
	AgentID               *string   `json:"agent_id"`
	CreatedAt             time.Time `json:"created_at"`
}

// GetRecommendationsResponse is the response for GET /dashboard/recommendations.
type GetRecommendationsResponse struct {
	Recommendations []DashboardRecommendationRow `json:"recommendations"`
	Total           int64                        `json:"total"`
	Limit           int                          `json:"limit"`
	Offset          int                          `json:"offset"`
	Page            int                          `json:"page"`
}

// ─── Dashboard Recommendation Stats ─

// RecommendationTrends holds daily/weekly trend metrics.
type RecommendationTrends struct {
	ServedToday             int     `json:"served_today"`
	FollowThroughChangeWeek float64 `json:"follow_through_change_week"`
	SuccessChangeWeek       float64 `json:"success_change_week"`
}

// GetRecommendationStatsResponse is the response for GET /dashboard/recommendations/stats.
type GetRecommendationStatsResponse struct {
	TotalServed           int64                `json:"total_served"`
	TotalFollowed         int64                `json:"total_followed"`
	FollowThroughRate     float64              `json:"follow_through_rate"`
	SuccessWhenFollowed   float64              `json:"success_when_followed"`
	BaselineSuccessRate   float64              `json:"baseline_success_rate"`
	ImprovementVsBaseline float64              `json:"improvement_vs_baseline"`
	Trends                RecommendationTrends `json:"trends"`
}
