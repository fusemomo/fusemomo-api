package payload

import "time"

// GraphEntity represents a canonical entity node in the behavioral graph.
type GraphEntity struct {
	ID                     string     `json:"id"`
	DisplayName            string     `json:"display_name"`
	EntityType             string     `json:"entity_type"`
	TotalInteractions      int        `json:"total_interactions"`
	SuccessfulInteractions int        `json:"successful_interactions"`
	BehavioralScore        *float64   `json:"behavioral_score"`
	PreferredActionType    string     `json:"preferred_action_type"`
	CreatedAt              time.Time  `json:"created_at"`
}

// GraphIdentifier represents an identifier satellite node linked to an entity.
type GraphIdentifier struct {
	ID              string  `json:"id"`
	EntityID        string  `json:"entity_id"`
	Source          string  `json:"source"`
	IdentifierType  string  `json:"identifier_type"`
	IdentifierValue string  `json:"identifier_value"`
	Confidence      float64 `json:"confidence"`
	LinkStrategy    string  `json:"link_strategy"`
}

// GraphInteractionEdge represents an aggregated interaction edge between an entity and an API.
type GraphInteractionEdge struct {
	EntityID        string    `json:"entity_id"`
	API             string    `json:"api"`
	TotalCount      int       `json:"total_count"`
	SuccessCount    int       `json:"success_count"`
	DominantOutcome string    `json:"dominant_outcome"`
	LastOccurredAt  time.Time `json:"last_occurred_at"`
}

// GraphEntityLink represents an entity merge audit trail edge.
type GraphEntityLink struct {
	ID                string  `json:"id"`
	CanonicalEntityID string  `json:"canonical_entity_id"`
	MergedEntityID    string  `json:"merged_entity_id"`
	LinkStrategy      string  `json:"link_strategy"`
	Confidence        float64 `json:"confidence"`
}

// GraphDataResponse is the top-level response for GET /app/graph.
type GraphDataResponse struct {
	Entities      []GraphEntity          `json:"entities"`
	Identifiers   []GraphIdentifier      `json:"identifiers"`
	Interactions  []GraphInteractionEdge `json:"interactions"`
	EntityLinks   []GraphEntityLink      `json:"entity_links"`
	TotalEntities int                    `json:"total_entities"`
	Truncated     bool                   `json:"truncated"`
}
