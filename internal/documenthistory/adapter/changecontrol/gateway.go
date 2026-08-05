// Package changecontrol adapts typed restore requests to the existing Change Control service.
package changecontrol

import (
	"context"
	"errors"

	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	restoreEvidence = "Target content was read by the server from a verified commit in the current canonical path history"
	restoreRisk     = "Restoring an older published body can remove newer information and affect downstream knowledge"
	restoreRollback = "Approve a new governed restore proposal targeting the previous HEAD content"
)

type proposalCreator interface {
	CreateRestoreDocumentProposal(context.Context, changecontrolapp.CreateRestoreDocumentCommand) (changecontrolapp.CreateResult, error)
}

type proposalLookup interface {
	FindProposalByIdempotencyKey(context.Context, foundation.ID, string) (changecontroldomain.Proposal, bool, error)
}

// Gateway exposes only Document restore creation and replay lookup.
type Gateway struct {
	creator proposalCreator
	lookup  proposalLookup
}

// NewGateway binds Document History to the existing Change Control aggregate owner.
func NewGateway(creator proposalCreator, lookup proposalLookup) (*Gateway, error) {
	if creator == nil || lookup == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeGitUnavailable, false, errors.New("change control restore dependencies are required"))
	}
	return &Gateway{creator: creator, lookup: lookup}, nil
}

// CreateRestoreDocumentProposal creates or replays one typed restore Proposal.
func (gateway *Gateway) CreateRestoreDocumentProposal(ctx context.Context, record application.CreateRestoreProposalRecord) (application.RestoreProposalReceipt, error) {
	result, err := gateway.creator.CreateRestoreDocumentProposal(ctx, changecontrolapp.CreateRestoreDocumentCommand{
		WorkspaceID: record.WorkspaceID, TargetPath: record.TargetPath, IdempotencyKey: record.IdempotencyKey,
		Content: record.TargetContent, EvidenceSummary: restoreEvidence, Risk: restoreRisk, RollbackPlan: restoreRollback,
		Restore: changecontroldomain.RestoreDocument{
			WorkspaceID: record.WorkspaceID, DocumentID: record.DocumentID,
			TargetCommit: record.TargetCommit, ExpectedHead: record.ExpectedHead,
			ExpectedDocumentVersion: record.ExpectedDocumentVersion, PreviewHash: record.PreviewHash,
			CurrentContentHash: record.CurrentContentHash, TargetContentHash: record.TargetContentHash,
			SchemaVersion: changecontroldomain.RestoreDocumentSchemaVersion,
		},
	})
	if err != nil {
		return application.RestoreProposalReceipt{}, err
	}
	receipt, err := receiptFromProposal(result.Proposal, false)
	if err != nil {
		return application.RestoreProposalReceipt{}, err
	}
	receipt.Replayed = result.Replayed
	return receipt, nil
}

// FindRestoreProposal resolves response-loss replay by the exact Workspace idempotency key.
func (gateway *Gateway) FindRestoreProposal(ctx context.Context, workspaceID foundation.ID, key string) (application.RestoreProposalReceipt, bool, error) {
	proposal, found, err := gateway.lookup.FindProposalByIdempotencyKey(ctx, workspaceID, key)
	if err != nil || !found {
		return application.RestoreProposalReceipt{}, found, err
	}
	receipt, err := receiptFromProposal(proposal, true)
	if err != nil {
		return application.RestoreProposalReceipt{}, false, err
	}
	return receipt, true, nil
}

func receiptFromProposal(proposal changecontroldomain.Proposal, idempotencyLookup bool) (application.RestoreProposalReceipt, error) {
	if changecontroldomain.NormalizeProposalType(proposal.Type) != changecontroldomain.ProposalTypeRestoreDocument {
		if idempotencyLookup {
			return application.RestoreProposalReceipt{}, foundation.NewError(foundation.ErrorVersionConflict, application.ErrorCodeIdempotencyReuse, false, errors.New("idempotency key belongs to another proposal type"))
		}
		return application.RestoreProposalReceipt{}, invalidProposalResult("restore creator returned another proposal type")
	}
	if proposal.Revision.RestoreDocument == nil || !validProposalID(proposal.ID) || !validProposalID(proposal.WorkspaceID) ||
		!validProposalID(proposal.Revision.ID) || !validProposalID(proposal.Revision.ProposalID) ||
		proposal.Revision.ProposalID != proposal.ID || proposal.WorkspaceID != proposal.Revision.RestoreDocument.WorkspaceID ||
		proposal.TargetPath != proposal.Revision.TargetPath || proposal.Version < 1 || !validProposalStatus(proposal.Status) {
		return application.RestoreProposalReceipt{}, invalidProposalResult("restore proposal aggregate binding is invalid")
	}
	if _, err := changecontroldomain.ValidateProposalRiskLevelForType(proposal.Type, proposal.RiskLevel); err != nil {
		return application.RestoreProposalReceipt{}, invalidProposalResult("restore proposal risk binding is invalid")
	}
	if err := changecontroldomain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision); err != nil {
		return application.RestoreProposalReceipt{}, invalidProposalResult("restore proposal revision is invalid")
	}
	restore := proposal.Revision.RestoreDocument
	return application.RestoreProposalReceipt{
		WorkspaceID: restore.WorkspaceID, DocumentID: restore.DocumentID,
		ProposalID: proposal.ID, ProposalRevisionID: proposal.Revision.ID,
		TargetCommit: restore.TargetCommit, ExpectedHead: restore.ExpectedHead,
		ExpectedDocumentVersion: restore.ExpectedDocumentVersion, PreviewHash: restore.PreviewHash,
		ProposalType: string(changecontroldomain.ProposalTypeRestoreDocument), Status: string(proposal.Status),
		ChangeHash: proposal.Revision.ChangeHash,
	}, nil
}

func validProposalID(value foundation.ID) bool {
	_, err := foundation.ParseID(string(value))
	return err == nil
}

func validProposalStatus(status changecontroldomain.ProposalStatus) bool {
	switch status {
	case changecontroldomain.StatusDraft, changecontroldomain.StatusValidating, changecontroldomain.StatusReady,
		changecontroldomain.StatusApproved, changecontroldomain.StatusApplying, changecontroldomain.StatusApplied,
		changecontroldomain.StatusVerifying, changecontroldomain.StatusCompleted, changecontroldomain.StatusRejected,
		changecontroldomain.StatusNeedsRevision, changecontroldomain.StatusDeferred, changecontroldomain.StatusApplyFailed,
		changecontroldomain.StatusVerifyFailed, changecontroldomain.StatusRolledBack, changecontroldomain.StatusCancelled:
		return true
	default:
		return false
	}
}

func invalidProposalResult(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, application.ErrorCodeResultInvalid, false, errors.New(message))
}

var _ application.RestoreProposalCreator = (*Gateway)(nil)
var _ application.RestoreProposalLookup = (*Gateway)(nil)
