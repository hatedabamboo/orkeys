package openrouter

import (
	"encoding/json"
	"time"
)

// Key is an OpenRouter API key as returned by the Management API.
type Key struct {
	Hash               string   `json:"hash"`
	Name               string   `json:"name"`
	Label              string   `json:"label"`
	Disabled           bool     `json:"disabled"`
	Limit              *float64 `json:"limit"`
	LimitRemaining     *float64 `json:"limit_remaining"`
	LimitReset         *string  `json:"limit_reset"`
	IncludeBYOKInLimit bool     `json:"include_byok_in_limit"`

	Usage        float64 `json:"usage"`
	UsageDaily   float64 `json:"usage_daily"`
	UsageWeekly  float64 `json:"usage_weekly"`
	UsageMonthly float64 `json:"usage_monthly"`

	BYOKUsage        float64 `json:"byok_usage"`
	BYOKUsageDaily   float64 `json:"byok_usage_daily"`
	BYOKUsageWeekly  float64 `json:"byok_usage_weekly"`
	BYOKUsageMonthly float64 `json:"byok_usage_monthly"`

	CreatedAt     string  `json:"created_at"`
	UpdatedAt     *string `json:"updated_at"`
	LastUsedAt    *string `json:"last_used_at"`
	ExpiresAt     *string `json:"expires_at,omitempty"`
	ExternalUser  *string `json:"external_user"`
	CreatorUserID *string `json:"creator_user_id"`
	WorkspaceID   string  `json:"workspace_id"`
}

// Status returns "disabled", "expired" or "active".
func (k Key) Status() string {
	if k.Disabled {
		return "disabled"
	}
	if k.ExpiresAt != nil {
		if t, err := time.Parse(time.RFC3339, *k.ExpiresAt); err == nil && !t.After(time.Now()) {
			return "expired"
		}
	}
	return "active"
}

// ListOptions controls GET /keys.
type ListOptions struct {
	Offset          int
	IncludeDisabled bool
	WorkspaceID     string
}

// CreateRequest is the body of POST /keys.
type CreateRequest struct {
	Name               string   `json:"name"`
	Limit              *float64 `json:"limit,omitempty"`
	LimitReset         *string  `json:"limit_reset,omitempty"`
	IncludeBYOKInLimit *bool    `json:"include_byok_in_limit,omitempty"`
	ExpiresAt          *string  `json:"expires_at,omitempty"`
	WorkspaceID        string   `json:"workspace_id,omitempty"`
	CreatorUserID      string   `json:"creator_user_id,omitempty"`
}

// CreateResponse is the body returned by POST /keys.
// Key holds the plaintext secret. The API returns it only once.
type CreateResponse struct {
	Data Key    `json:"data"`
	Key  string `json:"key"`
}

// Nullable is a field that can be absent, explicitly null, or set to a value.
type Nullable[T any] struct {
	Set   bool // field is present in the request
	Valid bool // value is non-null
	V     T
}

// Value returns a Nullable that holds v.
func Value[T any](v T) Nullable[T] { return Nullable[T]{Set: true, Valid: true, V: v} }

// Null returns a Nullable that is sent as JSON null.
func Null[T any]() Nullable[T] { return Nullable[T]{Set: true} }

// Ptr returns nil for absent or null, else a pointer to the value.
func (n Nullable[T]) Ptr() *T {
	if !n.Set || !n.Valid {
		return nil
	}
	v := n.V
	return &v
}

func (n Nullable[T]) json() any {
	if !n.Valid {
		return nil
	}
	return n.V
}

// UpdateRequest is the body of PATCH /keys/{hash}. Only set fields are sent.
type UpdateRequest struct {
	Name               *string
	Disabled           *bool
	IncludeBYOKInLimit *bool
	Limit              Nullable[float64]
	LimitReset         Nullable[string]
}

// Empty reports whether no field is set.
func (u UpdateRequest) Empty() bool {
	return u.Name == nil && u.Disabled == nil && u.IncludeBYOKInLimit == nil &&
		!u.Limit.Set && !u.LimitReset.Set
}

// MarshalJSON sends only the set fields. Null fields are sent as null.
func (u UpdateRequest) MarshalJSON() ([]byte, error) {
	m := map[string]any{}
	if u.Name != nil {
		m["name"] = *u.Name
	}
	if u.Disabled != nil {
		m["disabled"] = *u.Disabled
	}
	if u.IncludeBYOKInLimit != nil {
		m["include_byok_in_limit"] = *u.IncludeBYOKInLimit
	}
	if u.Limit.Set {
		m["limit"] = u.Limit.json()
	}
	if u.LimitReset.Set {
		m["limit_reset"] = u.LimitReset.json()
	}
	return json.Marshal(m)
}
