package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

type fakeFalServer struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeFalServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/keys":
		_, _ = io.WriteString(w, `{"keys":[
			{"key_id":"self","alias":"orkeys","created_at":"2026-11-01T10:00:00Z","expires_at":null,"mode":"preset","preset":"FULL"},
			{"key_id":"k1","alias":"alpha","created_at":"2026-11-01T10:00:00Z","expires_at":null,"mode":"preset","preset":"API"},
			{"key_id":"k2","alias":"beta","created_at":"2026-11-01T10:00:00Z","expires_at":"2027-01-01T00:00:00Z","mode":"custom","permissions":["models:requests:submit"]}],
			"has_more":false,"next_cursor":null}`)
	case r.Method == http.MethodGet && r.URL.Path == "/models/usage":
		_, _ = io.WriteString(w, `{"time_series":[],"has_more":false,"next_cursor":null}`)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/revoke"):
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func runFal(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	t.Setenv(EnvFalKey, "self:test-secret")
	t.Setenv(EnvFalBase, srv.URL)
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	cmd := (&app{version: "test"}).root()
	cmd.SetArgs(append([]string{"fal"}, args...))
	err := cmd.ExecuteContext(context.Background())
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out), err
}

func TestFalCommands(t *testing.T) {
	f := &fakeFalServer{}
	srv := httptest.NewServer(f)
	defer srv.Close()

	out, err := runFal(t, srv, "list", "-o", "wide")
	if err != nil || !strings.Contains(out, "alpha") || !strings.Contains(out, "custom") || !strings.Contains(out, "2027-01-01") {
		t.Fatalf("list: %v\n%s", err, out)
	}

	out, err = runFal(t, srv, "get", "beta")
	if err != nil || !strings.Contains(out, "models:requests:submit") {
		t.Fatalf("get: %v\n%s", err, out)
	}

	f.calls = nil
	if _, err := runFal(t, srv, "delete", "alpha", "-y"); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-1]; last != "POST /keys/k1/revoke " {
		t.Fatalf("revoke: %q", last)
	}

	if _, err := runFal(t, srv, "delete", "orkeys", "-y"); err == nil || !strings.Contains(err.Error(), "refusing to revoke the key in "+EnvFalKey) {
		t.Fatalf("self revoke: %v", err)
	}
	if _, err := runFal(t, srv, "delete", "nope", "-y"); err == nil || !strings.Contains(err.Error(), `no key matches "nope"`) {
		t.Fatalf("unknown key: %v", err)
	}
	if _, err := runFal(t, srv, "create", "--name", "x", "--scopes", "FULL"); err == nil || !strings.Contains(err.Error(), "preset FULL is not allowed") {
		t.Fatalf("create FULL: %v", err)
	}
}
