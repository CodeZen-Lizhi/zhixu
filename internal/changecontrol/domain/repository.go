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

// KnowledgeChangeProposalRepository 是支持持久化 `knowledge_change` Proposal 的可选扩展。
// 在 typed Proposal 迁移落地前，Adapter 可以不实现该接口并返回 fail-closed。
type KnowledgeChangeProposalRepository interface {
	CreateKnowledgeChangeProposal(context.Context, Proposal) (Proposal, error)
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
