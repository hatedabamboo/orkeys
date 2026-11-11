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

	"github.com/hatedabamboo/orkeys/internal/openai"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/values"
)

const (
	EnvOAKey     = "OPENAI_ADMIN_KEY"
	EnvOABase    = "OPENAI_API_BASE"
	EnvOAProject = "OPENAI_PROJECT_ID"
)

type oaKey struct {
	openai.Key
	Spend openai.Spend `json:"spend_usd"`
}

func (a *app) openaiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "openai",
		Aliases: []string{"oa"},
		Short:   "Manage OpenAI project API keys directly",
		Long: `Manage the API keys of one OpenAI project with an Admin API key.

OpenAI has no per-key spend limit and no disabled flag. Delete is the only way to revoke a key.
create makes one service account per key; delete also deletes the service account when it has no other keys.

Environment:
  ` + EnvOAKey + `    Admin API key (required)
  ` + EnvOAProject + `   project ID, proj_... (required, same as --project)
  ` + EnvOABase + `     API base URL (default ` + openai.DefaultBaseURL + `)

Commands that take <key> accept a key ID or an exact key name.`,
		Args: cobra.NoArgs,
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&a.oaBaseURL, "base-url", envOr(EnvOABase, openai.DefaultBaseURL), "API base URL (env "+EnvOABase+")")
	pf.StringVarP(&a.oaProject, "project", "p", os.Getenv(EnvOAProject), "project ID (env "+EnvOAProject+")")
	cmd.AddCommand(a.oaListCmd(), a.oaGetCmd(), a.oaCreateCmd(), a.oaUpdateCmd(), a.oaDeleteCmd())
	return cmd
}

func (a *app) oaConfig() (provider.Config, error) {
	tok := strings.TrimSpace(os.Getenv(EnvOAKey))
	if tok == "" {
		return provider.Config{}, fmt.Errorf("%s is not set", EnvOAKey)
	}
	if a.oaProject == "" {
		return provider.Config{}, fmt.Errorf("project ID is not set: pass --project or set %s", EnvOAProject)
	}
	return provider.Config{Key: tok, BaseURL: a.oaBaseURL, Workspace: a.oaProject, Timeout: a.timeout}, nil
}

func (a *app) oaClient() (*openai.Client, error) {
	c, err := a.oaConfig()
	if err != nil {
		return nil, err
	}
	return openai.New(c.BaseURL, c.Key, c.Workspace, c.Timeout), nil
}

func oaList(ctx context.Context, c *openai.Client) ([]oaKey, error) {
	keys, err := c.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	costs, err := c.Costs(ctx, openai.SpendStart(now))
	if err != nil {
		return nil, fmt.Errorf("costs: %w", err)
	}
	spend := openai.SpendByKey(costs, now)
	out := make([]oaKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, oaKey{Key: k, Spend: spend[k.ID]})
	}
	return out, nil
}

func oaResolve(keys []oaKey, ref string) (*oaKey, error) {
	var byName []*oaKey
	for i := range keys {
		if keys[i].ID == ref {
			return &keys[i], nil
		}
		if keys[i].Name == ref {
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
		fmt.Fprintf(&b, "\n  %s  %s", k.ID, k.Name)
	}
	return nil, errors.New(b.String())
}

func oaTime(v *int64) string {
	if v == nil || *v == 0 {
		return "-"
	}
	return time.Unix(*v, 0).UTC().Format("2006-01-02 15:04")
}

func oaOwner(k openai.Key) string {
	switch {
	case k.Owner.ServiceAccount != nil:
		return "service_account:" + k.Owner.ServiceAccount.Name
	case k.Owner.User != nil:
		return "user:" + values.Str(&k.Owner.User.Email)
	}
	return k.Owner.Type
}

func oaStatus(k openai.Key) string {
	if k.OwnerProjectAccess == "inactive" {
		return "inactive"
	}
	return "active"
}

func (a *app) writeOAKeys(w io.Writer, keys []oaKey) error {
	if a.output == "json" {
		if keys == nil {
			keys = []oaKey{}
		}
		return printJSON(w, keys)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if a.output == "wide" {
		fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tOWNER\tDAY\tWEEK\tMONTH\tEXPIRES\tLAST USED\tCREATED")
	} else {
		fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tMONTH\tEXPIRES\tLAST USED")
	}
	for _, k := range keys {
		created := k.CreatedAt
		if a.output == "wide" {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, oaStatus(k.Key), oaOwner(k.Key),
				values.USD(k.Spend.Day), values.USD(k.Spend.Week), values.USD(k.Spend.Month),
				oaTime(k.ExpiresAt), oaTime(k.LastUsedAt), oaTime(&created))
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, oaStatus(k.Key), values.USD(k.Spend.Month),
				oaTime(k.ExpiresAt), oaTime(k.LastUsedAt))
		}
	}
	return tw.Flush()
}

func (a *app) writeOAKey(w io.Writer, k oaKey) error {
	if a.output == "json" {
		return printJSON(w, k)
	}
	created := k.CreatedAt
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"ID", k.ID},
		{"Name", k.Name},
		{"Status", oaStatus(k.Key)},
		{"Owner", oaOwner(k.Key)},
		{"Display", k.RedactedValue},
		{"Spend (today)", values.USD(k.Spend.Day)},
		{"Spend (this week)", values.USD(k.Spend.Week)},
		{"Spend (this month)", values.USD(k.Spend.Month)},
		{"Expires", oaTime(k.ExpiresAt)},
		{"Last used", oaTime(k.LastUsedAt)},
		{"Created", oaTime(&created)},
	} {
		fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	return tw.Flush()
}

