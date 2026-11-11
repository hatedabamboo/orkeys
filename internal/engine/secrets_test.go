package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/provider"
)

const (
	prodPath  = "kv/llm/prod/chat-core/dialog"
	stagePath = "kv/llm/staging/chat-core/dialog"
)

func (v *env) twoSecrets(t *testing.T) {
	t.Helper()
	v.edit(t, "teams/chat-core/"+prodKey+".yaml", "secret:\n  path: "+prodPath+"\n",
		"secret:\n  - path: "+prodPath+"\n  - path: "+stagePath+"\n    field: OR_KEY\n")
}

func TestKeyInManySecrets(t *testing.T) {
	v := setup(t)
	v.twoSecrets(t)
	v.apply(t)
	a, b := v.store.secrets[prodPath+"#api_key"], v.store.secrets[stagePath+"#OR_KEY"]
	if a == "" || a != b {
		t.Fatalf("both places must hold the same key: %q %q", a, b)
	}
	if p := v.plan(t); !p.Empty() {
		t.Fatalf("plan after apply: %s", types(p))
	}

	v.clk.add(days(84))
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	a2, b2 := v.store.secrets[prodPath+"#api_key"], v.store.secrets[stagePath+"#OR_KEY"]
	if a2 == a || a2 != b2 {
		t.Fatalf("rotation must write the new key to both places: %q %q (old %q)", a2, b2, a)
	}
}

func TestSecretAddedAndRemoved(t *testing.T) {
	v := setup(t)
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID

	v.twoSecrets(t)
	p := v.plan(t)
	if !strings.Contains(types(p), "reissue:"+prodKey) {
		t.Fatalf("plan: %s", types(p))
	}
	v.apply(t)
	if v.store.secrets[prodPath+"#api_key"] != v.store.secrets[stagePath+"#OR_KEY"] {
		t.Fatal("places differ after reissue")
	}
	if pr := v.store.states[prodKey].PendingRevokes; len(pr) != 1 || pr[0].KeyID != oldID || len(pr[0].Secrets) != 0 {
		t.Fatalf("pending revokes: %+v", pr)
	}

	// Remove the staging place: the old field goes away after the revoke.
	v.edit(t, "teams/chat-core/"+prodKey+".yaml", "  - path: "+stagePath+"\n    field: OR_KEY\n", "")
	v.apply(t)
	pr := v.store.states[prodKey].PendingRevokes
	if len(pr) != 2 || len(pr[1].Secrets) != 1 || pr[1].Secrets[0].Path != stagePath {
		t.Fatalf("pending revokes: %+v", pr)
	}
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(time.Hour)
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(days(2))
	_ = v.rotate(t, RotateOptions{})
	if _, ok := v.store.secrets[stagePath+"#OR_KEY"]; ok {
		t.Fatalf("removed place still holds a key after the revoke: %+v", v.store.states[prodKey].PendingRevokes)
	}
	if v.store.secrets[prodPath+"#api_key"] == "" {
		t.Fatal("kept place lost its key")
	}
}

func TestSecretWriteFailureRestoresAll(t *testing.T) {
	v := setup(t)
	v.twoSecrets(t)
	v.apply(t)
	old := v.store.secrets[prodPath+"#api_key"]
	oldID := v.store.states[prodKey].KeyID

	v.store.failPath = stagePath
	v.clk.add(days(84))
	_ = v.rotate(t, RotateOptions{})
	if v.store.secrets[prodPath+"#api_key"] != old || v.store.secrets[stagePath+"#OR_KEY"] != old {
		t.Fatal("a failed write must put the old key back in all places")
	}
	if st := v.store.states[prodKey]; st.KeyID != oldID {
		t.Fatalf("state not restored: %+v", st)
	}

	v.store.failPath = ""
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := v.store.secrets[prodPath+"#api_key"]; n == old || n != v.store.secrets[stagePath+"#OR_KEY"] {
		t.Fatal("retry did not write the new key to both places")
	}
}

func TestImportWritesAllSecrets(t *testing.T) {
	v := setup(t)
	ctx := context.Background()
	v.or.keys = append(v.or.keys, provider.Key{ID: "legacy", Name: "old"})
	v.twoSecrets(t)
	v.edit(t, "teams/chat-core/"+prodKey+".yaml", "ttl: 90d", "ttl: 90d\nimport: {id: legacy, name: old}")
	_ = v.store.Put(ctx, prodPath, "api_key", "sk-old", nil)
	v.apply(t)
	if v.store.secrets[stagePath+"#OR_KEY"] != "sk-old" || v.store.states[prodKey].KeyID != "legacy" {
		t.Fatalf("import must copy the key to all places: %v", v.store.secrets)
	}
}
