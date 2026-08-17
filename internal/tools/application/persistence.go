package application

import (
	"context"
	"encoding/json"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	// MaxToolCallTimelineLimit 是一次 Tool Call 时间线查询允许返回的最大记录数。
	MaxToolCallTimelineLimit = 500
	// MaxToolStaleRecoveryLimit 是一次崩溃恢复扫描允许处理的最大 STARTED 记录数。
	MaxToolStaleRecoveryLimit = 100
)

// WorkflowToolPolicy 是从持久 Workflow Definition、Run、Node 和 Attempt 解析出的可信策略快照。
type WorkflowToolPolicy struct {
	Identity       domain.TrustedExecutionIdentity
	WorkflowKey    string
	NodeKind       string
	Permissions    []capability.Capability
	AllowedTools   []domain.ToolRef
	AttemptLeaseTo time.Time
}

// WorkflowPolicyReader 从服务端持久事实解析当前 Tool 执行策略。
type WorkflowPolicyReader interface {
	// ResolveToolPolicy 校验完整执行身份、活动租约和 Definition graph，并返回不可变策略副本。
	ResolveToolPolicy(context.Context, domain.TrustedExecutionIdentity) (WorkflowToolPolicy, error)
}

// RecordRefusedCommand 保存一次未进入 Executor 的受限拒绝事实。
type RecordRefusedCommand struct {
	Identity domain.TrustedExecutionIdentity
	Call     domain.ToolCall
}

// StartCallCommand 在 Executor 前登记 STARTED Tool Call，并携带 Registry 冻结的 Workflow binding。
type StartCallCommand struct {
	Identity         domain.TrustedExecutionIdentity
	Call             domain.ToolCall
	AllowedWorkflows []domain.WorkflowBinding
}

// TrustedWriteStartCommand 在 Safe Writeback 唯一副作用 seam 前登记带预期 writeback receipt 的 STARTED。
type TrustedWriteStartCommand struct {
	Identity         domain.TrustedExecutionIdentity
	Call             domain.ToolCall
	AllowedWorkflows []domain.WorkflowBinding
}

// TrustedWriteLoadCommand 使用当前 Attempt 身份读取既有 trusted write Call，用于 response-loss receipt reconciliation。
type TrustedWriteLoadCommand struct {
	Identity         domain.TrustedExecutionIdentity
	Tool             domain.ToolRef
	Capability       capability.Capability
	AllowedWorkflows []domain.WorkflowBinding
	IdempotencyKey   string
}

// StartCallDisposition 表示 STARTED Tool Call 是本次新建还是既有事实重放。
type StartCallDisposition string

const (
	// StartCallCreated 表示本次事务创建了 STARTED，调用方获得唯一 Executor 资格。
	StartCallCreated StartCallDisposition = "CREATED"
	// StartCallReplayed 表示已存在同绑定事实，调用方不得再次执行 Executor。
	StartCallReplayed StartCallDisposition = "REPLAYED"
)

// StartCallResult 返回持久 Tool Call 及本次调用的执行资格。
type StartCallResult struct {
	Call        domain.ToolCall
	Disposition StartCallDisposition
}

// FinalizeCallCommand 使用 expected version 将 STARTED 归约为 SUCCEEDED 或 FAILED。
type FinalizeCallCommand struct {
	ExpectedVersion int64
	Call            domain.ToolCall
}

// FinalizeCallWithReceiptCommand 原子完成一个 opt-in Tool Call 及其 canonical receipt。
type FinalizeCallWithReceiptCommand struct {
	ExpectedVersion int64
	Identity        domain.TrustedExecutionIdentity
	Call            domain.ToolCall
	Definition      domain.Definition
	ReceiptID       foundation.ID
	Output          json.RawMessage
	PrivateBinding  *ExecutorPrivateBinding
}

// LoadResultReceiptCommand 使用成功 Call 与冻结 Definition 精确读取 canonical receipt。
type LoadResultReceiptCommand struct {
	Call       domain.ToolCall
	Definition domain.Definition
}

// MarkUnknownCommand 使用 expected version 将无法证明结果的 STARTED 归约为 UNKNOWN。
type MarkUnknownCommand struct {
	ExpectedVersion int64
	Call            domain.ToolCall
}

// ToolCallMutationResult 返回终态写入结果及是否为响应丢失后的幂等重放。
type ToolCallMutationResult struct {
	Call     domain.ToolCall
	Replayed bool
}

// ResultReceiptMutationResult 返回同一事务持久化的 Call 与 receipt；重放时两者均来自数据库。
type ResultReceiptMutationResult struct {
	Call        domain.ToolCall
	Receipt     domain.ResultReceipt
	OperationID foundation.ID `json:"-"`
	Replayed    bool
}

