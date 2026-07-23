//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	eventcontract "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/eventcontract"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/candidateconfirm"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestApprovedCandidateAppliesOneConfirmedRelationAndExactlyReplays(t *testing.T) {
	fixture := prepareApprovedCandidateApply(t, "approved-apply")
	events, err := eventspostgres.NewStore(fixture.tx)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		fixture.tx,
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
		events,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.ApplyApprovedRelation(fixture.ctx, fixture.command)
	if err != nil {
		t.Fatal(err)
	}
	assertApprovedRelationApplyResult(t, result, fixture, false)

	replayed, err := repository.ApplyApprovedRelation(fixture.ctx, fixture.command)
	if err != nil {
		t.Fatal(err)
	}
	assertApprovedRelationApplyResult(t, replayed, fixture, true)
	if replayed.Relation.ID != result.Relation.ID || replayed.Relation.Fingerprint != result.Relation.Fingerprint {
		t.Fatalf("replayed relation = %#v, first = %#v", replayed.Relation, result.Relation)
	}

	var relationCount, evidenceCount, receiptCount, writebackCount int
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.relation WHERE workspace_id=$1`, string(fixture.workspaceID)).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.relation_evidence WHERE workspace_id=$1`, string(fixture.workspaceID)).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1 AND idempotency_key=$2`, string(fixture.workspaceID), knowledgeapplication.RelationApplyIdempotencyKey(fixture.approvalID)).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM change_control.writeback_execution WHERE proposal_id=$1`, string(fixture.proposal.ID)).Scan(&writebackCount); err != nil {
		t.Fatal(err)
	}
	if relationCount != 1 || evidenceCount != len(fixture.candidate.Evidence) || receiptCount != 1 || writebackCount != 0 {
		t.Fatalf("relation=%d evidence=%d receipt=%d writeback=%d", relationCount, evidenceCount, receiptCount, writebackCount)
	}
	var eventCount int
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM ops.server_event
		WHERE workspace_id=$1 AND event_type=$2 AND resource_ref=$3`,
		string(fixture.workspaceID), eventcontract.ProposalAppliedEventType, "proposal:"+string(fixture.proposal.ID)).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("proposal applied event count=%d, want 1", eventCount)
	}
	var eventVersion int64
	var sourceRef, status string
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT resource_version,source_event_ref,payload_summary->>'status'
		FROM ops.server_event WHERE workspace_id=$1 AND event_type=$2`,
		string(fixture.workspaceID), eventcontract.ProposalAppliedEventType).Scan(&eventVersion, &sourceRef, &status); err != nil {
		t.Fatal(err)
	}
	if eventVersion != result.ProposalVersion || sourceRef != eventcontract.ProposalAppliedEventType+":"+string(fixture.approvalID)+":v1" || status != string(changecontroldomain.StatusApplied) {
		t.Fatalf("proposal applied event version=%d source=%s status=%s", eventVersion, sourceRef, status)
	}
	var candidateStatus string
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT status FROM graph.semantic_link_candidate WHERE id=$1`, string(fixture.candidate.ID)).Scan(&candidateStatus); err != nil {
		t.Fatal(err)
	}
	if candidateStatus != string(graphdomain.SemanticLinkCandidateStatusProposalCreated) {
		t.Fatalf("candidate status = %s", candidateStatus)
	}
}

func TestApprovedCandidateApplyMarksNeedsRevisionOnEndpointOrProvenanceDrift(t *testing.T) {
	t.Run("endpoint version", func(t *testing.T) {
		fixture := prepareApprovedCandidateApply(t, "endpoint-drift")
		if _, err := fixture.tx.Exec(fixture.ctx, `
			UPDATE core.claim
			SET status='INVALID',version=version+1,updated_at=$3
			WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.candidate.Source.Ref.ID), fixture.now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		repository, err := knowledgepostgres.NewApprovedRelationApplyRepository(fixture.tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: fixture.now.Add(4 * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ApplyApprovedRelation(fixture.ctx, fixture.command); !hasCandidateApplyCode(err, "RELATION_PROPOSAL_BASE_STALE") {
			t.Fatalf("endpoint drift error = %v", err)
		}
		assertNeedsRevisionWithoutRelation(t, fixture)
	})

	t.Run("provenance unreachable", func(t *testing.T) {
		fixture := prepareApprovedCandidateApply(t, "provenance-drift")
		if _, err := fixture.tx.Exec(fixture.ctx, `ALTER TABLE ingestion.source_version_projection DISABLE TRIGGER source_version_projection_reject_mutation`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.tx.Exec(fixture.ctx, `DELETE FROM ingestion.source_version_projection WHERE source_version_id=$1`, string(fixture.provenance.sourceVersionID)); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.tx.Exec(fixture.ctx, `ALTER TABLE ingestion.source_version_projection ENABLE TRIGGER source_version_projection_reject_mutation`); err != nil {
			t.Fatal(err)
		}
		repository, err := knowledgepostgres.NewApprovedRelationApplyRepository(fixture.tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: fixture.now.Add(4 * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ApplyApprovedRelation(fixture.ctx, fixture.command); !hasCandidateApplyCode(err, "RELATION_PROPOSAL_BASE_STALE") {
			t.Fatalf("provenance drift error = %v", err)
		}
		assertNeedsRevisionWithoutRelation(t, fixture)
	})
}

func TestApprovedCandidateApplyRollsBackRelationAndProposalOnEvidenceFailure(t *testing.T) {
	fixture := prepareApprovedCandidateApply(t, "apply-rollback")
	injected := errors.New("injected relation evidence failure")
	repository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		relationApplyFailBeginner{Tx: fixture.tx, err: injected},
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ApplyApprovedRelation(fixture.ctx, fixture.command); !errors.Is(err, injected) {
		t.Fatalf("apply failure = %v", err)
	}
	var status string
	var version, relationCount, receiptCount int
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT status,version FROM change_control.proposal WHERE id=$1`, string(fixture.proposal.ID)).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.relation WHERE workspace_id=$1`, string(fixture.workspaceID)).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1`, string(fixture.workspaceID)).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if status != string(changecontroldomain.StatusApproved) || version != 2 || relationCount != 0 || receiptCount != 0 {
		t.Fatalf("status=%s version=%d relation=%d receipt=%d", status, version, relationCount, receiptCount)
	}
}

func TestApprovedCandidateApplyRecoversCommitResponseLoss(t *testing.T) {
	fixture := prepareApprovedCandidateApply(t, "apply-response-loss")
	injected := errors.New("injected apply commit response loss")
	lost, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		candidateConfirmCommitLossBeginner{Tx: fixture.tx, err: injected},
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lost.ApplyApprovedRelation(fixture.ctx, fixture.command); !errors.Is(err, injected) {
		t.Fatalf("commit response loss error = %v", err)
	}

	normal, err := knowledgepostgres.NewApprovedRelationApplyRepository(fixture.tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: fixture.now.Add(4 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := normal.ApplyApprovedRelation(fixture.ctx, fixture.command)
	if err != nil {
		t.Fatal(err)
	}
	assertApprovedRelationApplyResult(t, replayed, fixture, true)
}

func TestCandidateApprovalAndRelationApplyCommitInOneTransaction(t *testing.T) {
	fixture := prepareCandidateApplyProposal(t, "atomic-success")
	events, err := eventspostgres.NewStore(fixture.tx)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		fixture.tx,
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
		events,
	)
	if err != nil {
		t.Fatal(err)
	}
	approval, result, err := repository.ApproveAndApplyRelation(fixture.ctx, fixture.approval)
	if err != nil {
		t.Fatal(err)
	}
	if approval.ID != fixture.approvalID || approval.ProposalID != fixture.proposal.ID || approval.RevisionID != fixture.proposal.Revision.ID {
		t.Fatalf("atomic approval = %#v", approval)
	}
	assertApprovedRelationApplyResult(t, result, fixture, false)

	retry := fixture.approval
	retry.ID = graphTestID(t)
	replayedApproval, replayed, err := repository.ApproveAndApplyRelation(fixture.ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	if replayedApproval.ID != fixture.approvalID {
		t.Fatalf("replayed approval id=%s want=%s", replayedApproval.ID, fixture.approvalID)
	}
	assertApprovedRelationApplyResult(t, replayed, fixture, true)
	assertAtomicApprovalApplyRows(t, fixture, changecontroldomain.StatusApplied, 4, 1, 1, 1)
	assertProposalEventCount(t, fixture, eventcontract.ProposalApprovedEventType, 1)
	assertProposalEventCount(t, fixture, eventcontract.ProposalAppliedEventType, 1)
	assertProposalEventBinding(t, fixture, eventcontract.ProposalApprovedEventType, changecontroldomain.StatusApproved, 2, fixture.approval.DecidedAt)
	assertProposalEventBinding(t, fixture, eventcontract.ProposalAppliedEventType, changecontroldomain.StatusApplied, 4, fixture.now.Add(3*time.Second))
	assertRelationApplyReceiptBinding(t, fixture, result)
}

func TestCandidateApprovalAndRelationApplyRollBackTogetherOnEvidenceFailure(t *testing.T) {
	fixture := prepareCandidateApplyProposal(t, "atomic-rollback")
	events, err := eventspostgres.NewStore(fixture.tx)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected atomic relation evidence failure")
	repository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		relationApplyFailBeginner{Tx: fixture.tx, err: injected},
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
		events,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ApproveAndApplyRelation(fixture.ctx, fixture.approval); !errors.Is(err, injected) {
		t.Fatalf("atomic apply failure = %v", err)
	}
	assertAtomicApprovalApplyRows(t, fixture, changecontroldomain.StatusReady, 1, 0, 0, 0)
	assertProposalEventCount(t, fixture, eventcontract.ProposalApprovedEventType, 0)
	assertProposalEventCount(t, fixture, eventcontract.ProposalAppliedEventType, 0)
}

func TestCandidateApprovalAndRelationApplyRollBackTogetherOnEventFailure(t *testing.T) {
	for _, eventType := range []string{eventcontract.ProposalApprovedEventType, eventcontract.ProposalAppliedEventType} {
		t.Run(eventType, func(t *testing.T) {
			fixture := prepareCandidateApplyProposal(t, "atomic-event-"+eventType)
			injected := errors.New("injected atomic proposal event failure")
			repository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
				fixture.tx,
				foundation.NewUUIDGenerator(nil),
				foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
				selectiveFailProposalEventAppender{eventType: eventType, err: injected},
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := repository.ApproveAndApplyRelation(fixture.ctx, fixture.approval); !errors.Is(err, injected) {
				t.Fatalf("event failure type=%s err=%v", eventType, err)
			}
			assertAtomicApprovalApplyRows(t, fixture, changecontroldomain.StatusReady, 1, 0, 0, 0)
		})
	}
}

func TestCandidateApprovalAndRelationApplyRollsBackNeedsRevisionEventFailure(t *testing.T) {
	fixture := prepareCandidateApplyProposal(t, "atomic-needs-revision-event-failure")
	if _, err := fixture.tx.Exec(fixture.ctx, `
		UPDATE core.claim
		SET status='INVALID',version=version+1,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.candidate.Source.Ref.ID), fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected needs_revision event failure")
	repository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		fixture.tx,
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
		selectiveFailProposalEventAppender{eventType: eventcontract.ProposalNeedsRevisionEventType, err: injected},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ApproveAndApplyRelation(fixture.ctx, fixture.approval); !errors.Is(err, injected) {
		t.Fatalf("needs_revision event failure = %v", err)
	}
	assertAtomicApprovalApplyRows(t, fixture, changecontroldomain.StatusReady, 1, 0, 0, 0)
	assertProposalEventCount(t, fixture, eventcontract.ProposalApprovedEventType, 0)
	assertProposalEventCount(t, fixture, eventcontract.ProposalNeedsRevisionEventType, 0)
}

