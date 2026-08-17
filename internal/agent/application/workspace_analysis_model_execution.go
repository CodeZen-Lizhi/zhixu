package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeWorkspaceAnalysisModelCommandInvalid 表示模型调用未匹配冻结的逻辑槽位或 canonical 请求。
	ErrorCodeWorkspaceAnalysisModelCommandInvalid = "AGENT_WORKSPACE_ANALYSIS_MODEL_COMMAND_INVALID"
	// ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid 表示持久层返回的授权事实与本次请求不一致。
	ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid = "AGENT_WORKSPACE_ANALYSIS_MODEL_AUTHORIZATION_INVALID"
	// ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable 表示模型授权或结算依赖不可用。
	ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable = "AGENT_WORKSPACE_ANALYSIS_MODEL_AUTHORIZATION_UNAVAILABLE"
	// ErrorCodeWorkspaceAnalysisModelCancellationConflict 表示取消已经持久化，
	// 当前已授权 Call 不能再发布成功结果，调用方必须先走 FAILED/UNKNOWN 收尾。
	ErrorCodeWorkspaceAnalysisModelCancellationConflict = "AGENT_WORKSPACE_ANALYSIS_MODEL_CANCELLATION_CONFLICT"
)

// WorkspaceAnalysisModelTerminalEvidence 是仅在持久 Model Operation 已确认终态时附着的安全投影。
// OperationID 只用于受信调用方关联，不会进入 JSON、格式化输出或结构化日志。
type WorkspaceAnalysisModelTerminalEvidence struct {
	OperationID foundation.ID          `json:"-"`
	CallStatus  domain.ModelCallStatus `json:"-"`
	RunStatus   domain.ModelRunStatus  `json:"-"`
}

// String 只投影冻结的终态组合，不回显逻辑 Operation 身份。
func (evidence WorkspaceAnalysisModelTerminalEvidence) String() string {
	return "WorkspaceAnalysisModelTerminalEvidence{call_status:" + string(evidence.CallStatus) +
		" run_status:" + string(evidence.RunStatus) + "}"
}

// GoString 避免 %#v 绕过终态证据的安全投影。
func (evidence WorkspaceAnalysisModelTerminalEvidence) GoString() string { return evidence.String() }

// LogValue 只记录已确认的终态状态，不记录 Operation ID。
func (evidence WorkspaceAnalysisModelTerminalEvidence) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("call_status", string(evidence.CallStatus)),
		slog.String("run_status", string(evidence.RunStatus)),
	)
}

type workspaceAnalysisModelTerminalEvidenceError struct {
	cause    error
	evidence WorkspaceAnalysisModelTerminalEvidence
}

func (err *workspaceAnalysisModelTerminalEvidenceError) Error() string {
	if err == nil {
		return "<nil>"
	}
	var classified *foundation.Error
	if errors.As(err.cause, &classified) && classified != nil {
		return classified.Error()
	}
	return "workspace analysis model terminal failure"
}

