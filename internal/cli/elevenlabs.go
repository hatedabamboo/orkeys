package cli

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hatedabamboo/orkeys/internal/elevenlabs"
	"github.com/hatedabamboo/orkeys/internal/provider"
	"github.com/hatedabamboo/orkeys/internal/values"
)

const (
	EnvELKey            = "ELEVENLABS_ADMIN_KEY"
	EnvELBase           = "ELEVENLABS_API_BASE"
	EnvELServiceAccount = "ELEVENLABS_SERVICE_ACCOUNT"
	EnvELPrice          = "ELEVENLABS_USD_PER_CREDIT"
)

// elKey is a key with its spend and limit in USD. They are set only when the price per credit is known.
type elKey struct {
	elevenlabs.Key
	SpendUSD *float64 `json:"spend_usd,omitempty"`
	LimitUSD *float64 `json:"limit_usd,omitempty"`
}

func (a *app) elevenlabsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "elevenlabs",
		Aliases: []string{"el"},
		Short:   "Manage ElevenLabs service account API keys directly",
		Long: `Manage the API keys of one ElevenLabs service account.
Service accounts are only for multi-seat workspaces. create, update, disable, enable and delete need
a management key with the permission service_account_write, and an owner of that key in a group with
the permission service_accounts_manage. disable and delete refuse the management key.

Limits are in credits per billing period of the workspace. Set ` + EnvELPrice + ` to also see USD.

Environment:
  ` + EnvELKey + `         management key (required)
  ` + EnvELServiceAccount + `   service_account_user_id (required except for service-accounts, same as --service-account)
  ` + EnvELBase + `          API base URL (default ` + elevenlabs.DefaultBaseURL + `)
  ` + EnvELPrice + `    price of one credit in USD (optional)

Commands that take <key> accept a key ID or an exact key name.`,
		Args: cobra.NoArgs,
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&a.elBaseURL, "base-url", envOr(EnvELBase, elevenlabs.DefaultBaseURL), "API base URL (env "+EnvELBase+")")
	pf.StringVarP(&a.elSA, "service-account", "s", os.Getenv(EnvELServiceAccount), "service_account_user_id (env "+EnvELServiceAccount+")")
	cmd.AddCommand(a.elServiceAccountsCmd(), a.elListCmd(), a.elGetCmd(), a.elCreateCmd(), a.elUpdateCmd(),
		a.elToggleCmd("disable"), a.elToggleCmd("enable"), a.elDeleteCmd())
	return cmd
}

func (a *app) elClient(needSA bool) (*elevenlabs.Client, error) {
	tok := strings.TrimSpace(os.Getenv(EnvELKey))
	if tok == "" {
		return nil, fmt.Errorf("%s is not set", EnvELKey)
	}
	if needSA && a.elSA == "" {
		return nil, fmt.Errorf("service account is not set: pass --service-account or set %s (see orkeys el service-accounts)", EnvELServiceAccount)
	}
	return elevenlabs.New(a.elBaseURL, tok, a.elSA, a.timeout), nil
}

// elPrice returns the price of one credit from the environment, or nil when it is not set.
func elPrice() (*float64, error) {
	s := strings.TrimSpace(os.Getenv(EnvELPrice))
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) {
		return nil, fmt.Errorf("%s: %q is not a positive number", EnvELPrice, s)
	}
	return &v, nil
}

func elUSD(credits *int64, price *float64) *float64 {
	if credits == nil || price == nil {
		return nil
	}
	v := float64(*credits) * *price
	return &v
}

func (a *app) elList(cmd *cobra.Command) (*elevenlabs.Client, []elKey, *float64, error) {
	price, err := elPrice()
	if err != nil {
		return nil, nil, nil, err
	}
	c, err := a.elClient(true)
	if err != nil {
		return nil, nil, nil, err
	}
	keys, err := c.ListKeys(cmd.Context())
	if err != nil {
		return nil, nil, nil, err
	}
	out := make([]elKey, 0, len(keys))
	for _, k := range keys {
		used := k.CharacterCount
		if used == nil {
			zero := int64(0)
			used = &zero
		}
		out = append(out, elKey{Key: k, SpendUSD: elUSD(used, price), LimitUSD: elUSD(k.CharacterLimit, price)})
	}
	return c, out, price, nil
}

