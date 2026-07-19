package application

import (
	"context"
	"time"

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

// TrustedWriteCallRepository 扩展 Tool Call 持久化边界，只供既有 Safe Writeback 审计桥使用。
type TrustedWriteCallRepository interface {
	ToolCallRepository
	// StartTrustedWriteCall 校验当前 Attempt 资格，但允许重放历史 Attempt 创建的同一 writeback Call。
	StartTrustedWriteCall(context.Context, TrustedWriteStartCommand) (StartCallResult, error)
	// LoadTrustedWriteCall 先验证当前 Attempt 资格，再读取历史 receipt 绑定；不创建或执行副作用。
	LoadTrustedWriteCall(context.Context, TrustedWriteLoadCommand) (domain.ToolCall, error)
}