func (err *workspaceAnalysisModelTerminalEvidenceError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// WorkspaceAnalysisModelTerminalEvidenceFromError 从已确认终态的错误链提取安全证据。
func WorkspaceAnalysisModelTerminalEvidenceFromError(err error) (WorkspaceAnalysisModelTerminalEvidence, bool) {
	var terminal *workspaceAnalysisModelTerminalEvidenceError
	if !errors.As(err, &terminal) || terminal == nil {
		return WorkspaceAnalysisModelTerminalEvidence{}, false
	}
	return terminal.evidence, true
}

// WorkspaceAnalysisModelAuthorizationDisposition 是逻辑 Model Operation 唯一允许的恢复动作。
type WorkspaceAnalysisModelAuthorizationDisposition string

const (
	// WorkspaceAnalysisModelAuthorizationCreated 表示首次原子建立了 Model Run、Call、Operation 与预算。
	WorkspaceAnalysisModelAuthorizationCreated WorkspaceAnalysisModelAuthorizationDisposition = "CREATED"
	// WorkspaceAnalysisModelAuthorizationReconcile 表示既有 Call 尚不能安全重放，禁止再次调用 Provider。
	WorkspaceAnalysisModelAuthorizationReconcile WorkspaceAnalysisModelAuthorizationDisposition = "RECONCILE_EXACT_CALL"
	// WorkspaceAnalysisModelAuthorizationReuseResult 表示成功 Operation 只能读取既有受控结果。
	WorkspaceAnalysisModelAuthorizationReuseResult WorkspaceAnalysisModelAuthorizationDisposition = "REUSE_RESULT"
	// WorkspaceAnalysisModelAuthorizationReplayFailure 表示失败 Operation 只能重放稳定失败。
	WorkspaceAnalysisModelAuthorizationReplayFailure WorkspaceAnalysisModelAuthorizationDisposition = "REPLAY_FAILURE"
	// WorkspaceAnalysisModelAuthorizationTerminateUnknown 表示 Operation 已归约 Unknown，禁止再次调用 Provider。
	WorkspaceAnalysisModelAuthorizationTerminateUnknown WorkspaceAnalysisModelAuthorizationDisposition = "TERMINATE_UNKNOWN"
)

// WorkspaceAnalysisModelExecutionIdentity 是服务端从当前 Workflow Attempt 恢复的模型授权身份。
type WorkspaceAnalysisModelExecutionIdentity struct {
	WorkspaceID       foundation.ID
	DefinitionID      foundation.ID
	DefinitionVersion int64
	DefinitionHash    string
	WorkflowRunID     foundation.ID
	NodeKey           domain.WorkspaceAnalysisOperationNodeKey
	NodeRunID         foundation.ID
	NodeAttemptID     foundation.ID
	LeaseOwner        string
	LeaseFence        int64
}

// Validate 拒绝缺失、复用或不可 canonical 化的 Workflow/lease 身份。
func (identity WorkspaceAnalysisModelExecutionIdentity) Validate() error {
	ids := []foundation.ID{
		identity.WorkspaceID, identity.DefinitionID, identity.WorkflowRunID,
		identity.NodeRunID, identity.NodeAttemptID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return workspaceAnalysisModelError(errors.New("workspace analysis model identity contains an invalid id"))
		}
		if _, duplicate := seen[id]; duplicate {
			return workspaceAnalysisModelError(errors.New("workspace analysis model identity reuses an id"))
		}
		seen[id] = struct{}{}
	}
	owner := strings.TrimSpace(identity.LeaseOwner)
	if identity.DefinitionVersion < 1 || !canonicalWorkspaceAnalysisSHA256(identity.DefinitionHash) ||
		identity.NodeKey == "" || owner == "" || owner != identity.LeaseOwner || len(owner) > 256 || identity.LeaseFence < 1 {
		return workspaceAnalysisModelError(errors.New("workspace analysis model identity binding is invalid"))
	}
	return nil
}

// AuthorizeWorkspaceAnalysisModelCallCommand 原子授权一个服务端选择的 Model Operation。
// 候选 ID 只供首次创建；逻辑键已存在时持久层必须返回既有事实。
type AuthorizeWorkspaceAnalysisModelCallCommand struct {
	Identity        WorkspaceAnalysisModelExecutionIdentity
	OperationKey    domain.WorkspaceAnalysisOperationKey
	OperationID     foundation.ID
	ReservationID   foundation.ID
	Run             domain.ModelRun
	Call            domain.ModelCall
	RequestDocument []byte
}

// Validate 校验候选 Run/Call 逐项绑定实际 Provider 请求和冻结操作槽位。
func (command AuthorizeWorkspaceAnalysisModelCallCommand) Validate() error {
	if err := command.Identity.Validate(); err != nil {
		return err
	}
	contract, err := domain.WorkspaceAnalysisOperationContractForKey(command.OperationKey)
	if err != nil {
		return err
	}
	if contract.CallKind != domain.WorkspaceAnalysisOperationCallModel ||
		command.OperationKey.NodeKey != command.Identity.NodeKey ||
		!workspaceAnalysisModelCandidateIDs(command) ||
		domain.ValidateModelRun(command.Run) != nil || command.Run.Status != domain.ModelRunRunning || command.Run.Version != 1 ||
		domain.ValidateModelCall(command.Call) != nil || command.Call.Status != domain.ModelCallStarted || command.Call.Version != 1 ||
		!workspaceAnalysisModelRunCallBinding(command) || !workspaceAnalysisModelSlotBinding(command.OperationKey.Kind, command.Run, command.Call) ||
		len(command.RequestDocument) == 0 || int64(len(command.RequestDocument)) != command.Call.RequestBytes ||
		workspaceAnalysisSHA256(command.RequestDocument) != command.Call.RequestHash {
		return workspaceAnalysisModelError(errors.New("workspace analysis model authorization command is invalid"))
	}
	return nil
}

