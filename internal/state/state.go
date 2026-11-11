package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// SchemaVersion is the version of the state format that this build writes.
// A build refuses to write a state with a higher version. Such a write drops the fields that the build does not know.
const SchemaVersion = 2

// ErrConflict means that the state in the store has changed since the read, or that a new state exists already.
var ErrConflict = errors.New("state changed in the store since it was read")

type Rotation string

const (
	RotationIdle    Rotation = ""
	RotationPending Rotation = "pending"
	RotationFailed  Rotation = "failed"
)

const (
	RevokeRotation = "rotation"
)

// Phase marks a change that has more than one step. The state is saved before the first step.
// A crash then leaves a record of the change.
type Phase string

const (
	// PhaseReady: no change is in progress.
	PhaseReady Phase = ""
	// PhaseIssuing: the provider key exists and the state has its ID, but the secrets may not hold it yet.
	PhaseIssuing Phase = "issuing"
)

type PendingRevoke struct {
	KeyID  string    `json:"key_id"`
	Source string    `json:"source"`
	Reason string    `json:"reason"`
	After  time.Time `json:"after"`
	// Set when the new key did not go to some places of the old key: these fields are removed after the revoke.
	Secrets []SecretRef `json:"secrets,omitempty"`
	// After CheckFrom, a rotate run records the old key's usage in Baseline. At After, more usage
	// than Baseline means a consumer still has the old key, so the revoke waits.
	CheckFrom time.Time `json:"check_from"`
	Baseline  *float64  `json:"baseline_usd,omitempty"`
}

type State struct {
	// Schema is the SchemaVersion of the build that wrote the state. States from before version 2 have none.
	Schema int `json:"schema,omitempty"`
	// Rev is the store revision of the state at read time: a Vault version or an S3 ETag.
	// PutState writes only if the stored state has this revision. With an empty Rev, PutState only creates.
	// PutState sets Rev to the new revision.
	Rev string `json:"-"`

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

	Phase          Phase           `json:"phase,omitempty"`
	Rotation       Rotation        `json:"rotation,omitempty"`
	RotationError  string          `json:"rotation_error,omitempty"`
	PendingRevokes []PendingRevoke `json:"pending_revokes,omitempty"`
	ExpiryNotified bool            `json:"expiry_notified,omitempty"`

	LimitUSD      float64     `json:"limit_usd"`
	LimitReset    string      `json:"limit_reset,omitempty"`
	Secrets       []SecretRef `json:"secrets,omitempty"`
	NotifyChannel string      `json:"notify_channel,omitempty"`
	AlertChannel  string      `json:"alert_channel,omitempty"`

	// Pushed maps each push target of orkeys (<consumer type>/<target ID>) to the provider key ID in it.
	Pushed map[string]string `json:"pushed,omitempty"`
}

// SecretRef is one field of one secret in the secrets store.
type SecretRef struct {
	Path  string `json:"path"`
	Field string `json:"field"`
}

func (r SecretRef) String() string { return r.Path + "#" + r.Field }

// SameSecrets reports whether a and b hold the same places, in any order.
func SameSecrets(a, b []SecretRef) bool {
	if len(a) != len(b) {
		return false
	}
	for _, r := range a {
		if !slices.Contains(b, r) {
			return false
		}
	}
	return true
}

// legacySecret is the single secret_path and secret_field of state written before secrets was a list.
type legacySecret struct {
	SecretPath  string `json:"secret_path,omitempty"`
	SecretField string `json:"secret_field,omitempty"`
}

func (l legacySecret) refs(cur []SecretRef) []SecretRef {
	if len(cur) > 0 || l.SecretPath == "" {
		return cur
	}
	return []SecretRef{{Path: l.SecretPath, Field: l.SecretField}}
}

// legacyGitHub is the github_secrets map of states from schema 1.
type legacyGitHub struct {
	GitHubSecrets map[string]string `json:"github_secrets,omitempty"`
}

func (s *State) UnmarshalJSON(b []byte) error {
	type plain State
	var v struct {
		plain
		legacySecret
		legacyGitHub
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = State(v.plain)
	s.Secrets = v.refs(s.Secrets)
	for id, keyID := range v.GitHubSecrets {
		if s.Pushed == nil {
			s.Pushed = map[string]string{}
		}
		if _, ok := s.Pushed["github/"+id]; !ok {
			s.Pushed["github/"+id] = keyID
		}
	}
	return nil
}

func (p *PendingRevoke) UnmarshalJSON(b []byte) error {
	type plain PendingRevoke
	var v struct {
		plain
		legacySecret
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = PendingRevoke(v.plain)
	p.Secrets = v.refs(p.Secrets)
	return nil
}

// CheckSchema returns an error for the first state that a newer orkeys wrote.
func CheckSchema(states ...*State) error {
	for _, st := range states {
		if st != nil && st.Schema > SchemaVersion {
			return fmt.Errorf("state %s: schema %d is newer than %d; upgrade orkeys", st.Name, st.Schema, SchemaVersion)
		}
	}
	return nil
}

func (s *State) KnownIDs() []string {
	ids := []string{s.KeyID}
	for _, p := range s.PendingRevokes {
		ids = append(ids, p.KeyID)
	}
	return ids
}

// Heartbeats records the time of the last successful run of each operation. The exporter shows it.
// An alert on it fires when the scheduled rotate stops. Without rotate, keys with a native expiry expire.
type Heartbeats interface {
	PutHeartbeat(ctx context.Context, operation string, t time.Time) error
	Heartbeats(ctx context.Context) (map[string]time.Time, error)
}

// Store keeps one State per key name, and the lock of all states.
type Store interface {
	ListStates(ctx context.Context) ([]*State, error)
	// GetState returns nil, nil when no state has the name.
	GetState(ctx context.Context, name string) (*State, error)
	// PutState writes s with the check that Rev describes. A failed check returns an error that wraps ErrConflict.
	PutState(ctx context.Context, s *State) error
	DeleteState(ctx context.Context, name string) error
	Locker
}
