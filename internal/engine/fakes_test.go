package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

type fakeProvider struct {
	name string
	caps provider.Caps
	keys []provider.Key
	n    int
}

func (f *fakeProvider) Name() string        { return f.name }
func (f *fakeProvider) Caps() provider.Caps { return f.caps }
func (f *fakeProvider) SelfID() string      { return "self" }

func (f *fakeProvider) List(context.Context) ([]provider.Key, error) {
	return append([]provider.Key(nil), f.keys...), nil
}

func (f *fakeProvider) Create(_ context.Context, s provider.Spec) (provider.Key, string, error) {
	f.n++
	zero := 0.0
	k := provider.Key{ID: fmt.Sprintf("%s-id-%d", f.name, f.n), Name: s.Name, LimitUSD: s.LimitUSD, ExpiresAt: s.ExpiresAt, UsageUSD: &zero}
	if f.caps.LimitReset {
		k.LimitReset = s.LimitReset
	}
	f.keys = append(f.keys, k)
	return k, fmt.Sprintf("secret-%d", f.n), nil
}

func (f *fakeProvider) find(id string) *provider.Key { return provider.Find(f.keys, id) }

func (f *fakeProvider) SetLimit(_ context.Context, id string, l *float64, r string) error {
	k := f.find(id)
	if k == nil {
		return errors.New("not found")
	}
	k.LimitUSD, k.LimitReset = l, r
	return nil
}

func (f *fakeProvider) Disable(_ context.Context, id string) error {
	if k := f.find(id); k != nil {
		k.Disabled = true
	}
	return nil
}

func (f *fakeProvider) Enable(_ context.Context, id string) error {
	if k := f.find(id); k != nil {
		k.Disabled = false
	}
	return nil
}

func (f *fakeProvider) Delete(_ context.Context, id string) error {
	for i := range f.keys {
		if f.keys[i].ID == id {
			f.keys = append(f.keys[:i], f.keys[i+1:]...)
			return nil
		}
	}
	return nil
}

type fakeStore struct {
	states       map[string]state.State
	secrets      map[string]string
	failPutState int
	failSecret   error
	failPath     string // Put to this path fails
}

func newStore() *fakeStore {
	return &fakeStore{states: map[string]state.State{}, secrets: map[string]string{}}
}

func (s *fakeStore) ListStates(context.Context) ([]*state.State, error) {
	var names []string
	for n := range s.states {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []*state.State
	for _, n := range names {
		st := s.states[n]
		st.PendingRevokes = append([]state.PendingRevoke(nil), st.PendingRevokes...)
		out = append(out, &st)
	}
	return out, nil
}

func (s *fakeStore) GetState(_ context.Context, n string) (*state.State, error) {
	st, ok := s.states[n]
	if !ok {
		return nil, nil
	}
	return &st, nil
}

func (s *fakeStore) PutState(_ context.Context, st *state.State) error {
	if s.failPutState > 0 {
		s.failPutState--
		return errors.New("state store down")
	}
	c := *st
	c.PendingRevokes = append([]state.PendingRevoke(nil), st.PendingRevokes...)
	s.states[st.Name] = c
	return nil
}

func (s *fakeStore) DeleteState(_ context.Context, n string) error {
	delete(s.states, n)
	return nil
}

func (s *fakeStore) Put(_ context.Context, path, field, value string, _ map[string]string) error {
	if s.failSecret != nil {
		return s.failSecret
	}
	if path == s.failPath {
		return errors.New("secret store down for " + path)
	}
	s.secrets[path+"#"+field] = value
	return nil
}

func (s *fakeStore) DeleteField(_ context.Context, path, field string) error {
	delete(s.secrets, path+"#"+field)
	return nil
}

type fakeKube struct {
	calls []string
	fail  error
}

func (k *fakeKube) ForceSync(_ context.Context, c, ns, n string) error {
	k.calls = append(k.calls, "sync "+c+"/"+ns+"/"+n)
	return k.fail
}

func (k *fakeKube) Restart(_ context.Context, c, ns, t string) error {
	k.calls = append(k.calls, "restart "+c+"/"+ns+"/"+t)
	return k.fail
}

type fakeDeliverer struct {
	got  map[string]string
	fail error
}

func (d *fakeDeliverer) Deliver(_ context.Context, user, name, secret string) (string, error) {
	if d.fail != nil {
		return "", d.fail
	}
	if d.got == nil {
		d.got = map[string]string{}
	}
	d.got[user+"/"+name] = secret
	return "out/" + name + ".age", nil
}

type fakeOrg struct {
	members  map[string]bool
	checkErr error
	teams    map[string][]string // GitHub team slug -> members
}

func (o *fakeOrg) Check(context.Context) error { return o.checkErr }

func (o *fakeOrg) IsMember(_ context.Context, u string) (bool, error) { return o.members[u], nil }

func (o *fakeOrg) CheckTeam(_ context.Context, slug string) error {
	if _, ok := o.teams[slug]; !ok {
		return fmt.Errorf("%s: github team not found", slug)
	}
	return nil
}

func (o *fakeOrg) IsTeamMember(ctx context.Context, slug, u string) (bool, error) {
	ms, err := o.TeamMembers(ctx, slug)
	for _, m := range ms {
		if strings.EqualFold(m, u) {
			return true, nil
		}
	}
	return false, err
}

func (o *fakeOrg) TeamMembers(ctx context.Context, slug string) ([]string, error) {
	if err := o.CheckTeam(ctx, slug); err != nil {
		return nil, err
	}
	return o.teams[slug], nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func days(n int) time.Duration       { return time.Duration(n) * 24 * time.Hour }

func (s *fakeStore) Read(_ context.Context, path string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range s.secrets {
		if p, f, ok := strings.Cut(k, "#"); ok && p == path {
			out[f] = v
		}
	}
	return out, nil
}

type fakeGitHub struct {
	secrets map[string]string // GitHubSecret ID -> value
	fail    error
}

func (g *fakeGitHub) Put(_ context.Context, t manifest.GitHubSecret, value string) error {
	if g.fail != nil {
		return g.fail
	}
	if g.secrets == nil {
		g.secrets = map[string]string{}
	}
	g.secrets[t.ID()] = value
	return nil
}

func (g *fakeGitHub) Delete(_ context.Context, t manifest.GitHubSecret) error {
	if g.fail != nil {
		return g.fail
	}
	delete(g.secrets, t.ID())
	return nil
}
