// Package cli holds the cobra command tree.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hatedabamboo/orkeys/internal/openrouter"
)

// Environment variables.
const (
	EnvKey       = "OPENROUTER_MANAGEMENT_API_KEY"
	EnvBase      = "OPENROUTER_API_BASE"
	EnvWorkspace = "OPENROUTER_WORKSPACE_ID"
)

type app struct {
	version   string
	baseURL   string
	workspace string
	timeout   time.Duration
	output    string
	json      bool
	repo      string
	lockWait  time.Duration
	diBaseURL string
	events    *eventPrinter
}

// Execute runs the CLI and returns the process exit code.
func Execute(version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := &app{version: version}
	if err := a.root().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "orkeys:", err)
		return 1
	}
	return 0
}

func (a *app) root() *cobra.Command {
	root := &cobra.Command{
		Use:   "orkeys",
		Short: "Manage LLM API keys: provider commands and an inventory with rotation and alerts",
		Long: `orkeys manages LLM API keys.

Provider commands manage keys directly:
  orkeys openrouter (or) ...   OpenRouter keys
  orkeys deepinfra (di) ...    DeepInfra API tokens

Inventory commands (validate, plan, apply, rotate, request, issued, import, hygiene, unlock, exporter)
manage keys of all providers from an inventory repository, with state and secrets in Vault or AWS.

Environment for inventory commands:
  ` + EnvRepo + `                     inventory repository path (same as --repo)
  VAULT_ADDR, VAULT_TOKEN         Vault access (or VAULT_ROLE_ID + VAULT_SECRET_ID, or VAULT_K8S_ROLE)
  VAULT_CLIENT_CERT, VAULT_CLIENT_KEY, VAULT_CACERT
                                  Vault TLS: client certificate and key (mTLS), CA bundle
  AWS_REGION, AWS_PROFILE, ...    AWS access for s3:// state and aws secrets
  ORKEYS_<PROVIDER>_KEY           management key; overrides providers[].management_key.
                                  <PROVIDER> is the provider name in upper case, "-" as "_"
  ` + EnvGitHubToken + `                    team members from GitHub teams; org membership checks in hygiene`,
		Version:       a.version,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if a.json {
				if cmd.Flags().Changed("output") && a.output != "json" {
					return fmt.Errorf("--json conflicts with --output %s", a.output)
				}
				a.output = "json"
			}
			switch a.output {
			case "table", "wide", "json":
				return nil
			}
			return fmt.Errorf("invalid --output %q: want table|wide|json", a.output)
		},
	}

	pf := root.PersistentFlags()
	pf.DurationVar(&a.timeout, "timeout", 30*time.Second, "HTTP request timeout")
	pf.StringVarP(&a.output, "output", "o", "table", "output format: table|wide|json")
	pf.BoolVar(&a.json, "json", false, "shorthand for --output json")
	pf.StringVar(&a.repo, "repo", envOr(EnvRepo, "."), "path to the inventory repository (env "+EnvRepo+")")
	pf.DurationVar(&a.lockWait, "lock-wait", 5*time.Minute, "how long to wait for the state lock")

	root.AddCommand(a.openrouterCmd(), a.deepinfraCmd())
	root.AddCommand(a.inventoryCmds()...)
	return root
}

func (a *app) client() (*openrouter.Client, error) {
	tok := strings.TrimSpace(os.Getenv(EnvKey))
	if tok == "" {
		return nil, fmt.Errorf("%s is not set", EnvKey)
	}
	return openrouter.New(a.baseURL, tok, a.timeout, "orkeys/"+a.version), nil
}

func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
