package exporter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

type StateLister interface {
	ListStates(ctx context.Context) ([]*state.State, error)
}

type Exporter struct {
	Store     StateLister
	Providers provider.Set
	Interval  time.Duration
	Log       io.Writer
	Now       func() time.Time

	mu       sync.RWMutex
	body     []byte
	ready    bool
	states   []*state.State
	stateAt  time.Time
	lists    map[string][]provider.Key
	pollOK   map[string]bool
	lastPoll map[string]time.Time
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
	if w.seen == nil {
		w.seen = map[string]bool{}
	}
	if !w.seen[name] {
		w.seen[name] = true
		fmt.Fprintf(&w.buf, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
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
		func(st *state.State, l []string, _ *provider.Key) {
			// A provider without a per-key limit does not enforce the budget, so there is no hard limit to show.
			if p, ok := x.Providers[st.Provider]; ok && p.Caps().PerKeyLimit {
				w.gauge("orkeys_key_hard_limit_usd", "Limit set at the provider in USD, per period (label reset).", l, st.LimitUSD)
			}
		},
		func(st *state.State, l []string, _ *provider.Key) {
			w.gauge("orkeys_key_expires_timestamp_seconds", "Key expiry time.", l, float64(st.ExpiresAt.Unix()))
		},
		func(st *state.State, l []string, k *provider.Key) {
			if k != nil && k.LastUsedAt != nil {
				w.gauge("orkeys_key_last_used_timestamp_seconds", "Last use of the key.", l, float64(k.LastUsedAt.Unix()))
			}
		},
		func(st *state.State, l []string, _ *provider.Key) {
			w.gauge("orkeys_key_rotation_state", "0 idle, 1 consumers not synced, 2 old key waits for revoke, 4 failed.", l, rotationValue(st))
		},
		func(st *state.State, l []string, k *provider.Key) {
			v := 0.0
			if k == nil {
				v = 1
			}
			w.gauge("orkeys_key_missing", "1 if the key in state is not found at the provider.", l, v)
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
	for _, n := range names {
		unmanaged := 0
		self := x.Providers[n].SelfID()
		for _, k := range x.lists[n] {
			if !known[n+"/"+k.ID] && k.ID != self {
				unmanaged++
			}
		}
		if x.pollOK[n] {
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
	if !x.stateAt.IsZero() {
		w.gauge("orkeys_state_last_load_timestamp_seconds", "Time of the last successful state load.", nil, float64(x.stateAt.Unix()))
	}
	return w.buf.Bytes()
}

func resetOf(st *state.State) string {
	if st.LimitReset == "" {
		return "monthly"
	}
	return st.LimitReset
}
