package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// PrepareSynthesisManuscript 只包含身份标识。文件字节、模型输出、
// 所属模块状态和授权始终由服务端管理的端口提供。
type PrepareSynthesisManuscript struct {
	WorkspaceID    foundation.ID `json:"workspace_id"`
	NoteID         foundation.ID `json:"note_id"`
	ProcessingID   foundation.ID `json:"processing_id"`
	IdempotencyKey string        `json:"idempotency_key"`
}

// SynthesisManuscriptAuthority 指明每个可变的所属模块基线。
// 仅有授权 ID 不足以构成证明，根目录身份和工作区绑定也须冻结。
type SynthesisManuscriptAuthority struct {
	DocumentID                foundation.ID          `json:"document_id"`
	DocumentVersion           int64                  `json:"document_version"`
	NoteVersion               int64                  `json:"note_version"`
	LatestArticleID           foundation.ID          `json:"latest_article_id"`
	PublishedPublicationID    foundation.ID          `json:"published_publication_id,omitempty"`
	PublishedProposalCommitID foundation.ID          `json:"published_proposal_commit_id,omitempty"`
	PublishedGitCommit        string                 `json:"published_git_commit,omitempty"`
	TargetPath                string                 `json:"target_path"`
	RootGrantID               foundation.ID          `json:"root_grant_id"`
	RootFingerprint           string                 `json:"root_fingerprint"`
	WorkspaceBindingVersion   int64                  `json:"workspace_binding_version"`
	Anchor                    SynthesisAnchorBinding `json:"anchor"`
}

// SynthesisManuscriptPrepared 是仅供服务端使用的证明封装。GenerationInput
// 和 Generation 保留既有的精确模型、来源和工作流契约；
// Semantic 身份必须由独立的不可变模型账本证明。
type SynthesisManuscriptPrepared struct {
	Authority          SynthesisManuscriptAuthority  `json:"authority"`
	GenerationInput    SynthesisGenerationInput      `json:"generation_input"`
	Generation         SynthesisGenerationResult     `json:"generation"`
	SemanticModelRunID foundation.ID                 `json:"semantic_model_run_id"`
	SemanticOutputHash string                        `json:"semantic_output_hash"`
	MergeInput         SynthesisManuscriptMergeInput `json:"merge_input"`
}

type SynthesisManuscriptBaselineReader interface {
	// Load 返回当前 L/P 及根据精确验证过的生成结果派生的机器投影。
	// MergeInput.FileContent/Exists 必须为空/false，只有捕获读取器可以填充它们。
	LoadSynthesisManuscriptBaseline(context.Context, PrepareSynthesisManuscript) (SynthesisManuscriptPrepared, error)
}

type SynthesisManuscriptProof interface {
	// Verify 锁定并重验 L、Article 最新修订和 document 版本、有证明的 P 及其祖先链、
	// 待处理提案、生成和独立语义账本、工作流租约、精确机器增量、来源/片段准入及根目录授权。
	// 实现必须在当前事务内组合已有的所属模块作用域门禁。
	// 返回的授权须独立重读，并与冻结授权相等。生产中不提供宽松放行的实现。
	VerifySynthesisManuscriptPreparedScoped(context.Context, foundation.TransactionScope, SynthesisManuscriptPrepared) (SynthesisManuscriptAuthority, error)
}

type SynthesisManuscriptFileReader interface {
	CurrentContent(context.Context, foundation.ID, string, int64) ([]byte, string, error)
	EnsureTargetAbsent(context.Context, foundation.ID, string, string) error
}

type SynthesisManuscriptCapture struct {
	ID                      foundation.ID `json:"id"`
	WorkspaceID             foundation.ID `json:"workspace_id"`
	TargetPath              string        `json:"target_path"`
	RootGrantID             foundation.ID `json:"root_grant_id"`
	RootFingerprint         string        `json:"root_fingerprint"`
	WorkspaceBindingVersion int64         `json:"workspace_binding_version"`
	Exists                  bool          `json:"exists"`
	Bytes                   []byte        `json:"bytes"`
	ContentHash             string        `json:"content_hash"`
	AbsenceToken            string        `json:"absence_token"`
	CreatedAt               time.Time     `json:"created_at"`
}

type SynthesisManuscriptAttempt struct {
	ID        foundation.ID                   `json:"id"`
	Command   PrepareSynthesisManuscript      `json:"command"`
	CaptureID foundation.ID                   `json:"capture_id"`
	Prepared  SynthesisManuscriptPrepared     `json:"prepared"`
	Preview   SynthesisManuscriptMergePreview `json:"preview"`
	CreatedAt time.Time                       `json:"created_at"`
	Hash      string                          `json:"hash"`
}

type SynthesisManuscriptReceipt struct {
	Review      *SynthesisManuscriptReceiptReview `json:"review,omitempty"`
	ID          foundation.ID                     `json:"id"`
	WorkspaceID foundation.ID                     `json:"workspace_id"`
	AttemptID   foundation.ID                     `json:"attempt_id"`
	AttemptHash string                            `json:"attempt_hash"`
	Manuscript  domain.SynthesisManuscript        `json:"manuscript"`
	CreatedAt   time.Time                         `json:"created_at"`
	Hash        string                            `json:"hash"`
}

// 此存储切片不提供冲突裁决或发布 API。
// 回执是后续候选事务所需的证明，不是发布审批。
type SynthesisManuscriptStore interface {
	Prepare(context.Context, PrepareSynthesisManuscript) (SynthesisManuscriptAttempt, error)
	GetAttempt(context.Context, foundation.ID, foundation.ID) (SynthesisManuscriptAttempt, error)
	SealClean(context.Context, foundation.ID, foundation.ID) (SynthesisManuscriptReceipt, error)
	VerifyReceiptScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, domain.SynthesisRevision) error
}
