package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hatedabamboo/orkeys/internal/backend"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/secrets"
	"github.com/hatedabamboo/orkeys/internal/values"
)

// keysCmd uses only the common provider interface. It works for each provider type without code of its own.
func (a *app) keysCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Manage keys of any provider in policy.yaml",
		Long: `keys lists and changes keys of one provider of the inventory, with the management key of
policy.yaml (or ` + "ORKEYS_<PROVIDER>_KEY" + `). It works for every provider type. It does not change state:
apply and rotate do not know about keys made here, and show them as unmanaged.

Commands that take <key> accept a key ID or an exact key name.`,
		Example: `  orkeys keys list -p openrouter
  orkeys keys create -p deepinfra --name scratch --limit 5
  orkeys keys delete -p openrouter scratch`,
		Args: cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVarP(&name, "provider", "p", "", "provider name from policy.yaml (required)")
	_ = cmd.MarkPersistentFlagRequired("provider")
	get := func(ctx context.Context) (provider.Provider, error) { return a.inventoryProvider(ctx, name) }
	cmd.AddCommand(a.keysListCmd(get), a.keysCreateCmd(get), a.keysLimitCmd(get),
		a.keysToggleCmd(get, "disable"), a.keysToggleCmd(get, "enable"), a.keysDeleteCmd(get))
	return cmd
}

type getProvider func(context.Context) (provider.Provider, error)

// inventoryProvider returns the provider with this name from policy.yaml.
// It reads the secrets store only when the management key is not in the environment.
func (a *app) inventoryProvider(ctx context.Context, name string) (provider.Provider, error) {
	inv, err := a.loadInventory()
	if err != nil {
		return nil, err
	}
	cfg, ok := inv.Policy.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not in policy.yaml providers", name)
	}
	var sec secrets.Store
	if strings.TrimSpace(os.Getenv(providerEnv(name, "KEY"))) == "" {
		var f backend.Factory
		if sec, err = f.Secrets(ctx, inv.Policy.Secrets); err != nil {
			return nil, err
		}
	}
	return providerFromSecrets(ctx, sec, name, cfg, a.timeout)
}

// resolveKey finds a key by exact ID, then by exact name. A name of more than one key is an error.
func resolveKey(keys []provider.Key, ref string) (*provider.Key, error) {
	if k := provider.Find(keys, ref); k != nil {
		return k, nil
	}
	var found []provider.Key
	for _, k := range keys {
		if k.Name == ref {
			found = append(found, k)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no key matches %q", ref)
	case 1:
		return &found[0], nil
	}
	return nil, fmt.Errorf("%q is the name of %d keys; use the key ID", ref, len(found))
}

func status(k provider.Key) string {
	switch {
	case k.Disabled:
		return "disabled"
	case k.ExpiresAt != nil && !k.ExpiresAt.After(time.Now()):
		return "expired"
	}
	return "active"
}

func timeOrDash(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func writeProviderKeys(w io.Writer, keys []provider.Key, format string) error {
	if format == "json" {
		if keys == nil {
			keys = []provider.Key{}
		}
		return printJSON(w, keys)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tLIMIT\tRESET\tMONTH\tEXPIRES\tLAST USED")
	for _, k := range keys {
		reset := k.LimitReset
		if reset == "" {
			reset = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, status(k), values.USDPtr(k.LimitUSD), reset,
			values.USDPtr(k.SpendMonthUSD), timeOrDash(k.ExpiresAt), timeOrDash(k.LastUsedAt))
	}
	return tw.Flush()
}

func (a *app) keysListCmd(get getProvider) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List keys",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := get(cmd.Context())
			if err != nil {
				return err
			}
			keys, err := p.List(cmd.Context())
			if err != nil {
				return err
			}
			return writeProviderKeys(os.Stdout, keys, a.output)
		},
	}
}