func TestCandidateApprovalAndRelationApplyCommitNeedsRevisionOnBaselineDrift(t *testing.T) {
	fixture := prepareCandidateApplyProposal(t, "atomic-stale")
	if _, err := fixture.tx.Exec(fixture.ctx, `
		UPDATE core.claim
		SET status='INVALID',version=version+1,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.candidate.Source.Ref.ID), fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewStore(fixture.tx)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		fixture.tx,
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
		events,
	)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _, err := repository.ApproveAndApplyRelation(fixture.ctx, fixture.approval)
	if !hasCandidateApplyCode(err, "RELATION_PROPOSAL_BASE_STALE") || persisted.ID != fixture.approvalID {
		t.Fatalf("stale approval=%#v err=%v", persisted, err)
	}
	assertAtomicApprovalApplyRows(t, fixture, changecontroldomain.StatusNeedsRevision, 3, 1, 0, 0)
	assertProposalEventCount(t, fixture, eventcontract.ProposalApprovedEventType, 1)
	assertProposalEventCount(t, fixture, eventcontract.ProposalAppliedEventType, 0)
	assertProposalEventBinding(t, fixture, eventcontract.ProposalNeedsRevisionEventType, changecontroldomain.StatusNeedsRevision, 3, fixture.now.Add(3*time.Second))
	retry := fixture.approval
	retry.ID = graphTestID(t)
	replayedApproval, _, replayErr := repository.ApproveAndApplyRelation(fixture.ctx, retry)
	if !hasCandidateApplyCode(replayErr, "RELATION_PROPOSAL_BASE_STALE") || replayedApproval.ID != fixture.approvalID {
		t.Fatalf("stale replay approval=%#v err=%v", replayedApproval, replayErr)
	}
	assertAtomicApprovalApplyRows(t, fixture, changecontroldomain.StatusNeedsRevision, 3, 1, 0, 0)
	assertProposalEventCount(t, fixture, eventcontract.ProposalApprovedEventType, 1)
	assertProposalEventCount(t, fixture, eventcontract.ProposalNeedsRevisionEventType, 1)
}

func TestCandidateApprovalAndRelationApplyRecoverCommitResponseLoss(t *testing.T) {
	fixture := prepareCandidateApplyProposal(t, "atomic-response-loss")
	events, err := eventspostgres.NewStore(fixture.tx)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected atomic commit response loss")
	lost, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		candidateConfirmCommitLossBeginner{Tx: fixture.tx, err: injected},
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
		events,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := lost.ApproveAndApplyRelation(fixture.ctx, fixture.approval); !errors.Is(err, injected) {
		t.Fatalf("atomic commit response loss = %v", err)
	}

	normal, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		fixture.tx,
		foundation.NewUUIDGenerator(nil),
		foundation.FixedClock{Value: fixture.now.Add(4 * time.Second)},
		events,
	)
	if err != nil {
		t.Fatal(err)
	}
	retry := fixture.approval
	retry.ID = graphTestID(t)
	approval, replayed, err := normal.ApproveAndApplyRelation(fixture.ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	if approval.ID != fixture.approvalID {
		t.Fatalf("response-loss replay approval=%#v", approval)
	}
	assertApprovedRelationApplyResult(t, replayed, fixture, true)
	assertAtomicApprovalApplyRows(t, fixture, changecontroldomain.StatusApplied, 4, 1, 1, 1)
	assertProposalEventCount(t, fixture, eventcontract.ProposalApprovedEventType, 1)
	assertProposalEventCount(t, fixture, eventcontract.ProposalAppliedEventType, 1)
}

func TestCandidateApprovalAndRelationApplyReusesSuggestedRelationAndEvidence(t *testing.T) {
	for _, existingEvidenceCount := range []int{0, 1} {
		t.Run("existing-evidence-"+strconv.Itoa(existingEvidenceCount), func(t *testing.T) {
			fixture := prepareCandidateApplyProposal(t, "reuse-suggested-"+strconv.Itoa(existingEvidenceCount))
			knowledgeRepository, err := knowledgepostgres.NewRepository(fixture.tx)
			if err != nil {
				t.Fatal(err)
			}
			relation := newCandidateApplyRelation(t, fixture.workspaceID, fixture.candidate.SuggestedRelationType, fixture.candidate.Source.Ref, fixture.candidate.Target.Ref, fixture.now.Add(1500*time.Millisecond))
			var existingEvidence []knowledge.RelationEvidence
			if existingEvidenceCount == 1 {
				item := newCandidateApplyEvidence(t, fixture.workspaceID, "existing suggested evidence", fixture.provenance, fixture.now.Add(1600*time.Millisecond), nil)
				item.RelationID = relation.ID
				existingEvidence = append(existingEvidence, item)
			}
			persisted, err := knowledgeRepository.SuggestRelation(fixture.ctx, knowledge.SuggestRelationRecord{
				Relation: relation, Evidence: existingEvidence,
				IdempotencyKey: "reuse-suggested-" + strconv.Itoa(existingEvidenceCount), RequestHash: graphHash("reuse-suggested-" + strconv.Itoa(existingEvidenceCount)),
			})
			if err != nil {
				t.Fatal(err)
			}

			events, err := eventspostgres.NewStore(fixture.tx)
			if err != nil {
				t.Fatal(err)
			}
			applyRepository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
				fixture.tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)}, events,
			)
			if err != nil {
				t.Fatal(err)
			}
			approval, result, err := applyRepository.ApproveAndApplyRelation(fixture.ctx, fixture.approval)
			if err != nil {
				t.Fatal(err)
			}
			if approval.ID != fixture.approvalID || result.Relation.ID != persisted.Relation.ID || result.Relation.Status != knowledge.RelationStatusConfirmed ||
				result.Relation.Version != persisted.Relation.Version+1 || len(result.Evidence) != existingEvidenceCount+len(fixture.candidate.Evidence) {
				t.Fatalf("reused relation approval=%#v result=%#v persisted=%#v", approval, result, persisted)
			}
			if result.Relation.EvidenceFingerprint != knowledge.ComputeRelationEvidenceFingerprint(result.Evidence) {
				t.Fatalf("reused relation evidence fingerprint=%s evidence=%#v", result.Relation.EvidenceFingerprint, result.Evidence)
			}
			oldIDs := make(map[foundation.ID]struct{}, len(persisted.Evidence))
			for _, item := range persisted.Evidence {
				oldIDs[item.ID] = struct{}{}
			}
			oldCount, approvalCount := 0, 0
			for _, item := range result.Evidence {
				if _, ok := oldIDs[item.ID]; ok {
					oldCount++
					if item.Confirmation != nil {
						t.Fatalf("existing evidence unexpectedly confirmed: %#v", item)
					}
				} else if item.Confirmation != nil && item.Confirmation.Method == knowledge.ConfirmationUserApproval && item.Confirmation.Reference == string(fixture.approvalID) {
					approvalCount++
				}
			}
			if oldCount != existingEvidenceCount || approvalCount != len(fixture.candidate.Evidence) {
				t.Fatalf("existing evidence=%d approval evidence=%d result=%#v", oldCount, approvalCount, result.Evidence)
			}
			assertRelationApplyReceiptBinding(t, fixture, result)

			retry := fixture.approval
			retry.ID = graphTestID(t)
			replayedApproval, replayed, err := applyRepository.ApproveAndApplyRelation(fixture.ctx, retry)
			if err != nil {
				t.Fatal(err)
			}
			if replayedApproval.ID != fixture.approvalID || !replayed.Replayed || replayed.Relation.ID != persisted.Relation.ID || len(replayed.Evidence) != len(result.Evidence) {
				t.Fatalf("reused relation replay approval=%#v result=%#v", replayedApproval, replayed)
			}
			var relationCount, evidenceCount int
			if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.relation WHERE workspace_id=$1 AND fingerprint=$2`, string(fixture.workspaceID), persisted.Relation.Fingerprint).Scan(&relationCount); err != nil {
				t.Fatal(err)
			}
			if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.relation_evidence WHERE workspace_id=$1 AND relation_id=$2`, string(fixture.workspaceID), string(persisted.Relation.ID)).Scan(&evidenceCount); err != nil {
				t.Fatal(err)
			}
			if relationCount != 1 || evidenceCount != len(result.Evidence) {
				t.Fatalf("reused relation rows relations=%d evidence=%d", relationCount, evidenceCount)
			}
		})
	}
}

func TestCandidateApprovalAndRelationApplyDoesNotOverwriteConfirmedRelation(t *testing.T) {
	fixture := prepareCandidateApplyProposal(t, "existing-confirmed")
	knowledgeRepository, err := knowledgepostgres.NewRepository(fixture.tx)
	if err != nil {
		t.Fatal(err)
	}
	relation := newCandidateApplyRelation(t, fixture.workspaceID, fixture.candidate.SuggestedRelationType, fixture.candidate.Source.Ref, fixture.candidate.Target.Ref, fixture.now.Add(1500*time.Millisecond))
	persisted, err := knowledgeRepository.SuggestRelation(fixture.ctx, knowledge.SuggestRelationRecord{
		Relation: relation, IdempotencyKey: "existing-confirmed-suggest", RequestHash: graphHash("existing-confirmed-suggest"),
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmation := knowledge.Confirmation{Method: knowledge.ConfirmationUserApproval, Reference: "prior-approval"}
	evidence := newCandidateApplyEvidence(t, fixture.workspaceID, "prior confirmed evidence", fixture.provenance, fixture.now.Add(2*time.Second), &confirmation)
	evidence.RelationID = persisted.Relation.ID
	confirmed, err := knowledgeRepository.ConfirmRelation(fixture.ctx, knowledge.ConfirmRelationRecord{
		WorkspaceID: fixture.workspaceID, RelationID: persisted.Relation.ID, ExpectedVersion: persisted.Relation.Version,
		Evidence: evidence, Confirmation: confirmation, IdempotencyKey: "existing-confirmed-confirm", RequestHash: graphHash("existing-confirmed-confirm"), At: fixture.now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	events, err := eventspostgres.NewStore(fixture.tx)
	if err != nil {
		t.Fatal(err)
	}
	applyRepository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		fixture.tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)}, events,
	)
	if err != nil {
		t.Fatal(err)
	}
	persistedApproval, _, applyErr := applyRepository.ApproveAndApplyRelation(fixture.ctx, fixture.approval)
	if !hasCandidateApplyCode(applyErr, "RELATION_PROPOSAL_BASE_STALE") || persistedApproval.ID != fixture.approvalID {
		t.Fatalf("confirmed relation apply approval=%#v err=%v", persistedApproval, applyErr)
	}
	assertAtomicApprovalApplyRows(t, fixture, changecontroldomain.StatusNeedsRevision, 3, 1, 1, 2)
	assertProposalEventCount(t, fixture, eventcontract.ProposalApprovedEventType, 1)
	assertProposalEventCount(t, fixture, eventcontract.ProposalAppliedEventType, 0)
	assertProposalEventBinding(t, fixture, eventcontract.ProposalNeedsRevisionEventType, changecontroldomain.StatusNeedsRevision, 3, fixture.now.Add(3*time.Second))
	var applyReceiptCount int
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1 AND idempotency_key=$2`, string(fixture.workspaceID), knowledgeapplication.RelationApplyIdempotencyKey(fixture.approvalID)).Scan(&applyReceiptCount); err != nil {
		t.Fatal(err)
	}
	if applyReceiptCount != 0 {
		t.Fatalf("confirmed relation apply receipt count=%d", applyReceiptCount)
	}
	var relationStatus string
	var relationVersion int64
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT status,version FROM core.relation WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(confirmed.Relation.ID)).Scan(&relationStatus, &relationVersion); err != nil {
		t.Fatal(err)
	}
	if relationStatus != string(knowledge.RelationStatusConfirmed) || relationVersion != confirmed.Relation.Version {
		t.Fatalf("confirmed relation changed status=%s version=%d want=%s/%d", relationStatus, relationVersion, confirmed.Relation.Status, confirmed.Relation.Version)
	}
}

