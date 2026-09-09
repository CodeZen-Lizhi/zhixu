package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceAnalysisToolExecutionIdentity 是 Tools 从当前 Workflow Attempt 恢复的服务端身份。
type WorkspaceAnalysisToolExecutionIdentity struct {
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

// Validate 校验 Workflow、节点与租约身份完整且互不复用。
func (identity WorkspaceAnalysisToolExecutionIdentity) Validate() error {
	return (WorkspaceAnalysisModelExecutionIdentity(identity)).Validate()
}

// WorkspaceAnalysisToolOperationSnapshot 是当前 scope 中已锁定并完成 Agent 领域校验的值快照。
type WorkspaceAnalysisToolOperationSnapshot struct {
	Run         domain.WorkspaceAnalysisRun
	Operation   domain.WorkspaceAnalysisOperation
	Reservation *domain.WorkspaceAnalysisBudgetReservation
	DatabaseNow time.Time
}

// Validate 校验快照只包含一个完整 Tool Operation 预算闭包和数据库时间。
func (snapshot WorkspaceAnalysisToolOperationSnapshot) Validate() error {
	if domain.ValidateWorkspaceAnalysisRun(snapshot.Run) != nil ||
		domain.ValidateWorkspaceAnalysisOperation(snapshot.Operation) != nil ||
		snapshot.Operation.AnalysisRunID != snapshot.Run.ID ||
		validateWorkspaceAnalysisToolOperationKey(snapshot.Operation.LogicalKey()) != nil ||
		!canonicalWorkspaceAnalysisToolTime(snapshot.DatabaseNow) ||
		snapshot.DatabaseNow.Before(snapshot.Run.UpdatedAt) ||
		snapshot.DatabaseNow.Before(snapshot.Operation.UpdatedAt) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool operation snapshot is invalid"))
	}
	if snapshot.Operation.Status == domain.WorkspaceAnalysisOperationPending {
		if snapshot.Reservation != nil {
			return workspaceAnalysisToolInvalid(errors.New("pending workspace analysis tool operation has a reservation"))
		}
		return nil
	}
	if snapshot.Reservation == nil ||
		domain.ValidateWorkspaceAnalysisBudgetReservationBinding(snapshot.Run, *snapshot.Reservation, snapshot.Operation) != nil ||
		snapshot.DatabaseNow.Before(snapshot.Reservation.CreatedAt) ||
		(snapshot.Reservation.SettledAt != nil && snapshot.DatabaseNow.Before(*snapshot.Reservation.SettledAt)) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool operation reservation is invalid"))
	}
	return nil
}

// PrepareWorkspaceAnalysisToolOperationCommand 只为授权入口创建或锁定 logical Operation。
type PrepareWorkspaceAnalysisToolOperationCommand struct {
	Identity                WorkspaceAnalysisToolExecutionIdentity
	OperationKey            domain.WorkspaceAnalysisOperationKey
	CandidateOperationID    foundation.ID
	RequestHash             string
	ExpectedToolCatalogHash string
	Arguments               json.RawMessage `json:"-"`
	// RequireExisting prevents recovery from manufacturing a missing pending fact.
	RequireExisting bool
}

func (PrepareWorkspaceAnalysisToolOperationCommand) String() string {
	return "PrepareWorkspaceAnalysisToolOperationCommand{[REDACTED]}"
}

func (command PrepareWorkspaceAnalysisToolOperationCommand) GoString() string {
	return command.String()
}

func (command PrepareWorkspaceAnalysisToolOperationCommand) LogValue() slog.Value {
	return slog.StringValue(command.String())
}

// Validate 校验候选 Operation、逻辑槽位与冻结请求目录绑定。
func (command PrepareWorkspaceAnalysisToolOperationCommand) Validate() error {
	if err := validateWorkspaceAnalysisToolIdentityKey(command.Identity, command.OperationKey); err != nil {
		return err
	}
	ids := append(workspaceAnalysisToolIdentityIDs(command.Identity), command.OperationKey.AnalysisRunID, command.CandidateOperationID)
	if validateWorkspaceAnalysisModelQueryIDs(ids...) != nil ||
		!canonicalWorkspaceAnalysisSHA256(command.RequestHash) ||
		!canonicalWorkspaceAnalysisSHA256(command.ExpectedToolCatalogHash) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool prepare command is invalid"))
	}
	return nil
}

