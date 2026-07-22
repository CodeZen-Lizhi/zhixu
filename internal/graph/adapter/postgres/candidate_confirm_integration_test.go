//go:build integration

package postgres

import (
	"context"
	"encoding/json"
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
		RiskLevel: candidateconfirm.ProposalRiskLevel,
		Risk:      "medium relation change", RollbackPlan: "create a corrective relation proposal",
	}
	regular, err := confirmer.Confirm(ctx, regularCommand)
	if err != nil {
		t.Fatal(err)
	}
	assertCandidateConfirmResult(t, regular, regularCandidate, knowledge.RelationComplements, false)
	assertCandidateConfirmV2Binding(t, ctx, tx, regularCommand, regular)

	typedRelation := knowledge.RelationSupports
	typedCommand := candidateconfirm.Command{
		WorkspaceID: workspaceID, CandidateID: typedCandidate.ID, ExpectedVersion: 1,
		IdempotencyKey: "confirm-typed", Action: graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType,
		RelationType: &typedRelation, RiskLevel: candidateconfirm.ProposalRiskLevel,
		Risk: "medium relation change", RollbackPlan: "create a corrective relation proposal",
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
		RiskLevel: candidateconfirm.ProposalRiskLevel,
		Risk:      "medium relation change", RollbackPlan: "create a corrective relation proposal",
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
		RiskLevel: candidateconfirm.ProposalRiskLevel,
		Risk:      "medium relation change", RollbackPlan: "create a corrective relation proposal",
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

func TestCandidateConfirmExactlyReplaysLegacyV1ReceiptAndProposal(t *testing.T) {
	confirmer, tx, ctx, candidate, command := candidateConfirmReplayFixture(t, "legacy-v1")
	command.RiskLevel = ""
	command.Risk = "legacy relation risk"
	proposalID, revisionID, decisionID := seedCandidateConfirmRecord(
		t, ctx, tx, candidate, command, true, candidateconfirm.ProposalRiskLevel, command.Risk,
	)

	replayed, err := confirmer.Confirm(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Proposal.ID != proposalID || replayed.Proposal.Revision.ID != revisionID || replayed.DecisionID != decisionID || replayed.Proposal.RiskLevel != candidateconfirm.ProposalRiskLevel || replayed.Proposal.Revision.Risk != command.Risk {
		t.Fatalf("legacy replay = %#v", replayed)
	}
	legacyCommandHash, err := candidateconfirm.LegacyRequestHash(command)
	if err != nil {
		t.Fatal(err)
	}
	legacyProposalHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHash(command.WorkspaceID, *replayed.Proposal.Revision.KnowledgeChange, command.Risk, command.RollbackPlan)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Proposal.IdempotencyKey != "semantic-link-confirm/v1:"+legacyCommandHash || replayed.Proposal.RequestHash != legacyProposalHash {
		t.Fatalf("legacy proposal binding = %#v", replayed.Proposal)
	}
}

func TestCandidateConfirmV2ReplaysHistoricalV1ReceiptAndProposal(t *testing.T) {
	confirmer, tx, ctx, candidate, command := candidateConfirmReplayFixture(t, "v2-replays-v1")
	command.Risk = "historical free-text risk"
	proposalID, revisionID, decisionID := seedCandidateConfirmRecord(
		t, ctx, tx, candidate, command, true, candidateconfirm.ProposalRiskLevel, command.Risk,
	)

	replayed, err := confirmer.Confirm(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Proposal.ID != proposalID || replayed.Proposal.Revision.ID != revisionID || replayed.DecisionID != decisionID || replayed.Proposal.RiskLevel != candidateconfirm.ProposalRiskLevel {
		t.Fatalf("v2 replay of historical v1 = %#v", replayed)
	}
}

func TestCandidateConfirmV1CannotReplayPersistedV2(t *testing.T) {
	confirmer, tx, ctx, candidate, command := candidateConfirmReplayFixture(t, "v1-rejects-v2")
	seedCandidateConfirmRecord(t, ctx, tx, candidate, command, false, candidateconfirm.ProposalRiskLevel, command.Risk)
	command.RiskLevel = ""

	if _, err := confirmer.Confirm(ctx, command); !hasCandidateConfirmCode(err, "RELATION_PROPOSAL_CONFIRM_CONFLICT") {
		t.Fatalf("v1 reverse replay error = %v", err)
	}
}

func TestCandidateConfirmLegacyReplayRejectsWhitespaceRiskLevel(t *testing.T) {
	confirmer, tx, ctx, candidate, command := candidateConfirmReplayFixture(t, "v1-whitespace-risk")
	seedCandidateConfirmRecord(t, ctx, tx, candidate, command, true, candidateconfirm.ProposalRiskLevel, command.Risk)
	command.RiskLevel = " "

	if _, err := confirmer.Confirm(ctx, command); !hasCandidateConfirmCode(err, "RELATION_PROPOSAL_CONFIRM_INVALID") {
		t.Fatalf("whitespace legacy risk level error = %v", err)
	}
}

func TestCandidateConfirmNewLegacyV1CannotCreate(t *testing.T) {
	confirmer, _, ctx, _, command := candidateConfirmReplayFixture(t, "new-v1-rejected")
	command.RiskLevel = ""

	if _, err := confirmer.Confirm(ctx, command); !hasCandidateConfirmCode(err, "RELATION_PROPOSAL_CONFIRM_INVALID") {
		t.Fatalf("new v1 creation error = %v", err)
	}
}

func TestCandidateConfirmRejectsPersistedNonHighRiskLevelForV1AndV2(t *testing.T) {
	tests := []struct {
		name   string
		legacy bool
	}{
		{name: "v1", legacy: true},
		{name: "v2", legacy: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			confirmer, tx, ctx, candidate, command := candidateConfirmReplayFixture(t, "risk-binding-"+test.name)
			command.Risk = "independent reviewer description"
			proposalID, _, _ := seedCandidateConfirmRecord(
				t, ctx, tx, candidate, command, test.legacy, changecontroldomain.ProposalRiskLevelMedium, command.Risk,
			)
			assertCandidateConfirmRiskFacts(t, ctx, tx, proposalID, changecontroldomain.ProposalRiskLevelMedium, command.Risk)

			if _, err := confirmer.Confirm(ctx, command); !hasCandidateConfirmCode(err, "RELATION_PROPOSAL_CONFIRM_CONSISTENCY") {
				t.Fatalf("risk-level binding error = %v", err)
			}
		})
	}
}

func assertCandidateConfirmResult(t *testing.T, result candidateconfirm.Result, original graphdomain.SemanticLinkCandidate, relationType knowledge.RelationType, replayed bool) {
	t.Helper()
	if result.Replayed != replayed || result.DecisionID == "" || result.Candidate.ID != original.ID || result.Candidate.Status != graphdomain.SemanticLinkCandidateStatusProposalCreated || result.Candidate.Version != original.Version+1 || result.Candidate.ProposalID == nil || *result.Candidate.ProposalID != result.Proposal.ID {
		t.Fatalf("candidate confirm result = %#v", result)
	}
	proposal := result.Proposal
	if proposal.Type != changecontroldomain.ProposalTypeKnowledgeChange || proposal.RiskLevel != candidateconfirm.ProposalRiskLevel || proposal.Status != changecontroldomain.StatusReady || proposal.Version != 1 || proposal.TargetPath != "" || proposal.WorkflowRunID != nil || proposal.Revision.TargetPath != "" || proposal.Revision.BaseHash != "" || proposal.Revision.Content != "" || proposal.Revision.EvidenceSummary != "" || proposal.Revision.KnowledgeChange == nil {
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

func assertCandidateConfirmV2Binding(t *testing.T, ctx context.Context, tx pgx.Tx, command candidateconfirm.Command, result candidateconfirm.Result) {
	t.Helper()
	commandHash, err := candidateconfirm.RequestHash(command)
	if err != nil {
		t.Fatal(err)
	}
	proposalHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
		command.WorkspaceID,
		*result.Proposal.Revision.KnowledgeChange,
		command.RiskLevel,
		command.Risk,
		command.RollbackPlan,
	)
	if err != nil {
		t.Fatal(err)
	}
	var persistedRiskLevel, riskDescription, proposalKey, persistedProposalHash, receiptHash string
	if err := tx.QueryRow(ctx, `
		SELECT p.risk_level,r.risk,p.idempotency_key,p.request_hash,d.request_hash
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.revision_no=1
		JOIN graph.semantic_link_candidate_decision d ON d.proposal_id=p.id
		WHERE p.id=$1 AND d.id=$2`, string(result.Proposal.ID), string(result.DecisionID)).Scan(
		&persistedRiskLevel, &riskDescription, &proposalKey, &persistedProposalHash, &receiptHash); err != nil {
		t.Fatal(err)
	}
	if changecontroldomain.ProposalRiskLevel(persistedRiskLevel) != candidateconfirm.ProposalRiskLevel || riskDescription != command.Risk || proposalKey != "semantic-link-confirm/v2:"+commandHash || persistedProposalHash != proposalHash || receiptHash != commandHash {
		t.Fatalf("v2 binding risk_level=%s description=%s key=%s proposal_hash=%s receipt_hash=%s", persistedRiskLevel, riskDescription, proposalKey, persistedProposalHash, receiptHash)
	}
}

func candidateConfirmReplayFixture(t *testing.T, label string) (*CandidateConfirmRepository, pgx.Tx, context.Context, graphdomain.SemanticLinkCandidate, candidateconfirm.Command) {
	t.Helper()
	repository, tx, ctx := graphIntegrationRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	sourceID := seedGraphClaim(t, ctx, tx, workspaceID, label+" source", knowledge.ClaimStatusSuggested, floatPointer(0.9), now)
	targetID := seedGraphClaim(t, ctx, tx, workspaceID, label+" target", knowledge.ClaimStatusSuggested, floatPointer(0.8), now)
	candidate := candidateFixture(t, workspaceID, sourceID, targetID, provenance, now, label, 1, 1)
	if _, err := repository.UpsertSemanticLinkCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	confirmer, err := NewCandidateConfirmRepository(tx, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	command := candidateconfirm.Command{
		WorkspaceID: workspaceID, CandidateID: candidate.ID, ExpectedVersion: candidate.Version,
		IdempotencyKey: "confirm-" + label, Action: graphdomain.SemanticLinkCandidateDecisionConfirm,
		RiskLevel:    candidateconfirm.ProposalRiskLevel,
		Risk:         "Creates one canonical Relation only after explicit Proposal approval",
		RollbackPlan: "Create a compensating Relation Proposal if the approved Relation must be retired",
	}
	return confirmer, tx, ctx, candidate, command
}

func seedCandidateConfirmRecord(t *testing.T, ctx context.Context, tx pgx.Tx, candidate graphdomain.SemanticLinkCandidate, command candidateconfirm.Command, legacy bool, persistedRiskLevel changecontroldomain.ProposalRiskLevel, persistedRiskDescription string) (foundation.ID, foundation.ID, foundation.ID) {
	t.Helper()
	change, err := buildKnowledgeChange(candidate, candidate.SuggestedRelationType)
	if err != nil {
		t.Fatal(err)
	}
	commandHash, err := candidateconfirm.RequestHash(command)
	if legacy {
		commandHash, err = candidateconfirm.LegacyRequestHash(command)
	}
	if err != nil {
		t.Fatal(err)
	}
	proposalHash, err := changecontroldomain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
		command.WorkspaceID, change, command.RiskLevel, command.Risk, command.RollbackPlan,
	)
	proposalKeyPrefix := "semantic-link-confirm/v2:"
	if legacy {
		proposalKeyPrefix = "semantic-link-confirm/v1:"
		proposalHash, err = changecontroldomain.ComputeKnowledgeChangeRequestHash(command.WorkspaceID, change, command.Risk, command.RollbackPlan)
	}
	if err != nil {
		t.Fatal(err)
	}
	changeHash, err := changecontroldomain.ComputeKnowledgeChangeHash(change, persistedRiskDescription, command.RollbackPlan)
	if err != nil {
		t.Fatal(err)
	}
	proposalID, revisionID, decisionID := graphTestID(t), graphTestID(t), graphTestID(t)
	createdAt := candidate.UpdatedAt.Add(time.Second).UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal(
			id,workspace_id,proposal_type,idempotency_key,request_hash,risk_level,status,version,created_at,updated_at
		) VALUES($1,$2,'knowledge_change',$3,$4,$5,'ready_for_review',1,$6,$6)`,
		string(proposalID), string(command.WorkspaceID), proposalKeyPrefix+commandHash, proposalHash, string(persistedRiskLevel),
		createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			target_refs,base_versions,change_set,evidence_refs,schema_version,created_at
		) VALUES($1,$2,1,NULL,NULL,NULL,NULL,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		string(revisionID), string(proposalID), persistedRiskDescription, command.RollbackPlan, changeHash,
		mustMarshalCandidateConfirm(t, change.TargetRefs), mustMarshalCandidateConfirm(t, change.BaseVersions),
		mustMarshalCandidateConfirm(t, change.ChangeSet), mustMarshalCandidateConfirm(t, change.EvidenceRefs),
		change.SchemaVersion, createdAt); err != nil {
		t.Fatal(err)
	}
	inserted, err := insertDecision(ctx, tx, candidateDecisionRow{
		id: decisionID, workspaceID: command.WorkspaceID, candidateID: command.CandidateID,
		candidateVersion: command.ExpectedVersion, proposalID: &proposalID,
		idempotencyKey: command.IdempotencyKey, requestHash: commandHash,
		action: command.Action, relationType: command.RelationType, createdAt: createdAt,
	})
	if err != nil || !inserted {
		t.Fatalf("seed decision inserted=%v err=%v", inserted, err)
	}
	updated, err := tx.Exec(ctx, `
		UPDATE graph.semantic_link_candidate
		SET status='PROPOSAL_CREATED',current_proposal_id=$3,deferred_until=NULL,version=version+1,updated_at=$4
		WHERE workspace_id=$1 AND id=$2 AND version=$5 AND current_proposal_id IS NULL`,
		string(command.WorkspaceID), string(command.CandidateID), string(proposalID), createdAt, command.ExpectedVersion)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RowsAffected() != 1 {
		t.Fatalf("seed candidate rows = %d", updated.RowsAffected())
	}
	return proposalID, revisionID, decisionID
}

func mustMarshalCandidateConfirm(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertCandidateConfirmRiskFacts(t *testing.T, ctx context.Context, tx pgx.Tx, proposalID foundation.ID, expectedLevel changecontroldomain.ProposalRiskLevel, expectedDescription string) {
	t.Helper()
	var riskLevel, description string
	if err := tx.QueryRow(ctx, `
		SELECT p.risk_level,r.risk
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.revision_no=1
		WHERE p.id=$1`, string(proposalID)).Scan(&riskLevel, &description); err != nil {
		t.Fatal(err)
	}
	if changecontroldomain.ProposalRiskLevel(riskLevel) != expectedLevel || description != expectedDescription {
		t.Fatalf("risk facts level=%q description=%q, want level=%q description=%q", riskLevel, description, expectedLevel, expectedDescription)
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
