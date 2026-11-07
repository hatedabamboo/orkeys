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

	"github.com/hatedabamboo/orkeys/internal/openrouter"
)

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
	keys   []openrouter.Key
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
			_ = enc.Encode(map[string]any{"data": []openrouter.Key{}})
			return
		}
		_ = enc.Encode(map[string]any{"data": f.keys})
	case r.Method == http.MethodPost && r.URL.Path == "/keys":
		var req openrouter.CreateRequest
		_ = json.Unmarshal(body, &req)
		k := openrouter.Key{Hash: strings.Repeat("c", 64), Name: req.Name, Limit: req.Limit}
		f.keys = append(f.keys, k)
		w.WriteHeader(http.StatusCreated)
		_ = enc.Encode(openrouter.CreateResponse{Data: k, Key: "sk-or-v1-secret"})
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
	f := &fakeAPI{keys: []openrouter.Key{
		{Hash: strings.Repeat("a", 64), Name: "alpha"},
		{Hash: strings.Repeat("b", 64), Name: "beta", Disabled: true},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	out, err := run(t, srv, "or", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var keys []openrouter.Key
	if err := json.Unmarshal([]byte(out), &keys); err != nil || len(keys) != 2 {
		t.Fatalf("list --json: %v, %q", err, out)
	}

	out, err = run(t, srv, "or", "list")
	if err != nil || !strings.Contains(out, "alpha") || !strings.Contains(out, "HASH") {
		t.Fatalf("list table: %v, %q", err, out)
	}

	out, err = run(t, srv, "or", "create", "--name", "ci", "--limit", "5", "-q")
	if err != nil || strings.TrimSpace(out) != "sk-or-v1-secret" {
		t.Fatalf("create -q: %v, %q", err, out)
	}

	out, err = run(t, srv, "or", "create", "--name", "ci2", "-o", "json")
	if err != nil || !strings.Contains(out, `"key": "sk-or-v1-secret"`) {
		t.Fatalf("create -o json: %v, %q", err, out)
	}

	// Resolve by hash prefix, then by name.
	if _, err := run(t, srv, "or", "update", "aaaa", "--name", "alpha2"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, srv, "or", "disable", "alpha2"); err != nil {
		t.Fatal(err)
	}
	if !f.keys[0].Disabled || f.keys[0].Name != "alpha2" {
		t.Fatalf("key not updated: %+v", f.keys[0])
	}

	if _, err := run(t, srv, "or", "get", "nope"); err == nil || !strings.Contains(err.Error(), "no key matches") {
		t.Fatalf("get nope: %v", err)
	}
	if _, err := run(t, srv, "or", "list", "--json", "-o", "wide"); err == nil {
		t.Fatal("want conflict error for --json with -o wide")
	}
	if _, err := run(t, srv, "or", "update", "alpha2"); err == nil {
		t.Fatal("want error for update without flags")
	}
}
