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
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": f.versions[full]}})
	case kind == "metadata" && r.Method == http.MethodGet:
		if _, ok := f.data[full]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"custom_metadata": f.meta[full]}})
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
		delete(f.meta, full)
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
	if f.data["kv/app/x"]["other"] != "keep" || f.data["kv/app/x"]["api_key"] != "sk" || f.meta["kv/app/x"]["orkeys-key.api_key"] != "prod-a-b" {
		t.Fatalf("secret: %v %v", f.data["kv/app/x"], f.meta)
	}
	if got, err := sec.Read(ctx, "kv/app/x"); err != nil || got["api_key"] != "sk" {
		t.Fatalf("read: %v %v", got, err)
	}
	if m, err := sec.Meta(ctx, "kv/app/x", "api_key"); err != nil || m["orkeys-key"] != "prod-a-b" {
		t.Fatalf("meta: %v %v", m, err)
	}
	if m, err := sec.Meta(ctx, "kv/app/none", "api_key"); err != nil || m != nil {
		t.Fatalf("meta of a missing secret: %v %v", m, err)
	}
	if err := sec.DeleteField(ctx, "kv/app/x", "api_key"); err != nil || f.data["kv/app/x"]["api_key"] != nil || f.data["kv/app/x"]["other"] != "keep" {
		t.Fatalf("delete field: %v %v", err, f.data["kv/app/x"])
	}
	if _, ok := f.meta["kv/app/x"]["orkeys-key.api_key"]; ok {
		t.Fatalf("delete field must drop its metadata: %v", f.meta["kv/app/x"])
	}
	if err := sec.DeleteField(ctx, "kv/app/x", "other"); err != nil || f.data["kv/app/x"] != nil {
		t.Fatalf("delete of the last field must destroy the secret: %v %v", err, f.data)
	}
	if err := s.DeleteState(ctx, "prod-a-b"); err != nil || f.data["kv/orkeys/state/prod-a-b"] != nil {
		t.Fatal("delete")
	}
	if _, err := New(srv.URL, "bad").Read(ctx, "kv/app/x"); err == nil {
		t.Fatal("want 403 error")
	}
}

func TestFieldMetadata(t *testing.T) {
	f := &fakeVault{data: map[string]map[string]any{"kv/app/x": {"a": "old"}}, meta: map[string]map[string]string{
		"kv/app/x": {"orkeys-key": "prod-a", "orkeys-key-id": "k0", "orkeys-team": "t", "owner": "infra"},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	sec := &SecretStore{C: New(srv.URL, "t")}
	ctx := context.Background()

	if m, _ := sec.Meta(ctx, "kv/app/x", "a"); m["orkeys-key-id"] != "k0" {
		t.Fatalf("a field without own metadata must read the per-secret metadata: %v", m)
	}
	if err := sec.Put(ctx, "kv/app/x", "b", "sb", map[string]string{"orkeys-key-id": "kb"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.meta["kv/app/x"]["orkeys-key-id"]; ok || f.meta["kv/app/x"]["owner"] != "infra" {
		t.Fatalf("put must drop the per-secret orkeys metadata and keep other metadata: %v", f.meta["kv/app/x"])
	}
	if err := sec.Put(ctx, "kv/app/x", "a", "sa", map[string]string{"orkeys-key-id": "ka"}); err != nil {
		t.Fatal(err)
	}
	ma, _ := sec.Meta(ctx, "kv/app/x", "a")
	mb, _ := sec.Meta(ctx, "kv/app/x", "b")
	if ma["orkeys-key-id"] != "ka" || mb["orkeys-key-id"] != "kb" {
		t.Fatalf("meta a %v, b %v", ma, mb)
	}
	if err := sec.DeleteField(ctx, "kv/app/x", "a"); err != nil {
		t.Fatal(err)
	}
	ma, _ = sec.Meta(ctx, "kv/app/x", "a")
	mb, _ = sec.Meta(ctx, "kv/app/x", "b")
	if len(ma) != 0 || mb["orkeys-key-id"] != "kb" {
		t.Fatalf("after delete: meta a %v, b %v", ma, mb)
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

func TestStateConflicts(t *testing.T) {
	f := &fakeVault{data: map[string]map[string]any{}, meta: map[string]map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s := &StateStore{C: New(srv.URL, "t"), Prefix: "kv/orkeys/state"}
	ctx := context.Background()

	st := &state.State{Name: "prod-a-b", KeyID: "k1"}
	if err := s.PutState(ctx, st); err != nil || st.Rev != "1" {
		t.Fatalf("create: %v rev %q", err, st.Rev)
	}
	if err := s.PutState(ctx, &state.State{Name: "prod-a-b", KeyID: "k2"}); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("second create must conflict: %v", err)
	}
	read, _ := s.GetState(ctx, "prod-a-b")
	st.KeyID = "k3"
	if err := s.PutState(ctx, st); err != nil || st.Rev != "2" {
		t.Fatalf("update: %v rev %q", err, st.Rev)
	}
	read.KeyID = "stale"
	if err := s.PutState(ctx, read); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("write with a stale revision must conflict: %v", err)
	}
}

func TestHeartbeats(t *testing.T) {
	f := &fakeVault{data: map[string]map[string]any{}, meta: map[string]map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s := &StateStore{C: New(srv.URL, "t"), Prefix: "kv/orkeys/state"}
	ctx := context.Background()
	t1 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if err := s.PutHeartbeat(ctx, "rotate", t1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutHeartbeat(ctx, "apply", t1.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Heartbeats(ctx)
	if err != nil || !got["rotate"].Equal(t1) || !got["apply"].Equal(t1.Add(time.Hour)) {
		t.Fatalf("heartbeats %v %v", got, err)
	}
	if all, _ := s.ListStates(ctx); len(all) != 0 {
		t.Fatal("heartbeat listed as state")
	}
}

func TestStoredPolicy(t *testing.T) {
	f := &fakeVault{data: map[string]map[string]any{}, meta: map[string]map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s := &StateStore{C: New(srv.URL, "t"), Prefix: "kv/orkeys/state"}
	ctx := context.Background()
	if p, err := s.GetPolicy(ctx); p != nil || err != nil {
		t.Fatalf("no policy must be nil, nil: %v %v", p, err)
	}
	t1 := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	if err := s.PutPolicy(ctx, &state.StoredPolicy{YAML: "state: vault://kv/orkeys/state\n", UpdatedAt: t1}); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetPolicy(ctx)
	if err != nil || p.YAML != "state: vault://kv/orkeys/state\n" || !p.UpdatedAt.Equal(t1) {
		t.Fatalf("policy %+v %v", p, err)
	}
	if all, _ := s.ListStates(ctx); len(all) != 0 {
		t.Fatal("policy listed as state")
	}
}
