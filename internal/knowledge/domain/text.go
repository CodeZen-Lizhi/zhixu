package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	// MaxTopicNameBytes 是 Topic 名称和别名规范化后的最大 UTF-8 字节数。
	MaxTopicNameBytes = 256
	// MaxStatementBytes 是 Claim statement 规范化后的最大 UTF-8 字节数。
	MaxStatementBytes = 16 * 1024
	// MaxReasonBytes 是理由、摘要和人工分析文本的最大 UTF-8 字节数。
	MaxReasonBytes = 4 * 1024
	// MaxDescriptionBytes 是 Topic 描述的最大 UTF-8 字节数。
	MaxDescriptionBytes = 4 * 1024
	// MaxReferenceBytes 是模型运行、确认和 resolution reference 的最大 UTF-8 字节数。
	MaxReferenceBytes = 512
)

var unicodeFold = cases.Fold()

// NormalizeTopicText 返回保留展示大小写的 NFC 文本及 Unicode case-fold 规范值。
func NormalizeTopicText(value string) (string, string, error) {
	display, err := normalizeText(value, MaxTopicNameBytes, false)
	if err != nil {
		return "", "", err
	}
	normalized := norm.NFC.String(unicodeFold.String(display))
	if len(normalized) > MaxTopicNameBytes {
		return "", "", invalid(ErrorCodeTextInvalid, "normalized topic text exceeds byte limit")
	}
	return display, normalized, nil
}

// NormalizeStatement 返回保留语义大小写的 NFC Claim statement；第二个返回值是同一规范文本。
func NormalizeStatement(value string) (string, string, error) {
	statement, err := normalizeText(value, MaxStatementBytes, false)
	if err != nil {
		return "", "", err
	}
	return statement, statement, nil
}

// NormalizeReason 规范化理由、摘要或人工分析文本。
func NormalizeReason(value string, required bool) (string, error) {
	return normalizeText(value, MaxReasonBytes, !required)
}

// NormalizeDescription 规范化可选 Topic 描述。
func NormalizeDescription(value string) (string, error) {
	return normalizeText(value, MaxDescriptionBytes, true)
}

// NormalizeReference 规范化不可变确认或模型运行引用。
func NormalizeReference(value string, required bool) (string, error) {
	return normalizeText(value, MaxReferenceBytes, !required)
}

func normalizeText(value string, maxBytes int, allowEmpty bool) (string, error) {
	if !utf8.ValidString(value) {
		return "", invalid(ErrorCodeTextInvalid, "text is not valid utf-8")
	}
	value = norm.NFC.String(value)
	var builder strings.Builder
	builder.Grow(len(value))
	pendingSpace := false
	started := false
	for _, r := range value {
		if unicode.IsSpace(r) {
			if started {
				pendingSpace = true
			}
			continue
		}
		if unicode.IsControl(r) {
			return "", invalid(ErrorCodeTextInvalid, "text contains a control character")
		}
		if pendingSpace {
			builder.WriteByte(' ')
			pendingSpace = false
		}
		builder.WriteRune(r)
		started = true
		if builder.Len() > maxBytes {
			return "", invalid(ErrorCodeTextInvalid, "text exceeds byte limit")
		}
	}
	normalized := builder.String()
	if normalized == "" && !allowEmpty {
		return "", invalid(ErrorCodeTextInvalid, "text is required")
	}
	return normalized, nil
}

func foldedStatement(value string) string {
	return norm.NFC.String(unicodeFold.String(value))
}
