package vault

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/state"
)

type StateStore struct {
	C      *Client
	Prefix string
}

func (s *StateStore) path(name string) string { return strings.TrimRight(s.Prefix, "/") + "/" + name }

func (s *StateStore) ListStates(ctx context.Context) ([]*state.State, error) {
	names, err := s.C.List(ctx, s.Prefix)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []*state.State
	for _, n := range names {
		if strings.HasSuffix(n, "/") || strings.HasPrefix(n, ".") {
			continue
		}
		st, err := s.GetState(ctx, n)
		if err != nil {
			return nil, err
		}
		if st != nil {
			out = append(out, st)
		}
	}
	return out, nil
}

func (s *StateStore) GetState(ctx context.Context, name string) (*state.State, error) {
	data, version, err := s.C.ReadVersion(ctx, s.path(name))
	if err != nil || data == nil {
		return nil, err
	}
	var st state.State
	if err := fromMap(data, &st); err != nil {
		return nil, fmt.Errorf("state %s: %w", name, err)
	}
	st.Rev = strconv.Itoa(version)
	return &st, nil
}

// PutState writes with check-and-set on the Vault version in st.Rev (0 when st.Rev is empty: create only).
func (s *StateStore) PutState(ctx context.Context, st *state.State) error {
	cas := 0
	if st.Rev != "" {
		v, err := strconv.Atoi(st.Rev)
		if err != nil {
			return fmt.Errorf("state %s: bad revision %q", st.Name, st.Rev)
		}
		cas = v
	}
	m, err := toMap(st)
	if err != nil {
		return err
	}
	version, err := s.C.WriteCAS(ctx, s.path(st.Name), m, cas)
	if errors.Is(err, ErrCASMismatch) {
		return fmt.Errorf("state %s: %w", st.Name, state.ErrConflict)
	}
	if err != nil {
		return err
	}
	st.Rev = strconv.Itoa(version)
	return nil
}

func (s *StateStore) DeleteState(ctx context.Context, name string) error {
	return s.C.Destroy(ctx, s.path(name))
}

// heartbeatName is the secret with the heartbeats. ListStates skips names that start with ".".
const heartbeatName = ".heartbeat"

// PutHeartbeat records t as the last success of operation.
func (s *StateStore) PutHeartbeat(ctx context.Context, operation string, t time.Time) error {
	return s.C.Patch(ctx, s.path(heartbeatName), map[string]any{operation: t.UTC().Format(time.RFC3339)})
}

// Heartbeats returns the last success of each operation.
func (s *StateStore) Heartbeats(ctx context.Context) (map[string]time.Time, error) {
	data, err := s.C.Read(ctx, s.path(heartbeatName))
	if err != nil {
		return nil, err
	}
	return parseHeartbeats(data), nil
}

// policyName is the secret with the copy of policy.yaml. ListStates skips names that start with ".".
const policyName = ".policy"

// PutPolicy writes the copy of policy.yaml. apply calls it under the state lock.
func (s *StateStore) PutPolicy(ctx context.Context, p *state.StoredPolicy) error {
	return s.C.Write(ctx, s.path(policyName), map[string]any{
		"policy_yaml": p.YAML,
		"updated_at":  p.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

// GetPolicy returns the copy of policy.yaml, or nil when there is none.
func (s *StateStore) GetPolicy(ctx context.Context) (*state.StoredPolicy, error) {
	data, err := s.C.Read(ctx, s.path(policyName))
	if err != nil || data == nil {
		return nil, err
	}
	y, ok := data["policy_yaml"].(string)
	if !ok {
		return nil, fmt.Errorf("vault %s: field policy_yaml is not a string", s.path(policyName))
	}
	p := &state.StoredPolicy{YAML: y}
	if str, ok := data["updated_at"].(string); ok {
		p.UpdatedAt, _ = time.Parse(time.RFC3339, str)
	}
	return p, nil
}

func parseHeartbeats(data map[string]any) map[string]time.Time {
	out := map[string]time.Time{}
	for op, v := range data {
		if str, ok := v.(string); ok {
			if t, err := time.Parse(time.RFC3339, str); err == nil {
				out[op] = t
			}
		}
	}
	return out
}

type SecretStore struct {
	C *Client
}

// Put writes the value before the custom metadata. Then metadata with a key ID proves that the field holds that key.
// The metadata write reads and replaces all custom metadata. Vault has no check-and-set for metadata. The state lock
// keeps two orkeys runs from writing at the same time.
func (s *SecretStore) Put(ctx context.Context, path, field, value string, meta map[string]string) error {
	if err := s.C.Patch(ctx, path, map[string]any{field: value}); err != nil {
		return err
	}
	if len(meta) == 0 {
		return nil
	}
	cur, err := s.C.CustomMetadata(ctx, path)
	if err != nil {
		return err
	}
	return s.C.SetCustomMetadata(ctx, path, secrets.SetFieldMeta(cur, field, meta))
}

// DeleteField removes one field with check-and-set, then its custom metadata. It destroys the secret when no field
// is left.
func (s *SecretStore) DeleteField(ctx context.Context, path, field string) error {
	destroyed := false
	err := s.C.Update(ctx, path, func(cur map[string]any) (map[string]any, bool) {
		if _, ok := cur[field]; !ok {
			return nil, false
		}
		delete(cur, field)
		if len(cur) == 0 {
			destroyed = true
			return nil, true
		}
		return cur, true
	})
	if err != nil || destroyed {
		return err
	}
	cur, err := s.C.CustomMetadata(ctx, path)
	if err != nil || cur == nil {
		return err
	}
	next := secrets.DropFieldMeta(cur, field)
	if len(next) == len(cur) {
		return nil
	}
	return s.C.SetCustomMetadata(ctx, path, next)
}

// Meta returns the custom metadata of one field, or nil when the secret does not exist.
func (s *SecretStore) Meta(ctx context.Context, path, field string) (map[string]string, error) {
	cur, err := s.C.CustomMetadata(ctx, path)
	if err != nil {
		return nil, err
	}
	return secrets.FieldMeta(cur, field), nil
}

func (s *SecretStore) Read(ctx context.Context, path string) (map[string]string, error) {
	data, err := s.C.Read(ctx, path)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("vault %s: not found", path)
	}
	out := make(map[string]string, len(data))
	for k, v := range data {
		if str, ok := v.(string); ok {
			out[k] = str
		}
	}
	return out, nil
}