// WorkspaceAnalysisModelAuthorizationResult 返回实际持久 Model Run/Call 与逻辑事实身份。
type WorkspaceAnalysisModelAuthorizationResult struct {
	Run           domain.ModelRun
	Call          domain.ModelCall
	OperationID   foundation.ID
	ReservationID foundation.ID
	Disposition   WorkspaceAnalysisModelAuthorizationDisposition
}

// ValidateFor 校验持久授权没有替换本次模型请求或首次创建的候选身份。
func (result WorkspaceAnalysisModelAuthorizationResult) ValidateFor(command AuthorizeWorkspaceAnalysisModelCallCommand) error {
	if err := command.Validate(); err != nil {
		return err
	}
	if domain.ValidateModelRun(result.Run) != nil || domain.ValidateModelCall(result.Call) != nil ||
		!canonicalApplicationID(result.OperationID) || !canonicalApplicationID(result.ReservationID) ||
		result.OperationID == result.ReservationID || result.OperationID == result.Run.ID || result.OperationID == result.Call.ID ||
		result.ReservationID == result.Run.ID || result.ReservationID == result.Call.ID ||
		!sameWorkspaceAnalysisModelRequest(command.Run, command.Call, result.Run, result.Call) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis model authorization returned a different binding"))
	}
	valid := false
	switch result.Disposition {
	case WorkspaceAnalysisModelAuthorizationCreated:
		valid = result.Run.Status == domain.ModelRunRunning && result.Run.Version == 1 &&
			result.Call.Status == domain.ModelCallStarted && result.Call.Version == 1 &&
			result.Run.ID == command.Run.ID && result.Call.ID == command.Call.ID &&
			result.Run.NodeAttemptID == command.Run.NodeAttemptID &&
			result.OperationID == command.OperationID && result.ReservationID == command.ReservationID
	case WorkspaceAnalysisModelAuthorizationReconcile:
		valid = result.Run.Status == domain.ModelRunRunning && result.Call.Status == domain.ModelCallStarted &&
			result.Run.NodeAttemptID == command.Identity.NodeAttemptID
	case WorkspaceAnalysisModelAuthorizationReuseResult:
		valid = result.Run.Status == domain.ModelRunSucceeded && result.Call.Status == domain.ModelCallSucceeded
	case WorkspaceAnalysisModelAuthorizationReplayFailure:
		valid = (result.Run.Status == domain.ModelRunFailed && result.Call.Status == domain.ModelCallFailed) ||
			(result.Run.Status == domain.ModelRunRefused && result.Call.Status == domain.ModelCallSucceeded &&
				result.Run.FinalResultType == domain.ResultTypeRefusal &&
				result.Run.FinalErrorCode == string(domain.WorkspaceAnalysisRunModelRefused))
	case WorkspaceAnalysisModelAuthorizationTerminateUnknown:
		valid = result.Run.Status == domain.ModelRunUnknown && result.Call.Status == domain.ModelCallUnknown
	}
	if !valid {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis model authorization disposition is inconsistent"))
	}
	return nil
}

// FinalizeWorkspaceAnalysisModelCallCommand 原子关闭一个没有受控结果的拒绝、FAILED 或 UNKNOWN Model Call。
type FinalizeWorkspaceAnalysisModelCallCommand struct {
	Identity            WorkspaceAnalysisModelExecutionIdentity
	OperationKey        domain.WorkspaceAnalysisOperationKey
	OperationID         foundation.ID
	ReservationID       foundation.ID
	ExpectedCallVersion int64
	ExpectedRunVersion  int64
	Call                domain.ModelCall
	Run                 domain.ModelRun
}

