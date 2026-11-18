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

	"github.com/hatedabamboo/orkeys/internal/fal"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/spend"
	"github.com/hatedabamboo/orkeys/internal/values"
)

const (
	EnvFalKey  = "FAL_ADMIN_KEY"
	EnvFalBase = "FAL_API_BASE"
)

type falKey struct {
	fal.Key
	Spend spend.Spend `json:"spend_usd"`
}

func (a *app) falCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fal",
		Short: "Manage fal.ai API keys directly",
		Long: `Manage the API keys of one fal.ai workspace.
The management key needs the FULL preset. A custom key without auth:keys:create can list
and revoke keys, but create fails with 403.

fal.ai has no per-key spend limit and no disabled flag. Delete revokes a key, and a revoke cannot be undone.
Spend is the fal.ai estimate from the usage endpoint.

Environment:
  ` + EnvFalKey + `   management key, key_id:key_secret (required)
  ` + EnvFalBase + `    API base URL (default ` + fal.DefaultBaseURL + `)

Commands that take <key> accept a key ID or an exact key alias.`,
		Args: cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(&a.falBaseURL, "base-url", envOr(EnvFalBase, fal.DefaultBaseURL), "API base URL (env "+EnvFalBase+")")
	cmd.AddCommand(a.falListCmd(), a.falGetCmd(), a.falCreateCmd(), a.falDeleteCmd())
	return cmd
}

func (a *app) falConfig() (provider.Config, error) {
	tok := strings.TrimSpace(os.Getenv(EnvFalKey))
	if tok == "" {
		return provider.Config{}, fmt.Errorf("%s is not set", EnvFalKey)
	}
	return provider.Config{Key: tok, BaseURL: a.falBaseURL, Timeout: a.timeout}, nil
}

func falList(ctx context.Context, c *fal.Client) ([]falKey, error) {
	keys, err := c.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	costs, err := c.Usage(ctx, spend.Start(now))
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	byKey := spend.ByKey(costs, now)
	out := make([]falKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, falKey{Key: k, Spend: byKey[k.ID]})
	}
	return out, nil
}

func (a *app) falListKeys(ctx context.Context) (provider.Config, []falKey, error) {
	cfg, err := a.falConfig()
	if err != nil {
		return cfg, nil, err
	}
	keys, err := falList(ctx, fal.New(cfg.BaseURL, cfg.Key, cfg.Timeout))
	return cfg, keys, err
}

func falResolve(keys []falKey, ref string) (*falKey, error) {
	var byName []*falKey
	for i := range keys {
		if keys[i].ID == ref {
			return &keys[i], nil
		}
		if keys[i].Alias == ref {
			byName = append(byName, &keys[i])
		}
	}
	switch len(byName) {
	case 0:
		return nil, fmt.Errorf("no key matches %q", ref)
	case 1:
		return byName[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d keys, use the key ID:", ref, len(byName))
	for _, k := range byName {
		fmt.Fprintf(&b, "\n  %s  %s", k.ID, k.Alias)
	}
	return nil, errors.New(b.String())
}

func falTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func (a *app) writeFalKeys(w io.Writer, keys []falKey) error {
	if a.output == "json" {
		if keys == nil {
			keys = []falKey{}
		}
		return printJSON(w, keys)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if a.output == "wide" {
		fmt.Fprintln(tw, "ID\tALIAS\tACCESS\tDAY\tWEEK\tMONTH\tEXPIRES\tCREATED\tDESCRIPTION")
	} else {
		fmt.Fprintln(tw, "ID\tALIAS\tACCESS\tMONTH\tEXPIRES")
	}
	for _, k := range keys {
		created := k.Created()
		if a.output == "wide" {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Alias, k.Access(),
				values.USD(k.Spend.Day), values.USD(k.Spend.Week), values.USD(k.Spend.Month),
				falTime(k.Expires()), falTime(&created), k.Description)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", k.ID, k.Alias, k.Access(), values.USD(k.Spend.Month), falTime(k.Expires()))
		}
	}
	return tw.Flush()
}

func (a *app) writeFalKey(w io.Writer, k falKey) error {
	if a.output == "json" {
		return printJSON(w, k)
	}
	created := k.Created()
	perms := "-"
	if len(k.Permissions) > 0 {
		perms = strings.Join(k.Permissions, ", ")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"ID", k.ID},
		{"Alias", k.Alias},
		{"Description", k.Description},
		{"Access", k.Access()},
		{"Permissions", perms},
		{"Spend (today)", values.USD(k.Spend.Day)},
		{"Spend (this week)", values.USD(k.Spend.Week)},
		{"Spend (this month)", values.USD(k.Spend.Month)},
		{"Expires", falTime(k.Expires())},
		{"Created", falTime(&created)},
	} {
		fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	return tw.Flush()
}

func (a *app) falListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the keys with spend",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, keys, err := a.falListKeys(cmd.Context())
			if err != nil {
				return err
			}
			return a.writeFalKeys(os.Stdout, keys)
		},
	}
}

