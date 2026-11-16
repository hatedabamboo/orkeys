package cli

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestEnvDebug(t *testing.T) {
	for _, tc := range []struct {
		val     string
		want    bool
		wantErr bool
	}{
		{"", false, false},
		{"1", true, false},
		{"true", true, false},
		{"false", false, false},
		{"yes", false, true},
	} {
		t.Setenv(EnvDebug, tc.val)
		got, err := envDebug()
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s=%q: got %v, %v", EnvDebug, tc.val, got, err)
		}
	}
}

func TestNewLogHandler(t *testing.T) {
	for _, tc := range []struct {
		format string
		debug  bool
		want   string
	}{
		{"text", false, ""},
		{"text", true, "level=DEBUG msg=probe"},
		{"json", true, `"level":"DEBUG","msg":"probe"`},
	} {
		var buf bytes.Buffer
		slog.New(newLogHandler(&buf, tc.format, tc.debug)).DebugContext(context.Background(), "probe")
		if got := buf.String(); (tc.want == "" && got != "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s debug=%v: got %q, want %q", tc.format, tc.debug, got, tc.want)
		}
	}
}

func TestDebugFlag(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)
	t.Setenv(EnvDebug, "maybe")
	cmd := (&app{version: "test"}).root()
	cmd.SetArgs([]string{"validate", "--repo", t.TempDir()})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), EnvDebug) {
		t.Fatalf("bad %s: got %v, want an error that names it", EnvDebug, err)
	}
	// The flag has priority over the env.
	cmd = (&app{version: "test"}).root()
	cmd.SetArgs([]string{"validate", "--repo", t.TempDir(), "--debug=false"})
	if err := cmd.Execute(); err != nil && strings.Contains(err.Error(), EnvDebug) {
		t.Fatalf("--debug=false: env still read: %v", err)
	}
}
