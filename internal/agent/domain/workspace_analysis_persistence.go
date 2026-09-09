package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeWorkspaceAnalysisRunInvalid 表示工作区分析运行合同或生命周期不完整。
	ErrorCodeWorkspaceAnalysisRunInvalid = "AGENT_WORKSPACE_ANALYSIS_RUN_INVALID"
	// ErrorCodeWorkspaceAnalysisRunTransitionInvalid 表示工作区分析运行发生非法状态迁移。
	ErrorCodeWorkspaceAnalysisRunTransitionInvalid = "AGENT_WORKSPACE_ANALYSIS_RUN_TRANSITION_INVALID"
	// ErrorCodeWorkspaceAnalysisBudgetReservationInvalid 表示预算预留或结算事实不完整。
	ErrorCodeWorkspaceAnalysisBudgetReservationInvalid = "AGENT_WORKSPACE_ANALYSIS_BUDGET_RESERVATION_INVALID"
	// ErrorCodeWorkspaceAnalysisBudgetReservationTransitionInvalid 表示预算预留发生非法状态迁移。
	ErrorCodeWorkspaceAnalysisBudgetReservationTransitionInvalid = "AGENT_WORKSPACE_ANALYSIS_BUDGET_RESERVATION_TRANSITION_INVALID"
	// ErrorCodeWorkspaceAnalysisBudgetBindingMismatch 表示运行计数、操作与预算预留不能逐项闭合。
	ErrorCodeWorkspaceAnalysisBudgetBindingMismatch = "AGENT_WORKSPACE_ANALYSIS_BUDGET_BINDING_MISMATCH"
	// ErrorCodeWorkspaceAnalysisCandidateInvalid 表示不可变候选答案身份或文档不合法。
	ErrorCodeWorkspaceAnalysisCandidateInvalid = "AGENT_WORKSPACE_ANALYSIS_CANDIDATE_INVALID"
	// ErrorCodeWorkspaceAnalysisCandidateBindingMismatch 表示候选答案与运行、操作或模型运行不一致。
	ErrorCodeWorkspaceAnalysisCandidateBindingMismatch = "AGENT_WORKSPACE_ANALYSIS_CANDIDATE_BINDING_MISMATCH"
	// ErrorCodeWorkspaceAnalysisModelResultInvalid 表示可恢复模型结果回执身份或文档不合法。
	ErrorCodeWorkspaceAnalysisModelResultInvalid = "AGENT_WORKSPACE_ANALYSIS_MODEL_RESULT_INVALID"
	// ErrorCodeWorkspaceAnalysisModelResultBindingMismatch 表示模型结果回执与运行、调用或候选不一致。
	ErrorCodeWorkspaceAnalysisModelResultBindingMismatch = "AGENT_WORKSPACE_ANALYSIS_MODEL_RESULT_BINDING_MISMATCH"
)

const (
	workspaceAnalysisDefinitionKey       = "workspace-analysis"
	workspaceAnalysisDefinitionVersion   = int64(1)
	workspaceAnalysisMinimumOutputTokens = WorkspaceAnalysisV1PlanMaxOutputTokens + 1 + WorkspaceAnalysisV1ReviewMaxOutputTokens
	// MaxWorkspaceAnalysisCandidateBytes 是单个不可变候选文档的持久化上限。
	MaxWorkspaceAnalysisCandidateBytes int64 = 256 * 1024
	// MaxWorkspaceAnalysisModelResultBytes 是 Planner/Review canonical 结果回执的持久化上限。
	MaxWorkspaceAnalysisModelResultBytes int64 = 64 * 1024
	// ResultTypeWorkspaceAnalysisCandidate 是模型撰写候选片段的稳定结果类型。
	ResultTypeWorkspaceAnalysisCandidate      = "workspace_analysis_candidate"
	workspaceAnalysisCandidateDocumentVersion = "1"
	maxWorkspaceAnalysisCandidateAnswerBytes  = 64 * 1024
	maxWorkspaceAnalysisCandidateSummaryBytes = 4 * 1024
	maxWorkspaceAnalysisCandidateReferences   = 3
)

// WorkspaceAnalysisRunStatus 是工作区分析持久运行的生命周期状态。
type WorkspaceAnalysisRunStatus string

const (
	// WorkspaceAnalysisRunQueued 表示运行与首节点已经持久化但尚未开始执行。
	WorkspaceAnalysisRunQueued WorkspaceAnalysisRunStatus = "queued"
	// WorkspaceAnalysisRunRunning 表示运行允许在有效租约内授权新操作。
	WorkspaceAnalysisRunRunning WorkspaceAnalysisRunStatus = "running"
	// WorkspaceAnalysisRunSucceeded 表示候选、Citation 校验与 Review 已完整闭合。
	WorkspaceAnalysisRunSucceeded WorkspaceAnalysisRunStatus = "succeeded"
	// WorkspaceAnalysisRunRefused 表示系统以稳定且无未验证事实的拒答终结。
	WorkspaceAnalysisRunRefused WorkspaceAnalysisRunStatus = "refused"
	// WorkspaceAnalysisRunClarificationRequired 表示 Planner 要求用户补充信息。
	WorkspaceAnalysisRunClarificationRequired WorkspaceAnalysisRunStatus = "clarification_required"
	// WorkspaceAnalysisRunFailed 表示运行以稳定失败或 Unknown 终结。
	WorkspaceAnalysisRunFailed WorkspaceAnalysisRunStatus = "failed"
	// WorkspaceAnalysisRunCancelled 表示用户取消已在安全检查点生效。
	WorkspaceAnalysisRunCancelled WorkspaceAnalysisRunStatus = "cancelled"
)

// Active 报告运行是否仍可推进或终结。
func (status WorkspaceAnalysisRunStatus) Active() bool {
	return status == WorkspaceAnalysisRunQueued || status == WorkspaceAnalysisRunRunning
}

// Terminal 报告运行是否已经进入不可逆终态。
func (status WorkspaceAnalysisRunStatus) Terminal() bool {
	switch status {
	case WorkspaceAnalysisRunSucceeded, WorkspaceAnalysisRunRefused, WorkspaceAnalysisRunClarificationRequired,
		WorkspaceAnalysisRunFailed, WorkspaceAnalysisRunCancelled:
		return true
	default:
		return false
	}
}

// WorkspaceAnalysisRunTerminationReason 是运行终态使用的稳定原因。
type WorkspaceAnalysisRunTerminationReason string

