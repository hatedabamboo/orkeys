// Package fal is a client for the fal.ai Platform API: API keys and usage.
package fal

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/httpx"
	"github.com/hatedabamboo/orkeys/internal/spend"
)

// DefaultBaseURL is the fal.ai Platform API base URL.
const DefaultBaseURL = "https://api.fal.ai/v1"

// MaxExpiry is the longest key expiry. The fal.ai dashboard allows 1 hour to 1 year. The API docs give no limit.
const MaxExpiry = 365 * 24 * time.Hour

// DefaultPreset is the preset of a new key without scopes: model requests, files and workflows.
const DefaultPreset = "API"

// Presets are the permission presets of a key. A key has one preset or a list of permissions.
var Presets = []string{"FULL", "AGENT", "API", "DEPLOY", "READONLY", "BILLING", "COMPUTE", "SERVERLESS"}

// Key is an API key. The list never returns its secret.
type Key struct {
	ID          string   `json:"key_id"`
	Alias       string   `json:"alias"`
	Description string   `json:"description,omitempty"`
	CreatedAt   string   `json:"created_at"`
	ExpiresAt   *string  `json:"expires_at"`
	Mode        string   `json:"mode"`
	Preset      string   `json:"preset,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	// Scope is set only for legacy keys: API or ADMIN.
	Scope string `json:"scope,omitempty"`
}

// Created returns the creation time, or zero when it cannot be parsed.
func (k Key) Created() time.Time {
	t, _ := ParseTime(k.CreatedAt)
	return t
}

// Expires returns the expiry, or nil for a key that does not expire.
func (k Key) Expires() *time.Time {
	if k.ExpiresAt == nil || *k.ExpiresAt == "" {
		return nil
	}
	t, err := ParseTime(*k.ExpiresAt)
	if err != nil {
		return nil
	}
	return &t
}

// Access returns the preset, the legacy scope, or "custom" for a key with a permission list.
func (k Key) Access() string {
	switch {
	case k.Preset != "":
		return k.Preset
	case k.Mode == "custom":
		return "custom"
	case k.Scope != "":
		return "legacy:" + k.Scope
	}
	return k.Mode
}

// ParseTime parses an ISO 8601 time. A time without a zone is UTC.
func ParseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("time %q: not ISO 8601", s)
}

// CreateRequest is the body of a key create request. Preset and Permissions are mutually exclusive.
type CreateRequest struct {
	Alias       string   `json:"alias"`
	Description string   `json:"description,omitempty"`
	Preset      string   `json:"preset,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
}

// CreatedKey is a new key. Key is the value for the Authorization header: key_id:key_secret.
type CreatedKey struct {
	ID     string `json:"key_id"`
	Secret string `json:"key_secret"`
	Key    string `json:"key"`
}

// KeyID returns the key ID of a key value (key_id:key_secret), or "" for a value without an ID.
func KeyID(key string) string {
	id, _, ok := strings.Cut(strings.TrimSpace(key), ":")
	if !ok {
		return ""
	}
	return id
}

// Client calls the Platform API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New returns a client. token is a key with the FULL preset. Create needs auth:keys:create, revoke needs
// auth:keys:write, and usage needs billing:usage:read, auth:keys:read and account:users:read.
// The fal.ai docs list auth:keys:write for create. The API checks auth:keys:create, and the dashboard does not
// show it for custom keys.
func New(base, token string, timeout time.Duration) *Client {
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: timeout}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	return httpx.Do(ctx, c.http, httpx.Request{
		Method: method,
		URL:    c.base + path,
		Header: http.Header{"Authorization": {"Key " + c.token}},
		In:     in,
		Out:    out,
	})
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool { return httpx.StatusOf(err) == http.StatusNotFound }

type keysPage struct {
	Keys       []Key   `json:"keys"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

// ListKeys returns all keys of the workspace. Revoked keys are not in the list.
func (c *Client) ListKeys(ctx context.Context) ([]Key, error) {
	var out []Key
	cursor := ""
	for {
		q := url.Values{"limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var pg keysPage
		if err := c.do(ctx, http.MethodGet, "/keys?"+q.Encode(), nil, &pg); err != nil {
			return nil, err
		}
		out = append(out, pg.Keys...)
		if !pg.HasMore || pg.NextCursor == nil || *pg.NextCursor == "" || *pg.NextCursor == cursor {
			return out, nil
		}
		cursor = *pg.NextCursor
	}
}

// CreateKey creates a key. fal.ai returns the secret only in this response.
func (c *Client) CreateKey(ctx context.Context, r CreateRequest) (*CreatedKey, error) {
	var out CreatedKey
	if err := c.do(ctx, http.MethodPost, "/keys", r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeKey revokes a key. A revoke cannot be undone. fal.ai returns 204 for a key that does not exist.
func (c *Client) RevokeKey(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/keys/"+url.PathEscape(id)+"/revoke", nil, nil)
}

type lineItem struct {
	CostTotal            float64 `json:"cost_total"`
	Currency             string  `json:"currency"`
	AuthMethodStructured *struct {
		APIKeyID string `json:"api_key_id"`
	} `json:"auth_method_structured"`
}

type usagePage struct {
	TimeSeries []struct {
		Bucket  string     `json:"bucket"`
		Results []lineItem `json:"results"`
	} `json:"time_series"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

// Usage returns the cost per key and per UTC day, from start to now. The costs are estimates. Usage of a login
// (not of a key) is not in the result.
func (c *Client) Usage(ctx context.Context, start time.Time) ([]spend.DayCost, error) {
	var out []spend.DayCost
	cursor := ""
	for {
		q := url.Values{
			"start":     {start.UTC().Format(time.RFC3339)},
			"timeframe": {"day"},
			"timezone":  {"UTC"},
			"expand":    {"time_series", "auth_method_structured"},
			"limit":     {"100"},
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var pg usagePage
		if err := c.do(ctx, http.MethodGet, "/models/usage?"+q.Encode(), nil, &pg); err != nil {
			return nil, err
		}
		for _, b := range pg.TimeSeries {
			day, err := ParseTime(b.Bucket)
			if err != nil {
				return nil, fmt.Errorf("usage bucket: %w", err)
			}
			for _, r := range b.Results {
				if r.AuthMethodStructured == nil || r.AuthMethodStructured.APIKeyID == "" || !strings.EqualFold(r.Currency, "usd") {
					continue
				}
				out = append(out, spend.DayCost{Day: day, KeyID: r.AuthMethodStructured.APIKeyID, USD: r.CostTotal})
			}
		}
		if !pg.HasMore || pg.NextCursor == nil || *pg.NextCursor == "" || *pg.NextCursor == cursor {
			return out, nil
		}
		cursor = *pg.NextCursor
	}
}
