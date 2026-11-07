package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/state"
)

type env struct {
	e      *Engine
	or     *fakeProvider
	di     *fakeProvider
	store  *fakeStore
	kube   *fakeKube
	events *EventLog
	dlv    *fakeDeliverer
	clk    *clock
	root   string
}

func setup(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	src := "../manifest/testdata/repo"
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(root, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(root, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	v := &env{
		or:     &fakeProvider{name: "openrouter", caps: provider.Caps{NativeExpiry: true, PerKeyLimit: true, LimitReset: true, LastUsed: true}},
		di:     &fakeProvider{name: "deepinfra", caps: provider.Caps{PerKeyLimit: true, Scopes: true}},
		store:  newStore(),
		kube:   &fakeKube{},
		events: &EventLog{},
		dlv:    &fakeDeliverer{},
		clk:    &clock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)},
		root:   root,
	}
	v.e = &Engine{
		Providers: provider.Set{"openrouter": v.or, "deepinfra": v.di},
		State:     v.store, Secrets: v.store, Kube: v.kube, Events: v.events, Deliverer: v.dlv,
		Org: &fakeOrg{members: map[string]bool{"jdoe": true, "asmith": true}},
		Now: v.clk.now, Log: &strings.Builder{}, RetryDelay: time.Microsecond,
	}
	v.reload(t)
	return v
}

func (v *env) reload(t *testing.T) {
	t.Helper()
	inv, err := manifest.Load(v.root)
	if err != nil {
		t.Fatal(err)
	}
	if ps := inv.Validate(); len(ps) > 0 {
		t.Fatalf("invalid inventory: %v", ps)
	}
	v.e.Inv = inv
}

func (v *env) plan(t *testing.T) *Plan {
	t.Helper()
	p, err := v.e.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (v *env) apply(t *testing.T) {
	t.Helper()
	if err := v.e.Apply(context.Background(), v.plan(t)); err != nil {
		t.Fatal(err)
	}
}

func (v *env) rotate(t *testing.T, opt RotateOptions) error {
	t.Helper()
	return v.e.Rotate(context.Background(), opt)
}

func (v *env) edit(t *testing.T, file, old, new string) {
	t.Helper()
	p := filepath.Join(v.root, file)
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s: %q not found", file, old)
	}
	_ = os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644)
	v.reload(t)
}

func types(p *Plan) string {
	var s []string
	for _, a := range p.Actions {
		s = append(s, string(a.Type)+":"+a.Name)
	}
	return strings.Join(s, " ")
}

const prodKey = "prod-chat-core-dialog"
const stagingKey = "staging-chat-core-evals"

func TestCreateAndConverge(t *testing.T) {
	v := setup(t)
	if got := types(v.plan(t)); got != "create:"+prodKey+" create:"+stagingKey {
		t.Fatalf("plan: %s", got)
	}
	v.apply(t)

	if v.store.secrets["kv/llm/prod/chat-core/dialog#api_key"] == "" {
		t.Fatal("production secret not in Vault")
	}
	if v.store.secrets["kv/llm/staging/chat-core/evals#api_key"] == "" || len(v.di.keys) != 1 {
		t.Fatal("deepinfra key not created")
	}
	st := v.store.states[prodKey]
	if st.LimitUSD != 200 || st.AlertChannel != "#chat-core-alerts" || !st.ExpiresAt.Equal(v.clk.t.Add(days(90))) {
		t.Fatalf("state: %+v", st)
	}
	if lim := v.or.keys[0].LimitUSD; lim == nil || *lim != 200 {
		t.Fatal("hard limit not set at provider")
	}
	if p := v.plan(t); !p.Empty() {
		t.Fatalf("second plan not empty: %s", types(p))
	}
}

func TestBudgetChangeAndDelete(t *testing.T) {
	v := setup(t)
	v.apply(t)
	v.edit(t, "teams/chat-core/"+prodKey+".yaml", "limit: 200", "limit: 300")
	if got := types(v.plan(t)); got != "update-limit:"+prodKey+" update-state:"+prodKey {
		t.Fatalf("plan: %s", got)
	}
	v.apply(t)
	if lim := v.or.keys[0].LimitUSD; *lim != 300 {
		t.Fatalf("limit %v", *lim)
	}
	if v.store.states[prodKey].LimitUSD != 300 {
		t.Fatal("limit not saved")
	}

	_ = os.Remove(filepath.Join(v.root, "teams/chat-core/"+prodKey+".yaml"))
	v.reload(t)
	if got := types(v.plan(t)); got != "delete:"+prodKey {
		t.Fatalf("plan: %s", got)
	}
	v.apply(t)
	if len(v.or.keys) != 0 || v.store.secrets["kv/llm/prod/chat-core/dialog#api_key"] != "" {
		t.Fatalf("not removed: keys %v secrets %v", v.or.keys, v.store.secrets)
	}
}