const (
	// WorkspaceAnalysisRunCompleted 表示成功结果已经通过全部门禁。
	WorkspaceAnalysisRunCompleted WorkspaceAnalysisRunTerminationReason = "COMPLETED"
	// WorkspaceAnalysisRunEvidenceInsufficient 表示没有形成完整 Evidence 链。
	WorkspaceAnalysisRunEvidenceInsufficient WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT"
	// WorkspaceAnalysisRunCitationInvalid 表示候选引用未通过严格校验。
	WorkspaceAnalysisRunCitationInvalid WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_CITATION_INVALID"
	// WorkspaceAnalysisRunFaithfulnessRejected 表示候选未通过 Faithfulness Review。
	WorkspaceAnalysisRunFaithfulnessRejected WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED"
	// WorkspaceAnalysisRunModelRefused 表示受控模型阶段产生了稳定拒答。
	WorkspaceAnalysisRunModelRefused WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_MODEL_REFUSED"
	// WorkspaceAnalysisRunNeedsClarification 表示 Planner 要求用户补充信息。
	WorkspaceAnalysisRunNeedsClarification WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED"
	// WorkspaceAnalysisRunBudgetExhausted 表示持久预算不足以授权下一次调用。
	WorkspaceAnalysisRunBudgetExhausted WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED"
	// WorkspaceAnalysisRunReceiptInvalid 表示 exact receipt 缺失、漂移或绑定不一致。
	WorkspaceAnalysisRunReceiptInvalid WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_RECEIPT_INVALID"
	// WorkspaceAnalysisRunResultUnknown 表示外部调用结果无法安全证明。
	WorkspaceAnalysisRunResultUnknown WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_RESULT_UNKNOWN"
	// WorkspaceAnalysisRunDeadlineExceeded 表示剩余期限不足以完成并持久化调用。
	WorkspaceAnalysisRunDeadlineExceeded WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED"
	// WorkspaceAnalysisRunModelFailed 表示模型调用以稳定失败终结。
	WorkspaceAnalysisRunModelFailed WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_MODEL_FAILED"
	// WorkspaceAnalysisRunToolFailed 表示精确只读 Tool 以稳定失败终结。
	WorkspaceAnalysisRunToolFailed WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_TOOL_FAILED"
	// WorkspaceAnalysisRunRuntimeFailed 表示编排阶段失败，且不存在可证明的模型或 Tool 调用事实。
	WorkspaceAnalysisRunRuntimeFailed WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_RUNTIME_FAILED"
	// WorkspaceAnalysisRunCancellation 表示用户取消已在安全检查点生效。
	WorkspaceAnalysisRunCancellation WorkspaceAnalysisRunTerminationReason = "WORKSPACE_ANALYSIS_CANCELLED"
)

// WorkspaceAnalysisBudgetAmount 保存一个预算切片中的调用、Token 与可选费用计数。
type WorkspaceAnalysisBudgetAmount struct {
	// ModelCalls 是模型调用计数。
	ModelCalls int
	// ToolCalls 是 Tool 调用计数。
	ToolCalls int
	// SourceReads 是 Tool 调用中读取 Source 的计数。
	SourceReads int
	// InputTokens 是模型输入 Token 计数。
	InputTokens int64
	// OutputTokens 是模型输出 Token 计数。
	OutputTokens int64
	// CostMicrounits 为未来可信价格快照预留；v1 必须为空。
	CostMicrounits *int64
}

// WorkspaceAnalysisBudgetLimits 冻结一个运行可消费的全部预算上限。
type WorkspaceAnalysisBudgetLimits struct {
	// Nodes 是静态 DAG 节点上限。
	Nodes int
	// ToolConcurrency 是 Tool 最大并发数。
	ToolConcurrency int
	// Amount 是调用、Token 与可选费用上限。
	Amount WorkspaceAnalysisBudgetAmount
}

// WorkspaceAnalysisRun 是唯一绑定 Question、Answer 与 Workflow Run 的持久预算事实。
type WorkspaceAnalysisRun struct {
	// ID 是运行自身的 canonical UUID。
	ID foundation.ID
	// WorkspaceID 是运行所属 Workspace。
	WorkspaceID foundation.ID
	// ConversationID 是发起运行的 Conversation。
	ConversationID foundation.ID
	// QuestionID 是显式 workspace_analysis Question。
	QuestionID foundation.ID
	// AnswerID 是该 Question 唯一的待发布 Answer。
	AnswerID foundation.ID
	// WorkflowRunID 是 workspace-analysis@1 的持久 Workflow Run。
	WorkflowRunID foundation.ID
	// DefinitionKey 是冻结的 Workflow Definition 键。
	DefinitionKey string
	// DefinitionVersion 是冻结的 Workflow Definition 版本。
	DefinitionVersion int64
	// DefinitionHash 是冻结 Definition 的 canonical SHA-256。
	DefinitionHash string
	// ToolCatalogHash 是冻结 exact Tool 目录的 canonical SHA-256。
	ToolCatalogHash string
	// PolicyVersion 是预算与 deadline 策略版本。
	PolicyVersion int
	// ConfigRevision 是创建运行时已生效的服务端配置版本。
	ConfigRevision int64
	// DeadlineAt 是运行不得越过的数据库时间截止点。
	DeadlineAt time.Time
	// Timeouts 是创建时冻结、用于唯一派生 DeadlineAt 的调用 timeout 快照。
	Timeouts WorkspaceAnalysisV1Timeouts
	// Limits 是该运行冻结的预算上限。
	Limits WorkspaceAnalysisBudgetLimits
	// Reserved 是仍处于 RESERVED 状态的预留总和。
	Reserved WorkspaceAnalysisBudgetAmount
	// Settled 是 SETTLED 与 UNKNOWN_CHARGED 的实际计费总和。
	Settled WorkspaceAnalysisBudgetAmount
	// Status 是持久运行生命周期。
	Status WorkspaceAnalysisRunStatus
	// TerminationReason 是终态唯一允许的稳定原因。
	TerminationReason WorkspaceAnalysisRunTerminationReason
	// ValidationReceiptID 是成功发布要求的 exact ValidateCitation receipt。
	ValidationReceiptID *foundation.ID
	// ReviewModelRunID 是成功发布要求的独立 Faithfulness Review Model Run。
	ReviewModelRunID *foundation.ID
	// Version 是持久 CAS 版本。
	Version int64
	// CreatedAt 是运行创建时间。
	CreatedAt time.Time
	// UpdatedAt 是最近一次预算或生命周期更新时间。
	UpdatedAt time.Time
	// CompletedAt 是不可逆终态时间，必须逐字等于 UpdatedAt；成功不得晚于 DeadlineAt，超期失败可在其后落库。
	CompletedAt *time.Time
}

