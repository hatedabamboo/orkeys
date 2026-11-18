package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/elevenlabs"
)

type fakeElevenLabs struct {
	mu        sync.Mutex
	calls     []string
	auth      string
	delStatus int
	keys      string
}

func (f *fakeElevenLabs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	f.auth = r.Header.Get("xi-api-key")
	const base = "/v1/service-accounts/sa_1/api-keys"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == base:
		_, _ = io.WriteString(w, f.keys)
	case r.Method == http.MethodPost && r.URL.Path == base:
		_, _ = io.WriteString(w, `{"xi-api-key":"sk_new","key_id":"k3"}`)
	case r.Method == http.MethodDelete && f.delStatus != 0:
		w.WriteHeader(f.delStatus)
	case strings.HasPrefix(r.URL.Path, base+"/"):
		_, _ = io.WriteString(w, `null`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

const elevenLabsKeys = `{"api-keys":[
	{"name":"alpha","hint":"ab12","key_id":"k1","service_account_user_id":"sa_1","hashed_xi_api_key":"h1","created_at_unix":1793520000,
	 "is_disabled":false,"permissions":["text_to_speech"],"character_limit":100000,"character_count":2500,"expires_at_unix":1801296000},
	{"name":"beta","hint":"cd34","key_id":"k2","service_account_user_id":"sa_1","hashed_xi_api_key":"h2",
	 "is_disabled":true,"disable_reason":"exposed_publicly","permissions":null,"character_limit":null,"character_count":null,"expires_at_unix":null}]}`

var elevenLabsNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func newTestElevenLabs(t *testing.T, f *fakeElevenLabs) *elevenLabs {
	t.Helper()
	if f.keys == "" {
		f.keys = elevenLabsKeys
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := newElevenLabs(Config{BaseURL: srv.URL, Key: "mgmt", Timeout: time.Second,
		Options: map[string]string{OptServiceAccount: "sa_1", OptUSDPerCredit: "0.0002"}})
	p.now = func() time.Time { return elevenLabsNow }
	return p
}

func TestElevenLabsConfig(t *testing.T) {
	cases := map[string]struct {
		cfg  Config
		want string
	}{
		"no service account": {Config{Options: map[string]string{OptUSDPerCredit: "0.0002"}}, "option service_account (or workspace) is not set"},
		"no price":           {Config{Workspace: "sa_1"}, "option usd_per_credit is not set"},
		"bad price":          {Config{Workspace: "sa_1", Options: map[string]string{OptUSDPerCredit: "-1"}}, `option usd_per_credit: "-1" is not a positive number`},
		"text price":         {Config{Workspace: "sa_1", Options: map[string]string{OptUSDPerCredit: "cheap"}}, "is not a positive number"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := newElevenLabs(c.cfg)
			if _, err := p.List(context.Background()); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("list: %v, want %q", err, c.want)
			}
			if _, _, err := p.Create(context.Background(), Spec{Name: "x"}); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("create: %v, want %q", err, c.want)
			}
		})
	}
	if p := newElevenLabs(Config{Workspace: "sa_1", Options: map[string]string{OptUSDPerCredit: "0.0002"}}); p.cfgErr != nil {
		t.Fatalf("workspace as service account: %v", p.cfgErr)
	}
}

