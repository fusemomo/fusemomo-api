package payload

import "time"

type RecommendRequest struct {
	Intent          string `json:"intent"`
	LookbackDays    int    `json:"lookback_days"`     // 0 = service default (90)
	MinSuccessCount int    `json:"min_success_count"` // 0 = service default (1)
	AgentID         string `json:"agent_id"`
}

type FeedbackRequest struct {
	WasFollowed          bool    `json:"was_followed"`
	OutcomeInteractionID *string `json:"outcome_interaction_id"` // optional UUID
}

type FeedbackResponse struct {
	RecommendationID string    `json:"recommendation_id"`
	WasFollowed      bool      `json:"was_followed"`
	Outcome          *string   `json:"outcome"`
	UpdatedAt        time.Time `json:"updated_at"`
}