// ValidateWorkspaceAnalysisRun 校验运行身份、冻结策略、预算和终态矩阵。
func ValidateWorkspaceAnalysisRun(run WorkspaceAnalysisRun) error {
	ids := []foundation.ID{run.ID, run.WorkspaceID, run.ConversationID, run.QuestionID, run.AnswerID, run.WorkflowRunID}
	if run.ValidationReceiptID != nil {
		ids = append(ids, *run.ValidationReceiptID)
	}
	if run.ReviewModelRunID != nil {
		ids = append(ids, *run.ReviewModelRunID)
	}
	if !canonicalUniqueIDs(ids, true) || run.DefinitionKey != workspaceAnalysisDefinitionKey ||
		!validWorkspaceAnalysisVersion(run.DefinitionVersion, run.PolicyVersion) || !canonicalSHA256(run.DefinitionHash) ||
		!canonicalSHA256(run.ToolCatalogHash) ||
		run.ConfigRevision < 0 || run.Version < 1 || run.CreatedAt.IsZero() || run.UpdatedAt.Before(run.CreatedAt) ||
		!run.DeadlineAt.After(run.CreatedAt) {
		return invalid(ErrorCodeWorkspaceAnalysisRunInvalid, "workspace analysis run identity, policy, or lifecycle is invalid")
	}
	duration, err := workspaceAnalysisRunDuration(run)
	if err != nil || !run.DeadlineAt.Equal(run.CreatedAt.Add(duration)) {
		return invalid(ErrorCodeWorkspaceAnalysisRunInvalid, "workspace analysis run timeout snapshot or deadline drifted")
	}
	validLimits := run.PolicyVersion == WorkspaceAnalysisPolicyVersionV1 && validWorkspaceAnalysisLimits(run.Limits) ||
		run.PolicyVersion == WorkspaceAnalysisPolicyVersionV2 && validWorkspaceAnalysisV2Limits(run.Limits)
	if !validLimits || !workspaceAnalysisAmountWithin(run.Reserved, run.Settled, run.Limits.Amount) {
		return inconsistent(ErrorCodeWorkspaceAnalysisRunInvalid, "workspace analysis run budget exceeds its frozen limits")
	}
	if !run.Status.Active() && !run.Status.Terminal() {
		return invalid(ErrorCodeWorkspaceAnalysisRunInvalid, "workspace analysis run status is unsupported")
	}
	if run.Status.Active() {
		if run.TerminationReason != "" || run.CompletedAt != nil {
			return inconsistent(ErrorCodeWorkspaceAnalysisRunInvalid, "active workspace analysis run cannot claim a terminal result")
		}
		if run.Status == WorkspaceAnalysisRunQueued && (!workspaceAnalysisAmountZero(run.Reserved) || !workspaceAnalysisAmountZero(run.Settled)) {
			return inconsistent(ErrorCodeWorkspaceAnalysisRunInvalid, "queued workspace analysis run must have an empty budget")
		}
		return nil
	}
	if !workspaceAnalysisAmountZero(run.Reserved) || run.CompletedAt == nil || run.CompletedAt.IsZero() || !run.CompletedAt.Equal(run.UpdatedAt) ||
		!validWorkspaceAnalysisRunTermination(run.Status, run.TerminationReason) {
		return inconsistent(ErrorCodeWorkspaceAnalysisRunInvalid, "terminal workspace analysis run is inconsistent")
	}
	if run.Status == WorkspaceAnalysisRunSucceeded && run.CompletedAt.After(run.DeadlineAt) {
		return inconsistent(ErrorCodeWorkspaceAnalysisRunInvalid, "successful workspace analysis run completed after its deadline")
	}
	if run.Status == WorkspaceAnalysisRunSucceeded {
		if run.PolicyVersion == WorkspaceAnalysisPolicyVersionV2 {
			if run.ValidationReceiptID == nil || run.ReviewModelRunID == nil ||
				run.Settled.ModelCalls < WorkspaceAnalysisV2MinCompletedModelCalls || run.Settled.ModelCalls > WorkspaceAnalysisV2MaxModelCalls ||
				run.Settled.ToolCalls < WorkspaceAnalysisV2MinCompletedToolCalls || run.Settled.ToolCalls > WorkspaceAnalysisV2MaxToolCalls ||
				run.Settled.SourceReads < 1 || run.Settled.SourceReads > WorkspaceAnalysisV2MaxSourceReads ||
				run.Settled.ModelCalls != run.Settled.ToolCalls+2 {
				return inconsistent(ErrorCodeWorkspaceAnalysisRunInvalid, "successful workspace analysis v2 run proof or budget is incomplete")
			}
			return nil
		}
		if run.ValidationReceiptID == nil || run.ReviewModelRunID == nil ||
			run.Settled.ModelCalls != WorkspaceAnalysisV1MaxModelCalls ||
			run.Settled.ToolCalls < WorkspaceAnalysisV1MinCompletedToolCalls || run.Settled.ToolCalls > WorkspaceAnalysisV1MaxToolCalls ||
			run.Settled.SourceReads < 1 || run.Settled.SourceReads > WorkspaceAnalysisV1MaxSourceReads ||
			run.Settled.ToolCalls != run.Settled.SourceReads+3 {
			return inconsistent(ErrorCodeWorkspaceAnalysisRunInvalid, "successful workspace analysis run proof or budget is incomplete")
		}
	}
	return nil
}

// ValidateWorkspaceAnalysisRunTransition 校验活动运行可原地更新预算、开始或进入一个终态。
func ValidateWorkspaceAnalysisRunTransition(from, to WorkspaceAnalysisRunStatus) error {
	if !from.Active() || (!to.Active() && !to.Terminal()) || from == WorkspaceAnalysisRunRunning && to == WorkspaceAnalysisRunQueued {
		return versionConflict(ErrorCodeWorkspaceAnalysisRunTransitionInvalid, "workspace analysis run status transition is not allowed")
	}
	return nil
}

// WorkspaceAnalysisBudgetReservationStatus 是一次原子调用授权的持久预算状态。
type WorkspaceAnalysisBudgetReservationStatus string

const (
	// WorkspaceAnalysisBudgetReserved 表示调用已获授权但结果尚未安全归约。
	WorkspaceAnalysisBudgetReserved WorkspaceAnalysisBudgetReservationStatus = "RESERVED"
	// WorkspaceAnalysisBudgetSettled 表示调用已按可证明的实际使用量结算。
	WorkspaceAnalysisBudgetSettled WorkspaceAnalysisBudgetReservationStatus = "SETTLED"
	// WorkspaceAnalysisBudgetUnknownCharged 表示结果 Unknown，全部预留上限已计费。
	WorkspaceAnalysisBudgetUnknownCharged WorkspaceAnalysisBudgetReservationStatus = "UNKNOWN_CHARGED"
)

// WorkspaceAnalysisBudgetReservation 是与一个逻辑操作和实际 Call 一对一绑定的预算事实。
type WorkspaceAnalysisBudgetReservation struct {
	// ID 是预算预留自身的 canonical UUID。
	ID foundation.ID
	// WorkspaceID 是预留所属 Workspace。
	WorkspaceID foundation.ID
	// AnalysisRunID 是拥有预留的工作区分析运行。
	AnalysisRunID foundation.ID
	// OperationID 是拥有预留的唯一逻辑操作。
	OperationID foundation.ID
	// CallKind 区分 Model Call 与 Tool Call。
	CallKind WorkspaceAnalysisOperationCallKind
	// ModelCallID 仅在 CallKind=MODEL 时存在。
	ModelCallID *foundation.ID
	// ToolCallID 仅在 CallKind=TOOL 时存在。
	ToolCallID *foundation.ID
	// Status 是预留、实际结算或 Unknown 全额计费状态。
	Status WorkspaceAnalysisBudgetReservationStatus
	// Reserved 是调用前按最坏情况冻结的额度。
	Reserved WorkspaceAnalysisBudgetAmount
	// Settled 是完成后实际消费或 Unknown 全额消费的额度。
	Settled WorkspaceAnalysisBudgetAmount
	// CreatedAt 是原子授权时间。
	CreatedAt time.Time
	// SettledAt 是预留进入终态的时间。
	SettledAt *time.Time
}

// ValidateWorkspaceAnalysisBudgetReservation 校验调用形状、预留上限和结算生命周期。
func ValidateWorkspaceAnalysisBudgetReservation(reservation WorkspaceAnalysisBudgetReservation) error {
	ids := []foundation.ID{reservation.ID, reservation.WorkspaceID, reservation.AnalysisRunID, reservation.OperationID}
	if reservation.ModelCallID != nil {
		ids = append(ids, *reservation.ModelCallID)
	}
	if reservation.ToolCallID != nil {
		ids = append(ids, *reservation.ToolCallID)
	}
	if !canonicalUniqueIDs(ids, true) || reservation.CreatedAt.IsZero() || !validWorkspaceAnalysisReservationCall(reservation) ||
		!validWorkspaceAnalysisReservationShape(reservation.Reserved, reservation.CallKind) ||
		!workspaceAnalysisAmountWithin(workspaceAnalysisZeroAmount(reservation.Reserved.CostMicrounits != nil), reservation.Settled, reservation.Reserved) ||
		!matchingWorkspaceAnalysisCostMode(reservation.Reserved.CostMicrounits, reservation.Settled.CostMicrounits) {
		return invalid(ErrorCodeWorkspaceAnalysisBudgetReservationInvalid, "workspace analysis budget reservation identity or shape is invalid")
	}
	switch reservation.Status {
	case WorkspaceAnalysisBudgetReserved:
		if reservation.SettledAt != nil || !workspaceAnalysisAmountZero(reservation.Settled) {
			return inconsistent(ErrorCodeWorkspaceAnalysisBudgetReservationInvalid, "reserved workspace analysis budget cannot claim settlement")
		}
	case WorkspaceAnalysisBudgetSettled:
		if !validWorkspaceAnalysisSettlementTime(reservation) || !validWorkspaceAnalysisSettledReservationShape(reservation) {
			return inconsistent(ErrorCodeWorkspaceAnalysisBudgetReservationInvalid, "settled workspace analysis budget has an invalid lifecycle")
		}
	case WorkspaceAnalysisBudgetUnknownCharged:
		if !validWorkspaceAnalysisSettlementTime(reservation) || !workspaceAnalysisAmountsEqual(reservation.Reserved, reservation.Settled) {
			return inconsistent(ErrorCodeWorkspaceAnalysisBudgetReservationInvalid, "unknown workspace analysis budget must charge the full reservation")
		}
	default:
		return invalid(ErrorCodeWorkspaceAnalysisBudgetReservationInvalid, "workspace analysis budget reservation status is unsupported")
	}
	return nil
}

