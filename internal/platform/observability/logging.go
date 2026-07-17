// Package observability contains the project-owned logging, metrics and trace
// seams used by process composition and runtime packages.
package observability

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Logger is the narrow structured logging contract injected into application
// and runtime packages. *slog.Logger satisfies this interface.
type Logger interface {
	DebugContext(context.Context, string, ...any)
	InfoContext(context.Context, string, ...any)
	WarnContext(context.Context, string, ...any)
	ErrorContext(context.Context, string, ...any)
}

// NewLogger returns a JSON slog logger that adds context correlation and
// applies fail-closed redaction before values reach the output.
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
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: minimum})
	return slog.New(newSafeHandler(handler))
}
