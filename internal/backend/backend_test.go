package backend

import "testing"

func TestParseState(t *testing.T) {
	cases := []struct{ in, scheme, a, b string }{
		{"vault://infra/orkeys/state", "vault", "infra/orkeys/state", ""},
		{"s3://bucket/orkeys/state/", "s3", "bucket", "orkeys/state"},
		{"s3://bucket", "s3", "bucket", ""},
	}
	for _, c := range cases {
		s, a, b, err := ParseState(c.in)
		if err != nil || s != c.scheme || a != c.a || b != c.b {
			t.Errorf("%s: %q %q %q %v", c.in, s, a, b, err)
		}
	}
	for _, in := range []string{"", "vault://infra", "s3:///x", "gcs://b/p", "infra/orkeys/state"} {
		if _, _, _, err := ParseState(in); err == nil {
			t.Errorf("%s: want error", in)
		}
	}
}
