// Package changecontrol adapts frozen Authoring revisions to governed file-patch Proposals.
package changecontrol

import (
	"context"
	"errors"
	"strings"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	publicationEvidence = "The content is an immutable Authoring Article Revision explicitly submitted by the user."
	publicationRisk     = "Publishing changes the workspace knowledge files and the Git history used for later retrieval."
	publicationRollback = "Use the governed reverse-commit workflow to restore the previously approved workspace state."
)

// ProposalService is the narrow Change Control command dependency required by Authoring.
type ProposalService interface {
	CreateProposal(context.Context, changecontrolapp.CreateCommand) (changecontrolapp.CreateResult, error)
	CreateCreateOnlyFileProposal(context.Context, changecontrolapp.CreateCreateOnlyFileProposalCommand) (changecontrolapp.CreateResult, error)
}

// TargetReader verifies the current server-owned target baseline for replacements.
type TargetReader interface {
	CurrentHash(context.Context, foundation.ID, string) (string, error)
}

// ProposalCreator maps one frozen Article Revision to an immutable file-patch Proposal.
type ProposalCreator struct {
	proposals ProposalService
	targets   TargetReader
}

var _ authoringapp.ProposalCreator = (*ProposalCreator)(nil)

// NewProposalCreator constructs the Authoring publication adapter.
func NewProposalCreator(proposals ProposalService, targets TargetReader) (*ProposalCreator, error) {
	if proposals == nil || targets == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "AUTHORING_PROPOSAL_CREATOR_UNAVAILABLE", false, errors.New("change control proposal service is unavailable"))
	}
	return &ProposalCreator{proposals: proposals, targets: targets}, nil
}

// CreatePublicationProposal creates or exactly replays one governed file-patch Proposal.
func (creator *ProposalCreator) CreatePublicationProposal(ctx context.Context, request authoringapp.PublicationProposal) (authoringapp.PublicationProposalResult, error) {
	if creator == nil || creator.proposals == nil || creator.targets == nil {
		return authoringapp.PublicationProposalResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "AUTHORING_PROPOSAL_CREATOR_UNAVAILABLE", false, errors.New("change control proposal service is unavailable"))
	}
	mode, modeErr := changecontroldomain.ValidateTargetMode(request.TargetMode)
	canonicalPath, pathErr := authoringdomain.CanonicalizeTargetPath(request.TargetPath)
	if request.WorkspaceID == "" || modeErr != nil || pathErr != nil || canonicalPath != request.TargetPath ||
		changecontroldomain.ValidateTargetBaseVersion(request.WorkspaceID, request.TargetPath, mode, request.BaseVersion) != nil ||
		request.ContentHash != authoringdomain.ComputeContentHash(request.Content) || strings.TrimSpace(request.IdempotencyKey) == "" {
		return authoringapp.PublicationProposalResult{}, foundation.NewError(foundation.ErrorInvalidInput, authoringdomain.ErrorCodePublicationInvalid, false, errors.New("publication proposal request is invalid"))
	}
	provenance, err := generatedPublicationEvidence(request.CreatedByType)
	if err != nil {
		return authoringapp.PublicationProposalResult{}, err
	}
	evidence := provenance + " content_sha256=" + request.ContentHash
	var result changecontrolapp.CreateResult
	if mode == changecontroldomain.TargetModeCreateOnly {
		result, err = creator.proposals.CreateCreateOnlyFileProposal(ctx, changecontrolapp.CreateCreateOnlyFileProposalCommand{
			WorkspaceID: request.WorkspaceID, TargetPath: request.TargetPath,
			Content: request.Content, IdempotencyKey: request.IdempotencyKey,
			EvidenceSummary: evidence, RiskLevel: changecontroldomain.ProposalRiskLevelMedium,
			Risk: publicationRisk, RollbackPlan: publicationRollback,
		})
	} else {
		currentHash, readErr := creator.targets.CurrentHash(ctx, request.WorkspaceID, request.TargetPath)
		if readErr != nil {
			return authoringapp.PublicationProposalResult{}, readErr
		}
		if !strings.EqualFold(currentHash, request.BaseVersion) {
			return authoringapp.PublicationProposalResult{}, foundation.NewError(foundation.ErrorVersionConflict, "AUTHORING_TARGET_BASE_CONFLICT", false, errors.New("published target no longer matches the current document revision"))
		}
		result, err = creator.proposals.CreateProposal(ctx, changecontrolapp.CreateCommand{
			WorkspaceID: request.WorkspaceID, TargetPath: request.TargetPath, BaseHash: request.BaseVersion,
			Content: request.Content, IdempotencyKey: request.IdempotencyKey,
			EvidenceSummary: evidence, RiskLevel: changecontroldomain.ProposalRiskLevelMedium,
			Risk: publicationRisk, RollbackPlan: publicationRollback,
		})
	}
	if err != nil {
		return authoringapp.PublicationProposalResult{}, err
	}
	proposal := result.Proposal
	revision := proposal.Revision
	if proposal.ID == "" || revision.ID == "" || proposal.WorkspaceID != request.WorkspaceID ||
		proposal.Type != changecontroldomain.ProposalTypeFilePatch || proposal.TargetPath != request.TargetPath ||
		revision.ProposalID != proposal.ID || revision.TargetPath != request.TargetPath ||
		changecontroldomain.NormalizeTargetMode(revision.TargetMode) != mode ||
		revision.BaseHash != request.BaseVersion || revision.Content != request.Content ||
		authoringdomain.ComputeContentHash(revision.Content) != request.ContentHash {
		return authoringapp.PublicationProposalResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "AUTHORING_PROPOSAL_BINDING_CONFLICT", false, errors.New("change control returned a different publication binding"))
	}
	return authoringapp.PublicationProposalResult{
		ProposalID: proposal.ID, ProposalRevisionID: revision.ID,
		TargetMode: request.TargetMode, BaseVersion: request.BaseVersion, Replayed: result.Replayed,
	}, nil
}
