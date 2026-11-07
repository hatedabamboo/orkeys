package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hatedabamboo/orkeys/internal/openrouter"
	"github.com/hatedabamboo/orkeys/internal/values"
)

func (a *app) openrouterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "openrouter",
		Aliases: []string{"or"},
		Short:   "Manage OpenRouter keys directly",
		Long: `Manage OpenRouter keys with a management key.

Environment:
  ` + EnvKey + `   management API key (required)
  ` + EnvBase + `             API base URL (default ` + openrouter.DefaultBaseURL + `)
  ` + EnvWorkspace + `         workspace ID for list/create/lookup (optional)

Commands that take <key> accept a full hash, a unique hash prefix, or an exact key name.`,
		Args: cobra.NoArgs,
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&a.baseURL, "base-url", envOr(EnvBase, openrouter.DefaultBaseURL), "API base URL (env "+EnvBase+")")
	pf.StringVarP(&a.workspace, "workspace", "w", os.Getenv(EnvWorkspace), "workspace ID (env "+EnvWorkspace+")")
	cmd.AddCommand(
		a.listCmd(),
		a.getCmd(),
		a.createCmd(),
		a.updateCmd(),
		a.toggleCmd("enable", false),
		a.toggleCmd("disable", true),
		a.deleteCmd(),
	)
	return cmd
}

func (a *app) listCmd() *cobra.Command {
	var (
		offset          int
		includeDisabled bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List keys (all pages by default)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			opts := openrouter.ListOptions{IncludeDisabled: includeDisabled, WorkspaceID: a.workspace}
			var keys []openrouter.Key
			if cmd.Flags().Changed("offset") {
				opts.Offset = offset
				keys, err = c.List(cmd.Context(), opts)
			} else {
				keys, err = c.ListAll(cmd.Context(), opts)
			}
			if err != nil {
				return err
			}
			if keys == nil {
				keys = []openrouter.Key{}
			}
			return a.printKeys(keys)
		},
	}
	cmd.Flags().IntVar(&offset, "offset", 0, "fetch a single page starting at this offset")
	cmd.Flags().BoolVarP(&includeDisabled, "include-disabled", "d", false, "include disabled keys")
	return cmd
}

func (a *app) getCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Show one key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			hash, err := a.resolve(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			k, err := c.Get(cmd.Context(), hash)
			if err != nil {
				return err
			}
			return a.printKey(*k)
		},
	}
}

