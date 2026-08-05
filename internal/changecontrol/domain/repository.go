package domain

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Repository 持有 Change Control 的事务和一致性边界。
type Repository interface {
	CreateProposal(context.Context, Proposal) (Proposal, error)
	GetProposal(context.Context, foundation.ID) (Proposal, error)
	Approve(context.Context, Approval) (Approval, error)
	MarkNeedsRevision(context.Context, foundation.ID, time.Time) error
}

// ProposalListItem 是 Proposal 列表的有界只读摘要，不携带正文内容。
type ProposalListItem struct {
	ProposalID  foundation.ID
	WorkspaceID foundation.ID
	Type        ProposalType
	Status      ProposalStatus
	Target      string
	RiskLevel   ProposalRiskLevel
	Risk        string
	RevisionID  foundation.ID
	ChangeHash  string
	// Approval 是最新 Revision 的持久审批决定；未审批时为 nil。
	Approval *Approval
	// WorkflowRunID 是 file_patch 批准后持久绑定的写回 Workflow；未派发时为 nil。
	WorkflowRunID *foundation.ID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ProposalListQuery 描述 Proposal 列表的 Workspace 作用域、筛选和稳定分页边界。
type ProposalListQuery struct {
	WorkspaceID  foundation.ID
	Status       ProposalStatus
	Type         ProposalType
	RiskLevel    ProposalRiskLevel
	CreatedAfter *time.Time
	CursorTime   *time.Time
	CursorID     foundation.ID
	Limit        int
}

// ProposalListRepository 提供 Workspace 绑定的稳定摘要分页查询。
type ProposalListRepository interface {
	ListProposals(context.Context, ProposalListQuery) ([]ProposalListItem, bool, error)
}

// KnowledgeChangeProposalRepository 是支持持久化 `knowledge_change` Proposal 的可选扩展。
// 在 typed Proposal 迁移落地前，Adapter 可以不实现该接口并返回 fail-closed。
type KnowledgeChangeProposalRepository interface {
	CreateKnowledgeChangeProposal(context.Context, Proposal) (Proposal, error)
}

// PublishArtifactProposalRepository persists frozen Artifact publication proposals.
// Missing implementations must fail closed because no execution capability exists yet.
type PublishArtifactProposalRepository interface {
	CreatePublishArtifactProposal(context.Context, Proposal) (Proposal, error)
}

// DownstreamUpdateProposalRepository 持久化 approval-only 的 Impact 下游更新 Proposal。
type DownstreamUpdateProposalRepository interface {
	CreateDownstreamUpdateProposal(context.Context, Proposal) (Proposal, error)
}

// RestoreDocumentProposalRepository persists typed restore proposals with Document binding checks.
type RestoreDocumentProposalRepository interface {
	CreateRestoreDocumentProposal(context.Context, Proposal) (Proposal, error)
}

// WritebackRepository 持有 Safe Writeback Durable Operation 的幂等、状态和发布事务边界。
// Adapter 必须在数据库内再次执行领域校验，不能只依赖调用方传入的状态或版本。
type WritebackRepository interface {
	Repository
	CreateWritebackExecution(context.Context, CreateWriteback) (WritebackExecution, error)
	GetWritebackExecution(context.Context, foundation.ID) (WritebackExecution, error)
	CheckpointWritebackExecution(context.Context, CheckpointWriteback) (WritebackExecution, error)
	PublishWriteback(context.Context, PublishWriteback) (PublishWritebackResult, error)
}

// WritebackSagaRepository 扩展旧 WritebackRepository，提供 M5-04D 原子 Begin、lease guard 与 cleanup finalize。
// 单独扩展接口可保持已有 Adapter/Fake 的源码兼容。
type WritebackSagaRepository interface {
	WritebackRepository
	BeginWriteback(context.Context, BeginWriteback) (WritebackExecution, error)
	ValidateWritebackLease(context.Context, foundation.ID, string) error
	FinalizeWritebackCleanup(context.Context, foundation.ID, int64, time.Time) (WritebackExecution, error)
}

// WritebackExecutionLookup 提供 Bootstrap 在签发 Credential 前使用的 exact 恢复查询。
// 未找到稳定键是正常分支；实现必须以 found=false 返回，不能退化为其他唯一键搜索。
type WritebackExecutionLookup interface {
	FindWritebackExecutionByKey(context.Context, foundation.ID, string) (execution WritebackExecution, found bool, err error)
}
