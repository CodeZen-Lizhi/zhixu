// Package redaction 提供跨日志、Trace 与 Tool 输出共享的敏感文本检测事实源。
package redaction

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	sensitiveAssignmentPattern = regexp.MustCompile(`(?i)(authorization|bearer|api[_-]?key|credential|password|passwd|secret|token|cookie|dsn|database[_-]?url)\s*[:=]\s*\S+`)
	bearerTokenPattern         = regexp.MustCompile(`(?i)\bbearer\s+\S+`)
	credentialURLPattern       = regexp.MustCompile(`(?i)\b(postgres(?:ql)?|mysql|mariadb|mongodb(?:\+srv)?|redis|amqp)://\S+`)
	urlCandidatePattern        = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s<>"']+`)
	windowsAbsolutePathPattern = regexp.MustCompile(`(?:^|[\s\x22'\x60(=:;,])([A-Za-z]:[\\/][^\s\x22'\x60<>]*)`)
	unixAbsolutePathPattern    = regexp.MustCompile(`(?:^|[\s\x22'\x60(=:;,])(/[^/\s\x22'\x60<>][^\s\x22'\x60<>]*)`)
)

// ContainsSecret 判断文本是否携带常见凭据赋值、Bearer Token 或带凭据连接 URL。
func ContainsSecret(value string) bool {
	if sensitiveAssignmentPattern.MatchString(value) || bearerTokenPattern.MatchString(value) || credentialURLPattern.MatchString(value) {
		return true
	}
	for _, candidate := range urlCandidatePattern.FindAllString(value, -1) {
		parsed, err := url.Parse(strings.TrimRight(candidate, ").,;]}"))
		if err == nil && parsed.User != nil {
			return true
		}
	}
	return false
}

// ContainsAbsolutePath 判断文本是否暴露 Unix、Windows 或 file URL 形式的绝对路径。
func ContainsAbsolutePath(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "file://") || filepath.IsAbs(trimmed) {
		return true
	}
	withoutNetworkURLs := urlCandidatePattern.ReplaceAllStringFunc(trimmed, func(candidate string) string {
		parsed, err := url.Parse(strings.TrimRight(candidate, ").,;]}"))
		if err == nil && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) {
			return strings.Repeat(" ", len(candidate))
		}
		return candidate
	})
	if strings.Contains(strings.ToLower(withoutNetworkURLs), "file://") {
		return true
	}
	return windowsAbsolutePathPattern.MatchString(withoutNetworkURLs) || unixAbsolutePathPattern.MatchString(withoutNetworkURLs)
}
