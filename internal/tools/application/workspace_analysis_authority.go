package application

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

// SearchKnowledgeV2ReceiptReader 只读取 Workspace Analysis Run 绑定的唯一 SearchKnowledge@2 receipt。
// 实现必须校验 exact Definition/Call 绑定，并在缺失或存在多个成功 Search receipt 时失败。
type SearchKnowledgeV2ReceiptReader interface {
	LoadSearchKnowledgeV2Receipt(context.Context, foundation.ID, foundation.ID) (domain.ResultReceipt, error)
}

// SearchKnowledgeV2PublicationAuthorityQuery 精确定位一次 Search 操作的发布证明。
type SearchKnowledgeV2PublicationAuthorityQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
	ReceiptID     foundation.ID
	ReceiptHash   string
}

// SearchKnowledgeV2PublicationAuthority 只返回发布终态所需的逻辑操作和 canonical receipt。
// Search 正文、tuple、身份和 hash 不得经通用 JSON 或日志投影。
type SearchKnowledgeV2PublicationAuthority struct {
	OperationID foundation.ID        `json:"-"`
	Receipt     domain.ResultReceipt `json:"-"`
}

// String 禁止打印 Search 操作或 receipt 细节。
func (SearchKnowledgeV2PublicationAuthority) String() string {
	return "SearchKnowledgeV2PublicationAuthority{redacted}"
}

// GoString 避免 %#v 绕过安全投影。
func (authority SearchKnowledgeV2PublicationAuthority) GoString() string { return authority.String() }

// LogValue 禁止结构化日志反射序列化 Search 发布 authority。
func (SearchKnowledgeV2PublicationAuthority) LogValue() slog.Value {
	return slog.GroupValue(slog.String("binding", "redacted"))
}

// SearchKnowledgeV2PublicationAuthorityReader 读取与 Analysis Run、Operation、Call 完整闭合的 Search receipt。
type SearchKnowledgeV2PublicationAuthorityReader interface {
	LoadSearchKnowledgeV2PublicationAuthority(
		context.Context,
		SearchKnowledgeV2PublicationAuthorityQuery,
	) (SearchKnowledgeV2PublicationAuthority, error)
}

// WorkspaceAnalysisSynthesisEvidenceAuthorityQuery 精确定位一个 Analysis Run 的已打开证据闭包。
type WorkspaceAnalysisSynthesisEvidenceAuthorityQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
}

// WorkspaceAnalysisSynthesisEvidenceAuthority 只投影同 Run Search/Read receipts 和模型安全摘录。
// 所有字段都禁止被通用 JSON 或结构化日志反射序列化。
type WorkspaceAnalysisSynthesisEvidenceAuthority struct {
	WorkspaceID        foundation.ID                        `json:"-"`
	WorkflowRunID      foundation.ID                        `json:"-"`
	AnalysisRunID      foundation.ID                        `json:"-"`
	EvidenceRefs       []string                             `json:"-"`
	SearchReceipt      domain.ResultReceipt                 `json:"-"`
	SearchReceipts     []domain.ResultReceipt               `json:"-"`
	ReadSourceReceipts []domain.ResultReceipt               `json:"-"`
	Evidence           []domain.ReadSourceV3ReceiptEvidence `json:"-"`
}

// String 只显示数量，不打印 receipt、hash、摘录或身份。
func (authority WorkspaceAnalysisSynthesisEvidenceAuthority) String() string {
	return "WorkspaceAnalysisSynthesisEvidenceAuthority{evidence_refs:" + strconv.Itoa(len(authority.EvidenceRefs)) +
		" read_receipts:" + strconv.Itoa(len(authority.ReadSourceReceipts)) + "}"
}

// GoString 避免 %#v 绕过安全投影。
func (authority WorkspaceAnalysisSynthesisEvidenceAuthority) GoString() string {
	return authority.String()
}

// LogValue 仅向结构化日志提供有界计数。
func (authority WorkspaceAnalysisSynthesisEvidenceAuthority) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("evidence_refs", len(authority.EvidenceRefs)),
		slog.Int("read_receipts", len(authority.ReadSourceReceipts)),
	)
}

// WorkspaceAnalysisSynthesisEvidenceAuthorityReader 读取合成前唯一、完整的同 Run 证据闭包。
type WorkspaceAnalysisSynthesisEvidenceAuthorityReader interface {
	LoadWorkspaceAnalysisSynthesisEvidence(
		context.Context,
		WorkspaceAnalysisSynthesisEvidenceAuthorityQuery,
	) (WorkspaceAnalysisSynthesisEvidenceAuthority, error)
}

