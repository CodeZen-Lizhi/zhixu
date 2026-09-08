package postgres

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

type completionIdentity struct {
	workspaceID foundation.ID
	executionID foundation.ID
	proposalID  foundation.ID
}

type completionProposal struct {
	id            foundation.ID
	workspaceID   foundation.ID
	workflowRunID *foundation.ID
	status        string
	version       int64
}

type completionExecution struct {
	id, workspaceID, workflowRunID, nodeRunID foundation.ID
	proposalID, revisionID, approvalID        foundation.ID
	targetPath, resultHash, gitCommit         string
	status                                    string
	cleanupCompletedAt                        *time.Time
	version                                   int64
}

func validateFirstCompletionState(proposal completionProposal, execution completionExecution, delivery domain.Delivery, attempt domain.DeliveryAttempt, target domain.IndexVersion) error {
	if proposal.workspaceID != delivery.WorkspaceID || proposal.workflowRunID == nil || *proposal.workflowRunID != execution.workflowRunID ||
		proposal.id != execution.proposalID || proposal.status != "verifying" || execution.id != delivery.WritebackExecutionID ||
		execution.workspaceID != delivery.WorkspaceID || execution.status != "verifying" {
		return consistency("REINDEX_COMPLETION_GATE_FAILED", errors.New("proposal or writeback lifecycle binding is invalid"))
	}
	if execution.cleanupCompletedAt == nil {
		return completionPrerequisitePending("writeback cleanup has not completed")
	}
	if delivery.Regression == nil || !domain.SnapshotRegressionCodeMatchesIndex(delivery.Regression.Code, target) ||
		delivery.SourceVersionID == nil || delivery.ParseProjectionID == nil || delivery.ExcludedSourceCount == nil ||
		attempt.IngestionAttemptID == nil || target.Status != domain.IndexStatusReady || target.ProcessingContract == nil || target.ExpectedSourceCount == nil {
		return consistency("REINDEX_COMPLETION_GATE_FAILED", errors.New("delivery regression or ready index gate is not satisfied"))
	}
	return nil
}

type completionReadOnlyFacts struct {
	eventType                                            string
	payload                                              []byte
	publishedAt                                          *time.Time
	commitProposalID, commitRevisionID, commitApprovalID foundation.ID
	commitGit, commitTarget, commitResult                string
	runStatus, nodeStatus, sourceHash                    string
	ingestionStatus, securityStatus                      string
	manifestBound                                        bool
}

func scanCompletionReadOnlyFacts(row retrievalRowScanner) (completionReadOnlyFacts, error) {
	var facts completionReadOnlyFacts
	err := row.Scan(
		&facts.eventType, &facts.payload, &facts.publishedAt, &facts.commitProposalID, &facts.commitRevisionID, &facts.commitApprovalID,
		&facts.commitGit, &facts.commitTarget, &facts.commitResult, &facts.runStatus, &facts.nodeStatus, &facts.sourceHash,
		&facts.ingestionStatus, &facts.securityStatus, &facts.manifestBound,
	)
	return facts, err
}

func validateCompletionFacts(proposal completionProposal, execution completionExecution, delivery domain.Delivery, facts completionReadOnlyFacts) error {
	request, err := reindexcontract.DecodeStrict(facts.payload)
	if err != nil {
		return consistency("REINDEX_COMPLETION_BINDING_CONFLICT", err)
	}
	if err := reindexcontract.ValidateBinding(request, reindexcontract.Binding{
		WorkspaceID: delivery.WorkspaceID, WorkflowRunID: execution.workflowRunID, NodeRunID: execution.nodeRunID,
		ProposalID: execution.proposalID, RevisionID: execution.revisionID, ApprovalID: execution.approvalID,
		WritebackExecutionID: execution.id, TargetPath: execution.targetPath, ResultHash: execution.resultHash, GitCommit: execution.gitCommit,
	}); err != nil {
		return consistency("REINDEX_COMPLETION_BINDING_CONFLICT", err)
	}
	if facts.eventType != reindexcontract.EventTypeReindexRequested || facts.publishedAt == nil || proposal.id != facts.commitProposalID ||
		facts.commitProposalID != execution.proposalID || facts.commitRevisionID != execution.revisionID || facts.commitApprovalID != execution.approvalID ||
		facts.commitGit != execution.gitCommit || facts.commitTarget != execution.targetPath || facts.commitResult != execution.resultHash ||
		facts.sourceHash != execution.resultHash ||
		facts.ingestionStatus != "chunked" || facts.securityStatus != "passed" || !facts.manifestBound {
		return consistency("REINDEX_COMPLETION_GATE_FAILED", errors.New("outbox, commit, source, or ingestion binding is invalid"))
	}
	if facts.runStatus != "succeeded" || facts.nodeStatus != "succeeded" {
		if facts.runStatus == "failed" || facts.runStatus == "cancelled" || facts.nodeStatus == "failed" || facts.nodeStatus == "cancelled" {
			return consistency("REINDEX_COMPLETION_GATE_FAILED", errors.New("origin workflow terminated without success"))
		}
		return completionPrerequisitePending("origin workflow has not completed")
	}
	return nil
}

func completionPrerequisitePending(message string) error {
	return application.NewCompletionPrerequisitePending(errors.New(message))
}

func completionLeaseExpired() error {
	return foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_LEASE_EXPIRED", true, errors.New("delivery database lease has expired"))
}

func validateCompletedReindexState(command domain.CompleteReindexCommand, proposal completionProposal, execution completionExecution, delivery domain.Delivery, attempt domain.DeliveryAttempt, target domain.IndexVersion) error {
	if !committedFenceIdentityMatches(command.Fence, delivery, attempt) || attempt.Status != domain.DeliveryAttemptSucceeded ||
		delivery.ActivationID == nil || delivery.SourceVersionID == nil || delivery.ParseProjectionID == nil ||
		delivery.ExcludedSourceCount == nil || delivery.Regression == nil || attempt.IngestionAttemptID == nil ||
		execution.status != "completed" || execution.cleanupCompletedAt == nil || proposal.status != "completed" ||
		proposal.workspaceID != delivery.WorkspaceID || proposal.workflowRunID == nil || *proposal.workflowRunID != execution.workflowRunID ||
		proposal.id != execution.proposalID || execution.id != delivery.WritebackExecutionID || execution.workspaceID != delivery.WorkspaceID ||
		!domain.SnapshotRegressionCodeMatchesIndex(delivery.Regression.Code, target) {
		return conflict("REINDEX_COMPLETION_REPLAY_CONFLICT", errors.New("terminal completion does not match the requested fence"))
	}
	return nil
}