func TestCandidateApprovalAndRelationApplyRejectsNonSuggestedRelationStates(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, fixture approvedCandidateApplyFixture, repository *knowledgepostgres.Repository) knowledge.RelationResult
	}{
		{
			name: "stale",
			prepare: func(t *testing.T, fixture approvedCandidateApplyFixture, repository *knowledgepostgres.Repository) knowledge.RelationResult {
				relation := suggestExistingApplyRelation(t, fixture, repository, "non-suggested-stale")
				confirmed := confirmExistingApplyRelation(t, fixture, repository, relation, "previous-stale-approval")
				result, err := repository.TransitionRelation(fixture.ctx, knowledge.TransitionRelationRecord{
					WorkspaceID: fixture.workspaceID, RelationID: relation.Relation.ID, ExpectedVersion: confirmed.Relation.Version,
					Status: knowledge.RelationStatusStale, IdempotencyKey: "non-suggested-stale-transition", RequestHash: graphHash("non-suggested-stale-transition"), At: fixture.now.Add(3 * time.Second),
				})
				if err != nil {
					t.Fatal(err)
				}
				return result
			},
		},
		{
			name: "rejected",
			prepare: func(t *testing.T, fixture approvedCandidateApplyFixture, repository *knowledgepostgres.Repository) knowledge.RelationResult {
				relation := suggestExistingApplyRelation(t, fixture, repository, "non-suggested-rejected")
				result, err := repository.TransitionRelation(fixture.ctx, knowledge.TransitionRelationRecord{
					WorkspaceID: fixture.workspaceID, RelationID: relation.Relation.ID, ExpectedVersion: relation.Relation.Version,
					Status: knowledge.RelationStatusRejected, IdempotencyKey: "non-suggested-rejected-transition", RequestHash: graphHash("non-suggested-rejected-transition"), At: fixture.now.Add(2 * time.Second),
				})
				if err != nil {
					t.Fatal(err)
				}
				return result
			},
		},
		{
			name: "deprecated",
			prepare: func(t *testing.T, fixture approvedCandidateApplyFixture, repository *knowledgepostgres.Repository) knowledge.RelationResult {
				relation := suggestExistingApplyRelation(t, fixture, repository, "non-suggested-deprecated")
				confirmed := confirmExistingApplyRelation(t, fixture, repository, relation, "previous-deprecated-approval")
				result, err := repository.TransitionRelation(fixture.ctx, knowledge.TransitionRelationRecord{
					WorkspaceID: fixture.workspaceID, RelationID: relation.Relation.ID, ExpectedVersion: confirmed.Relation.Version,
					Status: knowledge.RelationStatusDeprecated, IdempotencyKey: "non-suggested-deprecated-transition", RequestHash: graphHash("non-suggested-deprecated-transition"), At: fixture.now.Add(3 * time.Second),
				})
				if err != nil {
					t.Fatal(err)
				}
				return result
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareCandidateApplyProposal(t, "reject-"+test.name)
			knowledgeRepository, err := knowledgepostgres.NewRepository(fixture.tx)
			if err != nil {
				t.Fatal(err)
			}
			existing := test.prepare(t, fixture, knowledgeRepository)
			var receiptCountBefore int
			if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1`, string(fixture.workspaceID)).Scan(&receiptCountBefore); err != nil {
				t.Fatal(err)
			}

			events, err := eventspostgres.NewStore(fixture.tx)
			if err != nil {
				t.Fatal(err)
			}
			applyRepository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
				fixture.tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: fixture.now.Add(4 * time.Second)}, events,
			)
			if err != nil {
				t.Fatal(err)
			}
			persistedApproval, _, applyErr := applyRepository.ApproveAndApplyRelation(fixture.ctx, fixture.approval)
			if !hasCandidateApplyCode(applyErr, "RELATION_PROPOSAL_BASE_STALE") || persistedApproval.ID != fixture.approvalID {
				t.Fatalf("%s relation apply approval=%#v err=%v", test.name, persistedApproval, applyErr)
			}
			assertAtomicApprovalApplyRows(t, fixture, changecontroldomain.StatusNeedsRevision, 3, 1, 1, receiptCountBefore)
			assertProposalEventCount(t, fixture, eventcontract.ProposalApprovedEventType, 1)
			assertProposalEventCount(t, fixture, eventcontract.ProposalAppliedEventType, 0)
			assertProposalEventBinding(t, fixture, eventcontract.ProposalNeedsRevisionEventType, changecontroldomain.StatusNeedsRevision, 3, fixture.now.Add(4*time.Second))
			var applyReceiptCount int
			if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1 AND idempotency_key=$2`, string(fixture.workspaceID), knowledgeapplication.RelationApplyIdempotencyKey(fixture.approvalID)).Scan(&applyReceiptCount); err != nil {
				t.Fatal(err)
			}
			if applyReceiptCount != 0 {
				t.Fatalf("%s relation apply receipt count=%d", test.name, applyReceiptCount)
			}

			loaded, err := knowledgeRepository.BatchGetRelations(fixture.ctx, knowledge.BatchGetRelationsQuery{
				WorkspaceID: fixture.workspaceID, IDs: []foundation.ID{existing.Relation.ID}, Limit: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded) != 1 || loaded[0].Relation.Status != existing.Relation.Status || loaded[0].Relation.Version != existing.Relation.Version || len(loaded[0].Evidence) != len(existing.Evidence) {
				t.Fatalf("non-suggested relation changed: before=%#v after=%#v", existing, loaded)
			}
		})
	}
}

