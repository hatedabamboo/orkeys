package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/hatedabamboo/orkeys/internal/backend"
	"github.com/hatedabamboo/orkeys/internal/engine"
	"github.com/hatedabamboo/orkeys/internal/state"
)

func (a *app) unlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock [lock-id]",
		Short: "Show the state lock, or remove it by ID after a crashed run",
		Long: `Without an argument, unlock shows the current state lock.
With the lock ID from the "state is locked" error, it removes that lock.
Remove a lock only when no orkeys run holds it. An expired lock is taken over automatically.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inv, err := a.loadInventory()
			if err != nil {
				return err
			}
			var f backend.Factory
			st, err := f.State(cmd.Context(), inv.Policy.State)
			if err != nil {
				return err
			}
			l, ok := st.(state.Locker)
			if !ok {
				return errors.New("this state store has no lock")
			}
			cur, err := l.Current(cmd.Context())
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if cur == nil {
					fmt.Println("Not locked.")
					return nil
				}
				fmt.Println((&state.LockedError{Info: *cur}).Error())
				return nil
			}
			if cur == nil {
				fmt.Println("Not locked.")
				return nil
			}
			if err := l.Release(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("Removed lock %s of %s.\n", cur.ID, cur.Holder)
			return nil
		},
	}
}

func (a *app) inventoryCmds() []*cobra.Command {
	return []*cobra.Command{
		a.unlockCmd(),
		a.validateCmd(), a.planCmd(), a.applyCmd(), a.rotateCmd(),
		a.requestCmd(), a.issuedCmd(), a.importCmd(), a.hygieneCmd(), a.exporterCmd(),
	}
}

func (a *app) validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the inventory repository against policy.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inv, err := a.loadInventory()
			if err != nil {
				return err
			}
			fmt.Printf("ok: %d keys, %d teams\n", len(inv.Keys), len(inv.Teams))
			return nil
		},
	}
}

func (a *app) planCmd() *cobra.Command {
	var markdown bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what apply would change",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := a.engine(cmd.Context(), "")
			if err != nil {
				return err
			}
			plan, err := e.Plan(cmd.Context())
			if err != nil {
				return err
			}
			switch {
			case a.output == "json":
				return printJSON(os.Stdout, plan)
			case markdown:
				writePlanMarkdown(os.Stdout, plan)
			default:
				writePlan(os.Stdout, plan)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&markdown, "markdown", false, "write the plan as Markdown (for PR comments)")
	return cmd
}

func (a *app) applyCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Make providers and the secrets store match the inventory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes && (!isTTY() || a.output == "json") {
				return errors.New("refusing to apply without confirmation: pass --yes")
			}
			e, err := a.engine(cmd.Context(), "")
			if err != nil {
				return err
			}
			var plan *engine.Plan
			err = a.withLock(cmd.Context(), e, "apply", func(ctx context.Context) error {
				var err error
				if plan, err = e.Plan(ctx); err != nil {
					return err
				}
				if a.output != "json" {
					writePlan(os.Stdout, plan)
				}
				if plan.Empty() {
					return nil
				}
				if !yes && !confirm("Apply these changes?") {
					return errors.New("aborted")
				}
				return e.Apply(ctx, plan)
			})
			return a.finish(map[string]any{"plan": plan}, err)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func (a *app) rotateCmd() *cobra.Command {
	var opt engine.RotateOptions
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Rotate production keys, expire personal and local keys, revoke old keys",
		Example: `  orkeys rotate
  orkeys rotate --now prod-search-embeddings
  orkeys rotate --retry prod-search-embeddings`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := a.engine(cmd.Context(), "")
			if err != nil {
				return err
			}
			err = a.withLock(cmd.Context(), e, "rotate", func(ctx context.Context) error {
				return e.Rotate(ctx, opt)
			})
			return a.finish(nil, err)
		},
	}
	cmd.Flags().StringSliceVar(&opt.Retry, "retry", nil, "retry a failed rotation of these keys")
	cmd.Flags().StringSliceVar(&opt.Now, "now", nil, "rotate these manifest keys now, before they are due")
	cmd.Flags().StringSliceVar(&opt.Only, "only", nil, "process only these keys")
	return cmd
}

func (a *app) requestCmd() *cobra.Command {
	var (
		r      engine.Request
		outDir string
	)
	cmd := &cobra.Command{
		Use:   "request",
		Short: "Issue a self-service key, encrypted to the user's GitHub SSH keys",
		Example: `  orkeys request --user jdoe --kind personal --provider openrouter --purpose "persona evals" --limit 30
  orkeys request --user jdoe --kind local --provider openrouter --purpose "local dev"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if r.User == "" || r.Provider == "" || r.Kind == "" {
				return errors.New("--user, --kind and --provider are required")
			}
			e, err := a.engine(cmd.Context(), outDir)
			if err != nil {
				return err
			}
			var is *engine.Issued
			err = a.withLock(cmd.Context(), e, "request", func(ctx context.Context) error {
				is, err = e.Request(ctx, r)
				return err
			})
			if a.output == "json" || err != nil {
				return a.finish(map[string]any{"issued": is}, err)
			}
			fmt.Printf("Issued %s (%s), limit $%.2f, expires %s.\nEncrypted key: %s\nDecrypt: age -d -i ~/.ssh/id_ed25519 %s\n",
				is.Name, is.Provider, is.LimitUSD, is.ExpiresAt.Format(time.DateOnly), is.File, filepath.Base(is.File))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&r.User, "user", "", "GitHub login of the requester")
	f.StringVar(&r.Kind, "kind", "", "a kind with source: request in policy.yaml")
	f.StringVar(&r.Provider, "provider", "", "provider name")
	f.StringVar(&r.Purpose, "purpose", "", "what the key is for (required)")
	f.Float64Var(&r.LimitUSD, "limit", 0, "limit in USD per kind reset period (default: kind budget.limit)")
	f.DurationVar(&r.TTL, "ttl", 0, "shorter TTL than the policy maximum, e.g. 72h")
	f.StringVar(&outDir, "out", "out", "directory for the encrypted key")
	return cmd
}

