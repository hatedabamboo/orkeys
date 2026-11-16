package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// EnvDebug is the default of --debug.
const EnvDebug = "ORKEYS_DEBUG"

// envDebug returns the value of EnvDebug. An empty variable is false.
func envDebug() (bool, error) {
	v := strings.TrimSpace(os.Getenv(EnvDebug))
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s %q: want true or false", EnvDebug, v)
	}
	return b, nil
}

// newLogHandler returns the handler of all log records. Debug records show only with debug set.
func newLogHandler(w io.Writer, format string, debug bool) slog.Handler {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if debug {
		opts.Level = slog.LevelDebug
	}
	if format == "json" {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}
