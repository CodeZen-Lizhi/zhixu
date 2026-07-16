// Package observability contains process-wide logging setup.
package observability

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a JSON slog logger. Secret values are never passed to it
// by the application; this package deliberately has no URL or token fields.
func NewLogger(level string, output io.Writer) *slog.Logger {
	if output == nil {
		output = os.Stderr
	}
	var minimum slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		minimum = slog.LevelDebug
	case "warn", "warning":
		minimum = slog.LevelWarn
	case "error":
		minimum = slog.LevelError
	default:
		minimum = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: minimum}))
}
