package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const SynthesisSourceReviewSchema = "agent.synthesis-manuscript-source-review"
const SynthesisSourceReviewDefinition = "organizing.synthesis-manuscript-source-review"
const SynthesisSourceReviewRecoveryDefinition = SynthesisSourceReviewDefinition + "-recovery"
const SynthesisSourceReviewRecover = SynthesisSourceReviewRecoveryDefinition + ".apply"
const SynthesisSourceReviewPrepare = SynthesisSourceReviewDefinition + ".prepare"
const SynthesisSourceReviewModel = SynthesisSourceReviewDefinition + ".review"
const SynthesisSourceReviewApply = SynthesisSourceReviewDefinition + ".apply"

// 此记录仅供服务端使用。读取适配器必须投影 SourceReviewView，
// 不能序列化根目录授权、完整模型输入或模型原始输出。
type SynthesisManuscriptSourceReview struct {
	ID, WorkspaceID, OriginProcessingID, OriginWorkflowRunID, WorkflowRunID foundation.ID
	AttemptNo                                                               int
	SupersedesID                                                            foundation.ID
	Status                                                                  string
	Version                                                                 int64
	CreatedAt                                                               time.Time
	CompletedAt                                                             *time.Time
	ErrorCode                                                               string
	Retryable                                                               bool
	Snapshot                                                                *SynthesisSourceReviewSnapshot
	SnapshotHash                                                            string
	ModelRunID, NodeRunID, NodeAttemptID                                    foundation.ID
	RequestHash                                                             string
	Output                                                                  json.RawMessage
	ReceiptHash                                                             string
}
type SynthesisSourceReviewSnapshot struct {
	Targets     []SynthesisSourceReviewTarget     `json:"targets"`
	Obligations []SynthesisSourceReviewObligation `json:"obligations"`
}
type SynthesisSourceReviewTarget struct {
	Label               string                           `json:"label"`
	NoteID              foundation.ID                    `json:"note_id"`
	BaseRevisionID      foundation.ID                    `json:"base_revision_id"`
	ProjectionHash      string                           `json:"projection_hash"`
	TargetKind          string                           `json:"target_kind"`
	Authority           SynthesisManuscriptAuthority     `json:"authority"`
	Anchor              *SynthesisAnchorBinding          `json:"anchor"`
	PublishedRevisionID foundation.ID                    `json:"published_revision_id,omitempty"`
	FullContent         string                           `json:"full_content"`
	FullContentHash     string                           `json:"full_content_hash"`
	FileExists          bool                             `json:"file_exists"`
	FileHash            string                           `json:"file_hash"`
	Paragraphs          []SynthesisSourceReviewParagraph `json:"paragraphs"`
}
type SynthesisSourceReviewParagraph = domain.SynthesisSourceReviewParagraph
type SynthesisSourceReviewObligation struct {
	Label                   string                    `json:"label"`
	NoteLabel               string                    `json:"note"`
	SourceLabel             string                    `json:"source"`
	Source                  domain.SynthesisSourceRef `json:"reference"`
	HistoricalStatement     string                    `json:"historical_statement"`
	HistoricalApplicability string                    `json:"historical_applicability"`
}
type SynthesisSourceReviewCheck struct {
	Obligation string   `json:"obligation"`
	Source     string   `json:"source"`
	Targets    []string `json:"targets"`
	Verdict    string   `json:"verdict"`
	ReasonCode string   `json:"reason_code"`
}
type SynthesisSourceReviewOutput struct {
	Checks []SynthesisSourceReviewCheck `json:"checks"`
}
type SynthesisSourceReviewInput struct {
	Snapshot SynthesisSourceReviewSnapshot
	Sources  []SynthesisSourceExcerpt
}
type SynthesisSourceReviewParagraphMapper interface {
	SourceReviewParagraphs(string, string) ([]SynthesisSourceReviewParagraph, error)
}