// WorkspaceAnalysisToolCallLockQuery 用 Tool-owned Call ID 锁定既有 Agent closure。
type WorkspaceAnalysisToolCallLockQuery struct {
	Identity    WorkspaceAnalysisToolExecutionIdentity
	ToolCallID  foundation.ID
	RequestHash string
}

// Validate 校验 Call、请求哈希与当前执行身份不会发生复用。
func (query WorkspaceAnalysisToolCallLockQuery) Validate() error {
	if err := query.Identity.Validate(); err != nil {
		return err
	}
	ids := append(workspaceAnalysisToolIdentityIDs(query.Identity), query.ToolCallID)
	if validateWorkspaceAnalysisModelQueryIDs(ids...) != nil || !canonicalWorkspaceAnalysisSHA256(query.RequestHash) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool call lock query is invalid"))
	}
	return nil
}

// ReserveWorkspaceAnalysisToolOperationCommand 在 Tool Call 已插入后建立 Agent runtime facts。
type ReserveWorkspaceAnalysisToolOperationCommand struct {
	Identity                 WorkspaceAnalysisToolExecutionIdentity
	OperationKey             domain.WorkspaceAnalysisOperationKey
	OperationID              foundation.ID
	CandidateReservationID   foundation.ID
	ToolCallID               foundation.ID
	RequestHash              string
	ExpectedRunVersion       int64
	ExpectedOperationVersion int64
}

// Validate 校验首次预算预留的完整身份、版本与 canonical 请求绑定。
func (command ReserveWorkspaceAnalysisToolOperationCommand) Validate() error {
	if err := validateWorkspaceAnalysisToolIdentityKey(command.Identity, command.OperationKey); err != nil {
		return err
	}
	ids := append(workspaceAnalysisToolIdentityIDs(command.Identity), command.OperationKey.AnalysisRunID,
		command.OperationID, command.CandidateReservationID, command.ToolCallID)
	if validateWorkspaceAnalysisModelQueryIDs(ids...) != nil ||
		!canonicalWorkspaceAnalysisSHA256(command.RequestHash) ||
		command.ExpectedRunVersion < 1 || command.ExpectedOperationVersion < 1 {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool reserve command is invalid"))
	}
	return nil
}

// WorkspaceAnalysisToolSettlementKind 冻结 Tool Call 归约到 Agent facts 的合法终态组合。
type WorkspaceAnalysisToolSettlementKind string

const (
	// WorkspaceAnalysisToolSettlementSucceeded 表示 Call 与 receipt 均已成功闭合。
	WorkspaceAnalysisToolSettlementSucceeded WorkspaceAnalysisToolSettlementKind = "SUCCEEDED"
	// WorkspaceAnalysisToolSettlementFailed 表示 Call 以稳定失败终结。
	WorkspaceAnalysisToolSettlementFailed WorkspaceAnalysisToolSettlementKind = "FAILED"
	// WorkspaceAnalysisToolSettlementReceiptInvalid 表示成功 Call 的 receipt 无法通过闭包校验。
	WorkspaceAnalysisToolSettlementReceiptInvalid WorkspaceAnalysisToolSettlementKind = "RECEIPT_INVALID"
	// WorkspaceAnalysisToolSettlementUnknown 表示 Call 结果无法安全证明并按全额计费。
	WorkspaceAnalysisToolSettlementUnknown WorkspaceAnalysisToolSettlementKind = "UNKNOWN"
)

// Validate 拒绝 settlement 四值合同之外的状态。
func (kind WorkspaceAnalysisToolSettlementKind) Validate() error {
	switch kind {
	case WorkspaceAnalysisToolSettlementSucceeded, WorkspaceAnalysisToolSettlementFailed,
		WorkspaceAnalysisToolSettlementReceiptInvalid, WorkspaceAnalysisToolSettlementUnknown:
		return nil
	default:
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool settlement kind is invalid"))
	}
}

