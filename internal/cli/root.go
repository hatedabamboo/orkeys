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

	"github.com/hatedabamboo/orkeys/internal/api"
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
		Short: "Manage OpenRouter API keys with the Management API",
		Long: `orkeys manages OpenRouter API keys with a management key.

Run without a command in a terminal to start interactive mode.

Environment:
  ` + EnvKey + `   management API key (required)
  ` + EnvBase + `             API base URL (default ` + api.DefaultBaseURL + `)
  ` + EnvWorkspace + `         workspace ID for list/create/lookup (optional)

Commands that take <key> accept a full hash, a unique hash prefix, or an exact key name.`,
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isTTY() {
				return cmd.Help()
			}
			return a.runInteractive(cmd.Context())
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&a.baseURL, "base-url", envOr(EnvBase, api.DefaultBaseURL), "API base URL (env "+EnvBase+")")
	pf.StringVarP(&a.workspace, "workspace", "w", os.Getenv(EnvWorkspace), "workspace ID (env "+EnvWorkspace+")")
	pf.DurationVar(&a.timeout, "timeout", 30*time.Second, "HTTP request timeout")
	pf.StringVarP(&a.output, "output", "o", "table", "output format: table|wide|json")
	pf.BoolVar(&a.json, "json", false, "shorthand for --output json")

	root.AddCommand(
		a.listCmd(),
		a.getCmd(),
		a.createCmd(),
		a.updateCmd(),
		a.toggleCmd("enable", false),
		a.toggleCmd("disable", true),
		a.deleteCmd(),
		a.interactiveCmd(),
	)
	return root
}

func (a *app) client() (*api.Client, error) {
	tok := strings.TrimSpace(os.Getenv(EnvKey))
	if tok == "" {
		return nil, fmt.Errorf("%s is not set", EnvKey)
	}
	return api.New(a.baseURL, tok, a.timeout, "orkeys/"+a.version), nil
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