func TestRecreateMissingKey(t *testing.T) {
	v := setup(t)
	v.apply(t)
	v.or.keys = nil
	if got := types(v.plan(t)); got != "recreate:"+prodKey {
		t.Fatalf("plan: %s", got)
	}
}

func TestUnmanagedAndImportCandidates(t *testing.T) {
	v := setup(t)
	v.apply(t)
	v.or.keys = append(v.or.keys, provider.Key{ID: "zzz", Name: "rogue"}, provider.Key{ID: "self"})
	p := v.plan(t)
	if len(p.Unmanaged) != 1 || p.Unmanaged[0].ID != "zzz" {
		t.Fatalf("unmanaged %v", p.Unmanaged)
	}
	c, err := v.e.Import(context.Background())
	if err != nil || len(c) != 1 || c[0].Key.ID != "zzz" || c[0].Provider != "openrouter" {
		t.Fatalf("import %+v %v", c, err)
	}
}

func TestRotation(t *testing.T) {
	v := setup(t)
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID

	v.clk.add(days(80))
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := v.store.states[prodKey]; st.KeyID != oldID {
		t.Fatal("rotated too early")
	}

	v.clk.add(days(4))
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	st := v.store.states[prodKey]
	if st.KeyID == oldID || st.Rotation != state.RotationIdle || len(st.PendingRevokes) != 1 {
		t.Fatalf("after start: %+v", st)
	}
	if want := "sync prod-eu/chat/dialog-llm restart prod-eu/chat/deployment/dialog"; strings.Join(v.kube.calls, " ") != want {
		t.Fatalf("kube calls: %v", v.kube.calls)
	}
	if v.store.secrets["kv/llm/prod/chat-core/dialog#api_key"] != "secret-2" {
		t.Fatal("new secret not written")
	}
	if v.or.find(oldID) == nil {
		t.Fatal("old key revoked before grace")
	}

	v.clk.add(time.Hour)
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(24 * time.Hour)
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if v.or.find(oldID) != nil || len(v.store.states[prodKey].PendingRevokes) != 0 {
		t.Fatal("old key not revoked after grace")
	}
}

func TestRotationSyncFailureAndRetry(t *testing.T) {
	v := setup(t)
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID
	v.kube.fail = context.DeadlineExceeded
	v.clk.add(days(85))
	_ = v.rotate(t, RotateOptions{})
	st := v.store.states[prodKey]
	if st.Rotation != state.RotationFailed || !hasAlert(v.events, EventRotationFailed, prodKey, "#chat-core-alerts") {
		t.Fatalf("want failed + alert: %+v %v", st, v.events.Events)
	}

	v.clk.add(days(2))
	_ = v.rotate(t, RotateOptions{})
	if v.or.find(oldID) == nil || len(v.or.keys) != 2 {
		t.Fatal("failed rotation must not revoke or start again")
	}

	v.kube.fail = nil
	if err := v.rotate(t, RotateOptions{Retry: []string{prodKey}}); err != nil {
		t.Fatal(err)
	}
	v.clk.add(time.Hour)
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(24 * time.Hour)
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if v.or.find(oldID) != nil {
		t.Fatal("old key not revoked after retry")
	}
}

func TestOldKeyStillUsed(t *testing.T) {
	v := setup(t)
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID
	v.clk.add(days(84))
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(time.Hour)
	_ = v.rotate(t, RotateOptions{})
	if b := v.store.states[prodKey].PendingRevokes[0].Baseline; b == nil || *b != 0 {
		t.Fatalf("baseline not recorded: %+v", v.store.states[prodKey].PendingRevokes)
	}
	used := 1.5
	v.or.find(oldID).UsageUSD = &used
	v.clk.add(24 * time.Hour)
	_ = v.rotate(t, RotateOptions{})
	if v.or.find(oldID) == nil || v.store.states[prodKey].Rotation != state.RotationFailed || !hasAlert(v.events, EventOldKeyInUse, prodKey, "#chat-core-alerts") {
		t.Fatal("old key in use must not be revoked")
	}
}

func TestRevokeWaitsForBaseline(t *testing.T) {
	v := setup(t)
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID
	v.clk.add(days(84))
	_ = v.rotate(t, RotateOptions{})
	v.clk.add(25 * time.Hour)
	_ = v.rotate(t, RotateOptions{})
	if v.or.find(oldID) == nil {
		t.Fatal("revoked without a usage baseline")
	}
	v.clk.add(time.Hour)
	_ = v.rotate(t, RotateOptions{})
	if v.or.find(oldID) != nil {
		t.Fatal("not revoked after an unchanged baseline")
	}
}

