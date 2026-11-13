package state

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLegacySecretFields(t *testing.T) {
	in := `{"name":"k","secret_path":"kv/a","secret_field":"api_key",
	"pending_revokes":[{"key_id":"old","secret_path":"kv/b","secret_field":"x"}]}`
	var st State
	if err := json.Unmarshal([]byte(in), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Secrets) != 1 || st.Secrets[0] != (SecretRef{"kv/a", "api_key"}) {
		t.Fatalf("secrets: %+v", st.Secrets)
	}
	if pr := st.PendingRevokes[0]; len(pr.Secrets) != 1 || pr.Secrets[0] != (SecretRef{"kv/b", "x"}) {
		t.Fatalf("pending revoke secrets: %+v", pr.Secrets)
	}
	b, _ := json.Marshal(&st)
	var back State
	if err := json.Unmarshal(b, &back); err != nil || !SameSecrets(back.Secrets, st.Secrets) {
		t.Fatalf("round trip: %s %v", b, err)
	}
}

func TestLegacyGitHubSecrets(t *testing.T) {
	in := `{"name":"k","key_id":"k2","github_secrets":{"repo:acme/x:KEY":"k1"}}`
	var st State
	if err := json.Unmarshal([]byte(in), &st); err != nil {
		t.Fatal(err)
	}
	if st.Pushed["github/repo:acme/x:KEY"] != "k1" || len(st.Pushed) != 1 {
		t.Fatalf("pushed: %v", st.Pushed)
	}
	b, _ := json.Marshal(&st)
	if strings.Contains(string(b), "github_secrets") {
		t.Fatalf("legacy field written back: %s", b)
	}
}

func TestCheckSchema(t *testing.T) {
	if err := CheckSchema(&State{Name: "a"}, &State{Name: "b", Schema: SchemaVersion}); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(&State{Name: "c", Schema: SchemaVersion + 1}); err == nil {
		t.Fatal("want an error for a newer schema")
	}
}

type memPolicies struct {
	p      *StoredPolicy
	writes int
}

func (m *memPolicies) PutPolicy(_ context.Context, p *StoredPolicy) error {
	m.p = p
	m.writes++
	return nil
}

func (m *memPolicies) GetPolicy(context.Context) (*StoredPolicy, error) { return m.p, nil }

func TestSavePolicy(t *testing.T) {
	m := &memPolicies{}
	ctx := context.Background()
	t1 := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	if wrote, err := SavePolicy(ctx, m, []byte("a: 1\n"), t1); !wrote || err != nil {
		t.Fatalf("first save: %v %v", wrote, err)
	}
	if wrote, _ := SavePolicy(ctx, m, []byte("a: 1\n"), t1.Add(time.Hour)); wrote || m.writes != 1 || !m.p.UpdatedAt.Equal(t1) {
		t.Fatalf("an equal policy must not be written again: writes %d, updated %v", m.writes, m.p.UpdatedAt)
	}
	if wrote, _ := SavePolicy(ctx, m, []byte("a: 2\n"), t1.Add(time.Hour)); !wrote || m.p.YAML != "a: 2\n" || !m.p.UpdatedAt.Equal(t1.Add(time.Hour)) {
		t.Fatalf("changed policy: %+v", m.p)
	}
}
