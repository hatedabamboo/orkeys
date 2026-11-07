package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpdateRequestJSON(t *testing.T) {
	name := "x"
	dis := false
	cases := []struct {
		req  UpdateRequest
		want string
	}{
		{UpdateRequest{}, `{}`},
		{UpdateRequest{Name: &name, Disabled: &dis}, `{"disabled":false,"name":"x"}`},
		{UpdateRequest{Limit: Null[float64](), LimitReset: Null[string]()}, `{"limit":null,"limit_reset":null}`},
		{UpdateRequest{Limit: Value(12.5), LimitReset: Value("daily")}, `{"limit":12.5,"limit_reset":"daily"}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.req)
		if err != nil || string(b) != c.want {
			t.Errorf("Marshal = %s, %v; want %s", b, err, c.want)
		}
	}
}

func TestListAllPaginates(t *testing.T) {
	const total = 230
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"bad key"}}`)
			return
		}
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var data []Key
		for i := off; i < total && i < off+100; i++ {
			data = append(data, Key{Hash: fmt.Sprintf("%064d", i)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	keys, err := New(srv.URL, "tok", 5*time.Second, "").ListAll(context.Background(), ListOptions{})
	if err != nil || len(keys) != total {
		t.Fatalf("ListAll = %d keys, %v; want %d", len(keys), err, total)
	}

	_, err = New(srv.URL, "wrong", 5*time.Second, "").ListAll(context.Background(), ListOptions{})
	if err == nil || err.Error() != "HTTP 401: bad key" {
		t.Fatalf("want 401 error, got %v", err)
	}
}

func TestListAllStopsOnRepeatedPage(t *testing.T) {
	// A server that ignores offset must not cause an endless loop.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"hash":"a"},{"hash":"b"}]}`)
	}))
	defer srv.Close()
	keys, err := New(srv.URL, "tok", time.Second, "").ListAll(context.Background(), ListOptions{})
	if err != nil || len(keys) != 2 {
		t.Fatalf("ListAll = %d, %v", len(keys), err)
	}
}

func TestGetRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"data":{"hash":"h","name":"n"}}`)
	}))
	defer srv.Close()
	k, err := New(srv.URL, "tok", time.Second, "").Get(context.Background(), "h")
	if err != nil || k.Name != "n" || calls.Load() != 2 {
		t.Fatalf("Get = %+v, %v, calls=%d", k, err, calls.Load())
	}
}