func (a *app) falGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Show one key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, keys, err := a.falListKeys(cmd.Context())
			if err != nil {
				return err
			}
			k, err := falResolve(keys, args[0])
			if err != nil {
				return err
			}
			return a.writeFalKey(os.Stdout, *k)
		},
	}
}

func (a *app) falCreateCmd() *cobra.Command {
	var (
		name, expires string
		scopes        []string
		quiet         bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a key (the secret is shown only once)",
		Example: `  orkeys fal create --name ci --expires 90d
  orkeys fal create --name deploy --scopes DEPLOY
  orkeys fal create --name worker --scopes models:requests:submit,models:requests:read
  KEY=$(orkeys fal create --name tmp --expires 12h -q)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(name) == "" {
				return errors.New("--name is required")
			}
			spec := provider.Spec{Name: name, Scopes: scopes}
			if cmd.Flags().Changed("expires") {
				t, err := oaExpiry(expires)
				if err != nil {
					return err
				}
				spec.ExpiresAt = t
			}
			cfg, err := a.falConfig()
			if err != nil {
				return err
			}
			p, err := provider.New("fal", cfg)
			if err != nil {
				return err
			}
			k, secret, err := p.Create(cmd.Context(), spec)
			if err != nil {
				return err
			}
			switch {
			case quiet:
				fmt.Println(secret)
			case a.output == "json":
				return printJSON(os.Stdout, map[string]any{"id": k.ID, "name": k.Name, "expires_at": k.ExpiresAt, "key": secret})
			default:
				fmt.Printf("ID:       %s\nAlias:    %s\nExpires:  %s\n\nKey: %s\n", k.ID, k.Name, falTime(k.ExpiresAt), secret)
				fmt.Fprintln(os.Stderr, "NOTE: save this key now. fal.ai does not show it again.")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&name, "name", "n", "", "key alias (required)")
	f.StringVar(&expires, "expires", "", "expiry: duration (30d, 2w, 12h), date (2027-12-31) or RFC 3339; at most 365d")
	f.StringSliceVar(&scopes, "scopes", nil, "one preset (API, DEPLOY, ...) or a list of permissions; FULL is not allowed (default: API)")
	f.BoolVarP(&quiet, "quiet", "q", false, "print only the secret key")
	return cmd
}

func (a *app) falDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <key>...",
		Aliases: []string{"rm", "revoke"},
		Short:   "Revoke one or more keys (a revoke cannot be undone)",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cfg, keys, err := a.falListKeys(ctx)
			if err != nil {
				return err
			}
			// Resolve everything first, so a typo aborts before any revoke.
			var targets []*falKey
			for _, ref := range args {
				k, err := falResolve(keys, ref)
				if err != nil {
					return fmt.Errorf("%s: %w", ref, err)
				}
				targets = append(targets, k)
			}
			p, err := provider.New("fal", cfg)
			if err != nil {
				return err
			}
			for _, k := range targets {
				if k.ID == p.SelfID() {
					return fmt.Errorf("%s: refusing to revoke the key in %s", k.Alias, EnvFalKey)
				}
			}
			if !yes {
				if !isTTY() {
					return errors.New("refusing to revoke without confirmation: pass --yes")
				}
				fmt.Println("Keys to revoke:")
				for _, k := range targets {
					fmt.Printf("  %s  %s  %s\n", k.ID, k.Alias, k.Access())
				}
				if !confirm(fmt.Sprintf("Revoke %d key(s)? This cannot be undone.", len(targets))) {
					return errors.New("aborted")
				}
			}
			var failed int
			for _, k := range targets {
				if err := p.Delete(ctx, k.ID); err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "orkeys: %s: %v\n", k.Alias, err)
					continue
				}
				fmt.Printf("revoked %s (%s)\n", k.Alias, k.ID)
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
