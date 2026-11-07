package vault

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/state"
)

type fakeVault struct {
	data     map[string]map[string]any
	meta     map[string]map[string]string
	versions map[string]int
}

func (f *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Vault-Token") != "t" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/v1/")
	mount, rest, _ := strings.Cut(p, "/")
	kind, path, _ := strings.Cut(rest, "/")
	full := mount + "/" + path
	body, _ := io.ReadAll(r.Body)
	switch {
	case kind == "data" && r.Method == http.MethodGet:
		d, ok := f.data[full]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": d, "metadata": map[string]any{"version": f.versions[full]}}})
	case kind == "data" && r.Method == http.MethodPost:
		var in struct {
			Data    map[string]any
			Options *struct{ CAS int }
		}
		_ = json.Unmarshal(body, &in)
		if f.versions == nil {
			f.versions = map[string]int{}
		}
		if in.Options != nil && in.Options.CAS != f.versions[full] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errors":["check-and-set parameter did not match the current version"]}`)
			return
		}
		f.data[full] = in.Data
		f.versions[full]++
	case kind == "metadata" && r.Method == http.MethodPost:
		var in struct {
			CustomMetadata map[string]string `json:"custom_metadata"`
		}
		_ = json.Unmarshal(body, &in)
		f.meta[full] = in.CustomMetadata
	case kind == "metadata" && r.Method == "LIST":
		var keys []string
		for k := range f.data {
			if strings.HasPrefix(k, full+"/") {
				keys = append(keys, strings.TrimPrefix(k, full+"/"))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"keys": keys}})
	case kind == "metadata" && r.Method == http.MethodDelete:
		delete(f.data, full)
		delete(f.versions, full)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestStore(t *testing.T) {
	f := &fakeVault{data: map[string]map[string]any{"kv/app/x": {"other": "keep"}}, meta: map[string]map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s := &StateStore{C: New(srv.URL, "t"), Prefix: "kv/orkeys/state"}
	sec := &SecretStore{C: s.C}
	ctx := context.Background()

	st := &state.State{Name: "prod-a-b", KeyID: "k1", ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		PendingRevokes: []state.PendingRevoke{{KeyID: "k0", After: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}}}
	if err := s.PutState(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetState(ctx, "prod-a-b")
	if err != nil || got.KeyID != "k1" || !got.ExpiresAt.Equal(st.ExpiresAt) || got.PendingRevokes[0].KeyID != "k0" {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	all, err := s.ListStates(ctx)
	if err != nil || len(all) != 1 {
		t.Fatal(all, err)
	}
	if missing, err := s.GetState(ctx, "nope"); missing != nil || err != nil {
		t.Fatal("missing state must be nil, nil")
	}

	if err := sec.Put(ctx, "kv/app/x", "api_key", "sk", map[string]string{"orkeys-key": "prod-a-b"}); err != nil {
		t.Fatal(err)
	}
	if f.data["kv/app/x"]["other"] != "keep" || f.data["kv/app/x"]["api_key"] != "sk" || f.meta["kv/app/x"]["orkeys-key"] != "prod-a-b" {
		t.Fatalf("secret: %v %v", f.data["kv/app/x"], f.meta)
	}
	if got, err := sec.Read(ctx, "kv/app/x"); err != nil || got["api_key"] != "sk" {
		t.Fatalf("read: %v %v", got, err)
	}
	if err := s.DeleteState(ctx, "prod-a-b"); err != nil || f.data["kv/orkeys/state/prod-a-b"] != nil {
		t.Fatal("delete")
	}
	if _, err := New(srv.URL, "bad").Read(ctx, "kv/app/x"); err == nil {
		t.Fatal("want 403 error")
	}
}

func TestAppRoleLoginAndRelogin(t *testing.T) {
	logins := 0
	f := &fakeVault{data: map[string]map[string]any{"kv/a/b": {"k": "v"}}, meta: map[string]map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/approle/login" {
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["role_id"] != "rid" || in["secret_id"] != "sid" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			logins++
			_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "t"}})
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	t.Setenv(EnvAddr, srv.URL)
	t.Setenv(EnvToken, "")
	t.Setenv(EnvRoleID, "rid")
	t.Setenv(EnvSecretID, "sid")
	c, err := NewFromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c.token = "expired"
	data, err := c.Read(context.Background(), "kv/a/b")
	v, _ := data["k"].(string)
	if err != nil || v != "v" || logins != 2 {
		t.Fatalf("v=%q err=%v logins=%d", v, err, logins)
	}
}

func TestVaultLock(t *testing.T) {
	f := &fakeVault{data: map[string]map[string]any{}, meta: map[string]map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s := &StateStore{C: New(srv.URL, "t"), Prefix: "kv/orkeys/state"}
	ctx := context.Background()
	now := time.Now().UTC()
	a := state.LockInfo{ID: "a", Holder: "run-1", Created: now, Expires: now.Add(time.Hour)}
	if err := s.Acquire(ctx, a); err != nil {
		t.Fatal(err)
	}
	var locked *state.LockedError
	if err := s.Acquire(ctx, state.LockInfo{ID: "b", Expires: now.Add(time.Hour)}); !errors.As(err, &locked) || locked.Info.Holder != "run-1" {
		t.Fatalf("second acquire: %v", err)
	}
	if err := s.Renew(ctx, a); err != nil {
		t.Fatal(err)
	}
	if all, err := s.ListStates(ctx); err != nil || len(all) != 0 {
		t.Fatalf("lock listed as state: %v %v", all, err)
	}
	if err := s.Release(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	stale := state.LockInfo{ID: "old", Expires: now.Add(-time.Minute)}
	if err := s.Acquire(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := s.Acquire(ctx, state.LockInfo{ID: "new", Expires: now.Add(time.Hour)}); err != nil {
		t.Fatalf("expired lock not taken over: %v", err)
	}
	if err := s.Renew(ctx, stale); !errors.Is(err, state.ErrLockLost) {
		t.Fatal("old holder must see a lost lock")
	}

	f.versions[f.lockPath()] = 99
	if err := s.writeLock(ctx, a, 1); !errors.Is(err, ErrCASMismatch) {
		t.Fatalf("want CAS mismatch, got %v", err)
	}
}

func (f *fakeVault) lockPath() string { return "kv/orkeys/state/.lock" }