func (a *app) keysCreateCmd(get getProvider) *cobra.Command {
	var (
		name, reset, expires string
		limit                float64
		scopes               []string
		quiet                bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a key (the secret is shown only once)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(name) == "" {
				return errors.New("--name is required")
			}
			p, err := get(cmd.Context())
			if err != nil {
				return err
			}
			spec := provider.Spec{Name: name, Scopes: scopes, LimitReset: reset}
			if cmd.Flags().Changed("limit") {
				spec.LimitUSD = &limit
			}
			if expires != "" {
				d, err := values.ParseDuration(expires)
				if err != nil {
					return err
				}
				exp := time.Now().Add(d)
				spec.ExpiresAt = &exp
			}
			k, secret, err := p.Create(cmd.Context(), spec)
			if err != nil {
				return err
			}
			switch {
			case quiet:
				fmt.Println(secret)
			case a.output == "json":
				return printJSON(os.Stdout, map[string]any{"key": k, "secret": secret})
			default:
				if err := writeProviderKeys(os.Stdout, []provider.Key{k}, a.output); err != nil {
					return err
				}
				fmt.Printf("\nKey: %s\n", secret)
				fmt.Fprintln(os.Stderr, "NOTE: save this key now. The provider does not show it again.")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&name, "name", "n", "", "key name (required)")
	f.Float64Var(&limit, "limit", 0, "limit in USD (providers with a per-key limit)")
	f.StringVar(&reset, "limit-reset", "monthly", "limit reset: daily|weekly|monthly")
	f.StringVar(&expires, "expires", "", "expiry as a duration: 30d, 2w, 12h (providers with native expiry)")
	f.StringSliceVar(&scopes, "scopes", nil, "scopes (providers with scopes)")
	f.BoolVarP(&quiet, "quiet", "q", false, "print only the secret key")
	return cmd
}

func (a *app) keysLimitCmd(get getProvider) *cobra.Command {
	var (
		limit string
		reset string
	)
	cmd := &cobra.Command{
		Use:   "set-limit <key>",
		Short: "Set or remove the limit of a key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := values.ParseLimit(limit)
			if err != nil {
				return err
			}
			p, err := get(cmd.Context())
			if err != nil {
				return err
			}
			l, err := provider.AsLimiter(p)
			if err != nil {
				return err
			}
			k, err := findKey(cmd.Context(), p, args[0])
			if err != nil {
				return err
			}
			if err := l.SetLimit(cmd.Context(), k.ID, v.Ptr(), reset); err != nil {
				return err
			}
			fmt.Printf("limit of %s (%s): %s %s\n", k.Name, k.ID, values.USDPtr(v.Ptr()), reset)
			return nil
		},
	}
	cmd.Flags().StringVar(&limit, "limit", "", "limit in USD, or \"none\" (required)")
	cmd.Flags().StringVar(&reset, "limit-reset", "monthly", "limit reset: daily|weekly|monthly")
	_ = cmd.MarkFlagRequired("limit")
	return cmd
}

func findKey(ctx context.Context, p provider.Provider, ref string) (*provider.Key, error) {
	keys, err := p.List(ctx)
	if err != nil {
		return nil, err
	}
	return resolveKey(keys, ref)
}

func (a *app) keysToggleCmd(get getProvider, use string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <key>...",
		Short: strings.ToUpper(use[:1]) + use[1:] + " keys (providers that can disable keys)",
		Long:  "enable sets no limit. Set the limit again with set-limit where the provider has one.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := get(cmd.Context())
			if err != nil {
				return err
			}
			d, err := provider.AsDisabler(p)
			if err != nil {
				return err
			}
			keys, err := p.List(cmd.Context())
			if err != nil {
				return err
			}
			var failed int
			for _, ref := range args {
				k, err := resolveKey(keys, ref)
				if err == nil {
					if use == "disable" {
						err = d.Disable(cmd.Context(), k.ID)
					} else {
						err = d.Enable(cmd.Context(), k.ID, k.LimitUSD, k.LimitReset)
					}
				}
				if err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "orkeys: %s: %v\n", ref, err)
					continue
				}
				fmt.Printf("%sd %s (%s)\n", use, k.Name, k.ID)
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d failed", failed, len(args))
			}
			return nil
		},
	}
}

func (a *app) keysDeleteCmd(get getProvider) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <key>...",
		Aliases: []string{"rm"},
		Short:   "Delete keys",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && !isTTY() {
				return errors.New("refusing to delete without confirmation: pass --yes")
			}
			p, err := get(cmd.Context())
			if err != nil {
				return err
			}
			keys, err := p.List(cmd.Context())
			if err != nil {
				return err
			}
			// A typo must stop the command before the first delete.
			var targets []*provider.Key
			for _, ref := range args {
				k, err := resolveKey(keys, ref)
				if err != nil {
					return err
				}
				targets = append(targets, k)
			}
			if !yes {
				for _, k := range targets {
					fmt.Printf("  %s  %s\n", k.ID, k.Name)
				}
				if !confirm(fmt.Sprintf("Delete %d key(s)?", len(targets))) {
					return errors.New("aborted")
				}
			}
			var failed int
			for _, k := range targets {
				if err := p.Delete(cmd.Context(), k.ID); err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "orkeys: %s: %v\n", k.Name, err)
					continue
				}
				fmt.Printf("deleted %s (%s)\n", k.Name, k.ID)
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d failed", failed, len(targets))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
