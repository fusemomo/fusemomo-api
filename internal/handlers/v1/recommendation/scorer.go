package recommendation

import (
	"math"
	"sort"
	"time"

	"fusemomo-api/internal/config"
	"fusemomo-api/internal/models"
)

// Scorer is the pure, stateless scoring engine for the L3 recommendation module.
// It has no DB dependency — feed it rows and feedback, get back a ranked opportunity set.
type Scorer struct {
	cfg config.Config
}

// NewScorer creates a Scorer configured with the supplied Config.
func NewScorer(cfg config.Config) *Scorer {
	return &Scorer{cfg: cfg}
}

// Score takes raw interaction buckets and historical feedback records, applies the
// composite scoring algorithm, and returns a ranked []OpportunityEntry.
//
// The first entry (IsPrimary = true) is the top recommendation.
// Entries with SuccessCount < minSuccessCount are excluded (defensive — SQL already filters).
// Returns an empty, non-nil slice when no qualifying pairs exist.
func (s *Scorer) Score(
	rows []models.RawInteractionRow,
	feedback []models.FeedbackRecord,
	minSuccessCount int,
) []models.OpportunityEntry {
	feedbackMap := buildFeedbackMap(feedback)

	entries := make([]models.OpportunityEntry, 0, len(rows))

	for _, r := range rows {
		// Defensive filter — SQL already applies HAVING, but guard in case rows
		// arrive from a different source (e.g. tests or future cache layer).
		if r.SuccessCount < int64(minSuccessCount) {
			continue
		}

		rawRate := float64(r.SuccessCount) / float64(max64(r.TotalCount, 1))

		recency := s.computeRecency(r.LastSuccessAt)

		fbWeight := s.computeFeedbackWeight(r.API, r.ActionType, feedbackMap)

		// Composite: weighted sum; feedback contribution is its delta from neutral (1.0).
		composite := clamp(
			s.cfg.WEIGHT_SUCCESS_RATE*rawRate+
				s.cfg.WEIGHT_RECENCY*recency+
				s.cfg.WEIGHT_FEEDBACK*(fbWeight-1.0),
			0.0, 1.0,
		)

		entries = append(entries, models.OpportunityEntry{
			API:            r.API,
			ActionType:     r.ActionType,
			TotalCount:     r.TotalCount,
			SuccessCount:   r.SuccessCount,
			RawSuccessRate: round4(rawRate),
			RecencyScore:   round4(recency),
			FeedbackWeight: round4(fbWeight),
			CompositeScore: round4(composite),
			LastSuccessAt:  r.LastSuccessAt,
			LastOccurredAt: r.LastOccurredAt,
		})
	}

	// Sort: composite DESC, break ties by success_count DESC then recency DESC.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CompositeScore != entries[j].CompositeScore {
			return entries[i].CompositeScore > entries[j].CompositeScore
		}
		if entries[i].SuccessCount != entries[j].SuccessCount {
			return entries[i].SuccessCount > entries[j].SuccessCount
		}
		return entries[i].RecencyScore > entries[j].RecencyScore
	})

	if len(entries) > 0 {
		entries[0].IsPrimary = true
	}

	return entries
}

// computeRecency applies exponential decay based on days since last success.
// Returns 0 if the pair has never succeeded.
func (s *Scorer) computeRecency(lastSuccessAt *time.Time) float64 {
	if lastSuccessAt == nil {
		return 0.0
	}
	daysSince := time.Since(*lastSuccessAt).Hours() / 24.0
	return math.Exp(-s.cfg.RECENCY_DECAY_LAMBDA * daysSince)
}

// computeFeedbackWeight returns the feedback-adjusted weight for a (api, action_type) pair.
// Neutral = 1.0 (no feedback history). >1.0 = more successes than failures. <1.0 = opposite.
func (s *Scorer) computeFeedbackWeight(api, actionType string, feedbackMap map[string]models.FeedbackRecord) float64 {
	fb, ok := feedbackMap[api+"|"+actionType]
	if !ok {
		return 1.0
	}
	delta := float64(fb.SucceededCount) - float64(fb.FollowedCount-fb.SucceededCount)
	return clamp(
		1.0+delta/math.Max(float64(fb.FollowedCount), 1),
		s.cfg.FEEDBACK_WEIGHT_MIN,
		s.cfg.FEEDBACK_WEIGHT_MAX,
	)
}

// buildFeedbackMap indexes feedback records by "api|action_type" for O(1) lookup.
func buildFeedbackMap(records []models.FeedbackRecord) map[string]models.FeedbackRecord {
	m := make(map[string]models.FeedbackRecord, len(records))
	for _, r := range records {
		m[r.API+"|"+r.ActionType] = r
	}
	return m
}

// clamp constrains v to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// round4 rounds to 4 decimal places.
func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

// max64 returns the larger of two int64 values.
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
