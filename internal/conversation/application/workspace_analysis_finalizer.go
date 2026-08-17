package application

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeWorkspaceAnalysisFinalizerInvalid 表示工作区分析发布命令不满足冻结合同。
	ErrorCodeWorkspaceAnalysisFinalizerInvalid = "CONVERSATION_WORKSPACE_ANALYSIS_FINALIZE_INVALID"
	// ErrorCodeWorkspaceAnalysisFinalizationUnknown 表示提交结果无法确认，调用方只能按 exact lookup 恢复。
	ErrorCodeWorkspaceAnalysisFinalizationUnknown = "CONVERSATION_WORKSPACE_ANALYSIS_FINALIZE_UNKNOWN"
)

// WorkspaceAnalysisPublicationLookup 冻结工作区分析运行、当前 Workflow Attempt 与 Answer 发布槽身份。
type WorkspaceAnalysisPublicationLookup struct {
	AnswerPublicationLookup
	AnalysisRunID      foundation.ID
	ExpectedLeaseOwner string
	ExpectedLeaseFence int64
}

// Validate 校验工作区分析发布查找身份均为 canonical 且没有复用。
func (lookup WorkspaceAnalysisPublicationLookup) Validate() error {
	ids := []foundation.ID{
		lookup.WorkspaceID, lookup.WorkflowRunID, lookup.NodeRunID, lookup.NodeAttemptID,
		lookup.ConversationID, lookup.QuestionID, lookup.AnswerID, lookup.AnalysisRunID,
	}
	if !validWorkspaceAnalysisFinalizerIDs(ids) ||
		lookup.ExpectedLeaseOwner == "" || strings.TrimSpace(lookup.ExpectedLeaseOwner) != lookup.ExpectedLeaseOwner ||
		len(lookup.ExpectedLeaseOwner) > 256 || !utf8.ValidString(lookup.ExpectedLeaseOwner) ||
		lookup.ExpectedLeaseFence < 1 || lookup.ExpectedLeaseFence > math.MaxInt32 {
		return invalidWorkspaceAnalysisFinalizerCommand(errors.New("workspace analysis publication lookup identity is invalid"))
	}
	return nil
}

// FinalizeWorkspaceAnalysisSuccessCommand 只引用已持久化的成功事实；公开正文由服务端重新投影。
type FinalizeWorkspaceAnalysisSuccessCommand struct {
	WorkspaceAnalysisPublicationLookup
	ExpectedAnswerVersion int64
	CandidateID           foundation.ID
	CandidateHash         string
	GitReceiptID          foundation.ID
	GitReceiptHash        string
	ValidationReceiptID   foundation.ID
	ValidationReceiptHash string
	ReviewModelResultID   foundation.ID
	ReviewModelResultHash string
}

// Validate 校验成功发布命令只包含完整且互不复用的事实引用。
func (command FinalizeWorkspaceAnalysisSuccessCommand) Validate() error {
	if err := command.WorkspaceAnalysisPublicationLookup.Validate(); err != nil {
		return err
	}
	ids := []foundation.ID{command.CandidateID, command.GitReceiptID, command.ValidationReceiptID, command.ReviewModelResultID}
	if command.ExpectedAnswerVersion < 1 || !validWorkspaceAnalysisFinalizerIDs(ids) ||
		!workspaceAnalysisFinalizerIDsDisjoint(command.WorkspaceAnalysisPublicationLookup, ids) ||
		!validWorkspaceAnalysisFinalizerHash(command.CandidateHash) ||
		!validWorkspaceAnalysisFinalizerHash(command.GitReceiptHash) ||
		!validWorkspaceAnalysisFinalizerHash(command.ValidationReceiptHash) ||
		!validWorkspaceAnalysisFinalizerHash(command.ReviewModelResultHash) {
		return invalidWorkspaceAnalysisFinalizerCommand(errors.New("workspace analysis success proof reference is invalid"))
	}
	return nil
}

// WorkspaceAnalysisTerminationArtifactKind 区分终止证明引用的 Tool receipt 与模型结果。
type WorkspaceAnalysisTerminationArtifactKind string

const (
	// WorkspaceAnalysisTerminationArtifactToolReceipt 表示不可变 Tool 结果回执。
	WorkspaceAnalysisTerminationArtifactToolReceipt WorkspaceAnalysisTerminationArtifactKind = "TOOL_RECEIPT"
	// WorkspaceAnalysisTerminationArtifactModelResult 表示不可变 Planner 或 Review 模型结果。
	WorkspaceAnalysisTerminationArtifactModelResult WorkspaceAnalysisTerminationArtifactKind = "MODEL_RESULT"
)

