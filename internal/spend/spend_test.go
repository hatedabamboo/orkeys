package spend

import (
	"testing"
	"time"
)

func TestPeriodStarts(t *testing.T) {
	// 2026-11-17 is a Tuesday.
	day, week, month := PeriodStarts(time.Date(2026, 11, 17, 15, 4, 0, 0, time.UTC))
	if !day.Equal(time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)) ||
		!week.Equal(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)) ||
		!month.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(day, week, month)
	}
	// The week of 2026-12-02 (Wednesday) starts in November.
	if got := Start(time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(got)
	}
}

func TestByKey(t *testing.T) {
	now := time.Date(2026, 12, 2, 12, 0, 0, 0, time.UTC)
	d := func(day int, m time.Month) time.Time { return time.Date(2026, m, day, 0, 0, 0, 0, time.UTC) }
	got := ByKey([]DayCost{
		{Day: d(30, 11), KeyID: "a", USD: 1},
		{Day: d(1, 12), KeyID: "a", USD: 2},
		{Day: d(2, 12), KeyID: "a", USD: 4},
		{Day: d(2, 12), KeyID: "b", USD: 8},
	}, now)
	if a := got["a"]; a.Day != 4 || a.Week != 7 || a.Month != 6 {
		t.Fatalf("a: %+v", a)
	}
	if b := got["b"]; b.Day != 8 || b.Week != 8 || b.Month != 8 {
		t.Fatalf("b: %+v", b)
	}
}
