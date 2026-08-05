package domain

import (
	"bytes"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	maxEvidenceSourceTypeBytes     = 128
	maxEvidenceLogicalNameBytes    = 1024
	maxEvidenceMediaTypeBytes      = 256
	maxEvidenceSecurityStatusBytes = 128
	maxEvidenceSpanTypeBytes       = 128
	maxEvidenceParserVersionBytes  = 256
	maxEvidenceSchemaVersionBytes  = 256
	maxEvidenceSelectorBytes       = 16 * 1024
	maxDerivedEvidenceBytes        = 2 * 1024 * 1024
)

// EvidenceKind 描述 Source Span excerpt 的不可变取证方式。
type EvidenceKind string

const (
	// EvidenceRawBytes 从原始 Content Artifact 的字节区间读取。
	EvidenceRawBytes EvidenceKind = "raw_bytes"
	// EvidenceDerivedText 从解析投影持久化的确定性文本读取。
	EvidenceDerivedText EvidenceKind = "derived_text"
)

// SourceVersionReference 是可打开 Evidence 所依赖的不可变 Source Version 元数据。
type SourceVersionReference struct {
	WorkspaceID       foundation.ID
	SourceID          foundation.ID
	SourceVersionID   foundation.ID
	ContentArtifactID foundation.ID
	SourceType        string
	LogicalName       string
	RelativePath      string
	ContentHash       string
	ByteSize          int64
	MediaType         string
	SecurityStatus    string
	// IngestionStatus 是该 Source Version 最近一次摄取 Attempt 的持久状态；没有 Attempt 时为空。
	IngestionStatus string
	// WorkflowStatus 是最近一次摄取 Attempt 绑定的 Workflow Run 状态；没有绑定时为空。
	WorkflowStatus string
	// IndexStatus 是当前 Active Index 对该 Source 的 included/excluded 选择；没有 Active Index 事实时为空。
	IndexStatus string
	CapturedAt  time.Time
}

// SourceSpanReference 将 Source Version、Content Artifact、Parse Projection 与 Source Span 绑定为一个引用。
type SourceSpanReference struct {
	SourceVersion     SourceVersionReference
	ParseProjectionID foundation.ID
	Span              EvidenceSpan
	SpanType          string
	Selector          json.RawMessage
	ExcerptHash       string
	EvidenceKind      EvidenceKind
	DerivedExcerpt    string
	ParserVersion     string
	SchemaVersion     string
}

// ValidateSourceVersionReference 校验持久化 Source Version 引用的作用域与公开字段。
func ValidateSourceVersionReference(value SourceVersionReference) error {
	ids := []foundation.ID{value.WorkspaceID, value.SourceID, value.SourceVersionID, value.ContentArtifactID}
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return inconsistent(ErrorCodeEvidenceReferenceInvalid, "source version reference identity is invalid")
		}
	}
	if !canonicalEvidenceText(value.SourceType, maxEvidenceSourceTypeBytes) ||
		!canonicalEvidenceText(value.LogicalName, maxEvidenceLogicalNameBytes) ||
		!canonicalRelativePath(value.RelativePath) || !isCanonicalHash(value.ContentHash) || value.ByteSize < 0 ||
		!canonicalEvidenceText(value.MediaType, maxEvidenceMediaTypeBytes) ||
		!canonicalEvidenceText(value.SecurityStatus, maxEvidenceSecurityStatusBytes) ||
		!validOptionalIngestionStatus(value.IngestionStatus) || !validOptionalWorkflowStatus(value.WorkflowStatus) ||
		!validOptionalIndexStatus(value.IndexStatus) || value.CapturedAt.IsZero() {
		return inconsistent(ErrorCodeEvidenceReferenceInvalid, "source version reference metadata is invalid")
	}
	return nil
}

func validOptionalIngestionStatus(value string) bool {
	switch value {
	case "", "validating", "parsing", "parsed", "chunking", "chunked", "parse_failed", "cancelled":
		return true
	default:
		return false
	}
}

func validOptionalWorkflowStatus(value string) bool {
	switch value {
	case "", "pending", "running", "waiting_for_human", "retry_wait", "paused", "succeeded", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func validOptionalIndexStatus(value string) bool {
	return value == "" || value == "included" || value == "excluded"
}

// ValidateSourceSpanReference 校验 Source Span 的版本、Artifact、范围与解析契约。
func ValidateSourceSpanReference(value SourceSpanReference) error {
	if err := ValidateSourceVersionReference(value.SourceVersion); err != nil {
		return err
	}
	projectionID, err := foundation.ParseID(string(value.ParseProjectionID))
	if err != nil || projectionID != value.ParseProjectionID {
		return inconsistent(ErrorCodeEvidenceReferenceInvalid, "source span projection identity is invalid")
	}
	spanID, err := foundation.ParseID(string(value.Span.ID))
	if err != nil || spanID != value.Span.ID || value.Span.StartLine < 1 || value.Span.EndLine < value.Span.StartLine ||
		value.Span.StartByte < 0 || value.Span.EndByte < value.Span.StartByte || value.Span.EndByte > value.SourceVersion.ByteSize {
		return inconsistent(ErrorCodeEvidenceReferenceInvalid, "source span range is invalid")
	}
	if !canonicalEvidenceText(value.SpanType, maxEvidenceSpanTypeBytes) || !validEvidenceSelector(value.Selector) ||
		!isCanonicalHash(value.ExcerptHash) || !canonicalEvidenceText(value.ParserVersion, maxEvidenceParserVersionBytes) ||
		!canonicalEvidenceText(value.SchemaVersion, maxEvidenceSchemaVersionBytes) {
		return inconsistent(ErrorCodeEvidenceReferenceInvalid, "source span metadata is invalid")
	}
	evidenceKind := value.EvidenceKind
	if evidenceKind == "" {
		evidenceKind = EvidenceRawBytes
	}
	switch evidenceKind {
	case EvidenceRawBytes:
		if value.DerivedExcerpt != "" {
			return inconsistent(ErrorCodeEvidenceReferenceInvalid, "raw source span contains derived evidence")
		}
	case EvidenceDerivedText:
		if value.Span.StartByte != 0 || value.Span.EndByte != value.SourceVersion.ByteSize || value.DerivedExcerpt == "" ||
			len(value.DerivedExcerpt) > maxDerivedEvidenceBytes || !utf8.ValidString(value.DerivedExcerpt) {
			return inconsistent(ErrorCodeEvidenceReferenceInvalid, "derived source span evidence is invalid")
		}
	default:
		return inconsistent(ErrorCodeEvidenceReferenceInvalid, "source span evidence kind is invalid")
	}
	return nil
}

func canonicalEvidenceText(value string, maxBytes int) bool {
	return isCanonicalText(value) && len(value) <= maxBytes && utf8.ValidString(value)
}

func validEvidenceSelector(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || len(trimmed) > maxEvidenceSelectorBytes || trimmed[0] != '{' || !json.Valid(trimmed) {
		return false
	}
	var decoded map[string]json.RawMessage
	return json.Unmarshal(trimmed, &decoded) == nil && decoded != nil
}
