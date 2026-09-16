package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// AnchorFusionRequest 将已审核的关联持久交接给受限合成工作流。
// 它不会替代摄取事件。
type AnchorFusionRequest struct {
	ID             foundation.ID               `json:"id"`
	WorkspaceID    foundation.ID               `json:"workspace_id"`
	AnchorID       foundation.ID               `json:"anchor_id"`
	NoteID         foundation.ID               `json:"note_id"`
	ProposalID     foundation.ID               `json:"proposal_id"`
	ScopeVersion   int64                       `json:"scope_version"`
	SourceEvent    domain.SynthesisSourceReady `json:"source_event"`
	AllowedSources []domain.SynthesisSourceRef `json:"allowed_sources"`
	ProcessingID   foundation.ID               `json:"processing_id,omitempty"`
	Status         string                      `json:"status"`
	CreatedAt      time.Time                   `json:"created_at"`
	UpdatedAt      time.Time                   `json:"updated_at"`
}

type AnchorFusionRequestStore interface {
	ClaimAnchorFusionRequestScoped(context.Context, foundation.TransactionScope, []foundation.ID) (AnchorFusionRequest, bool, error)
	MarkAnchorFusionRequestStartedScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID) error
	MarkAnchorFusionRequestStaleScoped(context.Context, foundation.TransactionScope, foundation.ID) error
}

// AnchorFusionRequestReader 提供待处理和已调度的审批驱动合成读取模型。
// HTTP 组合根可直接暴露此投影，无需轮询 Worker 或根据锚点决策推断完成状态。
type AnchorFusionRequestReader interface {
	GetAnchorFusionRequest(context.Context, foundation.ID, foundation.ID) (AnchorFusionRequest, error)
	ListAnchorFusionRequests(context.Context, foundation.ID, foundation.ID, int) ([]AnchorFusionRequest, error)
}

type CreateAnchorCommand struct {
	WorkspaceID         foundation.ID      `json:"-"`
	IdempotencyKey      string             `json:"-"`
	NoteID              foundation.ID      `json:"note_id"`
	ExpectedNoteVersion int64              `json:"expected_note_version"`
	BasisRevisionID     foundation.ID      `json:"basis_revision_id"`
	Title               string             `json:"title"`
	Scope               domain.AnchorScope `json:"scope"`
}
type AnchorMutationResult struct {
	Anchor   domain.Anchor `json:"anchor"`
	Replayed bool          `json:"replayed"`
}
type AnchorListQuery struct {
	WorkspaceID foundation.ID
	NoteID      foundation.ID
	AfterID     foundation.ID
	Limit       int
}
type AnchorPage struct {
	Items       []domain.Anchor `json:"items"`
	NextAfterID *foundation.ID  `json:"next_after_id"`
}
type AnchorProposalPage struct {
	Items       []domain.AnchorProposal `json:"items"`
	NextAfterID *foundation.ID          `json:"next_after_id"`
}
type AnchorProposalQuery struct {
	WorkspaceID foundation.ID
	AnchorID    foundation.ID
	Kind        string
	AfterID     foundation.ID
	Limit       int
}
type AnchorDecisionItem struct {
	ProposalID      foundation.ID `json:"proposal_id"`
	ExpectedVersion int64         `json:"expected_version"`
}
type DecideAnchorCommand struct {
	WorkspaceID           foundation.ID         `json:"-"`
	AnchorID              foundation.ID         `json:"-"`
	IdempotencyKey        string                `json:"-"`
	Kind                  string                `json:"-"`
	ExpectedAnchorVersion int64                 `json:"expected_anchor_version"`
	Decision              domain.AnchorDecision `json:"decision"`
	Items                 []AnchorDecisionItem  `json:"items"`
}
type AnchorDecisionResult struct {
	Anchor   domain.Anchor           `json:"anchor"`
	Items    []domain.AnchorProposal `json:"items"`
	Replayed bool                    `json:"replayed"`
}