// Validate 校验拒绝、失败或 Unknown 的 Call/Run 使用冻结的终态组合和稳定错误码。
func (command FinalizeWorkspaceAnalysisModelCallCommand) Validate() error {
	if err := validateWorkspaceAnalysisModelTerminalBinding(
		command.Identity, command.OperationKey, command.OperationID, command.ReservationID,
		command.ExpectedCallVersion, command.ExpectedRunVersion, command.Run, command.Call,
	); err != nil {
		return err
	}
	refused := command.Call.Status == domain.ModelCallSucceeded &&
		command.Run.Status == domain.ModelRunRefused &&
		command.Run.FinalResultType == domain.ResultTypeRefusal &&
		command.Run.FinalErrorCode == string(domain.WorkspaceAnalysisRunModelRefused) &&
		command.Call.ErrorCode == ""
	failed := command.Call.Status == domain.ModelCallFailed &&
		command.Run.Status == domain.ModelRunFailed &&
		command.Run.FinalErrorCode == command.Call.ErrorCode
	unknown := command.Call.Status == domain.ModelCallUnknown &&
		command.Run.Status == domain.ModelRunUnknown &&
		command.Run.FinalErrorCode == command.Call.ErrorCode
	if !refused && !failed && !unknown {
		return workspaceAnalysisModelError(errors.New("workspace analysis model failure terminal is invalid"))
	}
	return nil
}

// FinalizeWorkspaceAnalysisModelResultCommand 原子保存 Planner/Review 的成功 ModelResult 并结算预算。
type FinalizeWorkspaceAnalysisModelResultCommand struct {
	Identity            WorkspaceAnalysisModelExecutionIdentity
	OperationKey        domain.WorkspaceAnalysisOperationKey
	OperationID         foundation.ID
	ReservationID       foundation.ID
	ExpectedCallVersion int64
	ExpectedRunVersion  int64
	Call                domain.ModelCall
	Run                 domain.ModelRun
	Result              domain.WorkspaceAnalysisModelResult
}

// Validate 校验成功 Call、Run 与 canonical ModelResult 的直接绑定。
func (command FinalizeWorkspaceAnalysisModelResultCommand) Validate() error {
	if err := validateWorkspaceAnalysisModelTerminalBinding(
		command.Identity, command.OperationKey, command.OperationID, command.ReservationID,
		command.ExpectedCallVersion, command.ExpectedRunVersion, command.Run, command.Call,
	); err != nil {
		return err
	}
	if command.Call.Status != domain.ModelCallSucceeded || command.Run.Status != domain.ModelRunSucceeded ||
		domain.ValidateWorkspaceAnalysisModelResult(command.Result) != nil ||
		command.Result.WorkspaceID != command.Identity.WorkspaceID ||
		command.Result.AnalysisRunID != command.OperationKey.AnalysisRunID ||
		command.Result.OperationID != command.OperationID ||
		command.Result.NodeAttemptID != command.Run.NodeAttemptID ||
		command.Result.ModelRunID != command.Run.ID || command.Result.ModelCallID != command.Call.ID ||
		command.Result.ID == command.ReservationID ||
		command.Result.OperationKind != command.OperationKey.Kind ||
		command.Result.DocumentHash != command.Call.ResponseHash || command.Result.DocumentBytes != command.Call.ResponseBytes ||
		command.Result.Schema != command.Call.Schema || command.Result.Schema != command.Run.Schema ||
		command.Call.CompletedAt == nil || command.Run.CompletedAt == nil ||
		command.Result.CreatedAt.Before(*command.Call.CompletedAt) || command.Result.CreatedAt.Before(*command.Run.CompletedAt) {
		return workspaceAnalysisModelError(errors.New("workspace analysis model result terminal is invalid"))
	}
	return nil
}

// FinalizeWorkspaceAnalysisModelCandidateCommand 原子保存 Synthesis Candidate 并结算实际模型用量。
type FinalizeWorkspaceAnalysisModelCandidateCommand struct {
	Identity            WorkspaceAnalysisModelExecutionIdentity
	OperationKey        domain.WorkspaceAnalysisOperationKey
	OperationID         foundation.ID
	ReservationID       foundation.ID
	ExpectedCallVersion int64
	ExpectedRunVersion  int64
	Call                domain.ModelCall
	Run                 domain.ModelRun
	Candidate           domain.WorkspaceAnalysisCandidate
}

