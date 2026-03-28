package recommendation

import (
	"testing"
	"time"

	"fusemomo-api/internal/config"
	"fusemomo-api/internal/models"
)

// helpers

func defaultScorer() *Scorer {
	return NewScorer(config.Envs)
}

func now() time.Time { return time.Now().UTC() }

func daysAgo(d float64) time.Time {
	return now().Add(-time.Duration(d * 24 * float64(time.Hour)))
}

func ptr(t time.Time) *time.Time { return &t }

func makeRow(api, action string, total, success int64, lastSuccess *time.Time, lastOccurred time.Time) models.RawInteractionRow {
	return models.RawInteractionRow{
		API:            api,
		ActionType:     action,
		TotalCount:     total,
		SuccessCount:   success,
		LastSuccessAt:  lastSuccess,
		LastOccurredAt: lastOccurred,
	}
}

// tests

func TestScore_SinglePair_HighSuccessRate(t *testing.T) {
	s := defaultScorer()
	t0 := daysAgo(1)

	rows := []models.RawInteractionRow{
		makeRow("stripe", "send_invoice", 10, 10, ptr(t0), t0),
	}

	entries := s.Score(rows, nil, 1)

	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if !e.IsPrimary {
		t.Error("want IsPrimary = true for only entry")
	}
	if e.CompositeScore < 0.80 {
		t.Errorf("want composite_score near 1.0 for 100%% success rate, got %.4f", e.CompositeScore)
	}
}

func TestScore_MultiplePairs_SortedDescending(t *testing.T) {
	s := defaultScorer()
	recent := ptr(daysAgo(2))

	rows := []models.RawInteractionRow{
		makeRow("twilio", "send_sms", 10, 3, ptr(daysAgo(30)), daysAgo(2)),     // low success rate
		makeRow("stripe", "send_invoice", 10, 9, recent, daysAgo(2)),           // high
		makeRow("stripe", "apply_coupon", 10, 6, ptr(daysAgo(10)), daysAgo(2)), // medium
	}

	entries := s.Score(rows, nil, 1)

	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].CompositeScore > entries[i-1].CompositeScore {
			t.Errorf("entries not sorted descending: [%d]=%.4f > [%d]=%.4f",
				i, entries[i].CompositeScore, i-1, entries[i-1].CompositeScore)
		}
	}
	if !entries[0].IsPrimary {
		t.Error("first entry must be primary")
	}
}

func TestScore_RecencyDecay_OldVsRecent(t *testing.T) {
	s := defaultScorer()
	// Same success rate but different recency.
	old := daysAgo(90)
	recent := daysAgo(2)

	rowOld := makeRow("api_a", "act_x", 10, 9, ptr(old), old)
	rowRecent := makeRow("api_b", "act_x", 10, 9, ptr(recent), recent)

	onlyOld := s.Score([]models.RawInteractionRow{rowOld}, nil, 1)
	onlyRecent := s.Score([]models.RawInteractionRow{rowRecent}, nil, 1)

	if onlyOld[0].RecencyScore >= onlyRecent[0].RecencyScore {
		t.Errorf("old recency (%.4f) should be lower than recent recency (%.4f)",
			onlyOld[0].RecencyScore, onlyRecent[0].RecencyScore)
	}
}

func TestScore_ZeroSuccessCount_ExcludedFromSet(t *testing.T) {
	s := defaultScorer()
	t0 := daysAgo(1)

	rows := []models.RawInteractionRow{
		makeRow("api_a", "act_fail", 5, 0, nil, t0), // no successes
	}

	entries := s.Score(rows, nil, 1)

	if len(entries) != 0 {
		t.Errorf("want 0 entries (pair with 0 successes excluded), got %d", len(entries))
	}
}

func TestScore_FeedbackBoost_IncreasesScore(t *testing.T) {
	s := defaultScorer()
	t0 := ptr(daysAgo(5))
	t1 := daysAgo(5)

	rows := []models.RawInteractionRow{
		makeRow("stripe", "send_invoice", 10, 7, t0, t1),
	}

	// Without feedback
	baseline := s.Score(rows, nil, 1)

	// With positive feedback (10 followed, 9 succeeded)
	feedback := []models.FeedbackRecord{
		{API: "stripe", ActionType: "send_invoice", FollowedCount: 10, SucceededCount: 9},
	}
	boosted := s.Score(rows, feedback, 1)

	if boosted[0].CompositeScore <= baseline[0].CompositeScore {
		t.Errorf("positive feedback should boost score: baseline=%.4f boosted=%.4f",
			baseline[0].CompositeScore, boosted[0].CompositeScore)
	}
}

