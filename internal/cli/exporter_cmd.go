package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hatedabamboo/orkeys/internal/backend"
	"github.com/hatedabamboo/orkeys/internal/exporter"
	"github.com/hatedabamboo/orkeys/internal/manifest"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/state"
)

type exporterConfig struct {
	State     string
	Secrets   string
	Providers map[string]manifest.ProviderConfig
	// Policy is from --policy or from the state store. It is nil when neither has one.
	Policy *manifest.Policy
}

// resolveExporterConfig starts from the policy (when given) and lets explicit flags override it.
func resolveExporterConfig(pol *manifest.Policy, stateURL, secretsKind string, providerPaths []string, stateSet, secretsSet bool) (*exporterConfig, error) {
	c := &exporterConfig{State: stateURL, Secrets: secretsKind, Providers: map[string]manifest.ProviderConfig{}, Policy: pol}
	if pol != nil {
		if !stateSet {
			c.State = pol.State
		}
		if !secretsSet {
			c.Secrets = pol.Secrets
		}
		for name, pc := range pol.Providers {
			c.Providers[name] = pc
		}
	}
	for _, pp := range providerPaths {
		name, path, ok := strings.Cut(pp, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("--provider %q: want name=secret-path", pp)
		}
		pc := c.Providers[name]
		pc.ManagementKey = path
		c.Providers[name] = pc
	}
	if len(c.Providers) == 0 {
		return nil, errors.New("no providers: pass --policy or --provider name=secret-path, or run apply to save policy.yaml to the state store")
	}
	return c, nil
}

// storedPolicy returns the copy of policy.yaml that apply saved in the state store, or nil when there is none.
func storedPolicy(ctx context.Context, st state.Store) (*manifest.Policy, error) {
	ps, ok := st.(state.Policies)
	if !ok {
		return nil, nil
	}
	sp, err := ps.GetPolicy(ctx)
	if err != nil {
		return nil, fmt.Errorf("read policy from the state store: %w", err)
	}
	if sp == nil {
		fmt.Fprintln(os.Stderr, "orkeys: no policy.yaml in the state store; run apply to save it")
		return nil, nil
	}
	pol, err := manifest.ParsePolicy([]byte(sp.YAML))
	if err != nil {
		return nil, fmt.Errorf("policy in the state store: %w", err)
	}
	return pol, nil
}

func (a *app) exporterCmd() *cobra.Command {
	var (
		listen, stateURL, secretsKind, policyPath string
		interval                                  time.Duration
		providerPaths                             []string
	)
	cmd := &cobra.Command{
		Use:   "exporter",
		Short: "Serve key spend, limits and expiry as Prometheus metrics",
		Example: `  orkeys exporter --state vault://secret/orkeys/state --secrets vault

  orkeys exporter --policy /etc/orkeys/policy.yaml

  orkeys exporter --state vault://secret/orkeys/state --secrets vault \
    --provider openrouter=secret/orkeys/providers/openrouter \
    --provider deepinfra=secret/orkeys/providers/deepinfra

  orkeys exporter --state s3://my-bucket/orkeys/state --secrets aws \
    --provider openrouter=orkeys/providers/openrouter`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			flags := cmd.Flags()
			var (
				f   backend.Factory
				pol *manifest.Policy
				st  state.Store
				err error
			)
			fromStore := policyPath == ""
			if fromStore {
				if st, err = f.State(ctx, stateURL); err != nil {
					return err
				}
				if pol, err = storedPolicy(ctx, st); err != nil {
					return err
				}
			} else if pol, err = manifest.LoadPolicy(policyPath); err != nil {
				return err
			}
			// The stored policy cannot move the state: the exporter found the policy at --state.
			cfg, err := resolveExporterConfig(pol, stateURL, secretsKind, providerPaths, fromStore || flags.Changed("state"), flags.Changed("secrets"))
			if err != nil {
				return err
			}
			if st == nil {
				if st, err = f.State(ctx, cfg.State); err != nil {
					return err
				}
			}
			var sec secrets.Store
			providers := provider.Set{}
			names := make([]string, 0, len(cfg.Providers))
			for n := range cfg.Providers {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, name := range names {
				if sec == nil && os.Getenv(providerEnv(name, "KEY")) == "" {
					if sec, err = f.Secrets(ctx, cfg.Secrets); err != nil {
						return err
					}
				}
				p, err := providerFromSecrets(ctx, sec, name, cfg.Providers[name], a.timeout)
				if err != nil {
					return err
				}
				providers[name] = p
			}
			x := &exporter.Exporter{Store: st, Providers: providers, Policy: cfg.Policy, PolicyFromStore: fromStore, Version: a.version, Interval: interval}
			go x.Run(ctx)
			srv := &http.Server{Addr: listen, Handler: x, ReadHeaderTimeout: 10 * time.Second}
			go func() {
				<-ctx.Done()
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(sctx)
			}()
			fmt.Fprintf(os.Stderr, "orkeys exporter on %s, poll every %s\n", listen, interval)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&listen, "listen", ":9090", "listen address")
	f.DurationVar(&interval, "interval", 10*time.Minute, "provider poll interval")
	f.StringVar(&policyPath, "policy", "", "read state, secrets and providers from this policy.yaml; flags override it. Default: the copy of policy.yaml that apply saves in --state")
	f.StringVar(&stateURL, "state", "vault://secret/orkeys/state", "state store: vault://<mount>/<path> or s3://<bucket>/<prefix>")
	f.StringVar(&secretsKind, "secrets", "vault", "where management keys are: vault or aws (Secrets Manager)")
	f.StringArrayVar(&providerPaths, "provider", nil, "provider and secret path of its management key, name=path (repeat); with --policy, the name can be any provider name of the policy")
	return cmd
}