func (a *app) oaListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the project keys with spend",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.oaClient()
			if err != nil {
				return err
			}
			keys, err := oaList(cmd.Context(), c)
			if err != nil {
				return err
			}
			return a.writeOAKeys(os.Stdout, keys)
		},
	}
}

func (a *app) oaGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Show one key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.oaClient()
			if err != nil {
				return err
			}
			keys, err := oaList(cmd.Context(), c)
			if err != nil {
				return err
			}
			k, err := oaResolve(keys, args[0])
			if err != nil {
				return err
			}
			return a.writeOAKey(os.Stdout, *k)
		},
	}
}

func oaExpiry(s string) (*time.Time, error) {
	v, err := values.ParseExpiry(s, time.Now())
	if err != nil || v == nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339, *v)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (a *app) oaCreateCmd() *cobra.Command {
	var (
		name, expires string
		scopes        []string
		quiet         bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a service account and its key (the secret is shown only once)",
		Example: `  orkeys oa create --name ci --expires 90d
  orkeys oa create --name reader --scopes api.all.read
  KEY=$(orkeys oa create --name tmp --expires 12h -q)`,
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
			cfg, err := a.oaConfig()
			if err != nil {
				return err
			}
			p, err := provider.New("openai", cfg)
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
				exp := "-"
				if k.ExpiresAt != nil {
					exp = k.ExpiresAt.Format("2006-01-02 15:04")
				}
				fmt.Printf("ID:       %s\nName:     %s\nExpires:  %s\n\nKey: %s\n", k.ID, k.Name, exp, secret)
				fmt.Fprintln(os.Stderr, "NOTE: save this key now. OpenAI does not show it again.")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&name, "name", "n", "", "key and service account name (required)")
	f.StringVar(&expires, "expires", "", "expiry: duration (30d, 2w, 12h), date (2027-12-31) or RFC 3339; at most 365d")
	f.StringSliceVar(&scopes, "scopes", nil, "key scopes, for example api.all.read (default: all permissions of the service account)")
	f.BoolVarP(&quiet, "quiet", "q", false, "print only the secret key")
	return cmd
}

func (a *app) oaUpdateCmd() *cobra.Command {
	var (
		expires string
		scopes  []string
	)
	cmd := &cobra.Command{
		Use:   "update <key>",
		Short: "Change the scopes or the expiry of a key",
		Example: `  orkeys oa update ci --expires 30d
  orkeys oa update ci --scopes api.all.read`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if !f.Changed("expires") && !f.Changed("scopes") {
				return errors.New("nothing to change: pass --expires or --scopes")
			}
			var secs *int64
			if f.Changed("expires") {
				t, err := oaExpiry(expires)
				if err != nil {
					return err
				}
				if t == nil {
					return errors.New("--expires: openai cannot remove an expiry")
				}
				v := int64(time.Until(*t).Seconds())
				secs = &v
			}
			var sc []string
			if f.Changed("scopes") {
				sc = append([]string{}, scopes...)
			}
			c, err := a.oaClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			keys, err := oaList(ctx, c)
			if err != nil {
				return err
			}
			k, err := oaResolve(keys, args[0])
			if err != nil {
				return err
			}
			if err := c.UpdateKey(ctx, k.ID, sc, secs); err != nil {
				return err
			}
			fmt.Printf("updated %s (%s)\n", k.Name, k.ID)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&expires, "expires", "", "new expiry, counted from now: duration, date or RFC 3339; at most 365d")
	f.StringSliceVar(&scopes, "scopes", nil, "new scopes; an empty value gives all permissions of the owner")
	return cmd
}

func (a *app) oaDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <key>...",
		Aliases: []string{"rm"},
		Short:   "Delete one or more keys",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.oaConfig()
			if err != nil {
				return err
			}
			c := openai.New(cfg.BaseURL, cfg.Key, cfg.Workspace, cfg.Timeout)
			ctx := cmd.Context()
			keys, err := oaList(ctx, c)
			if err != nil {
				return err
			}
			// Resolve everything first, so a typo aborts before any delete.
			var targets []*oaKey
			for _, ref := range args {
				k, err := oaResolve(keys, ref)
				if err != nil {
					return fmt.Errorf("%s: %w", ref, err)
				}
				targets = append(targets, k)
			}
			if !yes {
				if !isTTY() {
					return errors.New("refusing to delete without confirmation: pass --yes")
				}
				fmt.Println("Keys to delete:")
				for _, k := range targets {
					fmt.Printf("  %s  %s  %s\n", k.ID, k.Name, oaOwner(k.Key))
				}
				if !confirm(fmt.Sprintf("Delete %d key(s)?", len(targets))) {
					return errors.New("aborted")
				}
			}
			p, err := provider.New("openai", cfg)
			if err != nil {
				return err
			}
			var failed int
			for _, k := range targets {
				if err := p.Delete(ctx, k.ID); err != nil {
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
