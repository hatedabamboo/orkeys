package exporter

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

type states []*state.State

func (s states) ListStates(context.Context) ([]*state.State, error) { return s, nil }

type prov struct {
	keys []provider.Key
	err  error
}

func (p *prov) Name() string        { return "openrouter" }
func (p *prov) Caps() provider.Caps { return provider.Caps{} }
func (p *prov) SelfID() string      { return "self" }
func (p *prov) List(context.Context) ([]provider.Key, error) {
	return p.keys, p.err
}
func (p *prov) Create(context.Context, provider.Spec) (provider.Key, string, error) {
	return provider.Key{}, "", nil
}
func (p *prov) SetLimit(context.Context, string, *float64, string) error { return nil }
func (p *prov) Disable(context.Context, string) error                    { return nil }
func (p *prov) Enable(context.Context, string) error                     { return nil }
func (p *prov) Delete(context.Context, string) error                     { return nil }

func TestMetrics(t *testing.T) {
	spend, month := 42.5, 99.0
	used := time.Unix(1700000000, 0)
	p := &prov{keys: []provider.Key{
		{ID: "k1", SpendWeekUSD: &spend, SpendMonthUSD: &month, LastUsedAt: &used},
		{ID: "old"}, {ID: "rogue"}, {ID: "self"},
	}}
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
