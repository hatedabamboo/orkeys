package exporter

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

type states []*state.State

func (s states) ListStates(context.Context) ([]*state.State, error) { return s, nil }

type prov struct {
	keys []provider.Key
	err  error
	caps provider.Caps
}

func (p *prov) Name() string        { return "openrouter" }
func (p *prov) Caps() provider.Caps { return p.caps }
func (p *prov) SelfID() string      { return "self" }
func (p *prov) List(context.Context) ([]provider.Key, error) {
	return p.keys, p.err
}
func (p *prov) Create(context.Context, provider.Spec) (provider.Key, string, error) {
	return provider.Key{}, "", nil
}
func (p *prov) Delete(context.Context, string) error { return nil }

func TestMetrics(t *testing.T) {
	spend, month := 42.5, 99.0
	used := time.Unix(1700000000, 0)
	p := &prov{keys: []provider.Key{
		{ID: "k1", SpendWeekUSD: &spend, SpendMonthUSD: &month, LastUsedAt: &used},
		{ID: "old"}, {ID: "rogue"}, {ID: "self"},
	}, caps: provider.Caps{PerKeyLimit: true}}
	x := &Exporter{
		Store: states{{
			Name: "prod-a-b", Team: "a", Kind: "production", Env: "prod", Provider: "openrouter", KeyID: "k1",
			LimitUSD: 200, LimitReset: "weekly", ExpiresAt: time.Unix(1800000000, 0),
			NotifyChannel: "#a-notifications", AlertChannel: "#a-alerts",
			PendingRevokes: []state.PendingRevoke{{KeyID: "old"}},
		}},
		Providers: provider.Set{"openrouter": p},
		Log:       &strings.Builder{},
	}
	x.Poll(context.Background())
	rec := httptest.NewRecorder()
	x.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	labels := `{name="prod-a-b",team="a",kind="production",env="prod",provider="openrouter",service="",owner="",notify_channel="#a-notifications",alert_channel="#a-alerts",reset="weekly"}`
	for _, want := range []string{
		"orkeys_key_spend_usd" + labels + " 42.5",
		"orkeys_key_hard_limit_usd" + labels + " 200",
		"orkeys_key_budget_usd" + labels + " 200",
		"orkeys_key_expires_timestamp_seconds" + labels + " 1.8e+09",
		"orkeys_key_last_used_timestamp_seconds" + labels + " 1.7e+09",
		"orkeys_key_rotation_state" + labels + " 2",
		`orkeys_unmanaged_keys{provider="openrouter"} 1`,
		`orkeys_provider_poll_success{provider="openrouter"} 1`,
		"# TYPE orkeys_key_spend_usd gauge",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "# TYPE orkeys_key_hard_limit_usd") != 1 {
		t.Error("duplicate TYPE line")
	}

	p.err = errors.New("down")
	x.Poll(context.Background())
	rec = httptest.NewRecorder()
	x.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out = rec.Body.String()
	if !strings.Contains(out, `orkeys_provider_poll_success{provider="openrouter"} 0`) || strings.Contains(out, "orkeys_key_spend_usd{") {
		t.Errorf("on failure want poll_success 0 and no key series:\n%s", out)
	}
}

func TestMetricsWithoutProviderLimit(t *testing.T) {
	month := 12.0
	x := &Exporter{
		Store:     states{{Name: "prod-a-c", Provider: "openai", KeyID: "k1", LimitUSD: 50, ExpiresAt: time.Unix(1800000000, 0)}},
		Providers: provider.Set{"openai": &prov{keys: []provider.Key{{ID: "k1", SpendMonthUSD: &month}}}},
		Log:       &strings.Builder{},
	}
	x.Poll(context.Background())
	rec := httptest.NewRecorder()
	x.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	if !strings.Contains(out, `orkeys_key_budget_usd{name="prod-a-c"`) || !strings.Contains(out, `orkeys_key_spend_usd{name="prod-a-c"`) {
		t.Errorf("want budget and spend:\n%s", out)
	}
	if strings.Contains(out, "orkeys_key_hard_limit_usd") {
		t.Errorf("no hard limit for a provider without per-key limits:\n%s", out)
	}
}

