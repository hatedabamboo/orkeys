package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeFal struct {
	mu    sync.Mutex
	calls []string
	auth  string
	query string
}

func (f *fakeFal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	f.auth = r.Header.Get("Authorization")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/keys":
		if r.URL.Query().Get("cursor") == "" {
			_, _ = io.WriteString(w, `{"keys":[{"key_id":"k1","alias":"alpha","created_at":"2026-11-01T10:00:00Z","expires_at":null,"mode":"preset","preset":"API"}],"has_more":true,"next_cursor":"Mg=="}`)
			return
		}
		_, _ = io.WriteString(w, `{"keys":[{"key_id":"k2","alias":"beta","created_at":"2026-11-02T10:00:00.123456","expires_at":"2027-01-01T00:00:00+00:00","mode":"custom","permissions":["models:requests:submit"]}],"has_more":false,"next_cursor":null}`)
	case r.Method == http.MethodGet && r.URL.Path == "/models/usage":
		f.query = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"time_series":[
			{"bucket":"2026-11-16T00:00:00+00:00","results":[
				{"endpoint_id":"fal-ai/flux","cost_total":1.5,"currency":"USD","auth_method_structured":{"detail":"key","api_key_id":"k1"}},
				{"endpoint_id":"fal-ai/flux","cost_total":9,"currency":"USD","auth_method_structured":{"detail":"login","login_username":"u"}}]},
			{"bucket":"2026-11-17T00:00:00+00:00","results":[
				{"endpoint_id":"fal-ai/flux","cost_total":2,"currency":"USD","auth_method_structured":{"detail":"key","api_key_id":"k1"}}]}],
			"has_more":false,"next_cursor":null}`)
	case r.Method == http.MethodPost && r.URL.Path == "/keys":
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"key_id":"k3","key_secret":"s3","key":"k3:s3"}`)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/revoke"):
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTestFal(t *testing.T, f *fakeFal) *falAI {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := newFal(Config{BaseURL: srv.URL, Key: "self:secret", Timeout: time.Second})
	p.now = func() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC) }
	return p
}

func TestFalList(t *testing.T) {
	f := &fakeFal{}
	p := newTestFal(t, f)
	keys, err := p.List(context.Background())
	if err != nil || len(keys) != 2 {
		t.Fatal(keys, err)
	}
	if f.auth != "Key self:secret" {
		t.Fatalf("auth header: %q", f.auth)
	}
	if !strings.Contains(f.query, "start=2026-11-01T00%3A00%3A00Z") || !strings.Contains(f.query, "expand=auth_method_structured") {
		t.Fatalf("usage query: %s", f.query)
	}
	k1, k2 := keys[0], keys[1]
	if k1.Name != "alpha" || *k1.SpendDayUSD != 2 || *k1.SpendWeekUSD != 3.5 || *k1.SpendMonthUSD != 3.5 || k1.ExpiresAt != nil {
		t.Fatalf("k1: %+v", k1)
	}
	if *k2.SpendMonthUSD != 0 || k2.ExpiresAt == nil || k2.ExpiresAt.Year() != 2027 || k2.CreatedAt.Day() != 2 {
		t.Fatalf("k2: %+v", k2)
	}
}

func TestFalSelfID(t *testing.T) {
	if got := newFal(Config{Key: "abc:def"}).SelfID(); got != "abc" {
		t.Fatal(got)
	}
	if got := newFal(Config{Key: "abc:def", SelfID: "x"}).SelfID(); got != "x" {
		t.Fatal(got)
	}
}

func TestFalCreate(t *testing.T) {
	f := &fakeFal{}
	p := newTestFal(t, f)
	exp := time.Date(2027, 2, 15, 0, 0, 0, 0, time.UTC)
	k, secret, err := p.Create(context.Background(), Spec{Name: "new", ExpiresAt: &exp})
	if err != nil || secret != "k3:s3" || k.ID != "k3" || !k.ExpiresAt.Equal(exp) {
		t.Fatal(k, secret, err)
	}
	if want := `POST /keys {"alias":"new","preset":"API","expires_at":"2027-02-15T00:00:00Z"}`; f.calls[0] != want {
		t.Fatalf("create: %s", f.calls[0])
	}

	f.calls = nil
	if _, _, err := p.Create(context.Background(), Spec{Name: "p", Scopes: []string{"models:requests:submit", "files:read"}}); err != nil {
		t.Fatal(err)
	}
	if want := `POST /keys {"alias":"p","permissions":["models:requests:submit","files:read"]}`; f.calls[0] != want {
		t.Fatalf("create with permissions: %s", f.calls[0])
	}

	f.calls = nil
	for _, bad := range []Spec{
		{Name: "full", Scopes: []string{"FULL"}},
		{Name: "late", ExpiresAt: new(time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC))},
	} {
		if _, _, err := p.Create(context.Background(), bad); err == nil {
			t.Fatalf("%s: want error", bad.Name)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("refused create called the API: %v", f.calls)
	}
}

func TestFalDelete(t *testing.T) {
	f := &fakeFal{}
	p := newTestFal(t, f)
	if err := p.Delete(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	if f.calls[0] != "POST /keys/k1/revoke " {
		t.Fatalf("revoke: %q", f.calls[0])
	}
	if _, ok := Provider(p).(Disabler); ok {
		t.Fatal("fal must not be a Disabler")
	}
	if _, ok := Provider(p).(Limiter); ok {
		t.Fatal("fal must not be a Limiter")
	}
}

func TestCheckFalScopes(t *testing.T) {
	for scopes, ok := range map[string]bool{
		"API":                               true,
		"SERVERLESS":                        true,
		"models:requests:submit,files:read": true,
		"FULL":                              false,
		"ADMIN":                             false,
		"auth:keys:write":                   false,
		"models:list,auth:keys:create":      false,
		"auth:keys:read":                    true,
		"api":                               false,
		"API,models:requests:submit":        false,
		"API,DEPLOY":                        false,
	} {
		if err := checkFalScopes(strings.Split(scopes, ",")); (err == nil) != ok {
			t.Errorf("%s: %v", scopes, err)
		}
	}
}
