package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	testHash    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testGitHead = "abcdef0123456789abcdef0123456789abcdef01"
)

type fakeRepo struct {
	proposal          domain.Proposal
	approval          domain.Approval
	err               error
	markedNeedsReview bool
	authorization     domain.ToolAuthorization
	authReplayed      bool
	consumed          domain.AuthorizationConsume
	workflowErr       error
}

type fakeApprovalDispatcher struct {
	command ApprovalDispatchCommand
	result  ApprovalDispatchResult
	err     error
	calls   int
}

type fakeKnowledgeRelationApplier struct {
	command knowledgeapplication.ApprovedRelationApplyCommand
	result  knowledgeapplication.ApprovedRelationApplyResult
	err     error
	calls   int
}

func (f *fakeKnowledgeRelationApplier) ApplyApprovedRelation(_ context.Context, command knowledgeapplication.ApprovedRelationApplyCommand) (knowledgeapplication.ApprovedRelationApplyResult, error) {
	f.calls++
	f.command = command
	if f.result.Relation.WorkspaceID == "" {
		f.result = knowledgeapplication.ApprovedRelationApplyResult{
			Relation: knowledge.Relation{
				WorkspaceID: command.WorkspaceID,
				Status:      knowledge.RelationStatusConfirmed,
				Confirmation: &knowledge.Confirmation{
					Method: knowledge.ConfirmationUserApproval, Reference: string(command.ApprovalID),
				},
			},
			Evidence:        []knowledge.RelationEvidence{{}},
			ProposalStatus:  domain.StatusApplied,
			ProposalVersion: 4,
		}
	}
	return f.result, f.err
}

func (f *fakeApprovalDispatcher) DecideAndDispatch(_ context.Context, command ApprovalDispatchCommand) (ApprovalDispatchResult, error) {
	f.calls++
	f.command = command
	if f.result.Approval.ID == "" {
		f.result.Approval = command.Approval
	}
	return f.result, f.err
}

func (f *fakeRepo) CreateProposal(_ context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	f.proposal = proposal
	return proposal, f.err
}
func (f *fakeRepo) CreateKnowledgeChangeProposal(_ context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	f.proposal = proposal
	return proposal, f.err
}
func (f *fakeRepo) Approve(_ context.Context, approval domain.Approval) (domain.Approval, error) {
	f.approval = approval
	return approval, f.err
}
func (f *fakeRepo) GetProposal(_ context.Context, _ foundation.ID) (domain.Proposal, error) {
	return f.proposal, f.err
}
func (f *fakeRepo) MarkNeedsRevision(_ context.Context, _ foundation.ID, _ time.Time) error {
	f.markedNeedsReview = true
	return f.err
}
func (f *fakeRepo) ValidateWorkflowContext(context.Context, foundation.ID, foundation.ID, foundation.ID) error {
	return f.workflowErr
}
func (f *fakeRepo) GetAuthorization(context.Context, foundation.ID, string, string) (domain.ToolAuthorization, error) {
	return f.authorization, f.err
}
func (f *fakeRepo) CreateAuthorization(_ context.Context, authorization domain.ToolAuthorization) (domain.AuthorizationIssueResult, error) {
	if f.authorization.ID != "" {
		return domain.AuthorizationIssueResult{Authorization: f.authorization, Replayed: f.authReplayed}, f.err
	}
	authorization.ID = "authorization"
	f.authorization = authorization
	return domain.AuthorizationIssueResult{Authorization: authorization}, f.err
}
func (f *fakeRepo) ConsumeAuthorization(_ context.Context, request domain.AuthorizationConsume) (domain.AuthorizationConsumeResult, error) {
	f.consumed = request
	authorization := f.authorization
	if authorization.Status == domain.AuthorizationExpired {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_EXPIRED", false, errors.New("authorization has expired"))
	}
	authorization.Status = domain.AuthorizationConsumed
	return domain.AuthorizationConsumeResult{Authorization: authorization}, f.err
}
func (f *fakeRepo) RevokeAuthorization(_ context.Context, _ foundation.ID, _ time.Time) error {
	return f.err
}

type fakeTargets struct {
	hash  string
	err   error
	calls int
}

func (f *fakeTargets) CurrentHash(context.Context, foundation.ID, string) (string, error) {
	f.calls++
	return f.hash, f.err
}

type fakeApprovalGitInspector struct {
	snapshot domain.GitSnapshot
	err      error
	calls    int
}

