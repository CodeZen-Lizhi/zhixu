package observability

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	foundationredaction "github.com/CodeZen-Lizhi/zhixu/internal/foundation/redaction"
)

// RedactedValue is the only replacement emitted when a sensitive value is
// detected. The original value is never included in errors or telemetry.
const RedactedValue = "<redacted>"

type safeHandler struct {
	next      slog.Handler
	boundKeys map[string]struct{}
	grouped   bool
}

func newSafeHandler(next slog.Handler) slog.Handler {
	return &safeHandler{next: next, boundKeys: make(map[string]struct{})}
}

func (handler *safeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler *safeHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, redactString("message", record.Message), record.PC)
	existing := make(map[string]struct{}, len(handler.boundKeys)+record.NumAttrs())
	for key := range handler.boundKeys {
		existing[key] = struct{}{}
	}
	record.Attrs(func(attr slog.Attr) bool {
		attr.Value = attr.Value.Resolve()
		if attr.Key != "" {
			existing[attr.Key] = struct{}{}
		}
		clean.AddAttrs(redactAttr(attr))
		return true
	})
	for _, attr := range correlationAttrs(CorrelationFromContext(ctx)) {
		if _, found := existing[attr.Key]; found {
			continue
		}
		clean.AddAttrs(redactAttr(attr))
	}
	return handler.next.Handle(ctx, clean)
}

func (handler *safeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	keys := cloneKeys(handler.boundKeys)
	for _, attr := range attrs {
		attr.Value = attr.Value.Resolve()
		redacted = append(redacted, redactAttr(attr))
		if !handler.grouped && attr.Key != "" {
			keys[attr.Key] = struct{}{}
		}
	}
	return &safeHandler{next: handler.next.WithAttrs(redacted), boundKeys: keys, grouped: handler.grouped}
}

func (handler *safeHandler) WithGroup(name string) slog.Handler {
	return &safeHandler{next: handler.next.WithGroup(name), boundKeys: cloneKeys(handler.boundKeys), grouped: true}
}

func cloneKeys(source map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(source))
	for key := range source {
		cloned[key] = struct{}{}
	}
	return cloned
}

func redactAttr(attr slog.Attr) slog.Attr {
	attr.Value = redactSlogValue(attr.Key, attr.Value.Resolve())
	return attr
}

func redactSlogValue(key string, value slog.Value) slog.Value {
	if isSensitiveKey(key) {
		return slog.StringValue(RedactedValue)
	}
	switch value.Kind() {
	case slog.KindString:
		return slog.StringValue(redactString(key, value.String()))
	case slog.KindAny:
		return slog.AnyValue(redactAny(key, value.Any()))
	case slog.KindGroup:
		attrs := value.Group()
		clean := make([]slog.Attr, 0, len(attrs))
		for _, attr := range attrs {
			attr.Value = attr.Value.Resolve()
			clean = append(clean, redactAttr(attr))
		}
		return slog.GroupValue(clean...)
	default:
		return value
	}
}

func redactAny(key string, value any) any {
	if isSensitiveKey(key) {
		return RedactedValue
	}
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return redactString(key, typed)
	case []byte:
		return fmt.Sprintf("<bytes:%d>", len(typed))
	case error:
		return redactString(key, typed.Error())
	case time.Time, time.Duration:
		return typed
	case fmt.Stringer:
		return redactString(key, typed.String())
	case map[string]string:
		clean := make(map[string]string, len(typed))
		for childKey, childValue := range typed {
			clean[childKey] = redactString(childKey, childValue)
		}
		return clean
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			clean[childKey] = redactAny(childKey, childValue)
		}
		return clean
	case []slog.Attr:
		clean := make([]slog.Attr, 0, len(typed))
		for _, attr := range typed {
			attr.Value = attr.Value.Resolve()
			clean = append(clean, redactAttr(attr))
		}
		return clean
	}

	reflected := reflect.ValueOf(value)
	if reflected.IsValid() {
		switch reflected.Kind() {
		case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
			reflect.Float32, reflect.Float64:
			return value
		}
	}
	// Unknown composites are fail-closed: callers should log stable scalar
	// summaries instead of handing telemetry an arbitrary domain object.
	return RedactedValue
}

func redactString(key, value string) string {
	if value == "" {
		return value
	}
	if isSensitiveKey(key) || containsSecret(value) || isAbsoluteFilesystemPath(key, value) {
		return RedactedValue
	}
	return value
}

func containsSecret(value string) bool {
	return foundationredaction.ContainsSecret(value)
}

func isSensitiveKey(key string) bool {
	normalized := normalizeKey(key)
	if normalized == "" || isSummaryKey(normalized) {
		return false
	}
	for _, marker := range []string{
		"authorization", "cookie", "credential", "password", "passwd", "secret", "token",
		"apikey", "dsn", "databaseurl", "connectionstring", "requestbody", "responsebody",
		"body", "content", "prompt", "source", "rawresponse", "stderr", "locktoken",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func isSummaryKey(normalized string) bool {
	for _, suffix := range []string{"id", "hash", "size", "length", "count", "version", "status", "code", "kind", "configured"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func normalizeKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	replacer := strings.NewReplacer("_", "", "-", "", ".", "", " ", "")
	return replacer.Replace(key)
}

func isAbsoluteFilesystemPath(key, value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	normalizedKey := normalizeKey(key)
	pathKey := strings.Contains(normalizedKey, "file") || strings.Contains(normalizedKey, "directory") ||
		strings.Contains(normalizedKey, "workspace") || strings.Contains(normalizedKey, "root") ||
		strings.Contains(normalizedKey, "targetpath") || strings.Contains(normalizedKey, "absolutepath")
	if pathKey && foundationredaction.ContainsAbsolutePath(trimmed) {
		return true
	}
	return foundationredaction.ContainsAbsolutePath(trimmed)
}
