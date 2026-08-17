package domain

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeWorkspaceAnalysisOperationInvalid 表示逻辑操作身份或生命周期不完整。
	ErrorCodeWorkspaceAnalysisOperationInvalid = "AGENT_WORKSPACE_ANALYSIS_OPERATION_INVALID"
	// ErrorCodeWorkspaceAnalysisOperationTransitionInvalid 表示逻辑操作状态发生非法迁移。
	ErrorCodeWorkspaceAnalysisOperationTransitionInvalid = "AGENT_WORKSPACE_ANALYSIS_OPERATION_TRANSITION_INVALID"
	// ErrorCodeWorkspaceAnalysisOperationRequestBindingMismatch 表示操作未绑定实际 Call 的 canonical 请求哈希。
	ErrorCodeWorkspaceAnalysisOperationRequestBindingMismatch = "AGENT_WORKSPACE_ANALYSIS_OPERATION_REQUEST_BINDING_MISMATCH"
	// ErrorCodeWorkspaceAnalysisOperationResultBindingMismatch 表示操作未绑定实际持久结果的 typed identity/hash。
	ErrorCodeWorkspaceAnalysisOperationResultBindingMismatch = "AGENT_WORKSPACE_ANALYSIS_OPERATION_RESULT_BINDING_MISMATCH"
)

// WorkspaceAnalysisOperationNodeKey 是逻辑操作所属的静态 Workflow 节点键。
type WorkspaceAnalysisOperationNodeKey string

const (
	// WorkspaceAnalysisOperationNodeInspectWorkspace 是 Git 聚合节点。
	WorkspaceAnalysisOperationNodeInspectWorkspace WorkspaceAnalysisOperationNodeKey = "inspect_workspace"
	// WorkspaceAnalysisOperationNodeRetrieveEvidence 是规划并检索证据节点。
	WorkspaceAnalysisOperationNodeRetrieveEvidence WorkspaceAnalysisOperationNodeKey = "retrieve_evidence"
	// WorkspaceAnalysisOperationNodeReadEvidence 是顺序读取 Source 节点。
	WorkspaceAnalysisOperationNodeReadEvidence WorkspaceAnalysisOperationNodeKey = "read_evidence"
	// WorkspaceAnalysisOperationNodeSynthesizeAnswer 是生成候选答案节点。
	WorkspaceAnalysisOperationNodeSynthesizeAnswer WorkspaceAnalysisOperationNodeKey = "synthesize_answer"
	// WorkspaceAnalysisOperationNodeValidateCitations 是校验 Citation 节点。
	WorkspaceAnalysisOperationNodeValidateCitations WorkspaceAnalysisOperationNodeKey = "validate_citations"
	// WorkspaceAnalysisOperationNodeReviewPublish 是 Faithfulness Review 与发布节点。
	WorkspaceAnalysisOperationNodeReviewPublish WorkspaceAnalysisOperationNodeKey = "review_publish"
)

// WorkspaceAnalysisOperationKind 是跨 Attempt 保持稳定的语义调用种类。
type WorkspaceAnalysisOperationKind string

const (
	// WorkspaceAnalysisOperationGitStatus 表示一次 Git 状态聚合 Tool 调用。
	WorkspaceAnalysisOperationGitStatus WorkspaceAnalysisOperationKind = "GIT_STATUS"
	// WorkspaceAnalysisOperationRetrievalPlan 表示一次受限检索规划模型调用。
	WorkspaceAnalysisOperationRetrievalPlan WorkspaceAnalysisOperationKind = "RETRIEVAL_PLAN"
	// WorkspaceAnalysisOperationKnowledgeSearch 表示一次知识检索 Tool 调用。
	WorkspaceAnalysisOperationKnowledgeSearch WorkspaceAnalysisOperationKind = "KNOWLEDGE_SEARCH"
	// WorkspaceAnalysisOperationSourceRead 表示一次按序 Source 读取 Tool 调用。
	WorkspaceAnalysisOperationSourceRead WorkspaceAnalysisOperationKind = "SOURCE_READ"
	// WorkspaceAnalysisOperationAnswerSynthesis 表示一次候选答案模型调用。
	WorkspaceAnalysisOperationAnswerSynthesis WorkspaceAnalysisOperationKind = "ANSWER_SYNTHESIS"
	// WorkspaceAnalysisOperationCitationValidation 表示一次 Citation 校验 Tool 调用。
	WorkspaceAnalysisOperationCitationValidation WorkspaceAnalysisOperationKind = "CITATION_VALIDATION"
	// WorkspaceAnalysisOperationFaithfulnessReview 表示一次发布前 Faithfulness Review 模型调用。
	WorkspaceAnalysisOperationFaithfulnessReview WorkspaceAnalysisOperationKind = "FAITHFULNESS_REVIEW"
)

