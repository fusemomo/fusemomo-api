package models

import (
	"time"

	"github.com/google/uuid"
)

// input from the HTTP handler to the recommendation service.
type RecommendRequest struct {
	TenantID        uuid.UUID
	EntityID        uuid.UUID
	Intent          string // optional; scopes scoring to a specific intent
	LookbackDays    int    // default resolved by service (90)
	MinSuccessCount int    // default resolved by service (1)
	AgentID         string // optional; for audit trail
}

// OpportunityEntry is one scored (api, action_type) pair in the opportunity set.
type OpportunityEntry struct {
	API            string     `json:"api"`
	ActionType     string     `json:"action_type"`
	TotalCount     int64      `json:"total_count"`
	SuccessCount   int64      `json:"success_count"`
	RawSuccessRate float64    `json:"raw_success_rate"`
	RecencyScore   float64    `json:"recency_score"`
	FeedbackWeight float64    `json:"feedback_weight"`
	CompositeScore float64    `json:"composite_score"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastOccurredAt time.Time  `json:"last_occurred_at"`
	IsPrimary      bool       `json:"is_primary"`
}

// RecommendResponse is returned to the agent and persisted to the recommendations table.
type RecommendResponse struct {
	RecommendationID uuid.UUID          `json:"recommendation_id"`
	EntityID         uuid.UUID          `json:"entity_id"`
	Intent           string             `json:"intent,omitempty"`
	Primary          *OpportunityEntry  `json:"primary"`          // nil if no qualifying data
	OpportunitySet   []OpportunityEntry `json:"opportunity_set"`  // all qualifying pairs, ranked
	ConfidenceScore  float64            `json:"confidence_score"` // primary.CompositeScore, or 0
	DataSufficient   bool               `json:"data_sufficient"`  // false if below MinInteractionsForConfidence
	LookbackDays     int                `json:"lookback_days"`
	GeneratedAt      time.Time          `json:"generated_at"`
}

// RawInteractionRow is one bucketed (api, action_type) row returned by the store query.
type RawInteractionRow struct {
	API             string
	ActionType      string
	TotalCount      int64
	SuccessCount    int64
	LastSuccessAt   *time.Time
	LastOccurredAt  time.Time
	DaysAgoLastSeen float64 // computed: (NOW() - last_occurred_at) in days
}

// FeedbackRecord holds historical follow-through data per (api, action_type) for feedback weighting.
type FeedbackRecord struct {
	API            string
	ActionType     string
	FollowedCount  int64 // times was_followed = true
	SucceededCount int64 // times was_followed = true AND outcome_interaction_id IS NOT NULL
}
