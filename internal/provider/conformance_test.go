package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/provider/providertest"
)

// Each adapter runs the contract test against a fake of its API that keeps keys in memory.

func TestContractOpenRouter(t *testing.T) {
	srv := httptest.NewServer(&memOpenRouter{})
	t.Cleanup(srv.Close)
	p, err := provider.New("openrouter", provider.Config{BaseURL: srv.URL, Key: "k", SelfID: "self"})
	if err != nil {
		t.Fatal(err)
	}
	providertest.Run(t, p)
}

func TestContractDeepInfra(t *testing.T) {
	srv := httptest.NewServer(&memDeepInfra{})
	t.Cleanup(srv.Close)
	p, err := provider.New("deepinfra", provider.Config{BaseURL: srv.URL, Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	providertest.Run(t, p)
}

func TestContractOpenAI(t *testing.T) {
	srv := httptest.NewServer(&memOpenAI{})
	t.Cleanup(srv.Close)
	p, err := provider.New("openai", provider.Config{BaseURL: srv.URL, Key: "k", Options: map[string]string{"project": "proj_1"}})
	if err != nil {
		t.Fatal(err)
	}
	providertest.Run(t, p)
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func decode(r *http.Request) map[string]any {
	var m map[string]any
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &m)
	return m
}

type memOpenRouter struct {
	mu   sync.Mutex
	n    int
	keys []map[string]any
}

func (f *memOpenRouter) find(hash string) int {
	for i, k := range f.keys {
		if k["hash"] == hash {
			return i
		}
	}
	return -1
}

func (f *memOpenRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hash, one := strings.CutPrefix(r.URL.Path, "/keys/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/keys":
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		page := []map[string]any{}
		if off < len(f.keys) {
			page = f.keys[off:]
		}
		writeJSON(w, map[string]any{"data": page})
	case r.Method == http.MethodPost && r.URL.Path == "/keys":
		in := decode(r)
		f.n++
		k := map[string]any{"hash": fmt.Sprintf("h%d", f.n), "name": in["name"], "disabled": false, "limit": in["limit"],
			"limit_reset": in["limit_reset"], "expires_at": in["expires_at"], "created_at": time.Now().UTC().Format(time.RFC3339)}
		f.keys = append(f.keys, k)
		writeJSON(w, map[string]any{"data": k, "key": "sk-or-" + k["hash"].(string)})
	case one && r.Method == http.MethodPatch:
		i := f.find(hash)
		if i < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		for field, v := range decode(r) {
			f.keys[i][field] = v
		}
		writeJSON(w, map[string]any{"data": f.keys[i]})
	case one && r.Method == http.MethodDelete:
		i := f.find(hash)
		if i < 0 {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{"error": map[string]any{"code": 404, "message": "Key not found"}})
			return
		}
		f.keys = append(f.keys[:i], f.keys[i+1:]...)
		writeJSON(w, map[string]any{"deleted": true})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type memDeepInfra struct {
	mu     sync.Mutex
	n      int
	tokens []map[string]any
	limits map[string]any
}

func (f *memDeepInfra) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.limits == nil {
		f.limits = map[string]any{}
	}
	id, one := strings.CutPrefix(r.URL.Path, "/v1/api-tokens/id/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/api-tokens":
		out := []map[string]any{}
		out = append(out, f.tokens...)
		writeJSON(w, out)
	case r.Method == http.MethodGet && r.URL.Path == "/payment/key-limits":
		out := []map[string]any{}
		for _, t := range f.tokens {
			out = append(out, map[string]any{"token_id": t["token_id"], "name": t["name"], "limit": f.limits[t["token_id"].(string)], "spend": 0})
		}
		writeJSON(w, out)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/api-tokens":
		in := decode(r)
		f.n++
		t := map[string]any{"token_id": fmt.Sprintf("t%d", f.n), "name": in["name"], "created_at": time.Now().Unix()}
		f.tokens = append(f.tokens, t)
		writeJSON(w, map[string]any{"token_id": t["token_id"], "name": t["name"], "created_at": t["created_at"], "token": "sk-di-" + t["token_id"].(string)})
	case r.Method == http.MethodPost && r.URL.Path == "/payment/key-limits":
		in := decode(r)
		f.limits[in["token_id"].(string)] = in["limit"]
	case one && r.Method == http.MethodDelete:
		for i, t := range f.tokens {
			if t["token_id"] == id {
				f.tokens = append(f.tokens[:i], f.tokens[i+1:]...)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type memOpenAI struct {
	mu   sync.Mutex
	n    int
	keys []map[string]any
	sas  map[string]bool
}

func (f *memOpenAI) addKey(sa, name string) map[string]any {
	f.n++
	k := map[string]any{"id": fmt.Sprintf("key_%d", f.n), "name": name, "created_at": time.Now().Unix(),
		"owner": map[string]any{"type": "service_account", "service_account": map[string]any{"id": sa}}, "owner_project_access": "active"}
	f.keys = append(f.keys, k)
	return k
}

func (f *memOpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sas == nil {
		f.sas = map[string]bool{}
	}
	const base = "/organization/projects/proj_1"
	rest, inProject := strings.CutPrefix(r.URL.Path, base+"/")
	parts := strings.Split(rest, "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/organization/costs":
		writeJSON(w, map[string]any{"data": []any{}, "has_more": false})
	case inProject && r.Method == http.MethodGet && rest == "api_keys":
		writeJSON(w, map[string]any{"data": f.keys, "has_more": false})
	case inProject && r.Method == http.MethodPost && rest == "service_accounts":
		in := decode(r)
		f.n++
		sa := fmt.Sprintf("sa_%d", f.n)
		f.sas[sa] = true
		def := f.addKey(sa, "")
		writeJSON(w, map[string]any{"id": sa, "name": in["name"], "api_key": map[string]any{"id": def["id"], "value": "sk-default"}})
	case inProject && r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "service_accounts" && parts[2] == "api_keys":
		in := decode(r)
		k := f.addKey(parts[1], fmt.Sprint(in["name"]))
		writeJSON(w, map[string]any{"id": k["id"], "name": k["name"], "value": "sk-oa-" + k["id"].(string), "created_at": k["created_at"]})
	case inProject && r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "api_keys":
		for i, k := range f.keys {
			if k["id"] == parts[1] {
				f.keys = append(f.keys[:i], f.keys[i+1:]...)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	case inProject && r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "service_accounts":
		if !f.sas[parts[1]] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.sas, parts[1])
		kept := f.keys[:0]
		for _, k := range f.keys {
			if k["owner"].(map[string]any)["service_account"].(map[string]any)["id"] != parts[1] {
				kept = append(kept, k)
			}
		}
		f.keys = kept
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
