package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

func TestHygieneBadOrgRevokesNothing(t *testing.T) {
	for name, org := range map[string]*fakeOrg{
		"no member found": {members: map[string]bool{}},
		"org check fails": {members: map[string]bool{"asmith": true}, checkErr: errors.New("HTTP 404")},
	} {
		t.Run(name, func(t *testing.T) {
			v := setup(t)
			pk, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "openrouter", Purpose: "x"})
			if err != nil {
				t.Fatal(err)
			}
			v.e.Org = org
			fs, err := v.e.Hygiene(context.Background(), false)
			if err == nil || !strings.Contains(err.Error(), "no key revoked") {
				t.Fatalf("want error, got %v", err)
			}
			for _, f := range fs {
				if f.Kind == "owner-left" {
					t.Fatalf("owner check ran: %+v", f)
				}
			}
			if _, ok := v.store.states[pk.Name]; !ok || v.or.find(v.store.states[pk.Name].KeyID) == nil {
				t.Fatal("key was revoked")
			}
		})
	}
}

func TestIssueSecretWriteFails(t *testing.T) {
	v := setup(t)
	v.store.failSecret = errors.New("vault sealed")
	if err := v.e.Apply(context.Background(), v.plan(t), ApplyOptions{}); err == nil {
		t.Fatal("want error")
	}
	if len(v.or.keys) != 0 || len(v.di.keys) != 0 || len(v.store.states) != 0 {
		t.Fatalf("not rolled back: or=%v di=%v states=%v", v.or.keys, v.di.keys, v.store.states)
	}
}

func TestIssueStateWriteFails(t *testing.T) {
	v := setup(t)
	v.store.failPutState = 100
	if err := v.e.Apply(context.Background(), v.plan(t), ApplyOptions{}); err == nil {
		t.Fatal("want error")
	}
	if len(v.or.keys) != 0 || len(v.store.secrets) != 0 {
		t.Fatalf("key or secret left behind: %v %v", v.or.keys, v.store.secrets)
	}
}

func TestRotationSecretWriteFailsKeepsOldKey(t *testing.T) {
	v := setup(t)
	v.apply(t)
	before := v.store.states[prodKey]
	v.clk.add(days(85))
	v.store.failSecret = errors.New("vault sealed")
	if err := v.rotate(t, RotateOptions{}); err == nil {
		t.Fatal("want error")
	}
	after := v.store.states[prodKey]
	if after.KeyID != before.KeyID || after.Rotation != state.RotationIdle || len(after.PendingRevokes) != 0 {
		t.Fatalf("state not restored: %+v", after)
	}
	if len(v.or.keys) != 1 || v.store.secrets["kv/llm/prod/chat-core/dialog#api_key"] != "secret-1" {
		t.Fatalf("old key or secret changed: %v %v", v.or.keys, v.store.secrets)
	}
	v.store.failSecret = nil
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if v.store.states[prodKey].KeyID == before.KeyID {
		t.Fatal("rotation did not run after the store recovered")
	}
}

func TestRotationStartIsPendingInFirstSave(t *testing.T) {
	v := setup(t)
	v.apply(t)
	v.clk.add(days(85))
	v.kube.fail = errors.New("cluster down")
	_ = v.rotate(t, RotateOptions{})
	st := v.store.states[prodKey]
	if st.Rotation != state.RotationFailed || len(st.PendingRevokes) != 1 || st.PendingRevokes[0].Source != state.RevokeRotation {
		t.Fatalf("state: %+v", st)
	}
}

func TestRemoveKeepsOtherSecretFields(t *testing.T) {
	v := setup(t)
	v.apply(t)
	v.store.secrets["kv/llm/prod/chat-core/dialog#DB_PASSWORD"] = "keep-me"
	_ = os.Remove(filepath.Join(v.root, "teams/chat-core/"+prodKey+".yaml"))
	v.reload(t)
	v.apply(t)
	if v.store.secrets["kv/llm/prod/chat-core/dialog#DB_PASSWORD"] != "keep-me" {
		t.Fatal("other field deleted")
	}
	if _, ok := v.store.secrets["kv/llm/prod/chat-core/dialog#api_key"]; ok {
		t.Fatal("orkeys field not deleted")
	}
}

func TestReissueOnSecretMove(t *testing.T) {
	v := setup(t)
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID
	v.edit(t, "teams/chat-core/"+prodKey+".yaml", "path: kv/llm/prod/chat-core/dialog", "path: kv/llm/prod/chat-core/dialog-v2")
	if got := types(v.plan(t)); got != "reissue:"+prodKey {
		t.Fatalf("plan: %s", got)
	}
	v.apply(t)
	if v.store.secrets["kv/llm/prod/chat-core/dialog-v2#api_key"] == "" {
		t.Fatal("new path not written")
	}
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
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
	if _, ok := v.store.secrets["kv/llm/prod/chat-core/dialog#api_key"]; ok {
		t.Fatal("old secret field left at the old path")
	}
}