// Validate 校验成功 Call、Run 与不可变 Candidate 的直接绑定。
func (command FinalizeWorkspaceAnalysisModelCandidateCommand) Validate() error {
	if err := validateWorkspaceAnalysisModelTerminalBinding(
		command.Identity, command.OperationKey, command.OperationID, command.ReservationID,
		command.ExpectedCallVersion, command.ExpectedRunVersion, command.Run, command.Call,
	); err != nil {
		return err
	}
	candidate := command.Candidate
	if command.OperationKey.Kind != domain.WorkspaceAnalysisOperationAnswerSynthesis ||
		command.Call.Status != domain.ModelCallSucceeded || command.Run.Status != domain.ModelRunSucceeded ||
		command.Run.FinalResultType != domain.ResultTypeWorkspaceAnalysisAnswer ||
		domain.ValidateWorkspaceAnalysisCandidate(candidate) != nil ||
		candidate.WorkspaceID != command.Identity.WorkspaceID ||
		candidate.AnalysisRunID != command.OperationKey.AnalysisRunID ||
		candidate.SynthesisOperationID != command.OperationID ||
		candidate.NodeAttemptID != command.Run.NodeAttemptID || candidate.SynthesisModelRunID != command.Run.ID ||
		candidate.ID == command.ReservationID || candidate.ID == command.Call.ID ||
		candidate.SchemaID != command.Run.Schema.ID || candidate.SchemaVersion != workspaceAnalysisSchemaVersion(command.Run.Schema.Version) ||
		candidate.DocumentHash != command.Call.ResponseHash || candidate.DocumentBytes != command.Call.ResponseBytes ||
		command.Call.CompletedAt == nil || command.Run.CompletedAt == nil ||
		candidate.CreatedAt.Before(*command.Call.CompletedAt) || candidate.CreatedAt.Before(*command.Run.CompletedAt) {
		return workspaceAnalysisModelError(errors.New("workspace analysis model candidate terminal is invalid"))
	}
	return nil
}

// WorkspaceAnalysisModelMutationResult 返回模型终态闭环的权威事实。
type WorkspaceAnalysisModelMutationResult struct {
	Run           domain.ModelRun
	Call          domain.ModelCall
	Result        *domain.WorkspaceAnalysisModelResult
	Candidate     *domain.WorkspaceAnalysisCandidate
	OperationID   foundation.ID
	ReservationID foundation.ID
	Replayed      bool
}

// WorkspaceAnalysisCandidateQuery 只允许 exact Operation/Call 读取 Synthesis Candidate。
type WorkspaceAnalysisCandidateQuery struct {
	WorkspaceID   foundation.ID
	AnalysisRunID foundation.ID
	OperationID   foundation.ID
	ModelRunID    foundation.ID
	ModelCallID   foundation.ID
}

// Validate 拒绝缺失或复用的候选答案身份。
func (query WorkspaceAnalysisCandidateQuery) Validate() error {
	return validateWorkspaceAnalysisModelQueryIDs(
		query.WorkspaceID, query.AnalysisRunID, query.OperationID, query.ModelRunID, query.ModelCallID,
	)
}

// WorkspaceAnalysisModelResultQuery 只允许 exact Operation/Call 读取 Planner 或 Review 回执。
type WorkspaceAnalysisModelResultQuery struct {
	WorkspaceID   foundation.ID
	AnalysisRunID foundation.ID
	OperationID   foundation.ID
	ModelRunID    foundation.ID
	ModelCallID   foundation.ID
}

// Validate 拒绝缺失或复用的模型结果身份。
func (query WorkspaceAnalysisModelResultQuery) Validate() error {
	return validateWorkspaceAnalysisModelQueryIDs(
		query.WorkspaceID, query.AnalysisRunID, query.OperationID, query.ModelRunID, query.ModelCallID,
	)
}

// WorkspaceAnalysisRetrievalPlanCheckpointQuery 按跨 Attempt 的稳定逻辑槽位查找冻结检索计划。
type WorkspaceAnalysisRetrievalPlanCheckpointQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
	NodeRunID     foundation.ID
}

// Validate 拒绝缺失、复用或跨运行的检查点查询身份。
func (query WorkspaceAnalysisRetrievalPlanCheckpointQuery) Validate() error {
	return validateWorkspaceAnalysisModelQueryIDs(
		query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID, query.NodeRunID,
	)
}