// 建议写入端口仅供 Worker 使用，不能通过 HTTP 接收用户提供的模型输出。
// 适配器会重新核验模型运行及精确证据。
type RecordAnchorRecommendation struct {
	RequestID            foundation.ID
	WorkspaceID          foundation.ID
	AnchorID             foundation.ID
	IdempotencyKey       string
	ExpectedScopeVersion int64
	Kind                 string
	Suggested            *domain.AnchorScope
	Reason               string
	Evidence             []domain.SynthesisSourceRef
	ModelRunID           foundation.ID
	ModelOutputHash      string
	ModelOutput          json.RawMessage
}
type AnchorRecommendationResult struct {
	Proposal domain.AnchorProposal `json:"proposal"`
	Replayed bool                  `json:"replayed"`
}

// AnchorRecommendationRequest 是 AI 建议的持久 Worker 输入和公开读取模型。
// HTTP 只能创建初始请求或重试已知失败，不能提供范围、证据或 ModelRun ID。
type AnchorRecommendationRequest struct {
	ID                   foundation.ID                     `json:"id"`
	WorkspaceID          foundation.ID                     `json:"workspace_id"`
	NoteID               foundation.ID                     `json:"note_id"`
	AnchorID             foundation.ID                     `json:"anchor_id,omitempty"`
	BasisRevisionID      foundation.ID                     `json:"basis_revision_id"`
	ExpectedScopeVersion int64                             `json:"expected_scope_version,omitempty"`
	Kind                 string                            `json:"kind"`
	Source               *domain.SynthesisSourceVersion    `json:"source,omitempty"`
	Evidence             []domain.SynthesisSourceRef       `json:"evidence"`
	Status               domain.AnchorRecommendationStatus `json:"status"`
	ModelRunID           foundation.ID                     `json:"model_run_id,omitempty"`
	ModelInputHash       string                            `json:"model_input_hash,omitempty"`
	ModelOutput          json.RawMessage                   `json:"model_output,omitempty"`
	ProposalID           foundation.ID                     `json:"proposal_id,omitempty"`
	ErrorCode            string                            `json:"error_code,omitempty"`
	Retryable            bool                              `json:"retryable"`
	Version              int64                             `json:"version"`
	CreatedAt            time.Time                         `json:"created_at"`
	UpdatedAt            time.Time                         `json:"updated_at"`
}

const AnchorInitialScopeRecommendation = "INITIAL_SCOPE"

type RequestInitialAnchorRecommendationCommand struct {
	WorkspaceID         foundation.ID `json:"-"`
	NoteID              foundation.ID `json:"note_id"`
	ExpectedNoteVersion int64         `json:"expected_note_version"`
	BasisRevisionID     foundation.ID `json:"basis_revision_id"`
	IdempotencyKey      string        `json:"-"`
}

type RetryAnchorRecommendationCommand struct {
	WorkspaceID     foundation.ID `json:"-"`
	RequestID       foundation.ID `json:"request_id"`
	ExpectedVersion int64         `json:"expected_version"`
	IdempotencyKey  string        `json:"-"`
}

type AnchorRecommendationListQuery struct {
	WorkspaceID foundation.ID
	NoteID      foundation.ID
	AnchorID    foundation.ID
	Status      domain.AnchorRecommendationStatus
	AfterID     foundation.ID
	Limit       int
}

type AnchorRecommendationPage struct {
	Items       []AnchorRecommendationRequest `json:"items"`
	NextAfterID *foundation.ID                `json:"next_after_id"`
}

type AnchorRecommendationService interface {
	RequestInitialAnchorRecommendation(context.Context, RequestInitialAnchorRecommendationCommand) (AnchorRecommendationRequest, error)
	GetAnchorRecommendation(context.Context, foundation.ID, foundation.ID) (AnchorRecommendationRequest, error)
	ListAnchorRecommendations(context.Context, AnchorRecommendationListQuery) (AnchorRecommendationPage, error)
	RetryAnchorRecommendation(context.Context, RetryAnchorRecommendationCommand) (AnchorRecommendationRequest, error)
}

