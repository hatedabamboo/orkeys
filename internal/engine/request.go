package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

var unsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

type Request struct {
	User     string
	Team     string
	Kind     string
	Provider string
	Purpose  string
	LimitUSD float64
	TTL      time.Duration
}

type Issued struct {
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Provider   string    `json:"provider"`
	File       string    `json:"file"`
	ExpiresAt  time.Time `json:"expires_at"`
	LimitUSD   float64   `json:"limit_usd"`
	LimitReset string    `json:"limit_reset"`
}

func (e *Engine) Request(ctx context.Context, r Request) (*Issued, error) {
	pol := e.Inv.Policy.Kind(r.Kind)
	if pol == nil || pol.Source != manifest.SourceRequest {
		return nil, fmt.Errorf("kind %q: want one of %s", r.Kind, strings.Join(e.Inv.Policy.RequestKinds(), ", "))
	}
	if !pol.AllowsProvider(r.Provider) {
		return nil, fmt.Errorf("%s keys are not allowed for %q (allowed: %s)", r.Kind, r.Provider, strings.Join(pol.Providers, ", "))
	}
	if strings.TrimSpace(r.Purpose) == "" {
		return nil, errors.New("purpose is required")
	}
	team, err := e.teamOf(ctx, r.User, r.Team)
	if err != nil {
		return nil, err
	}
	limit := r.LimitUSD
	if limit == 0 {
		limit = pol.Budget.Limit
	}
	if limit <= 0 || limit > pol.MaxLimit {
		return nil, fmt.Errorf("limit $%.2f: want more than 0 and at most $%.2f", limit, pol.MaxLimit)
	}
	reset := pol.Budget.Reset
	if info, ok := provider.Lookup(r.Provider); ok && !info.Caps.LimitReset && reset != manifest.DefaultReset {
		return nil, fmt.Errorf("%s supports only monthly limits; kinds.%s.budget.reset is %s", r.Provider, r.Kind, reset)
	}
	ttl := pol.TTL.Duration
	if r.TTL > 0 {
		if r.TTL > ttl {
			return nil, fmt.Errorf("ttl %s: more than the %s maximum %s", r.TTL, r.Kind, ttl)
		}
		ttl = r.TTL
	}

	states, err := e.State.ListStates(ctx)
	if err != nil {
		return nil, err
	}
	active, total := 0, 0.0
	for _, st := range states {
		if st.Kind == r.Kind && strings.EqualFold(st.Owner, r.User) {
			active++
			total += st.LimitUSD
		}
	}
	if pol.MaxActive > 0 && active >= pol.MaxActive {
		return nil, fmt.Errorf("%s already has %d active %s key(s); the limit is %d. Wait for one to expire", r.User, active, r.Kind, pol.MaxActive)
	}
	if pol.MaxTotalLimit > 0 && total+limit > pol.MaxTotalLimit {
		return nil, fmt.Errorf("%s keys of %s would total a $%.2f limit; the maximum is $%.2f", r.Kind, r.User, total+limit, pol.MaxTotalLimit)
	}

	p, err := e.Providers.Get(r.Provider)
	if err != nil {
		return nil, err
	}
	now := e.now()
	exp := now.Add(ttl)
	user := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(r.User), "-"), "-")
	name := r.Kind + "-" + user + "-" + now.Format("20060102-150405")
	spec := provider.Spec{Name: name, LimitUSD: &limit, LimitReset: reset}
	if p.Caps().NativeExpiry {
		spec.ExpiresAt = &exp
	}
	pk, secret, err := p.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	undo := func(err error) (*Issued, error) {
		if derr := p.Delete(ctx, pk.ID); derr != nil {
			return nil, fmt.Errorf("%w; delete new key %s also failed: %w", err, pk.ID, derr)
		}
		return nil, fmt.Errorf("%w (new key deleted)", err)
	}
	file, err := e.Deliverer.Deliver(ctx, r.User, name, secret)
	if err != nil {
		return undo(fmt.Errorf("deliver: %w", err))
	}
	st := &state.State{
		Name: name, Team: team.Team, Kind: r.Kind, Provider: r.Provider, Owner: r.User, Purpose: r.Purpose,
		KeyID: pk.ID, CreatedAt: now, ExpiresAt: exp, LimitUSD: limit, LimitReset: reset,
		NotifyChannel: team.Channels.Notifications, AlertChannel: team.ChannelFor(pol),
	}
	if err := e.putState(ctx, st); err != nil {
		return undo(err)
	}
	e.emit(st, LevelInfo, EventKeyIssued, "%s key ($%.2f %s) issued to %s for: %s; expires %s",
		r.Kind, limit, reset, r.User, r.Purpose, exp.Format(time.DateOnly))
	return &Issued{Name: name, Kind: r.Kind, Provider: r.Provider, File: file, ExpiresAt: exp, LimitUSD: limit, LimitReset: reset}, nil
}

type IssuedKey struct {
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Team       string    `json:"team"`
	Owner      string    `json:"owner"`
	Purpose    string    `json:"purpose"`
	Provider   string    `json:"provider"`
	LimitUSD   float64   `json:"limit_usd"`
	LimitReset string    `json:"limit_reset"`
	SpendUSD   *float64  `json:"spend_usd"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type IssuedFilter struct {
	Team  string
	Owner string
	Kind  string
}

func (e *Engine) Issued(ctx context.Context, f IssuedFilter) ([]IssuedKey, error) {
	states, err := e.State.ListStates(ctx)
	if err != nil {
		return nil, err
	}
	lists, err := e.listAll(ctx)
	if err != nil {
		return nil, err
	}
	var out []IssuedKey
	for _, st := range states {
		if !e.Inv.Policy.Requested(st.Kind, st.Owner) ||
			(f.Team != "" && st.Team != f.Team) ||
			(f.Owner != "" && !strings.EqualFold(st.Owner, f.Owner)) ||
			(f.Kind != "" && st.Kind != f.Kind) {
			continue
		}
		ik := IssuedKey{
			Name: st.Name, Kind: st.Kind, Team: st.Team, Owner: st.Owner, Purpose: st.Purpose, Provider: st.Provider,
			LimitUSD: st.LimitUSD, LimitReset: st.LimitReset, CreatedAt: st.CreatedAt, ExpiresAt: st.ExpiresAt,
		}
		if k := provider.Find(lists[st.Provider], st.KeyID); k != nil {
			ik.SpendUSD = k.SpendFor(st.LimitReset)
		}
		out = append(out, ik)
	}
	slices.SortFunc(out, func(a, b IssuedKey) int {
		return strings.Compare(a.Team+"/"+a.Owner+"/"+a.Name, b.Team+"/"+b.Owner+"/"+b.Name)
	})
	return out, nil
}