// WorkspaceAnalysisRetrievalPlanCheckpoint 是首次授权后不可变的计划请求身份与 Retrieval 版本。
type WorkspaceAnalysisRetrievalPlanCheckpoint struct {
	OperationID   foundation.ID
	ModelRunID    foundation.ID
	ModelCallID   foundation.ID
	ModelResultID foundation.ID
	Retrieval     domain.RetrievalRef
	RequestHash   string
	ResultHash    string
	Status        domain.WorkspaceAnalysisOperationStatus
}

// ValidateFor 校验检查点只描述 exact RETRIEVAL_PLAN 槽位的完整持久事实。
func (checkpoint WorkspaceAnalysisRetrievalPlanCheckpoint) ValidateFor(
	query WorkspaceAnalysisRetrievalPlanCheckpointQuery,
) error {
	if err := query.Validate(); err != nil {
		return err
	}
	ids := []foundation.ID{checkpoint.OperationID, checkpoint.ModelRunID, checkpoint.ModelCallID}
	if checkpoint.ModelResultID != "" {
		ids = append(ids, checkpoint.ModelResultID)
	}
	if err := validateWorkspaceAnalysisModelQueryIDs(ids...); err != nil || checkpoint.Retrieval.Validate() != nil ||
		!canonicalWorkspaceAnalysisSHA256(checkpoint.RequestHash) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis retrieval plan checkpoint is invalid"))
	}
	hasResult := checkpoint.ModelResultID != "" || checkpoint.ResultHash != ""
	switch checkpoint.Status {
	case domain.WorkspaceAnalysisOperationStarted,
		domain.WorkspaceAnalysisOperationFailed,
		domain.WorkspaceAnalysisOperationUnknown:
		if hasResult {
			return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis retrieval plan terminal has an unexpected result"))
		}
	case domain.WorkspaceAnalysisOperationSucceeded:
		if checkpoint.ModelResultID == "" || !canonicalWorkspaceAnalysisSHA256(checkpoint.ResultHash) {
			return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis retrieval plan success result is incomplete"))
		}
	default:
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis retrieval plan checkpoint status is invalid"))
	}
	return nil
}

// WorkspaceAnalysisRetrievalPlanCheckpointReader 读取先于当前 Active Index 的冻结计划请求事实。
type WorkspaceAnalysisRetrievalPlanCheckpointReader interface {
	FindWorkspaceAnalysisRetrievalPlanCheckpoint(
		context.Context,
		WorkspaceAnalysisRetrievalPlanCheckpointQuery,
	) (WorkspaceAnalysisRetrievalPlanCheckpoint, bool, error)
}

func validateWorkspaceAnalysisModelQueryIDs(ids ...foundation.ID) error {
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return workspaceAnalysisModelError(errors.New("workspace analysis model result query id is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return workspaceAnalysisModelError(errors.New("workspace analysis model result query id is reused"))
		}
		seen[id] = struct{}{}
	}
	return nil
}

// WorkspaceAnalysisModelOperationRepository 是 Workspace Analysis 模型调用的原子授权、终结与回放边界。
type WorkspaceAnalysisModelOperationRepository interface {
	AuthorizeWorkspaceAnalysisModelCall(context.Context, AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error)
	FinalizeWorkspaceAnalysisModelCall(context.Context, FinalizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelMutationResult, error)
	FinalizeWorkspaceAnalysisModelResult(context.Context, FinalizeWorkspaceAnalysisModelResultCommand) (WorkspaceAnalysisModelMutationResult, error)
	FinalizeWorkspaceAnalysisModelCandidate(context.Context, FinalizeWorkspaceAnalysisModelCandidateCommand) (WorkspaceAnalysisModelMutationResult, error)
	LoadWorkspaceAnalysisModelResult(context.Context, WorkspaceAnalysisModelResultQuery) (domain.WorkspaceAnalysisModelResult, error)
	LoadWorkspaceAnalysisCandidate(context.Context, WorkspaceAnalysisCandidateQuery) (domain.WorkspaceAnalysisCandidate, error)
}

func workspaceAnalysisSchemaVersion(value string) int64 {
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return version
}

