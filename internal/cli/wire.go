package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/hatedabamboo/orkeys/internal/backend"
	"github.com/hatedabamboo/orkeys/internal/deliver"
	"github.com/hatedabamboo/orkeys/internal/engine"
	"github.com/hatedabamboo/orkeys/internal/github"
	"github.com/hatedabamboo/orkeys/internal/kube"
	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/state"
)

const (
	EnvRepo        = "ORKEYS_REPO"
	EnvGitHubToken = "GITHUB_TOKEN"
	// EnvGitHubSecretsToken writes GitHub Actions secrets for github consumers. Default: GITHUB_TOKEN.
	EnvGitHubSecretsToken = "ORKEYS_GITHUB_SECRETS_TOKEN"
)

func (a *app) loadInventory() (*manifest.Inventory, error) {
	inv, err := manifest.Load(a.repo)
	if err != nil {
		return nil, err
	}
	if ps := inv.Validate(); len(ps) > 0 {
		var b strings.Builder
		for _, p := range ps {
			fmt.Fprintf(&b, "\n  %s", p)
		}
		return nil, fmt.Errorf("%d problem(s) in %s:%s", len(ps), a.repo, b.String())
	}
	for _, p := range inv.Warnings() {
		fmt.Fprintf(os.Stderr, "warning: %s\n", p)
	}
	return inv, nil
}

// eventPrinter sends each event to all sinks at the time of the event.
// A killed run prints no JSON document. The events file and the webhook still have its events.
type eventPrinter struct {
	out   io.Writer
	quiet bool
	log   engine.EventLog
	file  io.Writer
	hook  *webhook
}

func (p *eventPrinter) Emit(ev engine.Event) {
	p.log.Emit(ev)
	if !p.quiet {
		fmt.Fprintln(p.out, ev.String())
	}
	if p.file != nil {
		b, _ := json.Marshal(ev)
		if _, err := p.file.Write(append(b, '\n')); err != nil {
			fmt.Fprintln(os.Stderr, "orkeys: write events file:", err)
		}
		if f, ok := p.file.(*os.File); ok {
			_ = f.Sync()
		}
	}
	if p.hook != nil {
		if err := p.hook.post(ev); err != nil {
			fmt.Fprintln(os.Stderr, "orkeys: events webhook:", err)
		}
	}
}

// finish prints one JSON document with the result, the events and the error in JSON mode.
// In text mode the events were already printed as they happened.
func (a *app) finish(result map[string]any, err error) error {
	if a.output != "json" {
		return err
	}
	if result == nil {
		result = map[string]any{}
	}
	events := []engine.Event{}
	if a.events != nil && a.events.log.Events != nil {
		events = a.events.log.Events
	}
	result["events"] = events
	if err != nil {
		result["error"] = err.Error()
	}
	if perr := printJSON(os.Stdout, result); perr != nil {
		return perr
	}
	return err
}

// providerEnv returns the env var for a provider name: openrouter-private, KEY -> ORKEYS_OPENROUTER_PRIVATE_KEY.
func providerEnv(name, suffix string) string {
	return "ORKEYS_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_" + suffix
}

func providerFromSecrets(ctx context.Context, sec secrets.Store, name string, cfg manifest.ProviderConfig, timeout time.Duration) (provider.Provider, error) {
	pc := provider.Config{BaseURL: cfg.BaseURL, Workspace: cfg.Workspace, Options: maps.Clone(cfg.Options), Timeout: timeout}
	kind := cfg.Kind
	if kind == "" {
		kind = name
	}
	info, _ := provider.Lookup(kind)
	envKey := providerEnv(name, "KEY")
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		pc.Key = v
		pc.SelfID = os.Getenv(providerEnv(name, "SELF_ID"))
		return provider.New(kind, pc)
	}
	if cfg.ManagementKey == "" {
		return nil, fmt.Errorf("%s: set %s or providers.%s.management_key", name, envKey, name)
	}
	data, err := sec.Read(ctx, cfg.ManagementKey)
	if err != nil {
		return nil, fmt.Errorf("%s: %w (or set %s)", name, err, envKey)
	}
	pc.Key, pc.SelfID = data["key"], data["self_id"]
	if pc.Workspace == "" {
		pc.Workspace = data["workspace"]
	}
	if pc.BaseURL == "" {
		pc.BaseURL = data["base_url"]
	}
	// The management key secret can hold provider options. A value in policy.yaml has priority.
	for _, opt := range info.Options {
		if v := data[opt]; v != "" && pc.Options[opt] == "" {
			if pc.Options == nil {
				pc.Options = map[string]string{}
			}
			pc.Options[opt] = v
		}
	}
	if pc.Key == "" {
		return nil, fmt.Errorf("%s: field \"key\" is empty in %s (or set %s)", name, cfg.ManagementKey, envKey)
	}
	return provider.New(kind, pc)
}