// ValidateWorkspaceAnalysisBudgetReservationTransition 校验预算只能从 RESERVED 进入一个终态。
func ValidateWorkspaceAnalysisBudgetReservationTransition(from, to WorkspaceAnalysisBudgetReservationStatus) error {
	if from == WorkspaceAnalysisBudgetReserved && (to == WorkspaceAnalysisBudgetSettled || to == WorkspaceAnalysisBudgetUnknownCharged) {
		return nil
	}
	return versionConflict(ErrorCodeWorkspaceAnalysisBudgetReservationTransitionInvalid, "workspace analysis budget reservation transition is not allowed")
}

// ValidateWorkspaceAnalysisBudgetReservationBinding 校验 Run、预留与逻辑操作的 exact 调用额度一致。
func ValidateWorkspaceAnalysisBudgetReservationBinding(run WorkspaceAnalysisRun, reservation WorkspaceAnalysisBudgetReservation, operation WorkspaceAnalysisOperation) error {
	if err := ValidateWorkspaceAnalysisRun(run); err != nil {
		return err
	}
	if err := ValidateWorkspaceAnalysisBudgetReservation(reservation); err != nil {
		return err
	}
	if err := ValidateWorkspaceAnalysisOperation(operation); err != nil {
		return err
	}
	if operation.Status == WorkspaceAnalysisOperationPending || operation.BudgetReservationID == nil || operation.Call == nil ||
		run.WorkspaceID != reservation.WorkspaceID || run.ID != reservation.AnalysisRunID ||
		run.ID != operation.AnalysisRunID ||
		reservation.AnalysisRunID != operation.AnalysisRunID || reservation.OperationID != operation.ID ||
		reservation.ID != *operation.BudgetReservationID || reservation.CallKind != operation.Call.Kind ||
		!workspaceAnalysisReservationCallMatchesOperation(reservation, *operation.Call) ||
		!workspaceAnalysisReservationStatusMatchesOperation(reservation.Status, operation.Status) ||
		!workspaceAnalysisReservationMatchesOperationPolicy(run, reservation, operation.Kind) {
		return versionConflict(ErrorCodeWorkspaceAnalysisBudgetBindingMismatch, "workspace analysis budget reservation does not match its logical operation")
	}
	return nil
}

// ValidateWorkspaceAnalysisRunBudgetTotals 复核 Run 聚合计数逐项等于全部 reservation 的权威总和。
func ValidateWorkspaceAnalysisRunBudgetTotals(run WorkspaceAnalysisRun, reservations []WorkspaceAnalysisBudgetReservation) error {
	if err := ValidateWorkspaceAnalysisRun(run); err != nil {
		return err
	}
	active := workspaceAnalysisZeroAmount(run.Limits.Amount.CostMicrounits != nil)
	settled := workspaceAnalysisZeroAmount(run.Limits.Amount.CostMicrounits != nil)
	seenReservations := make(map[foundation.ID]struct{}, len(reservations))
	seenOperations := make(map[foundation.ID]struct{}, len(reservations))
	for _, reservation := range reservations {
		if err := ValidateWorkspaceAnalysisBudgetReservation(reservation); err != nil {
			return err
		}
		if reservation.WorkspaceID != run.WorkspaceID || reservation.AnalysisRunID != run.ID {
			return versionConflict(ErrorCodeWorkspaceAnalysisBudgetBindingMismatch, "workspace analysis budget reservation crosses its run scope")
		}
		if _, duplicate := seenReservations[reservation.ID]; duplicate {
			return versionConflict(ErrorCodeWorkspaceAnalysisBudgetBindingMismatch, "workspace analysis budget reservation is duplicated")
		}
		if _, duplicate := seenOperations[reservation.OperationID]; duplicate {
			return versionConflict(ErrorCodeWorkspaceAnalysisBudgetBindingMismatch, "workspace analysis operation has multiple budget reservations")
		}
		seenReservations[reservation.ID] = struct{}{}
		seenOperations[reservation.OperationID] = struct{}{}
		if reservation.Status == WorkspaceAnalysisBudgetReserved {
			if !addWorkspaceAnalysisAmount(&active, reservation.Reserved) {
				return inconsistent(ErrorCodeWorkspaceAnalysisBudgetBindingMismatch, "workspace analysis active budget total overflowed")
			}
		} else if !addWorkspaceAnalysisAmount(&settled, reservation.Settled) {
			return inconsistent(ErrorCodeWorkspaceAnalysisBudgetBindingMismatch, "workspace analysis settled budget total overflowed")
		}
	}
	if !workspaceAnalysisAmountsEqual(active, run.Reserved) || !workspaceAnalysisAmountsEqual(settled, run.Settled) {
		return inconsistent(ErrorCodeWorkspaceAnalysisBudgetBindingMismatch, "workspace analysis run budget totals drifted")
	}
	return nil
}

// WorkspaceAnalysisModelResult 是 Planner 或 Review Model Call 的不可变 canonical 结果回执。
type WorkspaceAnalysisModelResult struct {
	// ID 是模型结果回执自身的 canonical UUID。
	ID foundation.ID
	// WorkspaceID 是模型结果所属 Workspace。
	WorkspaceID foundation.ID
	// AnalysisRunID 是拥有该结果的工作区分析运行。
	AnalysisRunID foundation.ID
	// OperationID 是生成该结果的唯一逻辑操作。
	OperationID foundation.ID
	// NodeAttemptID 是实际完成 Model Call 的首次 Node Attempt。
	NodeAttemptID foundation.ID
	// ModelRunID 是拥有实际 Model Call 的 Model Run。
	ModelRunID foundation.ID
	// ModelCallID 是生成该结果文档的实际 Model Call。
	ModelCallID foundation.ID
	// OperationKind 只允许 RETRIEVAL_PLAN 或 FAITHFULNESS_REVIEW。
	OperationKind WorkspaceAnalysisOperationKind
	// Schema 是 canonical 结果文档的 exact Schema。
	Schema SchemaRef
	// SubjectCandidateID 是 Review 服务端绑定的候选；Planner 必须为空。
	SubjectCandidateID *foundation.ID
	// SubjectCandidateHash 是 Review 服务端绑定的候选 hash；Planner 必须为空。
	SubjectCandidateHash string
	// Document 是不得进入通用日志、HTTP 或时间线的 canonical 模型结果。
	Document json.RawMessage `json:"-"`
	// DocumentHash 是 Document canonical 字节的 SHA-256。
	DocumentHash string
	// DocumentBytes 是 Document canonical 字节数。
	DocumentBytes int64
	// CreatedAt 是成功 Model Call 与 Model Run 完成后保存结果的时间。
	CreatedAt time.Time
}

// String 只返回模型结果身份、Schema、Hash 与长度，不返回文档或候选正文。
func (result WorkspaceAnalysisModelResult) String() string {
	return fmt.Sprintf("WorkspaceAnalysisModelResult{id:%s analysis_run_id:%s operation_id:%s schema:%s@%s hash:%s bytes:%d}",
		result.ID, result.AnalysisRunID, result.OperationID, result.Schema.ID, result.Schema.Version,
		result.DocumentHash, result.DocumentBytes)
}