func TestElevenLabsList(t *testing.T) {
	f := &fakeElevenLabs{}
	p := newTestElevenLabs(t, f)
	keys, err := p.List(context.Background())
	if err != nil || len(keys) != 2 {
		t.Fatal(keys, err)
	}
	if f.auth != "mgmt" {
		t.Fatalf("xi-api-key header: %q", f.auth)
	}
	k1, k2 := keys[0], keys[1]
	if k1.ID != "k1" || k1.Name != "alpha" || k1.Disabled || k1.LimitReset != "monthly" {
		t.Fatalf("k1: %+v", k1)
	}
	if !near(*k1.LimitUSD, 20) || !near(*k1.SpendMonthUSD, 0.5) || !near(*k1.UsageUSD, 0.5) || k1.SpendDayUSD != nil {
		t.Fatalf("k1 money: limit %v month %v usage %v", *k1.LimitUSD, *k1.SpendMonthUSD, *k1.UsageUSD)
	}
	if k1.CreatedAt.IsZero() || k1.ExpiresAt == nil || k1.ExpiresAt.Year() != 2027 || k1.LastUsedAt != nil {
		t.Fatalf("k1 times: %+v", k1)
	}
	if !k2.Disabled || k2.LimitUSD != nil || k2.LimitReset != "" || *k2.SpendMonthUSD != 0 || k2.ExpiresAt != nil || !k2.CreatedAt.IsZero() {
		t.Fatalf("k2: %+v", k2)
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func TestElevenLabsCreate(t *testing.T) {
	f := &fakeElevenLabs{}
	p := newTestElevenLabs(t, f)
	limit := 10.00003
	exp := elevenLabsNow.Add(30 * 24 * time.Hour)
	k, secret, err := p.Create(context.Background(), Spec{Name: "ci", LimitUSD: &limit, LimitReset: "monthly", ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	if k.ID != "k3" || secret != "sk_new" || !near(*k.LimitUSD, 10) || k.ExpiresAt == nil || !k.ExpiresAt.Equal(exp) {
		t.Fatalf("key: %+v", k)
	}
	want := `POST /v1/service-accounts/sa_1/api-keys {"name":"ci","permissions":["text_to_speech","speech_to_speech","speech_to_text","voices_read","models_read"],"character_limit":50000,"expires_at_unix":1797595200}`
	if got := f.calls[len(f.calls)-1]; got != want {
		t.Fatalf("request:\n got %s\nwant %s", got, want)
	}

	for name, spec := range map[string]Spec{
		"scope all":         {Name: "x", Scopes: []string{"text_to_speech", "all"}},
		"service account":   {Name: "x", Scopes: []string{"service_account_write"}},
		"expiry too soon":   {Name: "x", ExpiresAt: ptr(elevenLabsNow.Add(10 * time.Minute))},
		"expiry after 90 d": {Name: "x", ExpiresAt: ptr(elevenLabsNow.Add(91 * 24 * time.Hour))},
	} {
		n := len(f.calls)
		if _, _, err := p.Create(context.Background(), spec); err == nil {
			t.Fatalf("%s: no error", name)
		}
		if len(f.calls) != n {
			t.Fatalf("%s: request sent: %v", name, f.calls[n:])
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestElevenLabsUpdates(t *testing.T) {
	f := &fakeElevenLabs{}
	p := newTestElevenLabs(t, f)
	ctx := context.Background()
	five := 5.0
	steps := []struct {
		run  func() error
		want string
	}{
		{func() error { return p.SetLimit(ctx, "k1", &five, "monthly") }, `PATCH /v1/service-accounts/sa_1/api-keys/k1 {"character_limit":25000}`},
		{func() error { return p.SetLimit(ctx, "k1", nil, "") }, `PATCH /v1/service-accounts/sa_1/api-keys/k1 {"character_limit":"clear"}`},
		{func() error { return p.Disable(ctx, "k1") }, `POST /v1/service-accounts/sa_1/api-keys/k1/disable`},
		{func() error { return p.Enable(ctx, "k1", nil, "") }, `PATCH /v1/service-accounts/sa_1/api-keys/k1 {"is_enabled":true}`},
		{func() error { return p.Enable(ctx, "k1", &five, "monthly") }, `PATCH /v1/service-accounts/sa_1/api-keys/k1 {"character_limit":25000,"is_enabled":true}`},
		{func() error { return p.Delete(ctx, "k1") }, `DELETE /v1/service-accounts/sa_1/api-keys/k1`},
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			t.Fatalf("%s: %v", s.want, err)
		}
		if got := strings.TrimSpace(f.calls[len(f.calls)-1]); got != s.want {
			t.Fatalf("request:\n got %s\nwant %s", got, s.want)
		}
	}
}

func TestElevenLabsEnableExposed(t *testing.T) {
	f := &fakeElevenLabs{}
	p := newTestElevenLabs(t, f)
	err := p.Enable(context.Background(), "k2", nil, "")
	if err == nil || !strings.Contains(err.Error(), "key k2: elevenlabs disabled it because it was public") {
		t.Fatalf("enable: %v", err)
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasPrefix(last, "GET ") {
		t.Fatalf("request after the refusal: %s", last)
	}
}

func TestElevenLabsSelfID(t *testing.T) {
	f := &fakeElevenLabs{keys: strings.Replace(elevenLabsKeys, `"h2"`, `"`+strings.ToUpper(elevenlabs.KeyHash("mgmt"))+`"`, 1)}
	p := newTestElevenLabs(t, f)
	if p.SelfID() != "" {
		t.Fatalf("self ID before list: %q", p.SelfID())
	}
	if _, err := p.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.SelfID() != "k2" {
		t.Fatalf("self ID after list: %q, want k2", p.SelfID())
	}
	cfg := newElevenLabs(Config{Key: "mgmt", SelfID: "k9", Workspace: "sa_1", Options: map[string]string{OptUSDPerCredit: "1"}})
	if cfg.SelfID() != "k9" || cfg.selfHash != "" {
		t.Fatalf("configured self ID: %q, hash %q", cfg.SelfID(), cfg.selfHash)
	}
}

func TestElevenLabsDeleteMissing(t *testing.T) {
	cases := map[string]struct {
		status  int
		id      string
		wantErr bool
	}{
		"404": {http.StatusNotFound, "k9", false},
		"422": {http.StatusUnprocessableEntity, "k9", true},
		"500": {http.StatusInternalServerError, "k9", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeElevenLabs{delStatus: c.status}
			p := newTestElevenLabs(t, f)
			if err := p.Delete(context.Background(), c.id); (err != nil) != c.wantErr {
				t.Fatalf("delete: %v, want error %t", err, c.wantErr)
			}
		})
	}
}

func TestCheckElevenLabsScopes(t *testing.T) {
	if err := checkElevenLabsScopes([]string{"text_to_speech", "voices_read"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range deniedElevenLabsScopes {
		if err := checkElevenLabsScopes([]string{s}); err == nil {
			t.Fatalf("%s: no error", s)
		}
	}
}
