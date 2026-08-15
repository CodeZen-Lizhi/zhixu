package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ListProposalRevisions returns a bounded newest-first historical projection.
func (s *Service) ListProposalRevisions(ctx context.Context, query domain.ProposalRevisionHistoryQuery) ([]domain.ProposalRevisionHistoryItem, bool, error) {
	if err := domain.ValidateProposalRevisionHistoryQuery(query); err != nil {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_HISTORY_QUERY_INVALID", false, err)
	}
	repository, ok := s.repo.(domain.ProposalRevisionHistoryRepository)
	if !ok {
		return nil, false, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_REVISION_REPOSITORY_UNAVAILABLE", true, errors.New("proposal revision history repository is unavailable"))
	}
	return repository.ListProposalRevisions(ctx, query)
}

// GetProposalRevision returns one exact immutable Revision and its historical
// Approval/Workflow facts without changing the current Proposal projection.
func (s *Service) GetProposalRevision(ctx context.Context, proposalID, revisionID foundation.ID) (domain.ProposalRevisionHistoryDetail, error) {
	if proposalID == "" || revisionID == "" {
		return domain.ProposalRevisionHistoryDetail{}, foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_HISTORY_QUERY_INVALID", false, domain.ErrProposalRevisionHistoryQueryInvalid)
	}
	repository, ok := s.repo.(domain.ProposalRevisionHistoryRepository)
	if !ok {
		return domain.ProposalRevisionHistoryDetail{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "PROPOSAL_REVISION_REPOSITORY_UNAVAILABLE", true, errors.New("proposal revision history repository is unavailable"))
	}
	return repository.GetProposalRevision(ctx, proposalID, revisionID)
}