// GoString 避免 %#v 调试格式绕过模型结果的安全 String 投影。
func (result WorkspaceAnalysisModelResult) GoString() string { return result.String() }

// ValidateWorkspaceAnalysisModelResult 校验 exact Schema、严格文档、服务端候选绑定和 canonical hash。
func ValidateWorkspaceAnalysisModelResult(result WorkspaceAnalysisModelResult) error {
	ids := []foundation.ID{result.ID, result.WorkspaceID, result.AnalysisRunID, result.OperationID,
		result.NodeAttemptID, result.ModelRunID, result.ModelCallID}
	if result.SubjectCandidateID != nil {
		ids = append(ids, *result.SubjectCandidateID)
	}
	if !canonicalUniqueIDs(ids, true) || result.CreatedAt.IsZero() ||
		result.DocumentBytes < 1 || result.DocumentBytes > MaxWorkspaceAnalysisModelResultBytes ||
		result.DocumentBytes != int64(len(result.Document)) ||
		result.DocumentHash != workspaceAnalysisDocumentHash(result.Document) {
		return invalid(ErrorCodeWorkspaceAnalysisModelResultInvalid, "workspace analysis model result identity or document is invalid")
	}
	limits := DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(MaxWorkspaceAnalysisModelResultBytes)
	switch result.OperationKind {
	case WorkspaceAnalysisOperationRetrievalPlan:
		if result.Schema != (SchemaRef{ID: WorkspaceAnalysisPlanSchemaID, Version: OutputSchemaVersionV1}) ||
			result.SubjectCandidateID != nil || result.SubjectCandidateHash != "" {
			return invalid(ErrorCodeWorkspaceAnalysisModelResultInvalid, "workspace analysis plan result contract is invalid")
		}
		decoded, err := DecodeWorkspaceAnalysisPlan(result.Document, limits)
		if err != nil || decoded.ModelRunRef != result.ModelRunID || !workspaceAnalysisCanonicalDocument(result.Document, decoded) {
			return invalid(ErrorCodeWorkspaceAnalysisModelResultInvalid, "workspace analysis plan result document is invalid")
		}
	case WorkspaceAnalysisOperationFaithfulnessReview:
		if result.Schema != (SchemaRef{ID: FaithfulnessReviewSchemaID, Version: OutputSchemaVersionV1}) ||
			result.SubjectCandidateID == nil || !lowerHex(result.SubjectCandidateHash, 64) {
			return invalid(ErrorCodeWorkspaceAnalysisModelResultInvalid, "workspace analysis review result contract is invalid")
		}
		decoded, err := DecodeFaithfulnessReview(result.Document, limits)
		if err != nil || decoded.ModelRunRef != result.ModelRunID || !workspaceAnalysisCanonicalDocument(result.Document, decoded) {
			return invalid(ErrorCodeWorkspaceAnalysisModelResultInvalid, "workspace analysis review result document is invalid")
		}
	default:
		return invalid(ErrorCodeWorkspaceAnalysisModelResultInvalid, "workspace analysis model result kind is invalid")
	}
	return nil
}

// ValidateWorkspaceAnalysisModelResultBinding 校验回执与 Operation、Call、Model Run 及 Review 候选逐项闭合。
func ValidateWorkspaceAnalysisModelResultBinding(
	result WorkspaceAnalysisModelResult,
	run WorkspaceAnalysisRun,
	operation WorkspaceAnalysisOperation,
	modelRun ModelRun,
	modelCall ModelCall,
	candidate *WorkspaceAnalysisCandidate,
) error {
	if err := ValidateWorkspaceAnalysisModelResult(result); err != nil {
		return err
	}
	if err := ValidateWorkspaceAnalysisRun(run); err != nil {
		return err
	}
	if err := ValidateWorkspaceAnalysisOperation(operation); err != nil {
		return err
	}
	if err := ValidateModelRun(modelRun); err != nil {
		return err
	}
	if err := ValidateModelCall(modelCall); err != nil {
		return err
	}
	expectedFinalResult := ResultTypeWorkspaceAnalysisPlan
	if result.OperationKind == WorkspaceAnalysisOperationFaithfulnessReview {
		expectedFinalResult = ResultTypeFaithfulnessReview
	}
	if result.WorkspaceID != run.WorkspaceID || result.AnalysisRunID != run.ID ||
		result.OperationID != operation.ID || operation.AnalysisRunID != run.ID ||
		result.OperationKind != operation.Kind || operation.Call == nil ||
		operation.Call.Kind != WorkspaceAnalysisOperationCallModel || operation.Call.ID != result.ModelCallID ||
		operation.FirstNodeAttemptID == nil || result.NodeAttemptID != *operation.FirstNodeAttemptID ||
		result.ModelRunID != modelRun.ID || modelRun.WorkspaceID != run.WorkspaceID ||
		modelRun.WorkflowRunID != run.WorkflowRunID || modelRun.NodeAttemptID != result.NodeAttemptID ||
		modelRun.Status != ModelRunSucceeded || modelRun.FinalResultType != expectedFinalResult ||
		modelRun.Schema != result.Schema || modelRun.CompletedAt == nil || result.CreatedAt.Before(*modelRun.CompletedAt) ||
		result.ModelCallID != modelCall.ID || modelCall.ModelRunID != modelRun.ID ||
		modelCall.Status != ModelCallSucceeded || modelCall.Schema != result.Schema ||
		modelCall.ResponseHash != result.DocumentHash || modelCall.ResponseBytes != result.DocumentBytes ||
		modelCall.CompletedAt == nil || result.CreatedAt.Before(*modelCall.CompletedAt) {
		return versionConflict(ErrorCodeWorkspaceAnalysisModelResultBindingMismatch, "workspace analysis model result authority binding drifted")
	}
	if result.OperationKind == WorkspaceAnalysisOperationRetrievalPlan {
		if candidate != nil {
			return versionConflict(ErrorCodeWorkspaceAnalysisModelResultBindingMismatch, "workspace analysis plan cannot bind a candidate")
		}
	} else if candidate == nil || ValidateWorkspaceAnalysisCandidate(*candidate) != nil ||
		result.SubjectCandidateID == nil || *result.SubjectCandidateID != candidate.ID ||
		result.SubjectCandidateHash != candidate.DocumentHash || candidate.AnalysisRunID != run.ID ||
		candidate.CreatedAt.After(result.CreatedAt) {
		return versionConflict(ErrorCodeWorkspaceAnalysisModelResultBindingMismatch, "workspace analysis review candidate binding drifted")
	}
	if operation.Status == WorkspaceAnalysisOperationSucceeded {
		if operation.Result == nil || operation.Result.Kind != WorkspaceAnalysisOperationResultModelCall ||
			operation.Result.ID != result.ID || operation.Result.Hash != result.DocumentHash {
			return versionConflict(ErrorCodeWorkspaceAnalysisModelResultBindingMismatch, "workspace analysis model result operation binding drifted")
		}
	} else if operation.Status != WorkspaceAnalysisOperationStarted {
		return versionConflict(ErrorCodeWorkspaceAnalysisModelResultBindingMismatch, "workspace analysis model result operation is not active or successful")
	}
	return nil
}

// WorkspaceAnalysisCandidateProposal 是模型可建议的 Proposal 摘要与候选短引用。
type WorkspaceAnalysisCandidateProposal struct {
	Summary      string   `json:"summary"`
	CitationRefs []string `json:"citation_refs"`
}

// WorkspaceAnalysisCandidatePayload 只包含模型拥有的正文、短引用和可选建议。
type WorkspaceAnalysisCandidatePayload struct {
	AnswerMarkdown     string                              `json:"answer_markdown"`
	CitationRefs       []string                            `json:"citation_refs"`
	ProposalSuggestion *WorkspaceAnalysisCandidateProposal `json:"proposal_suggestion"`
}

