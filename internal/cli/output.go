package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/hatedabamboo/orkeys/internal/openrouter"
	"github.com/hatedabamboo/orkeys/internal/values"
)

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *app) printKeys(keys []openrouter.Key) error { return a.writeKeys(os.Stdout, keys) }

func (a *app) printKey(k openrouter.Key) error { return a.writeKey(os.Stdout, k) }

func (a *app) writeKeys(w io.Writer, keys []openrouter.Key) error {
	return writeKeysAs(w, keys, a.output)
}

func (a *app) writeKey(w io.Writer, k openrouter.Key) error { return writeKeyAs(w, k, a.output) }

// writeKeysAs writes keys as "table", "wide" or "json".
func writeKeysAs(w io.Writer, keys []openrouter.Key, format string) error {
	if format == "json" {
		if keys == nil {
			keys = []openrouter.Key{}
		}
		return printJSON(w, keys)
	}
	wide := format == "wide"

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		fmt.Fprintln(tw, "HASH\tNAME\tSTATUS\tLIMIT\tREMAINING\tRESET\tBYOK\tUSAGE\tDAY\tWEEK\tMONTH\tEXPIRES\tLAST USED\tCREATED")
	} else {
		fmt.Fprintln(tw, "HASH\tNAME\tSTATUS\tLIMIT\tREMAINING\tRESET\tUSAGE\tMONTH\tEXPIRES")
	}
	for _, k := range keys {
		if wide {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				k.Hash, k.Name, k.Status(),
				values.USDPtr(k.Limit), values.USDPtr(k.LimitRemaining), values.Str(k.LimitReset),
				values.YesNo(k.IncludeBYOKInLimit),
				values.USD(k.Usage), values.USD(k.UsageDaily), values.USD(k.UsageWeekly), values.USD(k.UsageMonthly),
				values.Time(k.ExpiresAt), values.Time(k.LastUsedAt), values.Time(&k.CreatedAt))
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				values.ShortHash(k.Hash), k.Name, k.Status(),
				values.USDPtr(k.Limit), values.USDPtr(k.LimitRemaining), values.Str(k.LimitReset),
				values.USD(k.Usage), values.USD(k.UsageMonthly), values.Time(k.ExpiresAt))
		}
	}
	return tw.Flush()
}

// writeKeyAs writes one key as label/value lines, or as JSON.
func writeKeyAs(w io.Writer, k openrouter.Key, format string) error {
	if format == "json" {
		return printJSON(w, k)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range orFields(k) {
		fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	return tw.Flush()
}

func orFields(k openrouter.Key) [][2]string {
	return [][2]string{
		{"Name", k.Name},
		{"Label", k.Label},
		{"Hash", k.Hash},
		{"Status", k.Status()},
		{"Limit", values.USDPtr(k.Limit)},
		{"Limit remaining", values.USDPtr(k.LimitRemaining)},
		{"Limit reset", values.Str(k.LimitReset)},
		{"BYOK in limit", values.YesNo(k.IncludeBYOKInLimit)},
		{"Usage (total)", values.USD(k.Usage)},
		{"Usage (day/week/month)", fmt.Sprintf("%s / %s / %s", values.USD(k.UsageDaily), values.USD(k.UsageWeekly), values.USD(k.UsageMonthly))},
		{"BYOK usage (total)", values.USD(k.BYOKUsage)},
		{"BYOK usage (day/week/month)", fmt.Sprintf("%s / %s / %s", values.USD(k.BYOKUsageDaily), values.USD(k.BYOKUsageWeekly), values.USD(k.BYOKUsageMonthly))},
		{"Expires", values.Time(k.ExpiresAt)},
		{"Last used", values.Time(k.LastUsedAt)},
		{"Created", values.Time(&k.CreatedAt)},
		{"Updated", values.Time(k.UpdatedAt)},
		{"Workspace", values.Str(&k.WorkspaceID)},
		{"Creator user", values.Str(k.CreatorUserID)},
		{"External user", values.Str(k.ExternalUser)},
	}
}