// WorkspaceAnalysisOperationCallKind 区分实际授权的是 Model Call 还是 Tool Call。
type WorkspaceAnalysisOperationCallKind string

const (
	// WorkspaceAnalysisOperationCallModel 表示项目持久化的 Model Call。
	WorkspaceAnalysisOperationCallModel WorkspaceAnalysisOperationCallKind = "MODEL"
	// WorkspaceAnalysisOperationCallTool 表示项目持久化的 Tool Call。
	WorkspaceAnalysisOperationCallTool WorkspaceAnalysisOperationCallKind = "TOOL"
)

// WorkspaceAnalysisOperationResultKind 区分成功操作允许引用的持久结果事实。
type WorkspaceAnalysisOperationResultKind string

const (
	// WorkspaceAnalysisOperationResultModelCall 表示结果是已终结 Model Call 对应的不可变 canonical receipt。
	WorkspaceAnalysisOperationResultModelCall WorkspaceAnalysisOperationResultKind = "MODEL_RESULT_RECEIPT"
	// WorkspaceAnalysisOperationResultToolReceipt 表示结果是不可变 canonical Tool receipt。
	WorkspaceAnalysisOperationResultToolReceipt WorkspaceAnalysisOperationResultKind = "TOOL_RESULT_RECEIPT"
	// WorkspaceAnalysisOperationResultCandidate 表示结果是不可变 Synthesis Candidate。
	WorkspaceAnalysisOperationResultCandidate WorkspaceAnalysisOperationResultKind = "SYNTHESIS_CANDIDATE"
)

// WorkspaceAnalysisOperationStatus 是窄逻辑调用检查点状态，不拥有 Workflow 节点生命周期。
type WorkspaceAnalysisOperationStatus string

const (
	// WorkspaceAnalysisOperationPending 表示尚未持久授权实际调用。
	WorkspaceAnalysisOperationPending WorkspaceAnalysisOperationStatus = "PENDING"
	// WorkspaceAnalysisOperationStarted 表示 reservation 与实际 Call 已原子建立。
	WorkspaceAnalysisOperationStarted WorkspaceAnalysisOperationStatus = "STARTED"
	// WorkspaceAnalysisOperationSucceeded 表示 Call 与受控结果已经原子终结。
	WorkspaceAnalysisOperationSucceeded WorkspaceAnalysisOperationStatus = "SUCCEEDED"
	// WorkspaceAnalysisOperationFailed 表示实际 Call 以稳定失败终结。
	WorkspaceAnalysisOperationFailed WorkspaceAnalysisOperationStatus = "FAILED"
	// WorkspaceAnalysisOperationUnknown 表示无法证明实际 Call 或结果，禁止自动重试。
	WorkspaceAnalysisOperationUnknown WorkspaceAnalysisOperationStatus = "UNKNOWN"
)

// WorkspaceAnalysisOperationReplayDisposition 是 replacement Attempt 对同一逻辑操作唯一允许的动作。
type WorkspaceAnalysisOperationReplayDisposition string

const (
	// WorkspaceAnalysisOperationReplayAuthorize 表示 PENDING 操作可进入原子授权流程。
	WorkspaceAnalysisOperationReplayAuthorize WorkspaceAnalysisOperationReplayDisposition = "AUTHORIZE"
	// WorkspaceAnalysisOperationReplayReconcile 表示 STARTED 操作必须先归约 exact Call，禁止重新调用。
	WorkspaceAnalysisOperationReplayReconcile WorkspaceAnalysisOperationReplayDisposition = "RECONCILE_EXACT_CALL"
	// WorkspaceAnalysisOperationReplayReuseResult 表示 SUCCEEDED 操作只能复用已绑定的 typed result。
	WorkspaceAnalysisOperationReplayReuseResult WorkspaceAnalysisOperationReplayDisposition = "REUSE_RESULT"
	// WorkspaceAnalysisOperationReplayFailure 表示 FAILED 操作只能重放已绑定的稳定失败。
	WorkspaceAnalysisOperationReplayFailure WorkspaceAnalysisOperationReplayDisposition = "REPLAY_FAILURE"
	// WorkspaceAnalysisOperationReplayTerminateUnknown 表示 UNKNOWN 操作必须终止且不得重新调用。
	WorkspaceAnalysisOperationReplayTerminateUnknown WorkspaceAnalysisOperationReplayDisposition = "TERMINATE_UNKNOWN"
)