func TestRotateNow(t *testing.T) {
	v := setup(t)
	v.apply(t)
	oldID := v.store.states[prodKey].KeyID
	v.clk.add(days(10))
	if err := v.rotate(t, RotateOptions{Now: []string{prodKey}}); err != nil {
		t.Fatal(err)
	}
	st := v.store.states[prodKey]
	if st.KeyID == oldID || len(st.PendingRevokes) != 1 || !strings.Contains(st.PendingRevokes[0].Reason, "manual") {
		t.Fatalf("rotate now: %+v", st)
	}
	if err := v.rotate(t, RotateOptions{Now: []string{prodKey}}); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("want in-progress error, got %v", err)
	}
	if len(v.store.states[prodKey].PendingRevokes) != 1 {
		t.Fatal("a second rotate-now during a pending revoke must not start another rotation")
	}
}

func (v *env) request(t *testing.T, r Request) (*Issued, error) {
	t.Helper()
	return v.e.Request(context.Background(), r)
}

func TestPersonalKeys(t *testing.T) {
	v := setup(t)
	if _, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "deepinfra"}); err == nil || !strings.Contains(err.Error(), "purpose") {
		t.Fatalf("want purpose error, got %v", err)
	}
	if _, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "deepinfra", Purpose: "x", LimitUSD: 500}); err == nil {
		t.Fatal("limit over max accepted")
	}
	if _, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "deepinfra", Purpose: "x", TTL: days(30)}); err == nil {
		t.Fatal("ttl over max accepted")
	}

	a, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "deepinfra", Purpose: "persona evals"})
	if err != nil {
		t.Fatal(err)
	}
	if a.LimitUSD != 25 || !strings.HasPrefix(a.Name, "personal-jdoe-") || v.dlv.got["jdoe/"+a.Name] == "" {
		t.Fatalf("issued %+v", a)
	}
	st := v.store.states[a.Name]
	if st.Purpose != "persona evals" || st.Team != "chat-core" || st.AlertChannel != "#chat-core-notifications" {
		t.Fatalf("state %+v", st)
	}

	v.clk.add(time.Second)
	if _, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "openrouter", Purpose: "y", LimitUSD: 100}); err != nil {
		t.Fatal(err)
	}
	v.clk.add(time.Second)
	if _, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "openrouter", Purpose: "z", LimitUSD: 1}); err == nil || !strings.Contains(err.Error(), "limit is 2") {
		t.Fatalf("want max_active error, got %v", err)
	}

	if p := v.plan(t); len(p.Unmanaged) != 0 || types(p) != "create:"+prodKey+" create:"+stagingKey {
		t.Fatalf("self-service keys leaked into plan: %s %v", types(p), p.Unmanaged)
	}
	list, err := v.e.Issued(context.Background(), IssuedFilter{Owner: "JDOE"})
	if err != nil || len(list) != 2 || list[0].Purpose == "" {
		t.Fatalf("issued: %v %v", list, err)
	}

	v.clk.add(days(12))
	_ = v.rotate(t, RotateOptions{})
	if !v.store.states[a.Name].ExpiryNotified || !v.events.Has(EventExpiryNotice, "will not be renewed") {
		t.Fatal("no expiry notice")
	}
	v.clk.add(days(2))
	_ = v.rotate(t, RotateOptions{})
	if _, ok := v.store.states[a.Name]; ok || len(v.di.keys) != 0 {
		t.Fatal("personal key not removed at expiry")
	}
}

func TestLocalKey(t *testing.T) {
	v := setup(t)
	if _, err := v.request(t, Request{User: "stranger", Kind: "local", Provider: "openrouter", Purpose: "dev"}); err == nil {
		t.Fatal("non-member got a key")
	}
	is, err := v.request(t, Request{User: "ASmith", Kind: "local", Provider: "openrouter", Purpose: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(is.Name, "local-asmith-") || is.LimitUSD != 20 || v.dlv.got["ASmith/"+is.Name] == "" {
		t.Fatalf("issued %+v", is)
	}
	if p := v.plan(t); len(p.Unmanaged) != 0 {
		t.Fatal("local key counted as unmanaged")
	}
	v.clk.add(days(7))
	_ = v.rotate(t, RotateOptions{})
	if _, ok := v.store.states[is.Name]; ok || len(v.or.keys) != 0 {
		t.Fatal("local key not removed at expiry")
	}
}

func TestHygiene(t *testing.T) {
	v := setup(t)
	v.apply(t)
	pk, err := v.request(t, Request{User: "jdoe", Kind: "personal", Provider: "openrouter", Purpose: "x"})
	if err != nil {
		t.Fatal(err)
	}
	v.e.Org = &fakeOrg{members: map[string]bool{"asmith": true}}
	v.clk.t = time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)
	fs, err := v.e.Hygiene(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, f := range fs {
		kinds = append(kinds, f.Kind+":"+f.Name)
	}
	got := strings.Join(kinds, " ")
	for _, want := range []string{"owner-left:" + pk.Name, "unused:" + prodKey} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %s in %s", want, got)
		}
	}
	if _, ok := v.store.states[pk.Name]; ok {
		t.Fatal("hygiene did not act")
	}
}