// WorkspaceAnalysisCandidateResult 是 Synthesis Model Call 的严格候选文档。
type WorkspaceAnalysisCandidateResult struct {
	ResultType    string                            `json:"result_type"`
	SchemaID      string                            `json:"schema_id"`
	SchemaVersion string                            `json:"schema_version"`
	ModelRunRef   foundation.ID                     `json:"model_run_ref"`
	Payload       WorkspaceAnalysisCandidatePayload `json:"payload"`
}

// Validate 校验候选只包含有界正文、E1..E3 短引用和引用子集建议。
func (result WorkspaceAnalysisCandidateResult) Validate() error {
	if result.ResultType != ResultTypeWorkspaceAnalysisCandidate || result.SchemaID != WorkspaceAnalysisCandidateSchemaID ||
		(result.SchemaVersion != workspaceAnalysisCandidateDocumentVersion && result.SchemaVersion != "2") || !canonicalID(result.ModelRunRef) ||
		!boundedText(result.Payload.AnswerMarkdown, maxWorkspaceAnalysisCandidateAnswerBytes, true) ||
		!validWorkspaceAnalysisCandidateReferencesForVersion(result.Payload.CitationRefs, true, result.SchemaVersion) {
		return invalid(ErrorCodeWorkspaceAnalysisCandidateInvalid, "workspace analysis candidate document is invalid")
	}
	if proposal := result.Payload.ProposalSuggestion; proposal != nil {
		if !boundedText(proposal.Summary, maxWorkspaceAnalysisCandidateSummaryBytes, true) ||
			!validWorkspaceAnalysisCandidateReferencesForVersion(proposal.CitationRefs, true, result.SchemaVersion) ||
			!workspaceAnalysisCandidateReferenceSubset(proposal.CitationRefs, result.Payload.CitationRefs) {
			return invalid(ErrorCodeWorkspaceAnalysisCandidateInvalid, "workspace analysis candidate proposal is invalid")
		}
	}
	return nil
}

type workspaceAnalysisCandidateDocument struct {
	ResultType    *string                            `json:"result_type"`
	SchemaID      *string                            `json:"schema_id"`
	SchemaVersion *string                            `json:"schema_version"`
	ModelRunRef   *foundation.ID                     `json:"model_run_ref"`
	Payload       *workspaceAnalysisCandidatePayload `json:"payload"`
}

type workspaceAnalysisCandidatePayload struct {
	AnswerMarkdown     *string         `json:"answer_markdown"`
	CitationRefs       *[]string       `json:"citation_refs"`
	ProposalSuggestion json.RawMessage `json:"proposal_suggestion"`
}

type workspaceAnalysisCandidateProposal struct {
	Summary      *string   `json:"summary"`
	CitationRefs *[]string `json:"citation_refs"`
}

// DecodeWorkspaceAnalysisCandidate 严格解析候选，拒绝缺失 nullable key、未知字段和重复键。
func DecodeWorkspaceAnalysisCandidate(raw []byte, limits DecodeLimits) (WorkspaceAnalysisCandidateResult, error) {
	document, err := DecodeStrict(raw, limits, func(document workspaceAnalysisCandidateDocument) error {
		if document.ResultType == nil || document.SchemaID == nil || document.SchemaVersion == nil ||
			document.ModelRunRef == nil || document.Payload == nil || document.Payload.AnswerMarkdown == nil ||
			document.Payload.CitationRefs == nil || len(document.Payload.ProposalSuggestion) == 0 {
			return invalid(ErrorCodeWorkspaceAnalysisCandidateInvalid, "workspace analysis candidate document is incomplete")
		}
		return nil
	})
	if err != nil {
		return WorkspaceAnalysisCandidateResult{}, err
	}
	payload, err := decodeWorkspaceAnalysisCandidatePayload(*document.Payload, limits)
	if err != nil {
		return WorkspaceAnalysisCandidateResult{}, err
	}
	result := WorkspaceAnalysisCandidateResult{
		ResultType: *document.ResultType, SchemaID: *document.SchemaID, SchemaVersion: *document.SchemaVersion,
		ModelRunRef: *document.ModelRunRef,
		Payload:     payload,
	}
	if err := result.Validate(); err != nil {
		return WorkspaceAnalysisCandidateResult{}, err
	}
	return result, nil
}

// WorkspaceAnalysisCandidate 是 Synthesis 成功后保存的不可变候选答案事实。
type WorkspaceAnalysisCandidate struct {
	// ID 是候选答案自身的 canonical UUID。
	ID foundation.ID
	// WorkspaceID 是候选答案所属 Workspace。
	WorkspaceID foundation.ID
	// AnalysisRunID 是拥有候选答案的工作区分析运行。
	AnalysisRunID foundation.ID
	// AnswerID 是候选最终可能发布到的 Answer。
	AnswerID foundation.ID
	// SynthesisOperationID 是生成候选的唯一逻辑操作。
	SynthesisOperationID foundation.ID
	// NodeAttemptID 是实际完成 Synthesis Model Call 的 Node Attempt。
	NodeAttemptID foundation.ID
	// SynthesisModelRunID 是撰写候选正文的 Model Run。
	SynthesisModelRunID foundation.ID
	// SchemaID 是候选文档的 exact Schema ID。
	SchemaID string
	// SchemaVersion 是候选文档的 exact 数字版本。
	SchemaVersion int64
	// Document 是不得进入通用日志、HTTP 或时间线的 canonical 候选文档。
	Document json.RawMessage `json:"-"`
	// DocumentHash 是 Document canonical 字节的 SHA-256。
	DocumentHash string
	// DocumentBytes 是 Document canonical 字节数。
	DocumentBytes int64
	// CreatedAt 是成功 Model Call 完成后保存候选的时间。
	CreatedAt time.Time
}

// String 只返回候选身份、Schema、Hash 与长度，不返回候选正文。
func (candidate WorkspaceAnalysisCandidate) String() string {
	return fmt.Sprintf("WorkspaceAnalysisCandidate{id:%s analysis_run_id:%s answer_id:%s schema:%s@%d hash:%s bytes:%d}",
		candidate.ID, candidate.AnalysisRunID, candidate.AnswerID, candidate.SchemaID, candidate.SchemaVersion,
		candidate.DocumentHash, candidate.DocumentBytes)
}

// GoString 避免 %#v 调试格式绕过候选答案的安全 String 投影。
func (candidate WorkspaceAnalysisCandidate) GoString() string { return candidate.String() }

// ValidateWorkspaceAnalysisCandidate 校验候选身份、exact Schema、文档边界和 canonical hash。
func ValidateWorkspaceAnalysisCandidate(candidate WorkspaceAnalysisCandidate) error {
	ids := []foundation.ID{candidate.ID, candidate.WorkspaceID, candidate.AnalysisRunID, candidate.AnswerID,
		candidate.SynthesisOperationID, candidate.NodeAttemptID, candidate.SynthesisModelRunID}
	limits := DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(MaxWorkspaceAnalysisCandidateBytes)
	limits.MaxStringBytes = maxWorkspaceAnalysisCandidateAnswerBytes
	decoded, decodeErr := DecodeWorkspaceAnalysisCandidate(candidate.Document, limits)
	if !canonicalUniqueIDs(ids, true) || candidate.SchemaID != WorkspaceAnalysisCandidateSchemaID ||
		(candidate.SchemaVersion != WorkspaceAnalysisCandidateSchemaVersion && candidate.SchemaVersion != WorkspaceAnalysisCandidateSchemaVersionV2) || candidate.CreatedAt.IsZero() ||
		candidate.DocumentBytes < 1 || candidate.DocumentBytes > MaxWorkspaceAnalysisCandidateBytes ||
		candidate.DocumentBytes != int64(len(candidate.Document)) || decodeErr != nil ||
		decoded.ModelRunRef != candidate.SynthesisModelRunID || decoded.SchemaVersion != fmt.Sprint(candidate.SchemaVersion) || !workspaceAnalysisCanonicalDocument(candidate.Document, decoded) ||
		candidate.DocumentHash != workspaceAnalysisDocumentHash(candidate.Document) {
		return invalid(ErrorCodeWorkspaceAnalysisCandidateInvalid, "workspace analysis candidate identity or document is invalid")
	}
	return nil
}

