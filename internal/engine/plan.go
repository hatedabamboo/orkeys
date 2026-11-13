package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
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
	// ActTrack saves the state of a key with track_only: true. It changes nothing at the provider.
	ActTrack ActionType = "track"
	// ActUntrack removes the state of a tracked key whose manifest is gone. The key stays at the provider.
	ActUntrack ActionType = "untrack"
	// ActSyncConsumers writes the key to new push consumers and deletes removed ones.
	ActSyncConsumers ActionType = "sync-consumers"
	// ActResume finishes an interrupted key issue.
	ActResume ActionType = "resume"
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

// Plan is the list of changes that apply makes.
type Plan struct {
	Actions   []Action     `json:"actions"`
	Unmanaged []ForeignKey `json:"unmanaged"`
	Warnings  []string     `json:"warnings"`
	// Fingerprint identifies the actions. apply --plan compares it with a new plan.
	// Then apply does only what the reviewer saw.
	Fingerprint string `json:"fingerprint"`
}

// fingerprint hashes the type, key and detail of each action. Unmanaged keys and warnings do not count.
func (p *Plan) fingerprint() string {
	lines := make([]string, len(p.Actions))
	for i, a := range p.Actions {
		lines[i] = string(a.Type) + "\x00" + a.Name + "\x00" + a.Detail
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// Matches returns nil when cur has the same actions as p, else an error that names the differences.
func (p *Plan) Matches(cur *Plan) error {
	if p.Fingerprint != "" && p.Fingerprint == cur.Fingerprint {
		return nil
	}
	key := func(a Action) string { return string(a.Type) + " " + a.Name + ": " + a.Detail }
	saved, now := map[string]bool{}, map[string]bool{}
	for _, a := range p.Actions {
		saved[key(a)] = true
	}
	for _, a := range cur.Actions {
		now[key(a)] = true
	}
	var d []string
	for _, a := range cur.Actions {
		if !saved[key(a)] {
			d = append(d, "+ "+key(a))
		}
	}
	for _, a := range p.Actions {
		if !now[key(a)] {
			d = append(d, "- "+key(a))
		}
	}
	if len(d) == 0 {
		return nil
	}
	return fmt.Errorf("the plan changed since it was saved; run plan again and review it:\n  %s", strings.Join(d, "\n  "))
}

func (p *Plan) Empty() bool { return len(p.Actions) == 0 }

// Deletes returns the number of delete actions. Each one revokes a key with no manifest.
func (p *Plan) Deletes() int {
	n := 0
	for _, a := range p.Actions {
		if a.Type == ActDelete {
			n++
		}
	}
	return n
}

// Plan compares the inventory with the state and the providers and returns the changes. It changes nothing.
func (e *Engine) Plan(ctx context.Context) (*Plan, error) {
	p, _, err := e.plan(ctx)
	if p != nil {
		p.Unmanaged = slices.DeleteFunc(p.Unmanaged, func(fk ForeignKey) bool { return !e.Inv.Policy.ReportUnmanaged(fk.Provider) })
	}
	return p, err
}

// staleIgnores warns about entries of options.ignore_unmanaged that name a missing or a managed key.
func (e *Engine) staleIgnores(lists map[string][]provider.Key, known map[string]string) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(lists)) {
		for _, id := range e.Inv.Policy.Providers[name].Options.IgnoreUnmanaged {
			at := "policy.yaml: providers." + name + ".options." + manifest.OptIgnoreUnmanaged
			switch {
			case known[name+"/"+id] != "":
				out = append(out, fmt.Sprintf("%s: key %s is managed as %s; remove it from the list", at, id, known[name+"/"+id]))
			case provider.Find(lists[name], id) == nil:
				out = append(out, fmt.Sprintf("%s: no key with id %s at the provider; remove it from the list", at, id))
			}
		}
	}
	return out
}

