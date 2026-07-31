// Package redaction 提供跨日志、Trace 与 Tool 输出共享的敏感文本检测事实源。
package redaction

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	sensitiveAssignmentPattern = regexp.MustCompile(`(?i)(authorization|bearer|api[_-]?key|credential|password|passwd|secret|token|cookie|session|csrf|access[_-]?token|refresh[_-]?token|set-cookie|dsn|database[_-]?url)["']?\s*[:=]\s*["']?\S+`)
	bearerTokenPattern         = regexp.MustCompile(`(?i)\bbearer\s+\S+`)
	jwtPattern                 = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	emailPattern               = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	credentialURLPattern       = regexp.MustCompile(`(?i)\b(postgres(?:ql)?|mysql|mariadb|mongodb(?:\+srv)?|redis|amqp)://\S+`)
	urlCandidatePattern        = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s<>"']+`)
	windowsAbsolutePathPattern = regexp.MustCompile(`(?:^|[\s\x22'\x60(=:;,])([A-Za-z]:[\\/][^\s\x22'\x60<>]*)`)
	unixAbsolutePathPattern    = regexp.MustCompile(`(?:^|[\s\x22'\x60(=:;,])(/[^/\s\x22'\x60<>][^\s\x22'\x60<>]*)`)
)

const maskedValue = "<redacted>"

// ContainsSecret 判断文本是否携带常见凭据、Bearer/JWT Token 或带凭据连接 URL。
func ContainsSecret(value string) bool {
	if sensitiveAssignmentPattern.MatchString(value) || bearerTokenPattern.MatchString(value) || jwtPattern.MatchString(value) || credentialURLPattern.MatchString(value) {
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

// ContainsPII 判断文本是否携带不应进入日志或 Trace 的个人信息。
func ContainsPII(value string) bool { return emailPattern.MatchString(value) }

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

// RedactText 将凭据赋值、邮箱、Bearer/JWT Token、带凭据连接 URL 和绝对本地路径替换为稳定遮罩。
// HTTP(S) URL 的普通 path 会保留，避免把可公开引用误判为本地文件路径。
func RedactText(value string) string {
	redacted := RedactSecrets(value)
	redacted = emailPattern.ReplaceAllString(redacted, maskedValue)
	redacted = windowsAbsolutePathPattern.ReplaceAllStringFunc(redacted, redactPathMatch)
	redacted = unixAbsolutePathPattern.ReplaceAllStringFunc(redacted, redactPathMatch)
	return redacted
}

// RedactSecrets 只替换凭据、Bearer/JWT Token 和带凭据连接 URL，保留普通内容与本地路径供已授权边界处理。
func RedactSecrets(value string) string {
	redacted := jwtPattern.ReplaceAllString(value, maskedValue)
	redacted = bearerTokenPattern.ReplaceAllString(redacted, maskedValue)
	redacted = sensitiveAssignmentPattern.ReplaceAllString(redacted, maskedValue)
	redacted = credentialURLPattern.ReplaceAllString(redacted, maskedValue)
	redacted = urlCandidatePattern.ReplaceAllStringFunc(redacted, func(candidate string) string {
		trimmed := strings.TrimRight(candidate, ").,;]}")
		parsed, err := url.Parse(trimmed)
		if err == nil && parsed.User != nil {
			return maskedValue + strings.TrimPrefix(candidate, trimmed)
		}
		return candidate
	})
	return redacted
}

func redactPathMatch(match string) string {
	if match == "" {
		return maskedValue
	}
	first := match[0]
	if first == '/' || (len(match) >= 3 && ((first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z')) && match[1] == ':') {
		return maskedValue
	}
	return match[:1] + maskedValue
}