func (f *fakeApprovalGitInspector) CaptureApprovalSnapshot(context.Context, foundation.ID) (domain.GitSnapshot, error) {
	f.calls++
	return f.snapshot, f.err
}

type seqIDs struct{ n int }

func (s *seqIDs) New() (foundation.ID, error) {
	s.n++
	return foundation.ID("00000000-0000-4000-8000-00000000000" + string(rune('0'+s.n))), nil
}

func newTestService(repository *fakeRepo, targets *fakeTargets) *Service {
	return newTestServiceWithGit(repository, targets, &fakeApprovalGitInspector{snapshot: domain.GitSnapshot{
		WorkspaceID: "workspace", Branch: "main", Head: testGitHead, ObjectFormat: domain.GitObjectFormatSHA1, Clean: true,
	}})
}

func newTestServiceWithGit(repository *fakeRepo, targets *fakeTargets, git ApprovalGitInspector) *Service {
	service, err := NewService(repository, &seqIDs{}, foundation.FixedClock{Value: time.Unix(1, 0)}, targets, git)
	if err != nil {
		panic(err)
	}
	return service
}

func newTestServiceWithKnowledgeApply(repository *fakeRepo, targets *fakeTargets, git ApprovalGitInspector, applier knowledgeapplication.ApprovedRelationApplyPort) *Service {
	service, err := NewService(repository, &seqIDs{}, foundation.FixedClock{Value: time.Unix(1, 0)}, targets, git, applier)
	if err != nil {
		panic(err)
	}
	return service
}

func newTestDispatchService(repository *fakeRepo, targets *fakeTargets, git ApprovalGitInspector, dispatcher ApprovalDispatcher) *Service {
	service, err := NewServiceWithDispatch(repository, &seqIDs{}, foundation.FixedClock{Value: time.Unix(1, 0)}, targets, git, dispatcher)
	if err != nil {
		panic(err)
	}
	return service
}

func newTestDispatchServiceWithKnowledgeApply(repository *fakeRepo, targets *fakeTargets, git ApprovalGitInspector, dispatcher ApprovalDispatcher, applier knowledgeapplication.ApprovedRelationApplyPort) *Service {
	service, err := NewServiceWithDispatch(repository, &seqIDs{}, foundation.FixedClock{Value: time.Unix(1, 0)}, targets, git, dispatcher, applier)
	if err != nil {
		panic(err)
	}
	return service
}

func TestCreateProposalBindsTargetBaseAndContent(t *testing.T) {
	repository := &fakeRepo{}
	service := newTestService(repository, &fakeTargets{})
	result, err := service.CreateProposal(context.Background(), CreateCommand{
		WorkspaceID: "workspace", TargetPath: "notes/../notes/a.md", BaseHash: testHash,
		IdempotencyKey: "create-1",
		Content:        "  code\r\n", EvidenceSummary: "evidence", Risk: "low", RollbackPlan: "revert commit",
	})
	if err != nil {
		t.Fatalf("CreateProposal() error = %v", err)
	}
	proposal := result.Proposal
	if proposal.Type != domain.ProposalTypeFilePatch || proposal.TargetPath != "notes/a.md" || proposal.Revision.TargetPath != "notes/a.md" || proposal.Status != domain.StatusReady || proposal.IdempotencyKey != "create-1" || proposal.RequestHash == "" {
		t.Fatalf("proposal = %#v", proposal)
	}
	wantHash := domain.ComputeChangeHash("notes/a.md", testHash, "  code\r\n")
	if proposal.Revision.ChangeHash != wantHash {
		t.Fatalf("change hash = %s, want %s", proposal.Revision.ChangeHash, wantHash)
	}
	if wantHash == domain.ComputeChangeHash("notes/a.md", testHash, "code\r\n") || wantHash == domain.ComputeChangeHash("other.md", testHash, "  code\r\n") {
		t.Fatal("change hash did not bind semantic content and target")
	}
}