// WorkspaceAnalysisOperationContract 冻结一个静态节点内唯一允许的逻辑调用槽位。
type WorkspaceAnalysisOperationContract struct {
	// NodeKey 是与 workspace-analysis@1 节点及公开 phase 相同的稳定键。
	NodeKey WorkspaceAnalysisOperationNodeKey `json:"node_key"`
	// Kind 是该槽位唯一允许的语义操作种类。
	Kind WorkspaceAnalysisOperationKind `json:"operation_kind"`
	// Ordinal 是同节点同种类内从 1 开始的确定性序号。
	Ordinal int `json:"ordinal"`
	// CallKind 是该槽位必须绑定的实际 Call 类型。
	CallKind WorkspaceAnalysisOperationCallKind `json:"call_kind"`
	// ResultKind 是成功时唯一允许的受控结果引用类型。
	ResultKind WorkspaceAnalysisOperationResultKind `json:"result_kind"`
}

var workspaceAnalysisV1OperationContracts = [...]WorkspaceAnalysisOperationContract{
	{NodeKey: WorkspaceAnalysisOperationNodeInspectWorkspace, Kind: WorkspaceAnalysisOperationGitStatus, Ordinal: 1, CallKind: WorkspaceAnalysisOperationCallTool, ResultKind: WorkspaceAnalysisOperationResultToolReceipt},
	{NodeKey: WorkspaceAnalysisOperationNodeRetrieveEvidence, Kind: WorkspaceAnalysisOperationRetrievalPlan, Ordinal: 1, CallKind: WorkspaceAnalysisOperationCallModel, ResultKind: WorkspaceAnalysisOperationResultModelCall},
	{NodeKey: WorkspaceAnalysisOperationNodeRetrieveEvidence, Kind: WorkspaceAnalysisOperationKnowledgeSearch, Ordinal: 1, CallKind: WorkspaceAnalysisOperationCallTool, ResultKind: WorkspaceAnalysisOperationResultToolReceipt},
	{NodeKey: WorkspaceAnalysisOperationNodeReadEvidence, Kind: WorkspaceAnalysisOperationSourceRead, Ordinal: 1, CallKind: WorkspaceAnalysisOperationCallTool, ResultKind: WorkspaceAnalysisOperationResultToolReceipt},
	{NodeKey: WorkspaceAnalysisOperationNodeReadEvidence, Kind: WorkspaceAnalysisOperationSourceRead, Ordinal: 2, CallKind: WorkspaceAnalysisOperationCallTool, ResultKind: WorkspaceAnalysisOperationResultToolReceipt},
	{NodeKey: WorkspaceAnalysisOperationNodeReadEvidence, Kind: WorkspaceAnalysisOperationSourceRead, Ordinal: 3, CallKind: WorkspaceAnalysisOperationCallTool, ResultKind: WorkspaceAnalysisOperationResultToolReceipt},
	{NodeKey: WorkspaceAnalysisOperationNodeSynthesizeAnswer, Kind: WorkspaceAnalysisOperationAnswerSynthesis, Ordinal: 1, CallKind: WorkspaceAnalysisOperationCallModel, ResultKind: WorkspaceAnalysisOperationResultCandidate},
	{NodeKey: WorkspaceAnalysisOperationNodeValidateCitations, Kind: WorkspaceAnalysisOperationCitationValidation, Ordinal: 1, CallKind: WorkspaceAnalysisOperationCallTool, ResultKind: WorkspaceAnalysisOperationResultToolReceipt},
	{NodeKey: WorkspaceAnalysisOperationNodeReviewPublish, Kind: WorkspaceAnalysisOperationFaithfulnessReview, Ordinal: 1, CallKind: WorkspaceAnalysisOperationCallModel, ResultKind: WorkspaceAnalysisOperationResultModelCall},
}