// SettleWorkspaceAnalysisToolOperationCommand 在 Tools 已完成 Call/receipt mutation 后归约 Agent facts。
type SettleWorkspaceAnalysisToolOperationCommand struct {
	Identity                 WorkspaceAnalysisToolExecutionIdentity
	OperationKey             domain.WorkspaceAnalysisOperationKey
	OperationID              foundation.ID
	ReservationID            foundation.ID
	ToolCallID               foundation.ID
	RequestHash              string
	ExpectedRunVersion       int64
	ExpectedOperationVersion int64
	Kind                     WorkspaceAnalysisToolSettlementKind
	Result                   *domain.WorkspaceAnalysisOperationResultRef
	ErrorCode                string
	CompletedAt              time.Time
}

// Validate 校验 settlement kind 与 result/error 的唯一合法组合。
func (command SettleWorkspaceAnalysisToolOperationCommand) Validate() error {
	if err := validateWorkspaceAnalysisToolIdentityKey(command.Identity, command.OperationKey); err != nil {
		return err
	}
	if err := command.Kind.Validate(); err != nil {
		return err
	}
	ids := append(workspaceAnalysisToolIdentityIDs(command.Identity), command.OperationKey.AnalysisRunID,
		command.OperationID, command.ReservationID, command.ToolCallID)
	if validateWorkspaceAnalysisModelQueryIDs(ids...) != nil ||
		!canonicalWorkspaceAnalysisSHA256(command.RequestHash) ||
		command.ExpectedRunVersion < 1 || command.ExpectedOperationVersion < 1 ||
		!canonicalWorkspaceAnalysisToolTime(command.CompletedAt) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool settlement command is invalid"))
	}

	switch command.Kind {
	case WorkspaceAnalysisToolSettlementSucceeded:
		if command.ErrorCode != "" || !validWorkspaceAnalysisToolResult(command.Result) {
			return workspaceAnalysisToolInvalid(errors.New("successful workspace analysis tool settlement is invalid"))
		}
		if validateWorkspaceAnalysisModelQueryIDs(append(ids, command.Result.ID)...) != nil {
			return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool settlement reuses its result id"))
		}
	case WorkspaceAnalysisToolSettlementReceiptInvalid:
		if command.Result != nil || command.ErrorCode != string(domain.WorkspaceAnalysisRunReceiptInvalid) {
			return workspaceAnalysisToolInvalid(errors.New("receipt-invalid workspace analysis tool settlement is invalid"))
		}
	case WorkspaceAnalysisToolSettlementFailed:
		if command.Result != nil || !canonicalStableErrorCode(command.ErrorCode) ||
			command.ErrorCode == string(domain.WorkspaceAnalysisRunReceiptInvalid) ||
			command.ErrorCode == string(domain.WorkspaceAnalysisRunResultUnknown) {
			return workspaceAnalysisToolInvalid(errors.New("failed workspace analysis tool settlement is invalid"))
		}
	case WorkspaceAnalysisToolSettlementUnknown:
		if command.Result != nil || command.ErrorCode != string(domain.WorkspaceAnalysisRunResultUnknown) {
			return workspaceAnalysisToolInvalid(errors.New("unknown workspace analysis tool settlement is invalid"))
		}
	}
	return nil
}

// AdvanceWorkspaceAnalysisToolOperationAttemptCommand 只推进 terminal replay 的 latest Attempt。
type AdvanceWorkspaceAnalysisToolOperationAttemptCommand struct {
	Identity                 WorkspaceAnalysisToolExecutionIdentity
	OperationKey             domain.WorkspaceAnalysisOperationKey
	OperationID              foundation.ID
	ToolCallID               foundation.ID
	ExpectedOperationVersion int64
}

// Validate 校验 terminal replay 的逻辑槽位、Call 与 CAS 版本。
func (command AdvanceWorkspaceAnalysisToolOperationAttemptCommand) Validate() error {
	if err := validateWorkspaceAnalysisToolIdentityKey(command.Identity, command.OperationKey); err != nil {
		return err
	}
	ids := append(workspaceAnalysisToolIdentityIDs(command.Identity), command.OperationKey.AnalysisRunID,
		command.OperationID, command.ToolCallID)
	if validateWorkspaceAnalysisModelQueryIDs(ids...) != nil || command.ExpectedOperationVersion < 1 {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool attempt advance command is invalid"))
	}
	return nil
}

