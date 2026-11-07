package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hatedabamboo/orkeys/internal/engine"
	"github.com/hatedabamboo/orkeys/internal/manifest"
)

func writePlan(w io.Writer, p *engine.Plan) {
	if p.Empty() {
		fmt.Fprintln(w, "No changes.")
	}
	for _, a := range p.Actions {
		fmt.Fprintf(w, "%-13s %s: %s\n", a.Type, a.Name, a.Detail)
	}
	for _, s := range p.Warnings {
		fmt.Fprintf(w, "warning: %s\n", s)
	}
	for _, k := range p.Unmanaged {
		fmt.Fprintf(w, "unmanaged: %s %s (%s); run `orkeys import`\n", k.Provider, k.ID, k.Name)
	}
}

func writePlanMarkdown(w io.Writer, p *engine.Plan) {
	fmt.Fprintln(w, "### orkeys plan")
	fmt.Fprintln(w)
	if p.Empty() {
		fmt.Fprintln(w, "No changes.")
	} else {
		fmt.Fprintln(w, "| Action | Key | Team | Provider | Detail |")
		fmt.Fprintln(w, "|---|---|---|---|---|")
		for _, a := range p.Actions {
			fmt.Fprintf(w, "| `%s` | `%s` | %s | %s | %s |\n", a.Type, a.Name, a.Team, a.Provider, strings.ReplaceAll(a.Detail, "|", "\\|"))
		}
	}
	if len(p.Warnings) > 0 {
		fmt.Fprintln(w, "\n**Warnings**")
		for _, s := range p.Warnings {
			fmt.Fprintf(w, "- %s\n", s)
		}
	}
	if n := len(p.Unmanaged); n > 0 {
		fmt.Fprintf(w, "\n%d key(s) at providers have no manifest. Run orkeys import.\n", n)
	}
}

func writeIssued(w io.Writer, keys []engine.IssuedKey, markdown bool) {
	if len(keys) == 0 {
		fmt.Fprintln(w, "No active requested keys.")
		return
	}
	spend := func(k engine.IssuedKey) string {
		if k.SpendUSD == nil {
			return "-"
		}
		return fmt.Sprintf("$%.2f", *k.SpendUSD)
	}
	if markdown {
		fmt.Fprintln(w, "| Team | Owner | Kind | Provider | Purpose | Spend | Limit | Expires | Key |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|")
		for _, k := range keys {
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | $%.2f %s | %s | `%s` |\n", k.Team, k.Owner, k.Kind, k.Provider,
				strings.ReplaceAll(k.Purpose, "|", "\\|"), spend(k), k.LimitUSD, k.LimitReset, k.ExpiresAt.Format(time.DateOnly), k.Name)
		}
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TEAM\tOWNER\tKIND\tPROVIDER\tSPEND\tLIMIT\tEXPIRES\tNAME\tPURPOSE")
	for _, k := range keys {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t$%.2f %s\t%s\t%s\t%s\n", k.Team, k.Owner, k.Kind, k.Provider,
			spend(k), k.LimitUSD, k.LimitReset, k.ExpiresAt.Format(time.DateOnly), k.Name, k.Purpose)
	}
	_ = tw.Flush()
}

// writeImportStubs prints one manifest per unmanaged key, with the import block filled in.
func writeImportStubs(w io.Writer, cands []engine.Candidate) {
	for i, c := range cands {
		k := c.Key
		if i > 0 {
			fmt.Fprintln(w, "---")
		}
		info := []string{"created " + dateOr(k.CreatedAt.IsZero(), k.CreatedAt)}
		if k.LastUsedAt != nil {
			info = append(info, "last used "+k.LastUsedAt.Format(time.DateOnly))
		}
		if k.SpendMonthUSD != nil {
			info = append(info, fmt.Sprintf("$%.2f this month", *k.SpendMonthUSD))
		}
		fmt.Fprintf(w, "# %s key %q: %s\n", c.Provider, k.Name, strings.Join(info, ", "))
		fmt.Fprintf(w, "# Write the current key value to secret.path first. Save as teams/<team>/<name>.yaml.\n")
		fmt.Fprintf(w, "name: TODO\nkind: TODO\nprovider: %s\nenv: TODO\ndescription: TODO\n", c.Provider)
		fmt.Fprintf(w, "import:\n  id: %s\n  name: %s\n", k.ID, yamlString(k.Name))
		if k.LimitUSD != nil {
			reset := k.LimitReset
			if reset == "" {
				reset = manifest.DefaultReset
			}
			fmt.Fprintf(w, "budget:\n  reset: %s\n  limit: %g\n", reset, *k.LimitUSD)
		}
		fmt.Fprintf(w, "secret:\n  path: TODO\n")
	}
}

func dateOr(zero bool, t time.Time) string {
	if zero {
		return "unknown"
	}
	return t.Format(time.DateOnly)
}

func yamlString(s string) string {
	b, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return strings.TrimSuffix(string(b), "\n")
}