// WorkspaceAnalysisV1OperationContracts 返回 workspace-analysis@1 的确定性操作目录副本。
func WorkspaceAnalysisV1OperationContracts() []WorkspaceAnalysisOperationContract {
	contracts := make([]WorkspaceAnalysisOperationContract, len(workspaceAnalysisV1OperationContracts))
	copy(contracts, workspaceAnalysisV1OperationContracts[:])
	return contracts
}

// WorkspaceAnalysisOperationKey 是不含 Node Attempt 的跨 Attempt 稳定逻辑键。
type WorkspaceAnalysisOperationKey struct {
	// AnalysisRunID 是拥有该逻辑调用的 Workspace Analysis Run。
	AnalysisRunID foundation.ID `json:"analysis_run_id"`
	// NodeKey 是静态 Workflow 节点键。
	NodeKey WorkspaceAnalysisOperationNodeKey `json:"node_key"`
	// Kind 是语义操作种类。
	Kind WorkspaceAnalysisOperationKind `json:"operation_kind"`
	// Ordinal 是同节点同种类内的确定性序号。
	Ordinal int `json:"ordinal"`
}

// Validate 校验逻辑键只引用 workspace-analysis@1 冻结的操作槽位。
func (key WorkspaceAnalysisOperationKey) Validate() error {
	if !canonicalID(key.AnalysisRunID) {
		return invalid(ErrorCodeWorkspaceAnalysisOperationInvalid, "workspace analysis operation run id is invalid")
	}
	if _, exists := workspaceAnalysisOperationContract(key.NodeKey, key.Kind, key.Ordinal); !exists {
		return invalid(ErrorCodeWorkspaceAnalysisOperationInvalid, "workspace analysis operation key is not in the v1 contract")
	}
	return nil
}

// WorkspaceAnalysisOperationContractForKey 返回逻辑键对应的唯一 v1 操作合同。
func WorkspaceAnalysisOperationContractForKey(key WorkspaceAnalysisOperationKey) (WorkspaceAnalysisOperationContract, error) {
	if err := key.Validate(); err != nil {
		return WorkspaceAnalysisOperationContract{}, err
	}
	contract, _ := workspaceAnalysisOperationContract(key.NodeKey, key.Kind, key.Ordinal)
	return contract, nil
}

// WorkspaceAnalysisOperationCallRef 只引用项目持久化的实际 Model/Tool Call，不携带输出。
type WorkspaceAnalysisOperationCallRef struct {
	// Kind 区分 Model Call 与 Tool Call。
	Kind WorkspaceAnalysisOperationCallKind
	// ID 是实际 Call 的 canonical UUID。
	ID foundation.ID
}

// WorkspaceAnalysisOperationResultRef 是成功操作唯一允许持久化的 typed 结果引用。
type WorkspaceAnalysisOperationResultRef struct {
	// Kind 区分 Model result receipt、Tool receipt 与 Synthesis Candidate。
	Kind WorkspaceAnalysisOperationResultKind
	// ID 是受控结果事实的 canonical UUID，不是正文或自由 locator。
	ID foundation.ID
	// Hash 是该受控结果事实已有的 canonical SHA-256，不是操作层另算的内容哈希。
	Hash string
}

// WorkspaceAnalysisCanonicalCallBinding 是实际 Model/Tool Call 的权威请求绑定。
type WorkspaceAnalysisCanonicalCallBinding struct {
	// Kind 区分实际 Model Call 与 Tool Call。
	Kind WorkspaceAnalysisOperationCallKind
	// CallID 是实际 Call 的 canonical UUID。
	CallID foundation.ID
	// RequestHash 是实际 Call 已保存的 canonical request hash。
	RequestHash string
}

// WorkspaceAnalysisCanonicalResultBinding 是实际 model/tool receipt 或 candidate 的权威绑定。
type WorkspaceAnalysisCanonicalResultBinding struct {
	// Kind 是持久结果事实的受控类型。
	Kind WorkspaceAnalysisOperationResultKind
	// ResultID 是该持久结果事实的 canonical UUID。
	ResultID foundation.ID
	// Hash 是该结果事实自身已有的 canonical hash，操作层不得重新计算另一套内容哈希。
	Hash string
}

