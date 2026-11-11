package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/openai"
)

type fakeOA struct {
	mu       sync.Mutex
	calls    []string
	keys     string
	failKey  bool
	costsURL string
}

func (f *fakeOA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	const base = "/organization/projects/proj_1"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == base+"/api_keys":
		_, _ = io.WriteString(w, `{"data":`+f.keys+`,"has_more":false}`)
	case r.Method == http.MethodGet && r.URL.Path == "/organization/costs":
		f.costsURL = r.URL.RawQuery
		// Buckets: 2026-10-31 (last month), 2026-11-02 (this month, last week), 2026-11-11 (today).
		_, _ = io.WriteString(w, `{"data":[
			{"start_time":1793404800,"results":[{"api_key_id":"key_a","amount":{"value":100,"currency":"usd"}}]},
			{"start_time":1793577600,"results":[{"api_key_id":"key_a","amount":{"value":2,"currency":"usd"}}]},
			{"start_time":1794355200,"results":[{"api_key_id":"key_a","amount":{"value":0.5,"currency":"usd"}},{"api_key_id":"","amount":{"value":9,"currency":"usd"}}]}
		],"has_more":false,"next_page":null}`)
	case r.Method == http.MethodPost && r.URL.Path == base+"/service_accounts":
		_, _ = io.WriteString(w, `{"id":"sa_new","name":"n","api_key":{"id":"key_default","value":"sk-default"}}`)
	case r.Method == http.MethodPost && r.URL.Path == base+"/service_accounts/sa_new/api_keys":
		if f.failKey {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"id":"key_new","name":"n","value":"sk-new","created_at":1762819200,"expires_at":1770595200}`)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTestOpenAI(t *testing.T, f *fakeOA) *openAI {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := newOpenAI(Config{BaseURL: srv.URL, Key: "k", Workspace: "proj_1", Timeout: time.Second})
	p.now = func() time.Time { return time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC) }
	return p
}

func TestOpenAIList(t *testing.T) {
	f := &fakeOA{keys: `[
		{"id":"key_a","name":"a","created_at":1700000000,"last_used_at":1762819200,"expires_at":null,"owner_project_access":"active","owner":{"type":"service_account","service_account":{"id":"sa_a"}}},
		{"id":"key_b","name":"b","created_at":1700000000,"owner_project_access":"inactive","owner":{"type":"user","user":{"id":"u1"}}}]`}
	p := newTestOpenAI(t, f)
	keys, err := p.List(context.Background())
	if err != nil || len(keys) != 2 {
		t.Fatal(keys, err)
	}
	a := keys[0]
	if *a.SpendDayUSD != 0.5 || *a.SpendWeekUSD != 0.5 || *a.SpendMonthUSD != 2.5 || *a.UsageUSD != 2.5 {
		t.Fatalf("spend: day %v week %v month %v", *a.SpendDayUSD, *a.SpendWeekUSD, *a.SpendMonthUSD)
	}
	if a.LastUsedAt == nil || a.ExpiresAt != nil || a.Disabled {
		t.Fatalf("key a: %+v", a)
	}
	if !keys[1].Disabled || *keys[1].SpendMonthUSD != 0 {
		t.Fatalf("inactive key b: %+v", keys[1])
	}
	for _, want := range []string{"group_by=api_key_id", "bucket_width=1d", "project_ids=proj_1", "start_time=1793491200"} {
		if !strings.Contains(f.costsURL, want) {
			t.Errorf("costs query %q: missing %s", f.costsURL, want)
		}
	}
}

func TestOpenAICreate(t *testing.T) {
	f := &fakeOA{}
	p := newTestOpenAI(t, f)
	exp := p.now().Add(90 * 24 * time.Hour)
	k, secret, err := p.Create(context.Background(), Spec{Name: "n", ExpiresAt: &exp, Scopes: []string{"api.responses.write"}})
	if err != nil || secret != "sk-new" || k.ID != "key_new" || k.ExpiresAt == nil {
		t.Fatal(k, secret, err)
	}
	var req map[string]any
	_ = json.Unmarshal([]byte(strings.SplitN(f.calls[1], " ", 3)[2]), &req)
	if req["name"] != "n" || req["expires_in_seconds"] != float64(90*24*3600) || req["scopes"].([]any)[0] != "api.responses.write" {
		t.Fatalf("key request: %v", f.calls[1])
	}
	if !strings.HasPrefix(f.calls[2], "DELETE /organization/projects/proj_1/api_keys/key_default") {
		t.Fatalf("default key not deleted: %v", f.calls)
	}

	f.failKey, f.calls = true, nil
	if _, _, err := p.Create(context.Background(), Spec{Name: "n"}); err == nil {
		t.Fatal("want error")
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasPrefix(last, "DELETE /organization/projects/proj_1/service_accounts/sa_new") {
		t.Fatalf("service account not deleted: %v", f.calls)
	}

	far := p.now().Add(400 * 24 * time.Hour)
	if _, _, err := p.Create(context.Background(), Spec{Name: "n", ExpiresAt: &far}); err == nil {
		t.Fatal("want error for expiry over one year")
	}
}

func TestOpenAIDelete(t *testing.T) {
	f := &fakeOA{keys: `[
		{"id":"key_a","owner":{"type":"service_account","service_account":{"id":"sa_a"}}},
		{"id":"key_b","owner":{"type":"service_account","service_account":{"id":"sa_shared"}}},
		{"id":"key_c","owner":{"type":"service_account","service_account":{"id":"sa_shared"}}}]`}
	p := newTestOpenAI(t, f)
	ctx := context.Background()

	if err := p.Delete(ctx, "key_a"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "DELETE /organization/projects/proj_1/service_accounts/sa_a") {
		t.Fatalf("sole service account not deleted: %v", f.calls)
	}

	f.calls = nil
	if err := p.Delete(ctx, "key_b"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(f.calls, "\n"), "service_accounts") {
		t.Fatalf("shared service account deleted: %v", f.calls)
	}

	f.calls = nil
	if err := p.Delete(ctx, "key_gone"); err != nil || len(f.calls) != 1 {
		t.Fatalf("missing key: err %v calls %v", err, f.calls)
	}
}

func TestOpenAINoProject(t *testing.T) {
	p := newOpenAI(Config{Key: "k"})
	if _, err := p.List(context.Background()); !errors.Is(err, openai.ErrNoProject) {
		t.Fatalf("got %v", err)
	}
	if p.Disable(context.Background(), "x") == nil || p.SetLimit(context.Background(), "x", nil, "") == nil {
		t.Fatal("disable and set limit must fail")
	}
}