func TestScore_FeedbackPenalty_DecreasesScore(t *testing.T) {
	s := defaultScorer()
	t0 := ptr(daysAgo(5))
	t1 := daysAgo(5)

	rows := []models.RawInteractionRow{
		makeRow("stripe", "send_invoice", 10, 7, t0, t1),
	}

	baseline := s.Score(rows, nil, 1)

	// Negative feedback: followed 10 times, succeeded only 1
	feedback := []models.FeedbackRecord{
		{API: "stripe", ActionType: "send_invoice", FollowedCount: 10, SucceededCount: 1},
	}
	penalised := s.Score(rows, feedback, 1)

	if penalised[0].CompositeScore >= baseline[0].CompositeScore {
		t.Errorf("negative feedback should reduce score: baseline=%.4f penalised=%.4f",
			baseline[0].CompositeScore, penalised[0].CompositeScore)
	}
}

func TestScore_EmptyRows_ReturnsEmptySlice(t *testing.T) {
	s := defaultScorer()
	entries := s.Score(nil, nil, 1)
	if entries == nil {
		t.Error("want empty slice, got nil")
	}
	if len(entries) != 0 {
		t.Errorf("want 0 entries, got %d", len(entries))
	}
}

func TestScore_MinSuccessCount_FiltersLowSuccessPairs(t *testing.T) {
	s := defaultScorer()
	t0 := ptr(daysAgo(3))
	t1 := daysAgo(3)

	rows := []models.RawInteractionRow{
		makeRow("api_a", "act_x", 10, 2, t0, t1), // only 2 successes
		makeRow("api_b", "act_y", 10, 5, t0, t1), // 5 successes — qualifies
	}

	entries := s.Score(rows, nil, 3) // min_success_count = 3

	if len(entries) != 1 {
		t.Fatalf("want 1 entry (min_success_count=3 filters act_x), got %d", len(entries))
	}
	if entries[0].ActionType != "act_y" {
		t.Errorf("want act_y to survive filter, got %s", entries[0].ActionType)
	}
}

func TestScore_MultipleAPIs_SameActionType_TreatedDistinct(t *testing.T) {
	s := defaultScorer()
	t0 := ptr(daysAgo(3))
	t1 := daysAgo(3)

	rows := []models.RawInteractionRow{
		makeRow("github", "create_issue", 10, 9, t0, t1),
		makeRow("gitlab", "create_issue", 10, 5, t0, t1),
	}

	entries := s.Score(rows, nil, 1)

	if len(entries) != 2 {
		t.Fatalf("want 2 distinct entries (different APIs), got %d", len(entries))
	}
	// github should rank higher due to more successes
	if entries[0].API != "github" {
		t.Errorf("want github first (higher score), got %s", entries[0].API)
	}
}

func TestScore_NullLastSuccessAt_RecencyIsZero(t *testing.T) {
	s := defaultScorer()
	// A pair that has occurrences but no successful ones is excluded (SuccessCount=0 < min=1).
	// Test instead a pair with success but nil LastSuccessAt (shouldn't happen in practice
	// but the scorer must not panic).
	rows := []models.RawInteractionRow{
		makeRow("api_a", "act_x", 5, 3, nil, daysAgo(1)),
	}

	entries := s.Score(rows, nil, 1)

	// Entry included because SuccessCount=3 >= min=1
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	// RecencyScore must be 0.0 when LastSuccessAt is nil
	if entries[0].RecencyScore != 0.0 {
		t.Errorf("want recency_score=0 for nil last_success_at, got %.4f", entries[0].RecencyScore)
	}
}

func TestScore_CompositeScoreClamped(t *testing.T) {
	s := defaultScorer()
	t0 := ptr(now())

	// Pathological case: 100% success, happened just now, extreme positive feedback
	rows := []models.RawInteractionRow{
		makeRow("api_a", "act_x", 100, 100, t0, now()),
	}
	feedback := []models.FeedbackRecord{
		{API: "api_a", ActionType: "act_x", FollowedCount: 100, SucceededCount: 100},
	}

	entries := s.Score(rows, feedback, 1)
	if entries[0].CompositeScore > 1.0 {
		t.Errorf("composite_score must be <= 1.0, got %.4f", entries[0].CompositeScore)
	}
}
