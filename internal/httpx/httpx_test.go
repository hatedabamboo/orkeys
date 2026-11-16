package httpx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetries(t *testing.T) {
	t.Helper()
	b, r := baseDelay, rateLimitDelay
	baseDelay, rateLimitDelay = time.Millisecond, time.Millisecond
	t.Cleanup(func() { baseDelay, rateLimitDelay = b, r })
}

// failing returns a server that answers status to the first fail requests and 200 after them.
func failing(t *testing.T, fail int32, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= fail {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestRetries(t *testing.T) {
	fastRetries(t)
	for _, tc := range []struct {
		name   string
		method string
		status int
		fail   int32
		calls  int32
		ok     bool
	}{
		{"GET 429", http.MethodGet, http.StatusTooManyRequests, 2, 3, true},
		{"GET 408", http.MethodGet, http.StatusRequestTimeout, 1, 2, true},
		{"GET 503", http.MethodGet, http.StatusServiceUnavailable, 1, 2, true},
		{"LIST 502", "LIST", http.StatusBadGateway, 1, 2, true},
		{"GET 429 gives up", http.MethodGet, http.StatusTooManyRequests, 99, maxAttempts, false},
		{"GET 400 no retry", http.MethodGet, http.StatusBadRequest, 1, 1, false},
		{"POST 429", http.MethodPost, http.StatusTooManyRequests, 1, 2, true},
		{"DELETE 429", http.MethodDelete, http.StatusTooManyRequests, 1, 2, true},
		{"POST 408 no retry", http.MethodPost, http.StatusRequestTimeout, 1, 1, false},
		{"POST 500 no retry", http.MethodPost, http.StatusInternalServerError, 1, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, n := failing(t, tc.fail, tc.status)
			var out struct{ OK bool }
			err := Do(context.Background(), srv.Client(), Request{Method: tc.method, URL: srv.URL, In: map[string]int{"a": 1}, Out: &out})
			if n.Load() != tc.calls {
				t.Errorf("calls = %d, want %d", n.Load(), tc.calls)
			}
			if tc.ok && (err != nil || !out.OK) {
				t.Errorf("err = %v, ok = %v", err, out.OK)
			}
			if !tc.ok && StatusOf(err) != tc.status {
				t.Errorf("err = %v, want HTTP %d", err, tc.status)
			}
		})
	}
}

func TestRetriesNetworkErrorOnlyForReads(t *testing.T) {
	fastRetries(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if err := Do(context.Background(), srv.Client(), Request{Method: http.MethodGet, URL: srv.URL}); err != nil || n.Load() != 2 {
		t.Fatalf("GET: err=%v calls=%d", err, n.Load())
	}
	n.Store(0)
	err := Do(context.Background(), srv.Client(), Request{Method: http.MethodPost, URL: srv.URL + "?token=secret", In: map[string]int{}})
	if err == nil || n.Load() != 1 {
		t.Fatalf("POST must not retry: err=%v calls=%d", err, n.Load())
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error has the query: %v", err)
	}
}

func TestRetryStopsOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := Do(ctx, srv.Client(), Request{Method: http.MethodGet, URL: srv.URL})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestRetryDelay(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	type resp struct {
		status int
		h      http.Header
	}
	r := func(status int, kv ...string) resp {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return resp{status, h}
	}
	delay := func(x resp, attempt int) time.Duration { return retryDelay(x.status, x.h, attempt, now) }
	for _, tc := range []struct {
		name string
		resp resp
		want time.Duration
	}{
		{"seconds", r(429, "Retry-After", "7"), 7 * time.Second},
		{"zero", r(429, "Retry-After", "0"), 0},
		{"too long", r(429, "Retry-After", "3600"), maxDelay},
		{"date", r(503, "Retry-After", now.Add(12*time.Second).Format(http.TimeFormat)), 12 * time.Second},
		{"past date", r(503, "Retry-After", now.Add(-time.Minute).Format(http.TimeFormat)), 0},
		{"openai reset", r(429, "x-ratelimit-reset-requests", "1m30s"), maxDelay},
		{"openai reset short", r(429, "x-ratelimit-reset-requests", "250ms"), 250 * time.Millisecond},
		{"openrouter reset", r(429, "X-RateLimit-Reset", strconv.FormatInt(now.Add(3*time.Second).UnixMilli(), 10)), 3 * time.Second},
		{"retry-after first", r(429, "Retry-After", "2", "x-ratelimit-reset-requests", "9s"), 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := delay(tc.resp, 0); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}

	in := func(d, lo, hi time.Duration) bool { return d >= lo && d <= hi }
	if d := delay(r(429, "Retry-After", "soon"), 1); !in(d, 2*rateLimitDelay, 2*rateLimitDelay*6/5) {
		t.Errorf("429 backoff attempt 1: %s", d)
	}
	if d := delay(r(503), 2); !in(d, 4*baseDelay, 4*baseDelay*6/5) {
		t.Errorf("503 backoff attempt 2: %s", d)
	}
	if d := delay(r(429), 10); d != maxDelay {
		t.Errorf("backoff cap: %s", d)
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