func (e *Engine) plan(ctx context.Context) (*Plan, map[string][]provider.Key, error) {
	if err := e.ready(); err != nil {
		return nil, nil, err
	}
	states, err := e.listStates(ctx)
	if err != nil {
		return nil, nil, err
	}
	lists, err := e.listAll(ctx)
	if err != nil {
		return nil, nil, err
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
			return nil, nil, err
		}
		st := byName[k.Name]
		act := Action{Name: k.Name, Team: k.Team, Provider: k.Provider, Key: k, State: st}
		limit := k.Budget.Limit

		if k.TrackOnly() || (st != nil && st.TrackOnly) {
			e.planTracked(plan, act, lists[k.Provider], known)
			continue
		}
		if st == nil && k.Import != nil {
			cur, warn := importTarget(k, lists[k.Provider], known)
			switch {
			case warn != "":
				plan.Warnings = append(plan.Warnings, warn)
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
		if st.Phase == state.PhaseIssuing {
			act.Type, act.Detail = ActResume, "an earlier run stopped while it wrote key "+short(st.KeyID)+" to the secrets"
			plan.Actions = append(plan.Actions, act)
			continue
		}
		want := e.stateFor(k)
		if !state.SameSecrets(want.Secrets, st.Secrets) {
			act.Type, act.Detail = ActReissue, fmt.Sprintf("secret moved %s -> %s", secretList(st.Secrets), secretList(want.Secrets))
			plan.Actions = append(plan.Actions, act)
			continue
		}
		cur := provider.Find(lists[k.Provider], st.KeyID)
		if cur == nil {
			act.Type, act.Detail = ActRecreate, "key "+short(st.KeyID)+" is missing at the provider"
			plan.Actions = append(plan.Actions, act)
			continue
		}
		drift, err := e.secretDrift(ctx, st)
		if err != nil {
			return nil, nil, err
		}
		if drift != "" {
			act.Type, act.Detail = ActReissue, drift
			plan.Actions = append(plan.Actions, act)
			continue
		}
		caps := p.Caps()
		// Disable and enable save the disabled field of the state. Without them, update-state saves it.
		toggle := true
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
		default:
			toggle = false
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
		d := metaDiff(st, want)
		if !toggle && st.Disabled != want.Disabled {
			d = strings.TrimPrefix(d+fmt.Sprintf(", disabled %v -> %v", st.Disabled, want.Disabled), ", ")
		}
		if d != "" {
			a := act
			a.Type, a.Detail = ActUpdateState, d
			plan.Actions = append(plan.Actions, a)
		}
		// During a rotation, rotate writes the push consumers.
		if push, drop := pushDiff(k, st); st.Rotation == state.RotationIdle && len(push)+len(drop) > 0 {
			a := act
			a.Type, a.Detail = ActSyncConsumers, pushDetail(push, drop)
			plan.Actions = append(plan.Actions, a)
		}
	}

	for _, st := range states {
		if e.Inv.Policy.Requested(st.Kind, st.Owner) || e.Inv.Key(st.Name) != nil {
			continue
		}
		if st.TrackOnly {
			plan.Actions = append(plan.Actions, Action{
				Type: ActUntrack, Name: st.Name, Team: st.Team, Provider: st.Provider, State: st,
				Detail: "manifest removed; remove the state; key " + short(st.KeyID) + " stays at the provider",
			})
			continue
		}
		plan.Actions = append(plan.Actions, Action{
			Type: ActDelete, Name: st.Name, Team: st.Team, Provider: st.Provider, State: st,
			Detail: "manifest removed; revoke " + strings.Join(shortAll(st.KnownIDs()), ", "),
		})
	}

	for _, k := range e.Inv.Keys {
		if k.Import != nil && (byName[k.Name] == nil || k.TrackOnly()) {
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
			if e.Inv.Policy.IgnoresUnmanaged(name, pk) {
				continue
			}
			plan.Unmanaged = append(plan.Unmanaged, ForeignKey{Provider: name, ID: pk.ID, Name: pk.Name})
		}
	}
	sortForeign(plan.Unmanaged)
	plan.Warnings = append(plan.Warnings, e.staleIgnores(lists, known)...)
	plan.Fingerprint = plan.fingerprint()
	if n, limit := plan.Deletes(), e.Inv.Policy.DeleteLimit(); n > limit {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%d keys to revoke, more than max_deletes %d; apply refuses this plan without --allow-deletes %d", n, limit, n))
	}
	return plan, lists, nil
}

// importTarget returns the provider key that k imports, or a warning why the import cannot run.
func importTarget(k *manifest.Key, keys []provider.Key, known map[string]string) (*provider.Key, string) {
	cur := provider.Find(keys, k.Import.ID)
	switch {
	case known[k.Provider+"/"+k.Import.ID] != "" && known[k.Provider+"/"+k.Import.ID] != k.Name:
		return nil, fmt.Sprintf("%s: import: key %s is already managed as %s", k.Name, short(k.Import.ID), known[k.Provider+"/"+k.Import.ID])
	case cur == nil:
		return nil, fmt.Sprintf("%s: import: no %s key with id %s", k.Name, k.Provider, k.Import.ID)
	case cur.Name != k.Import.Name:
		return nil, fmt.Sprintf("%s: import: key %s is named %q at the provider, not %q", k.Name, short(k.Import.ID), cur.Name, k.Import.Name)
	}
	return cur, ""
}

// planTracked plans a key with track_only: true, or a key whose state is tracked. A tracked key gets only state
// changes: no limit, secret, consumer, disable or rotation action.
func (e *Engine) planTracked(plan *Plan, act Action, keys []provider.Key, known map[string]string) {
	k, st := act.Key, act.State
	warn := func(format string, args ...any) {
		plan.Warnings = append(plan.Warnings, k.Name+": "+fmt.Sprintf(format, args...))
	}
	switch {
	case st != nil && !st.TrackOnly:
		// A managed key has secrets, consumers and old keys. orkeys does not drop them silently.
		warn("track_only: true on a managed key; remove track_only, or add a key with a new name instead")
		return
	case !k.TrackOnly():
		warn("track_only removed from a tracked key; to manage the key, delete the manifest and apply (the key stays), " +
			"write the key to secret.path, then add the manifest with import and without track_only")
		return
	case k.Import == nil:
		warn("import: required with track_only: true")
		return
	case st == nil || st.KeyID != k.Import.ID:
		cur, w := importTarget(k, keys, known)
		if w != "" {
			plan.Warnings = append(plan.Warnings, w)
			return
		}
		act.Type, act.Current = ActTrack, cur
		act.Detail = fmt.Sprintf("track %s key %s (%s), budget $%.2f monthly; no change at the provider", k.Kind, short(cur.ID), cur.Name, k.Budget.Limit)
		if st != nil {
			act.Detail = fmt.Sprintf("tracked key %s -> %s (%s); no change at the provider", short(st.KeyID), short(cur.ID), cur.Name)
		}
		plan.Actions = append(plan.Actions, act)
		return
	}
	if provider.Find(keys, st.KeyID) == nil {
		warn("tracked key %s is missing at the provider; change import.id or delete the manifest", short(st.KeyID))
	}
	if d := metaDiff(st, e.stateFor(k)); d != "" {
		act.Type, act.Detail = ActUpdateState, d
		plan.Actions = append(plan.Actions, act)
	}
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
