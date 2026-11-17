package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/state"
)

const voidKey = "prod-chat-core-tts"

const voidManifest = `name: prod-chat-core-tts
kind: production
provider: tts
env: prod
service: tts
description: TTS for the voice gateway.
secret:
  path: kv/llm/prod/chat-core/tts
consumers:
  - type: eso
    cluster: prod-eu
    namespace: chat
    externalsecret: tts-llm
    restart: [deployment/voice]
  - type: github
    repo: acme/voice
    name: TTS_API_KEY
`

const voidSecret = "kv/llm/prod/chat-core/tts#api_key"

func voidFile() string { return "teams/chat-core/" + voidKey + ".yaml" }

// setupVoid adds the void provider tts and writes the manifest. The other keys of the test repo exist already.
func setupVoid(t *testing.T, keyFile string) (*env, *fakeGitHub) {
	t.Helper()
	v := setup(t)
	v.apply(t)
	p := filepath.Join(v.root, "policy.yaml")
	b, _ := os.ReadFile(p)
	b = []byte(strings.Replace(string(b), "providers:\n", "providers:\n  tts:\n    kind: void\n    options: {console: https://vendor.example/keys}\n", 1))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.root, voidFile()), []byte(keyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	tts, err := provider.New("void", provider.Config{})
	if err != nil {
		t.Fatal(err)
	}
	v.e.Providers["tts"] = tts
	gh := &fakeGitHub{}
	v.e.Pushers = map[string]Pusher{manifest.ConsumerGitHub: GitHubPusher{Secrets: gh}}
	v.reload(t)
	v.kube.calls = nil
	return v, gh
}

func (v *env) put(t *testing.T, opt PutOptions) {
	t.Helper()
	if err := v.e.Put(context.Background(), voidKey, opt); err != nil {
		t.Fatal(err)
	}
}