// WorkspaceAnalysisTerminationArtifact 引用迁移允许作为终止证据的不可变文档。
type WorkspaceAnalysisTerminationArtifact struct {
	Kind WorkspaceAnalysisTerminationArtifactKind
	ID   foundation.ID
	Hash string
}

// WorkspaceAnalysisBudgetRequest 保存授权失败时对下一逻辑操作的 exact 预算请求。
type WorkspaceAnalysisBudgetRequest struct {
	ModelCalls              int
	ToolCalls               int
	SourceReads             int
	InputTokens             int64
	OutputTokens            int64
	RequestedCostMicrounits *int64
}

// WorkspaceAnalysisReceiptFailureCode 是迁移冻结的 receipt 校验失败类型。
type WorkspaceAnalysisReceiptFailureCode string

const (
	// WorkspaceAnalysisReceiptMissing 表示成功 Call 没有产生可证明的 receipt。
	WorkspaceAnalysisReceiptMissing WorkspaceAnalysisReceiptFailureCode = "MISSING"
	// WorkspaceAnalysisReceiptHashMismatch 表示观察到的输出 hash 与 Call 不同。
	WorkspaceAnalysisReceiptHashMismatch WorkspaceAnalysisReceiptFailureCode = "HASH_MISMATCH"
	// WorkspaceAnalysisReceiptContractInvalid 表示输出不满足 exact Tool 合同。
	WorkspaceAnalysisReceiptContractInvalid WorkspaceAnalysisReceiptFailureCode = "CONTRACT_INVALID"
	// WorkspaceAnalysisReceiptBindingInvalid 表示 server-only binding 不满足 exact 合同。
	WorkspaceAnalysisReceiptBindingInvalid WorkspaceAnalysisReceiptFailureCode = "BINDING_INVALID"
)

// WorkspaceAnalysisReceiptFailure 引用已持久化的 receipt failure 及其公开可比较 hash。
type WorkspaceAnalysisReceiptFailure struct {
	ID           foundation.ID
	Code         WorkspaceAnalysisReceiptFailureCode
	ExpectedHash string
	ActualHash   *string
}

// FinalizeWorkspaceAnalysisTerminationCommand 请求发布迁移支持的拒答、澄清、失败或取消终态。
// Summary、公开正文和 Model Run 绑定均由服务端从权威事实派生。
type FinalizeWorkspaceAnalysisTerminationCommand struct {
	WorkspaceAnalysisPublicationLookup
	ExpectedAnswerVersion int64
	Reason                agentdomain.WorkspaceAnalysisRunTerminationReason
	OperationID           *foundation.ID
	Artifact              *WorkspaceAnalysisTerminationArtifact
	BudgetRequest         *WorkspaceAnalysisBudgetRequest
	ReceiptFailure        *WorkspaceAnalysisReceiptFailure
}

// Validate 校验 termination reason 与迁移证明参数矩阵完全一致。
func (command FinalizeWorkspaceAnalysisTerminationCommand) Validate() error {
	if err := command.WorkspaceAnalysisPublicationLookup.Validate(); err != nil {
		return err
	}
	if command.ExpectedAnswerVersion < 1 || !validWorkspaceAnalysisTerminationShape(command) {
		return invalidWorkspaceAnalysisFinalizerCommand(errors.New("workspace analysis termination proof shape is invalid"))
	}
	ids := make([]foundation.ID, 0, 3)
	if command.OperationID != nil {
		ids = append(ids, *command.OperationID)
	}
	if command.Artifact != nil {
		ids = append(ids, command.Artifact.ID)
		if !validWorkspaceAnalysisFinalizerHash(command.Artifact.Hash) {
			return invalidWorkspaceAnalysisFinalizerCommand(errors.New("workspace analysis termination artifact hash is invalid"))
		}
	}
	if command.ReceiptFailure != nil {
		ids = append(ids, command.ReceiptFailure.ID)
		if !validWorkspaceAnalysisReceiptFailure(*command.ReceiptFailure) {
			return invalidWorkspaceAnalysisFinalizerCommand(errors.New("workspace analysis receipt failure proof is invalid"))
		}
	}
	if !validWorkspaceAnalysisFinalizerIDs(ids) || !workspaceAnalysisFinalizerIDsDisjoint(command.WorkspaceAnalysisPublicationLookup, ids) {
		return invalidWorkspaceAnalysisFinalizerCommand(errors.New("workspace analysis termination proof identity is invalid"))
	}
	return nil
}

