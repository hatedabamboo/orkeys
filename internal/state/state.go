package state

import (
	"context"
	"time"
)

type Rotation string

const (
	RotationIdle    Rotation = ""
	RotationPending Rotation = "pending"
	RotationFailed  Rotation = "failed"
)

const (
	RevokeRotation = "rotation"
)

type PendingRevoke struct {
	KeyID  string    `json:"key_id"`
	Source string    `json:"source"`
	Reason string    `json:"reason"`
	After  time.Time `json:"after"`
	// Set when the new key went to another secret path: the old field is removed after the revoke.
	SecretPath  string `json:"secret_path,omitempty"`
	SecretField string `json:"secret_field,omitempty"`
	// After CheckFrom, a rotate run records the old key's usage in Baseline. At After, more usage
	// than Baseline means a consumer still has the old key, so the revoke waits.
	CheckFrom time.Time `json:"check_from"`
	Baseline  *float64  `json:"baseline_usd,omitempty"`
}

type State struct {
	Name     string `json:"name"`
	Team     string `json:"team"`
	Kind     string `json:"kind"`
	Env      string `json:"env,omitempty"`
	Service  string `json:"service,omitempty"`
	Provider string `json:"provider"`
	Owner    string `json:"owner,omitempty"`
	Purpose  string `json:"purpose,omitempty"`

	KeyID     string    `json:"key_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`

	Rotation       Rotation        `json:"rotation,omitempty"`
	RotationError  string          `json:"rotation_error,omitempty"`
	PendingRevokes []PendingRevoke `json:"pending_revokes,omitempty"`
	ExpiryNotified bool            `json:"expiry_notified,omitempty"`

	LimitUSD      float64 `json:"limit_usd"`
	LimitReset    string  `json:"limit_reset,omitempty"`
	SecretPath    string  `json:"secret_path,omitempty"`
	SecretField   string  `json:"secret_field,omitempty"`
	NotifyChannel string  `json:"notify_channel,omitempty"`
	AlertChannel  string  `json:"alert_channel,omitempty"`
}

func (s *State) KnownIDs() []string {
	ids := []string{s.KeyID}
	for _, p := range s.PendingRevokes {
		ids = append(ids, p.KeyID)
	}
	return ids
}

type Store interface {
	ListStates(ctx context.Context) ([]*State, error)
	GetState(ctx context.Context, name string) (*State, error)
	PutState(ctx context.Context, s *State) error
	DeleteState(ctx context.Context, name string) error
}