// WorkspaceAnalysisOperation 是跨替换 Attempt 归约一次实际调用的窄检查点。
type WorkspaceAnalysisOperation struct {
	// ID 是逻辑操作记录的 canonical UUID。
	ID foundation.ID
	// AnalysisRunID 是拥有该操作的 Workspace Analysis Run。
	AnalysisRunID foundation.ID
	// NodeKey 是静态 Workflow 节点键。
	NodeKey WorkspaceAnalysisOperationNodeKey
	// Kind 是语义操作种类。
	Kind WorkspaceAnalysisOperationKind
	// Ordinal 是同节点同种类内的确定性序号。
	Ordinal int
	// RequestHash 必须逐字等于实际 Model/Tool Call 的 canonical request hash。
	RequestHash string
	// Status 是窄逻辑调用检查点状态。
	Status WorkspaceAnalysisOperationStatus
	// FirstNodeAttemptID 是首次授权该调用的 Node Attempt。
	FirstNodeAttemptID *foundation.ID
	// LatestNodeAttemptID 是最近一次归约该调用的 Node Attempt，不属于逻辑键。
	LatestNodeAttemptID *foundation.ID
	// BudgetReservationID 是与实际 Call 原子建立的预算 reservation。
	BudgetReservationID *foundation.ID
	// Call 是实际 Model/Tool Call 的 typed 引用。
	Call *WorkspaceAnalysisOperationCallRef
	// Result 是成功时的 typed 结果引用；失败和 Unknown 必须为空。
	Result *WorkspaceAnalysisOperationResultRef
	// ErrorCode 是 Failed/Unknown 唯一允许保存的稳定错误码。
	ErrorCode string
	// Version 是持久 CAS 版本。
	Version int64
	// CreatedAt 是操作记录创建时间。
	CreatedAt time.Time
	// UpdatedAt 是最近一次持久归约时间。
	UpdatedAt time.Time
	// StartedAt 是实际 Call 获得授权的时间。
	StartedAt *time.Time
	// CompletedAt 是实际 Call 归约为终态的时间。
	CompletedAt *time.Time
}

// LogicalKey 返回不含首次/最近 Attempt 的稳定逻辑键。
func (operation WorkspaceAnalysisOperation) LogicalKey() WorkspaceAnalysisOperationKey {
	return WorkspaceAnalysisOperationKey{
		AnalysisRunID: operation.AnalysisRunID,
		NodeKey:       operation.NodeKey,
		Kind:          operation.Kind,
		Ordinal:       operation.Ordinal,
	}
}

// ValidateWorkspaceAnalysisOperation 校验 operation key、实际 Call、typed result 与生命周期一致。
func ValidateWorkspaceAnalysisOperation(operation WorkspaceAnalysisOperation) error {
	contract, err := WorkspaceAnalysisOperationContractForKey(operation.LogicalKey())
	if err != nil {
		return err
	}
	if !canonicalID(operation.ID) || operation.ID == operation.AnalysisRunID || !lowerHex(operation.RequestHash, 64) ||
		operation.Version < 1 || operation.CreatedAt.IsZero() || operation.UpdatedAt.Before(operation.CreatedAt) {
		return invalid(ErrorCodeWorkspaceAnalysisOperationInvalid, "workspace analysis operation identity or version is invalid")
	}

	switch operation.Status {
	case WorkspaceAnalysisOperationPending:
		if operationRuntimeBound(operation) || operation.StartedAt != nil || operation.CompletedAt != nil || operation.ErrorCode != "" {
			return inconsistent(ErrorCodeWorkspaceAnalysisOperationInvalid, "pending workspace analysis operation cannot claim runtime facts")
		}
	case WorkspaceAnalysisOperationStarted:
		if err := validateWorkspaceAnalysisOperationRuntime(operation, contract); err != nil {
			return err
		}
		if operation.Result != nil || operation.ErrorCode != "" || operation.CompletedAt != nil {
			return inconsistent(ErrorCodeWorkspaceAnalysisOperationInvalid, "started workspace analysis operation cannot claim a terminal result")
		}
	case WorkspaceAnalysisOperationSucceeded:
		if err := validateWorkspaceAnalysisOperationRuntime(operation, contract); err != nil {
			return err
		}
		if operation.ErrorCode != "" || !validWorkspaceAnalysisOperationCompletion(operation) ||
			!validWorkspaceAnalysisOperationResult(operation, contract) {
			return inconsistent(ErrorCodeWorkspaceAnalysisOperationInvalid, "succeeded workspace analysis operation result is invalid")
		}
	case WorkspaceAnalysisOperationFailed, WorkspaceAnalysisOperationUnknown:
		if err := validateWorkspaceAnalysisOperationRuntime(operation, contract); err != nil {
			return err
		}
		if operation.Result != nil || !canonicalErrorCode(operation.ErrorCode) || !validWorkspaceAnalysisOperationCompletion(operation) {
			return inconsistent(ErrorCodeWorkspaceAnalysisOperationInvalid, "failed or unknown workspace analysis operation must not claim a result")
		}
	default:
		return invalid(ErrorCodeWorkspaceAnalysisOperationInvalid, "workspace analysis operation status is unsupported")
	}
	return nil
}

