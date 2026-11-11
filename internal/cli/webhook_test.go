package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/engine"
)

func TestEventSinks(t *testing.T) {
	var calls atomic.Int32
	var got engine.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first attempt fails, the second works.
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
	}))
	defer srv.Close()

	var file, out strings.Builder
	p := &eventPrinter{out: &out, file: &file, hook: &webhook{url: srv.URL + "/hook?token=secret", delay: time.Millisecond}}
	p.Emit(engine.Event{Type: engine.EventKeyIssued, Key: "prod-a-b", Message: "m"})

	var line engine.Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(file.String())), &line); err != nil || line.Key != "prod-a-b" {
		t.Fatalf("events file: %q %v", file.String(), err)
	}
	if calls.Load() != 2 || got.Type != engine.EventKeyIssued {
		t.Fatalf("webhook: %d calls, got %+v", calls.Load(), got)
	}
	if !strings.Contains(out.String(), "key_issued prod-a-b") {
		t.Fatalf("stdout: %q", out.String())
	}

	bad := &webhook{url: srv.URL, delay: time.Millisecond}
	srv.Close()
	err := bad.post(engine.Event{Type: engine.EventKeyIssued})
	if err == nil || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("webhook error must not show the URL: %v", err)
	}
}