func TestCreateKnowledgeChangeProposalUsesTypedCanonicalHash(t *testing.T) {
	repository := &fakeRepo{}
	service := newTestService(repository, &fakeTargets{})
	change := knowledgeChangeFixture()
	result, err := service.CreateKnowledgeChangeProposal(context.Background(), CreateKnowledgeChangeCommand{
		WorkspaceID: "workspace", IdempotencyKey: "knowledge-create", KnowledgeChange: change, Risk: " medium ", RollbackPlan: " restore relation ",
	})
	if err != nil {
		t.Fatalf("CreateKnowledgeChangeProposal() error = %v", err)
	}
	proposal := result.Proposal
	if proposal.Type != domain.ProposalTypeKnowledgeChange || proposal.Revision.KnowledgeChange == nil || proposal.TargetPath != "" || proposal.Revision.TargetPath != "" || proposal.Revision.BaseHash != "" || proposal.Revision.Content != "" {
		t.Fatalf("proposal = %#v", proposal)
	}
	if proposal.Revision.ChangeHash == "" || proposal.RequestHash == "" {
		t.Fatalf("proposal hash missing: %#v", proposal)
	}
	expectedHash, err := domain.ComputeKnowledgeChangeHash(*proposal.Revision.KnowledgeChange, "medium", "restore relation")
	if err != nil || proposal.Revision.ChangeHash != expectedHash {
		t.Fatalf("knowledge change hash = %s want %s err=%v", proposal.Revision.ChangeHash, expectedHash, err)
	}
}

func TestKnowledgeChangeProposalRejectsFilePatchFields(t *testing.T) {
	change := knowledgeChangeFixture()
	revision := domain.Revision{TargetPath: "notes/a.md", BaseHash: testHash, Content: "new content", EvidenceSummary: "evidence", Risk: "medium", RollbackPlan: "restore relation", ChangeHash: testHash, KnowledgeChange: &change}
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeKnowledgeChange, revision); err == nil {
		t.Fatal("knowledge_change revision accepted file patch fields")
	}
}

func TestCreateProposalRejectsUnsafeOrIncompleteInput(t *testing.T) {
	tests := []CreateCommand{
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "../a.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "/tmp/a.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "a.md", BaseHash: "zz" + testHash[2:], Content: "x", EvidenceSummary: "e", Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "a.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", Risk: " ", RollbackPlan: "r"},
		{WorkspaceID: "workspace", TargetPath: "a.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", Risk: "low", RollbackPlan: "r"},
	}
	for _, command := range tests {
		service := newTestService(&fakeRepo{}, &fakeTargets{})
		if _, err := service.CreateProposal(context.Background(), command); err == nil {
			t.Fatalf("CreateProposal(%#v) expected error", command)
		}
	}
}

func TestDecideProposalPassesBoundHash(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Status = domain.StatusReady
	proposal.Approval = nil
	repository := &fakeRepo{proposal: proposal}
	service := newTestService(repository, &fakeTargets{hash: testHash})
	_, err := service.DecideProposal(context.Background(), "proposal", "revision", proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || repository.approval.ChangeHash != proposal.Revision.ChangeHash || repository.approval.Decision != domain.DecisionApproved || repository.approval.ApprovedGitHead == nil || *repository.approval.ApprovedGitHead != testGitHead {
		t.Fatalf("approval = %#v, err = %v", repository.approval, err)
	}
}

func TestDecideKnowledgeChangeProposalBypassesFileMutableFacts(t *testing.T) {
	proposal := knowledgeChangeProposal()
	repository := &fakeRepo{proposal: proposal}
	targets := &fakeTargets{err: errors.New("must not be called")}
	git := &fakeApprovalGitInspector{err: errors.New("must not be called")}
	applier := &fakeKnowledgeRelationApplier{}
	service := newTestServiceWithKnowledgeApply(repository, targets, git, applier)
	approval, err := service.DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || approval.Decision != domain.DecisionApproved || approval.ApprovedGitHead != nil || targets.calls != 0 || git.calls != 0 || applier.calls != 1 ||
		applier.command.WorkspaceID != proposal.WorkspaceID || applier.command.ProposalID != proposal.ID || applier.command.RevisionID != proposal.Revision.ID || applier.command.ApprovalID != proposal.Approval.ID {
		t.Fatalf("approval=%#v apply=%#v target calls=%d git calls=%d err=%v", approval, applier.command, targets.calls, git.calls, err)
	}
}

func TestDecideKnowledgeChangeProposalFailsClosedWithoutApplySeam(t *testing.T) {
	proposal := knowledgeChangeProposal()
	_, err := newTestServiceWithGit(&fakeRepo{proposal: proposal}, &fakeTargets{}, &fakeApprovalGitInspector{}).
		DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "KNOWLEDGE_RELATION_APPLIER_UNAVAILABLE" {
		t.Fatalf("err=%v", err)
	}
}

func TestDecideProposalRejectsGitSnapshotFailureWithoutApproval(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Status = domain.StatusReady
	proposal.Approval = nil
	repository := &fakeRepo{proposal: proposal}
	git := &fakeApprovalGitInspector{err: foundation.NewError(foundation.ErrorVersionConflict, "GIT_REPOSITORY_DIRTY", false, errors.New("dirty"))}
	service := newTestServiceWithGit(repository, &fakeTargets{hash: testHash}, git)
	_, err := service.DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err == nil || repository.approval.ID != "" || git.calls != 1 {
		t.Fatalf("approval=%#v git calls=%d err=%v", repository.approval, git.calls, err)
	}
}

func TestDecideProposalRejectsInvalidGitSnapshotBinding(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Status = domain.StatusReady
	proposal.Approval = nil
	repository := &fakeRepo{proposal: proposal}
	git := &fakeApprovalGitInspector{snapshot: domain.GitSnapshot{
		WorkspaceID: "other-workspace", Branch: "main", Head: testGitHead, ObjectFormat: domain.GitObjectFormatSHA1, Clean: true,
	}}
	service := newTestServiceWithGit(repository, &fakeTargets{hash: testHash}, git)
	_, err := service.DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	var applicationError *foundation.Error
	if !errors.As(err, &applicationError) || applicationError.Code != "APPROVAL_GIT_SNAPSHOT_INVALID" || repository.approval.ID != "" {
		t.Fatalf("approval=%#v err=%v", repository.approval, err)
	}
}

func TestDecideProposalRejectedDoesNotInspectTargetOrGit(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Status = domain.StatusReady
	proposal.Approval = nil
	repository := &fakeRepo{proposal: proposal}
	targets := &fakeTargets{err: errors.New("must not be called")}
	git := &fakeApprovalGitInspector{err: errors.New("must not be called")}
	service := newTestServiceWithGit(repository, targets, git)
	approval, err := service.DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionRejected)
	if err != nil || approval.Decision != domain.DecisionRejected || approval.ApprovedGitHead != nil || targets.calls != 0 || git.calls != 0 {
		t.Fatalf("approval=%#v target calls=%d git calls=%d err=%v", approval, targets.calls, git.calls, err)
	}
}

