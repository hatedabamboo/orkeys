package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
			l, err := f.State(cmd.Context(), inv.Policy.State)
			if err != nil {
				return err
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
		a.requestCmd(), a.issuedCmd(), a.revokeCmd(), a.stateCmd(),
		a.importCmd(), a.hygieneCmd(), a.exporterCmd(),
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
	var (
		markdown bool
		out      string
	)
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
			if out != "" {
				if err := writePlanFile(out, plan); err != nil {
					return err
				}
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
	cmd.Flags().StringVar(&out, "out", "", "also save the plan as JSON to this file, for apply --plan")
	return cmd
}

func writePlanFile(path string, plan *engine.Plan) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := printJSON(f, plan); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func readPlanFile(path string) (*engine.Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p engine.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("plan file %s: %w", path, err)
	}
	if p.Fingerprint == "" {
		return nil, fmt.Errorf("plan file %s: no fingerprint; save it with plan --out", path)
	}
	return &p, nil
}

func (a *app) applyCmd() *cobra.Command {
	var (
		yes      bool
		opt      engine.ApplyOptions
		planFile string
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Make providers and the secrets store match the inventory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes && (!isTTY() || a.output == "json") {
				return errors.New("refusing to apply without confirmation: pass --yes")
			}
			var saved *engine.Plan
			if planFile != "" {
				var err error
				if saved, err = readPlanFile(planFile); err != nil {
					return err
				}
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
				if saved != nil {
					if err := saved.Matches(plan); err != nil {
						return err
					}
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
				return e.Apply(ctx, plan, opt)
			})
			return a.finish(map[string]any{"plan": plan}, err)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().IntVar(&opt.AllowDeletes, "allow-deletes", 0, "allow this run to revoke up to this many keys (above policy max_deletes)")
	cmd.Flags().StringVar(&planFile, "plan", "", "apply only if the plan is the same as this file from plan --out")
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
  orkeys request --user jdoe --kind local --provider openrouter --purpose "local dev"
  orkeys request --user jdoe --team search --kind local --provider openrouter --purpose "local dev"`,
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
	f.StringVar(&r.Team, "team", "", "team of the requester (required if the requester is in several teams)")
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

func (a *app) revokeCmd() *cobra.Command {
	var (
		yes    bool
		reason string
	)
	cmd := &cobra.Command{
		Use:   "revoke <name>...",
		Short: "Revoke requested keys before they expire and remove their state",
		Long: `revoke deletes requested keys (kinds with source: request) at the provider, removes their secret
fields and GitHub secrets, and removes their state. It does not revoke manifest keys: delete the
manifest or set disabled: true instead. Get the names from orkeys issued.`,
		Example: `  orkeys revoke personal-jdoe-20261101-120000
  orkeys revoke --reason "laptop lost" local-jdoe-20261101-120000`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && (!isTTY() || a.output == "json") {
				return errors.New("refusing to revoke without confirmation: pass --yes")
			}
			e, err := a.engine(cmd.Context(), "")
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Revoke %s?", strings.Join(args, ", "))) {
				return errors.New("aborted")
			}
			err = a.withLock(cmd.Context(), e, "revoke", func(ctx context.Context) error {
				return e.Revoke(ctx, args, reason)
			})
			return a.finish(map[string]any{"revoked": args}, err)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().StringVar(&reason, "reason", "", "why the key is revoked; goes to the key_revoked event")
	return cmd
}

func (a *app) stateCmd() *cobra.Command {
	var f engine.TrackedFilter
	cmd := &cobra.Command{
		Use:   "state [name]",
		Short: "List the keys in the state store, or show one state entry",
		Long: `Without an argument, state lists all keys in the state store. SOURCE is manifest, request, or
orphan (no manifest and not a requested key; the next apply revokes it).
With a name, state prints that state entry as JSON.
state reads only the state store. It needs no provider management keys.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inv, err := a.loadInventory()
			if err != nil {
				return err
			}
			var bf backend.Factory
			store, err := bf.State(cmd.Context(), inv.Policy.State)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				st, err := store.GetState(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if st == nil {
					return fmt.Errorf("key %s: not in state", args[0])
				}
				return printJSON(os.Stdout, engine.Tracked(inv, []*state.State{st}, engine.TrackedFilter{})[0])
			}
			states, err := store.ListStates(cmd.Context())
			if err != nil {
				return err
			}
			keys := engine.Tracked(inv, states, f)
			if a.output == "json" {
				return printJSON(os.Stdout, keys)
			}
			writeTracked(os.Stdout, keys)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.Team, "team", "", "only this team")
	fl.StringVar(&f.Kind, "kind", "", "only keys of this kind")
	fl.StringVar(&f.Owner, "owner", "", "only keys of this GitHub login")
	fl.StringVar(&f.Provider, "provider", "", "only keys of this provider")
	fl.StringVar(&f.Source, "source", "", "only keys of this source: manifest, request or orphan")
	return cmd
}

func (a *app) importCmd() *cobra.Command {
	var trackOnly bool
	cmd := &cobra.Command{
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
			writeImportStubs(os.Stdout, found, trackOnly)
			return nil
		},
	}
	cmd.Flags().BoolVar(&trackOnly, "track-only", false, "print manifests with track_only: true and no secret")
	return cmd
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
