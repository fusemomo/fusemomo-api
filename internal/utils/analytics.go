package utils

import (
	"math"
	"time"
)

// ValidAnalyticsPeriods enumerates the periods accepted by every analytics handler.
var ValidAnalyticsPeriods = map[string]bool{
	"7d": true, "30d": true, "90d": true, "all": true,
}

// AnalyticsPeriodRange converts a period string into UTC start/end times.
// "all" returns a start far in the past (year 2000) so no rows are excluded.
func AnalyticsPeriodRange(period string) (start, end time.Time) {
	now := time.Now().UTC()
	end = now
	switch period {
	case "7d":
		start = now.AddDate(0, 0, -7)
	case "30d":
		start = now.AddDate(0, 0, -30)
	case "90d":
		start = now.AddDate(0, 0, -90)
	case "all":
		start = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return
}

// PreviousPeriodRange returns the immediately preceding period's start and end.
func PreviousPeriodRange(period string) (start, end time.Time) {
	now := time.Now().UTC()
	switch period {
	case "7d":
		end = now.AddDate(0, 0, -7)
		start = now.AddDate(0, 0, -14)
	case "30d":
		end = now.AddDate(0, 0, -30)
		start = now.AddDate(0, 0, -60)
	case "90d":
		end = now.AddDate(0, 0, -90)
		start = now.AddDate(0, 0, -180)
	default: // "all" — no meaningful previous period
		start, end = time.Time{}, time.Time{}
	}
	return
}

// Trend returns "up", "down", or "neutral" from a signed change value.
func Trend(change float64) string {
	if change > 0 {
		return "up"
	} else if change < 0 {
		return "down"
	}
	return "neutral"
}

// Round1 rounds to one decimal place.
func Round1(v float64) float64 { return math.Round(v*10) / 10 }

// Round2 rounds to two decimal places.
func Round2(v float64) float64 { return math.Round(v*100) / 100 }

// Round3 rounds to three decimal places.
func Round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// AnalyticsPredefinedColors is a fixed palette for API distribution pie charts.
var AnalyticsPredefinedColors = []string{
	"#6366f1", "#10b981", "#f59e0b", "#ef4444",
	"#8b5cf6", "#06b6d4", "#ec4899", "#84cc16",
	"#f97316", "#64748b",
}
