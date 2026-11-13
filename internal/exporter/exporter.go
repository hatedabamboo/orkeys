package exporter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

type StateLister interface {
	ListStates(ctx context.Context) ([]*state.State, error)
}

// PolicySource returns the copy of policy.yaml in the state store.
type PolicySource interface {
	GetPolicy(ctx context.Context) (*state.StoredPolicy, error)
}

type Exporter struct {
	Store     StateLister
	Providers provider.Set
	// Policy sets the report of unmanaged keys (providers[].options in policy.yaml). Nil means the defaults.
	Policy *manifest.Policy
	// PolicyFromStore: each poll reads Policy again from the copy in Store. Store must be a PolicySource.
	// A new provider in that copy needs a restart: the exporter makes the provider clients at start.
	PolicyFromStore bool
	// Version is the orkeys version for orkeys_exporter_build_info.
	Version  string
	Interval time.Duration
	Log      io.Writer
	Now      func() time.Time

	mu       sync.RWMutex
	body     []byte
	ready    bool
	states   []*state.State
	stateAt  time.Time
	lists    map[string][]provider.Key
	pollOK   map[string]bool
	lastPoll map[string]time.Time
	beats    map[string]time.Time
	policyAt time.Time
}

func (x *Exporter) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

func (x *Exporter) logf(format string, args ...any) {
	w := x.Log
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, format+"\n", args...)
}

func (x *Exporter) Run(ctx context.Context) {
	interval := x.Interval
	if interval == 0 {
		interval = 10 * time.Minute
	}
	x.Poll(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			x.Poll(ctx)
		}
	}
}

func (x *Exporter) Poll(ctx context.Context) {
	x.mu.Lock()
	if x.lists == nil {
		x.lists, x.pollOK, x.lastPoll = map[string][]provider.Key{}, map[string]bool{}, map[string]time.Time{}
	}
	x.mu.Unlock()

	states, err := x.Store.ListStates(ctx)
	now := x.now()
	x.mu.Lock()
	if err != nil {
		x.logf("load states: %v", err)
	} else {
		x.states, x.stateAt = states, now
	}
	x.mu.Unlock()

	if x.PolicyFromStore {
		x.loadPolicy(ctx)
	}

	if hb, ok := x.Store.(state.Heartbeats); ok {
		beats, err := hb.Heartbeats(ctx)
		x.mu.Lock()
		if err != nil {
			x.logf("load heartbeats: %v", err)
		} else {
			x.beats = beats
		}
		x.mu.Unlock()
	}

	for name, p := range x.Providers {
		keys, err := p.List(ctx)
		x.mu.Lock()
		x.pollOK[name] = err == nil
		if err != nil {
			x.logf("%s: %v", name, err)
		} else {
			x.lists[name], x.lastPoll[name] = keys, x.now()
		}
		x.mu.Unlock()
	}

	x.mu.Lock()
	x.body = x.render()
	x.ready = true
	x.mu.Unlock()
}

// loadPolicy reads the stored policy. On an error it keeps the last policy, and the load time shows the age.
func (x *Exporter) loadPolicy(ctx context.Context) {
	ps, ok := x.Store.(PolicySource)
	if !ok {
		x.logf("load policy: the state store keeps no policy")
		return
	}
	sp, err := ps.GetPolicy(ctx)
	if err == nil && sp == nil {
		err = errors.New("no policy.yaml in the state store; run apply to save it")
	}
	var pol *manifest.Policy
	if err == nil {
		pol, err = manifest.ParsePolicy([]byte(sp.YAML))
	}
	if err != nil {
		x.logf("load policy: %v", err)
		return
	}
	for name := range pol.Providers {
		if _, ok := x.Providers[name]; !ok {
			x.logf("load policy: provider %s is new; restart the exporter to export its keys", name)
		}
	}
	x.mu.Lock()
	x.Policy, x.policyAt = pol, x.now()
	x.mu.Unlock()
}