func validateWorkspaceAnalysisModelTerminalBinding(
	identity WorkspaceAnalysisModelExecutionIdentity,
	key domain.WorkspaceAnalysisOperationKey,
	operationID foundation.ID,
	reservationID foundation.ID,
	expectedCallVersion int64,
	expectedRunVersion int64,
	run domain.ModelRun,
	call domain.ModelCall,
) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	contract, err := domain.WorkspaceAnalysisOperationContractForKey(key)
	if err != nil {
		return err
	}
	if contract.CallKind != domain.WorkspaceAnalysisOperationCallModel || key.NodeKey != identity.NodeKey ||
		!canonicalApplicationID(operationID) || !canonicalApplicationID(reservationID) || operationID == reservationID ||
		operationID == run.ID || operationID == call.ID || reservationID == run.ID || reservationID == call.ID ||
		expectedCallVersion < 1 || expectedRunVersion < 1 || call.Version != expectedCallVersion+1 || run.Version != expectedRunVersion+1 ||
		domain.ValidateModelRun(run) != nil || run.Status == domain.ModelRunRunning ||
		domain.ValidateModelCall(call) != nil || call.Status == domain.ModelCallStarted ||
		!workspaceAnalysisModelRunCallBinding(AuthorizeWorkspaceAnalysisModelCallCommand{Identity: identity, OperationKey: key, Run: run, Call: call}) ||
		!workspaceAnalysisModelSlotBinding(key.Kind, run, call) || call.CompletedAt == nil || run.CompletedAt == nil ||
		!run.CompletedAt.Equal(run.UpdatedAt) || run.CompletedAt.Before(*call.CompletedAt) {
		return workspaceAnalysisModelError(errors.New("workspace analysis model terminal binding is invalid"))
	}
	return nil
}

func workspaceAnalysisModelRunCallBinding(command AuthorizeWorkspaceAnalysisModelCallCommand) bool {
	run, call, identity := command.Run, command.Call, command.Identity
	return run.WorkspaceID == identity.WorkspaceID && run.WorkflowRunID == identity.WorkflowRunID &&
		run.NodeRunID == identity.NodeRunID && run.NodeAttemptID == identity.NodeAttemptID &&
		call.ModelRunID == run.ID && call.CallNo == 1 && call.Model == run.Model && call.Profile == run.Profile &&
		call.Prompt == run.Prompt && call.Schema == run.Schema
}

func workspaceAnalysisModelSlotBinding(kind domain.WorkspaceAnalysisOperationKind, run domain.ModelRun, call domain.ModelCall) bool {
	switch kind {
	case domain.WorkspaceAnalysisOperationRetrievalPlan:
		return call.Phase == domain.ModelCallPlan && call.MaxOutputTokens == int(WorkspaceAnalysisV1PlanMaxOutputTokens) &&
			run.Schema == (domain.SchemaRef{ID: domain.WorkspaceAnalysisPlanSchemaID, Version: domain.OutputSchemaVersionV1}) &&
			run.ReducedSchema == run.Schema
	case domain.WorkspaceAnalysisOperationAnswerSynthesis:
		return call.Phase == domain.ModelCallAnswer && call.MaxOutputTokens > 0 &&
			call.MaxOutputTokens <= int(WorkspaceAnalysisV1SynthesisMaxOutputTokens) &&
			run.Schema == (domain.SchemaRef{ID: domain.WorkspaceAnalysisCandidateSchemaID, Version: "1"}) && run.ReducedSchema == run.Schema
	case domain.WorkspaceAnalysisOperationFaithfulnessReview:
		return call.Phase == domain.ModelCallReview && call.MaxOutputTokens == int(WorkspaceAnalysisV1ReviewMaxOutputTokens) &&
			run.Schema == (domain.SchemaRef{ID: domain.FaithfulnessReviewSchemaID, Version: domain.OutputSchemaVersionV1}) &&
			run.ReducedSchema == run.Schema
	default:
		return false
	}
}