// WorkspaceAnalysisFinalizer 是 review_publish 与安全终止路径访问 Answer 终态的唯一应用层端口。
type WorkspaceAnalysisFinalizer interface {
	// LookupPublication 返回 exact 已发布回执；pending 返回 found=false，split state 失败关闭。
	LookupPublication(context.Context, WorkspaceAnalysisPublicationLookup) (workflow.WorkspaceAnalysisPublicationOutput, bool, error)
	// FinalizeSuccess 原子发布服务器派生的成功正文；bool 表示 exact replay。
	FinalizeSuccess(context.Context, FinalizeWorkspaceAnalysisSuccessCommand) (workflow.WorkspaceAnalysisPublicationOutput, bool, error)
	// FinalizeTermination 原子发布拒答、澄清、失败或取消正文；bool 表示 exact replay。
	FinalizeTermination(context.Context, FinalizeWorkspaceAnalysisTerminationCommand) (workflow.WorkspaceAnalysisPublicationOutput, bool, error)
}

func validWorkspaceAnalysisTerminationShape(command FinalizeWorkspaceAnalysisTerminationCommand) bool {
	hasOperation := command.OperationID != nil
	hasArtifact := command.Artifact != nil
	hasBudget := command.BudgetRequest != nil
	hasFailure := command.ReceiptFailure != nil
	switch command.Reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient, agentdomain.WorkspaceAnalysisRunCitationInvalid:
		return hasOperation && hasArtifact && command.Artifact.Kind == WorkspaceAnalysisTerminationArtifactToolReceipt && !hasBudget && !hasFailure
	case agentdomain.WorkspaceAnalysisRunFaithfulnessRejected, agentdomain.WorkspaceAnalysisRunNeedsClarification:
		return hasOperation && hasArtifact && command.Artifact.Kind == WorkspaceAnalysisTerminationArtifactModelResult && !hasBudget && !hasFailure
	case agentdomain.WorkspaceAnalysisRunBudgetExhausted:
		return hasOperation && !hasArtifact && hasBudget && !hasFailure && validWorkspaceAnalysisBudgetRequest(*command.BudgetRequest)
	case agentdomain.WorkspaceAnalysisRunReceiptInvalid:
		return hasOperation && !hasArtifact && !hasBudget && hasFailure
	case agentdomain.WorkspaceAnalysisRunDeadlineExceeded:
		return !hasArtifact && !hasBudget && !hasFailure
	case agentdomain.WorkspaceAnalysisRunModelRefused, agentdomain.WorkspaceAnalysisRunResultUnknown,
		agentdomain.WorkspaceAnalysisRunModelFailed,
		agentdomain.WorkspaceAnalysisRunToolFailed:
		return hasOperation && !hasArtifact && !hasBudget && !hasFailure
	case agentdomain.WorkspaceAnalysisRunCancellation:
		return !hasOperation && !hasArtifact && !hasBudget && !hasFailure
	default:
		return false
	}
}

func validWorkspaceAnalysisBudgetRequest(request WorkspaceAnalysisBudgetRequest) bool {
	return request.ModelCalls >= 0 && request.ToolCalls >= 0 && request.SourceReads >= 0 &&
		request.InputTokens >= 0 && request.OutputTokens >= 0 && request.RequestedCostMicrounits == nil &&
		(request.ModelCalls > 0 || request.ToolCalls > 0 || request.SourceReads > 0 || request.InputTokens > 0 || request.OutputTokens > 0)
}

func validWorkspaceAnalysisReceiptFailure(failure WorkspaceAnalysisReceiptFailure) bool {
	if !validWorkspaceAnalysisFinalizerHash(failure.ExpectedHash) {
		return false
	}
	switch failure.Code {
	case WorkspaceAnalysisReceiptMissing:
		return failure.ActualHash == nil
	case WorkspaceAnalysisReceiptHashMismatch:
		return failure.ActualHash != nil && validWorkspaceAnalysisFinalizerHash(*failure.ActualHash) && *failure.ActualHash != failure.ExpectedHash
	case WorkspaceAnalysisReceiptContractInvalid, WorkspaceAnalysisReceiptBindingInvalid:
		return failure.ActualHash != nil && *failure.ActualHash == failure.ExpectedHash
	default:
		return false
	}
}

func validWorkspaceAnalysisFinalizerIDs(ids []foundation.ID) bool {
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func workspaceAnalysisFinalizerIDsDisjoint(lookup WorkspaceAnalysisPublicationLookup, ids []foundation.ID) bool {
	known := map[foundation.ID]struct{}{
		lookup.WorkspaceID: {}, lookup.WorkflowRunID: {}, lookup.NodeRunID: {}, lookup.NodeAttemptID: {},
		lookup.ConversationID: {}, lookup.QuestionID: {}, lookup.AnswerID: {}, lookup.AnalysisRunID: {},
	}
	for _, id := range ids {
		if _, reused := known[id]; reused {
			return false
		}
	}
	return true
}

func validWorkspaceAnalysisFinalizerHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}

func invalidWorkspaceAnalysisFinalizerCommand(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisFinalizerInvalid, false, cause)
}
