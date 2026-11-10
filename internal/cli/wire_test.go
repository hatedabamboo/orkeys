package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/engine"
	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

type lockStore struct {
	mu       sync.Mutex
	cur      *state.LockInfo
	released []string
	steal    bool
}

func (l *lockStore) ListStates(context.Context) ([]*state.State, error)     { return nil, nil }
func (l *lockStore) GetState(context.Context, string) (*state.State, error) { return nil, nil }
func (l *lockStore) PutState(context.Context, *state.State) error           { return nil }
func (l *lockStore) DeleteState(context.Context, string) error              { return nil }
func (l *lockStore) Current(context.Context) (*state.LockInfo, error)       { return l.cur, nil }
func (l *lockStore) Acquire(_ context.Context, info state.LockInfo) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur != nil {
		return &state.LockedError{Info: *l.cur}
	}
	l.cur = &info
	return nil
}
func (l *lockStore) Renew(_ context.Context, info state.LockInfo) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.steal {
		l.cur = &state.LockInfo{ID: "thief", Expires: time.Now().Add(time.Hour)}
	}
	if l.cur == nil || l.cur.ID != info.ID {
		return state.ErrLockLost
	}
	return nil
}
func (l *lockStore) Release(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = append(l.released, id)
	if l.cur != nil && l.cur.ID == id {
		l.cur = nil
	}
	return nil
}

func TestWithLock(t *testing.T) {
	ls := &lockStore{}
	e := &engine.Engine{State: ls}
	a := &app{}
	ctx := context.Background()

	boom := errors.New("boom")
	if err := a.withLock(ctx, e, "apply", func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("want fn error, got %v", err)
	}
	if ls.cur != nil || len(ls.released) != 1 {
		t.Fatal("lock not released after an error")
	}

	ls.cur = &state.LockInfo{ID: "other", Holder: "run-9", Expires: time.Now().Add(time.Hour)}
	ran := false
	err := a.withLock(ctx, e, "rotate", func(context.Context) error { ran = true; return nil })
	var locked *state.LockedError
	if !errors.As(err, &locked) || ran || !strings.Contains(err.Error(), "run-9") {
		t.Fatalf("held lock: ran=%v err=%v", ran, err)
	}
}

func TestWithLockLostCancelsRun(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a renew")
	}
	old := lockTTL
	lockTTL = 30 * time.Millisecond
	defer func() { lockTTL = old }()
	ls := &lockStore{steal: true}
	a := &app{}
	err := a.withLock(context.Background(), &engine.Engine{State: ls}, "apply", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return errors.New("not canceled")
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled run, got %v", err)
	}
}

type mapSecrets map[string]map[string]string

func (m mapSecrets) Put(context.Context, string, string, string, map[string]string) error { return nil }
func (m mapSecrets) DeleteField(context.Context, string, string) error                    { return nil }
func (m mapSecrets) Read(_ context.Context, p string) (map[string]string, error) {
	if d, ok := m[p]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}

func TestProviderFromSecrets(t *testing.T) {
	ctx := context.Background()
	sec := mapSecrets{"mgmt/or": {"key": "sk-mgmt", "self_id": "me", "workspace": "ws"}}
	p, err := providerFromSecrets(ctx, sec, "openrouter", manifest.ProviderConfig{ManagementKey: "mgmt/or"}, time.Second)
	if err != nil || p.SelfID() != "me" {
		t.Fatalf("from secret: %v", err)
	}
	if _, err := providerFromSecrets(ctx, sec, "openrouter", manifest.ProviderConfig{ManagementKey: "missing"}, time.Second); err == nil || !strings.Contains(err.Error(), "ORKEYS_OPENROUTER_KEY") {
		t.Fatalf("missing secret must name the env override: %v", err)
	}
	if _, err := providerFromSecrets(ctx, sec, "openrouter", manifest.ProviderConfig{}, time.Second); err == nil {
		t.Fatal("no key and no management_key must fail")
	}
	t.Setenv("ORKEYS_DEEPINFRA_KEY", "env-key")
	t.Setenv("ORKEYS_DEEPINFRA_SELF_ID", "tok-1")
	p, err = providerFromSecrets(ctx, nil, "deepinfra", manifest.ProviderConfig{}, time.Second)
	if err != nil || p.SelfID() != "tok-1" {
		t.Fatalf("env override: %v", err)
	}

	// A named provider of a kind: the env var uses the name, the client uses the kind.
	t.Setenv("ORKEYS_OPENROUTER_PRIVATE_KEY", "env-key")
	t.Setenv("ORKEYS_OPENROUTER_PRIVATE_SELF_ID", "priv")
	p, err = providerFromSecrets(ctx, nil, "openrouter-private", manifest.ProviderConfig{Kind: "openrouter"}, time.Second)
	if err != nil || p.SelfID() != "priv" || p.Name() != "openrouter" {
		t.Fatalf("named provider: %v", err)
	}
	if _, err := providerFromSecrets(ctx, sec, "openrouter-x", manifest.ProviderConfig{ManagementKey: "missing"}, time.Second); err == nil || !strings.Contains(err.Error(), "ORKEYS_OPENROUTER_X_KEY") {
		t.Fatalf("env var name with dash: %v", err)
	}
}

