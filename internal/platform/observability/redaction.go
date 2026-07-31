package observability

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"time"

	foundationredaction "github.com/CodeZen-Lizhi/zhixu/internal/foundation/redaction"
)

// RedactedValue is the only replacement emitted when a sensitive value is
// detected. The original value is never included in errors or telemetry.
const RedactedValue = "<redacted>"

var (
	// 常见会话/CSRF/JWT 文本即使没有显式 key 也必须 fail closed。
	jwtPattern                 = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	sensitiveAssignmentPattern = regexp.MustCompile(`(?i)\b(session|csrf|access[_-]?token|refresh[_-]?token|set-cookie)\s*[:=]\s*\S+`)
)

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
		// error 文本可能携带 SQL 参数、凭据、正文或本地路径；调用方
		// 应额外记录稳定 error_code，而不是把未知错误对象交给日志。
		return RedactedValue
	case time.Time, time.Duration:
		return typed
	case fmt.Stringer:
		// Stringer 的实现不受本包控制，默认不信任其返回内容。
		return RedactedValue
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
	if isSensitiveKey(key) || containsSecret(value) || isAbsoluteFilesystemPath(key, value) {
		return RedactedValue
	}
	if value == "" {
		return value
	}
	return value
}

func containsSecret(value string) bool {
	return foundationredaction.ContainsSecret(value) || foundationredaction.ContainsPII(value) || jwtPattern.MatchString(value) || sensitiveAssignmentPattern.MatchString(value)
}

func isSensitiveKey(key string) bool {
	normalized := normalizeKey(key)
	if normalized == "" {
		return false
	}
	// 凭据 marker 必须优先于 `_id`/`_hash` 等摘要后缀，避免
	// `token_hash`、`credential_id` 这类字段把原始凭据带出日志。
	for _, marker := range []string{
		"authorization", "cookie", "credential", "password", "passwd", "secret", "token",
		"apikey", "dsn", "databaseurl", "connectionstring", "session", "csrf", "setcookie",
		"locktoken", "email", "phone", "mobile", "telephone", "address", "postalcode", "zipcode",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	if isSummaryKey(normalized) {
		return false
	}
	for _, marker := range []string{
		"requestbody", "responsebody", "body", "content", "prompt", "source", "rawresponse", "stderr",
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
	if normalizedKey == "httproute" && strings.HasPrefix(trimmed, "/") && !strings.ContainsAny(trimmed, "?#\r\n") {
		return false
	}
	pathKey := strings.Contains(normalizedKey, "file") || strings.Contains(normalizedKey, "directory") ||
		strings.Contains(normalizedKey, "workspace") || strings.Contains(normalizedKey, "root") ||
		strings.Contains(normalizedKey, "targetpath") || strings.Contains(normalizedKey, "absolutepath")
	if pathKey && foundationredaction.ContainsAbsolutePath(trimmed) {
		return true
	}
	return foundationredaction.ContainsAbsolutePath(trimmed)
}

// RedactString 对日志、Trace 和导出边界的文本执行统一 fail-closed 脱敏。
func RedactString(key, value string) string { return redactString(key, value) }

// RedactValue 对任意日志值执行递归脱敏；未知复合类型会被替换为摘要占位符。
func RedactValue(key string, value any) any { return redactAny(key, value) }

// IsSensitiveKey 判断字段名是否属于凭据或受限正文边界。
func IsSensitiveKey(key string) bool { return isSensitiveKey(key) }
