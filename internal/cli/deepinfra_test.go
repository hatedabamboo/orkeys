package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

type fakeDIServer struct {
	mu     sync.Mutex
	calls  []string
	limits map[string]any
}

func (f *fakeDIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/api-tokens":
		_, _ = io.WriteString(w, `[{"token_id":"t1","name":"alpha","created_at":1700000000,"scopes":["inference"]},{"token_id":"t2","name":"beta","created_at":1700000000}]`)
	case r.Method == http.MethodGet && r.URL.Path == "/payment/key-limits":
		_ = json.NewEncoder(w).Encode([]map[string]any{{"token_id": "t1", "limit": f.limits["t1"], "spend": 3.5}, {"token_id": "t2", "limit": nil, "spend": nil}})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/api-tokens":
		_, _ = io.WriteString(w, `{"token_id":"t3","name":"new","created_at":1700000000,"token":"sk-di-secret"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/payment/key-limits":
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		f.limits[in["token_id"].(string)] = in["limit"]
	case r.Method == http.MethodPatch:
		_, _ = io.WriteString(w, `{"token_id":"t1","name":"alpha","created_at":1700000000,"scopes":["inference","models"]}`)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func runDI(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	t.Setenv(EnvDIKey, "test-token")
	t.Setenv(EnvDIBase, srv.URL)
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	cmd := (&app{version: "test"}).root()
	cmd.SetArgs(append([]string{"di"}, args...))
	err := cmd.ExecuteContext(context.Background())
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out), err
}

func TestDeepInfraCommands(t *testing.T) {
	f := &fakeDIServer{limits: map[string]any{"t1": 50.0}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	out, err := runDI(t, srv, "list", "--json")
	var tokens []diToken
	if err != nil || json.Unmarshal([]byte(out), &tokens) != nil || len(tokens) != 2 || *tokens[0].Spend != 3.5 || *tokens[0].Limit != 50 {
		t.Fatalf("list: %v %q", err, out)
	}
	if out, err := runDI(t, srv, "get", "alpha"); err != nil || !strings.Contains(out, "t1") || !strings.Contains(out, "$50.00") {
		t.Fatalf("get by name: %v %q", err, out)
	}
	if _, err := runDI(t, srv, "get", "nope"); err == nil {
		t.Fatal("want error for unknown token")
	}

	f.calls = nil
	out, err = runDI(t, srv, "create", "--name", "new", "--limit", "20", "-q")
	if err != nil || strings.TrimSpace(out) != "sk-di-secret" {
		t.Fatalf("create: %v %q", err, out)
	}
	if !strings.Contains(f.calls[0], `"scopes":["inference"]`) || !strings.Contains(f.calls[1], `"limit":20`) {
		t.Fatalf("create calls: %v", f.calls)
	}

	if _, err := runDI(t, srv, "update", "t1", "--limit", "none", "--scopes", "inference,models"); err != nil {
		t.Fatal(err)
	}
	if f.limits["t1"] != nil {
		t.Fatalf("limit not removed: %v", f.limits)
	}
	if _, err := runDI(t, srv, "disable", "beta"); err != nil || f.limits["t2"] != 0.0 {
		t.Fatalf("disable: %v %v", err, f.limits)
	}
	if _, err := runDI(t, srv, "delete", "alpha"); err == nil {
		t.Fatal("delete without --yes and without a terminal must fail")
	}
	f.calls = nil
	if _, err := runDI(t, srv, "delete", "alpha", "-y"); err != nil || !strings.HasPrefix(f.calls[len(f.calls)-1], "DELETE /v1/api-tokens/id/t1") {
		t.Fatalf("delete: %v %v", err, f.calls)
	}
}