// AnchorRecommendationExecutionStore 仅供 Worker 使用。Claim 在创建 ModelRun 前
// 冻结精确的工作流节点身份；调用方不能事后附加已完成的运行。
type AnchorRecommendationExecutionStore interface {
	ClaimAnchorRecommendation(context.Context, ClaimAnchorRecommendationCommand) (AnchorRecommendationRequest, error)
	CompleteAnchorRecommendation(context.Context, CompleteAnchorRecommendationCommand) (AnchorRecommendationRequest, error)
	FailAnchorRecommendation(context.Context, FailAnchorRecommendationCommand) (AnchorRecommendationRequest, error)
}

type ClaimAnchorRecommendationCommand struct {
	WorkspaceID     foundation.ID
	RequestID       foundation.ID
	ExpectedVersion int64
	WorkflowRunID   foundation.ID
	NodeRunID       foundation.ID
	NodeAttemptID   foundation.ID
	ModelInputHash  string
}

type CompleteAnchorRecommendationCommand struct {
	WorkspaceID     foundation.ID
	RequestID       foundation.ID
	ExpectedVersion int64
	ModelRunID      foundation.ID
	ModelOutputHash string
	ModelOutput     json.RawMessage
}

type FailAnchorRecommendationCommand struct {
	WorkspaceID     foundation.ID
	RequestID       foundation.ID
	ExpectedVersion int64
	Status          domain.AnchorRecommendationStatus
	ErrorCode       string
	Retryable       bool
}

type RequestAnchorSourceRecommendationCommand struct {
	WorkspaceID          foundation.ID
	AnchorID             foundation.ID
	ExpectedScopeVersion int64
	Source               domain.SynthesisSourceVersion
	Evidence             []domain.SynthesisSourceRef
	IdempotencyKey       string
}

type AnchorRecommendationSourceRequester interface {
	RequestAnchorSourceRecommendation(context.Context, RequestAnchorSourceRecommendationCommand) (AnchorRecommendationRequest, error)
}

func (c RequestInitialAnchorRecommendationCommand) Validate() error {
	if ValidateAnchorCommand(c.WorkspaceID, c.IdempotencyKey) != nil || !anchorID(c.NoteID) || !anchorID(c.BasisRevisionID) || c.ExpectedNoteVersion < 1 {
		return AnchorInvalid()
	}
	return nil
}

func (c RetryAnchorRecommendationCommand) Validate() error {
	if ValidateAnchorCommand(c.WorkspaceID, c.IdempotencyKey) != nil || !anchorID(c.RequestID) || c.ExpectedVersion < 1 {
		return AnchorInvalid()
	}
	return nil
}

type AnchorRecommendations interface {
	RecordAnchorRecommendation(context.Context, RecordAnchorRecommendation) (AnchorRecommendationResult, error)
}

// AI 所属模块必须返回可审计的成功模型运行，不能用关键词启发式结果代替。
type AnchorRecommender interface {
	RecommendAnchor(context.Context, domain.Anchor, []SynthesisSourceView) (RecordAnchorRecommendation, error)
}
type AnchorService interface {
	CreateAnchor(context.Context, CreateAnchorCommand) (AnchorMutationResult, error)
	GetAnchor(context.Context, foundation.ID, foundation.ID) (domain.Anchor, error)
	ListAnchors(context.Context, AnchorListQuery) (AnchorPage, error)
	ListAnchorProposals(context.Context, AnchorProposalQuery) (AnchorProposalPage, error)
	DecideAnchor(context.Context, DecideAnchorCommand) (AnchorDecisionResult, error)
}

