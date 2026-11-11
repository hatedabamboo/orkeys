package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/state"
)

const (
	ghRepo = "repo:acme/dialog:OPENROUTER_API_KEY"
	ghEnv  = "env:acme/dialog:prod:OPENROUTER_API_KEY"
	ghOrg  = "org:acme:DIALOG_KEY"
)

const ghConsumers = `
  - type: github
    repo: acme/dialog
    name: OPENROUTER_API_KEY
  - type: github
    repo: acme/dialog
    environment: prod
    name: OPENROUTER_API_KEY
`

const prodFile = "teams/chat-core/" + prodKey + ".yaml"
const prodSecret = "kv/llm/prod/chat-core/dialog#api_key"

func TestGitHubConsumers(t *testing.T) {
	v := setup(t)
	gh := &fakeGitHub{}
	v.e.GitHub = gh
	v.edit(t, prodFile, "restart: [deployment/dialog]\n", "restart: [deployment/dialog]\n"+ghConsumers)

	// Create writes the GitHub secrets at once.
	v.apply(t)
	key := v.store.secrets[prodSecret]
	if gh.secrets[ghRepo] != key || gh.secrets[ghEnv] != key {
		t.Fatalf("github secrets after create: %v", gh.secrets)
	}
	if p := v.plan(t); !p.Empty() {
		t.Fatalf("plan after create: %s", types(p))
	}

	// A new consumer shows in the plan; a removed one is deleted.
	v.edit(t, prodFile, "    environment: prod\n    name: OPENROUTER_API_KEY\n",
		"    environment: prod\n    name: OPENROUTER_API_KEY\n  - {type: github, org: acme, name: DIALOG_KEY}\n")
	v.edit(t, prodFile, "  - type: github\n    repo: acme/dialog\n    name: OPENROUTER_API_KEY\n", "")
	p := v.plan(t)
	if got := types(p); got != "sync-github:"+prodKey || !strings.Contains(p.Actions[0].Detail, "write "+ghOrg) || !strings.Contains(p.Actions[0].Detail, "delete "+ghRepo) {
		t.Fatalf("plan: %s %+v", got, p.Actions)
	}
	v.apply(t)
	if _, ok := gh.secrets[ghRepo]; ok || gh.secrets[ghOrg] != key {
		t.Fatalf("github secrets after sync: %v", gh.secrets)
	}

	// Rotation writes the new key; a failed write fails the rotation and keeps the old key.
	oldID := v.store.states[prodKey].KeyID
	gh.fail = errors.New("HTTP 403")
	v.clk.add(days(85))
	_ = v.rotate(t, RotateOptions{})
	st := v.store.states[prodKey]
	if st.Rotation != state.RotationFailed || !strings.Contains(st.RotationError, "403") {
		t.Fatalf("want failed rotation: %+v", st)
	}
	if gh.secrets[ghOrg] != key {
		t.Fatal("github secret changed on failure")
	}
	if p := v.plan(t); types(p) != "" {
		t.Fatalf("plan must leave a rotation to rotate: %s", types(p))
	}
	gh.fail = nil
	if err := v.rotate(t, RotateOptions{Retry: []string{prodKey}}); err != nil {
		t.Fatal(err)
	}
	newKey := v.store.secrets[prodSecret]
	if newKey == key || gh.secrets[ghOrg] != newKey || gh.secrets[ghEnv] != newKey {
		t.Fatalf("github secrets after rotation: %v", gh.secrets)
	}
	v.clk.add(time.Hour)
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(24 * time.Hour)
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if v.or.find(oldID) != nil {
		t.Fatal("old key not revoked")
	}
	if p := v.plan(t); !p.Empty() {
		t.Fatalf("plan after rotation: %s", types(p))
	}

	// Removing the manifest deletes the GitHub secrets.
	if err := os.Remove(filepath.Join(v.root, prodFile)); err != nil {
		t.Fatal(err)
	}
	v.reload(t)
	v.apply(t)
	if len(gh.secrets) != 0 {
		t.Fatalf("github secrets after delete: %v", gh.secrets)
	}
}

func TestGitHubConsumerWithoutToken(t *testing.T) {
	v := setup(t)
	v.edit(t, prodFile, "restart: [deployment/dialog]\n", "restart: [deployment/dialog]\n"+ghConsumers)
	if err := v.e.Apply(t.Context(), v.plan(t)); err == nil || !strings.Contains(err.Error(), "no GitHub token") {
		t.Fatalf("want token error, got %v", err)
	}
	if !v.events.Has(EventGitHubSyncFailed, "no GitHub token") {
		t.Fatal("no alert")
	}
	// The key exists; the next plan only syncs.
	v.e.GitHub = &fakeGitHub{}
	if got := types(v.plan(t)); got != "sync-github:"+prodKey {
		t.Fatalf("plan: %s", got)
	}
}
