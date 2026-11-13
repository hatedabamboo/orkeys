package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hatedabamboo/orkeys/internal/provider"
)

const trackedKey = "prod-chat-core-legacy"

const trackedManifest = `name: prod-chat-core-legacy
kind: production
provider: openrouter
env: prod
service: legacy
description: Legacy service, key made in the dashboard.
import: {id: legacy-id, name: legacy}
track_only: true
budget: {limit: 50}
`

func trackedFile() string { return "teams/chat-core/" + trackedKey + ".yaml" }

func TestTrackOnlyKey(t *testing.T) {
	v := setup(t)
	ctx := context.Background()
	limit, usage := 30.0, 0.0
	v.or.keys = append(v.or.keys,
		provider.Key{ID: "legacy-id", Name: "legacy", LimitUSD: &limit, LimitReset: "weekly", UsageUSD: &usage},
		provider.Key{ID: "legacy-2", Name: "legacy2", UsageUSD: &usage})
	if err := os.WriteFile(filepath.Join(v.root, trackedFile()), []byte(trackedManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	v.reload(t)

	if got := types(v.plan(t)); !strings.Contains(got, "track:"+trackedKey) {
		t.Fatalf("plan: %s", got)
	}
	v.apply(t)
	st, ok := v.store.states[trackedKey]
	if !ok || !st.TrackOnly || st.KeyID != "legacy-id" || st.LimitUSD != 50 || st.AlertChannel != "#chat-core-alerts" ||
		st.Service != "legacy" || len(st.Secrets) != 0 || !st.ExpiresAt.IsZero() {
		t.Fatalf("state: %+v", st)
	}
	if k := v.or.find("legacy-id"); *k.LimitUSD != 30 || k.LimitReset != "weekly" {
		t.Fatalf("provider limit changed: %+v", k)
	}
	if !v.events.Has(EventKeyTracked, "legacy-id") {
		t.Fatalf("events: %v", v.events.Events)
	}
	if p := v.plan(t); !p.Empty() || fmt.Sprint(p.Unmanaged) != "[{openrouter legacy-2 legacy2}]" {
		t.Fatalf("plan after track: %s, unmanaged %v", types(p), p.Unmanaged)
	}

	v.edit(t, trackedFile(), "limit: 50", "limit: 60")
	if got := types(v.plan(t)); got != "update-state:"+trackedKey {
		t.Fatalf("budget change: %s", got)
	}
	v.apply(t)
	if v.store.states[trackedKey].LimitUSD != 60 || *v.or.find("legacy-id").LimitUSD != 30 {
		t.Fatal("budget change reached the provider or missed the state")
	}

	v.clk.add(days(400))
	if err := v.rotate(t, RotateOptions{Only: []string{trackedKey}, Now: []string{trackedKey}}); err != nil {
		t.Fatal(err)
	}
	if st := v.store.states[trackedKey]; st.KeyID != "legacy-id" || v.or.find("legacy-id") == nil {
		t.Fatalf("rotate changed a tracked key: %+v", st)
	}
	findings, err := v.e.Hygiene(ctx, true)
	if err != nil || strings.Contains(fmt.Sprint(findings), trackedKey) {
		t.Fatalf("hygiene: %v %v", findings, err)
	}
	if err := v.e.Revoke(ctx, []string{trackedKey}, ""); err == nil || !strings.Contains(err.Error(), "tracked key") {
		t.Fatalf("revoke: %v", err)
	}

	v.edit(t, trackedFile(), "track_only: true", "secret: {path: kv/llm/prod/chat-core/legacy}")
	if p := v.plan(t); !p.Empty() || !strings.Contains(fmt.Sprint(p.Warnings), "track_only removed from a tracked key") {
		t.Fatalf("track removed: %s %v", types(p), p.Warnings)
	}
	v.edit(t, trackedFile(), "secret: {path: kv/llm/prod/chat-core/legacy}", "track_only: true")

	v.edit(t, trackedFile(), "{id: legacy-id, name: legacy}", "{id: legacy-2, name: legacy2}")
	p := v.plan(t)
	if types(p) != "track:"+trackedKey || !strings.Contains(p.Actions[0].Detail, "legacy-id -> legacy-2") {
		t.Fatalf("import.id change: %s %+v", types(p), p.Actions)
	}
	v.apply(t)
	if st := v.store.states[trackedKey]; st.KeyID != "legacy-2" || v.or.find("legacy-id") == nil {
		t.Fatalf("after import.id change: %+v", st)
	}

	if err := os.Remove(filepath.Join(v.root, trackedFile())); err != nil {
		t.Fatal(err)
	}
	v.reload(t)
	p = v.plan(t)
	if types(p) != "untrack:"+trackedKey || p.Deletes() != 0 {
		t.Fatalf("manifest removed: %s", types(p))
	}
	v.apply(t)
	if _, ok := v.store.states[trackedKey]; ok || v.or.find("legacy-2") == nil || !v.events.Has(EventKeyUntracked, "legacy-2") {
		t.Fatal("untrack must remove only the state")
	}
}

func TestTrackOnManagedKey(t *testing.T) {
	v := setup(t)
	v.apply(t)
	id := v.store.states[stagingKey].KeyID
	v.edit(t, "teams/chat-core/"+stagingKey+".yaml", "secret:\n  path: kv/llm/staging/chat-core/evals",
		"track_only: true\nimport: {id: "+id+", name: x}")
	p := v.plan(t)
	if !p.Empty() || !strings.Contains(fmt.Sprint(p.Warnings), "track_only: true on a managed key") {
		t.Fatalf("plan: %s %v", types(p), p.Warnings)
	}
}

func TestTrackedStateIsNotRevoked(t *testing.T) {
	v := setup(t)
	v.apply(t)
	st := v.store.states[prodKey]
	st.TrackOnly = true
	if err := v.e.remove(context.Background(), &st, "test"); err == nil || v.or.find(st.KeyID) == nil {
		t.Fatalf("remove of a tracked state: %v", err)
	}
}

func TestTrackOnlyProvider(t *testing.T) {
	v := setup(t)
	v.apply(t)
	// Both files change before the reload: the inventory is valid only with both changes.
	replace := func(file, old, new string) {
		p := filepath.Join(v.root, file)
		b, _ := os.ReadFile(p)
		if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	replace("policy.yaml", "    management_key: kv/orkeys/providers/deepinfra\n",
		"    management_key: kv/orkeys/providers/deepinfra\n    options: {track_only: true}\n")
	replace("teams/chat-core/"+stagingKey+".yaml", "secret:\n  path: kv/llm/staging/chat-core/evals", "import: {id: x, name: x}")
	v.reload(t)
	if p := v.plan(t); !p.Empty() || !strings.Contains(fmt.Sprint(p.Warnings), stagingKey+": track_only: true on a managed key") {
		t.Fatalf("provider default on a managed key: %s %v", types(p), p.Warnings)
	}
	_, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "deepinfra", Purpose: "x"})
	if err == nil || !strings.Contains(err.Error(), "provider deepinfra: options.track_only is true") {
		t.Fatalf("request on a track-only provider: %v", err)
	}
}