// ValidateCitationV3AuthorityQuery 是服务端加载候选及其上游回执的窄查询。
type ValidateCitationV3AuthorityQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	CandidateID   foundation.ID
}

// ValidateCitationV3Authority 只投影候选绑定和已经领域校验的持久回执。
// Candidate 正文不得经过该边界。
type ValidateCitationV3Authority struct {
	WorkspaceID        foundation.ID          `json:"-"`
	WorkflowRunID      foundation.ID          `json:"-"`
	AnalysisRunID      foundation.ID          `json:"-"`
	CandidateID        foundation.ID          `json:"-"`
	CandidateHash      string                 `json:"-"`
	EvidenceRefs       []string               `json:"-"`
	SearchReceipt      domain.ResultReceipt   `json:"-"`
	ReadSourceReceipts []domain.ResultReceipt `json:"-"`
}

// String 仅显示计数，不打印 receipt 文档、候选哈希或 Citation tuple。
func (authority ValidateCitationV3Authority) String() string {
	return "ValidateCitationV3Authority{evidence_refs:" + strconv.Itoa(len(authority.EvidenceRefs)) +
		" read_receipts:" + strconv.Itoa(len(authority.ReadSourceReceipts)) + "}"
}

// GoString 避免 %#v 绕过安全投影。
func (authority ValidateCitationV3Authority) GoString() string { return authority.String() }

// LogValue 仅向结构化日志提供有界计数，不解析候选或 receipt 文档。
func (authority ValidateCitationV3Authority) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("evidence_refs", len(authority.EvidenceRefs)),
		slog.Int("read_receipts", len(authority.ReadSourceReceipts)),
	)
}

// ValidateCitationV3AuthorityReader 仅供受信 Worker 批量读取一个候选的同 Run 权威事实。
// 返回的 ResultReceipt 必须已通过 domain.ValidateResultReceipt 校验。
type ValidateCitationV3AuthorityReader interface {
	LoadValidateCitationV3Authority(context.Context, ValidateCitationV3AuthorityQuery) (ValidateCitationV3Authority, error)
}

// ValidateCitationV3ReceiptQuery 精确定位一次候选校验已经完成的 canonical receipt。
type ValidateCitationV3ReceiptQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
	CandidateID   foundation.ID
	CandidateHash string
}

// ValidateCitationV3ReceiptReader 读取与候选和 Citation validation 逻辑操作精确闭合的 receipt。
type ValidateCitationV3ReceiptReader interface {
	LoadValidateCitationV3Receipt(context.Context, ValidateCitationV3ReceiptQuery) (domain.ResultReceipt, error)
}

// ValidateCitationV3PublicationAuthorityQuery 精确定位发布前 Citation 校验闭包。
type ValidateCitationV3PublicationAuthorityQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
	CandidateID   foundation.ID
	CandidateHash string
	ReceiptID     foundation.ID
	ReceiptHash   string
}

// ValidateCitationV3PublicationAuthority 只返回发布器所需的校验操作身份和 canonical receipt。
// 候选正文、Citation tuple 和 receipt 文档不得经通用 JSON 或日志投影。
type ValidateCitationV3PublicationAuthority struct {
	OperationID foundation.ID        `json:"-"`
	Receipt     domain.ResultReceipt `json:"-"`
}

// String 禁止打印操作、Call、receipt 身份、哈希或文档。
func (ValidateCitationV3PublicationAuthority) String() string {
	return "ValidateCitationV3PublicationAuthority{redacted}"
}

// GoString 避免 %#v 绕过安全投影。
func (authority ValidateCitationV3PublicationAuthority) GoString() string {
	return authority.String()
}

// LogValue 禁止结构化日志反射序列化发布 authority。
func (ValidateCitationV3PublicationAuthority) LogValue() slog.Value {
	return slog.GroupValue(slog.String("binding", "redacted"))
}

// ValidateCitationV3PublicationAuthorityReader 读取与候选、逻辑操作和 Call 完整闭合的发布前校验事实。
type ValidateCitationV3PublicationAuthorityReader interface {
	LoadValidateCitationV3PublicationAuthority(
		context.Context,
		ValidateCitationV3PublicationAuthorityQuery,
	) (ValidateCitationV3PublicationAuthority, error)
}