func elResolve(keys []elKey, ref string) (*elKey, error) {
	var byName []*elKey
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

// elResolveAll resolves every ref before a change, so a typo stops the command before the first request.
func elResolveAll(keys []elKey, refs []string) ([]*elKey, error) {
	out := make([]*elKey, 0, len(refs))
	for _, ref := range refs {
		k, err := elResolve(keys, ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ref, err)
		}
		out = append(out, k)
	}
	return out, nil
}

// elRefuseSelf refuses a change of the management key. The list has the hash of each key value.
func elRefuseSelf(targets []*elKey, verb string) error {
	self := elevenlabs.KeyHash(os.Getenv(EnvELKey))
	for _, k := range targets {
		if strings.EqualFold(k.HashedKey, self) {
			return fmt.Errorf("%s: refusing to %s the key in %s", k.Name, verb, EnvELKey)
		}
	}
	return nil
}

func elStatus(k elevenlabs.Key) string {
	switch {
	case k.Disabled && k.DisableReason != nil && *k.DisableReason != "":
		return "disabled:" + *k.DisableReason
	case k.Disabled:
		return "disabled"
	case k.Expires() != nil && !k.Expires().After(time.Now()):
		return "expired"
	}
	return "active"
}

func elCredits(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(*v, 10)
}

func elUsed(k elevenlabs.Key) string {
	if k.CharacterCount == nil {
		return "0"
	}
	return strconv.FormatInt(*k.CharacterCount, 10)
}

func elTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func elPerms(p []string) string {
	if len(p) == 0 {
		return "-"
	}
	return strings.Join(p, ",")
}

func (a *app) writeELKeys(w io.Writer, keys []elKey, price *float64) error {
	if a.output == "json" {
		if keys == nil {
			keys = []elKey{}
		}
		return printJSON(w, keys)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	head := "ID\tNAME\tSTATUS\tUSED\tLIMIT"
	if price != nil {
		head += "\tSPEND\tLIMIT USD"
	}
	head += "\tEXPIRES"
	if a.output == "wide" {
		head += "\tHINT\tCREATED\tPERMISSIONS"
	}
	fmt.Fprintln(tw, head)
	for _, k := range keys {
		row := []string{k.ID, k.Name, elStatus(k.Key), elUsed(k.Key), elCredits(k.CharacterLimit)}
		if price != nil {
			row = append(row, values.USDPtr(k.SpendUSD), values.USDPtr(k.LimitUSD))
		}
		row = append(row, elTime(k.Expires()))
		if a.output == "wide" {
			created := k.Created()
			row = append(row, k.Hint, elTime(&created), elPerms(k.Permissions))
		}
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

func (a *app) writeELKey(w io.Writer, k elKey, price *float64) error {
	if a.output == "json" {
		return printJSON(w, k)
	}
	created := k.Created()
	reason := "-"
	if k.DisableReason != nil && *k.DisableReason != "" {
		reason = *k.DisableReason
	}
	ips := "-"
	if len(k.AllowedIPs) > 0 {
		ips = strings.Join(k.AllowedIPs, ", ")
	}
	rows := [][2]string{
		{"ID", k.ID},
		{"Name", k.Name},
		{"Hint", k.Hint},
		{"Service account", k.ServiceAccountID},
		{"Status", elStatus(k.Key)},
		{"Disable reason", reason},
		{"Permissions", strings.ReplaceAll(elPerms(k.Permissions), ",", ", ")},
		{"Credits used", elUsed(k.Key)},
		{"Credit limit", elCredits(k.CharacterLimit)},
	}
	if price != nil {
		rows = append(rows, [2]string{"Spend (billing period)", values.USDPtr(k.SpendUSD)}, [2]string{"Limit (USD)", values.USDPtr(k.LimitUSD)})
	}
	rows = append(rows, [2]string{"Allowed IPs", ips}, [2]string{"Expires", elTime(k.Expires())}, [2]string{"Created", elTime(&created)})
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	return tw.Flush()
}

func (a *app) elServiceAccountsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "service-accounts",
		Aliases: []string{"sa"},
		Short:   "List the service accounts of the workspace",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.elClient(false)
			if err != nil {
				return err
			}
			sas, err := c.ListServiceAccounts(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				if sas == nil {
					sas = []elevenlabs.ServiceAccount{}
				}
				return printJSON(os.Stdout, sas)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tKEYS\tCREATED")
			for _, sa := range sas {
				var created *time.Time
				if sa.CreatedAtUnix != nil && *sa.CreatedAtUnix != 0 {
					t := time.Unix(*sa.CreatedAtUnix, 0)
					created = &t
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", sa.ID, sa.Name, len(sa.Keys), elTime(created))
			}
			return tw.Flush()
		},
	}
}

func (a *app) elListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the keys of the service account with credits used",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, keys, price, err := a.elList(cmd)
			if err != nil {
				return err
			}
			return a.writeELKeys(os.Stdout, keys, price)
		},
	}
}

