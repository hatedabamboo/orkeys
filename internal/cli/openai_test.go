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

type fakeOAServer struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeOAServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	const base = "/organization/projects/proj_1"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == base+"/api_keys":
		_, _ = io.WriteString(w, `{"data":[
			{"id":"key_a","name":"alpha","created_at":1700000000,"owner_project_access":"active","owner":{"type":"service_account","service_account":{"id":"sa_a","name":"alpha"}}},
			{"id":"key_b","name":"beta","created_at":1700000000,"owner_project_access":"active","owner":{"type":"user","user":{"id":"u1","email":"u@example.com"}}}],"has_more":false}`)
	case r.Method == http.MethodGet && r.URL.Path == "/organization/costs":
		_, _ = io.WriteString(w, `{"data":[],"has_more":false}`)
	case r.Method == http.MethodPost && r.URL.Path == base+"/api_keys/key_a":
		_, _ = io.WriteString(w, `{}`)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func runOA(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	t.Setenv(EnvOAKey, "test-token")
	t.Setenv(EnvOABase, srv.URL)
	t.Setenv(EnvOAProject, "proj_1")
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	cmd := (&app{version: "test"}).root()
	cmd.SetArgs(append([]string{"oa"}, args...))
	err := cmd.ExecuteContext(context.Background())
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out), err
}

func TestOpenAICommands(t *testing.T) {
	f := &fakeOAServer{}
	srv := httptest.NewServer(f)
	defer srv.Close()

	out, err := runOA(t, srv, "list", "-o", "wide")
	if err != nil || !strings.Contains(out, "service_account:alpha") || !strings.Contains(out, "user:u@example.com") {
		t.Fatalf("list: %v\n%s", err, out)
	}

	if _, err := runOA(t, srv, "update", "alpha", "--scopes", "api.all.read"); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-1]; last != `POST /organization/projects/proj_1/api_keys/key_a {"scopes":["api.all.read"]}` {
		t.Fatalf("update: %s", last)
	}

	f.calls = nil
	if _, err := runOA(t, srv, "delete", "alpha", "-y"); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(f.calls, "\n")
	if !strings.Contains(all, "DELETE /organization/projects/proj_1/api_keys/key_a") ||
		!strings.Contains(all, "DELETE /organization/projects/proj_1/service_accounts/sa_a") {
		t.Fatalf("delete calls:\n%s", all)
	}

	if _, err := runOA(t, srv, "delete", "nope", "-y"); err == nil || !strings.Contains(err.Error(), `no key matches "nope"`) {
		t.Fatalf("unknown key: %v", err)
	}
}
