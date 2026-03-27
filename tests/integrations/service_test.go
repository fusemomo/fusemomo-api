package integrations

import (
	"context"
	"testing"
	"time"

	"fusemomo-api/internal/config"
	rec "fusemomo-api/internal/handlers/v1/recommendation"
	"fusemomo-api/internal/models"

	"github.com/google/uuid"
)

// helper: build a service with a mock store and default config.
func newTestService(store rec.Store) *rec.Service {
	return rec.NewService(store, config.Envs)
}

func TestService_Recommend_WithData_ReturnsPrimary(t *testing.T) {
	t0 := time.Now().UTC().Add(-24 * time.Hour)
	store := &mockStore{
		statsRows: []models.RawInteractionRow{
			{
				API:             "stripe",
				ActionType:      "send_invoice",
				TotalCount:      10,
				SuccessCount:    9,
				LastSuccessAt:   &t0,
				LastOccurredAt:  t0,
				DaysAgoLastSeen: 1,
			},
		},
		persistedID: uuid.New(),
	}

	svc := newTestService(store)

	resp, err := svc.Recommend(context.Background(), models.RecommendRequest{
		TenantID: uuid.New(),
		EntityID: uuid.New(),
		Intent:   "billing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Primary == nil {
		t.Fatal("want Primary to be set, got nil")
	}
	if resp.Primary.API != "stripe" {
		t.Errorf("want primary API = stripe, got %s", resp.Primary.API)
	}
	if resp.ConfidenceScore <= 0 {
		t.Errorf("want confidence_score > 0, got %.4f", resp.ConfidenceScore)
	}
}

func TestService_Recommend_NoData_PrimaryNilAndNotSufficient(t *testing.T) {
	store := &mockStore{
		statsRows:   nil, // no qualifying interactions
		persistedID: uuid.New(),
	}

	svc := newTestService(store)

	resp, err := svc.Recommend(context.Background(), models.RecommendRequest{
		TenantID: uuid.New(),
		EntityID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Primary != nil {
		t.Errorf("want Primary = nil for empty data, got %+v", resp.Primary)
	}
	if resp.DataSufficient {
		t.Error("want DataSufficient = false when no qualifying pairs")
	}
	if resp.ConfidenceScore != 0 {
		t.Errorf("want confidence_score = 0, got %.4f", resp.ConfidenceScore)
	}
	if len(resp.OpportunitySet) != 0 {
		t.Errorf("want empty opportunity_set, got %d entries", len(resp.OpportunitySet))
	}
}

func TestService_Recommend_DataInsufficient_FlagCorrect(t *testing.T) {
	// total_count = 3 < MinInteractionsForConfidence (5) → DataSufficient = false
	t0 := time.Now().UTC().Add(-5 * time.Hour)
	store := &mockStore{
		statsRows: []models.RawInteractionRow{
			{
				API:            "api_a",
				ActionType:     "act_x",
				TotalCount:     3, // below threshold
				SuccessCount:   3,
				LastSuccessAt:  &t0,
				LastOccurredAt: t0,
			},
		},
		persistedID: uuid.New(),
	}

	svc := newTestService(store)

	resp, err := svc.Recommend(context.Background(), models.RecommendRequest{
		TenantID: uuid.New(),
		EntityID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.DataSufficient {
		t.Errorf("want DataSufficient = false when total_count (%d) < MIN_INTERACTIONS_CONFIDENCE (%d)",
			3, config.Envs.MIN_INTERACTIONS_CONFIDENCE)
	}
}

func TestService_Recommend_DefaultLookbackApplied(t *testing.T) {
	var capturedParams rec.FetchStatsParams
	store := &captureStore{
		onFetch: func(p rec.FetchStatsParams) { capturedParams = p },
	}

	svc := newTestService(store)

	// Pass LookbackDays = 0 — service must default to 90.
	_, err := svc.Recommend(context.Background(), models.RecommendRequest{
		TenantID:     uuid.New(),
		EntityID:     uuid.New(),
		LookbackDays: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedParams.LookbackDays != 90 {
		t.Errorf("want default lookback_days = 90, got %d", capturedParams.LookbackDays)
	}
}

func TestService_Recommend_FetchStatsError_PropagatesError(t *testing.T) {
	store := &mockStore{statsErr: errTest("db connection lost")}

	svc := newTestService(store)

	_, err := svc.Recommend(context.Background(), models.RecommendRequest{
		TenantID: uuid.New(),
		EntityID: uuid.New(),
	})
	if err == nil {
		t.Error("expected an error to be returned, got nil")
	}
}

//  captureStore — records FetchStatsParams for assertion

type captureStore struct {
	onFetch func(rec.FetchStatsParams)
}

func (c *captureStore) FetchInteractionStats(_ context.Context, p rec.FetchStatsParams) ([]models.RawInteractionRow, error) {
	if c.onFetch != nil {
		c.onFetch(p)
	}
	return nil, nil
}

func (c *captureStore) FetchFeedbackWeights(_ context.Context, _, _ uuid.UUID) ([]models.FeedbackRecord, error) {
	return nil, nil
}

func (c *captureStore) PersistRecommendation(_ context.Context, _ rec.PersistParams) (uuid.UUID, error) {
	return uuid.New(), nil
}

// errTest is a simple string error type for test clarity.
type errTest string

func (e errTest) Error() string { return string(e) }
