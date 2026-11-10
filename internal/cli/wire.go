package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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

type eventPrinter struct {
	out   io.Writer
	quiet bool
	log   engine.EventLog
}

func (p *eventPrinter) Emit(ev engine.Event) {
	p.log.Emit(ev)
	if !p.quiet {
		fmt.Fprintln(p.out, ev.String())
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
	pc := provider.Config{BaseURL: cfg.BaseURL, Workspace: cfg.Workspace, Timeout: timeout}
	kind := cfg.Kind
	if kind == "" {
		kind = name
	}
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
	e := &engine.Engine{
		Inv:       inv,
		Providers: providers,
		State:     st,
		Secrets:   sec,
		Kube:      &kube.Kubectl{Context: inv.Policy.KubeContext},
		Events:    a.events,
		Deliverer: &deliver.AgeSSH{OutDir: outDir, KeysURL: inv.Policy.GitHub.KeysURL},
		RunURL:    state.RunURL(),
	}
	if tok := os.Getenv(EnvGitHubToken); tok != "" && inv.Policy.GitHub.Org != "" {
		e.Org = &github.Org{Name: inv.Policy.GitHub.Org, Token: tok, API: inv.Policy.GitHub.APIURL}
	}
	return e, nil
}

var lockTTL = 15 * time.Minute

func (a *app) withLock(ctx context.Context, e *engine.Engine, op string, fn func(context.Context) error) error {
	l, ok := e.State.(state.Locker)
	if !ok {
		return fn(ctx)
	}
	le, err := state.Lock(ctx, l, state.DefaultHolder(), op, lockTTL, a.lockWait)
	if err != nil {
		return err
	}
	lctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-le.Lost():
			fmt.Fprintln(os.Stderr, "orkeys:", state.ErrLockLost)
			cancel()
		case <-lctx.Done():
		}
	}()
	err = fn(lctx)
	cancel()
	if rerr := le.Release(context.Background()); rerr != nil {
		err = errors.Join(err, fmt.Errorf("release lock %s: %w", le.ID(), rerr))
	}
	return err
}
