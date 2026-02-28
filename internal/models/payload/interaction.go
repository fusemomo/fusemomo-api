package payload

import (
	"encoding/json"
	"time"
)

type InteractionLogRequest struct {
	EntityID    string                 `json:"entity_id"    validate:"required,uuid"`
	API         string                 `json:"api"          validate:"required,max=100"`
	ActionType  string                 `json:"action_type"  validate:"required,max=100"`
	Action      string                 `json:"action"       validate:"required,max=255"`
	Outcome     string                 `json:"outcome"      validate:"required,oneof=success failed pending ignored unknown"`
	Intent      *string                `json:"intent,omitempty"       validate:"omitempty,max=255"`
	AgentID     *string                `json:"agent_id,omitempty"     validate:"omitempty,max=255"`
	ExternalRef *string                `json:"external_ref,omitempty" validate:"omitempty,max=500"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	OccurredAt  *time.Time             `json:"occurred_at,omitempty"`
}

// MetadataByteSize returns the JSON-serialized size of Metadata for the 50KB limit check.
func (r *InteractionLogRequest) MetadataByteSize() (int, error) {
	if r.Metadata == nil {
		return 0, nil
	}
	b, err := json.Marshal(r.Metadata)
	return len(b), err
}

type InteractionLogResponse struct {
	InteractionID string    `json:"interaction_id"`
	EntityID      string    `json:"entity_id"`
	LoggedAt      time.Time `json:"logged_at"`
}

type BatchInteractionLogRequest struct {
	Interactions []InteractionLogRequest `json:"interactions" validate:"required,min=1,max=100"`
}

type BatchInteractionLogResponse struct {
	LoggedCount int       `json:"logged_count"`
	FirstID     string    `json:"first_id"`
	LastID      string    `json:"last_id"`
	LoggedAt    time.Time `json:"logged_at"`
}