func TestImportKey(t *testing.T) {
	v := setup(t)
	ctx := context.Background()
	secretPath := "kv/llm/prod/chat-core/dialog"
	v.or.keys = append(v.or.keys, provider.Key{ID: "legacy", Name: "old"})

	v.edit(t, "teams/chat-core/"+prodKey+".yaml", "ttl: 90d", "ttl: 90d\nimport: {id: legacy, name: wrong}")
	if p := v.plan(t); strings.Contains(types(p), "import:") || !strings.Contains(fmt.Sprint(p.Warnings), `named "old" at the provider, not "wrong"`) {
		t.Fatalf("name mismatch not caught: %s %v", types(p), p.Warnings)
	}
	v.edit(t, "teams/chat-core/"+prodKey+".yaml", "name: wrong}", "name: old}")
	p := v.plan(t)
	if types(p) != "import:"+prodKey+" create:"+stagingKey {
		t.Fatalf("plan: %s", types(p))
	}
	for _, u := range p.Unmanaged {
		if u.ID == "legacy" {
			t.Fatal("key waiting for import listed as unmanaged")
		}
	}

	if err := v.e.Apply(ctx, p, ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "write the current key to "+secretPath) {
		t.Fatalf("import without the secret must fail: %v", err)
	}
	if _, ok := v.store.states[prodKey]; ok {
		t.Fatal("state saved without the secret")
	}

	_ = v.store.Put(ctx, secretPath, "api_key", "sk-old", nil)
	created := len(v.or.keys)
	v.apply(t)
	st := v.store.states[prodKey]
	if st.KeyID != "legacy" || !st.ExpiresAt.Equal(v.clk.t.Add(days(90))) || len(v.or.keys) != created {
		t.Fatalf("state %+v, %d keys", st, len(v.or.keys))
	}
	if lim := v.or.find("legacy").LimitUSD; lim == nil || *lim != 200 {
		t.Fatalf("limit not set on the imported key: %v", lim)
	}
	if v.store.secrets[secretPath+"#api_key"] != "sk-old" || !hasEvent(v.events, EventKeyImported, prodKey) {
		t.Fatal("secret changed or no key_imported event")
	}
	if p := v.plan(t); !p.Empty() {
		t.Fatalf("plan after import: %s", types(p))
	}

	v.clk.add(days(88))
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := v.store.states[prodKey]; st.KeyID == "legacy" || len(st.PendingRevokes) != 1 || st.PendingRevokes[0].KeyID != "legacy" {
		t.Fatalf("imported key not rotated like the others: %+v", st)
	}
}

func TestImportKeepsEarlierProviderExpiry(t *testing.T) {
	v := setup(t)
	early := v.clk.t.Add(days(30))
	v.or.keys = append(v.or.keys, provider.Key{ID: "legacy", Name: "old", ExpiresAt: &early})
	v.edit(t, "teams/chat-core/"+prodKey+".yaml", "ttl: 90d", "ttl: 90d\nimport: {id: legacy, name: old}")
	_ = v.store.Put(context.Background(), "kv/llm/prod/chat-core/dialog", "api_key", "sk-old", nil)
	v.apply(t)
	if got := v.store.states[prodKey].ExpiresAt; !got.Equal(early) {
		t.Fatalf("expiry %v, want the provider expiry %v", got, early)
	}
}

func hasEvent(l *EventLog, typ, key string) bool {
	for _, ev := range l.Events {
		if ev.Type == typ && ev.Key == key {
			return true
		}
	}
	return false
}

func TestRequestUndoOnDeliverFailure(t *testing.T) {
	v := setup(t)
	v.dlv.fail = errors.New("no ssh keys")
	if _, err := v.request(t, Request{User: "jdoe", Kind: "local", Provider: "openrouter", Purpose: "x"}); err == nil {
		t.Fatal("want error")
	}
	if len(v.or.keys) != 0 || len(v.store.states) != 0 {
		t.Fatal("not rolled back")
	}
}

func TestUnusedSkipsProvidersWithoutLastUse(t *testing.T) {
	v := setup(t)
	v.apply(t)
	v.clk.add(days(40))
	fs, err := v.e.Hygiene(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Kind == "unused" && f.Name == stagingKey {
			t.Fatal("deepinfra key reported as unused without last-use data")
		}
	}
}

func TestLocalKeyExpiryNotice(t *testing.T) {
	v := setup(t)
	v.e.Inv.Policy.Kind("local").NotifyBefore.Duration = 24 * time.Hour
	is, err := v.request(t, Request{User: "jdoe", Kind: "local", Provider: "openrouter", Purpose: "x"})
	if err != nil {
		t.Fatal(err)
	}
	v.clk.add(days(6) + time.Hour)
	_ = v.rotate(t, RotateOptions{})
	if !v.store.states[is.Name].ExpiryNotified {
		t.Fatal("no expiry notice for a local key")
	}
}
