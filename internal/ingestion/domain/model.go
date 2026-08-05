// Package domain 定义 Ingestion 的稳定领域契约，不依赖具体解析器或存储实现。
package domain

import (
	"context"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// AttemptStatus 是一次摄取尝试的生命周期状态。
type AttemptStatus string

const (
	AttemptValidating  AttemptStatus = "validating"
	AttemptParsing     AttemptStatus = "parsing"
	AttemptParsed      AttemptStatus = "parsed"
	AttemptChunking    AttemptStatus = "chunking"
	AttemptChunked     AttemptStatus = "chunked"
	AttemptParseFailed AttemptStatus = "parse_failed"
	AttemptCancelled   AttemptStatus = "cancelled"
)

// SecurityStatus 是与解析生命周期正交的安全检查状态。
type SecurityStatus string

const (
	SecurityPending     SecurityStatus = "pending"
	SecurityPassed      SecurityStatus = "passed"
	SecurityQuarantined SecurityStatus = "quarantined"
)

// Attempt 描述一次不可覆盖的摄取尝试。
type Attempt struct {
	ID                   foundation.ID
	WorkspaceID          foundation.ID
	SourceVersionID      foundation.ID
	WorkflowRunID        *foundation.ID
	Status               AttemptStatus
	SecurityStatus       SecurityStatus
	FailureStage         string
	ErrorCode            string
	Retryable            bool
	ParserID             string
	ParserVersion        string
	ParserConfigHash     string
	ChunkStrategyVersion string
	SchemaVersion        string
	IdempotencyKey       string
	AttemptNumber        int32
	Warnings             []Warning
}

// ValidateAttemptState 校验 Attempt 状态组合，防止把隔离和失败混成一个枚举。
func ValidateAttemptState(status AttemptStatus, security SecurityStatus) error {
	if status == "" || security == "" {
		return errors.New("attempt status and security status are required")
	}
	if security == SecurityQuarantined && status != AttemptValidating {
		return errors.New("quarantined attempt must remain validating")
	}
	if status == AttemptValidating && security == SecurityPassed {
		return errors.New("validating attempt cannot be security passed")
	}
	if (status == AttemptParsing || status == AttemptParsed || status == AttemptChunking || status == AttemptChunked) && security != SecurityPassed {
		return errors.New("parse and chunk states require passed security validation")
	}
	switch status {
	case AttemptValidating, AttemptParsing, AttemptParsed, AttemptChunking, AttemptChunked, AttemptParseFailed, AttemptCancelled:
		return nil
	default:
		return fmt.Errorf("unknown attempt status %q", status)
	}
}

// ValidateAttemptTransition 校验一次持久化状态转移是否允许。
func ValidateAttemptTransition(from, to AttemptStatus, security SecurityStatus) error {
	if err := ValidateAttemptState(to, security); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	allowed := map[AttemptStatus]map[AttemptStatus]bool{
		AttemptValidating:  {AttemptParsing: true, AttemptParseFailed: true, AttemptCancelled: true},
		AttemptParsing:     {AttemptParsed: true, AttemptParseFailed: true, AttemptCancelled: true},
		AttemptParsed:      {AttemptChunking: true, AttemptParseFailed: true, AttemptCancelled: true},
		AttemptChunking:    {AttemptChunked: true, AttemptParseFailed: true, AttemptCancelled: true},
		AttemptChunked:     {},
		AttemptParseFailed: {},
		AttemptCancelled:   {},
	}
	if !allowed[from][to] {
		return fmt.Errorf("invalid attempt transition %q -> %q", from, to)
	}
	return nil
}

// SourceInput 是 Parser 接收的不可变内容输入；Parser 不得读取路径。
type SourceInput struct {
	SourceVersionID foundation.ID
	MediaType       string
	ImmutableBytes  []byte
}

// Warning 是可展示且可追踪的解析警告。
type Warning struct {
	Code      string
	Message   string
	StartByte int64
	EndByte   int64
}

// ParsedBlock 是 Parser Adapter 输出的结构块和原始位置。
type ParsedBlock struct {
	SpanType    string
	StartLine   int32
	EndLine     int32
	StartByte   int64
	EndByte     int64
	HeadingPath []string
	Selector    map[string]string
	Content     string
	Atomic      bool
	// EvidenceKind 指明 Evidence 应从原始字节还是解析器的确定性派生文本读取。
	EvidenceKind EvidenceKind
}

// ParsedDocument 是不含第三方 AST 类型的解析结果。
type ParsedDocument struct {
	ParserID              string
	ParserVersion         string
	ParserConfigHash      string
	SchemaVersion         string
	NormalizedContentHash string
	Blocks                []ParsedBlock
	Warnings              []Warning
}

// Parser 是 Markdown/TXT 等内容解析器的稳定端口。
type Parser interface {
	Supports(mediaType string) bool
	Version() string
	Descriptor() ParserDescriptor
	Parse(context.Context, SourceInput) (ParsedDocument, error)
}

// ParserDescriptor 提供失败前即可持久化的解析器版本契约。
type ParserDescriptor struct {
	ID            string
	Version       string
	ConfigHash    string
	SchemaVersion string
}

// EvidenceKind 是 Source Span excerpt 的不可变取证方式。
type EvidenceKind string

const (
	// EvidenceRawBytes 表示 excerpt 必须与原始 Content Artifact 的字节区间完全一致。
	EvidenceRawBytes EvidenceKind = "raw_bytes"
	// EvidenceDerivedText 表示 excerpt 是从原始 Artifact 确定性解析出的文本投影。
	EvidenceDerivedText EvidenceKind = "derived_text"
)

// SourceSpan 是 Content Artifact 中不可变的原文位置。
type SourceSpan struct {
	ID                foundation.ID
	WorkspaceID       foundation.ID
	ContentArtifactID foundation.ID
	ParseProjectionID foundation.ID
	SpanType          string
	StartLine         int32
	EndLine           int32
	StartByte         int64
	EndByte           int64
	Selector          map[string]string
	ExcerptHash       string
	EvidenceKind      EvidenceKind
	DerivedExcerpt    string
	ParserVersion     string
	SchemaVersion     string
}

// CanonicalChunk 是 Ingestion 产生的确定性结构块，不包含模型 Token 数。
type CanonicalChunk struct {
	ID                   foundation.ID
	WorkspaceID          foundation.ID
	ParseProjectionID    foundation.ID
	Sequence             int32
	HeadingPath          []string
	Content              string
	ContentHash          string
	SourceSpanID         foundation.ID
	ByteCount            int64
	RuneCount            int64
	ParserVersion        string
	ChunkStrategyVersion string
	SchemaVersion        string
	AtomicOversized      bool
	Status               string
}
