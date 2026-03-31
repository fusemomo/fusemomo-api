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

// InteractionItem is a single row returned in the list response.
type InteractionItem struct {
	ID          string          `json:"id"`
	API         string          `json:"api"`
	ActionType  string          `json:"action_type"`
	Action      string          `json:"action"`
	Outcome     string          `json:"outcome"`
	Intent      *string         `json:"intent,omitempty"`
	AgentID     *string         `json:"agent_id,omitempty"`
	ExternalRef *string         `json:"external_ref,omitempty"`
	Metadata    json.RawMessage `json:"metadata"`
	OccurredAt  time.Time       `json:"occurred_at"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ListInteractionsResponse is the full paginated response.
type ListInteractionsResponse struct {
	Data   []InteractionItem `json:"data"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}