func TestResolveExporterConfig(t *testing.T) {
	dir := t.TempDir()
	pol := filepath.Join(dir, "policy.yaml")
	_ = os.WriteFile(pol, []byte("state: s3://b/st\nsecrets: aws\nproviders:\n  openrouter:\n    management_key: or/key\n"), 0o644)

	c, err := resolveExporterConfig(pol, "vault://secret/x", "vault", nil, false, false)
	if err != nil || c.State != "s3://b/st" || c.Secrets != "aws" || c.Providers["openrouter"].ManagementKey != "or/key" {
		t.Fatalf("from policy: %+v %v", c, err)
	}
	c, err = resolveExporterConfig(pol, "vault://secret/x", "vault", []string{"openrouter=other/key", "deepinfra=di/key"}, true, false)
	if err != nil || c.State != "vault://secret/x" || c.Secrets != "aws" || c.Providers["openrouter"].ManagementKey != "other/key" || len(c.Providers) != 2 {
		t.Fatalf("flags override: %+v %v", c, err)
	}
	_ = os.WriteFile(pol, []byte("providers:\n  - {name: or-a, kind: openrouter, management_key: a/key}\n"), 0o644)
	c, err = resolveExporterConfig(pol, "s", "vault", []string{"or-a=b/key"}, true, true)
	if err != nil || c.Providers["or-a"].Kind != "openrouter" || c.Providers["or-a"].ManagementKey != "b/key" {
		t.Fatalf("flag keeps the policy kind: %+v %v", c, err)
	}
	if _, err := resolveExporterConfig("", "s", "vault", nil, false, false); err == nil {
		t.Fatal("no providers must fail")
	}
	if _, err := resolveExporterConfig("", "s", "vault", []string{"broken"}, false, false); err == nil {
		t.Fatal("bad --provider must fail")
	}
}

func TestRenderPlanMarkdownAndImportStubs(t *testing.T) {
	var b bytes.Buffer
	writePlanMarkdown(&b, &engine.Plan{
		Actions:   []engine.Action{{Type: engine.ActCreate, Name: "prod-a-b", Team: "a", Provider: "openrouter", Detail: "x | y"}},
		Warnings:  []string{"w1"},
		Unmanaged: []engine.ForeignKey{{Provider: "openrouter", ID: "z"}},
	})
	out := b.String()
	for _, want := range []string{"| `create` | `prod-a-b` | a | openrouter | x \\| y |", "- w1", "1 key(s) at providers have no manifest"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	b.Reset()
	writePlanMarkdown(&b, &engine.Plan{})
	if !strings.Contains(b.String(), "No changes.") {
		t.Error("empty plan")
	}

	b.Reset()
	lim := 50.0
	writeImportStubs(&b, []engine.Candidate{
		{Provider: "openrouter", Key: &provider.Key{ID: "abc", Name: "old: key", LimitUSD: &lim, LimitReset: "weekly"}},
		{Provider: "deepinfra", Key: &provider.Key{ID: "t1", Name: "plain"}},
	})
	out = b.String()
	for _, want := range []string{"import:\n  id: abc\n  name: 'old: key'\n", "budget:\n  reset: weekly\n  limit: 50\n", "---\n", "provider: deepinfra", "created unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFinishJSON(t *testing.T) {
	var text bytes.Buffer
	a := &app{output: "json", events: &eventPrinter{out: &text, quiet: true}}
	a.events.Emit(engine.Event{Type: engine.EventRotationFailed, Level: engine.LevelAlert, Key: "prod-a-b", Channel: "#a-alerts", Message: "boom"})
	if text.Len() != 0 {
		t.Fatal("JSON mode must not print text events")
	}
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	err := a.finish(map[string]any{"findings": []string{}}, errors.New("1 key failed"))
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	var doc struct {
		Findings []string       `json:"findings"`
		Events   []engine.Event `json:"events"`
		Error    string         `json:"error"`
	}
	if err == nil || json.Unmarshal(out, &doc) != nil || len(doc.Events) != 1 || doc.Events[0].Channel != "#a-alerts" || doc.Error != "1 key failed" || doc.Findings == nil {
		t.Fatalf("err=%v out=%s", err, out)
	}

	text.Reset()
	a = &app{output: "table", events: &eventPrinter{out: &text}}
	a.events.Emit(engine.Event{Type: engine.EventKeyIssued, Level: engine.LevelInfo, Key: "prod-a-b", Message: "new key"})
	if strings.TrimSpace(text.String()) != "[info] key_issued prod-a-b: new key" {
		t.Fatalf("text event: %q", text.String())
	}
	if err := a.finish(nil, nil); err != nil {
		t.Fatal(err)
	}
}
