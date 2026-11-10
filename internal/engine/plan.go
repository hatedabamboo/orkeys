package engine

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
	"github.com/hatedabamboo/orkeys/internal/values"
)

type ActionType string

const (
	ActCreate      ActionType = "create"
	ActImport      ActionType = "import"
	ActRecreate    ActionType = "recreate"
	ActReissue     ActionType = "reissue"
	ActUpdateLimit ActionType = "update-limit"
	ActUpdateState ActionType = "update-state"
	ActDisable     ActionType = "disable"
	ActEnable      ActionType = "enable"
	ActDelete      ActionType = "delete"
)

type Action struct {
	Type     ActionType    `json:"type"`
	Name     string        `json:"name"`
	Team     string        `json:"team"`
	Provider string        `json:"provider"`
	Detail   string        `json:"detail"`
	Key      *manifest.Key `json:"-"`
	State    *state.State  `json:"-"`
	Current  *provider.Key `json:"-"`
}

type ForeignKey struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Name     string `json:"name"`
}

type Plan struct {
	Actions   []Action     `json:"actions"`
	Unmanaged []ForeignKey `json:"unmanaged"`
	Warnings  []string     `json:"warnings"`
}

func (p *Plan) Empty() bool { return len(p.Actions) == 0 }

func (e *Engine) Plan(ctx context.Context) (*Plan, error) {
	states, err := e.State.ListStates(ctx)
	if err != nil {
		return nil, err
	}
	lists, err := e.listAll(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]*state.State{}
	known := map[string]string{}
	for _, st := range states {
		byName[st.Name] = st
		for _, id := range st.KnownIDs() {
			known[st.Provider+"/"+id] = st.Name
		}
	}

	plan := &Plan{Actions: []Action{}, Unmanaged: []ForeignKey{}, Warnings: []string{}}
	if e.Org != nil {
		for _, n := range e.Inv.TeamNames() {
			if slug := e.slugOf(e.Inv.Teams[n]); slug != "" {
				if err := e.Org.CheckTeam(ctx, slug); err != nil {
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("teams/%s/team.yaml: %v", n, err))
				}
			}
		}
	}
	for _, k := range e.Inv.Keys {
		p, err := e.Providers.Get(k.Provider)
		if err != nil {
			return nil, err
		}
		st := byName[k.Name]
		act := Action{Name: k.Name, Team: k.Team, Provider: k.Provider, Key: k, State: st}
		limit := k.Budget.Limit

		if st == nil && k.Import != nil {
			cur := provider.Find(lists[k.Provider], k.Import.ID)
			switch {
			case known[k.Provider+"/"+k.Import.ID] != "":
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: import: key %s is already managed as %s", k.Name, short(k.Import.ID), known[k.Provider+"/"+k.Import.ID]))
			case cur == nil:
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: import: no %s key with id %s", k.Name, k.Provider, k.Import.ID))
			case cur.Name != k.Import.Name:
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: import: key %s is named %q at the provider, not %q", k.Name, short(k.Import.ID), cur.Name, k.Import.Name))
			default:
				act.Type, act.Current = ActImport, cur
				act.Detail = fmt.Sprintf("adopt %s key %s (%s), limit $%.2f %s, ttl %s from now", k.Kind, short(cur.ID), cur.Name, limit, k.Budget.ResetOrDefault(), values.Days(k.TTL.Duration))
				plan.Actions = append(plan.Actions, act)
			}
			continue
		}
		if st == nil {
			act.Type, act.Detail = ActCreate, fmt.Sprintf("%s key, limit $%.2f %s, ttl %s", k.Kind, limit, k.Budget.ResetOrDefault(), values.Days(k.TTL.Duration))
			plan.Actions = append(plan.Actions, act)
			continue
		}
		if st.Provider != k.Provider || st.Kind != k.Kind {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: provider or kind changed; add a key with a new name instead", k.Name))
			continue
		}
		want := e.stateFor(k)
		if want.SecretPath != st.SecretPath || want.SecretField != st.SecretField {
			act.Type, act.Detail = ActReissue, fmt.Sprintf("secret moved %s#%s -> %s#%s", st.SecretPath, st.SecretField, want.SecretPath, want.SecretField)
			plan.Actions = append(plan.Actions, act)
			continue
		}
		cur := provider.Find(lists[k.Provider], st.KeyID)
		if cur == nil {
			act.Type, act.Detail = ActRecreate, "key "+short(st.KeyID)+" is missing at the provider"
			plan.Actions = append(plan.Actions, act)
			continue
		}
		caps := p.Caps()
		switch {
		case k.Disabled && !cur.Disabled:
			a := act
			a.Type, a.Current, a.Detail = ActDisable, cur, "disabled: true in the manifest"
			plan.Actions = append(plan.Actions, a)
		case !k.Disabled && cur.Disabled && e.Inv.Policy.TrackDisabled:
			a := act
			a.Type, a.Current = ActEnable, cur
			a.Detail = "disabled at the provider, not in the manifest (track_disabled)"
			if caps.PerKeyLimit {
				a.Detail += fmt.Sprintf("; limit $%.2f %s", limit, k.Budget.ResetOrDefault())
			}
			plan.Actions = append(plan.Actions, a)
		}
		if caps.PerKeyLimit && !cur.Disabled && !k.Disabled {
			resetDiff := caps.LimitReset && cur.LimitReset != k.Budget.ResetOrDefault()
			if cur.LimitUSD == nil || math.Abs(*cur.LimitUSD-limit) > 0.005 || resetDiff {
				a := act
				a.Type, a.Current = ActUpdateLimit, cur
				a.Detail = fmt.Sprintf("limit %s -> $%.2f %s", fmtLimit(cur), limit, k.Budget.ResetOrDefault())
				plan.Actions = append(plan.Actions, a)
			}
		}
		if d := metaDiff(st, want); d != "" {
			a := act
			a.Type, a.Detail = ActUpdateState, d
			plan.Actions = append(plan.Actions, a)
		}
	}

	for _, st := range states {
		if e.Inv.Policy.Requested(st.Kind, st.Owner) || e.Inv.Key(st.Name) != nil {
			continue
		}
		plan.Actions = append(plan.Actions, Action{
			Type: ActDelete, Name: st.Name, Team: st.Team, Provider: st.Provider, State: st,
			Detail: "manifest removed; revoke " + strings.Join(shortAll(st.KnownIDs()), ", "),
		})
	}

	for _, k := range e.Inv.Keys {
		if k.Import != nil && byName[k.Name] == nil {
			known[k.Provider+"/"+k.Import.ID] = k.Name
		}
	}
	for name, keys := range lists {
		self := e.Providers[name].SelfID()
		for _, pk := range keys {
			id := name + "/" + pk.ID
			if known[id] != "" || pk.ID == self {
				continue
			}
			// Without track_disabled, a disabled key without a manifest is not reported for import.
			if pk.Disabled && !e.Inv.Policy.TrackDisabled {
				continue
			}
			plan.Unmanaged = append(plan.Unmanaged, ForeignKey{Provider: name, ID: pk.ID, Name: pk.Name})
		}
	}
	sortForeign(plan.Unmanaged)
	return plan, nil
}

func metaDiff(cur, want *state.State) string {
	var d []string
	cmp := func(name string, a, b any) {
		if a != b {
			d = append(d, fmt.Sprintf("%s %v -> %v", name, a, b))
		}
	}
	cmp("team", cur.Team, want.Team)
	cmp("env", cur.Env, want.Env)
	cmp("service", cur.Service, want.Service)
	cmp("budget.limit", cur.LimitUSD, want.LimitUSD)
	cmp("budget.reset", cur.LimitReset, want.LimitReset)
	cmp("channels.notifications", cur.NotifyChannel, want.NotifyChannel)
	cmp("channels.alerts", cur.AlertChannel, want.AlertChannel)
	return strings.Join(d, ", ")
}

func sortForeign(f []ForeignKey) {
	slices.SortFunc(f, func(a, b ForeignKey) int { return strings.Compare(a.Provider+a.ID, b.Provider+b.ID) })
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func shortAll(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = short(id)
	}
	return out
}

func fmtLimit(k *provider.Key) string {
	if k.LimitUSD == nil {
		return "none"
	}
	s := fmt.Sprintf("$%.2f", *k.LimitUSD)
	if k.LimitReset != "" {
		s += " " + k.LimitReset
	}
	return s
}