func TestVoidKeyLifecycle(t *testing.T) {
	v, gh := setupVoid(t, voidManifest)
	ctx := context.Background()

	if got := types(v.plan(t)); got != "register:"+voidKey {
		t.Fatalf("plan: %s", got)
	}
	v.apply(t)
	st := v.store.states[voidKey]
	if st.KeyID != "" || st.Provider != "tts" || !st.ExpiresAt.IsZero() || st.LimitReset != "" {
		t.Fatalf("state after register: %+v", st)
	}
	if p := v.plan(t); !p.Empty() || !strings.Contains(strings.Join(p.Warnings, "\n"), "no value yet") {
		t.Fatalf("plan while waiting: %s %v", types(p), p.Warnings)
	}

	// The alert about the missing value repeats once a day.
	for range 2 {
		if err := v.rotate(t, RotateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := countEvents(v.events, EventValueMissing, voidKey); n != 1 {
		t.Fatalf("value_missing events: %d", n)
	}
	v.clk.add(21 * time.Hour)
	_ = v.rotate(t, RotateOptions{})
	if n := countEvents(v.events, EventValueMissing, voidKey); n != 2 {
		t.Fatalf("value_missing events after a day: %d", n)
	}

	v.put(t, PutOptions{Value: "tts-v1"})
	st = v.store.states[voidKey]
	id1 := valueID(voidKey, "tts-v1")
	if st.KeyID != id1 || st.Rotation != state.RotationPending || len(st.PendingRevokes) != 0 ||
		!st.ExpiresAt.Equal(v.clk.now().Add(days(90))) {
		t.Fatalf("state after the first put: %+v", st)
	}
	if v.store.secrets[voidSecret] != "tts-v1" || v.store.meta[voidSecret][secrets.MetaKeyID] != id1 {
		t.Fatalf("secret after put: %q %v", v.store.secrets[voidSecret], v.store.meta[voidSecret])
	}
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if want := "sync prod-eu/chat/tts-llm restart prod-eu/chat/deployment/voice"; strings.Join(v.kube.calls, " ") != want {
		t.Fatalf("kube calls: %v", v.kube.calls)
	}
	if gh.secrets["repo:acme/voice:TTS_API_KEY"] != "tts-v1" || !v.events.Has(EventRotationSynced, short(id1)) {
		t.Fatalf("github %v, events %v", gh.secrets, v.events.Events)
	}
	if p := v.plan(t); !p.Empty() || len(p.Warnings) != 0 {
		t.Fatalf("plan after sync: %s %v", types(p), p.Warnings)
	}

	if err := v.rotate(t, RotateOptions{Now: []string{voidKey}}); err == nil || !strings.Contains(err.Error(), "orkeys put "+voidKey) {
		t.Fatalf("rotate --now: %v", err)
	}
	if err := v.e.Put(ctx, voidKey, PutOptions{Value: "tts-v1"}); err == nil || !strings.Contains(err.Error(), "hold this value already") {
		t.Fatalf("put of the same value: %v", err)
	}

	// The notice goes once at rotate_before (7d). After the expiry, an alert goes once a day.
	v.clk.add(days(84))
	_ = v.rotate(t, RotateOptions{})
	_ = v.rotate(t, RotateOptions{})
	if n := countEvents(v.events, EventExpiryNotice, voidKey); n != 1 || !v.events.Has(EventExpiryNotice, "https://vendor.example/keys") {
		t.Fatalf("expiry notices: %d", n)
	}
	v.clk.add(days(7))
	_ = v.rotate(t, RotateOptions{})
	_ = v.rotate(t, RotateOptions{})
	if n := countEvents(v.events, EventKeyExpired, voidKey); n != 1 {
		t.Fatalf("key_expired events: %d", n)
	}

	v.put(t, PutOptions{Value: "tts-v2"})
	id2 := valueID(voidKey, "tts-v2")
	if st := v.store.states[voidKey]; st.KeyID != id2 || len(st.PendingRevokes) != 1 || st.PendingRevokes[0].KeyID != id1 {
		t.Fatalf("state after the second put: %+v", st)
	}
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if gh.secrets["repo:acme/voice:TTS_API_KEY"] != "tts-v2" || !v.events.Has(EventRevokeByHand, short(id1)) {
		t.Fatalf("github %v, events %v", gh.secrets, v.events.Events)
	}
	v.clk.add(25 * time.Hour)
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := v.store.states[voidKey]; len(st.PendingRevokes) != 0 {
		t.Fatalf("old value not forgotten after grace: %+v", st.PendingRevokes)
	}
	if n := countEvents(v.events, EventKeyExpired, voidKey); n != 1 {
		t.Fatalf("key_expired after the new value: %d", n)
	}

	if err := os.Remove(filepath.Join(v.root, voidFile())); err != nil {
		t.Fatal(err)
	}
	v.reload(t)
	p := v.plan(t)
	if types(p) != "delete:"+voidKey || !strings.Contains(p.Actions[0].Detail, "revoke "+short(id2)+" at the vendor by hand") {
		t.Fatalf("plan after manifest delete: %s %v", types(p), p.Actions)
	}
	v.apply(t)
	if _, ok := v.store.states[voidKey]; ok || v.store.secrets[voidSecret] != "" || len(gh.secrets) != 0 {
		t.Fatalf("after delete: state %v, secret %q, github %v", ok, v.store.secrets[voidSecret], gh.secrets)
	}
	if !v.events.Has(EventRevokeByHand, short(id2)) {
		t.Fatalf("events: %v", v.events.Events)
	}
}

func TestVoidRegisterAdoptsValue(t *testing.T) {
	v, gh := setupVoid(t, voidManifest)
	v.store.secrets[voidSecret] = "existing"
	v.apply(t)
	id := valueID(voidKey, "existing")
	st := v.store.states[voidKey]
	if st.KeyID != id || v.store.meta[voidSecret][secrets.MetaKeyID] != id || gh.secrets["repo:acme/voice:TTS_API_KEY"] != "existing" {
		t.Fatalf("state %+v, meta %v, github %v", st, v.store.meta[voidSecret], gh.secrets)
	}
	if len(v.kube.calls) != 0 {
		t.Fatalf("register restarted workloads: %v", v.kube.calls)
	}
	issued := v.clk.now().Add(-days(60))
	v.put(t, PutOptions{Value: "existing", Issued: issued})
	if st := v.store.states[voidKey]; st.KeyID != id || st.Rotation != state.RotationIdle || !st.ExpiresAt.Equal(issued.Add(days(90))) {
		t.Fatalf("dates only: %+v", st)
	}
}

func TestVoidKeyWithoutSecret(t *testing.T) {
	m := "name: prod-chat-core-tts\nkind: production\nprovider: tts\nenv: prod\ndescription: Calendar entry.\nttl: 30d\n"
	v, _ := setupVoid(t, m)
	v.apply(t)
	st := v.store.states[voidKey]
	if st.KeyID != "" || !st.ExpiresAt.Equal(v.clk.now().Add(days(30))) {
		t.Fatalf("state: %+v", st)
	}
	ctx := context.Background()
	if err := v.e.Put(ctx, voidKey, PutOptions{Value: "x"}); err == nil || !strings.Contains(err.Error(), "no secret in the manifest") {
		t.Fatalf("put with a value: %v", err)
	}
	if err := v.e.Put(ctx, voidKey, PutOptions{Issued: v.clk.now().Add(-days(31))}); err == nil || !strings.Contains(err.Error(), "in the past") {
		t.Fatalf("put of an expired key: %v", err)
	}
	issued := v.clk.now().Add(-days(20))
	v.put(t, PutOptions{Issued: issued})
	if st := v.store.states[voidKey]; !st.ExpiresAt.Equal(issued.Add(days(30))) || st.KeyID != "" {
		t.Fatalf("state after put: %+v", st)
	}
	if p := v.plan(t); !p.Empty() {
		t.Fatalf("plan after put: %s", types(p))
	}

	// With issued in the manifest, the manifest sets the dates.
	v.edit(t, voidFile(), "ttl: 30d", "ttl: 30d\nissued: 2026-10-20")
	if p := v.plan(t); types(p) != "set-dates:"+voidKey || !strings.Contains(p.Actions[0].Detail, "issued none -> 2026-10-20") {
		t.Fatalf("plan: %s %v", types(p), p.Actions)
	}
	v.apply(t)
	if st := v.store.states[voidKey]; st.Issued != "2026-10-20" || st.ExpiresAt.Format(time.DateOnly) != "2026-11-19" {
		t.Fatalf("state after set-dates: %+v", st)
	}
	if err := v.e.Put(ctx, prodKey, PutOptions{Value: "x"}); err == nil || !strings.Contains(err.Error(), "has an API") {
		t.Fatalf("put of an openrouter key: %v", err)
	}
}

func TestVoidMoveSecret(t *testing.T) {
	v, _ := setupVoid(t, voidManifest)
	v.store.secrets[voidSecret] = "tts-v1"
	v.apply(t)
	id := valueID(voidKey, "tts-v1")
	v.kube.calls = nil

	v.edit(t, voidFile(), "path: kv/llm/prod/chat-core/tts", "path: kv/llm/prod/voice/tts")
	p := v.plan(t)
	if types(p) != "move-secret:"+voidKey || !strings.Contains(p.Actions[0].Detail, "copy key "+short(id)) {
		t.Fatalf("plan: %s %v", types(p), p.Actions)
	}
	v.apply(t)
	if v.store.secrets["kv/llm/prod/voice/tts#api_key"] != "tts-v1" || v.store.secrets[voidSecret] != "tts-v1" {
		t.Fatalf("secrets after move: %v", v.store.secrets)
	}
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(v.kube.calls) == 0 || v.events.Has(EventRevokeByHand, short(id)) {
		t.Fatalf("kube %v, events %v", v.kube.calls, v.events.Events)
	}
	v.clk.add(25 * time.Hour)
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := v.store.states[voidKey]; st.KeyID != id || len(st.PendingRevokes) != 0 || v.store.secrets[voidSecret] != "" {
		t.Fatalf("after grace: %+v, secrets %v", st, v.store.secrets)
	}
}

func TestVoidIssuedInManifest(t *testing.T) {
	m := strings.Replace(voidManifest, "secret:\n", "issued: 2026-10-01\nsecret:\n", 1)
	v, gh := setupVoid(t, m)
	v.store.secrets[voidSecret] = "tts-v1"
	id1, id2 := valueID(voidKey, "tts-v1"), valueID(voidKey, "tts-v2")

	p := v.plan(t)
	if types(p) != "register:"+voidKey || !strings.Contains(p.Actions[0].Detail, "adopt value "+short(id1)) ||
		!strings.Contains(p.Actions[0].Detail, "issued 2026-10-01, expires 2026-12-30") {
		t.Fatalf("plan: %s %v", types(p), p.Actions)
	}
	v.apply(t)
	st := v.store.states[voidKey]
	if st.KeyID != id1 || st.Issued != "2026-10-01" || st.ExpiresAt.Format(time.DateOnly) != "2026-12-30" {
		t.Fatalf("state after register: %+v", st)
	}

	// A new value in the secret without a manifest change is not seen.
	v.store.secrets[voidSecret] = "tts-v2"
	if p := v.plan(t); !p.Empty() {
		t.Fatalf("plan without issued change: %s", types(p))
	}

	v.edit(t, voidFile(), "issued: 2026-10-01", "issued: 2026-11-01")
	p = v.plan(t)
	if types(p) != "adopt-value:"+voidKey || !strings.Contains(p.Actions[0].Detail, "new value "+short(id2)) ||
		!strings.Contains(p.Actions[0].Detail, "old value "+short(id1)+" waits for revoke by hand") {
		t.Fatalf("plan after issued change: %s %v", types(p), p.Actions)
	}
	v.apply(t)
	st = v.store.states[voidKey]
	if st.KeyID != id2 || st.Rotation != state.RotationPending || st.Issued != "2026-11-01" ||
		st.ExpiresAt.Format(time.DateOnly) != "2027-01-30" || v.store.meta[voidSecret][secrets.MetaKeyID] != id2 {
		t.Fatalf("state after adopt-value: %+v", st)
	}
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if gh.secrets["repo:acme/voice:TTS_API_KEY"] != "tts-v2" || len(v.kube.calls) == 0 || !v.events.Has(EventRevokeByHand, short(id1)) {
		t.Fatalf("github %v, kube %v, events %v", gh.secrets, v.kube.calls, v.events.Events)
	}

	// The same value with another date changes only the dates.
	v.edit(t, voidFile(), "issued: 2026-11-01", "issued: 2026-10-25")
	if p := v.plan(t); types(p) != "set-dates:"+voidKey || !strings.Contains(p.Actions[0].Detail, "same value "+short(id2)) {
		t.Fatalf("plan for a date fix: %s %v", types(p), p.Actions)
	}
	v.apply(t)
	if st := v.store.states[voidKey]; st.KeyID != id2 || st.ExpiresAt.Format(time.DateOnly) != "2027-01-23" {
		t.Fatalf("state after the date fix: %+v", st)
	}

	// A ttl change moves the expiry.
	v.edit(t, voidFile(), "issued: 2026-10-25", "issued: 2026-10-25\nttl: 30d")
	if p := v.plan(t); types(p) != "set-dates:"+voidKey || !strings.Contains(p.Actions[0].Detail, "expires 2027-01-23 -> 2026-11-24 (ttl 30d)") {
		t.Fatalf("plan for a ttl change: %s %v", types(p), p.Actions)
	}
	v.apply(t)

	// put works, and tells the person to update issued.
	v.put(t, PutOptions{Value: "tts-v3"})
	if !v.events.Has(EventKeyIssued, "set issued: 2026-11-01 in "+voidFile()) {
		t.Fatalf("events: %v", v.events.Events)
	}
	if p := v.plan(t); !p.Empty() {
		t.Fatalf("plan after put: %s %v", types(p), p.Actions)
	}
}

func TestVoidDifferentValues(t *testing.T) {
	m := strings.Replace(voidManifest, "secret:\n  path: kv/llm/prod/chat-core/tts\n",
		"secret:\n  - path: kv/llm/prod/chat-core/tts\n  - path: kv/llm/prod/voice/tts\n", 1)
	v, _ := setupVoid(t, m)
	v.store.secrets[voidSecret] = "a"
	v.store.secrets["kv/llm/prod/voice/tts#api_key"] = "b"
	p := v.plan(t)
	if !p.Empty() || !strings.Contains(strings.Join(p.Warnings, "\n"), "hold different values") {
		t.Fatalf("plan: %s %v", types(p), p.Warnings)
	}
	v.store.secrets["kv/llm/prod/voice/tts#api_key"] = ""
	v.apply(t)
	if v.store.secrets["kv/llm/prod/voice/tts#api_key"] != "a" || v.store.states[voidKey].KeyID != valueID(voidKey, "a") {
		t.Fatalf("an empty place is filled: %v", v.store.secrets)
	}
}
