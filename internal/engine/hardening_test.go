package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/state"
)

func TestApplyRefusesTooManyDeletes(t *testing.T) {
	v := setup(t)
	v.apply(t)
	v.edit(t, "policy.yaml", "secrets: vault", "secrets: vault\nmax_deletes: 1")
	for _, n := range []string{prodKey, stagingKey} {
		_ = os.Remove(filepath.Join(v.root, "teams/chat-core/"+n+".yaml"))
	}
	v.reload(t)

	p := v.plan(t)
	if p.Deletes() != 2 || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "max_deletes 1") {
		t.Fatalf("plan: %s, warnings %v", types(p), p.Warnings)
	}
	err := v.e.Apply(context.Background(), p, ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "--allow-deletes 2") {
		t.Fatalf("apply: %v", err)
	}
	if len(v.or.keys) != 1 || len(v.di.keys) != 1 {
		t.Fatal("a refused apply revoked keys")
	}
	if err := v.e.Apply(context.Background(), p, ApplyOptions{AllowDeletes: 2}); err != nil {
		t.Fatal(err)
	}
	if len(v.or.keys)+len(v.di.keys) != 0 {
		t.Fatal("keys not revoked with --allow-deletes")
	}
}

func TestCancelDuringIssueStillCleansUp(t *testing.T) {
	v := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v.store.beforePut = cancel // SIGTERM while the first secret is written

	if err := v.e.Apply(ctx, v.plan(t), ApplyOptions{}); err == nil {
		t.Fatal("want an error")
	}
	if len(v.or.keys)+len(v.di.keys) != 0 {
		t.Fatalf("new key left at the provider: %v %v", v.or.keys, v.di.keys)
	}
	if len(v.store.states) != 0 {
		t.Fatalf("state left: %v", v.store.states)
	}
}

// crash runs fn and swallows the panic that the fakes use to stop a run at a chosen point.
func crash(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

func TestResumeAfterCrashBeforeSecretWrite(t *testing.T) {
	v := setup(t)
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID
	v.clk.add(days(85))

	v.store.beforePut = func() { panic("killed") }
	crash(func() { _ = v.rotate(t, RotateOptions{}) })
	v.store.beforePut = nil

	st := v.store.states[prodKey]
	lostID := st.KeyID
	if st.Phase != state.PhaseIssuing || lostID == oldID || v.store.secrets[prodPath+"#api_key"] != "secret-1" {
		t.Fatalf("after crash: %+v, secret %q", st, v.store.secrets[prodPath+"#api_key"])
	}
	if got := types(v.plan(t)); !strings.Contains(got, "resume:"+prodKey) {
		t.Fatalf("plan: %s", got)
	}

	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	st = v.store.states[prodKey]
	if st.Phase != state.PhaseReady || st.KeyID == lostID || st.KeyID == oldID {
		t.Fatalf("after resume: %+v", st)
	}
	if v.store.meta[prodPath][secrets.MetaKeyID] != st.KeyID || v.store.secrets[prodPath+"#api_key"] == "secret-1" {
		t.Fatal("secret does not hold the reissued key")
	}
	var waiting []string
	for _, pr := range st.PendingRevokes {
		waiting = append(waiting, pr.KeyID)
		if pr.After.IsZero() {
			t.Fatalf("revoke of %s not scheduled after the consumer sync", pr.KeyID)
		}
	}
	if !slices.Contains(waiting, oldID) || !slices.Contains(waiting, lostID) {
		t.Fatalf("pending revokes %v, want %s and %s", waiting, oldID, lostID)
	}
	if !v.events.Has(EventRotationStart, "stopped before it wrote key") {
		t.Fatalf("no recovery alert: %+v", v.events.Events)
	}
}

func TestResumeWhenOnlyTheLastStateWriteFailed(t *testing.T) {
	v := setup(t)
	failed := 0
	v.store.onPutState = func(st *state.State) error {
		if st.Name == prodKey && st.Phase == state.PhaseReady && failed < 3 {
			failed++
			return errors.New("state store down")
		}
		return nil
	}
	_ = v.e.Apply(context.Background(), v.plan(t), ApplyOptions{})
	st := v.store.states[prodKey]
	if st.Phase != state.PhaseIssuing || v.store.meta[prodPath][secrets.MetaKeyID] != st.KeyID {
		t.Fatalf("setup: %+v", st)
	}

	v.apply(t)
	if got := v.store.states[prodKey]; got.Phase != state.PhaseReady || got.KeyID != st.KeyID || len(v.or.keys) != 1 {
		t.Fatalf("resume must keep the written key: %+v, keys %v", got, v.or.keys)
	}
	if p := v.plan(t); !p.Empty() {
		t.Fatalf("plan after resume: %s", types(p))
	}
}

func TestPlanReissuesOnSecretDrift(t *testing.T) {
	v := setup(t)
	v.apply(t)
	v.store.meta[prodPath] = map[string]string{secrets.MetaKeyID: "someone-else"}
	p := v.plan(t)
	if got := types(p); got != "reissue:"+prodKey || !strings.Contains(p.Actions[0].Detail, "holds key someone-els") {
		t.Fatalf("plan: %s %v", got, p.Actions)
	}
}

func TestOldKeyUsedAfterSwitchByLastUse(t *testing.T) {
	v := setup(t)
	v.or.caps.LastUsedExact = true
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID
	v.clk.add(days(84))
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(time.Hour)
	_ = v.rotate(t, RotateOptions{})

	// Spend does not move (a provider with delayed costs), but the last use is after the switch.
	used := v.clk.t
	v.or.find(oldID).LastUsedAt = &used
	v.clk.add(24 * time.Hour)
	_ = v.rotate(t, RotateOptions{})
	st := v.store.states[prodKey]
	if v.or.find(oldID) == nil || st.Rotation != state.RotationFailed || !strings.Contains(st.RotationError, "used at") {
		t.Fatalf("old key used after the switch must not be revoked: %+v", st)
	}
}

func TestSpendResetTakesNewBaseline(t *testing.T) {
	v := setup(t)
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID
	high := 40.0
	v.or.find(oldID).UsageUSD = &high
	v.clk.add(days(84))
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(time.Hour)
	_ = v.rotate(t, RotateOptions{})

	low := 0.2 // a new month starts
	v.or.find(oldID).UsageUSD = &low
	v.clk.add(24 * time.Hour)
	_ = v.rotate(t, RotateOptions{})
	pr := v.store.states[prodKey].PendingRevokes
	if v.or.find(oldID) == nil || len(pr) != 1 || *pr[0].Baseline != low {
		t.Fatalf("a spend reset must take a new baseline, not revoke: %+v", pr)
	}
	v.clk.add(time.Hour)
	_ = v.rotate(t, RotateOptions{})
	if v.or.find(oldID) != nil {
		t.Fatal("not revoked after the new baseline stayed the same")
	}
}

func TestNewerSchemaStopsTheRun(t *testing.T) {
	v := setup(t)
	v.apply(t)
	st := v.store.states[prodKey]
	st.Schema = state.SchemaVersion + 1
	v.store.states[prodKey] = st
	if _, err := v.e.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "upgrade orkeys") {
		t.Fatalf("plan: %v", err)
	}
	if err := v.rotate(t, RotateOptions{}); err == nil || !strings.Contains(err.Error(), "upgrade orkeys") {
		t.Fatalf("rotate: %v", err)
	}
}