func (a *app) elGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Show one key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, keys, price, err := a.elList(cmd)
			if err != nil {
				return err
			}
			k, err := elResolve(keys, args[0])
			if err != nil {
				return err
			}
			return a.writeELKey(os.Stdout, *k, price)
		},
	}
}

// elScopes checks permissions with the same rules as inventory mode.
func elScopes(scopes []string) error {
	info, _ := provider.Lookup("elevenlabs")
	if err := info.CheckScopes(scopes); err != nil {
		return fmt.Errorf("--scopes: %w", err)
	}
	return nil
}

// elExpiry parses --expires. It returns nil for "none".
func elExpiry(s string) (*time.Time, error) {
	t, err := oaExpiry(s)
	if err != nil || t == nil {
		return nil, err
	}
	if err := elevenlabs.CheckExpiry(*t, time.Now()); err != nil {
		return nil, err
	}
	return t, nil
}

func (a *app) elCreateCmd() *cobra.Command {
	var (
		name, expires string
		limit         int64
		scopes        []string
		quiet         bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a key (the secret is shown only once)",
		Example: `  orkeys el create --name ci --expires 30d --credit-limit 100000
  orkeys el create --name stt --scopes speech_to_text
  KEY=$(orkeys el create --name tmp --expires 1h -q)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(name) == "" {
				return errors.New("--name is required")
			}
			r := elevenlabs.CreateRequest{Name: name, Permissions: elevenlabs.DefaultPermissions}
			if len(scopes) > 0 {
				if err := elScopes(scopes); err != nil {
					return err
				}
				r.Permissions = scopes
			}
			if cmd.Flags().Changed("credit-limit") {
				if limit < 0 {
					return errors.New("--credit-limit: want 0 or more")
				}
				r.CharacterLimit = &limit
			}
			if cmd.Flags().Changed("expires") {
				t, err := elExpiry(expires)
				if err != nil {
					return err
				}
				if t != nil {
					u := t.Unix()
					r.ExpiresAtUnix = &u
				}
			}
			c, err := a.elClient(true)
			if err != nil {
				return err
			}
			k, err := c.CreateKey(cmd.Context(), r)
			if err != nil {
				return err
			}
			switch {
			case quiet:
				fmt.Println(k.Value)
			case a.output == "json":
				return printJSON(os.Stdout, map[string]any{"id": k.ID, "name": name, "permissions": r.Permissions,
					"character_limit": r.CharacterLimit, "expires_at_unix": r.ExpiresAtUnix, "key": k.Value})
			default:
				exp := "-"
				if r.ExpiresAtUnix != nil {
					exp = time.Unix(*r.ExpiresAtUnix, 0).UTC().Format("2006-01-02 15:04")
				}
				fmt.Printf("ID:           %s\nName:         %s\nPermissions:  %s\nCredit limit: %s\nExpires:      %s\n\nKey: %s\n",
					k.ID, name, strings.Join(r.Permissions, ", "), elCredits(r.CharacterLimit), exp, k.Value)
				fmt.Fprintln(os.Stderr, "NOTE: save this key now. ElevenLabs does not show it again.")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&name, "name", "n", "", "key name (required)")
	f.Int64Var(&limit, "credit-limit", 0, "credits per billing period; requests fail after the limit (default: no limit)")
	f.StringVar(&expires, "expires", "", "expiry: duration (30d, 2w, 12h), date (2027-12-31) or RFC 3339; 15m to 90d")
	f.StringSliceVar(&scopes, "scopes", nil, "permissions, for example text_to_speech,voices_read (default: "+strings.Join(elevenlabs.DefaultPermissions, ",")+")")
	f.BoolVarP(&quiet, "quiet", "q", false, "print only the secret key")
	return cmd
}

func (a *app) elUpdateCmd() *cobra.Command {
	var (
		name, limit, expires string
		scopes               []string
	)
	cmd := &cobra.Command{
		Use:   "update <key>",
		Short: "Rename a key, or change its credit limit, expiry or permissions",
		Example: `  orkeys el update ci --credit-limit 200000
  orkeys el update ci --credit-limit none --expires 30d
  orkeys el update ci --name ci-old --scopes text_to_speech`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			u := elevenlabs.Update{}
			if f.Changed("name") {
				if strings.TrimSpace(name) == "" {
					return errors.New("--name: empty")
				}
				u["name"] = name
			}
			if f.Changed("credit-limit") {
				if strings.EqualFold(strings.TrimSpace(limit), "none") {
					u["character_limit"] = elevenlabs.Clear
				} else {
					n, err := strconv.ParseInt(strings.TrimSpace(limit), 10, 64)
					if err != nil || n < 0 {
						return fmt.Errorf("--credit-limit: %q is not a number of credits or none", limit)
					}
					u["character_limit"] = n
				}
			}
			if f.Changed("expires") {
				t, err := elExpiry(expires)
				if err != nil {
					return err
				}
				u["expires_at_unix"] = elevenlabs.Clear
				if t != nil {
					u["expires_at_unix"] = t.Unix()
				}
			}
			if f.Changed("scopes") {
				if len(scopes) == 0 {
					return errors.New("--scopes: give at least one permission")
				}
				if err := elScopes(scopes); err != nil {
					return err
				}
				u["permissions"] = scopes
			}
			if len(u) == 0 {
				return errors.New("nothing to change: pass --name, --credit-limit, --expires or --scopes")
			}
			c, keys, _, err := a.elList(cmd)
			if err != nil {
				return err
			}
			k, err := elResolve(keys, args[0])
			if err != nil {
				return err
			}
			if err := c.UpdateKey(cmd.Context(), k.ID, u); err != nil {
				return err
			}
			fmt.Printf("updated %s (%s)\n", k.Name, k.ID)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&name, "name", "n", "", "new name")
	f.StringVar(&limit, "credit-limit", "", "credits per billing period, or none")
	f.StringVar(&expires, "expires", "", "new expiry, counted from now: duration, date, RFC 3339, or none; 15m to 90d")
	f.StringSliceVar(&scopes, "scopes", nil, "new permissions; they replace the old ones")
	return cmd
}

func (a *app) elToggleCmd(use string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <key>...",
		Short: strings.ToUpper(use[:1]) + use[1:] + " one or more keys",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, keys, _, err := a.elList(cmd)
			if err != nil {
				return err
			}
			targets, err := elResolveAll(keys, args)
			if err != nil {
				return err
			}
			if use == "disable" {
				if err := elRefuseSelf(targets, use); err != nil {
					return err
				}
			}
			var failed int
			for _, k := range targets {
				var err error
				switch {
				case use == "disable":
					err = c.DisableKey(cmd.Context(), k.ID)
				case k.DisableReason != nil && *k.DisableReason == elevenlabs.ReasonExposed:
					err = fmt.Errorf("elevenlabs disabled the key because it was public (%s); make a new key", elevenlabs.ReasonExposed)
				default:
					err = c.UpdateKey(cmd.Context(), k.ID, elevenlabs.Update{"is_enabled": true})
				}
				if err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "orkeys: %s: %v\n", k.Name, err)
					continue
				}
				fmt.Printf("%sd %s (%s)\n", use, k.Name, k.ID)
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d failed", failed, len(targets))
			}
			return nil
		},
	}
}

func (a *app) elDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <key>...",
		Aliases: []string{"rm"},
		Short:   "Delete one or more keys (a delete cannot be undone)",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, keys, _, err := a.elList(cmd)
			if err != nil {
				return err
			}
			targets, err := elResolveAll(keys, args)
			if err != nil {
				return err
			}
			if err := elRefuseSelf(targets, "delete"); err != nil {
				return err
			}
			if !yes {
				if !isTTY() {
					return errors.New("refusing to delete without confirmation: pass --yes")
				}
				fmt.Println("Keys to delete:")
				for _, k := range targets {
					fmt.Printf("  %s  %s  %s\n", k.ID, k.Name, elStatus(k.Key))
				}
				if !confirm(fmt.Sprintf("Delete %d key(s)? This cannot be undone.", len(targets))) {
					return errors.New("aborted")
				}
			}
			var failed int
			for _, k := range targets {
				if err := c.DeleteKey(cmd.Context(), k.ID); err != nil {
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
