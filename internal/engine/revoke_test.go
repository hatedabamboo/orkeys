package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hatedabamboo/orkeys/internal/state"
)

func TestRevoke(t *testing.T) {
	v := setup(t)
	ctx := context.Background()
	v.apply(t)
	is, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "openrouter", Purpose: "evals"})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, want string }{
		{prodKey, "has a manifest"},
		{"personal-nobody", "not in state"},
	} {
		if err := v.e.Revoke(ctx, []string{is.Name, tc.name}, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want %q, got %v", tc.name, tc.want, err)
		}
	}
	if _, ok := v.store.states[is.Name]; !ok {
		t.Fatal("a failed check must revoke nothing")
	}

	orphan := v.store.states[stagingKey]
	orphan.Name = "staging-chat-core-gone"
	v.store.states[orphan.Name] = orphan
	if err := v.e.Revoke(ctx, []string{orphan.Name}, ""); err == nil || !strings.Contains(err.Error(), "not a request kind") {
		t.Fatalf("want request-kind error, got %v", err)
	}

	keyID := v.store.states[is.Name].KeyID
	if err := v.e.Revoke(ctx, []string{is.Name}, "laptop lost"); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.store.states[is.Name]; ok || v.or.find(keyID) != nil {
		t.Fatal("key or state left after revoke")
	}
	if !v.events.Has(EventKeyRevoked, "revoked by hand: laptop lost") {
		t.Fatalf("no key_revoked event: %+v", v.events.Events)
	}
}

func TestRevokeKeyGoneAtProvider(t *testing.T) {
	v := setup(t)
	is, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "openrouter", Purpose: "evals"})
	if err != nil {
		t.Fatal(err)
	}
	v.or.keys = nil
	if err := v.e.Revoke(context.Background(), []string{is.Name}, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.store.states[is.Name]; ok {
		t.Fatal("state left after revoke of a deleted key")
	}
}

func TestTracked(t *testing.T) {
	v := setup(t)
	v.apply(t)
	is, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "openrouter", Purpose: "evals"})
	if err != nil {
		t.Fatal(err)
	}
	orphan := v.store.states[stagingKey]
	orphan.Name = "staging-chat-core-gone"
	v.store.states[orphan.Name] = orphan

	states, err := v.store.ListStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	src := func(keys []TrackedKey) string {
		var s []string
		for _, k := range keys {
			s = append(s, k.Source+":"+k.Name)
		}
		return strings.Join(s, " ")
	}
	want := "request:" + is.Name + " manifest:" + prodKey + " manifest:" + stagingKey + " orphan:staging-chat-core-gone"
	if got := src(Tracked(v.e.Inv, states, TrackedFilter{})); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	if got := src(Tracked(v.e.Inv, states, TrackedFilter{Owner: "JDOE"})); got != "request:"+is.Name {
		t.Fatalf("owner filter: %s", got)
	}
	if got := src(Tracked(v.e.Inv, states, TrackedFilter{Source: SourceOrphan})); got != "orphan:staging-chat-core-gone" {
		t.Fatalf("source filter: %s", got)
	}
	b, err := json.Marshal(Tracked(v.e.Inv, states, TrackedFilter{Source: SourceRequest})[0])
	if err != nil || !strings.Contains(string(b), `"source":"request"`) || !strings.Contains(string(b), `"owner":"jdoe"`) {
		t.Fatalf("json: %s %v", b, err)
	}
	if got := Tracked(v.e.Inv, []*state.State{}, TrackedFilter{}); got == nil {
		t.Fatal("want an empty list, not nil")
	}
}
