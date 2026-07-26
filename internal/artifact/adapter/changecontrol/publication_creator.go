// Package changecontrol adapts Artifact publication requests to Change Control.
package changecontrol

import (
	"context"
	"errors"
	"reflect"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	publicationRisk         = "Artifact publication requires Change Control approval before any formal knowledge write."
	publicationRollbackPlan = "Retain the isolated Artifact and do not create a formal Document until a supported execution capability is available."
)

// ProposalCreator is the narrow Change Control command dependency needed by Artifact.
type ProposalCreator interface {
	CreatePublishArtifactProposal(context.Context, changecontrolapp.CreatePublishArtifactCommand) (changecontrolapp.CreateResult, error)
}

// PublicationCreator creates only a frozen publish_artifact Proposal.
type PublicationCreator struct{ proposals ProposalCreator }

var _ artifactapp.PublicationCreator = (*PublicationCreator)(nil)

// NewPublicationCreator constructs the Artifact publication adapter.
func NewPublicationCreator(proposals ProposalCreator) (*PublicationCreator, error) {
	if proposals == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "ARTIFACT_PUBLICATION_CREATOR_UNAVAILABLE", false, errors.New("change control publication creator is unavailable"))
	}
	return &PublicationCreator{proposals: proposals}, nil
}

// CreateArtifactPublication maps Artifact's trusted frozen request to the typed Proposal contract.
func (creator *PublicationCreator) CreateArtifactPublication(ctx context.Context, request artifactdomain.PublicationRequest, _ string) (foundation.ID, error) {
	if creator == nil || creator.proposals == nil {
		return "", foundation.NewError(foundation.ErrorDependencyUnavailable, "ARTIFACT_PUBLICATION_CREATOR_UNAVAILABLE", false, errors.New("change control publication creator is unavailable"))
	}
	publication, err := publicationFromRequest(request)
	if err != nil {
		return "", err
	}
	proposalKey, err := proposalIdempotencyKey(publication)
	if err != nil {
		return "", err
	}
	commandPublication, err := changecontroldomain.ValidatePublishArtifact(publication)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorInvalidInput, "ARTIFACT_PUBLICATION_REQUEST_INVALID", false, err)
	}
	result, err := creator.proposals.CreatePublishArtifactProposal(ctx, changecontrolapp.CreatePublishArtifactCommand{
		WorkspaceID: request.WorkspaceID, IdempotencyKey: proposalKey, Publication: commandPublication,
		RiskLevel: changecontroldomain.ProposalRiskLevelHigh, Risk: publicationRisk, RollbackPlan: publicationRollbackPlan,
	})
	if err != nil {
		return "", err
	}
	if result.Proposal.ID == "" || result.Proposal.WorkspaceID != request.WorkspaceID || result.Proposal.Type != changecontroldomain.ProposalTypePublishArtifact || result.Proposal.Revision.PublishArtifact == nil {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "ARTIFACT_PUBLICATION_PROPOSAL_INVALID", false, errors.New("change control returned an invalid artifact publication proposal"))
	}
	frozen := result.Proposal.Revision.PublishArtifact
	validated, err := changecontroldomain.ValidatePublishArtifact(*frozen)
	if err != nil || !reflect.DeepEqual(*frozen, validated) {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "ARTIFACT_PUBLICATION_PROPOSAL_INVALID", false, errors.New("change control returned an invalid artifact publication payload"))
	}
	if !reflect.DeepEqual(validated, publication) {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "ARTIFACT_PUBLICATION_PROPOSAL_BINDING_CONFLICT", false, errors.New("change control proposal binding differs from artifact request"))
	}
	return result.Proposal.ID, nil
}

// proposalIdempotencyKey identifies the immutable publication binding rather
// than a caller command receipt. This lets concurrent Artifact commands share
// the same Change Control Proposal while their own receipts remain distinct.
func proposalIdempotencyKey(publication changecontroldomain.PublishArtifact) (string, error) {
	digest, err := changecontroldomain.ComputePublishArtifactRequestHash(
		publication.WorkspaceID,
		publication,
		changecontroldomain.ProposalRiskLevelHigh,
		publicationRisk,
		publicationRollbackPlan,
	)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorInvalidInput, "ARTIFACT_PUBLICATION_REQUEST_INVALID", false, err)
	}
	return "artifact-publication/v1:" + digest, nil
}

func publicationFromRequest(request artifactdomain.PublicationRequest) (changecontroldomain.PublishArtifact, error) {
	if request.ProposalType != artifactdomain.PublishArtifactProposalType {
		return changecontroldomain.PublishArtifact{}, foundation.NewError(foundation.ErrorInvalidInput, "ARTIFACT_PUBLICATION_REQUEST_INVALID", false, errors.New("artifact publication request type is invalid"))
	}
	coverage := make([]changecontroldomain.ArtifactSourceCoverage, len(request.SourceCoverage))
	for index, item := range request.SourceCoverage {
		status, err := coverageStatus(item.Status)
		if err != nil {
			return changecontroldomain.PublishArtifact{}, err
		}
		gaps := make([]changecontroldomain.ArtifactCoverageGap, len(item.Gaps))
		for gapIndex, gap := range item.Gaps {
			gaps[gapIndex] = changecontroldomain.ArtifactCoverageGap{Code: gap.Code, Description: gap.Description}
		}
		coverage[index] = changecontroldomain.ArtifactSourceCoverage{SectionKey: item.SectionKey, Status: status, Gaps: gaps}
	}
	publication, err := changecontroldomain.ValidatePublishArtifact(changecontroldomain.PublishArtifact{
		WorkspaceID: request.WorkspaceID, ArtifactID: request.ArtifactID, RevisionID: request.RevisionID,
		RevisionNo: request.RevisionNo, ArtifactVersion: request.ArtifactVersion, ContentHash: request.ContentHash,
		SourceCoverage: coverage, SchemaVersion: changecontroldomain.PublishArtifactSchemaVersion,
	})
	if err != nil {
		return changecontroldomain.PublishArtifact{}, foundation.NewError(foundation.ErrorInvalidInput, "ARTIFACT_PUBLICATION_REQUEST_INVALID", false, err)
	}
	return publication, nil
}

func coverageStatus(value artifactdomain.CoverageStatus) (changecontroldomain.ArtifactCoverageStatus, error) {
	switch value {
	case artifactdomain.CoverageCovered:
		return changecontroldomain.ArtifactCoverageCovered, nil
	case artifactdomain.CoveragePartial:
		return changecontroldomain.ArtifactCoveragePartial, nil
	case artifactdomain.CoverageGap:
		return changecontroldomain.ArtifactCoverageStatusGap, nil
	default:
		return "", foundation.NewError(foundation.ErrorInvalidInput, "ARTIFACT_PUBLICATION_REQUEST_INVALID", false, errors.New("artifact source coverage status is invalid"))
	}
}