func (a *app) engine(ctx context.Context, outDir string) (*engine.Engine, error) {
	inv, err := a.loadInventory()
	if err != nil {
		return nil, err
	}
	var f backend.Factory
	st, err := f.State(ctx, inv.Policy.State)
	if err != nil {
		return nil, err
	}
	sec, err := f.Secrets(ctx, inv.Policy.Secrets)
	if err != nil {
		return nil, err
	}
	providers := provider.Set{}
	for name, cfg := range inv.Policy.Providers {
		p, err := providerFromSecrets(ctx, sec, name, cfg, a.timeout)
		if err != nil {
			return nil, err
		}
		providers[name] = p
	}
	a.events = &eventPrinter{out: os.Stdout, quiet: a.output == "json"}
	if a.eventsFile != "" {
		f, err := os.OpenFile(a.eventsFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("events file: %w", err)
		}
		a.events.file = f
	}
	if a.eventsWebhook != "" {
		a.events.hook = &webhook{url: a.eventsWebhook}
	}
	e := &engine.Engine{
		Inv:       inv,
		Providers: providers,
		State:     st,
		Secrets:   sec,
		Notifiers: map[string]engine.Notifier{
			manifest.ConsumerESO:    engine.KubeNotifier{Kube: &kube.Kubectl{Context: inv.Policy.KubeContext}},
			manifest.ConsumerDirect: engine.KubeNotifier{Kube: &kube.Kubectl{Context: inv.Policy.KubeContext}},
		},
		Pushers: map[string]engine.Pusher{
			manifest.ConsumerGitHub: engine.Unavailable{Reason: "github consumers: no GitHub token (set " + EnvGitHubSecretsToken + " or " + EnvGitHubToken + ")"},
		},
		NotifyConcurrency: notifyConcurrency,
		Events:            a.events,
		Deliverer:         &deliver.AgeSSH{OutDir: outDir, KeysURL: inv.Policy.GitHub.KeysURL},
		RunURL:            state.RunURL(),
	}
	if a.logFormat == "json" {
		e.Logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	if tok := os.Getenv(EnvGitHubToken); tok != "" && inv.Policy.GitHub.Org != "" {
		e.Org = &github.Org{Name: inv.Policy.GitHub.Org, Token: tok, API: inv.Policy.GitHub.APIURL}
	}
	if tok := cmp.Or(os.Getenv(EnvGitHubSecretsToken), os.Getenv(EnvGitHubToken)); tok != "" {
		e.Pushers[manifest.ConsumerGitHub] = engine.GitHubPusher{Secrets: &github.Secrets{Token: tok, API: inv.Policy.GitHub.APIURL}}
	}
	return e, nil
}

var lockTTL = 15 * time.Minute

// notifyConcurrency is how many eso and direct consumers of one key sync and restart at a time.
const notifyConcurrency = 4

func (a *app) withLock(ctx context.Context, e *engine.Engine, op string, fn func(context.Context) error) error {
	le, err := state.Lock(ctx, e.State, state.DefaultHolder(), op, lockTTL, a.lockWait)
	if err != nil {
		return err
	}
	e.Guard = le.Err
	defer func() { e.Guard = nil }()
	lctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-le.Lost():
			fmt.Fprintln(os.Stderr, "orkeys:", le.Err())
			cancel()
		case <-lctx.Done():
		}
	}()
	err = fn(lctx)
	if err == nil {
		if hb, ok := e.State.(state.Heartbeats); ok {
			// A failed heartbeat write does not fail the run. The missing heartbeat raises an alert later.
			if herr := hb.PutHeartbeat(lctx, op, time.Now()); herr != nil {
				fmt.Fprintf(os.Stderr, "orkeys: record heartbeat of %s: %v\n", op, herr)
			}
		}
	}
	cancel()
	if rerr := le.Release(context.Background()); rerr != nil {
		err = errors.Join(err, fmt.Errorf("release lock %s: %w", le.ID(), rerr))
	}
	return err
}