func (a *app) issuedCmd() *cobra.Command {
	var f engine.IssuedFilter
	var markdown bool
	cmd := &cobra.Command{
		Use:   "issued",
		Short: "List active personal and local keys",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := a.engine(cmd.Context(), "")
			if err != nil {
				return err
			}
			keys, err := e.Issued(cmd.Context(), f)
			if err != nil {
				return err
			}
			if a.output == "json" {
				if keys == nil {
					keys = []engine.IssuedKey{}
				}
				return printJSON(os.Stdout, keys)
			}
			writeIssued(os.Stdout, keys, markdown)
			return nil
		},
	}
	cmd.Flags().StringVar(&f.Team, "team", "", "only this team")
	cmd.Flags().StringVar(&f.Owner, "owner", "", "only this GitHub login")
	cmd.Flags().StringVar(&f.Kind, "kind", "", "only keys of this kind")
	cmd.Flags().BoolVar(&markdown, "markdown", false, "write a Markdown table")
	return cmd
}

func (a *app) importCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import",
		Short: "Print a manifest with an import block for each provider key that orkeys does not manage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := a.engine(cmd.Context(), "")
			if err != nil {
				return err
			}
			found, err := e.Import(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return printJSON(os.Stdout, found)
			}
			if len(found) == 0 {
				fmt.Fprintln(os.Stderr, "No unmanaged keys.")
				return nil
			}
			writeImportStubs(os.Stdout, found)
			return nil
		},
	}
}

func (a *app) hygieneCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "hygiene",
		Short: "Revoke keys of people who left the GitHub org, list unused keys",
		Long: `hygiene changes keys unless you pass --dry-run:
  - revokes requested keys whose owner is not in github.org (after a sanity check of the org)
  - lists manifest keys with no use for 30 days`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := a.engine(cmd.Context(), "")
			if err != nil {
				return err
			}
			if e.Org == nil {
				fmt.Fprintln(os.Stderr, "warning: GITHUB_TOKEN or github.org not set; owner checks skipped")
			}
			var fs []engine.Finding
			run := func(ctx context.Context) error {
				var err error
				fs, err = e.Hygiene(ctx, dryRun)
				return err
			}
			if dryRun {
				err = run(cmd.Context())
			} else {
				err = a.withLock(cmd.Context(), e, "hygiene", run)
			}
			if a.output == "json" {
				if fs == nil {
					fs = []engine.Finding{}
				}
				return a.finish(map[string]any{"findings": fs}, err)
			}
			for _, f := range fs {
				mark := " "
				if f.Done {
					mark = "x"
				}
				fmt.Printf("[%s] %-11s %s: %s\n", mark, f.Kind, f.Name, f.Detail)
			}
			if len(fs) == 0 {
				fmt.Println("Nothing found.")
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report only, change nothing")
	return cmd
}