func TestStateConflictIsNotRetriedOrOverwritten(t *testing.T) {
	v := setup(t)
	v.apply(t)
	v.edit(t, "teams/chat-core/"+prodKey+".yaml", "limit: 200", "limit: 300")
	p := v.plan(t)

	// Another process changes the state after the plan read it.
	other := v.store.states[prodKey]
	other.Service = "changed elsewhere"
	if err := v.store.PutState(context.Background(), &other); err != nil {
		t.Fatal(err)
	}
	err := v.e.Apply(context.Background(), p, ApplyOptions{})
	if !errors.Is(err, state.ErrConflict) {
		t.Fatalf("want a conflict, got %v", err)
	}
	if got := v.store.states[prodKey]; got.Service != "changed elsewhere" {
		t.Fatalf("the other write was lost: %+v", got)
	}
}

func TestRequestNameCollision(t *testing.T) {
	v := setup(t)
	r := Request{User: "jdoe", Kind: "local", Provider: "openrouter", Purpose: "dev"}
	if _, err := v.request(t, r); err != nil {
		t.Fatal(err)
	}
	r.Kind = "personal"
	if _, err := v.request(t, r); err != nil {
		t.Fatal(err)
	}
	keys := len(v.or.keys)
	_, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "openrouter", Purpose: "again"})
	if err == nil || !strings.Contains(err.Error(), "exists already") || len(v.or.keys) != keys {
		t.Fatalf("second request in the same second: %v, keys %d -> %d", err, keys, len(v.or.keys))
	}
}

func TestSavedPlanMatches(t *testing.T) {
	v := setup(t)
	saved := v.plan(t)
	if saved.Fingerprint == "" {
		t.Fatal("no fingerprint")
	}
	if err := saved.Matches(v.plan(t)); err != nil {
		t.Fatalf("same plan: %v", err)
	}
	v.edit(t, "teams/chat-core/"+prodKey+".yaml", "limit: 200", "limit: 300")
	err := saved.Matches(v.plan(t))
	if err == nil || !strings.Contains(err.Error(), "+ create "+prodKey) || !strings.Contains(err.Error(), "- create "+prodKey) {
		t.Fatalf("changed plan: %v", err)
	}
}

func TestInexactLastUseDoesNotBlockRevoke(t *testing.T) {
	v := setup(t)
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID
	v.clk.add(days(84))
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(time.Hour)
	_ = v.rotate(t, RotateOptions{})
	used := v.clk.t
	v.or.find(oldID).LastUsedAt = &used // OpenRouter: the time of the list request, not of the last use
	v.clk.add(24 * time.Hour)
	_ = v.rotate(t, RotateOptions{})
	if v.or.find(oldID) != nil {
		t.Fatal("an inexact last use must not keep the old key")
	}
}
