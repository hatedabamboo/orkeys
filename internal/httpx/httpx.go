package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxRetries = 3

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

func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

type Request struct {
	Method string
	URL    string
	Header http.Header
	In     any
	Out    any
}

func Do(ctx context.Context, c *http.Client, r Request) error {
	var payload []byte
	if r.In != nil {
		var err error
		if payload, err = json.Marshal(r.In); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	attempts := 1
	if r.Method == http.MethodGet || r.Method == "LIST" {
		attempts = maxRetries
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
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
		resp, err := c.Do(req)
		if err != nil {
			return fmt.Errorf("%s %s: %w", r.Method, redact(r.URL), err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		if resp.StatusCode >= 400 {
			lastErr = &Error{Status: resp.StatusCode, Body: string(data)}
			if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && i < attempts-1 {
				if err := sleep(ctx, retryDelay(resp, i)); err != nil {
					return err
				}
				continue
			}
			return lastErr
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

func redact(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= 60 {
			return time.Duration(n) * time.Second
		}
	}
	return time.Duration(500*(1<<attempt)) * time.Millisecond
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