func TestDecideProposalReplayDoesNotRecaptureMutableFacts(t *testing.T) {
	proposal := approvedProposal(testHash)
	targets := &fakeTargets{err: errors.New("must not be called")}
	git := &fakeApprovalGitInspector{err: errors.New("must not be called")}
	service := newTestServiceWithGit(&fakeRepo{proposal: proposal}, targets, git)
	approval, err := service.DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || approval.ID != proposal.Approval.ID || targets.calls != 0 || git.calls != 0 {
		t.Fatalf("approval=%#v target calls=%d git calls=%d err=%v", approval, targets.calls, git.calls, err)
	}
}

func TestDecideProposalWithDispatchExactReplaySkipsMutableFacts(t *testing.T) {
	proposal := approvedProposal(testHash)
	runID := foundation.ID("10000000-0000-4000-8000-000000000001")
	proposal.WorkflowRunID = &runID
	dispatcher := &fakeApprovalDispatcher{result: ApprovalDispatchResult{
		Approval: *proposal.Approval, WorkflowRunID: runID,
		NodeRunID: "10000000-0000-4000-8000-000000000002", JobID: 42,
		DispatchStatus: DispatchStatusReplayed, Replayed: true,
	}}
	targets := &fakeTargets{err: errors.New("must not be called")}
	git := &fakeApprovalGitInspector{err: errors.New("must not be called")}
	service := newTestDispatchService(&fakeRepo{proposal: proposal}, targets, git, dispatcher)

	result, err := service.DecideProposalWithDispatch(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || !result.Replayed || result.Workflow == nil || result.Workflow.RunID != runID || targets.calls != 0 || git.calls != 0 || dispatcher.calls != 1 {
		t.Fatalf("result=%#v target calls=%d git calls=%d dispatch calls=%d err=%v", result, targets.calls, git.calls, dispatcher.calls, err)
	}
	if dispatcher.command.ObservedBaseHash != "" || dispatcher.command.ObservedGitHead != "" {
		t.Fatalf("exact replay unexpectedly carried mutable facts: %#v", dispatcher.command)
	}
}

func TestDecideProposalWithDispatchBackfillsHistoricalApprovalOnlyAfterSafetyGate(t *testing.T) {
	proposal := approvedProposal(testHash)
	dispatcher := &fakeApprovalDispatcher{result: ApprovalDispatchResult{
		Approval:      *proposal.Approval,
		WorkflowRunID: "10000000-0000-4000-8000-000000000003",
		NodeRunID:     "10000000-0000-4000-8000-000000000004", JobID: 43,
		DispatchStatus: DispatchStatusQueued,
	}}
	targets := &fakeTargets{hash: testHash}
	git := &fakeApprovalGitInspector{snapshot: domain.GitSnapshot{WorkspaceID: proposal.WorkspaceID, Branch: "main", Head: testGitHead, ObjectFormat: domain.GitObjectFormatSHA1, Clean: true}}
	service := newTestDispatchService(&fakeRepo{proposal: proposal}, targets, git, dispatcher)

	result, err := service.DecideProposalWithDispatch(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || result.Replayed || result.Workflow == nil || targets.calls != 1 || git.calls != 1 || dispatcher.calls != 1 {
		t.Fatalf("result=%#v target calls=%d git calls=%d dispatch calls=%d err=%v", result, targets.calls, git.calls, dispatcher.calls, err)
	}
	if dispatcher.command.ObservedBaseHash != testHash || dispatcher.command.ObservedGitHead != testGitHead {
		t.Fatalf("dispatch safety facts=%#v", dispatcher.command)
	}
}

func TestDecideProposalWithDispatchRejectsHistoricalApprovalWithoutGitBaseline(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Approval.ApprovedGitHead = nil
	dispatcher := &fakeApprovalDispatcher{}
	service := newTestDispatchService(&fakeRepo{proposal: proposal}, &fakeTargets{hash: testHash}, &fakeApprovalGitInspector{}, dispatcher)

	_, err := service.DecideProposalWithDispatch(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "APPROVAL_GIT_BASELINE_MISSING" || dispatcher.calls != 0 {
		t.Fatalf("dispatch calls=%d err=%v", dispatcher.calls, err)
	}
}

func TestDecideProposalWithDispatchRejectedHasNoWorkflow(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Status = domain.StatusReady
	proposal.Approval = nil
	dispatcher := &fakeApprovalDispatcher{result: ApprovalDispatchResult{DispatchStatus: ""}}
	targets := &fakeTargets{err: errors.New("must not be called")}
	git := &fakeApprovalGitInspector{err: errors.New("must not be called")}
	service := newTestDispatchService(&fakeRepo{proposal: proposal}, targets, git, dispatcher)

	result, err := service.DecideProposalWithDispatch(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionRejected)
	if err != nil || result.Approval.Decision != domain.DecisionRejected || result.Workflow != nil || targets.calls != 0 || git.calls != 0 || dispatcher.calls != 1 {
		t.Fatalf("result=%#v target calls=%d git calls=%d dispatch calls=%d err=%v", result, targets.calls, git.calls, dispatcher.calls, err)
	}
}

func TestDecideProposalWithDispatchRoutesKnowledgeChangeWithoutDispatcher(t *testing.T) {
	proposal := knowledgeChangeProposal()
	repository := &fakeRepo{proposal: proposal}
	targets := &fakeTargets{err: errors.New("must not be called")}
	git := &fakeApprovalGitInspector{err: errors.New("must not be called")}
	dispatcher := &fakeApprovalDispatcher{err: errors.New("must not be called")}
	applier := &fakeKnowledgeRelationApplier{}
	service := newTestDispatchServiceWithKnowledgeApply(repository, targets, git, dispatcher, applier)

	result, err := service.DecideProposalWithDispatch(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || result.Workflow != nil || result.Approval.Decision != domain.DecisionApproved || targets.calls != 0 || git.calls != 0 || dispatcher.calls != 0 || applier.calls != 1 {
		t.Fatalf("result=%#v target calls=%d git calls=%d dispatch calls=%d apply calls=%d err=%v", result, targets.calls, git.calls, dispatcher.calls, applier.calls, err)
	}
}

func TestDecideProposalMarksNeedsRevisionWhenTargetUnavailable(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Status = domain.StatusReady
	proposal.Approval = nil
	repository := &fakeRepo{proposal: proposal}
	service := newTestService(repository, &fakeTargets{err: &domain.TargetUnavailableError{Cause: errors.New("missing")}})
	_, err := service.DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err == nil || !repository.markedNeedsReview {
		t.Fatalf("err=%v marked=%v", err, repository.markedNeedsReview)
	}
}

func TestApplyPreflightReadsServerTargetAndPassesWithoutWrite(t *testing.T) {
	proposal := approvedProposal(testHash)
	repository := &fakeRepo{proposal: proposal}
	service := newTestService(repository, &fakeTargets{hash: testHash})
	result, err := service.CheckApplyPreflight(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash)
	if err != nil || result.BaseHash != testHash || repository.markedNeedsReview {
		t.Fatalf("result = %#v, marked = %v, err = %v", result, repository.markedNeedsReview, err)
	}
}

func TestApplyPreflightMarksNeedsRevisionOnRealBaselineConflict(t *testing.T) {
	proposal := approvedProposal(testHash)
	repository := &fakeRepo{proposal: proposal}
	service := newTestService(repository, &fakeTargets{hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	_, err := service.CheckApplyPreflight(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash)
	var conflict *HashConflict
	if !errors.As(err, &conflict) || !repository.markedNeedsReview {
		t.Fatalf("err = %v, marked = %v", err, repository.markedNeedsReview)
	}
}

func TestApplyPreflightMarksNeedsRevisionWhenTargetUnavailable(t *testing.T) {
	proposal := approvedProposal(testHash)
	repository := &fakeRepo{proposal: proposal}
	service := newTestService(repository, &fakeTargets{err: &domain.TargetUnavailableError{Cause: errors.New("missing")}})
	_, err := service.CheckApplyPreflight(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash)
	if err == nil || !repository.markedNeedsReview {
		t.Fatalf("err = %v, marked = %v", err, repository.markedNeedsReview)
	}
}

func TestApplyPreflightRejectsUnapprovedProposalBeforeReadingTarget(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Status = domain.StatusRejected
	proposal.Approval.Decision = domain.DecisionRejected
	service := newTestService(&fakeRepo{proposal: proposal}, &fakeTargets{err: errors.New("must not be called")})
	if _, err := service.CheckApplyPreflight(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash); err == nil {
		t.Fatal("CheckApplyPreflight() expected error")
	}
}

func TestApplyPreflightRejectsKnowledgeChangeProposalBeforeReadingTarget(t *testing.T) {
	proposal := knowledgeChangeProposal()
	service := newTestService(&fakeRepo{proposal: proposal}, &fakeTargets{err: errors.New("must not be called")})
	if _, err := service.CheckApplyPreflight(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash); err == nil {
		t.Fatal("CheckApplyPreflight() expected error")
	}
}

func TestIssueWriteAuthorizationBindsApprovalAndTarget(t *testing.T) {
	proposal := approvedProposal(testHash)
	repository := &fakeRepo{proposal: proposal}
	service := newTestService(repository, &fakeTargets{hash: testHash})
	result, err := service.IssueWriteAuthorization(context.Background(), domain.AuthorizationIssue{
		WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node", ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", IdempotencyKey: "auth-1", TTL: time.Minute,
	})
	if err != nil || result.Credential == "" || result.Authorization.ID == "" || result.Authorization.TokenHash != "" || result.Authorization.TargetVersion != testHash || result.Authorization.Status != domain.AuthorizationIssued {
		t.Fatalf("authorization = %#v, err=%v", result, err)
	}
	consume, err := service.ConsumeWriteAuthorization(context.Background(), domain.AuthorizationConsume{
		Credential: result.Credential, IdempotencyKey: "auth-1", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node",
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash,
	})
	if err != nil || consume.Authorization.Status != domain.AuthorizationConsumed || repository.consumed.Credential == result.Credential {
		t.Fatalf("consumption = %#v, err=%v, raw=%q", consume, err, repository.consumed.Credential)
	}
}

func TestIssueWriteAuthorizationRejectsStaleOrUnapprovedProposal(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Status = domain.StatusNeedsRevision
	service := newTestService(&fakeRepo{proposal: proposal}, &fakeTargets{hash: testHash})
	if _, err := service.IssueWriteAuthorization(context.Background(), domain.AuthorizationIssue{
		WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node", ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", IdempotencyKey: "auth-1", TTL: time.Minute,
	}); err == nil {
		t.Fatal("stale proposal received write authorization")
	}
}

func TestIssueWriteAuthorizationRejectsKnowledgeChangeProposal(t *testing.T) {
	proposal := knowledgeChangeProposal()
	service := newTestService(&fakeRepo{proposal: proposal}, &fakeTargets{hash: testHash})
	if _, err := service.IssueWriteAuthorization(context.Background(), domain.AuthorizationIssue{
		WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node", ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", IdempotencyKey: "auth-knowledge", TTL: time.Minute,
	}); err == nil {
		t.Fatal("knowledge_change proposal received write authorization")
	}
}

func TestIssueWriteAuthorizationRejectsTargetConflict(t *testing.T) {
	proposal := approvedProposal(testHash)
	repository := &fakeRepo{proposal: proposal}
	service := newTestService(repository, &fakeTargets{hash: strings.Repeat("a", 64)})
	if _, err := service.IssueWriteAuthorization(context.Background(), domain.AuthorizationIssue{
		WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node", ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", IdempotencyKey: "auth-1", TTL: time.Minute,
	}); err == nil || !repository.markedNeedsReview {
		t.Fatalf("target conflict err=%v marked=%v", err, repository.markedNeedsReview)
	}
}

func TestIssueWriteAuthorizationRejectsBroaderScope(t *testing.T) {
	proposal := approvedProposal(testHash)
	service := newTestService(&fakeRepo{proposal: proposal}, &fakeTargets{hash: testHash})
	if _, err := service.IssueWriteAuthorization(context.Background(), domain.AuthorizationIssue{
		WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node", ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:other.md", IdempotencyKey: "auth-scope", TTL: time.Minute,
	}); err == nil {
		t.Fatal("broader target scope received write authorization")
	}
}

func TestConsumeWriteAuthorizationReplaysAfterProposalStateAdvances(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Status = domain.StatusCompleted
	repository := &fakeRepo{proposal: proposal, authorization: domain.ToolAuthorization{
		ID: "authorization", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node", ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge,
		Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash, TokenHash: hashCredential("credential"), IdempotencyKey: "auth-1", Status: domain.AuthorizationConsumed,
	}}
	targets := &fakeTargets{err: errors.New("must not be called for replay")}
	service := newTestService(repository, targets)
	result, err := service.ConsumeWriteAuthorization(context.Background(), domain.AuthorizationConsume{
		Credential: "credential", IdempotencyKey: "auth-1", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node",
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash,
	})
	if err != nil || result.Authorization.Status != domain.AuthorizationConsumed || targets.calls != 0 {
		t.Fatalf("replay=%#v err=%v target calls=%d", result, err, targets.calls)
	}
}

func TestConsumeWriteAuthorizationRejectsTamperedBindingBeforeTargetSideEffects(t *testing.T) {
	proposal := approvedProposal(testHash)
	repository := &fakeRepo{proposal: proposal, authorization: domain.ToolAuthorization{
		ID: "authorization", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node", ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge,
		Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash, TokenHash: hashCredential("credential"), IdempotencyKey: "auth-1", Status: domain.AuthorizationIssued, ExpiresAt: time.Unix(2, 0),
	}}
	targets := &fakeTargets{hash: strings.Repeat("a", 64)}
	service := newTestService(repository, targets)
	_, err := service.ConsumeWriteAuthorization(context.Background(), domain.AuthorizationConsume{
		Credential: "credential", IdempotencyKey: "auth-1", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node",
		ProposalID: "other-proposal", RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash,
	})
	if err == nil || targets.calls != 0 || repository.markedNeedsReview {
		t.Fatalf("tampered consume err=%v target calls=%d marked=%v", err, targets.calls, repository.markedNeedsReview)
	}
}

func TestConsumeWriteAuthorizationRejectsExpiredBeforeTargetSideEffects(t *testing.T) {
	proposal := approvedProposal(testHash)
	repository := &fakeRepo{proposal: proposal, authorization: domain.ToolAuthorization{
		ID: "authorization", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node", ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge,
		Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash, TokenHash: hashCredential("credential"), IdempotencyKey: "auth-expired", Status: domain.AuthorizationExpired, ExpiresAt: time.Unix(0, 0),
	}}
	targets := &fakeTargets{hash: strings.Repeat("a", 64)}
	service := newTestService(repository, targets)
	_, err := service.ConsumeWriteAuthorization(context.Background(), domain.AuthorizationConsume{
		Credential: "credential", IdempotencyKey: "auth-expired", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node",
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash,
	})
	if err == nil || targets.calls != 0 || repository.markedNeedsReview {
		t.Fatalf("expired consume err=%v target calls=%d marked=%v", err, targets.calls, repository.markedNeedsReview)
	}
}

func TestConsumeWriteAuthorizationTargetConflictHasNoProposalSideEffect(t *testing.T) {
	proposal := approvedProposal(testHash)
	repository := &fakeRepo{proposal: proposal, authorization: domain.ToolAuthorization{
		ID: "authorization", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node", ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge,
		Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash, TokenHash: hashCredential("credential"), IdempotencyKey: "auth-target-conflict", Status: domain.AuthorizationIssued,
	}}
	service := newTestService(repository, &fakeTargets{hash: strings.Repeat("a", 64)})
	_, err := service.ConsumeWriteAuthorization(context.Background(), domain.AuthorizationConsume{
		Credential: "credential", IdempotencyKey: "auth-target-conflict", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node",
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash,
	})
	if err == nil || repository.markedNeedsReview {
		t.Fatalf("target conflict err=%v marked=%v", err, repository.markedNeedsReview)
	}
}

func TestConsumeWriteAuthorizationRejectsWrongCredentialBeforeReplay(t *testing.T) {
	proposal := approvedProposal(testHash)
	repository := &fakeRepo{proposal: proposal, authorization: domain.ToolAuthorization{
		ID: "authorization", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node", ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge,
		Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash, TokenHash: hashCredential("correct"), IdempotencyKey: "auth-1", Status: domain.AuthorizationConsumed,
	}}
	service := newTestService(repository, &fakeTargets{err: errors.New("must not be called")})
	_, err := service.ConsumeWriteAuthorization(context.Background(), domain.AuthorizationConsume{
		Credential: "wrong", IdempotencyKey: "auth-1", WorkspaceID: proposal.WorkspaceID, WorkflowRunID: "run", NodeRunID: "node",
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID, ToolName: "ApplyApprovedPatch",
		Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: testHash,
	})
	if err == nil {
		t.Fatal("wrong credential replay accepted")
	}
}

func approvedProposal(baseHash string) domain.Proposal {
	changeHash := domain.ComputeChangeHash("a.md", baseHash, "new content")
	approvedGitHead := testGitHead
	return domain.Proposal{
		ID: "proposal", WorkspaceID: "workspace", Type: domain.ProposalTypeFilePatch, TargetPath: "a.md", Status: domain.StatusApproved,
		Revision: domain.Revision{ID: "revision", ProposalID: "proposal", TargetPath: "a.md", BaseHash: baseHash, Content: "new content", ChangeHash: changeHash},
		Approval: &domain.Approval{ID: "approval", ProposalID: "proposal", RevisionID: "revision", ChangeHash: changeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead},
	}
}

func knowledgeChangeFixture() domain.KnowledgeChange {
	change := domain.KnowledgeChange{
		TargetRefs: []domain.KnowledgeTargetRef{{
			Type: domain.KnowledgeTargetRefRelationCandidate, ID: "70000000-0000-4000-8000-000000000001", Fingerprint: strings.ToUpper(strings.Repeat("a", 64)),
		}},
		BaseVersions: []domain.KnowledgeBaseVersion{
			{NodeType: knowledge.NodeTypeClaim, NodeID: "90000000-0000-4000-8000-000000000003", Version: 8},
			{NodeType: knowledge.NodeTypeTopic, NodeID: "90000000-0000-4000-8000-000000000002", Version: 4},
		},
		ChangeSet: domain.KnowledgeChangeSet{
			Operation:    domain.KnowledgeChangeOperationCreateRelation,
			Source:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: "90000000-0000-4000-8000-000000000003"},
			Target:       knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: "90000000-0000-4000-8000-000000000002"},
			RelationType: knowledge.RelationBelongsTo,
		},
		EvidenceRefs: []domain.KnowledgeEvidenceRef{{
			CandidateEvidenceID: "80000000-0000-4000-8000-000000000004", SemanticHash: strings.ToUpper(strings.Repeat("b", 64)),
		}},
		SchemaVersion: domain.KnowledgeChangeSchemaVersion,
	}
	normalized, err := domain.ValidateKnowledgeChange(change)
	if err != nil {
		panic(err)
	}
	return normalized
}

func knowledgeChangeProposal() domain.Proposal {
	change := knowledgeChangeFixture()
	hash, err := domain.ComputeKnowledgeChangeHash(change, "medium", "restore relation")
	if err != nil {
		panic(err)
	}
	return domain.Proposal{
		ID: "60000000-0000-4000-8000-000000000002", WorkspaceID: "60000000-0000-4000-8000-000000000001", Type: domain.ProposalTypeKnowledgeChange, Status: domain.StatusApproved, Version: 2,
		Revision: domain.Revision{ID: "60000000-0000-4000-8000-000000000003", ProposalID: "60000000-0000-4000-8000-000000000002", Risk: "medium", RollbackPlan: "restore relation", ChangeHash: hash, KnowledgeChange: &change},
		Approval: &domain.Approval{ID: "60000000-0000-4000-8000-000000000004", ProposalID: "60000000-0000-4000-8000-000000000002", RevisionID: "60000000-0000-4000-8000-000000000003", ChangeHash: hash, Decision: domain.DecisionApproved},
	}
}