func hasAlert(l *EventLog, typ, key, channel string) bool {
	for _, ev := range l.Events {
		if ev.Type == typ && ev.Key == key && ev.Level == LevelAlert && ev.Channel == channel {
			return true
		}
	}
	return false
}

func TestEventsCarryRoutingFields(t *testing.T) {
	v := setup(t)
	v.e.RunURL = "https://ci/run/1"
	v.apply(t)
	var issued []Event
	for _, ev := range v.events.Events {
		if ev.Type == EventKeyIssued {
			issued = append(issued, ev)
		}
	}
	if len(issued) != 2 {
		t.Fatalf("want 2 key_issued events, got %v", v.events.Events)
	}
	ev := issued[0]
	if ev.Level != LevelInfo || ev.Team != "chat-core" || ev.Channel != "#chat-core-notifications" || ev.RunURL != "https://ci/run/1" || ev.Time.IsZero() {
		t.Fatalf("event: %+v", ev)
	}
}

func TestCustomRequestKind(t *testing.T) {
	v := setup(t)
	v.edit(t, "policy.yaml", "kinds:\n", "kinds:\n  contractor: {source: request, ttl: 3d, budget: {reset: weekly}, max_limit: 10, providers: [openrouter]}\n")
	if _, err := v.request(t, Request{User: "jdoe", Kind: "production", Provider: "openrouter", Purpose: "x"}); err == nil ||
		!strings.Contains(err.Error(), "want one of contractor, local, personal") {
		t.Fatalf("manifest kind accepted for request: %v", err)
	}
	if _, err := v.request(t, Request{User: "jdoe", Kind: "contractor", Provider: "deepinfra", Purpose: "x"}); err == nil {
		t.Fatal("provider outside kind accepted")
	}
	is, err := v.request(t, Request{User: "jdoe", Kind: "contractor", Provider: "openrouter", Purpose: "audit"})
	if err != nil {
		t.Fatal(err)
	}
	st := v.store.states[is.Name]
	if !strings.HasPrefix(is.Name, "contractor-jdoe-") || is.LimitUSD != 10 || is.LimitReset != "weekly" || st.LimitReset != "weekly" || v.or.keys[len(v.or.keys)-1].LimitReset != "weekly" || st.AlertChannel != "#chat-core-alerts" {
		t.Fatalf("issued %+v state %+v", is, st)
	}
	if list, err := v.e.Issued(context.Background(), IssuedFilter{Kind: "contractor"}); err != nil || len(list) != 1 {
		t.Fatalf("issued list %v %v", list, err)
	}
	v.clk.add(days(3))
	if err := v.rotate(t, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.store.states[is.Name]; ok {
		t.Fatal("contractor key not removed at expiry")
	}
}

func TestKindRemovedFromPolicy(t *testing.T) {
	v := setup(t)
	is, err := v.request(t, Request{User: "asmith", Kind: "local", Provider: "openrouter", Purpose: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	v.apply(t)
	v.edit(t, "policy.yaml", "  local:\n", "  old-local:\n")
	if p := v.plan(t); strings.Contains(types(p), "delete") {
		t.Fatalf("requested key of a removed kind planned for delete: %s", types(p))
	}
	v.clk.add(days(7))
	err = v.rotate(t, RotateOptions{})
	if _, ok := v.store.states[is.Name]; ok {
		t.Fatal("expired key of a removed kind not revoked")
	}
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"policy.yaml", "teams/chat-core/prod-chat-core-dialog.yaml", "teams/chat-core/staging-chat-core-evals.yaml"} {
		p := filepath.Join(v.root, f)
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, []byte(strings.Replace(strings.Replace(string(b), "  production:\n", "  prod:\n", 1), "kind: production", "kind: prod", 1)), 0o644)
	}
	v.reload(t)
	if err := v.rotate(t, RotateOptions{}); err == nil || !strings.Contains(err.Error(), `kind "production" is not in policy.yaml kinds`) {
		t.Fatalf("want unknown kind error, got %v", err)
	}
}
