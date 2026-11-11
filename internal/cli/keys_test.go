package cli

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hatedabamboo/orkeys/internal/openrouter"
)

func TestKeysCommands(t *testing.T) {
	f := &fakeAPI{keys: []openrouter.Key{{Hash: strings.Repeat("a", 64), Name: "alpha"}}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	repo := t.TempDir()
	policy := "kinds:\n  production: {ttl: 30d}\nproviders:\n  orx: {kind: openrouter, base_url: " + srv.URL + "}\nstate: s3://b/state\n"
	if err := os.WriteFile(filepath.Join(repo, "policy.yaml"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORKEYS_ORX_KEY", "test-token")

	out, err := run(t, srv, "keys", "list", "-p", "orx", "--repo", repo)
	if err != nil || !strings.Contains(out, "alpha") || !strings.Contains(out, "STATUS") {
		t.Fatalf("list: %v, %q", err, out)
	}
	out, err = run(t, srv, "keys", "create", "-p", "orx", "--repo", repo, "--name", "tmp", "--limit", "5", "-q")
	if err != nil || strings.TrimSpace(out) != "sk-or-v1-secret" {
		t.Fatalf("create: %v, %q", err, out)
	}
	if _, err := run(t, srv, "keys", "disable", "-p", "orx", "--repo", repo, "alpha"); err != nil {
		t.Fatal(err)
	}
	if !f.keys[0].Disabled {
		t.Fatal("key not disabled")
	}
	if _, err := run(t, srv, "keys", "list", "-p", "nope", "--repo", repo); err == nil || !strings.Contains(err.Error(), `provider "nope" is not in policy.yaml`) {
		t.Fatalf("unknown provider: %v", err)
	}
}
