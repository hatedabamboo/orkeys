// Package openai is a client for the OpenAI Admin API: project API keys, service accounts and costs.
package openai

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/httpx"
)

// DefaultBaseURL is the OpenAI API base URL.
const DefaultBaseURL = "https://api.openai.com/v1"

// MaxExpiry is the longest key expiry that the Admin API accepts (expires_in_seconds).
const MaxExpiry = 365 * 24 * time.Hour

// ErrNoProject is returned when no project ID is set. All key endpoints are per project.
var ErrNoProject = errors.New("project ID is not set")

// Owner is the user or service account that owns a project API key.
type Owner struct {
	Type           string   `json:"type"`
	User           *Account `json:"user,omitempty"`
	ServiceAccount *Account `json:"service_account,omitempty"`
}

// Account is a user or a service account.
type Account struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	Role      string `json:"role,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// Key is a project API key. The Admin API never returns its secret after creation.
type Key struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	RedactedValue      string   `json:"redacted_value"`
	CreatedAt          int64    `json:"created_at"`
	LastUsedAt         *int64   `json:"last_used_at"`
	ExpiresAt          *int64   `json:"expires_at"`
	Owner              Owner    `json:"owner"`
	OwnerProjectAccess string   `json:"owner_project_access"`
	Scopes             []string `json:"scopes,omitempty"`
}

// ServiceAccountID returns the ID of the owning service account, or "" for a user key.
func (k Key) ServiceAccountID() string {
	if k.Owner.Type == "service_account" && k.Owner.ServiceAccount != nil {
		return k.Owner.ServiceAccount.ID
	}
	return ""
}

// CreatedKey is a new service account key with its secret.
type CreatedKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Value     string `json:"value"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt *int64 `json:"expires_at"`
}

// ServiceAccount is a project service account. APIKey is set only in the create response.
type ServiceAccount struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Role      string      `json:"role"`
	CreatedAt int64       `json:"created_at"`
	APIKey    *CreatedKey `json:"api_key,omitempty"`
}