// WorkspaceAnalysisToolClosureQuery 精确定位一次可恢复的 Agent durable closure。
type WorkspaceAnalysisToolClosureQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
	OperationKey  domain.WorkspaceAnalysisOperationKey
	OperationID   foundation.ID
	ReservationID foundation.ID
	ToolCallID    foundation.ID
	RequestHash   string
}

// Validate 校验 durable closure 的全部不可变身份和 canonical 请求哈希。
func (query WorkspaceAnalysisToolClosureQuery) Validate() error {
	if query.OperationKey.AnalysisRunID != query.AnalysisRunID ||
		validateWorkspaceAnalysisToolOperationKey(query.OperationKey) != nil ||
		validateWorkspaceAnalysisModelQueryIDs(query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID,
			query.OperationID, query.ReservationID, query.ToolCallID) != nil ||
		!canonicalWorkspaceAnalysisSHA256(query.RequestHash) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool closure query is invalid"))
	}
	return nil
}

// WorkspaceAnalysisToolClosure 是 durable verifier 返回的完整 Agent-owned 事实闭包。
type WorkspaceAnalysisToolClosure struct {
	Run         domain.WorkspaceAnalysisRun
	Operation   domain.WorkspaceAnalysisOperation
	Reservation domain.WorkspaceAnalysisBudgetReservation
}

// Validate 校验 durable closure 不含 PENDING 或部分预算事实。
func (closure WorkspaceAnalysisToolClosure) Validate() error {
	if domain.ValidateWorkspaceAnalysisRun(closure.Run) != nil ||
		domain.ValidateWorkspaceAnalysisOperation(closure.Operation) != nil ||
		closure.Operation.Status == domain.WorkspaceAnalysisOperationPending ||
		validateWorkspaceAnalysisToolOperationKey(closure.Operation.LogicalKey()) != nil ||
		domain.ValidateWorkspaceAnalysisBudgetReservationBinding(closure.Run, closure.Reservation, closure.Operation) != nil {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool closure is invalid"))
	}
	return nil
}

// ScopedWorkspaceAnalysisToolParticipant 在 caller-owned scope 中维护 Agent-owned Tool Operation 事实。
type ScopedWorkspaceAnalysisToolParticipant interface {
	PrepareWorkspaceAnalysisToolOperationScoped(
		context.Context,
		foundation.TransactionScope,
		PrepareWorkspaceAnalysisToolOperationCommand,
	) (WorkspaceAnalysisToolOperationSnapshot, error)

	LockWorkspaceAnalysisToolOperationByCallScoped(
		context.Context,
		foundation.TransactionScope,
		WorkspaceAnalysisToolCallLockQuery,
	) (WorkspaceAnalysisToolOperationSnapshot, bool, error)

	ReserveWorkspaceAnalysisToolOperationScoped(
		context.Context,
		foundation.TransactionScope,
		ReserveWorkspaceAnalysisToolOperationCommand,
	) (WorkspaceAnalysisToolOperationSnapshot, error)

	SettleWorkspaceAnalysisToolOperationScoped(
		context.Context,
		foundation.TransactionScope,
		SettleWorkspaceAnalysisToolOperationCommand,
	) (WorkspaceAnalysisToolOperationSnapshot, error)

	AdvanceWorkspaceAnalysisToolOperationAttemptScoped(
		context.Context,
		foundation.TransactionScope,
		AdvanceWorkspaceAnalysisToolOperationAttemptCommand,
	) (domain.WorkspaceAnalysisOperation, error)

	VerifyWorkspaceAnalysisToolClosureScoped(
		context.Context,
		foundation.TransactionScope,
		WorkspaceAnalysisToolClosureQuery,
	) (WorkspaceAnalysisToolClosure, bool, error)
}

// RecordWorkspaceAnalysisToolRefusalScopedCommand 记录一个不创建 Operation 的拒绝事实。
type RecordWorkspaceAnalysisToolRefusalScopedCommand struct {
	Identity     WorkspaceAnalysisToolExecutionIdentity
	OperationKey domain.WorkspaceAnalysisOperationKey
	RefusalID    foundation.ID
	AuditEventID foundation.ID
	ErrorCode    string
}