// 此端口仅提供给可信运行时执行器，不能作为 HTTP 命令服务。
// 公开命令须另行检查已认证的能力。
type SynthesisSourceReviewStore interface {
	GetSourceReview(context.Context, foundation.ID, foundation.ID) (SynthesisManuscriptSourceReview, error)
	PrepareSourceReview(context.Context, workflowapp.ExecutionContext, foundation.ID) (SynthesisManuscriptSourceReview, error)
	ReadSourceReviewInput(context.Context, workflowapp.ExecutionContext, foundation.ID) (SynthesisSourceReviewInput, error)
	ClaimSourceReview(context.Context, workflowapp.ExecutionContext, foundation.ID, foundation.ID, string) (SynthesisManuscriptSourceReview, error)
	CompleteSourceReview(context.Context, workflowapp.ExecutionContext, foundation.ID, []byte) (SynthesisManuscriptSourceReview, error)
	ApplySourceReview(context.Context, workflowapp.ExecutionContext, foundation.ID) (SynthesisManuscriptSourceReview, error)
	FailSourceReview(context.Context, foundation.ID, foundation.ID, foundation.ID, error) error
}
type SynthesisSourceReviewDispatchStore interface {
	DispatchSourceReviewScoped(context.Context, foundation.TransactionScope, foundation.ID) (SynthesisManuscriptSourceReview, bool, error)
	BindSourceReviewWorkflowScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, foundation.ID) error
}
type SynthesisSourceReviewView struct {
	SupersedesID          foundation.ID                     `json:"supersedes_id,omitempty"`
	Latest                bool                              `json:"latest"`
	CanRecheck            bool                              `json:"can_recheck"`
	CanRecover            bool                              `json:"can_recover"`
	RecoveryWorkflowRunID foundation.ID                     `json:"recovery_workflow_run_id,omitempty"`
	RecoveryCompletedAt   *time.Time                        `json:"recovery_completed_at,omitempty"`
	RecoveryStatus        string                            `json:"recovery_status,omitempty"`
	ID                    foundation.ID                     `json:"id"`
	WorkspaceID           foundation.ID                     `json:"workspace_id"`
	OriginProcessingID    foundation.ID                     `json:"origin_processing_id"`
	OriginWorkflowRunID   foundation.ID                     `json:"origin_workflow_run_id"`
	WorkflowRunID         foundation.ID                     `json:"workflow_run_id"`
	AttemptNo             int                               `json:"attempt_no"`
	Version               int64                             `json:"version"`
	Status                string                            `json:"status"`
	EffectiveStatus       string                            `json:"effective_status"`
	Completed             bool                              `json:"completed"`
	Retryable             bool                              `json:"retryable"`
	Failure               string                            `json:"failure,omitempty"`
	CreatedAt             time.Time                         `json:"created_at"`
	CompletedAt           *time.Time                        `json:"completed_at,omitempty"`
	Targets               []SynthesisSourceReviewTargetView `json:"targets"`
	ObligationCount       int                               `json:"obligation_count"`
	SupportedCount        int                               `json:"supported_count"`
	ReceiptHash           string                            `json:"receipt_hash,omitempty"`
}
type SynthesisSourceReviewTargetView struct {
	NoteID          foundation.ID `json:"note_id"`
	BaseRevisionID  foundation.ID `json:"base_revision_id"`
	TargetKind      string        `json:"target_kind"`
	FullContentHash string        `json:"full_content_hash"`
	// 精确的已复核快照有明确范围；TargetKind 为 LOCAL_FILE 时，
	// 不能把它渲染成 BaseRevisionID 对应修订的内容。
	FullContent string                              `json:"full_content"`
	Paragraphs  []SynthesisSourceReviewParagraph    `json:"paragraphs"`
	Evidence    []SynthesisSourceReviewEvidenceView `json:"evidence"`
}
type SynthesisSourceReviewEvidenceView struct {
	ID          foundation.ID             `json:"id"`
	Obligations []string                  `json:"obligations"`
	Paragraph   string                    `json:"paragraph"`
	Source      domain.SynthesisSourceRef `json:"source"`
}