// ValidateWorkspaceAnalysisOperationTransition 校验 PENDING 只能授权为 STARTED，STARTED 只能进入一个终态。
func ValidateWorkspaceAnalysisOperationTransition(from, to WorkspaceAnalysisOperationStatus) error {
	if from == WorkspaceAnalysisOperationPending && to == WorkspaceAnalysisOperationStarted ||
		from == WorkspaceAnalysisOperationStarted && (to == WorkspaceAnalysisOperationSucceeded ||
			to == WorkspaceAnalysisOperationFailed || to == WorkspaceAnalysisOperationUnknown) {
		return nil
	}
	return versionConflict(ErrorCodeWorkspaceAnalysisOperationTransitionInvalid, "workspace analysis operation status transition is not allowed")
}

// ValidateWorkspaceAnalysisOperationCallBinding 逐项复核实际 Call 类型、ID 与 canonical request hash。
func ValidateWorkspaceAnalysisOperationCallBinding(operation WorkspaceAnalysisOperation, binding WorkspaceAnalysisCanonicalCallBinding) error {
	if err := ValidateWorkspaceAnalysisOperation(operation); err != nil {
		return err
	}
	contract, err := WorkspaceAnalysisOperationContractForKey(operation.LogicalKey())
	if err != nil {
		return err
	}
	if operation.Call == nil || !canonicalID(binding.CallID) || !lowerHex(binding.RequestHash, 64) ||
		binding.Kind != contract.CallKind || operation.Call.Kind != binding.Kind || operation.Call.ID != binding.CallID ||
		operation.RequestHash != binding.RequestHash {
		return versionConflict(ErrorCodeWorkspaceAnalysisOperationRequestBindingMismatch, "workspace analysis operation does not match the canonical call request")
	}
	return nil
}

// ValidateWorkspaceAnalysisOperationResultBinding 逐项复核成功操作与实际持久结果的类型、ID 和 canonical hash。
func ValidateWorkspaceAnalysisOperationResultBinding(operation WorkspaceAnalysisOperation, binding WorkspaceAnalysisCanonicalResultBinding) error {
	if err := ValidateWorkspaceAnalysisOperation(operation); err != nil {
		return err
	}
	if operation.Status != WorkspaceAnalysisOperationSucceeded || operation.Result == nil ||
		!canonicalID(binding.ResultID) || !lowerHex(binding.Hash, 64) ||
		operation.Result.Kind != binding.Kind || operation.Result.ID != binding.ResultID || operation.Result.Hash != binding.Hash {
		return versionConflict(ErrorCodeWorkspaceAnalysisOperationResultBindingMismatch, "workspace analysis operation does not match the canonical persisted result")
	}
	return nil
}

// WorkspaceAnalysisOperationReplay 冻结 replacement Attempt 对 exact request hash 的恢复动作。
func WorkspaceAnalysisOperationReplay(operation WorkspaceAnalysisOperation, canonicalRequestHash string) (WorkspaceAnalysisOperationReplayDisposition, error) {
	if err := ValidateWorkspaceAnalysisOperation(operation); err != nil {
		return "", err
	}
	if !lowerHex(canonicalRequestHash, 64) || operation.RequestHash != canonicalRequestHash {
		return "", versionConflict(ErrorCodeWorkspaceAnalysisOperationRequestBindingMismatch, "workspace analysis operation replay request hash does not match the canonical call request")
	}
	switch operation.Status {
	case WorkspaceAnalysisOperationPending:
		return WorkspaceAnalysisOperationReplayAuthorize, nil
	case WorkspaceAnalysisOperationStarted:
		return WorkspaceAnalysisOperationReplayReconcile, nil
	case WorkspaceAnalysisOperationSucceeded:
		return WorkspaceAnalysisOperationReplayReuseResult, nil
	case WorkspaceAnalysisOperationFailed:
		return WorkspaceAnalysisOperationReplayFailure, nil
	case WorkspaceAnalysisOperationUnknown:
		return WorkspaceAnalysisOperationReplayTerminateUnknown, nil
	default:
		return "", invalid(ErrorCodeWorkspaceAnalysisOperationInvalid, "workspace analysis operation status is unsupported")
	}
}

