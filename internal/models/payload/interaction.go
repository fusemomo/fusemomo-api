package payload

import "time"

type InteractionLogRequest struct {
	EntityID    string                 `json:"entity_id" validate:"required,uuid"`
	API         string                 `json:"api" validate:"required"`
	ActionType  string                 `json:"action_type" validate:"required"`
	Action      string                 `json:"action" validate:"required"`
	Outcome     string                 `json:"outcome" validate:"required,oneof=success failed pending ignored unknown"`
	Intent      string                 `json:"intent,omitempty"`
	AgentID     string                 `json:"agent_id,omitempty"`
	ExternalRef string                 `json:"external_ref,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	OccurredAt  *time.Time             `json:"occurred_at,omitempty"`
}

type InteractionLogResponse struct {
	InteractionID string    `json:"interaction_id"`
	LoggedAt      time.Time `json:"logged_at"`
}

type BatchInteractionLogRequest struct {
	Interactions []InteractionLogRequest `json:"interactions" validate:"required,min=1,max=100"`
}

type BatchInteractionLogResponse struct {
	LoggedCount int    `json:"logged_count"`
	FirstID     string `json:"first_id"`
	LastID      string `json:"last_id"`
}
