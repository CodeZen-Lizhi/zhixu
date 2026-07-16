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

// WritebackRepository 持有 Safe Writeback Durable Operation 的幂等、状态和发布事务边界。
// Adapter 必须在数据库内再次执行领域校验，不能只依赖调用方传入的状态或版本。
type WritebackRepository interface {
	Repository
	CreateWritebackExecution(context.Context, CreateWriteback) (WritebackExecution, error)
	GetWritebackExecution(context.Context, foundation.ID) (WritebackExecution, error)
	CheckpointWritebackExecution(context.Context, CheckpointWriteback) (WritebackExecution, error)
	PublishWriteback(context.Context, PublishWriteback) (PublishWritebackResult, error)
}
