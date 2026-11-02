// Package api is a small client for the OpenRouter key Management API.
// Docs: https://openrouter.ai/docs/guides/overview/auth/management-api-keys
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public OpenRouter API base.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

const maxRetries = 3

// Client calls the Management API with a management key.
type Client struct {
	baseURL   string
	token     string
	userAgent string
	http      *http.Client
}

// New returns a Client. An empty baseURL uses DefaultBaseURL.
func New(baseURL, token string, timeout time.Duration, userAgent string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		token:     token,
		userAgent: userAgent,
		http:      &http.Client{Timeout: timeout},
	}
}

// Error is a non-2xx response from the API.
type Error struct {
	Status  int
	Code    string
	Message string
	Body    string
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = strings.TrimSpace(e.Body)
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, msg)
}

// IsNotFound reports whether err is an HTTP 404 from the API.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}

	// Only GET is retried. Writes are not idempotent from our side.
	attempts := 1
	if method == http.MethodGet {
		attempts = maxRetries
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		if c.userAgent != "" {
			req.Header.Set("User-Agent", c.userAgent)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("%s %s: %w", method, path, err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}

		if resp.StatusCode >= 400 {
			lastErr = parseError(resp.StatusCode, data)
			if retryable(resp.StatusCode) && i < attempts-1 {
				if err := sleep(ctx, retryDelay(resp, i)); err != nil {
					return err
				}
				continue
			}
			return lastErr
		}

		if out != nil && len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
		}
		return nil
	}
	return lastErr
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
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

func parseError(status int, data []byte) error {
	e := &Error{Status: status, Body: string(data)}
	var env struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &env) == nil {
		e.Message = env.Error.Message
		e.Code = strings.Trim(string(env.Error.Code), `"`)
	}
	return e
}

// List returns one page of keys.
func (c *Client) List(ctx context.Context, opts ListOptions) ([]Key, error) {
	q := url.Values{}
	if opts.Offset > 0 {
		q.Set("offset", strconv.Itoa(opts.Offset))
	}
	if opts.IncludeDisabled {
		q.Set("include_disabled", "true")
	}
	if opts.WorkspaceID != "" {
		q.Set("workspace_id", opts.WorkspaceID)
	}
	var out struct {
		Data []Key `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/keys", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// ListAll follows pagination from opts.Offset until an empty page.
func (c *Client) ListAll(ctx context.Context, opts ListOptions) ([]Key, error) {
	var all []Key
	seen := map[string]bool{}
	for {
		page, err := c.List(ctx, opts)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, k := range page {
			if !seen[k.Hash] {
				seen[k.Hash] = true
				all = append(all, k)
				added++
			}
		}
		// Stop on an empty page, or if the server repeats a page.
		if len(page) == 0 || added == 0 {
			return all, nil
		}
		opts.Offset += len(page)
	}
}

// Get returns one key by hash.
func (c *Client) Get(ctx context.Context, hash string) (*Key, error) {
	var out struct {
		Data Key `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/keys/"+url.PathEscape(hash), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Create makes a new key. The response holds the plaintext secret once.
func (c *Client) Create(ctx context.Context, req CreateRequest) (*CreateResponse, error) {
	var out CreateResponse
	if err := c.do(ctx, http.MethodPost, "/keys", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Update changes the set fields of a key.
func (c *Client) Update(ctx context.Context, hash string, req UpdateRequest) (*Key, error) {
	var out struct {
		Data Key `json:"data"`
	}
	if err := c.do(ctx, http.MethodPatch, "/keys/"+url.PathEscape(hash), nil, req, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Delete removes a key.
func (c *Client) Delete(ctx context.Context, hash string) error {
	return c.do(ctx, http.MethodDelete, "/keys/"+url.PathEscape(hash), nil, nil, nil)
}