func validWorkspaceAnalysisCandidateReferences(references []string, required bool) bool {
	if references == nil || len(references) > maxWorkspaceAnalysisCandidateReferences || (required && len(references) == 0) {
		return false
	}
	seen := make(map[string]struct{}, len(references))
	for _, reference := range references {
		if len(reference) != 2 || reference[0] != 'E' || reference[1] < '1' || reference[1] > '3' {
			return false
		}
		if _, duplicate := seen[reference]; duplicate {
			return false
		}
		seen[reference] = struct{}{}
	}
	return true
}

func workspaceAnalysisCandidateReferenceSubset(subset, superset []string) bool {
	allowed := make(map[string]struct{}, len(superset))
	for _, reference := range superset {
		allowed[reference] = struct{}{}
	}
	for _, reference := range subset {
		if _, exists := allowed[reference]; !exists {
			return false
		}
	}
	return true
}

// ValidateWorkspaceAnalysisCandidateBinding 校验候选与 Synthesis 操作、运行和 Model Run 逐项一致。
func ValidateWorkspaceAnalysisCandidateBinding(candidate WorkspaceAnalysisCandidate, run WorkspaceAnalysisRun, operation WorkspaceAnalysisOperation, modelRun ModelRun) error {
	if err := ValidateWorkspaceAnalysisCandidate(candidate); err != nil {
		return err
	}
	if err := ValidateWorkspaceAnalysisRun(run); err != nil {
		return err
	}
	if err := ValidateWorkspaceAnalysisOperation(operation); err != nil {
		return err
	}
	if err := ValidateModelRun(modelRun); err != nil {
		return err
	}
	if candidate.SchemaVersion != run.DefinitionVersion || candidate.WorkspaceID != run.WorkspaceID || candidate.AnalysisRunID != run.ID || candidate.AnswerID != run.AnswerID ||
		candidate.SynthesisOperationID != operation.ID || operation.AnalysisRunID != run.ID ||
		operation.Kind != WorkspaceAnalysisOperationAnswerSynthesis || operation.Call == nil ||
		operation.Call.Kind != WorkspaceAnalysisOperationCallModel || operation.FirstNodeAttemptID == nil ||
		candidate.NodeAttemptID != *operation.FirstNodeAttemptID ||
		candidate.SynthesisModelRunID != modelRun.ID || modelRun.WorkspaceID != candidate.WorkspaceID ||
		modelRun.WorkflowRunID != run.WorkflowRunID || modelRun.NodeAttemptID != candidate.NodeAttemptID ||
		modelRun.Status != ModelRunSucceeded || modelRun.FinalResultType != ResultTypeWorkspaceAnalysisAnswer ||
		modelRun.Schema.ID != candidate.SchemaID || modelRun.Schema.Version != fmt.Sprint(candidate.SchemaVersion) ||
		modelRun.CompletedAt == nil || candidate.CreatedAt.Before(*modelRun.CompletedAt) {
		return versionConflict(ErrorCodeWorkspaceAnalysisCandidateBindingMismatch, "workspace analysis candidate authority binding drifted")
	}
	if operation.Status == WorkspaceAnalysisOperationSucceeded {
		if operation.Result == nil || operation.Result.Kind != WorkspaceAnalysisOperationResultCandidate ||
			operation.Result.ID != candidate.ID || operation.Result.Hash != candidate.DocumentHash {
			return versionConflict(ErrorCodeWorkspaceAnalysisCandidateBindingMismatch, "workspace analysis candidate result binding drifted")
		}
	} else if operation.Status != WorkspaceAnalysisOperationStarted {
		return versionConflict(ErrorCodeWorkspaceAnalysisCandidateBindingMismatch, "workspace analysis candidate operation is not active or successful")
	}
	return nil
}

func validWorkspaceAnalysisLimits(limits WorkspaceAnalysisBudgetLimits) bool {
	return limits.Nodes == WorkspaceAnalysisV1MaxNodes && limits.ToolConcurrency == WorkspaceAnalysisV1MaxToolConcurrency &&
		limits.Amount.ModelCalls == WorkspaceAnalysisV1MaxModelCalls && limits.Amount.ToolCalls == WorkspaceAnalysisV1MaxToolCalls &&
		limits.Amount.SourceReads == WorkspaceAnalysisV1MaxSourceReads && limits.Amount.InputTokens == WorkspaceAnalysisV1MaxRunInputTokens &&
		limits.Amount.OutputTokens >= workspaceAnalysisMinimumOutputTokens && limits.Amount.OutputTokens <= WorkspaceAnalysisV1MaxRunOutputTokens &&
		limits.Amount.CostMicrounits == nil
}

func validWorkspaceAnalysisRunTermination(status WorkspaceAnalysisRunStatus, reason WorkspaceAnalysisRunTerminationReason) bool {
	switch status {
	case WorkspaceAnalysisRunSucceeded:
		return reason == WorkspaceAnalysisRunCompleted
	case WorkspaceAnalysisRunRefused:
		return reason == WorkspaceAnalysisRunEvidenceInsufficient || reason == WorkspaceAnalysisRunCitationInvalid ||
			reason == WorkspaceAnalysisRunFaithfulnessRejected || reason == WorkspaceAnalysisRunModelRefused
	case WorkspaceAnalysisRunClarificationRequired:
		return reason == WorkspaceAnalysisRunNeedsClarification
	case WorkspaceAnalysisRunFailed:
		return reason == WorkspaceAnalysisRunBudgetExhausted || reason == WorkspaceAnalysisRunReceiptInvalid ||
			reason == WorkspaceAnalysisRunResultUnknown || reason == WorkspaceAnalysisRunDeadlineExceeded ||
			reason == WorkspaceAnalysisRunModelFailed || reason == WorkspaceAnalysisRunToolFailed ||
			reason == WorkspaceAnalysisRunRuntimeFailed
	case WorkspaceAnalysisRunCancelled:
		return reason == WorkspaceAnalysisRunCancellation
	default:
		return false
	}
}

func validWorkspaceAnalysisReservationCall(reservation WorkspaceAnalysisBudgetReservation) bool {
	return reservation.CallKind == WorkspaceAnalysisOperationCallModel && reservation.ModelCallID != nil && reservation.ToolCallID == nil ||
		reservation.CallKind == WorkspaceAnalysisOperationCallTool && reservation.ToolCallID != nil && reservation.ModelCallID == nil
}

func validWorkspaceAnalysisReservationShape(amount WorkspaceAnalysisBudgetAmount, kind WorkspaceAnalysisOperationCallKind) bool {
	if amount.CostMicrounits != nil {
		return false
	}
	switch kind {
	case WorkspaceAnalysisOperationCallModel:
		return amount.ModelCalls == 1 && amount.ToolCalls == 0 && amount.SourceReads == 0 &&
			amount.InputTokens == WorkspaceAnalysisV1MaxInputTokensPerModelCall &&
			amount.OutputTokens >= 1 && amount.OutputTokens <= WorkspaceAnalysisV1SynthesisMaxOutputTokens
	case WorkspaceAnalysisOperationCallTool:
		return amount.ModelCalls == 0 && amount.ToolCalls == 1 && (amount.SourceReads == 0 || amount.SourceReads == 1) &&
			amount.InputTokens == 0 && amount.OutputTokens == 0
	default:
		return false
	}
}