func workspaceAnalysisModelCandidateIDs(command AuthorizeWorkspaceAnalysisModelCallCommand) bool {
	ids := []foundation.ID{
		command.OperationID, command.ReservationID, command.OperationKey.AnalysisRunID,
		command.Run.ID, command.Call.ID, command.Identity.WorkspaceID, command.Identity.DefinitionID,
		command.Identity.WorkflowRunID, command.Identity.NodeRunID, command.Identity.NodeAttemptID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func sameWorkspaceAnalysisModelRequest(expectedRun domain.ModelRun, expectedCall domain.ModelCall, actualRun domain.ModelRun, actualCall domain.ModelCall) bool {
	return expectedCall.ModelRunID == expectedRun.ID && actualCall.ModelRunID == actualRun.ID &&
		expectedRun.WorkspaceID == actualRun.WorkspaceID && expectedRun.WorkflowRunID == actualRun.WorkflowRunID &&
		expectedRun.NodeRunID == actualRun.NodeRunID && sameWorkspaceAnalysisOptionalInt64(expectedRun.ModelSettingsRevision, actualRun.ModelSettingsRevision) &&
		expectedRun.Model == actualRun.Model && expectedRun.Profile == actualRun.Profile && expectedRun.Prompt == actualRun.Prompt &&
		expectedRun.Schema == actualRun.Schema && expectedRun.ReducedSchema == actualRun.ReducedSchema &&
		sameWorkspaceAnalysisRetrieval(expectedRun.Retrieval, actualRun.Retrieval) && expectedRun.MemoryContext == actualRun.MemoryContext &&
		expectedCall.CallNo == actualCall.CallNo &&
		expectedCall.Phase == actualCall.Phase && expectedCall.Model == actualCall.Model && expectedCall.Profile == actualCall.Profile &&
		expectedCall.Prompt == actualCall.Prompt && expectedCall.Schema == actualCall.Schema &&
		expectedCall.MaxOutputTokens == actualCall.MaxOutputTokens && expectedCall.RequestHash == actualCall.RequestHash &&
		expectedCall.RequestBytes == actualCall.RequestBytes
}

func sameWorkspaceAnalysisOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameWorkspaceAnalysisRetrieval(left, right domain.RetrievalRef) bool {
	if left.IndexVersionID != right.IndexVersionID || left.RerankModelVersion != right.RerankModelVersion {
		return false
	}
	if left.EmbeddingVersionID == nil || right.EmbeddingVersionID == nil {
		return left.EmbeddingVersionID == nil && right.EmbeddingVersionID == nil
	}
	return *left.EmbeddingVersionID == *right.EmbeddingVersionID
}

func workspaceAnalysisSHA256(document []byte) string {
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:])
}

func workspaceAnalysisModelError(cause error) error {
	return applicationError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisModelCommandInvalid, false, cause)
}

func workspaceAnalysisModelAuthorizationError(cause error) error {
	return applicationError(foundation.ErrorConsistencyViolation, ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid, false, cause)
}

// WorkspaceAnalysisModelCancellationConflict reports the narrow cancellation
// race marker emitted by a persistence adapter. It intentionally does not
// classify ordinary lease, budget, definition, or version conflicts.
func WorkspaceAnalysisModelCancellationConflict(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified != nil &&
		classified.Kind == foundation.ErrorVersionConflict &&
		classified.Code == ErrorCodeWorkspaceAnalysisModelCancellationConflict &&
		!classified.Retryable
}

// workspaceAnalysisModelTerminalError 只接受已由 Repository 返回且已验证的终态事实。
// 非冻结组合永远不携带 evidence，避免候选或未确认状态越过持久化边界。
func workspaceAnalysisModelTerminalError(
	cause error,
	operationID foundation.ID,
	call domain.ModelCall,
	run domain.ModelRun,
) error {
	if cause == nil || !canonicalApplicationID(operationID) || !workspaceAnalysisModelFrozenTerminal(call.Status, run.Status) {
		return cause
	}
	return &workspaceAnalysisModelTerminalEvidenceError{
		cause: cause,
		evidence: WorkspaceAnalysisModelTerminalEvidence{
			OperationID: operationID,
			CallStatus:  call.Status,
			RunStatus:   run.Status,
		},
	}
}

func workspaceAnalysisModelFrozenTerminal(call domain.ModelCallStatus, run domain.ModelRunStatus) bool {
	return (call == domain.ModelCallFailed && run == domain.ModelRunFailed) ||
		(call == domain.ModelCallUnknown && run == domain.ModelRunUnknown) ||
		(call == domain.ModelCallSucceeded && run == domain.ModelRunRefused)
}
