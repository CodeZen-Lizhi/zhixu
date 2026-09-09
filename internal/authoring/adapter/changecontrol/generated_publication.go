package changecontrol

import (
	"context"
	"errors"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// GeneratedPublicationRetirer exposes the narrow trusted retirement capability to Authoring.
type GeneratedPublicationRetirer struct {
	repository changecontroldomain.GeneratedPublicationRetirementRepository
}

var _ authoringapp.GeneratedProposalRetirer = (*GeneratedPublicationRetirer)(nil)

// NewGeneratedPublicationRetirer uses the same Pool-backed repository as Authoring.
func NewGeneratedPublicationRetirer(repository changecontroldomain.GeneratedPublicationRetirementRepository) (*GeneratedPublicationRetirer, error) {
	if repository == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "AUTHORING_GENERATED_RETIRER_UNAVAILABLE", false, errors.New("generated publication retirer is unavailable"))
	}
	return &GeneratedPublicationRetirer{repository: repository}, nil
}

// RetireGeneratedProposalScoped preserves the caller's scope and does not construct an Approval.
func (retirer *GeneratedPublicationRetirer) RetireGeneratedProposalScoped(ctx context.Context, scope foundation.TransactionScope, request authoringapp.GeneratedProposalRetirement) error {
	if retirer == nil || retirer.repository == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "AUTHORING_GENERATED_RETIRER_UNAVAILABLE", false, errors.New("generated publication retirer is unavailable"))
	}
	err := retirer.repository.RetireGeneratedPublicationScoped(ctx, scope, changecontroldomain.GeneratedPublicationRetirement{
		WorkspaceID: request.WorkspaceID, ProposalID: request.ProposalID, ProposalRevisionID: request.ProposalRevisionID,
		ExpectedProposalVersion: request.ExpectedProposalVersion, ProposalIdempotencyKey: request.ProposalIdempotencyKey,
		TargetPath: request.TargetPath, TargetMode: request.TargetMode, BaseVersion: request.BaseVersion,
		ContentHash: request.ContentHash, RetiredAt: request.RetiredAt,
	})
	var classified *foundation.Error
	if errors.As(err, &classified) {
		switch classified.Code {
		case changecontroldomain.ErrorCodeGeneratedPublicationBusy:
			return foundation.NewError(foundation.ErrorVersionConflict, authoringdomain.ErrorCodeGeneratedPublicationBusy, false, err)
		case changecontroldomain.ErrorCodeGeneratedPublicationConflict:
			return foundation.NewError(foundation.ErrorVersionConflict, authoringdomain.ErrorCodeGeneratedOriginConflict, false, err)
		}
	}
	return err
}