// Validate 校验拒绝事实与当前 Tool 槽位、Audit 身份和稳定错误码绑定。
func (command RecordWorkspaceAnalysisToolRefusalScopedCommand) Validate() error {
	if err := validateWorkspaceAnalysisToolIdentityKey(command.Identity, command.OperationKey); err != nil {
		return err
	}
	ids := append(workspaceAnalysisToolIdentityIDs(command.Identity), command.OperationKey.AnalysisRunID,
		command.RefusalID, command.AuditEventID)
	if validateWorkspaceAnalysisModelQueryIDs(ids...) != nil || !canonicalStableErrorCode(command.ErrorCode) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool refusal command is invalid"))
	}
	return nil
}

// WorkspaceAnalysisToolRefusalQuery 精确定位普通 replay 或 commit recovery 的拒绝事实。
type WorkspaceAnalysisToolRefusalQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
	NodeRunID     foundation.ID
	OperationKey  domain.WorkspaceAnalysisOperationKey
	RefusalID     foundation.ID
	AuditEventID  foundation.ID
	ErrorCode     string
}

// Validate 校验 durable refusal 查询的完整逻辑槽位和 Audit 绑定。
func (query WorkspaceAnalysisToolRefusalQuery) Validate() error {
	if query.OperationKey.AnalysisRunID != query.AnalysisRunID ||
		validateWorkspaceAnalysisToolOperationKey(query.OperationKey) != nil ||
		validateWorkspaceAnalysisModelQueryIDs(query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID,
			query.NodeRunID, query.RefusalID, query.AuditEventID) != nil ||
		!canonicalStableErrorCode(query.ErrorCode) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool refusal query is invalid"))
	}
	return nil
}

// WorkspaceAnalysisToolRefusal 是 Agent-owned 的拒绝与 Audit 关联事实。
type WorkspaceAnalysisToolRefusal struct {
	ID            foundation.ID
	WorkspaceID   foundation.ID
	AnalysisRunID foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	OperationKey  domain.WorkspaceAnalysisOperationKey
	AuditEventID  foundation.ID
	ErrorCode     string
	CreatedAt     time.Time
}

// Validate 校验拒绝投影可用于 exact replay 且时间为 UTC 微秒。
func (refusal WorkspaceAnalysisToolRefusal) Validate() error {
	query := WorkspaceAnalysisToolRefusalQuery{
		WorkspaceID: refusal.WorkspaceID, WorkflowRunID: refusal.WorkflowRunID, AnalysisRunID: refusal.AnalysisRunID,
		NodeRunID: refusal.NodeRunID, OperationKey: refusal.OperationKey, RefusalID: refusal.ID,
		AuditEventID: refusal.AuditEventID, ErrorCode: refusal.ErrorCode,
	}
	if query.Validate() != nil || !canonicalWorkspaceAnalysisToolTime(refusal.CreatedAt) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool refusal is invalid"))
	}
	return nil
}

// ScopedWorkspaceAnalysisToolRefusalStore 在 caller-owned scope 中记录或读取 Agent-owned 拒绝事实。
type ScopedWorkspaceAnalysisToolRefusalStore interface {
	RecordWorkspaceAnalysisToolRefusalScoped(
		context.Context,
		foundation.TransactionScope,
		RecordWorkspaceAnalysisToolRefusalScopedCommand,
	) (WorkspaceAnalysisToolRefusal, bool, error)

	LoadWorkspaceAnalysisToolRefusalExactScoped(
		context.Context,
		foundation.TransactionScope,
		WorkspaceAnalysisToolRefusalQuery,
	) (WorkspaceAnalysisToolRefusal, bool, error)
}

// WorkspaceAnalysisRunToolAuthorityQuery 按 Workspace 与 Workflow Run 读取 Analysis Run 身份。
type WorkspaceAnalysisRunToolAuthorityQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
}

// Validate 校验 Run authority 查询的两个 owner 身份。
func (query WorkspaceAnalysisRunToolAuthorityQuery) Validate() error {
	if validateWorkspaceAnalysisModelQueryIDs(query.WorkspaceID, query.WorkflowRunID) != nil {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis run tool authority query is invalid"))
	}
	return nil
}

