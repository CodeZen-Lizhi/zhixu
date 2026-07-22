package application

import (
	"bytes"
	"context"
	"testing"
	"time"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/candidateconfirm"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

func TestSemanticLinkCandidateServicePaginatesStableWindow(t *testing.T) {
	first := validApplicationSemanticLinkCandidate(t)
	second := first
	second.ID = candidateApplicationID(2)
	second.Source.Version++
	second.UpdatedAt = second.UpdatedAt.Add(-time.Second)
	second.Fingerprint = applicationCandidateFingerprint(t, second)
	repository := &candidateServiceRepositoryFake{window: SemanticLinkCandidateResultWindow{Items: []graphdomain.SemanticLinkCandidate{first, second}}}
	service := newCandidateServiceFixture(t, repository, &candidateConfirmFake{})
	query := graphdomain.SemanticLinkCandidateQuery{WorkspaceID: first.WorkspaceID, Limit: 1}

	firstPage, err := service.List(context.Background(), CandidateListRequest{Query: query})
	if err != nil || len(firstPage.Items) != 1 || firstPage.Items[0].ID != first.ID || firstPage.Meta.NextCursor == "" || firstPage.Meta.Complete {
		t.Fatalf("first page=%+v err=%v", firstPage, err)
	}
	secondPage, err := service.List(context.Background(), CandidateListRequest{Query: query, Cursor: firstPage.Meta.NextCursor})
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].ID != second.ID || secondPage.Meta.NextCursor != "" || !secondPage.Meta.Complete {
		t.Fatalf("second page=%+v err=%v", secondPage, err)
	}

	repository.window.Items[1].Reason = "changed result"
	if _, err := service.List(context.Background(), CandidateListRequest{Query: query, Cursor: firstPage.Meta.NextCursor}); !hasCandidateServiceCode(err, graphdomain.ErrorCodeCursorStale) {
		t.Fatalf("stale cursor err=%v", err)
	}
}

func TestSemanticLinkCandidateServiceRoutesDecisions(t *testing.T) {
	candidate := validApplicationSemanticLinkCandidate(t)
	decisionID := candidateApplicationID(600)
	now := candidate.UpdatedAt.Add(time.Second)
	ignored := candidate
	ignored.Status = graphdomain.SemanticLinkCandidateStatusIgnored
	ignored.Version = 2
	ignored.UpdatedAt = now
	repository := &candidateServiceRepositoryFake{candidate: candidate, decision: SemanticLinkCandidateDecisionResult{
		Candidate: ignored, DecisionID: decisionID, DecidedAt: now,
	}}
	proposalID := candidateApplicationID(601)
	confirmed := candidate
	confirmed.Status = graphdomain.SemanticLinkCandidateStatusProposalCreated
	confirmed.Version = 2
	confirmed.UpdatedAt = now
	confirmed.ProposalID = &proposalID
	confirmer := &candidateConfirmFake{result: candidateconfirm.Result{
		Candidate: confirmed, DecisionID: decisionID,
		Proposal: changecontroldomain.Proposal{ID: proposalID, WorkspaceID: candidate.WorkspaceID, Type: changecontroldomain.ProposalTypeKnowledgeChange},
	}}
	service := newCandidateServiceFixture(t, repository, confirmer)

	ignoreCommand := SemanticLinkCandidateDecisionCommand{
		WorkspaceID: candidate.WorkspaceID, CandidateID: candidate.ID, ExpectedVersion: 1, IdempotencyKey: "ignore-service",
		Decision: graphdomain.SemanticLinkCandidateDecision{Action: graphdomain.SemanticLinkCandidateDecisionIgnore, Reason: "not enough evidence"},
	}
	receipt, err := service.Decide(context.Background(), ignoreCommand)
	if err != nil || receipt.ID != decisionID || receipt.Status != graphdomain.SemanticLinkCandidateStatusIgnored || repository.decideCalls != 1 || confirmer.calls != 0 {
		t.Fatalf("ignore receipt=%+v err=%v repo_calls=%d confirm_calls=%d", receipt, err, repository.decideCalls, confirmer.calls)
	}

	confirmCommand := SemanticLinkCandidateDecisionCommand{
		WorkspaceID: candidate.WorkspaceID, CandidateID: candidate.ID, ExpectedVersion: 1, IdempotencyKey: "confirm-service",
		Decision: graphdomain.SemanticLinkCandidateDecision{Action: graphdomain.SemanticLinkCandidateDecisionConfirm},
	}
	receipt, err = service.Decide(context.Background(), confirmCommand)
	if err != nil || receipt.ProposalID == nil || *receipt.ProposalID != proposalID || confirmer.calls != 1 || repository.decideCalls != 1 {
		t.Fatalf("confirm receipt=%+v err=%v repo_calls=%d confirm_calls=%d", receipt, err, repository.decideCalls, confirmer.calls)
	}
	if confirmer.command.RiskLevel != candidateconfirm.ProposalRiskLevel || confirmer.command.Risk != semanticLinkConfirmRisk || confirmer.command.RollbackPlan != semanticLinkConfirmRollbackPlan {
		t.Fatalf("confirm command=%+v", confirmer.command)
	}
}

func newCandidateServiceFixture(t *testing.T, repository SemanticLinkCandidateRepository, confirmer candidateconfirm.Port) *SemanticLinkCandidateService {
	t.Helper()
	cursor, err := NewCursorCodec(bytes.Repeat([]byte{0x7a}, cursorSigningKeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSemanticLinkCandidateService(repository, confirmer, cursor)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type candidateServiceRepositoryFake struct {
	window      SemanticLinkCandidateResultWindow
	candidate   graphdomain.SemanticLinkCandidate
	decision    SemanticLinkCandidateDecisionResult
	decideCalls int
}

func (repository *candidateServiceRepositoryFake) SemanticLinkCandidateWindow(context.Context, graphdomain.SemanticLinkCandidateQuery) (SemanticLinkCandidateResultWindow, error) {
	return repository.window, nil
}

func (repository *candidateServiceRepositoryFake) GetSemanticLinkCandidate(context.Context, foundation.ID, foundation.ID) (graphdomain.SemanticLinkCandidate, error) {
	return repository.candidate, nil
}

func (repository *candidateServiceRepositoryFake) DecideSemanticLinkCandidate(_ context.Context, _ SemanticLinkCandidateDecisionCommand, _ *foundation.ID) (SemanticLinkCandidateDecisionResult, error) {
	repository.decideCalls++
	return repository.decision, nil
}

type candidateConfirmFake struct {
	result  candidateconfirm.Result
	command candidateconfirm.Command
	calls   int
}

func (confirm *candidateConfirmFake) Confirm(_ context.Context, command candidateconfirm.Command) (candidateconfirm.Result, error) {
	confirm.calls++
	confirm.command = command
	return confirm.result, nil
}

func hasCandidateServiceCode(err error, code string) bool {
	classified, ok := err.(*foundation.Error)
	return ok && classified.Code == code
}
