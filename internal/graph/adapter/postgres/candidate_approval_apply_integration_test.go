//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	eventcontract "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/eventcontract"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/candidateconfirm"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

type approvedCandidateApplyFixture struct {
	ctx         context.Context
	tx          pgx.Tx
	now         time.Time
	workspaceID foundation.ID
	approvalID  foundation.ID
	provenance  graphProvenance
	candidate   graphdomain.SemanticLinkCandidate
	proposal    changecontroldomain.Proposal
	command     knowledgeapplication.ApprovedRelationApplyCommand
}

func prepareApprovedCandidateApply(t *testing.T, label string) approvedCandidateApplyFixture {
	t.Helper()
	graphRepository, tx, ctx := graphIntegrationRepository(t)
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
	changeRepository, err := changecontrolpostgres.NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	approvalID := graphTestID(t)
	if _, err := changeRepository.Approve(ctx, changecontroldomain.Approval{
		ID: approvalID, ProposalID: confirmed.Proposal.ID, RevisionID: confirmed.Proposal.Revision.ID,
		ChangeHash: confirmed.Proposal.Revision.ChangeHash, Decision: changecontroldomain.DecisionApproved,
		DecidedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	return approvedCandidateApplyFixture{
		ctx: ctx, tx: tx, now: now, workspaceID: workspaceID, approvalID: approvalID,
		provenance: provenance, candidate: candidate, proposal: confirmed.Proposal,
		command: knowledgeapplication.ApprovedRelationApplyCommand{
			WorkspaceID: workspaceID, ProposalID: confirmed.Proposal.ID,
			RevisionID: confirmed.Proposal.Revision.ID, ApprovalID: approvalID,
		},
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

func (tx *relationApplyFailTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "INSERT INTO core.relation_evidence") {
		return pgconn.CommandTag{}, tx.err
	}
	return tx.Tx.Exec(ctx, query, args...)
}
