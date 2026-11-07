package vault

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
	data, err := s.C.Read(ctx, s.path(name))
	if err != nil || data == nil {
		return nil, err
	}
	var st state.State
	if err := fromMap(data, &st); err != nil {
		return nil, fmt.Errorf("state %s: %w", name, err)
	}
	return &st, nil
}

func (s *StateStore) PutState(ctx context.Context, st *state.State) error {
	m, err := toMap(st)
	if err != nil {
		return err
	}
	return s.C.Write(ctx, s.path(st.Name), m)
}

func (s *StateStore) DeleteState(ctx context.Context, name string) error {
	return s.C.Destroy(ctx, s.path(name))
}

type SecretStore struct {
	C *Client
}

func (s *SecretStore) Put(ctx context.Context, path, field, value string, meta map[string]string) error {
	if err := s.C.Patch(ctx, path, map[string]any{field: value}); err != nil {
		return err
	}
	if len(meta) == 0 {
		return nil
	}
	return s.C.SetCustomMetadata(ctx, path, meta)
}

func (s *SecretStore) DeleteField(ctx context.Context, path, field string) error {
	data, err := s.C.Read(ctx, path)
	if err != nil || data == nil {
		return err
	}
	if _, ok := data[field]; !ok {
		return nil
	}
	delete(data, field)
	if len(data) == 0 {
		return s.C.Destroy(ctx, path)
	}
	return s.C.Write(ctx, path, data)
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
