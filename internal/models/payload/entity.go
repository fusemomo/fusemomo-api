package payload

import (
	"encoding/json"
	"time"
)

type EntityResponse struct {
	ID                     string                 `json:"id"`
	TenantID               string                 `json:"tenant_id"`
	DisplayName            string                 `json:"display_name,omitempty"`
	EntityType             string                 `json:"entity_type,omitempty"`
	TotalInteractions      int                    `json:"total_interactions"`
	SuccessfulInteractions int                    `json:"successful_interactions"`
	LastInteractionAt      *time.Time             `json:"last_interaction_at,omitempty"`
	PreferredActionType    string                 `json:"preferred_action_type,omitempty"`
	BehavioralScore        *float64               `json:"behavioral_score,omitempty"`
	Metadata               map[string]interface{} `json:"metadata"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
}

type EntitiesListResponse struct {
	Entities []EntityResponse `json:"entities"`
	Total    int              `json:"total"`
	Limit    int              `json:"limit"`
	Offset   int              `json:"offset"`
}

type EntityIdentifier struct {
	ID              string     `json:"id"`
	Source          string     `json:"source"`
	IdentifierType  string     `json:"identifier_type"`
	IdentifierValue string     `json:"identifier_value"`
	Confidence      float64    `json:"confidence"`
	LinkStrategy    string     `json:"link_strategy"`
	VerifiedAt      *time.Time `json:"verified_at,omitempty"`
}

type InteractionSummary struct {
	ID         string    `json:"id"`
	API        string    `json:"api"`
	ActionType string    `json:"action_type"`
	Outcome    string    `json:"outcome"`
	OccurredAt time.Time `json:"occurred_at"`
}

type EntityDetailResponse struct {
	EntityResponse
	Identifiers  []EntityIdentifier   `json:"identifiers"`
	Interactions []InteractionSummary `json:"recent_interactions"`
}

type EntityDeleteResponse struct {
	EntityID   string    `json:"entity_id"`
	Anonymized bool      `json:"anonymized"`
	ErasedAt   time.Time `json:"erased_at"`
}

// ResolveEntityRequest is the request body for POST /v1/entities/resolve.
type ResolveEntityRequest struct {
	// Identifiers maps source name → identifier value.
	Identifiers map[string]string `json:"identifiers" validate:"required,min=1"`
	EntityType  *string           `json:"entity_type"`
	DisplayName *string           `json:"display_name"`
	Metadata    map[string]any    `json:"metadata"`
}

// MetadataBytes returns the JSON-encoded size of Metadata for validation.
func (r *ResolveEntityRequest) MetadataBytes() (int, error) {
	if r.Metadata == nil {
		return 0, nil
	}
	b, err := json.Marshal(r.Metadata)
	return len(b), err
}

// ResolveEntityResponse is the success body for POST /v1/entities/resolve.
type ResolveEntityResponse struct {
	EntityID               string             `json:"entity_id"`
	Identifiers            []EntityIdentifier `json:"identifiers"`
	EntityType             *string            `json:"entity_type"`
	DisplayName            *string            `json:"display_name"`
	TotalInteractions      int                `json:"total_interactions"`
	SuccessfulInteractions int                `json:"successful_interactions"`
	LastInteractionAt      *time.Time         `json:"last_interaction_at"`
	PreferredActionType    *string            `json:"preferred_action_type"`
	BehavioralScore        *float64           `json:"behavioral_score"`
	Metadata               map[string]any     `json:"metadata"`
	CreatedAt              time.Time          `json:"created_at"`
}