// FinalizeCallWithReceiptFailureCommand 原子关闭一个无法形成 canonical receipt 的 Workspace Analysis Tool Call。
// Call 必须是包含安全响应摘要的 SUCCEEDED；Failure 只保存 observation 的哈希与长度，不携带被拒文档。
type FinalizeCallWithReceiptFailureCommand struct {
	ExpectedVersion int64
	Identity        domain.TrustedExecutionIdentity
	Call            domain.ToolCall
	Definition      domain.Definition
	Failure         domain.ResultReceiptFailureDraft
}

// ResultReceiptFailureMutationResult 返回同一事务持久化的成功 Call 与 immutable receipt failure。
// 重放时 Call、Failure 和 OperationID 都必须来自数据库中的 exact 事实。
type ResultReceiptFailureMutationResult struct {
	Call        domain.ToolCall
	Failure     domain.ResultReceiptFailure
	OperationID foundation.ID `json:"-"`
	Replayed    bool
}

// ToolCallTimelineQuery 按 Workspace 和 Workflow Run 查询稳定 Tool Call 时间线。
type ToolCallTimelineQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	Limit         int
}

// ToolCallRepository 隐藏 Tool Call 的 PostgreSQL 幂等、CAS、恢复和时间线实现。
type ToolCallRepository interface {
	// RecordRefused 保存不占执行幂等键的 REFUSED 事实；同逻辑拒绝可安全重放。
	RecordRefused(context.Context, RecordRefusedCommand) (ToolCallMutationResult, error)
	// StartCall 在同一事务复核持久 Workflow policy 并只向 CREATED 返回 Executor 资格。
	StartCall(context.Context, StartCallCommand) (StartCallResult, error)
	// FinalizeCall 使用 version CAS 将 STARTED 归约为唯一成功或已证明失败终态。
	FinalizeCall(context.Context, FinalizeCallCommand) (ToolCallMutationResult, error)
	// MarkUnknown 使用 version CAS 记录崩溃、超时或响应丢失后的未知副作用结果。
	MarkUnknown(context.Context, MarkUnknownCommand) (ToolCallMutationResult, error)
	// RecoverStaleStarted 使用数据库时间、SKIP LOCKED 与单事务 CAS 将失租 STARTED 归约为 UNKNOWN。
	RecoverStaleStarted(context.Context, int) ([]domain.ToolCall, error)
	// ListTimeline 返回按 started_at、call_no、id 稳定排序且经过领域校验的受限时间线。
	ListTimeline(context.Context, ToolCallTimelineQuery) ([]domain.ToolCall, error)
}

// ResultReceiptRepository 是仅供 ResultPersistenceCanonical Tool 使用的可选持久化扩展。
// 实现必须在同一事务中完成 Call CAS、receipt 插入及所属 Workspace Analysis 操作的预算结算和成功归约。
type ResultReceiptRepository interface {
	// FinalizeCallWithReceipt 原子持久化成功 Call 与 immutable receipt；不确定提交不得返回未持久输出。
	FinalizeCallWithReceipt(context.Context, FinalizeCallWithReceiptCommand) (ResultReceiptMutationResult, error)
	// LoadResultReceipt 按成功 Call 与冻结 Definition 精确读取并验证 persisted receipt。
	LoadResultReceipt(context.Context, LoadResultReceiptCommand) (domain.ResultReceipt, error)
}

// ResultReceiptFailureRepository 是仅供 canonical Workspace Analysis Tool 的回执形成前失败使用的可选扩展。
// 实现必须在同一事务中完成成功 Call CAS、失败事实插入、预算结算和 Operation 失败归约。
type ResultReceiptFailureRepository interface {
	// FinalizeCallWithReceiptFailure 原子持久化 SUCCEEDED Call 与 immutable receipt failure。
	// 提交结果不确定时不得返回未确认终态，也不得由调用方另行把 Call 改写为 FAILED/UNKNOWN。
	FinalizeCallWithReceiptFailure(context.Context, FinalizeCallWithReceiptFailureCommand) (ResultReceiptFailureMutationResult, error)
}

// WorkspaceAnalysisToolAuthorizationDisposition 是逻辑 Tool Operation 的唯一恢复动作。
type WorkspaceAnalysisToolAuthorizationDisposition string

const (
	// WorkspaceAnalysisToolAuthorizationCreated 表示本事务首次原子建立了 Operation、预算和 STARTED Call。
	WorkspaceAnalysisToolAuthorizationCreated WorkspaceAnalysisToolAuthorizationDisposition = "CREATED"
	// WorkspaceAnalysisToolAuthorizationReconcile 表示同一实际 Call 仍在执行或等待失租归约，禁止再次进入 Executor。
	WorkspaceAnalysisToolAuthorizationReconcile WorkspaceAnalysisToolAuthorizationDisposition = "RECONCILE_EXACT_CALL"
	// WorkspaceAnalysisToolAuthorizationReuseResult 表示成功 Operation 只能重放已持久 canonical receipt。
	WorkspaceAnalysisToolAuthorizationReuseResult WorkspaceAnalysisToolAuthorizationDisposition = "REUSE_RESULT"
	// WorkspaceAnalysisToolAuthorizationReplayFailure 表示失败 Operation 只能重放稳定失败。
	WorkspaceAnalysisToolAuthorizationReplayFailure WorkspaceAnalysisToolAuthorizationDisposition = "REPLAY_FAILURE"
	// WorkspaceAnalysisToolAuthorizationTerminateUnknown 表示 Operation 已归约为 Unknown，禁止重新执行。
	WorkspaceAnalysisToolAuthorizationTerminateUnknown WorkspaceAnalysisToolAuthorizationDisposition = "TERMINATE_UNKNOWN"
)

