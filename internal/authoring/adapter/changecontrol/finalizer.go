package changecontrol

import (
	"context"
	"errors"
	"strings"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// PublicationRepository 根据精确 proposal_commit 事实推进 Authoring 绑定。
type PublicationRepository interface {
	ReconcilePublications(context.Context, authoringapp.ReconcileQuery) (int, error)
	ValidateRestoreWriteback(context.Context, authoringapp.RestoreWritebackCheck) error
	FinalizeRestorePublication(context.Context, authoringapp.RestorePublicationRecord) (bool, error)
}

// PublicationFinalizer 将已完成的 Safe Writeback 桥接给 Authoring owner。
type PublicationFinalizer struct {
	repository PublicationRepository
	clock      foundation.Clock
}

var _ changecontrolapp.WritebackPublicationFinalizer = (*PublicationFinalizer)(nil)
var _ changecontrolapp.WritebackPreparationValidator = (*PublicationFinalizer)(nil)

// NewPublicationFinalizer 创建幂等的 Authoring 发布终态桥接器。
func NewPublicationFinalizer(repository PublicationRepository, clock foundation.Clock) (*PublicationFinalizer, error) {
	if repository == nil || clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "AUTHORING_PUBLICATION_FINALIZER_UNAVAILABLE", true, errors.New("authoring publication finalizer dependencies are incomplete"))
	}
	return &PublicationFinalizer{repository: repository, clock: clock}, nil
}

// ValidateWritebackPreparation re-checks the mutable Document owner facts for
// restore proposals after the target lock is held and before file mutation.
func (finalizer *PublicationFinalizer) ValidateWritebackPreparation(ctx context.Context, proposal changecontroldomain.Proposal) error {
	if finalizer == nil || finalizer.repository == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "AUTHORING_PUBLICATION_FINALIZER_UNAVAILABLE", true, errors.New("authoring publication finalizer is unavailable"))
	}
	if changecontroldomain.NormalizeProposalType(proposal.Type) != changecontroldomain.ProposalTypeRestoreDocument {
		return nil
	}
	if ctx == nil || proposal.Revision.RestoreDocument == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "RESTORE_DOCUMENT_OWNER_CHECK_INVALID", false, errors.New("restore proposal owner binding is missing"))
	}
	restore, err := changecontroldomain.ValidateRestoreDocument(*proposal.Revision.RestoreDocument)
	if err != nil || restore.WorkspaceID != proposal.WorkspaceID || proposal.TargetPath != proposal.Revision.TargetPath ||
		!strings.EqualFold(proposal.Revision.BaseHash, restore.CurrentContentHash) ||
		!strings.EqualFold(changecontroldomain.ComputeContentHash([]byte(proposal.Revision.Content)), restore.TargetContentHash) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "RESTORE_DOCUMENT_OWNER_BINDING_INVALID", false, errors.Join(err, changecontroldomain.ErrRestoreDocumentInvalid))
	}
	return finalizer.repository.ValidateRestoreWriteback(ctx, authoringapp.RestoreWritebackCheck{
		WorkspaceID: restore.WorkspaceID, DocumentID: restore.DocumentID,
		ExpectedDocumentVersion: restore.ExpectedDocumentVersion,
		TargetPath:              proposal.Revision.TargetPath, CurrentContentHash: restore.CurrentContentHash,
	})
}

// FinalizePublication reconciles either an Authoring publication binding or a
// restore proposal. Other file proposals remain intentional no-ops.
func (finalizer *PublicationFinalizer) FinalizePublication(ctx context.Context, publication changecontrolapp.WritebackPublication) error {
	if finalizer == nil || finalizer.repository == nil || finalizer.clock == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "AUTHORING_PUBLICATION_FINALIZER_UNAVAILABLE", true, errors.New("authoring publication finalizer is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "AUTHORING_PUBLICATION_FINALIZER_INVALID", false, errors.New("authoring publication finalizer context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "AUTHORING_PUBLICATION_FINALIZER_CONTEXT_DONE", true, err)
	}
	if !validPublicationID(publication.WorkspaceID) || !validPublicationID(publication.ProposalID) ||
		!validPublicationID(publication.ProposalRevisionID) || !validPublicationID(publication.WritebackID) ||
		!changecontroldomain.ValidGitObjectID(publication.GitCommit) || !changecontroldomain.ValidHash(publication.ResultHash) {
		return foundation.NewError(foundation.ErrorInvalidInput, "AUTHORING_PUBLICATION_FINALIZER_INVALID", false, errors.New("writeback publication binding is invalid"))
	}
	now := finalizer.clock.Now().UTC()
	if now.IsZero() {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "AUTHORING_PUBLICATION_FINALIZER_CLOCK_INVALID", false, errors.New("authoring publication finalizer clock returned zero time"))
	}
	if _, err := finalizer.repository.ReconcilePublications(ctx, authoringapp.ReconcileQuery{
		WorkspaceID: publication.WorkspaceID, ProposalID: publication.ProposalID,
		ProposalRevisionID: publication.ProposalRevisionID, Limit: 1, Now: now,
	}); err != nil {
		return err
	}
	_, err := finalizer.repository.FinalizeRestorePublication(ctx, authoringapp.RestorePublicationRecord{
		WorkspaceID: publication.WorkspaceID, ProposalID: publication.ProposalID,
		ProposalRevisionID: publication.ProposalRevisionID, WritebackID: publication.WritebackID,
		GitCommit: strings.ToLower(publication.GitCommit), ResultHash: strings.ToLower(publication.ResultHash),
		PublishedAt: now,
	})
	return err
}

func validPublicationID(id foundation.ID) bool {
	_, err := foundation.ParseID(string(id))
	return err == nil
}