// WorkspaceAnalysisRunToolAuthority 是 Tools 可见的最小 Analysis Run 身份投影。
type WorkspaceAnalysisRunToolAuthority struct {
	AnalysisRunID     foundation.ID
	WorkspaceID       foundation.ID
	WorkflowRunID     foundation.ID
	DefinitionVersion int64
}

// Validate 校验 Run authority 投影的 owner 身份完整且不复用。
func (authority WorkspaceAnalysisRunToolAuthority) Validate() error {
	if validateWorkspaceAnalysisModelQueryIDs(authority.AnalysisRunID, authority.WorkspaceID, authority.WorkflowRunID) != nil {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis run tool authority is invalid"))
	}
	return nil
}

// WorkspaceAnalysisSuccessfulToolAuthorityQuery 按稳定逻辑键读取成功 Tool Operation 闭包。
type WorkspaceAnalysisSuccessfulToolAuthorityQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	OperationKey  domain.WorkspaceAnalysisOperationKey
}

// Validate 校验成功 authority 查询只定位一个冻结 Tool 槽位。
func (query WorkspaceAnalysisSuccessfulToolAuthorityQuery) Validate() error {
	if validateWorkspaceAnalysisToolOperationKey(query.OperationKey) != nil ||
		validateWorkspaceAnalysisModelQueryIDs(query.WorkspaceID, query.WorkflowRunID, query.OperationKey.AnalysisRunID) != nil {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis successful tool authority query is invalid"))
	}
	return nil
}

// WorkspaceAnalysisSuccessfulToolAuthority 是成功 Tool Operation 的最小 receipt 绑定投影。
type WorkspaceAnalysisSuccessfulToolAuthority struct {
	AnalysisRunID foundation.ID
	OperationID   foundation.ID
	ReservationID foundation.ID
	ToolCallID    foundation.ID
	ResultID      foundation.ID
	ResultHash    string
}

// Validate 校验成功 authority 的 Operation、预算、Call 与 receipt 身份闭合。
func (authority WorkspaceAnalysisSuccessfulToolAuthority) Validate() error {
	if validateWorkspaceAnalysisModelQueryIDs(authority.AnalysisRunID, authority.OperationID, authority.ReservationID,
		authority.ToolCallID, authority.ResultID) != nil || !canonicalWorkspaceAnalysisSHA256(authority.ResultHash) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis successful tool authority is invalid"))
	}
	return nil
}

// WorkspaceAnalysisSourceReadCountQuery 定位一个 Run 的全部 SOURCE_READ 逻辑槽位。
type WorkspaceAnalysisSourceReadCountQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
}

// Validate 校验 SOURCE_READ 计数查询的 owner 身份。
func (query WorkspaceAnalysisSourceReadCountQuery) Validate() error {
	if validateWorkspaceAnalysisModelQueryIDs(query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID) != nil {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis source read count query is invalid"))
	}
	return nil
}

// WorkspaceAnalysisToolCandidateAuthorityQuery 定位一个 Workspace-owned Candidate 闭包。
type WorkspaceAnalysisToolCandidateAuthorityQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	CandidateID   foundation.ID
}

// Validate 校验 Candidate authority 查询的 owner 与候选身份。
func (query WorkspaceAnalysisToolCandidateAuthorityQuery) Validate() error {
	if validateWorkspaceAnalysisModelQueryIDs(query.WorkspaceID, query.WorkflowRunID, query.CandidateID) != nil {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool candidate authority query is invalid"))
	}
	return nil
}

// WorkspaceAnalysisToolCandidateAuthority 是不含正文的 Candidate hash 与有界 Citation 投影。
type WorkspaceAnalysisToolCandidateAuthority struct {
	CandidateID   foundation.ID
	AnalysisRunID foundation.ID
	CandidateHash string
	CitationRefs  []string
	SchemaVersion int64
}