func ValidateAnchorCommand(workspace foundation.ID, key string) error {
	id, err := foundation.ParseID(string(workspace))
	if err != nil || id != workspace || workspace == "00000000-0000-0000-0000-000000000000" || key == "" || key != strings.TrimSpace(key) || len(key) > 128 {
		return AnchorInvalid()
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return AnchorInvalid()
		}
	}
	return nil
}
func AnchorInvalid() error {
	return foundation.NewError(foundation.ErrorInvalidInput, "ANCHOR_INVALID", false, errors.New("anchor request is invalid"))
}
func AnchorConflict() error {
	return foundation.NewError(foundation.ErrorVersionConflict, "ANCHOR_VERSION_CONFLICT", false, errors.New("anchor version or immutable request binding changed"))
}

func (c CreateAnchorCommand) Validate() error {
	if ValidateAnchorCommand(c.WorkspaceID, c.IdempotencyKey) != nil || !anchorID(c.NoteID) || !anchorID(c.BasisRevisionID) || c.ExpectedNoteVersion < 1 || c.Title == "" || c.Title != strings.TrimSpace(c.Title) || len(c.Title) > 512 || c.Scope.Validate() != nil {
		return AnchorInvalid()
	}
	return nil
}
func (c DecideAnchorCommand) Validate() error {
	if ValidateAnchorCommand(c.WorkspaceID, c.IdempotencyKey) != nil || !anchorID(c.AnchorID) || c.ExpectedAnchorVersion < 1 || (c.Decision != domain.AnchorAccepted && c.Decision != domain.AnchorRejected) || len(c.Items) < 1 || len(c.Items) > 32 || (c.Kind != domain.AnchorScopeAdjustment && c.Kind != domain.AnchorSourceAssociation) || c.Kind == domain.AnchorScopeAdjustment && len(c.Items) != 1 {
		return AnchorInvalid()
	}
	seen := map[foundation.ID]bool{}
	for _, i := range c.Items {
		if !anchorID(i.ProposalID) || i.ExpectedVersion != 1 || seen[i.ProposalID] {
			return AnchorInvalid()
		}
		seen[i.ProposalID] = true
	}
	return nil
}
func anchorID(id foundation.ID) bool {
	v, e := foundation.ParseID(string(id))
	return e == nil && v == id && id != "00000000-0000-0000-0000-000000000000"
}

// AnchorRecommendationVerifier 将建议绑定到实际验证过的模型输出和冻结输入。
// 仅有 SUCCEEDED 状态不足以构成证明。
type AnchorRecommendationVerifier interface {
	VerifyAnchorRecommendationScoped(context.Context, foundation.TransactionScope, domain.Anchor, RecordAnchorRecommendation) error
}

// AcceptedAnchorSourceReader 是合成输入组装时的来源准入端口。
// 结果仅包含在锚点当前范围修订下被明确接受的片段，
// 不能将标签或整份来源转换为审批。
type AcceptedAnchorSourceReader interface {
	ReadAcceptedAnchorSources(context.Context, foundation.ID, foundation.ID, domain.SynthesisSourceVersion) ([]domain.SynthesisSourceRef, error)
}

// SynthesisAnchorAdmissionReader 区分未设锚点的笔记与没有已接受片段的锚点笔记，
// 并提供当前范围修订。此端口只用于准备阶段读取，不授予写入权限。
type SynthesisAnchorAdmissionReader interface {
	ReadSynthesisAnchorAdmission(context.Context, foundation.ID, foundation.ID, domain.SynthesisSourceVersion) (SynthesisAnchorAdmission, error)
}

type SynthesisAnchorAdmission struct {
	AnchorID       foundation.ID
	ScopeVersion   int64
	Scope          domain.AnchorScope
	AllowedSources []domain.SynthesisSourceRef
}

// SynthesisAnchorAdmissionFence 在候选所属模块的写入事务内检查，
// 检查时机位于模型验证之后、追加任何修订之前。
type SynthesisAnchorAdmissionFence interface {
	VerifySynthesisAnchorAdmissionScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, domain.SynthesisSourceVersion, *SynthesisAnchorBinding) error
}
