// Package values parses user input and formats key fields. CLI and interactive mode share it.
package values

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/nullable"
)

// LimitResets are the values the API accepts for limit_reset, plus "none" for null.
var LimitResets = []string{"none", "daily", "weekly", "monthly"}

// ParseLimit parses a USD limit. "", "none", "null" and "unlimited" mean null.
func ParseLimit(s string) (nullable.Value[float64], error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	switch strings.ToLower(s) {
	case "", "none", "null", "unlimited":
		return nullable.Null[float64](), nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || v < 0 || v >= 1_000_000 {
		return nullable.Value[float64]{}, fmt.Errorf("invalid limit %q: want a number in [0, 1000000) or \"none\"", s)
	}
	return nullable.Of(v), nil
}

// ParseLimitReset parses daily|weekly|monthly. "", "none" and "null" mean null.
func ParseLimitReset(s string) (nullable.Value[string], error) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "", "none", "null", "never":
		return nullable.Null[string](), nil
	case "daily", "weekly", "monthly":
		return nullable.Of(v), nil
	default:
		return nullable.Value[string]{}, fmt.Errorf("invalid limit reset %q: want daily|weekly|monthly|none", s)
	}
}

// ParseBool accepts true/false, yes/no, y/n, 1/0, on/off.
func ParseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "y", "1", "on":
		return true, nil
	case "false", "no", "n", "0", "off":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q", s)
}

// ParseExpiry turns user input into an API timestamp (UTC, with seconds).
// Accepted: "" / "never" / "none" (no expiry), a duration ("30d", "2w", "36h"),
// a date ("2027-12-31", midnight UTC) or an RFC 3339 timestamp.
func ParseExpiry(s string, now time.Time) (*string, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "none", "never", "null":
		return nil, nil
	}

	t, err := parseExpiryTime(s, now)
	if err != nil {
		return nil, err
	}
	if !t.After(now) {
		return nil, fmt.Errorf("expiry %q is in the past", s)
	}
	out := t.UTC().Format("2006-01-02T15:04:05Z")
	return &out, nil
}

func parseExpiryTime(s string, now time.Time) (time.Time, error) {
	if d, ok := parseDays(s); ok {
		return now.Add(d), nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(d), nil
	}
	layouts := []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid expiry %q: use a duration (30d, 2w, 12h), a date (2027-12-31) or RFC 3339", s)
}

func parseDays(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch unit {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, true
	}
	return 0, false
}

// IsFullHash reports whether s looks like a complete key hash (64 hex chars).
func IsFullHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// ShortHash returns the first 12 characters of a hash.
func ShortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// USD formats a dollar amount. Small non-zero amounts keep 4 decimals.
func USD(v float64) string {
	if v != 0 && math.Abs(v) < 0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// USDPtr formats a nullable amount. Nil prints as "-".
func USDPtr(v *float64) string {
	if v == nil {
		return "-"
	}
	return USD(*v)
}

// Str returns *s, or "-" when nil or empty.
func Str(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

// Time formats an API timestamp as "2006-01-02 15:04" UTC. Nil or empty prints "-".
func Time(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	for _, l := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(l, *s); err == nil {
			return t.UTC().Format("2006-01-02 15:04")
		}
	}
	return *s
}

// YesNo formats a bool.
func YesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if d, ok := parseDays(s); ok {
		return d, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q: use 90d, 2w, 24h or 30m", s)
	}
	return d, nil
}

func Days(d time.Duration) string {
	day := 24 * time.Hour
	if d > 0 && d%day == 0 {
		return fmt.Sprintf("%dd", d/day)
	}
	return d.String()
}
