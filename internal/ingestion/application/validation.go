// Package application 编排 Ingestion 用例并维护跨 Adapter 的状态边界。
package application

import (
	"bytes"
	"errors"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// DefaultContentMaxBytes is the bounded v1 ingestion input ceiling.
const DefaultContentMaxBytes int64 = 10 * 1024 * 1024

// ContentPolicy 定义 Parser 前可确定执行的输入边界。
type ContentPolicy struct {
	MaxBytes int64
}

// ContentValidationError 是不会被自由文本 fallback 掩盖的稳定输入错误。
type ContentValidationError struct {
	Code       string
	Quarantine bool
	Cause      error
}

func (e *ContentValidationError) Error() string { return e.Code }
func (e *ContentValidationError) Unwrap() error { return e.Cause }

// ValidateSourceContent 校验 MIME、大小和格式边界，原始字节保持不变。
func ValidateSourceContent(mediaType string, content []byte, policy ContentPolicy) ([]domain.Warning, error) {
	if mediaType != "text/markdown" && mediaType != "text/plain" && mediaType != "text/html" && mediaType != "application/pdf" {
		return nil, &ContentValidationError{Code: "SOURCE_MEDIA_TYPE_UNSUPPORTED", Cause: errors.New("only markdown, plain text, HTML, and PDF are supported")}
	}
	if policy.MaxBytes < 0 {
		return nil, &ContentValidationError{Code: "SOURCE_SIZE_POLICY_INVALID", Cause: errors.New("max bytes must not be negative")}
	}
	if policy.MaxBytes > 0 && int64(len(content)) > policy.MaxBytes {
		return nil, &ContentValidationError{Code: "SOURCE_FILE_TOO_LARGE", Cause: errors.New("source exceeds configured parser limit")}
	}
	if mediaType == "application/pdf" {
		if !bytes.HasPrefix(content, []byte("%PDF-")) {
			return nil, &ContentValidationError{Code: "SOURCE_PDF_SIGNATURE_INVALID", Cause: errors.New("source does not have a PDF signature")}
		}
		return nil, nil
	}
	if hasBinaryControl(content) {
		return nil, &ContentValidationError{Code: "SOURCE_BINARY_CONTENT", Quarantine: true, Cause: errors.New("binary control byte detected")}
	}
	if !utf8.Valid(content) {
		return nil, &ContentValidationError{Code: "SOURCE_INVALID_UTF8", Cause: errors.New("source is not valid UTF-8")}
	}
	warnings := []domain.Warning(nil)
	if bytes.HasPrefix(content, utf8BOM) {
		warnings = append(warnings, domain.Warning{Code: "UTF8_BOM_PRESENT", Message: "检测到 UTF-8 BOM，原始字节位置仍按 BOM 计数", StartByte: 0, EndByte: 3})
	}
	return warnings, nil
}

func hasBinaryControl(content []byte) bool {
	for _, value := range content {
		if value == 0 || value < 0x09 || value == 0x0b || value == 0x0c || (value >= 0x0e && value < 0x20) || value == 0x7f {
			return true
		}
	}
	return false
}