func validWorkspaceAnalysisSettlementTime(reservation WorkspaceAnalysisBudgetReservation) bool {
	return reservation.SettledAt != nil && !reservation.SettledAt.IsZero() && !reservation.SettledAt.Before(reservation.CreatedAt)
}

func validWorkspaceAnalysisSettledReservationShape(reservation WorkspaceAnalysisBudgetReservation) bool {
	switch reservation.CallKind {
	case WorkspaceAnalysisOperationCallModel:
		return reservation.Settled.ModelCalls == 1 && reservation.Settled.ToolCalls == 0 && reservation.Settled.SourceReads == 0
	case WorkspaceAnalysisOperationCallTool:
		return reservation.Settled.ModelCalls == 0 && reservation.Settled.ToolCalls == 1 &&
			reservation.Settled.SourceReads == reservation.Reserved.SourceReads && reservation.Settled.InputTokens == 0 &&
			reservation.Settled.OutputTokens == 0
	default:
		return false
	}
}

func workspaceAnalysisReservationCallMatchesOperation(reservation WorkspaceAnalysisBudgetReservation, call WorkspaceAnalysisOperationCallRef) bool {
	if call.Kind == WorkspaceAnalysisOperationCallModel {
		return reservation.ModelCallID != nil && *reservation.ModelCallID == call.ID
	}
	return call.Kind == WorkspaceAnalysisOperationCallTool && reservation.ToolCallID != nil && *reservation.ToolCallID == call.ID
}

func workspaceAnalysisReservationStatusMatchesOperation(reservation WorkspaceAnalysisBudgetReservationStatus, operation WorkspaceAnalysisOperationStatus) bool {
	switch operation {
	case WorkspaceAnalysisOperationStarted:
		return reservation == WorkspaceAnalysisBudgetReserved
	case WorkspaceAnalysisOperationSucceeded, WorkspaceAnalysisOperationFailed:
		return reservation == WorkspaceAnalysisBudgetSettled
	case WorkspaceAnalysisOperationUnknown:
		return reservation == WorkspaceAnalysisBudgetUnknownCharged
	default:
		return false
	}
}

func workspaceAnalysisReservationMatchesOperationPolicy(run WorkspaceAnalysisRun, reservation WorkspaceAnalysisBudgetReservation, kind WorkspaceAnalysisOperationKind) bool {
	if run.PolicyVersion == WorkspaceAnalysisPolicyVersionV2 {
		return workspaceAnalysisV2ReservationMatchesOperationPolicy(run, reservation, kind)
	}
	if reservation.CallKind == WorkspaceAnalysisOperationCallTool {
		return (reservation.Reserved.SourceReads == 1) == (kind == WorkspaceAnalysisOperationSourceRead)
	}
	if reservation.Reserved.InputTokens != WorkspaceAnalysisV1MaxInputTokensPerModelCall {
		return false
	}
	switch kind {
	case WorkspaceAnalysisOperationRetrievalPlan:
		return reservation.Reserved.OutputTokens == WorkspaceAnalysisV1PlanMaxOutputTokens
	case WorkspaceAnalysisOperationAnswerSynthesis:
		return reservation.Reserved.OutputTokens == run.Limits.Amount.OutputTokens-WorkspaceAnalysisV1PlanMaxOutputTokens-WorkspaceAnalysisV1ReviewMaxOutputTokens
	case WorkspaceAnalysisOperationFaithfulnessReview:
		return reservation.Reserved.OutputTokens == WorkspaceAnalysisV1ReviewMaxOutputTokens
	default:
		return false
	}
}

func workspaceAnalysisAmountWithin(reserved, settled, maximum WorkspaceAnalysisBudgetAmount) bool {
	return nonNegativeAndWithin(int64(reserved.ModelCalls), int64(settled.ModelCalls), int64(maximum.ModelCalls)) &&
		nonNegativeAndWithin(int64(reserved.ToolCalls), int64(settled.ToolCalls), int64(maximum.ToolCalls)) &&
		nonNegativeAndWithin(int64(reserved.SourceReads), int64(settled.SourceReads), int64(maximum.SourceReads)) &&
		nonNegativeAndWithin(reserved.InputTokens, settled.InputTokens, maximum.InputTokens) &&
		nonNegativeAndWithin(reserved.OutputTokens, settled.OutputTokens, maximum.OutputTokens) &&
		workspaceAnalysisCostWithin(reserved.CostMicrounits, settled.CostMicrounits, maximum.CostMicrounits)
}

func nonNegativeAndWithin(reserved, settled, maximum int64) bool {
	return reserved >= 0 && settled >= 0 && maximum >= 0 && settled <= maximum && reserved <= maximum-settled
}

func workspaceAnalysisCostWithin(reserved, settled, maximum *int64) bool {
	if maximum == nil {
		return reserved == nil && settled == nil
	}
	return reserved != nil && settled != nil && nonNegativeAndWithin(*reserved, *settled, *maximum)
}

func matchingWorkspaceAnalysisCostMode(reserved, settled *int64) bool {
	return reserved == nil && settled == nil || reserved != nil && settled != nil
}

func workspaceAnalysisAmountZero(amount WorkspaceAnalysisBudgetAmount) bool {
	return amount.ModelCalls == 0 && amount.ToolCalls == 0 && amount.SourceReads == 0 && amount.InputTokens == 0 &&
		amount.OutputTokens == 0 && (amount.CostMicrounits == nil || *amount.CostMicrounits == 0)
}

func workspaceAnalysisAmountsEqual(left, right WorkspaceAnalysisBudgetAmount) bool {
	if left.ModelCalls != right.ModelCalls || left.ToolCalls != right.ToolCalls || left.SourceReads != right.SourceReads ||
		left.InputTokens != right.InputTokens || left.OutputTokens != right.OutputTokens ||
		(left.CostMicrounits == nil) != (right.CostMicrounits == nil) {
		return false
	}
	return left.CostMicrounits == nil || *left.CostMicrounits == *right.CostMicrounits
}

func workspaceAnalysisZeroAmount(withCost bool) WorkspaceAnalysisBudgetAmount {
	if !withCost {
		return WorkspaceAnalysisBudgetAmount{}
	}
	zero := int64(0)
	return WorkspaceAnalysisBudgetAmount{CostMicrounits: &zero}
}

func addWorkspaceAnalysisAmount(target *WorkspaceAnalysisBudgetAmount, value WorkspaceAnalysisBudgetAmount) bool {
	if target == nil || target.ModelCalls > int(^uint(0)>>1)-value.ModelCalls || target.ToolCalls > int(^uint(0)>>1)-value.ToolCalls ||
		target.SourceReads > int(^uint(0)>>1)-value.SourceReads || target.InputTokens > int64(^uint64(0)>>1)-value.InputTokens ||
		target.OutputTokens > int64(^uint64(0)>>1)-value.OutputTokens ||
		(target.CostMicrounits == nil) != (value.CostMicrounits == nil) {
		return false
	}
	target.ModelCalls += value.ModelCalls
	target.ToolCalls += value.ToolCalls
	target.SourceReads += value.SourceReads
	target.InputTokens += value.InputTokens
	target.OutputTokens += value.OutputTokens
	if target.CostMicrounits != nil {
		if *target.CostMicrounits > int64(^uint64(0)>>1)-*value.CostMicrounits {
			return false
		}
		*target.CostMicrounits += *value.CostMicrounits
	}
	return true
}

func workspaceAnalysisDocumentHash(document []byte) string {
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}

func workspaceAnalysisCanonicalDocument[T any](document []byte, decoded T) bool {
	canonical, err := json.Marshal(decoded)
	return err == nil && bytes.Equal(document, canonical)
}
