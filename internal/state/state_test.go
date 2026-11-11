package state

import (
	"encoding/json"
	"testing"
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
