// Package spend sums the daily cost of each key into its spend in the current UTC day, week and month.
// Providers without a per-key spend in their key list (OpenAI, fal.ai) report cost per key and day.
package spend

import "time"

// DayCost is the cost of one key on one UTC day.
type DayCost struct {
	Day   time.Time
	KeyID string
	USD   float64
}

// Spend is the cost of one key in the current UTC day, week (from Monday) and month.
type Spend struct {
	Day   float64 `json:"day"`
	Week  float64 `json:"week"`
	Month float64 `json:"month"`
}

// PeriodStarts returns the start of the UTC day, week (Monday) and month of now.
func PeriodStarts(now time.Time) (day, week, month time.Time) {
	now = now.UTC()
	day = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	week = day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	month = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return day, week, month
}

// Start returns the first day that ByKey needs: the start of the week or of the month.
func Start(now time.Time) time.Time {
	_, week, month := PeriodStarts(now)
	if week.Before(month) {
		return week
	}
	return month
}

// ByKey sums day costs into the spend of each key in the periods of now.
func ByKey(costs []DayCost, now time.Time) map[string]Spend {
	day, week, month := PeriodStarts(now)
	out := map[string]Spend{}
	for _, c := range costs {
		s := out[c.KeyID]
		if !c.Day.Before(day) {
			s.Day += c.USD
		}
		if !c.Day.Before(week) {
			s.Week += c.USD
		}
		if !c.Day.Before(month) {
			s.Month += c.USD
		}
		out[c.KeyID] = s
	}
	return out
}
