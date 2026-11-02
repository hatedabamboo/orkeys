package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hatedabamboo/orkeys/internal/api"
)

func ptr[T any](v T) *T { return &v }

func TestDiffUpdate(t *testing.T) {
	orig := api.Key{Name: "a", Limit: ptr(10.0), LimitReset: ptr("daily")}

	req, err := diffUpdate(orig, "a", "10", "daily", false, false)
	if err != nil || !req.Empty() {
		t.Fatalf("unchanged: %+v, %v", req, err)
	}

	req, err = diffUpdate(orig, "b", "", "none", true, true)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req)
	want := `{"disabled":true,"include_byok_in_limit":true,"limit":null,"limit_reset":null,"name":"b"}`
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}

	if _, err := diffUpdate(orig, "a", "abc", "daily", false, false); err == nil {
		t.Fatal("want error for bad limit")
	}
}

func TestWriteKeysAsJSONEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := writeKeysAs(&buf, nil, "json"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Fatalf("got %q, want []", buf.String())
	}
}

// fakeAPI is a minimal in-memory Management API.
type fakeAPI struct {
	mu     sync.Mutex
	keys   []api.Key
	bodies []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if len(body) > 0 {
		f.bodies = append(f.bodies, r.Method+" "+string(body))
	}
	enc := json.NewEncoder(w)
	hash := strings.TrimPrefix(r.URL.Path, "/keys/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/keys":
		if r.URL.Query().Get("offset") != "" {
			_ = enc.Encode(map[string]any{"data": []api.Key{}})
			return
		}
		_ = enc.Encode(map[string]any{"data": f.keys})
	case r.Method == http.MethodPost && r.URL.Path == "/keys":
		var req api.CreateRequest
		_ = json.Unmarshal(body, &req)
		k := api.Key{Hash: strings.Repeat("c", 64), Name: req.Name, Limit: req.Limit}
		f.keys = append(f.keys, k)
		w.WriteHeader(http.StatusCreated)
		_ = enc.Encode(api.CreateResponse{Data: k, Key: "sk-or-v1-secret"})
	case r.Method == http.MethodPatch:
		for i := range f.keys {
			if f.keys[i].Hash == hash {
				var m map[string]any
				_ = json.Unmarshal(body, &m)
				if n, ok := m["name"].(string); ok {
					f.keys[i].Name = n
				}
				if d, ok := m["disabled"].(bool); ok {
					f.keys[i].Disabled = d
				}
				_ = enc.Encode(map[string]any{"data": f.keys[i]})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_ = enc.Encode(map[string]any{"error": map[string]any{"code": 404, "message": "Key not found"}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// run executes the CLI with args and returns stdout.
func run(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	t.Setenv(EnvKey, "test-token")
	t.Setenv(EnvBase, srv.URL)
	t.Setenv(EnvWorkspace, "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := (&app{version: "test"}).root()
	cmd.SetArgs(args)
	runErr := cmd.ExecuteContext(context.Background())

	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func TestCommands(t *testing.T) {
	f := &fakeAPI{keys: []api.Key{
		{Hash: strings.Repeat("a", 64), Name: "alpha"},
		{Hash: strings.Repeat("b", 64), Name: "beta", Disabled: true},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	out, err := run(t, srv, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var keys []api.Key
	if err := json.Unmarshal([]byte(out), &keys); err != nil || len(keys) != 2 {
		t.Fatalf("list --json: %v, %q", err, out)
	}

	out, err = run(t, srv, "list")
	if err != nil || !strings.Contains(out, "alpha") || !strings.Contains(out, "HASH") {
		t.Fatalf("list table: %v, %q", err, out)
	}

	out, err = run(t, srv, "create", "--name", "ci", "--limit", "5", "-q")
	if err != nil || strings.TrimSpace(out) != "sk-or-v1-secret" {
		t.Fatalf("create -q: %v, %q", err, out)
	}

	out, err = run(t, srv, "create", "--name", "ci2", "-o", "json")
	if err != nil || !strings.Contains(out, `"key": "sk-or-v1-secret"`) {
		t.Fatalf("create -o json: %v, %q", err, out)
	}

	// Resolve by hash prefix, then by name.
	if _, err := run(t, srv, "update", "aaaa", "--name", "alpha2"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, srv, "disable", "alpha2"); err != nil {
		t.Fatal(err)
	}
	if !f.keys[0].Disabled || f.keys[0].Name != "alpha2" {
		t.Fatalf("key not updated: %+v", f.keys[0])
	}

	if _, err := run(t, srv, "get", "nope"); err == nil || !strings.Contains(err.Error(), "no key matches") {
		t.Fatalf("get nope: %v", err)
	}
	if _, err := run(t, srv, "list", "--json", "-o", "wide"); err == nil {
		t.Fatal("want conflict error for --json with -o wide")
	}
	if _, err := run(t, srv, "update", "alpha2"); err == nil {
		t.Fatal("want error for update without flags")
	}
}