func TestCandidateApprovalAndRelationApplyPreservesHistoricalEvidenceAcrossLifecycleReplay(t *testing.T) {
	fixture := prepareCandidateApplyProposal(t, "historical-evidence-lifecycle")
	knowledgeRepository, err := knowledgepostgres.NewRepository(fixture.tx)
	if err != nil {
		t.Fatal(err)
	}
	relation := suggestExistingApplyRelation(t, fixture, knowledgeRepository, "historical-evidence-initial")
	confirmed := confirmExistingApplyRelation(t, fixture, knowledgeRepository, relation, "historical-approval")
	stale, err := knowledgeRepository.TransitionRelation(fixture.ctx, knowledge.TransitionRelationRecord{
		WorkspaceID: fixture.workspaceID, RelationID: relation.Relation.ID, ExpectedVersion: confirmed.Relation.Version,
		Status: knowledge.RelationStatusStale, IdempotencyKey: "historical-stale", RequestHash: graphHash("historical-stale"), At: fixture.now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := knowledgeRepository.TransitionRelation(fixture.ctx, knowledge.TransitionRelationRecord{
		WorkspaceID: fixture.workspaceID, RelationID: relation.Relation.ID, ExpectedVersion: stale.Relation.Version,
		Status: knowledge.RelationStatusRejected, IdempotencyKey: "historical-rejected", RequestHash: graphHash("historical-rejected"), At: fixture.now.Add(4 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	resuggestEvidence := newCandidateApplyEvidence(t, fixture.workspaceID, "historical resuggestion evidence", fixture.provenance, fixture.now.Add(5*time.Second), nil)
	resuggestEvidence.RelationID = relation.Relation.ID
	resuggested, err := knowledgeRepository.TransitionRelation(fixture.ctx, knowledge.TransitionRelationRecord{
		WorkspaceID: fixture.workspaceID, RelationID: relation.Relation.ID, ExpectedVersion: rejected.Relation.Version,
		Status: knowledge.RelationStatusSuggested, Evidence: &resuggestEvidence,
		IdempotencyKey: "historical-resuggest", RequestHash: graphHash("historical-resuggest"), At: fixture.now.Add(5 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resuggested.Relation.Status != knowledge.RelationStatusSuggested || resuggested.Relation.Version != 5 || len(resuggested.Evidence) != 2 {
		t.Fatalf("resuggested lifecycle state=%#v", resuggested)
	}

	events, err := eventspostgres.NewStore(fixture.tx)
	if err != nil {
		t.Fatal(err)
	}
	applyRepository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		fixture.tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: fixture.now.Add(6 * time.Second)}, events,
	)
	if err != nil {
		t.Fatal(err)
	}
	approval, result, err := applyRepository.ApproveAndApplyRelation(fixture.ctx, fixture.approval)
	if err != nil {
		t.Fatal(err)
	}
	if approval.ID != fixture.approvalID || result.Replayed || result.Relation.ID != relation.Relation.ID || result.Relation.Status != knowledge.RelationStatusConfirmed || result.Relation.Version != 6 || len(result.Evidence) != 2+len(fixture.candidate.Evidence) {
		t.Fatalf("historical apply approval=%#v result=%#v", approval, result)
	}
	assertHistoricalApprovalEvidence(t, result.Evidence, fixture.approvalID, "historical-approval", len(fixture.candidate.Evidence))
	if result.Relation.EvidenceFingerprint != knowledge.ComputeRelationEvidenceFingerprint(result.Evidence) {
		t.Fatalf("historical evidence fingerprint=%s evidence=%#v", result.Relation.EvidenceFingerprint, result.Evidence)
	}

	retry := fixture.approval
	retry.ID = graphTestID(t)
	replayedApproval, replayed, err := applyRepository.ApproveAndApplyRelation(fixture.ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	if replayedApproval.ID != fixture.approvalID || !replayed.Replayed || replayed.Relation.ID != relation.Relation.ID || replayed.Relation.Version != result.Relation.Version || len(replayed.Evidence) != len(result.Evidence) {
		t.Fatalf("historical replay approval=%#v result=%#v", replayedApproval, replayed)
	}
	assertHistoricalApprovalEvidence(t, replayed.Evidence, fixture.approvalID, "historical-approval", len(fixture.candidate.Evidence))
}

func suggestExistingApplyRelation(t *testing.T, fixture approvedCandidateApplyFixture, repository *knowledgepostgres.Repository, label string) knowledge.RelationResult {
	t.Helper()
	relation := newCandidateApplyRelation(t, fixture.workspaceID, fixture.candidate.SuggestedRelationType, fixture.candidate.Source.Ref, fixture.candidate.Target.Ref, fixture.now.Add(time.Second))
	returnResult, err := repository.SuggestRelation(fixture.ctx, knowledge.SuggestRelationRecord{
		Relation: relation, IdempotencyKey: label + "-suggest", RequestHash: graphHash(label + "-suggest"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return returnResult
}

func confirmExistingApplyRelation(t *testing.T, fixture approvedCandidateApplyFixture, repository *knowledgepostgres.Repository, relation knowledge.RelationResult, reference string) knowledge.RelationResult {
	t.Helper()
	confirmation := knowledge.Confirmation{Method: knowledge.ConfirmationUserApproval, Reference: reference}
	evidence := newCandidateApplyEvidence(t, fixture.workspaceID, reference+" evidence", fixture.provenance, fixture.now.Add(2*time.Second), &confirmation)
	evidence.RelationID = relation.Relation.ID
	result, err := repository.ConfirmRelation(fixture.ctx, knowledge.ConfirmRelationRecord{
		WorkspaceID: fixture.workspaceID, RelationID: relation.Relation.ID, ExpectedVersion: relation.Relation.Version,
		Evidence: evidence, Confirmation: confirmation, IdempotencyKey: reference + "-confirm", RequestHash: graphHash(reference + "-confirm"), At: fixture.now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertHistoricalApprovalEvidence(t *testing.T, evidence []knowledge.RelationEvidence, approvalID foundation.ID, historicalReference string, expectedCurrent int) {
	t.Helper()
	historical, current := 0, 0
	for _, item := range evidence {
		if item.Confirmation == nil {
			continue
		}
		if item.Confirmation.Reference == historicalReference {
			historical++
		}
		if item.Confirmation.Method == knowledge.ConfirmationUserApproval && item.Confirmation.Reference == string(approvalID) {
			current++
		}
	}
	if historical != 1 || current != expectedCurrent {
		t.Fatalf("historical confirmation count=%d current approval count=%d evidence=%#v", historical, current, evidence)
	}
}

func TestCandidateApprovalAndRelationApplyLocksCandidateBeforeProposal(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	graphRepository := &Repository{db: seedTx, statementTimeout: defaultStatementTimeout, inReadSnapshot: true}
	fixture := prepareCandidateApplyProposalOnTx(t, graphRepository, seedTx, ctx, "lock-order")
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	locker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locker.Rollback(context.Background()) }()
	if _, err := locker.Exec(ctx, `SELECT id FROM graph.semantic_link_candidate WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(fixture.workspaceID), string(fixture.candidate.ID)); err != nil {
		t.Fatal(err)
	}

	probe := newApprovalLockProbeDB(pool)
	applyRepository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		probe, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
	)
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan error, 1)
	go func() {
		_, _, applyErr := applyRepository.ApproveAndApplyRelation(ctx, fixture.approval)
		resultCh <- applyErr
	}()
	select {
	case <-probe.candidateAttempt:
	case <-ctx.Done():
		t.Fatalf("approval UoW did not reach candidate lock: %v", ctx.Err())
	}

	proposalProbeTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var proposalID string
	proposalErr := proposalProbeTx.QueryRow(ctx, `
		SELECT id::text FROM change_control.proposal
		WHERE workspace_id=$1 AND id=$2
		FOR UPDATE NOWAIT`, string(fixture.workspaceID), string(fixture.proposal.ID)).Scan(&proposalID)
	_ = proposalProbeTx.Rollback(context.Background())
	if proposalErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(proposalErr, &pgErr) && pgErr.Code == "55P03" {
			t.Fatalf("proposal row was locked before candidate row: %v", proposalErr)
		}
		t.Fatalf("proposal lock probe failed: %v", proposalErr)
	}
	if proposalID != string(fixture.proposal.ID) {
		t.Fatalf("proposal lock probe id=%s want=%s", proposalID, fixture.proposal.ID)
	}
	if err := locker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-resultCh; err != nil {
		t.Fatalf("approval UoW after candidate release: %v", err)
	}
}

func TestCandidateApprovalAndRelationApplyReusesConcurrentSuggestedWinner(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	graphRepository := &Repository{db: seedTx, statementTimeout: defaultStatementTimeout, inReadSnapshot: true}
	fixture := prepareCandidateApplyProposalOnTx(t, graphRepository, seedTx, ctx, "concurrent-suggested-winner")
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupApprovalApplyWorkspace(pool, fixture.workspaceID) })

	probe := newRelationWinnerProbeDB(pool)
	defer probe.release()
	applyRepository, err := knowledgepostgres.NewApprovedRelationApplyRepository(
		probe, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: fixture.now.Add(3 * time.Second)},
	)
	if err != nil {
		t.Fatal(err)
	}
	type applyOutcome struct {
		approval changecontroldomain.Approval
		result   knowledgeapplication.ApprovedRelationApplyResult
		err      error
	}
	applyCh := make(chan applyOutcome, 1)
	go func() {
		approval, result, applyErr := applyRepository.ApproveAndApplyRelation(ctx, fixture.approval)
		applyCh <- applyOutcome{approval: approval, result: result, err: applyErr}
	}()
	select {
	case <-probe.relationLookup:
	case <-ctx.Done():
		t.Fatalf("approval UoW did not complete its initial relation lookup: %v", ctx.Err())
	}

	suggestionRepository, err := knowledgepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	winnerRelation := newCandidateApplyRelation(t, fixture.workspaceID, fixture.candidate.SuggestedRelationType, fixture.candidate.Source.Ref, fixture.candidate.Target.Ref, fixture.now.Add(4*time.Second))
	winnerEvidence := newCandidateApplyEvidence(t, fixture.workspaceID, "concurrent winner evidence", fixture.provenance, fixture.now.Add(4*time.Second), nil)
	winnerEvidence.RelationID = winnerRelation.ID
	winner, err := suggestionRepository.SuggestRelation(ctx, knowledge.SuggestRelationRecord{
		Relation:       winnerRelation,
		Evidence:       []knowledge.RelationEvidence{winnerEvidence},
		IdempotencyKey: "concurrent-suggested-winner",
		RequestHash:    graphHash("concurrent-suggested-winner"),
	})
	if err != nil {
		t.Fatal(err)
	}
	probe.release()

	select {
	case outcome := <-applyCh:
		if outcome.err != nil {
			t.Fatalf("approval UoW error after concurrent Suggested winner: %v", outcome.err)
		}
		if outcome.approval.ID != fixture.approvalID || outcome.result.Relation.ID != winner.Relation.ID ||
			outcome.result.Relation.Status != knowledge.RelationStatusConfirmed || outcome.result.ProposalStatus != changecontroldomain.StatusApplied {
			t.Fatalf("approval=%#v result=%#v winner=%#v", outcome.approval, outcome.result, winner)
		}
		if outcome.result.Relation.UpdatedAt.Before(winner.Relation.UpdatedAt) ||
			len(outcome.result.Evidence) != len(winner.Evidence)+len(fixture.candidate.Evidence) ||
			outcome.result.Relation.EvidenceFingerprint != knowledge.ComputeRelationEvidenceFingerprint(outcome.result.Evidence) {
			t.Fatalf("winner lifecycle/evidence was not preserved: result=%#v winner=%#v", outcome.result, winner)
		}
		winnerEvidenceFound := false
		for _, item := range outcome.result.Evidence {
			if item.ID == winnerEvidence.ID {
				winnerEvidenceFound = true
				break
			}
		}
		if !winnerEvidenceFound {
			t.Fatalf("concurrent winner evidence missing: result=%#v winner=%#v", outcome.result, winner)
		}
	case <-ctx.Done():
		t.Fatalf("approval UoW did not finish after winner commit: %v", ctx.Err())
	}
}

type relationWinnerProbeDB struct {
	pool           *pgxpool.Pool
	relationLookup chan struct{}
	allowLookup    chan struct{}
	lookupOnce     sync.Once
	releaseOnce    sync.Once
}

func newRelationWinnerProbeDB(pool *pgxpool.Pool) *relationWinnerProbeDB {
	return &relationWinnerProbeDB{pool: pool, relationLookup: make(chan struct{}, 1), allowLookup: make(chan struct{})}
}

func (db *relationWinnerProbeDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &relationWinnerProbeTx{Tx: tx, db: db}, nil
}

func (db *relationWinnerProbeDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return db.pool.Query(ctx, query, args...)
}

func (db *relationWinnerProbeDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return db.pool.QueryRow(ctx, query, args...)
}

type relationWinnerProbeTx struct {
	pgx.Tx
	db *relationWinnerProbeDB
}

func (tx *relationWinnerProbeTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "FROM core.relation") && strings.Contains(query, "fingerprint=$2 FOR UPDATE") {
		intercept := false
		tx.db.lookupOnce.Do(func() { intercept = true })
		if intercept {
			return &relationWinnerProbeRow{Row: tx.Tx.QueryRow(ctx, query, args...), db: tx.db, ctx: ctx}
		}
	}
	return tx.Tx.QueryRow(ctx, query, args...)
}

type relationWinnerProbeRow struct {
	pgx.Row
	db  *relationWinnerProbeDB
	ctx context.Context
}

func (row *relationWinnerProbeRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	row.db.relationLookup <- struct{}{}
	select {
	case <-row.db.allowLookup:
	case <-row.ctx.Done():
	}
	return err
}

func (db *relationWinnerProbeDB) release() {
	db.releaseOnce.Do(func() { close(db.allowLookup) })
}

func cleanupApprovalApplyWorkspace(pool *pgxpool.Pool, workspaceID foundation.ID) {
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM ops.server_event WHERE workspace_id=$1`, string(workspaceID))
	_, _ = pool.Exec(ctx, `DELETE FROM core.knowledge_command_receipt WHERE workspace_id=$1`, string(workspaceID))
	_, _ = pool.Exec(ctx, `DELETE FROM change_control.approval WHERE proposal_id IN (SELECT id FROM change_control.proposal WHERE workspace_id=$1)`, string(workspaceID))
	_, _ = pool.Exec(ctx, `DELETE FROM change_control.proposal_revision WHERE proposal_id IN (SELECT id FROM change_control.proposal WHERE workspace_id=$1)`, string(workspaceID))
	_, _ = pool.Exec(ctx, `DELETE FROM change_control.proposal WHERE workspace_id=$1`, string(workspaceID))
	cleanupCandidateWorkspace(pool, workspaceID)
}

type approvalLockProbeDB struct {
	pool             *pgxpool.Pool
	candidateAttempt chan struct{}
	candidateOnce    sync.Once
}

func newApprovalLockProbeDB(pool *pgxpool.Pool) *approvalLockProbeDB {
	return &approvalLockProbeDB{pool: pool, candidateAttempt: make(chan struct{})}
}

func (db *approvalLockProbeDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &approvalLockProbeTx{Tx: tx, db: db}, nil
}

func (db *approvalLockProbeDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return db.pool.Query(ctx, query, args...)
}

func (db *approvalLockProbeDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return db.pool.QueryRow(ctx, query, args...)
}

type approvalLockProbeTx struct {
	pgx.Tx
	db *approvalLockProbeDB
}

func (tx *approvalLockProbeTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "FROM graph.semantic_link_candidate") && strings.Contains(query, "FOR UPDATE") {
		tx.db.candidateOnce.Do(func() { close(tx.db.candidateAttempt) })
	}
	return tx.Tx.QueryRow(ctx, query, args...)
}

type approvedCandidateApplyFixture struct {
	ctx         context.Context
	tx          pgx.Tx
	now         time.Time
	workspaceID foundation.ID
	approvalID  foundation.ID
	provenance  graphProvenance
	candidate   graphdomain.SemanticLinkCandidate
	proposal    changecontroldomain.Proposal
	approval    changecontroldomain.Approval
	command     knowledgeapplication.ApprovedRelationApplyCommand
}

func prepareApprovedCandidateApply(t *testing.T, label string) approvedCandidateApplyFixture {
	t.Helper()
	fixture := prepareCandidateApplyProposal(t, label)
	changeRepository, err := changecontrolpostgres.NewRepository(fixture.tx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changeRepository.Approve(fixture.ctx, fixture.approval); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func prepareCandidateApplyProposal(t *testing.T, label string) approvedCandidateApplyFixture {
	t.Helper()
	graphRepository, tx, ctx := graphIntegrationRepository(t)
	return prepareCandidateApplyProposalOnTx(t, graphRepository, tx, ctx, label)
}

func prepareCandidateApplyProposalOnTx(t *testing.T, graphRepository *Repository, tx pgx.Tx, ctx context.Context, label string) approvedCandidateApplyFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	sourceID := seedGraphClaim(t, ctx, tx, workspaceID, label+" source", knowledge.ClaimStatusSuggested, floatPointer(0.9), now)
	targetID := seedGraphClaim(t, ctx, tx, workspaceID, label+" target", knowledge.ClaimStatusSuggested, floatPointer(0.8), now)
	candidate := candidateFixture(t, workspaceID, sourceID, targetID, provenance, now, label, 1, 1)
	if _, err := graphRepository.UpsertSemanticLinkCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	confirmer, err := NewCandidateConfirmRepository(tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := confirmer.Confirm(ctx, candidateconfirm.Command{
		WorkspaceID: workspaceID, CandidateID: candidate.ID, ExpectedVersion: candidate.Version,
		IdempotencyKey: "confirm-" + label, Action: graphdomain.SemanticLinkCandidateDecisionConfirm,
		RiskLevel: candidateconfirm.ProposalRiskLevel,
		Risk:      "medium relation change", RollbackPlan: "create a corrective relation proposal",
	})
	if err != nil {
		t.Fatal(err)
	}
	approvalID := graphTestID(t)
	approval := changecontroldomain.Approval{
		ID: approvalID, ProposalID: confirmed.Proposal.ID, RevisionID: confirmed.Proposal.Revision.ID,
		ChangeHash: confirmed.Proposal.Revision.ChangeHash, Decision: changecontroldomain.DecisionApproved,
		DecidedAt: now.Add(2 * time.Second),
	}
	return approvedCandidateApplyFixture{
		ctx: ctx, tx: tx, now: now, workspaceID: workspaceID, approvalID: approvalID,
		provenance: provenance, candidate: candidate, proposal: confirmed.Proposal, approval: approval,
		command: knowledgeapplication.ApprovedRelationApplyCommand{
			WorkspaceID: workspaceID, ProposalID: confirmed.Proposal.ID,
			RevisionID: confirmed.Proposal.Revision.ID, ApprovalID: approvalID,
		},
	}
}

func newCandidateApplyRelation(t *testing.T, workspaceID foundation.ID, relationType knowledge.RelationType, source, target knowledge.NodeRef, at time.Time) knowledge.Relation {
	t.Helper()
	canonicalSource, canonicalTarget, err := knowledge.CanonicalizeRelationEndpoints(relationType, source, target)
	if err != nil {
		t.Fatal(err)
	}
	relation := knowledge.Relation{
		ID: graphTestID(t), WorkspaceID: workspaceID, Source: canonicalSource, Target: canonicalTarget,
		Type: relationType, Status: knowledge.RelationStatusSuggested, Version: 1, CreatedAt: at, UpdatedAt: at,
	}
	relation.Fingerprint = knowledge.ComputeRelationFingerprint(workspaceID, relationType, canonicalSource, canonicalTarget)
	return relation
}

func newCandidateApplyEvidence(t *testing.T, workspaceID foundation.ID, reason string, provenance graphProvenance, at time.Time, confirmation *knowledge.Confirmation) knowledge.RelationEvidence {
	t.Helper()
	applicability, err := knowledge.ParseApplicability([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return knowledge.RelationEvidence{
		ID: graphTestID(t), WorkspaceID: workspaceID,
		Provenance: knowledge.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: provenance.sourceVersionID, SourceSpanID: provenance.sourceSpanID},
		Reason:     reason, Applicability: applicability, Confirmation: confirmation, CreatedAt: at,
	}
}

func assertAtomicApprovalApplyRows(t *testing.T, fixture approvedCandidateApplyFixture, expectedStatus changecontroldomain.ProposalStatus, expectedVersion, approvalCount, relationCount, receiptCount int) {
	t.Helper()
	var status string
	var version, approvals, relations, receipts int
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT status,version FROM change_control.proposal WHERE id=$1`, string(fixture.proposal.ID)).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM change_control.approval WHERE revision_id=$1`, string(fixture.proposal.Revision.ID)).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.relation WHERE workspace_id=$1`, string(fixture.workspaceID)).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1`, string(fixture.workspaceID)).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if status != string(expectedStatus) || version != expectedVersion || approvals != approvalCount || relations != relationCount || receipts != receiptCount {
		t.Fatalf("status=%s version=%d approval=%d relation=%d receipt=%d", status, version, approvals, relations, receipts)
	}
}

func assertProposalEventCount(t *testing.T, fixture approvedCandidateApplyFixture, eventType string, expected int) {
	t.Helper()
	var count int
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM ops.server_event
		WHERE workspace_id=$1 AND event_type=$2 AND resource_ref=$3`,
		string(fixture.workspaceID), eventType, "proposal:"+string(fixture.proposal.ID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("proposal event type=%s count=%d want=%d", eventType, count, expected)
	}
}

func assertProposalEventBinding(t *testing.T, fixture approvedCandidateApplyFixture, eventType string, expectedStatus changecontroldomain.ProposalStatus, expectedVersion int64, expectedAt time.Time) {
	t.Helper()
	var resourceVersion int64
	var sourceRef, status string
	var occurredAt time.Time
	err := fixture.tx.QueryRow(fixture.ctx, `
		SELECT resource_version,source_event_ref,payload_summary->>'status',occurred_at
		FROM ops.server_event
		WHERE workspace_id=$1 AND event_type=$2 AND resource_ref=$3
		ORDER BY seq`,
		string(fixture.workspaceID), eventType, "proposal:"+string(fixture.proposal.ID)).Scan(
		&resourceVersion, &sourceRef, &status, &occurredAt)
	if err != nil {
		t.Fatal(err)
	}
	expectedSourceRef := eventType + ":" + string(fixture.approvalID) + ":v1"
	if resourceVersion != expectedVersion || sourceRef != expectedSourceRef || status != string(expectedStatus) || !occurredAt.Equal(expectedAt.UTC()) {
		t.Fatalf("proposal event type=%s version=%d source=%s status=%s occurred_at=%s", eventType, resourceVersion, sourceRef, status, occurredAt.UTC().Format(time.RFC3339Nano))
	}
}

func assertRelationApplyReceiptBinding(t *testing.T, fixture approvedCandidateApplyFixture, result knowledgeapplication.ApprovedRelationApplyResult) {
	t.Helper()
	requestHash, err := knowledgeapplication.RelationApplyRequestHash(fixture.command)
	if err != nil {
		t.Fatal(err)
	}
	var idempotencyKey, persistedHash, commandType, aggregateType, aggregateID string
	var aggregateVersion int64
	err = fixture.tx.QueryRow(fixture.ctx, `
		SELECT idempotency_key,request_hash,command_type,aggregate_type,aggregate_id::text,aggregate_version
		FROM core.knowledge_command_receipt
		WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(fixture.workspaceID), knowledgeapplication.RelationApplyIdempotencyKey(fixture.approvalID)).Scan(
		&idempotencyKey, &persistedHash, &commandType, &aggregateType, &aggregateID, &aggregateVersion)
	if err != nil {
		t.Fatal(err)
	}
	expectedKey := knowledgeapplication.RelationApplyIdempotencyKey(fixture.approvalID)
	if idempotencyKey != expectedKey || persistedHash != requestHash || commandType != string(knowledge.CommandConfirmRelation) ||
		aggregateType != string(knowledge.AggregateRelation) || aggregateID != string(result.Relation.ID) || aggregateVersion != result.Relation.Version {
		t.Fatalf("relation apply receipt key=%s hash=%s command=%s aggregate=%s/%s@%d result=%s@%d", idempotencyKey, persistedHash, commandType, aggregateType, aggregateID, aggregateVersion, result.Relation.ID, result.Relation.Version)
	}
}

func assertApprovedRelationApplyResult(t *testing.T, result knowledgeapplication.ApprovedRelationApplyResult, fixture approvedCandidateApplyFixture, replayed bool) {
	t.Helper()
	if result.Replayed != replayed || result.Relation.ID == "" || result.Relation.WorkspaceID != fixture.workspaceID ||
		result.Relation.Status != knowledge.RelationStatusConfirmed || result.Relation.Type != fixture.candidate.SuggestedRelationType ||
		result.Relation.Source != fixture.candidate.Source.Ref || result.Relation.Target != fixture.candidate.Target.Ref ||
		result.Relation.Confirmation == nil || result.Relation.Confirmation.Method != knowledge.ConfirmationUserApproval ||
		result.Relation.Confirmation.Reference != string(fixture.approvalID) || result.ProposalStatus != changecontroldomain.StatusApplied ||
		result.ProposalVersion != 4 || len(result.Evidence) != len(fixture.candidate.Evidence) {
		t.Fatalf("approved relation apply result = %#v", result)
	}
	for _, evidence := range result.Evidence {
		if evidence.Confirmation == nil || *evidence.Confirmation != *result.Relation.Confirmation ||
			evidence.Applicability.SchemaVersion != knowledge.ApplicabilitySchemaV1 || string(evidence.Applicability.CanonicalJSON) != `{}` {
			t.Fatalf("confirmed evidence = %#v", evidence)
		}
	}
}

func assertNeedsRevisionWithoutRelation(t *testing.T, fixture approvedCandidateApplyFixture) {
	t.Helper()
	var status string
	var version, relationCount, receiptCount int
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT status,version FROM change_control.proposal WHERE id=$1`, string(fixture.proposal.ID)).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.relation WHERE workspace_id=$1`, string(fixture.workspaceID)).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1`, string(fixture.workspaceID)).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if status != string(changecontroldomain.StatusNeedsRevision) || version != 3 || relationCount != 0 || receiptCount != 0 {
		t.Fatalf("status=%s version=%d relation=%d receipt=%d", status, version, relationCount, receiptCount)
	}
}

func hasCandidateApplyCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}

type relationApplyFailBeginner struct {
	pgx.Tx
	err error
}

func (beginner relationApplyFailBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := beginner.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &relationApplyFailTx{Tx: tx, err: beginner.err}, nil
}

type relationApplyFailTx struct {
	pgx.Tx
	err error
}

type selectiveFailProposalEventAppender struct {
	eventType string
	err       error
}

func (appender selectiveFailProposalEventAppender) AppendTx(_ context.Context, _ any, request eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error) {
	if request.Type == appender.eventType {
		return eventsdomain.ServerEvent{}, false, appender.err
	}
	return eventsdomain.ServerEvent{}, false, nil
}

func (tx *relationApplyFailTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "INSERT INTO core.relation_evidence") {
		return pgconn.CommandTag{}, tx.err
	}
	return tx.Tx.Exec(ctx, query, args...)
}
