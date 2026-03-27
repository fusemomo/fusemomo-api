package recommendation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fusemomo-api/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store defines the persistence contract for the L3 recommendation module.
// Using an interface allows the service to be tested with a mock store.
type Store interface {
	// FetchInteractionStats returns bucketed (api, action_type) stats for the
	// entity within the lookback window, optionally scoped by intent.
	FetchInteractionStats(ctx context.Context, p FetchStatsParams) ([]models.RawInteractionRow, error)

	// FetchFeedbackWeights returns historical recommendation follow-through rates
	// per (api, action_type) for this entity, to inform the feedback weight calculation.
	// Best-effort — returns empty slice on no data (not an error).
	FetchFeedbackWeights(ctx context.Context, tenantID, entityID uuid.UUID) ([]models.FeedbackRecord, error)

	// PersistRecommendation writes the scored result to the recommendations table
	// and returns the new recommendation UUID.
	PersistRecommendation(ctx context.Context, p PersistParams) (uuid.UUID, error)
}

// FetchStatsParams groups inputs for FetchInteractionStats.
type FetchStatsParams struct {
	TenantID        uuid.UUID
	EntityID        uuid.UUID
	Intent          string // empty = all intents (no intent filter)
	LookbackDays    int
	MinSuccessCount int
}

// PersistParams groups inputs for PersistRecommendation.
type PersistParams struct {
	TenantID          uuid.UUID
	EntityID          uuid.UUID
	Intent            string
	PrimaryAPI        string
	PrimaryActionType string
	ConfidenceScore   float64
	OpportunitySet    []models.OpportunityEntry // serialised to JSONB
	ScoringBreakdown  map[string]any            // audit map → scoring_breakdown column
	AgentID           string
	LookbackDays      int
	MinSuccessCount   int
}

// pgStore is the production pgx/v5 implementation of Store.
type pgStore struct {
	db *pgxpool.Pool
}

// NewStore returns a production-ready Store backed by a pgx connection pool.
func NewStore(db *pgxpool.Pool) Store {
	return &pgStore{db: db}
}

// FetchInteractionStats executes the interaction bucketing query.
//
// SQL notes:
//   - ($3 || ' days')::INTERVAL  — parameterised lookback window
//   - $4::TEXT IS NULL OR intent = $4 — optional intent filter via nullable param
//   - HAVING success_count >= $5 — pre-filters low-success pairs in the DB
func (s *pgStore) FetchInteractionStats(ctx context.Context, p FetchStatsParams) ([]models.RawInteractionRow, error) {
	var intentParam *string
	if p.Intent != "" {
		intentParam = &p.Intent
	}

	rows, err := s.db.Query(ctx, `
		SELECT
		    api,
		    action_type,
		    COUNT(*)                                                    AS total_count,
		    COUNT(*) FILTER (WHERE outcome = 'success')                 AS success_count,
		    MAX(occurred_at) FILTER (WHERE outcome = 'success')         AS last_success_at,
		    MAX(occurred_at)                                            AS last_occurred_at,
		    EXTRACT(EPOCH FROM (NOW() - MAX(occurred_at))) / 86400.0    AS days_ago_last_seen
		FROM interactions
		WHERE
		    tenant_id    = $1
		    AND entity_id = $2
		    AND occurred_at > NOW() - ($3 || ' days')::INTERVAL
		    AND ($4::TEXT IS NULL OR intent = $4)
		GROUP BY api, action_type
		HAVING COUNT(*) FILTER (WHERE outcome = 'success') >= $5
		ORDER BY success_count DESC
	`, p.TenantID.String(), p.EntityID.String(), p.LookbackDays, intentParam, p.MinSuccessCount)
	if err != nil {
		return nil, fmt.Errorf("store: fetch interaction stats: %w", err)
	}
	defer rows.Close()

	var result []models.RawInteractionRow
	for rows.Next() {
		var r models.RawInteractionRow
		if err := rows.Scan(
			&r.API,
			&r.ActionType,
			&r.TotalCount,
			&r.SuccessCount,
			&r.LastSuccessAt,
			&r.LastOccurredAt,
			&r.DaysAgoLastSeen,
		); err != nil {
			return nil, fmt.Errorf("store: scan interaction stats row: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate interaction stats rows: %w", err)
	}

	return result, nil
}

// FetchFeedbackWeights returns historical recommendation follow-through rates.
// Feedback is keyed on action_type only in v1 (see spec §5 v1 note).
// Returns an empty slice (not an error) when no feedback history exists.
func (s *pgStore) FetchFeedbackWeights(ctx context.Context, tenantID, entityID uuid.UUID) ([]models.FeedbackRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT
		    recommended_action_type                                         AS action_type,
		    COUNT(*) FILTER (WHERE was_followed = TRUE)                     AS followed_count,
		    COUNT(*) FILTER (
		        WHERE was_followed = TRUE
		          AND outcome_interaction_id IS NOT NULL
		    )                                                               AS succeeded_count
		FROM recommendations
		WHERE
		    tenant_id   = $1
		    AND entity_id = $2
		    AND was_followed IS NOT NULL
		GROUP BY recommended_action_type
	`, tenantID.String(), entityID.String())
	if err != nil {
		return nil, fmt.Errorf("store: fetch feedback weights: %w", err)
	}
	defer rows.Close()

	var result []models.FeedbackRecord
	for rows.Next() {
		var r models.FeedbackRecord
		// v1: api field not available from the recommendations table; left empty.
		if err := rows.Scan(&r.ActionType, &r.FollowedCount, &r.SucceededCount); err != nil {
			return nil, fmt.Errorf("store: scan feedback weight row: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate feedback weight rows: %w", err)
	}

	return result, nil
}

// PersistRecommendation inserts a scored recommendation into the recommendations table
// and returns the generated UUID.
func (s *pgStore) PersistRecommendation(ctx context.Context, p PersistParams) (uuid.UUID, error) {
	opportunityJSON, err := json.Marshal(p.OpportunitySet)
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: marshal opportunity_set: %w", err)
	}

	breakdownJSON, err := json.Marshal(p.ScoringBreakdown)
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: marshal scoring_breakdown: %w", err)
	}

	// Normalise empty maps to valid JSON objects for the jsonb column.
	if string(opportunityJSON) == "null" {
		opportunityJSON = []byte("[]")
	}
	if string(breakdownJSON) == "null" {
		breakdownJSON = []byte("{}")
	}

	// Coerce empty intent to an empty string (column is NOT NULL).
	intent := p.Intent

	// agent_id stored as empty string when not supplied.
	agentID := p.AgentID

	var createdAt time.Time
	var idStr string
	err = s.db.QueryRow(ctx, `
		INSERT INTO recommendations (
		    tenant_id,
		    entity_id,
		    intent,
		    recommended_action_type,
		    confidence_score,
		    scoring_breakdown,
		    opportunity_set,
		    lookback_days,
		    min_success_count,
		    agent_id
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10)
		RETURNING id, created_at
	`,
		p.TenantID.String(),
		p.EntityID.String(),
		intent,
		p.PrimaryActionType,
		p.ConfidenceScore,
		string(breakdownJSON),
		string(opportunityJSON),
		p.LookbackDays,
		p.MinSuccessCount,
		agentID,
	).Scan(&idStr, &createdAt)
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: insert recommendation: %w", err)
	}

	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: parse returned id: %w", err)
	}

	return id, nil
}
