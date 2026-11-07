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
)

type fakeDI struct {
	mu        sync.Mutex
	calls     []string
	failLimit bool
}

func (f *fakeDI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/api-tokens":
		_, _ = io.WriteString(w, `[{"token_id":"t1","name":"a","created_at":1700000000},{"token_id":"t2","name":"b","created_at":1700000000}]`)
	case r.Method == http.MethodGet && r.URL.Path == "/payment/key-limits":
		_, _ = io.WriteString(w, `[{"token_id":"t1","limit":50,"spend":12.5},{"token_id":"t2","limit":0,"spend":null}]`)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/api-tokens":
		_, _ = io.WriteString(w, `{"token_id":"t3","name":"new","created_at":1700000000,"token":"sk-di-secret"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/payment/key-limits":
		if f.failLimit {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestDeepInfraList(t *testing.T) {
	srv := httptest.NewServer(&fakeDI{})
	defer srv.Close()
	p, _ := New("deepinfra", Config{BaseURL: srv.URL, Key: "k"})
	keys, err := p.List(context.Background())
	if err != nil || len(keys) != 2 {
		t.Fatal(keys, err)
	}
	if *keys[0].SpendMonthUSD != 12.5 || *keys[0].LimitUSD != 50 || keys[0].Disabled {
		t.Fatalf("t1: %+v", keys[0])
	}
	if !keys[1].Disabled || keys[1].SpendMonthUSD != nil {
		t.Fatalf("t2 with limit 0 must be disabled: %+v", keys[1])
	}
}

func TestDeepInfraCreate(t *testing.T) {
	f := &fakeDI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	p, _ := New("deepinfra", Config{BaseURL: srv.URL, Key: "k"})
	limit := 25.0
	k, secret, err := p.Create(context.Background(), Spec{Name: "new", LimitUSD: &limit})
	if err != nil || secret != "sk-di-secret" || k.ID != "t3" {
		t.Fatal(k, secret, err)
	}
	var req map[string]any
	_ = json.Unmarshal([]byte(strings.SplitN(f.calls[0], " ", 3)[2]), &req)
	if req["scopes"].([]any)[0] != "inference" {
		t.Fatalf("default scope: %v", f.calls[0])
	}
	if !strings.Contains(f.calls[1], `"limit":25`) || !strings.Contains(f.calls[1], `"token_id":"t3"`) {
		t.Fatalf("limit call: %v", f.calls[1])
	}

	f.failLimit, f.calls = true, nil
	if _, _, err := p.Create(context.Background(), Spec{Name: "new", LimitUSD: &limit}); err == nil {
		t.Fatal("want error")
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasPrefix(last, "DELETE /v1/api-tokens/id/t3") {
		t.Fatalf("token without limit not deleted: %v", f.calls)
	}
}

func TestDeepInfraListWithoutTokenAccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/api-tokens" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `[{"token_id":"t1","name":"a","limit":10,"spend":2}]`)
	}))
	defer srv.Close()
	p, _ := New("deepinfra", Config{BaseURL: srv.URL, Key: "k"})
	keys, err := p.List(context.Background())
	if err != nil || len(keys) != 1 || keys[0].ID != "t1" || *keys[0].SpendMonthUSD != 2 || !keys[0].CreatedAt.IsZero() {
		t.Fatalf("fallback: %+v %v", keys, err)
	}
}