// Validate 校验 Candidate 投影的身份、哈希与 E1..E3 引用集合。
func (authority WorkspaceAnalysisToolCandidateAuthority) Validate() error {
	if validateWorkspaceAnalysisModelQueryIDs(authority.CandidateID, authority.AnalysisRunID) != nil ||
		!canonicalWorkspaceAnalysisSHA256(authority.CandidateHash) ||
		!validWorkspaceAnalysisToolCitationRefsVersion(authority.CitationRefs, authority.SchemaVersion) {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool candidate authority is invalid"))
	}
	return nil
}

// ScopedWorkspaceAnalysisToolAuthorityReader 在 caller-owned scope 中读取 Agent-verified 最小 authority 投影。
type ScopedWorkspaceAnalysisToolAuthorityReader interface {
	LoadWorkspaceAnalysisRunToolAuthorityScoped(
		context.Context,
		foundation.TransactionScope,
		WorkspaceAnalysisRunToolAuthorityQuery,
	) (WorkspaceAnalysisRunToolAuthority, bool, error)

	LoadWorkspaceAnalysisSuccessfulToolAuthorityScoped(
		context.Context,
		foundation.TransactionScope,
		WorkspaceAnalysisSuccessfulToolAuthorityQuery,
	) (WorkspaceAnalysisSuccessfulToolAuthority, bool, error)

	CountWorkspaceAnalysisSourceReadOperationsScoped(
		context.Context,
		foundation.TransactionScope,
		WorkspaceAnalysisSourceReadCountQuery,
	) (int, error)

	LoadWorkspaceAnalysisToolCandidateAuthorityScoped(
		context.Context,
		foundation.TransactionScope,
		WorkspaceAnalysisToolCandidateAuthorityQuery,
	) (WorkspaceAnalysisToolCandidateAuthority, bool, error)
}

func validateWorkspaceAnalysisToolIdentityKey(
	identity WorkspaceAnalysisToolExecutionIdentity,
	key domain.WorkspaceAnalysisOperationKey,
) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if err := validateWorkspaceAnalysisToolOperationKey(key); err != nil {
		return err
	}
	if key.NodeKey != identity.NodeKey {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis tool operation does not match the execution node"))
	}
	return nil
}

func validateWorkspaceAnalysisToolOperationKey(key domain.WorkspaceAnalysisOperationKey) error {
	contract, err := domain.WorkspaceAnalysisOperationContractForKey(key)
	if err != nil {
		return err
	}
	if contract.CallKind != domain.WorkspaceAnalysisOperationCallTool {
		return workspaceAnalysisToolInvalid(errors.New("workspace analysis operation is not a tool slot"))
	}
	return nil
}

func workspaceAnalysisToolIdentityIDs(identity WorkspaceAnalysisToolExecutionIdentity) []foundation.ID {
	return []foundation.ID{
		identity.WorkspaceID,
		identity.DefinitionID,
		identity.WorkflowRunID,
		identity.NodeRunID,
		identity.NodeAttemptID,
	}
}

func validWorkspaceAnalysisToolResult(result *domain.WorkspaceAnalysisOperationResultRef) bool {
	return result != nil && result.Kind == domain.WorkspaceAnalysisOperationResultToolReceipt &&
		canonicalApplicationID(result.ID) && canonicalWorkspaceAnalysisSHA256(result.Hash)
}

func validWorkspaceAnalysisToolCitationRefs(references []string) bool {
	return validWorkspaceAnalysisToolCitationRefsVersion(references, 1)
}

func validWorkspaceAnalysisToolCitationRefsVersion(references []string, version int64) bool {
	if version == 2 {
		if len(references) < 1 || len(references) > domain.WorkspaceAnalysisV2MaxSourceReads {
			return false
		}
		seen := map[string]bool{}
		for _, ref := range references {
			if !domain.ValidWorkspaceAnalysisV2EvidenceRef(ref) || seen[ref] {
				return false
			}
			seen[ref] = true
		}
		return true
	}
	if version != 0 && version != 1 {
		return false
	}
	if len(references) < 1 || len(references) > domain.WorkspaceAnalysisV1MaxSourceReads {
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

func canonicalWorkspaceAnalysisToolTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond()%1_000 == 0
}

func workspaceAnalysisToolInvalid(cause error) error {
	return workspaceAnalysisModelError(cause)
}