func (a *app) createCmd() *cobra.Command {
	var (
		name, limit, reset, expires, creator string
		byok, quiet                          bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a key (the secret is shown only once)",
		Example: `  orkeys or create --name ci --limit 20 --limit-reset monthly --expires 90d
  KEY=$(orkeys or create --name tmp --expires 12h -q)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(name) == "" {
				return errors.New("--name is required")
			}
			req := openrouter.CreateRequest{Name: name, WorkspaceID: a.workspace, CreatorUserID: creator}
			f := cmd.Flags()
			if f.Changed("limit") {
				v, err := values.ParseLimit(limit)
				if err != nil {
					return err
				}
				req.Limit = v.Ptr()
			}
			if f.Changed("limit-reset") {
				v, err := values.ParseLimitReset(reset)
				if err != nil {
					return err
				}
				req.LimitReset = v.Ptr()
			}
			if f.Changed("byok") {
				req.IncludeBYOKInLimit = &byok
			}
			if f.Changed("expires") {
				v, err := values.ParseExpiry(expires, time.Now())
				if err != nil {
					return err
				}
				req.ExpiresAt = v
			}

			c, err := a.client()
			if err != nil {
				return err
			}
			resp, err := c.Create(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch {
			case quiet:
				fmt.Println(resp.Key)
			case a.output == "json":
				return printJSON(os.Stdout, resp)
			default:
				if err := a.printKey(resp.Data); err != nil {
					return err
				}
				fmt.Printf("\nKey: %s\n", resp.Key)
				fmt.Fprintln(os.Stderr, "NOTE: save this key now. OpenRouter does not show it again.")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&name, "name", "n", "", "key name (required)")
	f.StringVar(&limit, "limit", "", "credit limit in USD, or \"none\"")
	f.StringVar(&reset, "limit-reset", "", "limit reset: daily|weekly|monthly|none")
	f.BoolVar(&byok, "byok", false, "count BYOK usage toward the limit")
	f.StringVar(&expires, "expires", "", "expiry: duration (30d, 2w, 12h), date (2027-12-31) or RFC 3339")
	f.StringVar(&creator, "creator-user-id", "", "creator user ID (organization keys only)")
	f.BoolVarP(&quiet, "quiet", "q", false, "print only the secret key")
	return cmd
}

func (a *app) updateCmd() *cobra.Command {
	var (
		name, limit, reset string
		disabled, byok     bool
	)
	cmd := &cobra.Command{
		Use:   "update <key>",
		Short: "Change fields of a key",
		Example: `  orkeys or update ci --limit 50 --limit-reset weekly
  orkeys or update 3f2a9c --limit none --name "ci (old)"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var req openrouter.UpdateRequest
			f := cmd.Flags()
			if f.Changed("name") {
				req.Name = &name
			}
			if f.Changed("limit") {
				v, err := values.ParseLimit(limit)
				if err != nil {
					return err
				}
				req.Limit = v
			}
			if f.Changed("limit-reset") {
				v, err := values.ParseLimitReset(reset)
				if err != nil {
					return err
				}
				req.LimitReset = v
			}
			if f.Changed("disabled") {
				req.Disabled = &disabled
			}
			if f.Changed("byok") {
				req.IncludeBYOKInLimit = &byok
			}
			if req.Empty() {
				return errors.New("nothing to change: pass at least one flag")
			}

			c, err := a.client()
			if err != nil {
				return err
			}
			hash, err := a.resolve(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			k, err := c.Update(cmd.Context(), hash, req)
			if err != nil {
				return err
			}
			return a.printKey(*k)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&name, "name", "n", "", "new name")
	f.StringVar(&limit, "limit", "", "credit limit in USD, or \"none\" to remove")
	f.StringVar(&reset, "limit-reset", "", "limit reset: daily|weekly|monthly|none")
	f.BoolVar(&disabled, "disabled", false, "disable (true) or enable (false) the key")
	f.BoolVar(&byok, "byok", false, "count BYOK usage toward the limit")
	return cmd
}

func (a *app) toggleCmd(use string, disable bool) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <key>...",
		Short: strings.ToUpper(use[:1]) + use[1:] + " one or more keys",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var failed int
			for _, ref := range args {
				hash, err := a.resolve(cmd.Context(), c, ref)
				if err == nil {
					var k *openrouter.Key
					k, err = c.Update(cmd.Context(), hash, openrouter.UpdateRequest{Disabled: &disable})
					if err == nil {
						fmt.Printf("%sd %s (%s)\n", use, k.Name, values.ShortHash(k.Hash))
						continue
					}
				}
				failed++
				fmt.Fprintf(os.Stderr, "orkeys: %s: %v\n", ref, err)
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d failed", failed, len(args))
			}
			return nil
		},
	}
}

func (a *app) deleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <key>...",
		Aliases: []string{"rm"},
		Short:   "Delete one or more keys",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			// Resolve everything first, so a typo aborts before any delete.
			var targets []openrouter.Key
			for _, ref := range args {
				hash, err := a.resolve(ctx, c, ref)
				if err != nil {
					return fmt.Errorf("%s: %w", ref, err)
				}
				k, err := c.Get(ctx, hash)
				if err != nil {
					return fmt.Errorf("%s: %w", ref, err)
				}
				targets = append(targets, *k)
			}

			if !yes {
				if !isTTY() {
					return errors.New("refusing to delete without confirmation: pass --yes")
				}
				fmt.Println("Keys to delete:")
				for _, k := range targets {
					fmt.Printf("  %s  %s\n", values.ShortHash(k.Hash), k.Name)
				}
				if !confirm(fmt.Sprintf("Delete %d key(s)?", len(targets))) {
					return errors.New("aborted")
				}
			}

			var failed int
			for _, k := range targets {
				if err := c.Delete(ctx, k.Hash); err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "orkeys: %s: %v\n", k.Name, err)
					continue
				}
				fmt.Printf("deleted %s (%s)\n", k.Name, values.ShortHash(k.Hash))
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

// resolve turns a full hash, unique hash prefix or exact name into a full hash.
func (a *app) resolve(ctx context.Context, c *openrouter.Client, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if values.IsFullHash(ref) {
		return ref, nil
	}
	if ref == "" {
		return "", errors.New("empty key reference")
	}
	keys, err := c.ListAll(ctx, openrouter.ListOptions{IncludeDisabled: true, WorkspaceID: a.workspace})
	if err != nil {
		return "", err
	}

	var byName, byPrefix []openrouter.Key
	for _, k := range keys {
		if k.Name == ref {
			byName = append(byName, k)
		}
		if strings.HasPrefix(k.Hash, strings.ToLower(ref)) {
			byPrefix = append(byPrefix, k)
		}
	}
	matches := byName
	if len(matches) == 0 {
		matches = byPrefix
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no key matches %q", ref)
	case 1:
		return matches[0].Hash, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d keys, use a longer hash prefix:", ref, len(matches))
	for _, k := range matches {
		fmt.Fprintf(&b, "\n  %s  %s", values.ShortHash(k.Hash), k.Name)
	}
	return "", errors.New(b.String())
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
