package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hatedabamboo/orkeys/internal/engine"
)

// maxValue is the largest key that put reads from stdin. API keys are much shorter.
const maxValue = 64 << 10

func (a *app) putCmd() *cobra.Command {
	var issued string
	cmd := &cobra.Command{
		Use:   "put <name>",
		Short: "Write a new value of a void key; the next rotate syncs the consumers",
		Long: `put is for keys of void providers: providers without an API. Make the new key at the vendor first.

put reads the key from stdin, or asks for it on a terminal. It does not take the key as an argument.
put writes the key to all secret places of the manifest and starts a rotation. The next orkeys rotate
syncs the consumers and tells the team to revoke the old key at the vendor.

For a key without secret, put reads no value and records only the dates.

put needs write access to the state store and the secrets store. It needs no management key.
The key expires at --issued plus the ttl. If the manifest sets issued, put tells you the new date
to write there.`,
		Example: `  orkeys put prod-voice-tts
  pbpaste | orkeys put prod-voice-tts
  orkeys put prod-voice-tts --issued 2026-09-01`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			var opt engine.PutOptions
			var err error
			if opt.Issued, err = parseDate("--issued", issued); err != nil {
				return err
			}
			e, err := a.newEngine(cmd.Context(), "", true)
			if err != nil {
				return err
			}
			k := e.Inv.Key(name)
			if k == nil {
				return fmt.Errorf("key %s: no manifest", name)
			}
			// Read the value before the lock: a person can take some time to paste it.
			if len(k.Secrets) > 0 {
				if opt.Value, err = readValue(name); err != nil {
					return err
				}
			}
			err = a.withLock(cmd.Context(), e, "put", func(ctx context.Context) error {
				return e.Put(ctx, name, opt)
			})
			return a.finish(map[string]any{"key": name}, err)
		},
	}
	cmd.Flags().StringVar(&issued, "issued", "", "date when the vendor made the key (YYYY-MM-DD); default: now")
	return cmd
}

func parseDate(flag, s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: want YYYY-MM-DD or RFC 3339, got %q", flag, s)
	}
	return t, nil
}

// readValue reads the key from stdin. On a terminal, it asks for the key and does not echo it.
func readValue(name string) (string, error) {
	var b []byte
	var err error
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		fmt.Fprintf(os.Stderr, "Key for %s: ", name)
		b, err = term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
	} else {
		b, err = io.ReadAll(io.LimitReader(os.Stdin, maxValue+1))
	}
	if err != nil {
		return "", fmt.Errorf("read key from stdin: %w", err)
	}
	if len(b) > maxValue {
		return "", fmt.Errorf("read key from stdin: longer than %d bytes", maxValue)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", errors.New("read key from stdin: empty")
	}
	return v, nil
}
