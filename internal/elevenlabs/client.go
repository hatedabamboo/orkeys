// Package elevenlabs is a client for the ElevenLabs API keys of one service account.
package elevenlabs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/httpx"
)

// DefaultBaseURL is the ElevenLabs API base URL. Data residency workspaces use api.eu.residency.elevenlabs.io and
// similar hosts.
const DefaultBaseURL = "https://api.elevenlabs.io"

// MinExpiry and MaxExpiry are the limits of expires_at_unix, counted from the time of the request.
const (
	MinExpiry = 15 * time.Minute
	MaxExpiry = 90 * 24 * time.Hour
)

// DefaultPermissions are the permissions of a new key without scopes: speech, transcription and read access to
// voices and models. The API requires a permission list.
var DefaultPermissions = []string{"text_to_speech", "speech_to_speech", "speech_to_text", "voices_read", "models_read"}

// CheckExpiry returns an error when t is not MinExpiry to MaxExpiry after now.
func CheckExpiry(t, now time.Time) error {
	if d := t.Sub(now); d < MinExpiry || d > MaxExpiry {
		return fmt.Errorf("expiry %s: elevenlabs allows 15m to 90d from now", t.UTC().Format(time.RFC3339))
	}
	return nil
}

// ServiceAccount is a service account of the workspace.
type ServiceAccount struct {
	ID            string `json:"service_account_user_id"`
	Name          string `json:"name"`
	CreatedAtUnix *int64 `json:"created_at_unix"`
	Keys          []Key  `json:"api-keys"`
}

// Key is an API key of a service account. The list never returns its secret.
type Key struct {
	ID               string `json:"key_id"`
	Name             string `json:"name"`
	Hint             string `json:"hint"`
	ServiceAccountID string `json:"service_account_user_id"`
	// HashedKey is the SHA-256 of the key value, in hex. KeyHash of a value finds its key.
	HashedKey     string   `json:"hashed_xi_api_key"`
	CreatedAtUnix *int64   `json:"created_at_unix"`
	Disabled      bool     `json:"is_disabled"`
	DisableReason *string  `json:"disable_reason"`
	Permissions   []string `json:"permissions"`
	// CharacterLimit and CharacterCount are credits in the current billing period of the workspace.
	CharacterLimit *int64   `json:"character_limit"`
	CharacterCount *int64   `json:"character_count"`
	AllowedIPs     []string `json:"allowed_ips"`
	ExpiresAtUnix  *int64   `json:"expires_at_unix"`
}

// Created returns the creation time, or zero when the API does not report it.
func (k Key) Created() time.Time {
	if k.CreatedAtUnix == nil || *k.CreatedAtUnix == 0 {
		return time.Time{}
	}
	return time.Unix(*k.CreatedAtUnix, 0).UTC()
}

// Expires returns the expiry, or nil for a key that does not expire.
func (k Key) Expires() *time.Time {
	if k.ExpiresAtUnix == nil || *k.ExpiresAtUnix == 0 {
		return nil
	}
	t := time.Unix(*k.ExpiresAtUnix, 0).UTC()
	return &t
}

// KeyHash returns the hashed_xi_api_key of a key value. The API docs do not describe the hash. A list compared with
// a known key shows that it is the SHA-256 of the value, in lower-case hex.
func KeyHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

// ReasonExposed is the disable_reason of a key that ElevenLabs found in public.
const ReasonExposed = "exposed_publicly"

// CreateRequest is the body of a key create request. Permissions is required by the API.
type CreateRequest struct {
	Name           string   `json:"name"`
	Permissions    []string `json:"permissions"`
	CharacterLimit *int64   `json:"character_limit,omitempty"`
	ExpiresAtUnix  *int64   `json:"expires_at_unix,omitempty"`
}

// CreatedKey is a new key. Value is the secret for the xi-api-key header.
type CreatedKey struct {
	Value string `json:"xi-api-key"`
	ID    string `json:"key_id"`
}

// Clear is the update value that removes a limit or an expiry. An absent field keeps its value.
const Clear = "clear"

// Update is the body of a key update. Only the set fields change.
type Update map[string]any

// Client calls the API keys endpoints of one service account.
type Client struct {
	base  string
	token string
	sa    string
	http  *http.Client
}

// New returns a client for the keys of service account sa (service_account_user_id).
func New(base, token, sa string, timeout time.Duration) *Client {
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, sa: sa, http: &http.Client{Timeout: timeout}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	return httpx.Do(ctx, c.http, httpx.Request{
		Method: method,
		URL:    c.base + path,
		Header: http.Header{"xi-api-key": {c.token}},
		In:     in,
		Out:    out,
	})
}

// ListServiceAccounts returns the service accounts of the workspace with their keys. It does not need a service
// account in the client.
func (c *Client) ListServiceAccounts(ctx context.Context) ([]ServiceAccount, error) {
	var out struct {
		Accounts []ServiceAccount `json:"service-accounts"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/service-accounts", nil, &out); err != nil {
		return nil, err
	}
	return out.Accounts, nil
}

func (c *Client) keysPath() string {
	return "/v1/service-accounts/" + url.PathEscape(c.sa) + "/api-keys"
}

func (c *Client) keyPath(id string) string { return c.keysPath() + "/" + url.PathEscape(id) }

// ListKeys returns all keys of the service account. The endpoint has no pagination.
func (c *Client) ListKeys(ctx context.Context) ([]Key, error) {
	var out struct {
		Keys []Key `json:"api-keys"`
	}
	if err := c.do(ctx, http.MethodGet, c.keysPath(), nil, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// CreateKey creates a key. ElevenLabs returns the secret only in this response.
func (c *Client) CreateKey(ctx context.Context, r CreateRequest) (*CreatedKey, error) {
	var out CreatedKey
	if err := c.do(ctx, http.MethodPost, c.keysPath(), r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateKey changes the fields in u. The API documents no response body.
func (c *Client) UpdateKey(ctx context.Context, id string, u Update) error {
	return c.do(ctx, http.MethodPatch, c.keyPath(id), u, nil)
}

// DisableKey disables a key and keeps its other settings. It works also after the workspace leaves a plan with
// service accounts. An enable then needs that plan again.
func (c *Client) DisableKey(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, c.keyPath(id)+"/disable", nil, nil)
}

// DeleteKey deletes a key. A key that does not exist gives 404.
func (c *Client) DeleteKey(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, c.keyPath(id), nil, nil)
}
