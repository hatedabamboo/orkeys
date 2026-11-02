package values

import (
	"testing"
	"time"
)

func TestParseLimit(t *testing.T) {
	cases := []struct {
		in      string
		valid   bool
		v       float64
		wantErr bool
	}{
		{"", false, 0, false},
		{"none", false, 0, false},
		{"$12.5", true, 12.5, false},
		{"0", true, 0, false},
		{"-1", false, 0, true},
		{"1000000", false, 0, true},
		{"abc", false, 0, true},
	}
	for _, c := range cases {
		got, err := ParseLimit(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseLimit(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && (got.Valid != c.valid || got.V != c.v || !got.Set) {
			t.Errorf("ParseLimit(%q) = %+v", c.in, got)
		}
	}
}

func TestParseLimitReset(t *testing.T) {
	for _, in := range []string{"daily", "Weekly", "monthly"} {
		if v, err := ParseLimitReset(in); err != nil || !v.Valid {
			t.Errorf("ParseLimitReset(%q) = %+v, %v", in, v, err)
		}
	}
	if v, err := ParseLimitReset("none"); err != nil || v.Valid || !v.Set {
		t.Errorf("ParseLimitReset(none) = %+v, %v", v, err)
	}
	if _, err := ParseLimitReset("yearly"); err == nil {
		t.Error("ParseLimitReset(yearly) want error")
	}
}

func TestParseExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"30d":                       "2026-01-31T12:00:00Z",
		"2w":                        "2026-01-15T12:00:00Z",
		"36h":                       "2026-01-03T00:00:00Z",
		"2027-12-31":                "2027-12-31T00:00:00Z",
		"2027-12-31T23:59":          "2027-12-31T23:59:00Z",
		"2027-12-31T23:59:59+02:00": "2027-12-31T21:59:59Z",
	}
	for in, want := range cases {
		got, err := ParseExpiry(in, now)
		if err != nil || got == nil || *got != want {
			t.Errorf("ParseExpiry(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	if got, err := ParseExpiry("never", now); err != nil || got != nil {
		t.Errorf("ParseExpiry(never) = %v, %v", got, err)
	}
	for _, bad := range []string{"2020-01-01", "-5d", "soon"} {
		if _, err := ParseExpiry(bad, now); err == nil {
			t.Errorf("ParseExpiry(%q) want error", bad)
		}
	}
}

func TestIsFullHash(t *testing.T) {
	full := "f01d52606dc8f0a8303a7b5cc3fa07109c2e346cec7c0a16b40de462992ce943"
	if !IsFullHash(full) || IsFullHash(full[:12]) || IsFullHash("z"+full[1:]) {
		t.Error("IsFullHash mismatch")
	}
}
