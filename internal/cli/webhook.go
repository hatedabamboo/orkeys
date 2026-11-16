package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/hatedabamboo/orkeys/internal/engine"
)

// webhookAttempts and webhookTimeout bound the time that one event can hold up the run.
const (
	webhookAttempts = 3
	webhookTimeout  = 10 * time.Second
)

// webhook posts each event as JSON. Errors do not show the URL. Webhook URLs often hold a token.
type webhook struct {
	url    string
	client *http.Client
	delay  time.Duration
}

func (h *webhook) post(ev engine.Event) error {
	if h.client == nil {
		h.client = &http.Client{Timeout: webhookTimeout}
	}
	delay := h.delay
	if delay == 0 {
		delay = time.Second
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var last error
	for i := 0; i < webhookAttempts; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * delay)
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("bad webhook URL")
		}
		req.Header.Set("Content-Type", "application/json")
		start := time.Now()
		resp, err := h.client.Do(req)
		if err != nil {
			slog.Debug("webhook post failed", "event", ev.Type, "key", ev.Key, "attempt", i+1, "duration", time.Since(start).Round(time.Millisecond))
			last = fmt.Errorf("post event %s: request failed", ev.Type)
			continue
		}
		_ = resp.Body.Close()
		slog.Debug("webhook post", "event", ev.Type, "key", ev.Key, "attempt", i+1, "status", resp.StatusCode, "duration", time.Since(start).Round(time.Millisecond))
		if resp.StatusCode < 300 {
			return nil
		}
		last = fmt.Errorf("post event %s: HTTP %d", ev.Type, resp.StatusCode)
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			break
		}
	}
	return last
}
