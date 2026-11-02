package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/hatedabamboo/orkeys/internal/api"
	"github.com/hatedabamboo/orkeys/internal/values"
)

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *app) printKeys(keys []api.Key) error { return a.writeKeys(os.Stdout, keys) }

func (a *app) printKey(k api.Key) error { return a.writeKey(os.Stdout, k) }

func (a *app) writeKeys(w io.Writer, keys []api.Key) error { return writeKeysAs(w, keys, a.output) }

func (a *app) writeKey(w io.Writer, k api.Key) error { return writeKeyAs(w, k, a.output) }

// writeKeysAs writes keys as "table", "wide" or "json".
func writeKeysAs(w io.Writer, keys []api.Key, format string) error {
	if format == "json" {
		if keys == nil {
			keys = []api.Key{}
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
func writeKeyAs(w io.Writer, k api.Key, format string) error {
	if format == "json" {
		return printJSON(w, k)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range values.Fields(k) {
		fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	return tw.Flush()
}
