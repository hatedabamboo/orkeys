package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

// handProvider is a provider whose API lists and revokes keys but cannot create them, as fal.
// A value is id:secret.
type handProvider struct{ *fakeProvider }

func (h handProvider) Create(context.Context, provider.Spec) (provider.Key, string, error) {
	return provider.Key{}, "", errors.New("create must not be called")
}

func (h handProvider) KeyIDOf(value string) string {
	id, secret, ok := strings.Cut(value, ":")
	if !ok || secret == "" {
		return ""
	}
	return id
}

func (h handProvider) add(id string) {
	zero := 0.0
	h.keys = append(h.keys, provider.Key{ID: id, Name: id, UsageUSD: &zero})
}

const (
	handKey    = "prod-chat-core-img"
	handSecret = "kv/llm/prod/chat-core/img#api_key"
)

const handManifest = `name: prod-chat-core-img
kind: production
provider: img
env: prod
service: img
description: Image generation.
budget: {limit: 50}
secret:
  path: kv/llm/prod/chat-core/img
consumers:
  - type: eso
    cluster: prod-eu
    namespace: chat
    externalsecret: img-llm
`

func setupHand(t *testing.T, keyFile string) (*env, handProvider) {
	t.Helper()
	v := setup(t)
	v.apply(t)
	p := filepath.Join(v.root, "policy.yaml")
	b, _ := os.ReadFile(p)
	b = []byte(strings.Replace(string(b), "providers:\n", "providers:\n  img:\n    kind: fal\n", 1))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.root, "teams/chat-core/"+handKey+".yaml"), []byte(keyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	h := handProvider{&fakeProvider{name: "img", caps: provider.Caps{ManualCreate: true, LimitReset: true}}}
	v.e.Providers["img"] = h
	v.reload(t)
	v.kube.calls = nil
	return v, h
}

func TestManualCreateLifecycle(t *testing.T) {
	v, h := setupHand(t, handManifest)
	ctx := context.Background()

	if got := types(v.plan(t)); got != "register:"+handKey {
		t.Fatalf("plan: %s", got)
	}
	v.apply(t)
	if st := v.store.states[handKey]; st.KeyID != "" || st.LimitUSD != 50 || st.LimitReset != "monthly" {
		t.Fatalf("state after register: %+v", st)
	}

	// A value that is not a provider key, or a key that the provider does not list, does not replace the key.
	if err := v.e.Put(ctx, handKey, PutOptions{Value: "garbage"}); !errors.Is(err, errNotProviderKey) {
		t.Fatalf("put of a non-key: %v", err)
	}
	if err := v.e.Put(ctx, handKey, PutOptions{Value: "k1:s1"}); err == nil || !strings.Contains(err.Error(), "does not list key k1") {
		t.Fatalf("put of an unlisted key: %v", err)
	}

	h.add("k1")
	v.put2(t, handKey, "k1:s1")
	if st := v.store.states[handKey]; st.KeyID != "k1" || st.Rotation != state.RotationPending || len(st.PendingRevokes) != 0 {
		t.Fatalf("state after the first put: %+v", st)
	}
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !v.events.Has(EventRotationSynced, "k1") || v.store.states[handKey].Rotation != state.RotationIdle {
		t.Fatalf("events %v", v.events.Events)
	}

	// The second key replaces k1. rotate revokes k1 at the provider after the grace time, with the usage check.
	h.add("k2")
	v.put2(t, handKey, "k2:s2")
	st := v.store.states[handKey]
	if st.KeyID != "k2" || len(st.PendingRevokes) != 1 || st.PendingRevokes[0].KeyID != "k1" || st.PendingRevokes[0].Source != state.RevokeRotation {
		t.Fatalf("state after the second put: %+v", st)
	}
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !v.events.Has(EventRotationSynced, "old key k1 will be revoked") || v.events.Has(EventRevokeByHand, "k1") {
		t.Fatalf("events after sync: %v", v.events.Events)
	}
	v.clk.add(25 * time.Hour)
	for range 2 {
		if err := v.rotate(t, RotateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if h.find("k1") != nil || h.find("k2") == nil || len(v.store.states[handKey].PendingRevokes) != 0 {
		t.Fatalf("after grace: keys %v, pending %v", h.keys, v.store.states[handKey].PendingRevokes)
	}
	if !v.events.Has(EventOldKeyRevoked, "k1") {
		t.Fatalf("events after revoke: %v", v.events.Events)
	}

	// A value in the secret with a new issued date: plan checks it as put does.
	if err := v.store.Put(ctx, "kv/llm/prod/chat-core/img", "api_key", "k3:s3", nil); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, v, strings.Replace(handManifest, "budget:", "issued: 2026-01-02\nbudget:", 1))
	if p := v.plan(t); !strings.Contains(strings.Join(p.Warnings, "\n"), "key k3 from the secret is not at img") {
		t.Fatalf("plan with an unlisted value: %s %v", types(p), p.Warnings)
	}
	h.add("k3")
	p := v.plan(t)
	if types(p) != "adopt-value:"+handKey || !strings.Contains(p.Actions[0].Detail, "rotate revokes old key k2") {
		t.Fatalf("plan with a listed value: %s %v", types(p), p.Actions)
	}

	// Delete of the manifest revokes all known keys at the provider.
	v.apply(t)
	if err := os.Remove(filepath.Join(v.root, "teams/chat-core/"+handKey+".yaml")); err != nil {
		t.Fatal(err)
	}
	v.reload(t)
	if p := v.plan(t); types(p) != "delete:"+handKey || !strings.Contains(p.Actions[0].Detail, "revoke k3, k2") {
		t.Fatalf("plan after manifest delete: %s %v", types(p), p.Actions)
	}
	v.apply(t)
	if h.find("k2") != nil || h.find("k3") != nil || v.store.secrets[handSecret] != "" {
		t.Fatalf("after delete: keys %v, secret %q", h.keys, v.store.secrets[handSecret])
	}
	if v.events.Has(EventRevokeByHand, "k3") {
		t.Fatalf("events after delete: %v", v.events.Events)
	}
}

func TestManualCreateOldKeyInUse(t *testing.T) {
	v, h := setupHand(t, handManifest)
	v.apply(t)
	h.add("k1")
	v.put2(t, handKey, "k1:s1")
	_ = v.rotate(t, RotateOptions{})
	h.add("k2")
	v.put2(t, handKey, "k2:s2")
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(25 * time.Hour)
	_ = v.rotate(t, RotateOptions{}) // records the baseline of k1
	*h.find("k1").UsageUSD = 3
	_ = v.rotate(t, RotateOptions{})
	if st := v.store.states[handKey]; h.find("k1") == nil || st.Rotation == state.RotationFailed || !v.events.Has(EventOldKeyRecheck, "k1") {
		t.Fatalf("first use must start a recheck: keys %v, state %+v", h.keys, st)
	}
	*h.find("k1").UsageUSD = 4
	v.clk.add(25 * time.Hour)
	_ = v.rotate(t, RotateOptions{})
	st := v.store.states[handKey]
	if h.find("k1") == nil || st.Rotation != state.RotationFailed || !v.events.Has(EventOldKeyInUse, "k1") {
		t.Fatalf("old key in use: keys %v, state %+v", h.keys, st)
	}
}

func (v *env) put2(t *testing.T, name, value string) {
	t.Helper()
	if err := v.e.Put(context.Background(), name, PutOptions{Value: value}); err != nil {
		t.Fatal(err)
	}
}

func writeManifest(t *testing.T, v *env, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(v.root, "teams/chat-core/"+handKey+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	v.reload(t)
}
