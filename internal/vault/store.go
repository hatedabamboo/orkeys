package vault

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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
func (s *SecretStore) Put(ctx context.Context, path, field, value string, meta map[string]string) error {
	if err := s.C.Patch(ctx, path, map[string]any{field: value}); err != nil {
		return err
	}
	if len(meta) == 0 {
		return nil
	}
	return s.C.SetCustomMetadata(ctx, path, meta)
}

// DeleteField removes one field with check-and-set. It destroys the secret when no field is left.
func (s *SecretStore) DeleteField(ctx context.Context, path, field string) error {
	return s.C.Update(ctx, path, func(cur map[string]any) (map[string]any, bool) {
		if _, ok := cur[field]; !ok {
			return nil, false
		}
		delete(cur, field)
		if len(cur) == 0 {
			return nil, true
		}
		return cur, true
	})
}

// Meta returns the custom metadata of the secret at path, or nil when it does not exist.
func (s *SecretStore) Meta(ctx context.Context, path string) (map[string]string, error) {
	return s.C.CustomMetadata(ctx, path)
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