func workspaceAnalysisOperationContract(nodeKey WorkspaceAnalysisOperationNodeKey, kind WorkspaceAnalysisOperationKind, ordinal int) (WorkspaceAnalysisOperationContract, bool) {
	for _, contract := range workspaceAnalysisV1OperationContracts {
		if contract.NodeKey == nodeKey && contract.Kind == kind && contract.Ordinal == ordinal {
			return contract, true
		}
	}
	return WorkspaceAnalysisOperationContract{}, false
}

func operationRuntimeBound(operation WorkspaceAnalysisOperation) bool {
	return operation.FirstNodeAttemptID != nil || operation.LatestNodeAttemptID != nil ||
		operation.BudgetReservationID != nil || operation.Call != nil || operation.Result != nil
}

func validateWorkspaceAnalysisOperationRuntime(operation WorkspaceAnalysisOperation, contract WorkspaceAnalysisOperationContract) error {
	if operation.FirstNodeAttemptID == nil || operation.LatestNodeAttemptID == nil || operation.BudgetReservationID == nil ||
		operation.Call == nil || operation.StartedAt == nil || !validWorkspaceAnalysisOperationStart(operation) {
		return inconsistent(ErrorCodeWorkspaceAnalysisOperationInvalid, "authorized workspace analysis operation runtime binding is incomplete")
	}
	firstAttemptID := *operation.FirstNodeAttemptID
	latestAttemptID := *operation.LatestNodeAttemptID
	reservationID := *operation.BudgetReservationID
	callID := operation.Call.ID
	if !canonicalID(firstAttemptID) || !canonicalID(latestAttemptID) || !canonicalID(reservationID) || !canonicalID(callID) ||
		operation.Call.Kind != contract.CallKind ||
		workspaceAnalysisOperationIDConflicts(operation.ID, operation.AnalysisRunID, firstAttemptID) ||
		workspaceAnalysisOperationIDConflicts(operation.ID, operation.AnalysisRunID, latestAttemptID) ||
		workspaceAnalysisOperationIDConflicts(operation.ID, operation.AnalysisRunID, reservationID) ||
		workspaceAnalysisOperationIDConflicts(operation.ID, operation.AnalysisRunID, callID) ||
		reservationID == firstAttemptID || reservationID == latestAttemptID || callID == firstAttemptID ||
		callID == latestAttemptID || callID == reservationID {
		return inconsistent(ErrorCodeWorkspaceAnalysisOperationInvalid, "workspace analysis operation runtime ids are invalid")
	}
	return nil
}

func validWorkspaceAnalysisOperationStart(operation WorkspaceAnalysisOperation) bool {
	return operation.StartedAt != nil && !operation.StartedAt.IsZero() &&
		!operation.StartedAt.Before(operation.CreatedAt) && !operation.StartedAt.After(operation.UpdatedAt)
}

func validWorkspaceAnalysisOperationCompletion(operation WorkspaceAnalysisOperation) bool {
	return operation.CompletedAt != nil && !operation.CompletedAt.IsZero() && operation.StartedAt != nil &&
		!operation.CompletedAt.Before(*operation.StartedAt) && !operation.CompletedAt.After(operation.UpdatedAt)
}

func validWorkspaceAnalysisOperationResult(operation WorkspaceAnalysisOperation, contract WorkspaceAnalysisOperationContract) bool {
	if operation.Result == nil || operation.Call == nil || operation.Result.Kind != contract.ResultKind ||
		!canonicalID(operation.Result.ID) || !lowerHex(operation.Result.Hash, 64) {
		return false
	}
	return operation.Result.ID != operation.ID && operation.Result.ID != operation.AnalysisRunID &&
		operation.Result.ID != *operation.FirstNodeAttemptID && operation.Result.ID != *operation.LatestNodeAttemptID &&
		operation.Result.ID != *operation.BudgetReservationID && operation.Result.ID != operation.Call.ID
}

func workspaceAnalysisOperationIDConflicts(operationID, analysisRunID, value foundation.ID) bool {
	return value == operationID || value == analysisRunID
}