// AuthorizeWorkspaceAnalysisToolCallCommand 原子授权一个服务端选择的 Workspace Analysis Tool Operation。
// OperationID、ReservationID 与 Call.ID 只供首次创建；逻辑键已存在时实现必须忽略这些候选 ID 并返回持久事实。
type AuthorizeWorkspaceAnalysisToolCallCommand struct {
	Identity      domain.TrustedExecutionIdentity
	OperationKey  agentdomain.WorkspaceAnalysisOperationKey
	OperationID   foundation.ID
	ReservationID foundation.ID
	Call          domain.ToolCall
	Definition    domain.Definition
}

// WorkspaceAnalysisToolAuthorizationResult 返回实际持久 Call 及其 Operation/Reservation 身份。
type WorkspaceAnalysisToolAuthorizationResult struct {
	Call          domain.ToolCall
	OperationID   foundation.ID
	ReservationID foundation.ID
	Disposition   WorkspaceAnalysisToolAuthorizationDisposition
	// ReceiptFailure 仅在 REPLAY_FAILURE 对应 SUCCEEDED Call 的回执校验失败时存在。
	ReceiptFailure *domain.ResultReceiptFailure `json:"-"`
}

// RecordWorkspaceAnalysisToolRefusalCommand records an immutable, pre-executor
// Workspace Analysis refusal. It deliberately has no Tool Call, request hash,
// receipt, or budget reservation binding.
type RecordWorkspaceAnalysisToolRefusalCommand struct {
	RefusalID    foundation.ID
	Identity     domain.TrustedExecutionIdentity
	OperationKey agentdomain.WorkspaceAnalysisOperationKey
	ErrorCode    string
}

// WorkspaceAnalysisToolRefusalResult is the exact durable refusal fact.
type WorkspaceAnalysisToolRefusalResult struct {
	RefusalID foundation.ID
	ErrorCode string
	Replayed  bool
}

// FinalizeWorkspaceAnalysisToolCallCommand 原子关闭一个未产生 canonical receipt 的失败或 Unknown Tool Call。
type FinalizeWorkspaceAnalysisToolCallCommand struct {
	ExpectedVersion int64
	Identity        domain.TrustedExecutionIdentity
	Call            domain.ToolCall
}

// WorkspaceAnalysisToolOperationRepository 实现 Workspace Analysis Tool 调用的授权和非成功关闭事务。
// 正常成功与回执失败分别由 ResultReceiptRepository、ResultReceiptFailureRepository 关闭；
// 四条路径都必须保持同一锁序并闭合预算与 Operation。
type WorkspaceAnalysisToolOperationRepository interface {
	// AuthorizeWorkspaceAnalysisToolCall 在 Executor 前原子创建或恢复 Operation、Reservation 与 STARTED Call。
	AuthorizeWorkspaceAnalysisToolCall(context.Context, AuthorizeWorkspaceAnalysisToolCallCommand) (WorkspaceAnalysisToolAuthorizationResult, error)
	// FinalizeWorkspaceAnalysisToolCall 原子归约 FAILED/UNKNOWN Call、预算和 Operation。
	FinalizeWorkspaceAnalysisToolCall(context.Context, FinalizeWorkspaceAnalysisToolCallCommand) (ToolCallMutationResult, error)
}

// WorkspaceAnalysisToolRefusalRepository persists only deterministic security
// refusals that occur before any executor, Tool Call, or budget reservation.
// Stale lease, cancellation, deadline, budget, execution, and receipt paths
// must not use this interface.
type WorkspaceAnalysisToolRefusalRepository interface {
	RecordWorkspaceAnalysisToolRefusal(context.Context, RecordWorkspaceAnalysisToolRefusalCommand) (WorkspaceAnalysisToolRefusalResult, error)
}

// TrustedWriteCallRepository 扩展 Tool Call 持久化边界，只供既有 Safe Writeback 审计桥使用。
type TrustedWriteCallRepository interface {
	ToolCallRepository
	// StartTrustedWriteCall 校验当前 Attempt 资格，但允许重放历史 Attempt 创建的同一 writeback Call。
	StartTrustedWriteCall(context.Context, TrustedWriteStartCommand) (StartCallResult, error)
	// LoadTrustedWriteCall 先验证当前 Attempt 资格，再读取历史 receipt 绑定；不创建或执行副作用。
	LoadTrustedWriteCall(context.Context, TrustedWriteLoadCommand) (domain.ToolCall, error)
}
