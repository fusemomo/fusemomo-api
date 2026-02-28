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
