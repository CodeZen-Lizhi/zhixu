package candidateconfirm

import (
	"context"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ScopedKnowledgeProposalPort is the Change Control capability required by
// Candidate Confirm. Implementations participate in the caller-owned live
// transaction and must not begin, commit, roll back, or fall back to a root DB.
type ScopedKnowledgeProposalPort interface {
	CreateKnowledgeChangeProposalScoped(
		ctx context.Context,
		scope foundation.TransactionScope,
		proposal changecontroldomain.Proposal,
	) (changecontroldomain.Proposal, error)

	GetInitialKnowledgeChangeProposalScoped(
		ctx context.Context,
		scope foundation.TransactionScope,
		workspaceID foundation.ID,
		proposalID foundation.ID,
	) (changecontroldomain.Proposal, error)
}
