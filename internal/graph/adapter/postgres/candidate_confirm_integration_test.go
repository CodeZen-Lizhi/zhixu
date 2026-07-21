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
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/candidateconfirm"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

func TestCandidateConfirmCreatesIndependentTypedProposalsAndExactlyReplays(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	firstSource := seedGraphClaim(t, ctx, tx, workspaceID, "confirm source", knowledge.ClaimStatusSuggested, floatPointer(0.9), now)
	firstTarget := seedGraphClaim(t, ctx, tx, workspaceID, "confirm target", knowledge.ClaimStatusSuggested, floatPointer(0.8), now)
	secondSource := seedGraphClaim(t, ctx, tx, workspaceID, "typed source", knowledge.ClaimStatusSuggested, floatPointer(0.9), now)
	secondTarget := seedGraphClaim(t, ctx, tx, workspaceID, "typed target", knowledge.ClaimStatusSuggested, floatPointer(0.8), now)
	regularCandidate := candidateFixture(t, workspaceID, firstSource, firstTarget, provenance, now, "confirm-uow", 1, 1)
	typedCandidate := candidateFixture(t, workspaceID, secondSource, secondTarget, provenance, now, "typed-confirm-uow", 1, 1)
	if _, err := repository.UpsertSemanticLinkCandidate(ctx, regularCandidate); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertSemanticLinkCandidate(ctx, typedCandidate); err != nil {
		t.Fatal(err)
	}

	confirmer, err := NewCandidateConfirmRepository(tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	regularCommand := candidateconfirm.Command{
		WorkspaceID: workspaceID, CandidateID: regularCandidate.ID, ExpectedVersion: 1,
		IdempotencyKey: "confirm-regular", Action: graphdomain.SemanticLinkCandidateDecisionConfirm,
		Risk: "medium relation change", RollbackPlan: "create a corrective relation proposal",
	}
	regular, err := confirmer.Confirm(ctx, regularCommand)
	if err != nil {
		t.Fatal(err)
	}
	assertCandidateConfirmResult(t, regular, regularCandidate, knowledge.RelationComplements, false)

	typedRelation := knowledge.RelationSupports
	typedCommand := candidateconfirm.Command{
		WorkspaceID: workspaceID, CandidateID: typedCandidate.ID, ExpectedVersion: 1,
		IdempotencyKey: "confirm-typed", Action: graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType,
		RelationType: &typedRelation, Risk: "medium relation change", RollbackPlan: "create a corrective relation proposal",
	}
	typed, err := confirmer.Confirm(ctx, typedCommand)
	if err != nil {
		t.Fatal(err)
	}
	assertCandidateConfirmResult(t, typed, typedCandidate, knowledge.RelationSupports, false)
	if regular.Proposal.ID == typed.Proposal.ID || regular.Proposal.Revision.ID == typed.Proposal.Revision.ID {
		t.Fatalf("confirmations shared Proposal identity: regular=%s typed=%s", regular.Proposal.ID, typed.Proposal.ID)
	}
	if typed.Candidate.SuggestedRelationType != typedCandidate.SuggestedRelationType || typed.Proposal.Revision.KnowledgeChange.ChangeSet.RelationType != knowledge.RelationSupports {
		t.Fatalf("typed confirm mutated candidate or lost override: candidate=%s proposal=%s", typed.Candidate.SuggestedRelationType, typed.Proposal.Revision.KnowledgeChange.ChangeSet.RelationType)
	}

	replayed, err := confirmer.Confirm(ctx, regularCommand)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Proposal.ID != regular.Proposal.ID || replayed.Proposal.Revision.ID != regular.Proposal.Revision.ID || replayed.DecisionID != regular.DecisionID || replayed.Proposal.Revision.ChangeHash != regular.Proposal.Revision.ChangeHash {
		t.Fatalf("exact replay = %#v, first = %#v", replayed, regular)
	}
	conflict := regularCommand
	conflict.Risk = "different risk"
	if _, err := confirmer.Confirm(ctx, conflict); !hasCandidateConfirmCode(err, "RELATION_PROPOSAL_CONFIRM_CONFLICT") {
		t.Fatalf("same-key different payload error = %v", err)
	}
	changeControlRepository, err := changecontrolpostgres.NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	approvalID := graphTestID(t)
	if _, err := changeControlRepository.Approve(ctx, changecontroldomain.Approval{
		ID: approvalID, ProposalID: regular.Proposal.ID, RevisionID: regular.Proposal.Revision.ID,
		ChangeHash: regular.Proposal.Revision.ChangeHash, Decision: changecontroldomain.DecisionApproved, DecidedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	afterApproval, err := confirmer.Confirm(ctx, regularCommand)
	if err != nil {
		t.Fatal(err)
	}
	if !afterApproval.Replayed || afterApproval.Proposal.ID != regular.Proposal.ID || afterApproval.Proposal.Status != changecontroldomain.StatusApproved || afterApproval.Proposal.Approval == nil || afterApproval.Proposal.Approval.ID != approvalID || afterApproval.Proposal.WorkflowRunID != nil {
		t.Fatalf("approved Proposal replay = %#v", afterApproval)
	}

	var proposalCount, revisionCount, decisionCount, relationCount, writebackCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1 AND proposal_type='knowledge_change'`, string(workspaceID)).Scan(&proposalCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal_revision r JOIN change_control.proposal p ON p.id=r.proposal_id WHERE p.workspace_id=$1`, string(workspaceID)).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_candidate_decision WHERE workspace_id=$1`, string(workspaceID)).Scan(&decisionCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.relation WHERE workspace_id=$1`, string(workspaceID)).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM change_control.writeback_execution WHERE proposal_id IN ($1,$2)`, string(regular.Proposal.ID), string(typed.Proposal.ID)).Scan(&writebackCount); err != nil {
		t.Fatal(err)
	}
	if proposalCount != 2 || revisionCount != 2 || decisionCount != 2 || relationCount != 0 || writebackCount != 0 || regular.Proposal.WorkflowRunID != nil || typed.Proposal.WorkflowRunID != nil {
		t.Fatalf("proposal=%d revision=%d decision=%d relation=%d writeback=%d", proposalCount, revisionCount, decisionCount, relationCount, writebackCount)
	}
}

func TestCandidateConfirmRollsBackProposalWhenDecisionFails(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	sourceID := seedGraphClaim(t, ctx, tx, workspaceID, "rollback source", knowledge.ClaimStatusSuggested, floatPointer(0.9), now)
	targetID := seedGraphClaim(t, ctx, tx, workspaceID, "rollback target", knowledge.ClaimStatusSuggested, floatPointer(0.8), now)
	candidate := candidateFixture(t, workspaceID, sourceID, targetID, provenance, now, "rollback-confirm", 1, 1)
	if _, err := repository.UpsertSemanticLinkCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	command := candidateconfirm.Command{
		WorkspaceID: workspaceID, CandidateID: candidate.ID, ExpectedVersion: 1,
		IdempotencyKey: "confirm-rollback", Action: graphdomain.SemanticLinkCandidateDecisionConfirm,
		Risk: "medium relation change", RollbackPlan: "create a corrective relation proposal",
	}
	injected := errors.New("injected candidate decision failure")
	failing, err := NewCandidateConfirmRepository(candidateConfirmFailBeginner{Tx: tx, err: injected}, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.Confirm(ctx, command); !errors.Is(err, injected) {
		t.Fatalf("confirm failure = %v", err)
	}
	assertNoCandidateConfirmPartialRows(t, ctx, tx, workspaceID, candidate.ID)

	normal, err := NewCandidateConfirmRepository(tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := normal.Confirm(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	assertCandidateConfirmResult(t, result, candidate, knowledge.RelationComplements, false)
}

func TestCandidateConfirmRecoversCommitResponseLossByExactReceipt(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	sourceID := seedGraphClaim(t, ctx, tx, workspaceID, "response loss source", knowledge.ClaimStatusSuggested, floatPointer(0.9), now)
	targetID := seedGraphClaim(t, ctx, tx, workspaceID, "response loss target", knowledge.ClaimStatusSuggested, floatPointer(0.8), now)
	candidate := candidateFixture(t, workspaceID, sourceID, targetID, provenance, now, "response-loss-confirm", 1, 1)
	if _, err := repository.UpsertSemanticLinkCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	command := candidateconfirm.Command{
		WorkspaceID: workspaceID, CandidateID: candidate.ID, ExpectedVersion: 1,
		IdempotencyKey: "confirm-response-loss", Action: graphdomain.SemanticLinkCandidateDecisionConfirm,
		Risk: "medium relation change", RollbackPlan: "create a corrective relation proposal",
	}
	injected := errors.New("injected commit response loss")
	lost, err := NewCandidateConfirmRepository(candidateConfirmCommitLossBeginner{Tx: tx, err: injected}, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lost.Confirm(ctx, command); !errors.Is(err, injected) {
		t.Fatalf("commit response loss error = %v", err)
	}
	var persistedProposalID string
	if err := tx.QueryRow(ctx, `SELECT current_proposal_id::text FROM graph.semantic_link_candidate WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(candidate.ID)).Scan(&persistedProposalID); err != nil {
		t.Fatal(err)
	}

	normal, err := NewCandidateConfirmRepository(tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := normal.Confirm(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || string(replayed.Proposal.ID) != persistedProposalID {
		t.Fatalf("response-loss replay = %#v, persisted proposal = %s", replayed, persistedProposalID)
	}
	var proposalCount, revisionCount, decisionCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1`, string(workspaceID)).Scan(&proposalCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal_revision r JOIN change_control.proposal p ON p.id=r.proposal_id WHERE p.workspace_id=$1`, string(workspaceID)).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_candidate_decision WHERE workspace_id=$1 AND candidate_id=$2`, string(workspaceID), string(candidate.ID)).Scan(&decisionCount); err != nil {
		t.Fatal(err)
	}
	if proposalCount != 1 || revisionCount != 1 || decisionCount != 1 {
		t.Fatalf("response-loss proposal=%d revision=%d decision=%d", proposalCount, revisionCount, decisionCount)
	}
}

func assertCandidateConfirmResult(t *testing.T, result candidateconfirm.Result, original graphdomain.SemanticLinkCandidate, relationType knowledge.RelationType, replayed bool) {
	t.Helper()
	if result.Replayed != replayed || result.DecisionID == "" || result.Candidate.ID != original.ID || result.Candidate.Status != graphdomain.SemanticLinkCandidateStatusProposalCreated || result.Candidate.Version != original.Version+1 || result.Candidate.ProposalID == nil || *result.Candidate.ProposalID != result.Proposal.ID {
		t.Fatalf("candidate confirm result = %#v", result)
	}
	proposal := result.Proposal
	if proposal.Type != changecontroldomain.ProposalTypeKnowledgeChange || proposal.Status != changecontroldomain.StatusReady || proposal.Version != 1 || proposal.TargetPath != "" || proposal.WorkflowRunID != nil || proposal.Revision.TargetPath != "" || proposal.Revision.BaseHash != "" || proposal.Revision.Content != "" || proposal.Revision.EvidenceSummary != "" || proposal.Revision.KnowledgeChange == nil {
		t.Fatalf("typed proposal = %#v", proposal)
	}
	change := proposal.Revision.KnowledgeChange
	if len(change.TargetRefs) != 1 || change.TargetRefs[0].ID != original.ID || change.TargetRefs[0].Fingerprint != original.Fingerprint || len(change.EvidenceRefs) != len(original.Evidence) || change.ChangeSet.RelationType != relationType || change.ChangeSet.Source != original.Source.Ref || change.ChangeSet.Target != original.Target.Ref {
		t.Fatalf("knowledge change = %#v", change)
	}
	if hash, err := changecontroldomain.ComputeKnowledgeChangeHash(*change, proposal.Revision.Risk, proposal.Revision.RollbackPlan); err != nil || hash != proposal.Revision.ChangeHash {
		t.Fatalf("change hash = %s, persisted = %s, err = %v", hash, proposal.Revision.ChangeHash, err)
	}
}

func assertNoCandidateConfirmPartialRows(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, candidateID foundation.ID) {
	t.Helper()
	var proposalCount, revisionCount, decisionCount int
	var status string
	var version int64
	var proposalID *string
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1`, string(workspaceID)).Scan(&proposalCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal_revision r JOIN change_control.proposal p ON p.id=r.proposal_id WHERE p.workspace_id=$1`, string(workspaceID)).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_candidate_decision WHERE workspace_id=$1`, string(workspaceID)).Scan(&decisionCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT status,version,current_proposal_id::text FROM graph.semantic_link_candidate WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(candidateID)).Scan(&status, &version, &proposalID); err != nil {
		t.Fatal(err)
	}
	if proposalCount != 0 || revisionCount != 0 || decisionCount != 0 || status != string(graphdomain.SemanticLinkCandidateStatusActive) || version != 1 || proposalID != nil {
		t.Fatalf("partial rows proposal=%d revision=%d decision=%d candidate=%s/%d/%v", proposalCount, revisionCount, decisionCount, status, version, proposalID)
	}
}

func hasCandidateConfirmCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}

type candidateConfirmFailBeginner struct {
	pgx.Tx
	err error
}

func (beginner candidateConfirmFailBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := beginner.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &candidateConfirmFailTx{Tx: tx, err: beginner.err}, nil
}

type candidateConfirmFailTx struct {
	pgx.Tx
	err error
}

func (tx *candidateConfirmFailTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "INSERT INTO graph.semantic_link_candidate_decision") {
		return candidateConfirmErrorRow{err: tx.err}
	}
	return tx.Tx.QueryRow(ctx, query, args...)
}

type candidateConfirmCommitLossBeginner struct {
	pgx.Tx
	err error
}

func (beginner candidateConfirmCommitLossBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := beginner.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &candidateConfirmCommitLossTx{Tx: tx, err: beginner.err}, nil
}

type candidateConfirmCommitLossTx struct {
	pgx.Tx
	err error
}

func (tx *candidateConfirmCommitLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return tx.err
}

type candidateConfirmErrorRow struct{ err error }

func (row candidateConfirmErrorRow) Scan(...any) error { return row.err }
