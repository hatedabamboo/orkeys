package deepinfra

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/httpx"
)

const DefaultBaseURL = "https://api.deepinfra.com"

// DeepInfra allows token management and limit changes only with a dashboard login, not with API keys.
// The write calls below return 401 for every API key; they are kept for credentials that may get access.
const keyLimitsWriteMethod = http.MethodPost

type Token struct {
	TokenID    string   `json:"token_id"`
	Name       string   `json:"name"`
	CreatedAt  int64    `json:"created_at"`
	Prefix     string   `json:"prefix"`
	Fragment   string   `json:"fragment"`
	Scopes     []string `json:"scopes"`
	AllowedIPs []string `json:"allowed_ips"`
}

func (t Token) Created() time.Time { return time.Unix(t.CreatedAt, 0).UTC() }

type CreatedToken struct {
	Token
	Secret string `json:"token"`
}

type KeyLimit struct {
	TokenID string   `json:"token_id"`
	Name    string   `json:"name"`
	Limit   *float64 `json:"limit"`
	Spend   *float64 `json:"spend"`
}

type Client struct {
	base  string
	token string
	http  *http.Client
}

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
		Header: http.Header{"Authorization": {"Bearer " + c.token}},
		In:     in,
		Out:    out,
	})
}

// IsForbidden reports a 401 or 403: the key may read limits and spend but not manage tokens.
func IsForbidden(err error) bool {
	s := httpx.StatusOf(err)
	return s == http.StatusUnauthorized || s == http.StatusForbidden
}

// TokensFromLimits builds a token list from key limits, for keys that cannot list tokens.
// Scopes and creation times are unknown.
func TokensFromLimits(limits []KeyLimit) []Token {
	out := make([]Token, 0, len(limits))
	for _, l := range limits {
		out = append(out, Token{TokenID: l.TokenID, Name: l.Name})
	}
	return out
}

func (c *Client) ListTokens(ctx context.Context) ([]Token, error) {
	var out []Token
	return out, c.do(ctx, http.MethodGet, "/v1/api-tokens", nil, &out)
}

func (c *Client) CreateToken(ctx context.Context, name string, scopes []string) (*CreatedToken, error) {
	in := map[string]any{"name": name, "scopes": scopes}
	var out CreatedToken
	if err := c.do(ctx, http.MethodPost, "/v1/api-tokens", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteToken(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/api-tokens/id/"+url.PathEscape(id), nil, nil)
}

func (c *Client) KeyLimits(ctx context.Context) ([]KeyLimit, error) {
	var out []KeyLimit
	return out, c.do(ctx, http.MethodGet, "/payment/key-limits", nil, &out)
}

func (c *Client) SetKeyLimit(ctx context.Context, id string, limit *float64) error {
	in := map[string]any{"token_id": id, "limit": limit}
	return c.do(ctx, keyLimitsWriteMethod, "/payment/key-limits", in, nil)
}

func (c *Client) UpdateScopes(ctx context.Context, id string, scopes []string) (*Token, error) {
	var out Token
	if err := c.do(ctx, http.MethodPatch, "/v1/api-tokens/id/"+url.PathEscape(id), map[string]any{"scopes": scopes}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Scopes(ctx context.Context) ([]Scope, error) {
	var out []Scope
	return out, c.do(ctx, http.MethodGet, "/v1/api-tokens/scopes", nil, &out)
}

type Scope struct {
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
}
