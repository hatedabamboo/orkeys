package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hatedabamboo/orkeys/internal/deepinfra"
	"github.com/hatedabamboo/orkeys/internal/values"
)

const (
	EnvDIKey  = "DEEPINFRA_API_KEY"
	EnvDIBase = "DEEPINFRA_API_BASE"
)

type diToken struct {
	deepinfra.Token
	Limit *float64 `json:"limit"`
	Spend *float64 `json:"spend"`
}

func (a *app) deepinfraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "deepinfra",
		Aliases: []string{"di"},
		Short:   "Show DeepInfra API tokens with limit and spend",
		Long: `Show DeepInfra API tokens with their monthly limit and spend.

DeepInfra allows creating, deleting and limiting tokens only with a dashboard login. With an API key, the write
commands (create, update, disable, delete, scopes) fail with 401.

Environment:
  ` + EnvDIKey + `    API token that can manage tokens (required)
  ` + EnvDIBase + `   API base URL (default ` + deepinfra.DefaultBaseURL + `)

Commands that take <token> accept a token ID or an exact token name.`,
		Args: cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(&a.diBaseURL, "base-url", envOr(EnvDIBase, deepinfra.DefaultBaseURL), "API base URL (env "+EnvDIBase+")")
	cmd.AddCommand(a.diListCmd(), a.diGetCmd(), a.diCreateCmd(), a.diUpdateCmd(), a.diDisableCmd(), a.diDeleteCmd(), a.diScopesCmd())
	return cmd
}

func (a *app) diClient() (*deepinfra.Client, error) {
	tok := strings.TrimSpace(os.Getenv(EnvDIKey))
	if tok == "" {
		return nil, fmt.Errorf("%s is not set", EnvDIKey)
	}
	return deepinfra.New(a.diBaseURL, tok, a.timeout), nil
}

func diList(ctx context.Context, c *deepinfra.Client) ([]diToken, error) {
	limits, err := c.KeyLimits(ctx)
	if err != nil {
		return nil, fmt.Errorf("key limits: %w", err)
	}
	tokens, err := c.ListTokens(ctx)
	if deepinfra.IsForbidden(err) {
		fmt.Fprintln(os.Stderr, "warning: this key cannot list tokens; showing key limits only (no scopes, no creation time)")
		tokens = deepinfra.TokensFromLimits(limits)
	} else if err != nil {
		return nil, err
	}
	byID := map[string]deepinfra.KeyLimit{}
	for _, l := range limits {
		byID[l.TokenID] = l
	}
	out := make([]diToken, 0, len(tokens))
	for _, t := range tokens {
		l := byID[t.TokenID]
		out = append(out, diToken{Token: t, Limit: l.Limit, Spend: l.Spend})
	}
	return out, nil
}

func diResolve(tokens []diToken, ref string) (*diToken, error) {
	var byName []*diToken
	for i := range tokens {
		if tokens[i].TokenID == ref {
			return &tokens[i], nil
		}
		if tokens[i].Name == ref {
			byName = append(byName, &tokens[i])
		}
	}
	switch len(byName) {
	case 0:
		return nil, fmt.Errorf("no token matches %q", ref)
	case 1:
		return byName[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d tokens, use the token ID:", ref, len(byName))
	for _, t := range byName {
		fmt.Fprintf(&b, "\n  %s  %s", t.TokenID, t.Name)
	}
	return nil, errors.New(b.String())
}

func (a *app) writeDITokens(w io.Writer, tokens []diToken) error {
	if a.output == "json" {
		if tokens == nil {
			tokens = []diToken{}
		}
		return printJSON(w, tokens)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if a.output == "wide" {
		fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tLIMIT\tSPEND\tSCOPES\tPREFIX\tCREATED")
	} else {
		fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tLIMIT\tSPEND\tCREATED")
	}
	for _, t := range tokens {
		status := "active"
		if t.Limit != nil && *t.Limit == 0 {
			status = "blocked"
		}
		created := "-"
		if t.CreatedAt > 0 {
			created = t.Created().Format("2006-01-02 15:04")
		}
		if a.output == "wide" {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.TokenID, t.Name, status, values.USDPtr(t.Limit),
				values.USDPtr(t.Spend), strings.Join(t.Scopes, ","), t.Prefix+"..."+t.Fragment, created)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.TokenID, t.Name, status, values.USDPtr(t.Limit), values.USDPtr(t.Spend), created)
		}
	}
	return tw.Flush()
}

func (a *app) writeDIToken(w io.Writer, t diToken) error {
	if a.output == "json" {
		return printJSON(w, t)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"ID", t.TokenID},
		{"Name", t.Name},
		{"Limit (monthly)", values.USDPtr(t.Limit)},
		{"Spend (this month)", values.USDPtr(t.Spend)},
		{"Scopes", strings.Join(t.Scopes, ", ")},
		{"Allowed IPs", strings.Join(t.AllowedIPs, ", ")},
		{"Display", t.Prefix + "..." + t.Fragment},
		{"Created", t.Created().Format("2006-01-02 15:04")},
	} {
		fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	return tw.Flush()
}

func (a *app) diListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List tokens with limit and spend",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.diClient()
			if err != nil {
				return err
			}
			tokens, err := diList(cmd.Context(), c)
			if err != nil {
				return err
			}
			return a.writeDITokens(os.Stdout, tokens)
		},
	}
}

func (a *app) diGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <token>",
		Short: "Show one token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.diClient()
			if err != nil {
				return err
			}
			tokens, err := diList(cmd.Context(), c)
			if err != nil {
				return err
			}
			t, err := diResolve(tokens, args[0])
			if err != nil {
				return err
			}
			return a.writeDIToken(os.Stdout, *t)
		},
	}
}

