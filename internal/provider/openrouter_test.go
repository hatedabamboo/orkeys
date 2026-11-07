package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeOR struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeOR) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/keys":
		if r.URL.Query().Get("offset") != "" {
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"hash":"h1","name":"a","disabled":false,"limit":100,"limit_reset":"monthly",
			"usage":50,"usage_monthly":12.5,"created_at":"2026-11-01T10:00:00Z","last_used_at":"2026-11-05T08:30:00Z",
			"expires_at":"2027-01-30T10:00:00Z"},{"hash":"h2","name":"b","disabled":true,"limit":null,"usage_monthly":0,
			"created_at":"2026-10-01T00:00:00Z"}]}`)
	case r.Method == http.MethodPost && r.URL.Path == "/keys":
		_, _ = io.WriteString(w, `{"data":{"hash":"h3","name":"new","created_at":"2026-11-06T00:00:00Z","limit":200,"limit_reset":"monthly"},"key":"sk-or-v1-secret"}`)
	case r.Method == http.MethodPatch:
		_, _ = io.WriteString(w, `{"data":{"hash":"h1","name":"a","created_at":"2026-11-01T10:00:00Z"}}`)
	case r.Method == http.MethodDelete && r.URL.Path == "/keys/gone":
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":404,"message":"Key not found"}}`)
	case r.Method == http.MethodDelete:
		_, _ = io.WriteString(w, `{"deleted":true}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTestOR(t *testing.T) (*fakeOR, Provider) {
	t.Helper()
	f := &fakeOR{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p, err := New("openrouter", Config{BaseURL: srv.URL, Key: "k", Workspace: "ws1"})
	if err != nil {
		t.Fatal(err)
	}
	return f, p
}

func TestOpenRouterList(t *testing.T) {
	f, p := newTestOR(t)
	keys, err := p.List(context.Background())
	if err != nil || len(keys) != 2 {
		t.Fatal(keys, err)
	}
	a := keys[0]
	if a.ID != "h1" || *a.SpendMonthUSD != 12.5 || *a.LimitUSD != 100 || a.LimitReset != "monthly" || a.Disabled {
		t.Fatalf("a: %+v", a)
	}
	if !a.CreatedAt.Equal(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)) ||
		!a.LastUsedAt.Equal(time.Date(2026, 11, 5, 8, 30, 0, 0, time.UTC)) ||
		!a.ExpiresAt.Equal(time.Date(2027, 1, 30, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("times: %+v", a)
	}
	b := keys[1]
	if !b.Disabled || b.LimitUSD != nil || b.LastUsedAt != nil || b.ExpiresAt != nil || b.LimitReset != "" {
		t.Fatalf("b: %+v", b)
	}
	if !strings.Contains(f.calls[0], "/keys") {
		t.Fatal(f.calls)
	}
}

func TestOpenRouterCreate(t *testing.T) {
	f, p := newTestOR(t)
	limit := 200.0
	exp := time.Date(2027, 2, 4, 12, 30, 15, 999, time.UTC)
	k, secret, err := p.Create(context.Background(), Spec{Name: "new", LimitUSD: &limit, LimitReset: "monthly", ExpiresAt: &exp})
	if err != nil || k.ID != "h3" || secret != "sk-or-v1-secret" {
		t.Fatal(k, secret, err)
	}
	var req map[string]any
	_ = json.Unmarshal([]byte(strings.SplitN(f.calls[0], " ", 3)[2]), &req)
	if req["name"] != "new" || req["limit"] != 200.0 || req["limit_reset"] != "monthly" ||
		req["expires_at"] != "2027-02-04T12:30:15Z" || req["workspace_id"] != "ws1" {
		t.Fatalf("create body: %v", req)
	}

	f.calls = nil
	if _, _, err := p.Create(context.Background(), Spec{Name: "nolimit", LimitReset: "monthly"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.calls[0], "limit_reset") {
		t.Fatalf("limit_reset without limit: %v", f.calls[0])
	}
}

func TestOpenRouterUpdateAndDelete(t *testing.T) {
	f, p := newTestOR(t)
	ctx := context.Background()
	limit := 50.0
	if err := p.SetLimit(ctx, "h1", &limit, "weekly"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetLimit(ctx, "h1", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Disable(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`PATCH /keys/h1 {"limit":50,"limit_reset":"weekly"}`,
		`PATCH /keys/h1 {"limit":null,"limit_reset":null}`,
		`PATCH /keys/h1 {"disabled":true}`,
	}
	for i, w := range want {
		if f.calls[i] != w {
			t.Fatalf("call %d: got %q, want %q", i, f.calls[i], w)
		}
	}
	if err := p.Delete(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, "gone"); err != nil {
		t.Fatalf("delete of a missing key must succeed: %v", err)
	}
}
