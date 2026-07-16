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