func (a *app) diCreateCmd() *cobra.Command {
	var (
		name, limit string
		scopes      []string
		quiet       bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a token (the secret is shown only once)",
		Example: `  orkeys di create --name ci --limit 20
  KEY=$(orkeys di create --name tmp --scopes inference,models -q)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(name) == "" {
				return errors.New("--name is required")
			}
			c, err := a.diClient()
			if err != nil {
				return err
			}
			var lim *float64
			if cmd.Flags().Changed("limit") {
				v, err := values.ParseLimit(limit)
				if err != nil {
					return err
				}
				lim = v.Ptr()
			}
			ctx := cmd.Context()
			t, err := c.CreateToken(ctx, name, scopes)
			if err != nil {
				return err
			}
			if lim != nil {
				if err := c.SetKeyLimit(ctx, t.TokenID, lim); err != nil {
					if derr := c.DeleteToken(ctx, t.TokenID); derr != nil {
						return fmt.Errorf("set limit: %w; delete new token %s also failed: %w", err, t.TokenID, derr)
					}
					return fmt.Errorf("set limit (new token deleted): %w", err)
				}
			}
			switch {
			case quiet:
				fmt.Println(t.Secret)
			case a.output == "json":
				return printJSON(os.Stdout, map[string]any{"token": diToken{Token: t.Token, Limit: lim}, "key": t.Secret})
			default:
				if err := a.writeDIToken(os.Stdout, diToken{Token: t.Token, Limit: lim}); err != nil {
					return err
				}
				fmt.Printf("\nKey: %s\n", t.Secret)
				fmt.Fprintln(os.Stderr, "NOTE: save this key now. DeepInfra does not show it again.")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&name, "name", "n", "", "token name (required)")
	f.StringSliceVar(&scopes, "scopes", []string{"inference"}, "scopes (see `orkeys di scopes`)")
	f.StringVar(&limit, "limit", "", "monthly limit in USD, or \"none\"")
	f.BoolVarP(&quiet, "quiet", "q", false, "print only the secret key")
	return cmd
}

func (a *app) diUpdateCmd() *cobra.Command {
	var (
		limit  string
		scopes []string
	)
	cmd := &cobra.Command{
		Use:   "update <token>",
		Short: "Change the monthly limit or the scopes of a token",
		Example: `  orkeys di update ci --limit 50
  orkeys di update ci --limit none --scopes inference,models`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if !f.Changed("limit") && !f.Changed("scopes") {
				return errors.New("nothing to change: pass --limit or --scopes")
			}
			c, err := a.diClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			tokens, err := diList(ctx, c)
			if err != nil {
				return err
			}
			t, err := diResolve(tokens, args[0])
			if err != nil {
				return err
			}
			if f.Changed("scopes") {
				nt, err := c.UpdateScopes(ctx, t.TokenID, scopes)
				if err != nil {
					return err
				}
				t.Token = *nt
			}
			if f.Changed("limit") {
				v, err := values.ParseLimit(limit)
				if err != nil {
					return err
				}
				if err := c.SetKeyLimit(ctx, t.TokenID, v.Ptr()); err != nil {
					return err
				}
				t.Limit = v.Ptr()
			}
			return a.writeDIToken(os.Stdout, *t)
		},
	}
	cmd.Flags().StringVar(&limit, "limit", "", "monthly limit in USD; 0 blocks the token; \"none\" removes the limit")
	cmd.Flags().StringSliceVar(&scopes, "scopes", nil, "replace the scopes")
	return cmd
}

func (a *app) diDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <token>...",
		Short: "Block tokens (sets the monthly limit to 0; undo with update --limit)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.diClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			tokens, err := diList(ctx, c)
			if err != nil {
				return err
			}
			zero := 0.0
			var failed int
			for _, ref := range args {
				t, err := diResolve(tokens, ref)
				if err == nil {
					err = c.SetKeyLimit(ctx, t.TokenID, &zero)
				}
				if err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "orkeys: %s: %v\n", ref, err)
					continue
				}
				fmt.Printf("disabled %s (%s)\n", t.Name, t.TokenID)
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d failed", failed, len(args))
			}
			return nil
		},
	}
}

func (a *app) diDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <token>...",
		Aliases: []string{"rm"},
		Short:   "Delete one or more tokens",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.diClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			tokens, err := diList(ctx, c)
			if err != nil {
				return err
			}
			var targets []*diToken
			for _, ref := range args {
				t, err := diResolve(tokens, ref)
				if err != nil {
					return fmt.Errorf("%s: %w", ref, err)
				}
				targets = append(targets, t)
			}
			if !yes {
				if !isTTY() {
					return errors.New("refusing to delete without confirmation: pass --yes")
				}
				fmt.Println("Tokens to delete:")
				for _, t := range targets {
					fmt.Printf("  %s  %s\n", t.TokenID, t.Name)
				}
				if !confirm(fmt.Sprintf("Delete %d token(s)?", len(targets))) {
					return errors.New("aborted")
				}
			}
			var failed int
			for _, t := range targets {
				if err := c.DeleteToken(ctx, t.TokenID); err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "orkeys: %s: %v\n", t.Name, err)
					continue
				}
				fmt.Printf("deleted %s (%s)\n", t.Name, t.TokenID)
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

func (a *app) diScopesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scopes",
		Short: "List the scopes a token can have",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.diClient()
			if err != nil {
				return err
			}
			scopes, err := c.Scopes(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return printJSON(os.Stdout, scopes)
			}
			for _, s := range scopes {
				def := ""
				if s.IsDefault {
					def = " (default)"
				}
				fmt.Printf("%s%s\n", s.Name, def)
			}
			return nil
		},
	}
}