func TestMetricsTrackedKey(t *testing.T) {
	month, limit := 12.0, 30.0
	x := &Exporter{
		Store: states{
			{Name: "prod-a-t", Provider: "deepinfra", KeyID: "t1", LimitUSD: 50, LimitReset: "monthly", TrackOnly: true},
			{Name: "prod-a-u", Provider: "deepinfra", KeyID: "t2", LimitUSD: 50, LimitReset: "monthly", TrackOnly: true},
		},
		Providers: provider.Set{"deepinfra": &prov{keys: []provider.Key{
			{ID: "t1", SpendMonthUSD: &month, LimitUSD: &limit},
			{ID: "t2", SpendMonthUSD: &month},
		}, caps: provider.Caps{PerKeyLimit: true}}},
		Log: &strings.Builder{},
	}
	x.Poll(context.Background())
	rec := httptest.NewRecorder()
	x.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		`orkeys_key_spend_usd{name="prod-a-t"`, `orkeys_key_budget_usd{name="prod-a-t"`,
		`orkeys_key_spend_usd{name="prod-a-u"`, `orkeys_key_missing{name="prod-a-t"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `reset="monthly"} 30`) || strings.Contains(out, `orkeys_key_hard_limit_usd{name="prod-a-u"`) {
		t.Errorf("hard limit of a tracked key must come from the provider:\n%s", out)
	}
	if strings.Contains(out, "orkeys_key_expires_timestamp_seconds") {
		t.Errorf("no expiry for a tracked key without one:\n%s", out)
	}
	if !strings.Contains(out, `orkeys_unmanaged_keys{provider="deepinfra"} 0`) {
		t.Errorf("tracked keys are not unmanaged:\n%s", out)
	}
}

func TestUnmanagedMetric(t *testing.T) {
	scrape := func(pol *manifest.Policy) string {
		x := &Exporter{
			Store: states{{Name: "prod-a-b", Provider: "openrouter", KeyID: "k1"}},
			Providers: provider.Set{"openrouter": &prov{keys: []provider.Key{
				{ID: "k1"}, {ID: "rogue"}, {ID: "known-rogue"}, {ID: "off", Disabled: true},
			}}},
			Policy: pol,
			Log:    &strings.Builder{},
		}
		x.Poll(context.Background())
		rec := httptest.NewRecorder()
		x.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		return rec.Body.String()
	}
	yes, no := true, false
	withOptions := func(o manifest.ProviderOptions) *manifest.Policy {
		return &manifest.Policy{TrackDisabled: true, Providers: manifest.Providers{"openrouter": {Options: o}}}
	}
	for name, c := range map[string]struct {
		pol  *manifest.Policy
		want string
	}{
		"no policy":       {nil, `orkeys_unmanaged_keys{provider="openrouter"} 2`},
		"track_disabled":  {&manifest.Policy{TrackDisabled: true}, `orkeys_unmanaged_keys{provider="openrouter"} 3`},
		"ignore_disabled": {withOptions(manifest.ProviderOptions{IgnoreDisabled: &yes}), `orkeys_unmanaged_keys{provider="openrouter"} 2`},
		"ignore list":     {withOptions(manifest.ProviderOptions{IgnoreUnmanaged: []string{"known-rogue"}}), `orkeys_unmanaged_keys{provider="openrouter"} 2`},
		"report false":    {withOptions(manifest.ProviderOptions{ReportUnmanaged: &no}), ""},
	} {
		out := scrape(c.pol)
		if c.want == "" {
			if strings.Contains(out, "orkeys_unmanaged_keys") {
				t.Errorf("%s: want no unmanaged metric:\n%s", name, out)
			}
			continue
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("%s: want %q in:\n%s", name, c.want, out)
		}
	}
}

type beatStore struct{ states }

func (beatStore) Heartbeats(context.Context) (map[string]time.Time, error) {
	return map[string]time.Time{"rotate": time.Unix(1800000000, 0)}, nil
}

func (beatStore) PutHeartbeat(context.Context, string, time.Time) error { return nil }

func TestHeartbeatMetric(t *testing.T) {
	x := &Exporter{Store: beatStore{}, Providers: provider.Set{}, Log: &strings.Builder{}}
	x.Poll(context.Background())
	rec := httptest.NewRecorder()
	x.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if want := `orkeys_last_success_timestamp_seconds{operation="rotate"} 1.8e+09`; !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("want %q in:\n%s", want, rec.Body.String())
	}
}