// KeySpec is the body of a service account key create request.
type KeySpec struct {
	Name             string   `json:"name,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	ExpiresInSeconds *int64   `json:"expires_in_seconds,omitempty"`
}

// Client calls the Admin API for one project.
type Client struct {
	base    string
	token   string
	project string
	http    *http.Client
}

// New returns a client. token is an Admin API key; project is a project ID (proj_...).
func New(base, token, project string, timeout time.Duration) *Client {
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, project: project, http: &http.Client{Timeout: timeout}}
}

// Project returns the project ID of the client.
func (c *Client) Project() string { return c.project }

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	return httpx.Do(ctx, c.http, httpx.Request{
		Method: method,
		URL:    c.base + path,
		Header: http.Header{"Authorization": {"Bearer " + c.token}},
		In:     in,
		Out:    out,
	})
}

func (c *Client) projectPath(parts ...string) (string, error) {
	if c.project == "" {
		return "", ErrNoProject
	}
	p := "/organization/projects/" + url.PathEscape(c.project)
	for _, s := range parts {
		p += "/" + url.PathEscape(s)
	}
	return p, nil
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool { return httpx.StatusOf(err) == http.StatusNotFound }

type page[T any] struct {
	Data    []T    `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// ListKeys returns all API keys of the project, of users and service accounts, active and inactive.
func (c *Client) ListKeys(ctx context.Context) ([]Key, error) {
	p, err := c.projectPath("api_keys")
	if err != nil {
		return nil, err
	}
	var out []Key
	after := ""
	for {
		q := url.Values{"limit": {"100"}, "owner_project_access": {"any"}}
		if after != "" {
			q.Set("after", after)
		}
		var pg page[Key]
		if err := c.do(ctx, http.MethodGet, p+"?"+q.Encode(), nil, &pg); err != nil {
			return nil, err
		}
		out = append(out, pg.Data...)
		if !pg.HasMore || pg.LastID == "" || pg.LastID == after {
			return out, nil
		}
		after = pg.LastID
	}
}

// GetKey returns one project API key.
func (c *Client) GetKey(ctx context.Context, id string) (*Key, error) {
	p, err := c.projectPath("api_keys", id)
	if err != nil {
		return nil, err
	}
	var out Key
	if err := c.do(ctx, http.MethodGet, p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteKey deletes a project API key. The owning service account stays.
func (c *Client) DeleteKey(ctx context.Context, id string) error {
	p, err := c.projectPath("api_keys", id)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, p, nil, nil)
}

// UpdateKey changes the scopes or the expiry of a key. The API counts the expiry from the time of the request.
func (c *Client) UpdateKey(ctx context.Context, id string, scopes []string, expiresIn *int64) error {
	p, err := c.projectPath("api_keys", id)
	if err != nil {
		return err
	}
	in := map[string]any{}
	if scopes != nil {
		in["scopes"] = scopes
	}
	if expiresIn != nil {
		in["expires_in_seconds"] = *expiresIn
	}
	return c.do(ctx, http.MethodPost, p, in, nil)
}

// CreateServiceAccount creates a service account. OpenAI also creates a key for it and returns it in APIKey.
func (c *Client) CreateServiceAccount(ctx context.Context, name string) (*ServiceAccount, error) {
	p, err := c.projectPath("service_accounts")
	if err != nil {
		return nil, err
	}
	var out ServiceAccount
	if err := c.do(ctx, http.MethodPost, p, map[string]any{"name": name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateServiceAccountKey creates one more key for a service account.
func (c *Client) CreateServiceAccountKey(ctx context.Context, saID string, s KeySpec) (*CreatedKey, error) {
	p, err := c.projectPath("service_accounts", saID, "api_keys")
	if err != nil {
		return nil, err
	}
	var out CreatedKey
	if err := c.do(ctx, http.MethodPost, p, s, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteServiceAccount deletes a service account.
func (c *Client) DeleteServiceAccount(ctx context.Context, saID string) error {
	p, err := c.projectPath("service_accounts", saID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, p, nil, nil)
}

type costsPage struct {
	Data []struct {
		StartTime int64 `json:"start_time"`
		Results   []struct {
			APIKeyID string `json:"api_key_id"`
			Amount   struct {
				Value    float64 `json:"value"`
				Currency string  `json:"currency"`
			} `json:"amount"`
		} `json:"results"`
	} `json:"data"`
	HasMore  bool    `json:"has_more"`
	NextPage *string `json:"next_page"`
}

// DayCost is the cost of one key on one UTC day.
type DayCost struct {
	Day      time.Time
	APIKeyID string
	USD      float64
}

// Costs returns the cost per key and per UTC day of the project, from start to now.
// The costs endpoint has only 1-day buckets. OpenAI fills them with a delay, so today is not complete.
func (c *Client) Costs(ctx context.Context, start time.Time) ([]DayCost, error) {
	if c.project == "" {
		return nil, ErrNoProject
	}
	var out []DayCost
	next := ""
	for {
		q := url.Values{
			"start_time":   {strconv.FormatInt(start.Unix(), 10)},
			"bucket_width": {"1d"},
			"group_by":     {"api_key_id"},
			"project_ids":  {c.project},
			"limit":        {"31"},
		}
		if next != "" {
			q.Set("page", next)
		}
		var pg costsPage
		if err := c.do(ctx, http.MethodGet, "/organization/costs?"+q.Encode(), nil, &pg); err != nil {
			return nil, err
		}
		for _, b := range pg.Data {
			for _, r := range b.Results {
				if r.APIKeyID == "" || !strings.EqualFold(r.Amount.Currency, "usd") {
					continue
				}
				out = append(out, DayCost{Day: time.Unix(b.StartTime, 0).UTC(), APIKeyID: r.APIKeyID, USD: r.Amount.Value})
			}
		}
		if !pg.HasMore || pg.NextPage == nil || *pg.NextPage == "" || *pg.NextPage == next {
			return out, nil
		}
		next = *pg.NextPage
	}
}

// Spend is the cost of one key in the current UTC day, week (from Monday) and month.
type Spend struct {
	Day   float64 `json:"day"`
	Week  float64 `json:"week"`
	Month float64 `json:"month"`
}

// PeriodStarts returns the start of the UTC day, week (Monday) and month of now.
func PeriodStarts(now time.Time) (day, week, month time.Time) {
	now = now.UTC()
	day = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	week = day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	month = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return day, week, month
}

// SpendStart returns the first day that SpendByKey needs: the start of the week or of the month.
func SpendStart(now time.Time) time.Time {
	_, week, month := PeriodStarts(now)
	if week.Before(month) {
		return week
	}
	return month
}

// SpendByKey sums day costs into the spend of each key in the periods of now.
func SpendByKey(costs []DayCost, now time.Time) map[string]Spend {
	day, week, month := PeriodStarts(now)
	out := map[string]Spend{}
	for _, c := range costs {
		s := out[c.APIKeyID]
		if !c.Day.Before(day) {
			s.Day += c.USD
		}
		if !c.Day.Before(week) {
			s.Week += c.USD
		}
		if !c.Day.Before(month) {
			s.Month += c.USD
		}
		out[c.APIKeyID] = s
	}
	return out
}