func (x *Exporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	switch r.URL.Path {
	case "/metrics":
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write(x.body)
	case "/healthz":
		if !x.ready {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	default:
		http.NotFound(w, r)
	}
}

type writer struct {
	buf  bytes.Buffer
	seen map[string]bool
}

func (w *writer) gauge(name, help string, labels []string, v float64) {
	w.sample("gauge", name, help, labels, v)
}

func (w *writer) counter(name, help string, labels []string, v float64) {
	w.sample("counter", name, help, labels, v)
}

func (w *writer) sample(typ, name, help string, labels []string, v float64) {
	if w.seen == nil {
		w.seen = map[string]bool{}
	}
	if !w.seen[name] {
		w.seen[name] = true
		fmt.Fprintf(&w.buf, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	w.buf.WriteString(name)
	if len(labels) > 0 {
		w.buf.WriteByte('{')
		for i := 0; i < len(labels); i += 2 {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			fmt.Fprintf(&w.buf, "%s=%q", labels[i], escape(labels[i+1]))
		}
		w.buf.WriteByte('}')
	}
	fmt.Fprintf(&w.buf, " %g\n", v)
}

func escape(s string) string { return strings.ReplaceAll(s, "\n", " ") }

func rotationValue(st *state.State) float64 {
	switch {
	case st.Rotation == state.RotationFailed:
		return 4
	case st.Rotation == state.RotationPending:
		return 1
	case len(st.PendingRevokes) > 0:
		return 2
	}
	return 0
}

func (x *Exporter) render() []byte {
	var w writer
	known := map[string]bool{}
	byMetric := []func(st *state.State, l []string, k *provider.Key){
		func(st *state.State, l []string, k *provider.Key) {
			if k == nil {
				return
			}
			if s := k.SpendFor(st.LimitReset); s != nil {
				w.gauge("orkeys_key_spend_usd", "Spend in the current limit period (label reset) in USD.", l, *s)
			}
		},
		func(st *state.State, l []string, _ *provider.Key) {
			w.gauge("orkeys_key_budget_usd", "Budget from the manifest in USD, per period (label reset).", l, st.LimitUSD)
		},
		func(st *state.State, l []string, k *provider.Key) {
			const help = "Limit at the provider in USD, per period (label reset)."
			// A provider without a per-key limit does not enforce the budget, so there is no hard limit to show.
			// A tracked key can have a limit that someone set at the provider.
			p, ok := x.Providers[st.Provider]
			if !st.TrackOnly && (!ok || !p.Caps().PerKeyLimit) {
				return
			}
			// The value from the provider shows a limit that someone changed outside orkeys. No value means no
			// limit: the key has no series, and the alerts for keys without a hard limit apply.
			if k != nil {
				if k.LimitUSD != nil {
					w.gauge("orkeys_key_hard_limit_usd", help, l, *k.LimitUSD)
				}
				return
			}
			if !st.TrackOnly {
				w.gauge("orkeys_key_hard_limit_usd", help, l, st.LimitUSD)
			}
		},
		func(st *state.State, l []string, k *provider.Key) {
			// The provider value is a counter of the current key. A new key after a rotation and the start of a
			// month at DeepInfra and OpenAI reset it. rate() and increase() handle the reset.
			if k != nil && k.UsageUSD != nil {
				w.counter("orkeys_key_usage_usd_total", "Usage of the key in USD as the provider reports it. Resets at rotation; DeepInfra and OpenAI reset it monthly.", l, *k.UsageUSD)
			}
		},
		func(st *state.State, l []string, _ *provider.Key) {
			// A tracked key without a provider expiry has none. A zero time would fire expiry alerts.
			if !st.ExpiresAt.IsZero() {
				w.gauge("orkeys_key_expires_timestamp_seconds", "Key expiry time.", l, float64(st.ExpiresAt.Unix()))
			}
		},
		func(st *state.State, l []string, k *provider.Key) {
			if k != nil && k.LastUsedAt != nil {
				w.gauge("orkeys_key_last_used_timestamp_seconds", "Last use of the key.", l, float64(k.LastUsedAt.Unix()))
			}
		},
		func(st *state.State, l []string, k *provider.Key) {
			// The state time of an imported key is the time of the import. The provider knows the real time.
			t := st.CreatedAt
			if k != nil && k.CreatedAt.Unix() > 0 {
				t = k.CreatedAt
			}
			if !t.IsZero() {
				w.gauge("orkeys_key_created_timestamp_seconds", "Creation time of the key.", l, float64(t.Unix()))
			}
		},
		func(st *state.State, l []string, _ *provider.Key) {
			w.gauge("orkeys_key_rotation_state", "0 idle, 1 consumers not synced, 2 old key waits for revoke, 4 failed.", l, rotationValue(st))
		},
		func(st *state.State, l []string, _ *provider.Key) {
			var first time.Time
			for _, pr := range st.PendingRevokes {
				if !pr.After.IsZero() && (first.IsZero() || pr.After.Before(first)) {
					first = pr.After
				}
			}
			if first.IsZero() {
				return
			}
			w.gauge("orkeys_key_pending_revoke_timestamp_seconds", "Earliest planned revoke time of an old key. rotate holds the revoke while the old key is in use.", l, float64(first.Unix()))
		},
		func(st *state.State, l []string, k *provider.Key) {
			v := 0.0
			if k == nil {
				v = 1
			}
			w.gauge("orkeys_key_missing", "1 if the key in state is not found at the provider.", l, v)
		},
		func(st *state.State, l []string, k *provider.Key) {
			if k == nil {
				return
			}
			v := 0.0
			switch {
			case k.Disabled && st.Disabled:
				v = 1
			case k.Disabled:
				v = 2
			}
			w.gauge("orkeys_key_disabled", "0 enabled, 1 disabled by the manifest (disabled: true), 2 disabled at the provider and not in the manifest.", l, v)
		},
		// A separate series and not a label: a new label on the other series changes their identity.
		func(st *state.State, l []string, _ *provider.Key) {
			v := 0.0
			if st.TrackOnly {
				v = 1
			}
			w.gauge("orkeys_key_track_only", "1 if orkeys only tracks the key (track_only: true) and does not rotate it.", l, v)
		},
	}

	var active []*state.State
	for _, st := range x.states {
		for _, id := range st.KnownIDs() {
			known[st.Provider+"/"+id] = true
		}
		if x.pollOK[st.Provider] {
			active = append(active, st)
		}
	}
	for _, metric := range byMetric {
		for _, st := range active {
			l := []string{"name", st.Name, "team", st.Team, "kind", st.Kind, "env", st.Env, "provider", st.Provider,
				"service", st.Service, "owner", st.Owner, "notify_channel", st.NotifyChannel, "alert_channel", st.AlertChannel, "reset", resetOf(st)}
			metric(st, l, provider.Find(x.lists[st.Provider], st.KeyID))
		}
	}

	names := make([]string, 0, len(x.Providers))
	for n := range x.Providers {
		names = append(names, n)
	}
	slices.Sort(names)
	pol := x.Policy
	if pol == nil {
		pol = &manifest.Policy{}
	}
	for _, n := range names {
		unmanaged := 0
		self := x.Providers[n].SelfID()
		for _, k := range x.lists[n] {
			if !known[n+"/"+k.ID] && k.ID != self && !pol.IgnoresUnmanaged(n, k) {
				unmanaged++
			}
		}
		if x.pollOK[n] && pol.ReportUnmanaged(n) {
			w.gauge("orkeys_unmanaged_keys", "Provider keys that orkeys does not manage.", []string{"provider", n}, float64(unmanaged))
		}
	}
	for _, n := range names {
		v := 0.0
		if x.pollOK[n] {
			v = 1
		}
		w.gauge("orkeys_provider_poll_success", "1 if the last poll of the provider worked.", []string{"provider", n}, v)
	}
	for _, n := range names {
		if t, ok := x.lastPoll[n]; ok {
			w.gauge("orkeys_provider_last_poll_timestamp_seconds", "Time of the last successful poll.", []string{"provider", n}, float64(t.Unix()))
		}
	}
	for _, op := range slices.Sorted(maps.Keys(x.beats)) {
		w.gauge("orkeys_last_success_timestamp_seconds", "Time of the last successful run of an operation (apply, rotate, hygiene).",
			[]string{"operation", op}, float64(x.beats[op].Unix()))
	}
	if !x.stateAt.IsZero() {
		w.gauge("orkeys_state_last_load_timestamp_seconds", "Time of the last successful state load.", nil, float64(x.stateAt.Unix()))
	}
	if x.Version != "" {
		w.gauge("orkeys_exporter_build_info", "Always 1. The label version is the orkeys version of the exporter.", []string{"version", x.Version}, 1)
	}
	if x.PolicyFromStore && !x.policyAt.IsZero() {
		w.gauge("orkeys_policy_last_load_timestamp_seconds", "Time of the last successful load of policy.yaml from the state store.", nil, float64(x.policyAt.Unix()))
	}
	return w.buf.Bytes()
}

func resetOf(st *state.State) string {
	if st.LimitReset == "" {
		return "monthly"
	}
	return st.LimitReset
}
