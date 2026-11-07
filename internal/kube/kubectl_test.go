package kube

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestForceSyncAndRestart(t *testing.T) {
	var calls []string
	polls := 0
	k := &Kubectl{
		Context:   func(c string) string { return "ctx-" + c },
		PollEvery: time.Millisecond,
		run: func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			if args[4] == "get" {
				polls++
				if polls < 2 {
					return []byte(`{"status":{"refreshTime":"2000-01-01T00:00:00Z","conditions":[{"type":"Ready","status":"True"}]}}`), nil
				}
				now := time.Now().UTC().Add(time.Second).Format(time.RFC3339)
				return []byte(`{"status":{"refreshTime":"` + now + `","conditions":[{"type":"Ready","status":"True"}]}}`), nil
			}
			return nil, nil
		},
	}
	if err := k.ForceSync(context.Background(), "prod", "ns", "es"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(calls[0], "--context ctx-prod --namespace ns annotate externalsecret es force-sync=") || polls != 2 {
		t.Fatalf("calls: %v", calls)
	}
	calls = nil
	if err := k.Restart(context.Background(), "prod", "ns", "deploy/api"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "rollout restart deployment/api") || !strings.Contains(calls[1], "rollout status deployment/api --timeout 10m0s") {
		t.Fatalf("calls: %v", calls)
	}
	calls = nil
	_ = k.Restart(context.Background(), "prod", "ns", "cronjob/nightly")
	if len(calls) != 0 {
		t.Fatal("cronjobs must not be restarted")
	}
}

func TestSynced(t *testing.T) {
	since := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]bool{
		`{"status":{"refreshTime":"2026-01-01T12:00:00Z","conditions":[{"type":"Ready","status":"True"}]}}`:  true,
		`{"status":{"refreshTime":"2026-01-01T11:59:59Z","conditions":[{"type":"Ready","status":"True"}]}}`:  false,
		`{"status":{"refreshTime":"2026-01-01T12:00:05Z","conditions":[{"type":"Ready","status":"False"}]}}`: false,
		`{"status":{"refreshTime":"2026-01-01T12:00:05Z"}}`:                                                  false,
		`{"status":{}}`: false,
	}
	for raw, want := range cases {
		got, err := synced([]byte(raw), since)
		if err != nil || got != want {
			t.Errorf("%s: got %v %v, want %v", raw, got, err, want)
		}
	}
	if _, err := synced([]byte(`{"status":{"refreshTime":"yesterday"}}`), since); err == nil {
		t.Error("bad refreshTime must be an error")
	}
}

func TestKubectlErrors(t *testing.T) {
	k := &Kubectl{Bin: "/bin/sh", Context: func(c string) string { return c }}
	if _, err := k.exec(context.Background(), "c", "ns", "-c", "echo boom >&2; exit 3"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr must be in the error: %v", err)
	}
	k = &Kubectl{Context: func(c string) string { return c }, SyncTimeout: 5 * time.Millisecond, PollEvery: time.Millisecond,
		run: func(_ context.Context, args ...string) ([]byte, error) {
			return []byte(`{"status":{"refreshTime":"2000-01-01T00:00:00Z"}}`), nil
		}}
	if err := k.ForceSync(context.Background(), "c", "ns", "es"); err == nil || !strings.Contains(err.Error(), "not synced") {
		t.Fatalf("want timeout, got %v", err)
	}
}
