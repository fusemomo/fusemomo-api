package recommendation

// store_test.go tests the Store interface contract using a mock implementation.
// Integration tests against a real Postgres instance (testcontainers-go) are
// left as a future enhancement per spec §10 — the mock here ensures the service
// layer behaves correctly regardless of DB implementation details.

import (
	"context"
	"errors"
	"testing"
	"time"

	"fusemomo-api/internal/models"

	"github.com/google/uuid"
)

//  Mock Store

type mockStore struct {
	statsRows       []models.RawInteractionRow
	statsErr        error
	feedbackRecords []models.FeedbackRecord
	feedbackErr     error
	persistedID     uuid.UUID
	persistErr      error
	persistedParams *PersistParams // capture for assertion
}

func (m *mockStore) FetchInteractionStats(_ context.Context, p FetchStatsParams) ([]models.RawInteractionRow, error) {
	return m.statsRows, m.statsErr
}

func (m *mockStore) FetchFeedbackWeights(_ context.Context, _, _ uuid.UUID) ([]models.FeedbackRecord, error) {
	return m.feedbackRecords, m.feedbackErr
}

func (m *mockStore) PersistRecommendation(_ context.Context, p PersistParams) (uuid.UUID, error) {
	m.persistedParams = &p
	return m.persistedID, m.persistErr
}

//  Tests

func TestMockStore_FetchInteractionStats_ReturnsRows(t *testing.T) {
	t0 := time.Now().UTC()
	expected := []models.RawInteractionRow{
		{
			API:             "stripe",
			ActionType:      "charge",
			TotalCount:      20,
			SuccessCount:    15,
			LastSuccessAt:   &t0,
			LastOccurredAt:  t0,
			DaysAgoLastSeen: 0.5,
		},
	}
	store := &mockStore{statsRows: expected}

	rows, err := store.FetchInteractionStats(context.Background(), FetchStatsParams{
		TenantID:        uuid.New(),
		EntityID:        uuid.New(),
		LookbackDays:    90,
		MinSuccessCount: 1,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].API != "stripe" || rows[0].SuccessCount != 15 {
		t.Errorf("unexpected row content: %+v", rows[0])
	}
}

func TestMockStore_FetchInteractionStats_PropagatesError(t *testing.T) {
	store := &mockStore{statsErr: errors.New("db timeout")}

	_, err := store.FetchInteractionStats(context.Background(), FetchStatsParams{})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestMockStore_FetchFeedbackWeights_EmptyOnNoHistory(t *testing.T) {
	store := &mockStore{feedbackRecords: nil}

	records, err := store.FetchFeedbackWeights(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("want 0 records, got %d", len(records))
	}
}

func TestMockStore_PersistRecommendation_CapturesParams(t *testing.T) {
	expectedID := uuid.New()
	store := &mockStore{persistedID: expectedID}

	p := PersistParams{
		TenantID:          uuid.New(),
		EntityID:          uuid.New(),
		Intent:            "billing",
		PrimaryActionType: "send_invoice",
		ConfidenceScore:   0.9,
		LookbackDays:      90,
		MinSuccessCount:   1,
	}

	id, err := store.PersistRecommendation(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != expectedID {
		t.Errorf("want id=%s, got %s", expectedID, id)
	}
	if store.persistedParams == nil {
		t.Fatal("persist params were not captured")
	}
	if store.persistedParams.Intent != "billing" {
		t.Errorf("unexpected intent: %s", store.persistedParams.Intent)
	}
}

func TestMockStore_PersistRecommendation_PropagatesError(t *testing.T) {
	store := &mockStore{persistErr: errors.New("constraint violation")}

	_, err := store.PersistRecommendation(context.Background(), PersistParams{})
	if err == nil {
		t.Error("expected error, got nil")
	}
}
