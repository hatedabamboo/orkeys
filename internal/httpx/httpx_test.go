package httpx

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRetries(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var out struct{ OK bool }
	if err := Do(context.Background(), srv.Client(), Request{Method: http.MethodGet, URL: srv.URL, Out: &out}); err != nil || !out.OK || n.Load() != 3 {
		t.Fatalf("GET: err=%v ok=%v calls=%d", err, out.OK, n.Load())
	}

	n.Store(0)
	err := Do(context.Background(), srv.Client(), Request{Method: http.MethodPost, URL: srv.URL, In: map[string]int{"a": 1}})
	if StatusOf(err) != http.StatusTooManyRequests || n.Load() != 1 {
		t.Fatalf("POST must not retry: err=%v calls=%d", err, n.Load())
	}
}

func TestErrorBodyAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"bad"}`))
	}))
	defer srv.Close()
	err := Do(context.Background(), srv.Client(), Request{Method: http.MethodPost, URL: srv.URL + "?token=secret", In: map[string]int{}})
	if StatusOf(err) != http.StatusBadRequest || err.Error() != `HTTP 400: {"detail":"bad"}` {
		t.Fatalf("got %v", err)
	}
	if StatusOf(nil) != 0 {
		t.Fatal("StatusOf(nil)")
	}
}

func TestDebugLogHidesQueryAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"key":"sk-response-value"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	req := Request{Method: http.MethodPost, URL: srv.URL + "/keys?token=query-value", In: map[string]string{"key": "sk-request-value"}}
	if err := Do(context.Background(), srv.Client(), req); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "status=200") || !strings.Contains(got, "/keys") {
		t.Fatalf("no request record: %q", got)
	}
	for _, s := range []string{"query-value", "sk-request-value", "sk-response-value"} {
		if strings.Contains(got, s) {
			t.Errorf("debug log has %q: %q", s, got)
		}
	}
}
