// Package httpx sends JSON requests over HTTP and retries the ones that are safe to send again.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxAttempts is the number of tries of one request, the first try included.
const maxAttempts = 5

// maxDelay is the longest wait before a retry. A longer Retry-After is cut to this value.
const maxDelay = 60 * time.Second

// Backoff without a server hint: the base doubles at each retry, plus jitter. 429 has a larger base because
// provider rate limits count requests per minute.
var (
	baseDelay      = time.Second
	rateLimitDelay = 5 * time.Second
)

// Error is a non-2xx response.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Body)
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, msg)
}

// StatusOf returns the HTTP status of an *Error in err, or 0.
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// Request is one JSON call.
type Request struct {
	Method string
	URL    string
	Header http.Header
	In     any
	Out    any
	// ParseError turns an error response into an error. Nil: *Error.
	ParseError func(status int, body []byte) error
}

// Do sends r and decodes the response into r.Out.
//
// Reads (GET, LIST) retry on network errors, HTTP 408, 429 and 5xx. Writes retry only on HTTP 429: the server
// rejects a rate-limited request before it does the work. Writes do not retry on other errors. They are not
// idempotent from our side, and a timeout or a 5xx can come after the server did the work.
func Do(ctx context.Context, c *http.Client, r Request) error {
	var payload []byte
	if r.In != nil {
		var err error
		if payload, err = json.Marshal(r.In); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	read := r.Method == http.MethodGet || r.Method == "LIST"
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
		if err != nil {
			return err
		}
		for k, v := range r.Header {
			req.Header[k] = v
		}
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		start := time.Now()
		resp, err := c.Do(req)
		if err != nil {
			// A *url.Error holds the full URL with the query. Only its inner error is logged.
			cause := err
			var ue *url.Error
			if errors.As(err, &ue) {
				cause = ue.Err
			}
			slog.DebugContext(ctx, "http request failed", "method", r.Method, "url", redact(r.URL), "attempt", i+1,
				"duration", time.Since(start).Round(time.Millisecond), "error", cause)
			lastErr = fmt.Errorf("%s %s: %w", r.Method, redact(r.URL), cause)
			if !read || ctx.Err() != nil || i == maxAttempts-1 {
				return lastErr
			}
			if err := wait(ctx, r, i, backoff(baseDelay, i), "error", cause); err != nil {
				return err
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		// Bodies are not logged. They can hold key values.
		slog.DebugContext(ctx, "http request", "method", r.Method, "url", redact(r.URL), "status", resp.StatusCode,
			"attempt", i+1, "duration", time.Since(start).Round(time.Millisecond), "bytes", len(data))
		if resp.StatusCode >= 400 {
			lastErr = &Error{Status: resp.StatusCode, Body: string(data)}
			if r.ParseError != nil {
				lastErr = r.ParseError(resp.StatusCode, data)
			}
			if !retryable(resp.StatusCode, read) || i == maxAttempts-1 {
				return lastErr
			}
			if err := wait(ctx, r, i, retryDelay(resp.StatusCode, resp.Header, i, time.Now()), "status", resp.StatusCode); err != nil {
				return err
			}
			continue
		}
		if r.Out != nil && len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, r.Out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
		}
		return nil
	}
	return lastErr
}

func retryable(status int, read bool) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	// OpenRouter answers 408 when its backend does not reply in 10 seconds.
	return read && (status == http.StatusRequestTimeout || status >= 500)
}

// wait logs the retry at warn level, so that a long wait is visible without --debug, and sleeps for d.
func wait(ctx context.Context, r Request, attempt int, d time.Duration, reason string, value any) error {
	slog.WarnContext(ctx, "http retry", "method", r.Method, "url", redact(r.URL), reason, value,
		"attempt", attempt+1, "of", maxAttempts, "delay", d.Round(time.Millisecond))
	return sleep(ctx, d)
}

func redact(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

// retryDelay returns the wait before the next try. It uses the first server hint that it can read:
//   - Retry-After: seconds or an HTTP date (RFC 9110).
//   - x-ratelimit-reset-requests: a duration such as "1s" or "6m0s" (OpenAI).
//   - X-RateLimit-Reset: a Unix time in milliseconds (OpenRouter).
//
// Without a hint, it backs off exponentially.
func retryDelay(status int, h http.Header, attempt int, now time.Time) time.Duration {
	if s := strings.TrimSpace(h.Get("Retry-After")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return capDelay(time.Duration(n) * time.Second)
		}
		if t, err := http.ParseTime(s); err == nil {
			return capDelay(t.Sub(now))
		}
	}
	if s := strings.TrimSpace(h.Get("X-Ratelimit-Reset-Requests")); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d >= 0 {
			return capDelay(d)
		}
	}
	if s := strings.TrimSpace(h.Get("X-Ratelimit-Reset")); s != "" {
		if ms, err := strconv.ParseInt(s, 10, 64); err == nil && ms > 0 {
			return capDelay(time.UnixMilli(ms).Sub(now))
		}
	}
	base := baseDelay
	if status == http.StatusTooManyRequests {
		base = rateLimitDelay
	}
	return backoff(base, attempt)
}

// backoff returns base * 2^attempt, plus up to 20% jitter, so that parallel runs do not retry at the same time.
func backoff(base time.Duration, attempt int) time.Duration {
	d := base << attempt
	if d <= 0 || d > maxDelay {
		d = maxDelay
	}
	if j := int64(d) / 5; j > 0 {
		d += time.Duration(rand.Int64N(j))
	}
	return capDelay(d)
}

func capDelay(d time.Duration) time.Duration {
	return max(0, min(d, maxDelay))
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
