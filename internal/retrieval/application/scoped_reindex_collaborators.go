package application

import (
	"context"
	"time"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
)

// ReindexOutboxClaimRequest 描述 Retrieval 从 Workflow 领取 Reindex Outbox 的稳定消费者身份。
type ReindexOutboxClaimRequest struct {
	ConsumerName string
}

// ReindexOutboxFact 是 Workflow 在 caller-owned scope 内锁定的最小 Outbox 投影。
// WorkflowRunID 保留 nullable 形状，使 Retrieval 能对损坏的历史事实 fail closed。
type ReindexOutboxFact struct {
	EventID       foundation.ID
	WorkspaceID   foundation.ID
	WorkflowRunID *foundation.ID
	Payload       string
}

// ScopedReindexOutbox 是 Retrieval 派发流程所需的 Workflow owner 能力。
// Claim 与 Publish 必须使用同一个 live scope，且实现不得管理事务生命周期。
type ScopedReindexOutbox interface {
	ClaimReindexOutboxScoped(context.Context, foundation.TransactionScope, ReindexOutboxClaimRequest) (ReindexOutboxFact, bool, error)
	PublishReindexOutboxScoped(context.Context, foundation.TransactionScope, foundation.ID) error
}

// ReindexBindingFacts 是 Change Control 对执行、Proposal 与不可变 Commit 映射的 typed 投影。
type ReindexBindingFacts struct {
	Execution       reindexcontract.Binding
	Commit          reindexcontract.Binding
	ExecutionStatus changecontroldomain.WritebackStatus
	ProposalStatus  changecontroldomain.ProposalStatus
}

// ScopedReindexBindingVerifier 在 caller-owned scope 内验证 Reindex 请求绑定。
// 实现只读取 Change Control owner 事实，不得开始、提交或回滚事务。
type ScopedReindexBindingVerifier interface {
	VerifyReindexBindingScoped(context.Context, foundation.TransactionScope, reindexcontract.RequestV1) (ReindexBindingFacts, error)
}

// ReindexCompletionDisposition 表示已锁定 Change Control 完成事实的处理分支。
type ReindexCompletionDisposition string

const (
	// ReindexCompletionCurrent 表示 Proposal 与 Writeback 仍处于 verifying，可执行首次完成。
	ReindexCompletionCurrent ReindexCompletionDisposition = "current"
	// ReindexCompletionReplay 表示两个 owner 事实均已完成，只允许精确重放。
	ReindexCompletionReplay ReindexCompletionDisposition = "replay"
	// ReindexCompletionStale 表示 owner 生命周期不再允许首次完成或精确重放。
	ReindexCompletionStale ReindexCompletionDisposition = "stale"
)

// ReindexCompletionLockRequest 绑定 Retrieval 在取得 Workspace 锁后要锁定的 Writeback 身份。
type ReindexCompletionLockRequest struct {
	WorkspaceID          foundation.ID
	WritebackExecutionID foundation.ID
}

// ReindexCompletionFacts 是按 Proposal -> Writeback Execution 顺序锁定后的不可变事实。
type ReindexCompletionFacts struct {
	Disposition ReindexCompletionDisposition

	WorkspaceID           foundation.ID
	ProposalID            foundation.ID
	ProposalWorkflowRunID *foundation.ID
	ProposalStatus        changecontroldomain.ProposalStatus
	ProposalVersion       int64

	WritebackExecutionID foundation.ID
	WorkflowRunID        foundation.ID
	NodeRunID            foundation.ID
	RevisionID           foundation.ID
	ApprovalID           foundation.ID
	TargetPath           string
	ResultHash           string
	GitCommit            string
	ExecutionStatus      changecontroldomain.WritebackStatus
	CleanupCompletedAt   *time.Time
	ExecutionVersion     int64
}

// ReindexCompletionTransition 是 Retrieval 使用同一数据库时间完成两个 Change Control CAS 的命令。
type ReindexCompletionTransition struct {
	WorkspaceID              foundation.ID
	ProposalID               foundation.ID
	ExpectedProposalVersion  int64
	WritebackExecutionID     foundation.ID
	ExpectedExecutionVersion int64
	CompletedAt              time.Time
}

// ScopedReindexCompletionLocker 按 Proposal -> Writeback Execution 顺序锁定完成事实。
type ScopedReindexCompletionLocker interface {
	LockReindexCompletionScoped(context.Context, foundation.TransactionScope, ReindexCompletionLockRequest) (ReindexCompletionFacts, error)
}

// ScopedReindexCompletionCompleter 在 caller-owned scope 内完成 Writeback 与 Proposal CAS。
type ScopedReindexCompletionCompleter interface {
	CompleteReindexScoped(context.Context, foundation.TransactionScope, ReindexCompletionTransition) error
}

// ScopedReindexCompletion 组合 Retrieval 原子完成所需的两阶段 Change Control owner 能力。
type ScopedReindexCompletion interface {
	ScopedReindexCompletionLocker
	ScopedReindexCompletionCompleter
}
