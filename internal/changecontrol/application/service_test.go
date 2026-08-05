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
	proposal           domain.Proposal
	approval           domain.Approval
	err                error
	createCalls        int
	markedNeedsReview  bool
	authorization      domain.ToolAuthorization
	authorizationReads int
	authReplayed       bool
	consumed           domain.AuthorizationConsume
	workflowErr        error
	downstreamUpdate   domain.DownstreamUpdate
	downstreamBuilds   int
}

type fakeApprovalDispatcher struct {
	command ApprovalDispatchCommand
	result  ApprovalDispatchResult
	err     error
	calls   int
}

type fakeKnowledgeRelationApplier struct {
	workspaceID    foundation.ID
	change         *domain.KnowledgeChange
	command        knowledgeapplication.ApprovedRelationApplyCommand
	approvalInput  domain.Approval
	approvalResult domain.Approval
	result         knowledgeapplication.ApprovedRelationApplyResult
	err            error
	calls          int
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

func (f *fakeKnowledgeRelationApplier) ApproveAndApplyRelation(_ context.Context, approval domain.Approval) (domain.Approval, knowledgeapplication.ApprovedRelationApplyResult, error) {
	f.calls++
	f.approvalInput = approval
	persisted := approval
	if f.approvalResult.ID != "" {
		persisted = f.approvalResult
	}
	f.command = knowledgeapplication.ApprovedRelationApplyCommand{
		WorkspaceID: f.workspaceID,
		ProposalID:  persisted.ProposalID,
		RevisionID:  persisted.RevisionID,
		ApprovalID:  persisted.ID,
	}
	if f.result.Relation.WorkspaceID == "" {
		f.result = validFakeKnowledgeApplyResult(f.workspaceID, f.change, persisted.ID, f.result.Replayed)
	}
	return persisted, f.result, f.err
}

func validFakeKnowledgeApplyResult(workspaceID foundation.ID, change *domain.KnowledgeChange, approvalID foundation.ID, replayed bool) knowledgeapplication.ApprovedRelationApplyResult {
	relationID := foundation.ID("70000000-0000-4000-8000-000000000001")
	confirmation := knowledge.Confirmation{Method: knowledge.ConfirmationUserApproval, Reference: string(approvalID)}
	at := time.Unix(2, 0).UTC()
	applicability, err := knowledge.ParseApplicability([]byte(`{}`))
	if err != nil {
		panic(err)
	}
	evidence := knowledge.RelationEvidence{
		ID: "70000000-0000-4000-8000-000000000002", WorkspaceID: workspaceID, RelationID: relationID,
		Provenance: knowledge.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: "70000000-0000-4000-8000-000000000003", SourceSpanID: "70000000-0000-4000-8000-000000000004"},
		Reason:     "validated approval evidence", Applicability: applicability, Confirmation: &confirmation, CreatedAt: at,
	}
	evidence.EvidenceHash = knowledge.ComputeRelationEvidenceHash(evidence)
	relation := knowledge.Relation{
		ID: relationID, WorkspaceID: workspaceID, Source: change.ChangeSet.Source, Target: change.ChangeSet.Target,
		Type: change.ChangeSet.RelationType, Status: knowledge.RelationStatusConfirmed, Confirmation: &confirmation,
		Fingerprint:         knowledge.ComputeRelationFingerprint(workspaceID, change.ChangeSet.RelationType, change.ChangeSet.Source, change.ChangeSet.Target),
		EvidenceFingerprint: knowledge.ComputeRelationEvidenceFingerprint([]knowledge.RelationEvidence{evidence}), Version: 2,
		CreatedAt: at, UpdatedAt: at,
	}
	return knowledgeapplication.ApprovedRelationApplyResult{
		Relation: relation, Evidence: []knowledge.RelationEvidence{evidence}, ProposalStatus: domain.StatusApplied,
		ProposalVersion: 4, Replayed: replayed,
	}
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
	f.createCalls++
	f.proposal = proposal
	return proposal, f.err
}
func (f *fakeRepo) CreateKnowledgeChangeProposal(_ context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	f.proposal = proposal
	return proposal, f.err
}
func (f *fakeRepo) CreatePublishArtifactProposal(_ context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	f.proposal = proposal
	return proposal, f.err
}
func (f *fakeRepo) CreateDownstreamUpdateProposal(_ context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	f.proposal = proposal
	return proposal, f.err
}
func (f *fakeRepo) CreateRestoreDocumentProposal(_ context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	f.createCalls++
	f.proposal = proposal
	return proposal, f.err
}
func (f *fakeRepo) FindProposalByIdempotencyKey(_ context.Context, workspaceID foundation.ID, idempotencyKey string) (domain.Proposal, bool, error) {
	if f.err != nil {
		return domain.Proposal{}, false, f.err
	}
	if f.proposal.ID != "" && f.proposal.WorkspaceID == workspaceID && f.proposal.IdempotencyKey == idempotencyKey {
		return f.proposal, true, nil
	}
	return domain.Proposal{}, false, nil
}
func (f *fakeRepo) BuildDownstreamUpdate(_ context.Context, _, _ foundation.ID, _ knowledge.ImpactObjectType, _ foundation.ID, _ knowledge.ImpactAction) (domain.DownstreamUpdate, error) {
	f.downstreamBuilds++
	return f.downstreamUpdate, f.err
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
	f.authorizationReads++
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
	hash             string
	content          []byte
	err              error
	calls            int
	absenceErr       error
	absenceCalls     int
	absenceWorkspace foundation.ID
	absencePath      string
	absenceToken     string
}

func (f *fakeTargets) CurrentHash(context.Context, foundation.ID, string) (string, error) {
	f.calls++
	return f.hash, f.err
}

func (f *fakeTargets) CurrentContent(context.Context, foundation.ID, string, int64) ([]byte, string, error) {
	f.calls++
	return append([]byte(nil), f.content...), f.hash, f.err
}

func (f *fakeTargets) EnsureTargetAbsent(_ context.Context, workspaceID foundation.ID, targetPath, absenceToken string) error {
	f.absenceCalls++
	f.absenceWorkspace = workspaceID
	f.absencePath = targetPath
	f.absenceToken = absenceToken
	return f.absenceErr
}

type fakeApprovalGitInspector struct {
	snapshot         domain.GitSnapshot
	err              error
	calls            int
	absenceErr       error
	absenceCalls     int
	absenceWorkspace foundation.ID
	absenceHead      string
	absencePath      string
}

func (f *fakeApprovalGitInspector) CaptureApprovalSnapshot(context.Context, foundation.ID) (domain.GitSnapshot, error) {
	f.calls++
	return f.snapshot, f.err
}

func (f *fakeApprovalGitInspector) EnsureTargetAbsentAt(_ context.Context, workspaceID foundation.ID, approvedHead, targetPath string) error {
	f.absenceCalls++
	f.absenceWorkspace = workspaceID
	f.absenceHead = approvedHead
	f.absencePath = targetPath
	return f.absenceErr
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
	if fake, ok := applier.(*fakeKnowledgeRelationApplier); ok {
		fake.workspaceID = repository.proposal.WorkspaceID
		fake.change = repository.proposal.Revision.KnowledgeChange
	}
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
	if fake, ok := applier.(*fakeKnowledgeRelationApplier); ok {
		fake.workspaceID = repository.proposal.WorkspaceID
		fake.change = repository.proposal.Revision.KnowledgeChange
	}
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
		IdempotencyKey: "create-1", RiskLevel: domain.ProposalRiskLevelLow,
		Content: "  code\r\n", EvidenceSummary: "evidence", Risk: "may change editorial structure", RollbackPlan: "revert commit",
	})
	if err != nil {
		t.Fatalf("CreateProposal() error = %v", err)
	}
	proposal := result.Proposal
	if proposal.Type != domain.ProposalTypeFilePatch || proposal.RiskLevel != domain.ProposalRiskLevelLow || proposal.TargetPath != "notes/a.md" || proposal.Revision.TargetPath != "notes/a.md" || proposal.Status != domain.StatusReady || proposal.IdempotencyKey != "create-1" || proposal.RequestHash == "" {
		t.Fatalf("proposal = %#v", proposal)
	}
	expectedRequestHash, err := domain.ComputeRequestHashWithRiskLevel("workspace", "notes/a.md", testHash, "  code\r\n", "evidence", domain.ProposalRiskLevelLow, "may change editorial structure", "revert commit")
	if err != nil || proposal.RequestHash != expectedRequestHash {
		t.Fatalf("request hash = %s, want %s, err=%v", proposal.RequestHash, expectedRequestHash, err)
	}
	wantHash := domain.ComputeChangeHash("notes/a.md", testHash, "  code\r\n")
	if proposal.Revision.ChangeHash != wantHash {
		t.Fatalf("change hash = %s, want %s", proposal.Revision.ChangeHash, wantHash)
	}
	if wantHash == domain.ComputeChangeHash("notes/a.md", testHash, "code\r\n") || wantHash == domain.ComputeChangeHash("other.md", testHash, "  code\r\n") {
		t.Fatal("change hash did not bind semantic content and target")
	}
}

func TestCreateCreateOnlyFileProposalProvesAbsenceBeforePersistence(t *testing.T) {
	repository := &fakeRepo{}
	targets := &fakeTargets{}
	service := newTestService(repository, targets)
	command := CreateCreateOnlyFileProposalCommand{
		WorkspaceID: "workspace", TargetPath: "notes/new.md", IdempotencyKey: "create-only-1",
		Content: "# new\n", EvidenceSummary: "authoring revision", RiskLevel: domain.ProposalRiskLevelLow,
		Risk: "creates a new document", RollbackPlan: "delete the created file through compensation",
	}
	result, err := service.CreateCreateOnlyFileProposal(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	absenceToken, err := domain.ComputeAbsenceToken(command.WorkspaceID, command.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	if targets.absenceCalls != 1 || targets.calls != 0 || targets.absenceWorkspace != command.WorkspaceID || targets.absencePath != command.TargetPath || targets.absenceToken != absenceToken {
		t.Fatalf("absence proof = %#v", targets)
	}
	if repository.createCalls != 1 || result.Proposal.Revision.TargetMode != domain.TargetModeCreateOnly || result.Proposal.Revision.BaseHash != absenceToken {
		t.Fatalf("proposal = %#v, create calls = %d", result.Proposal, repository.createCalls)
	}
	expectedRequestHash, err := domain.ComputeRequestHashWithTargetMode(
		command.WorkspaceID, command.TargetPath, domain.TargetModeCreateOnly, absenceToken, command.Content,
		command.EvidenceSummary, command.RiskLevel, command.Risk, command.RollbackPlan,
	)
	if err != nil || result.Proposal.RequestHash != expectedRequestHash {
		t.Fatalf("request hash = %s, want %s, err=%v", result.Proposal.RequestHash, expectedRequestHash, err)
	}
}

func TestCreateCreateOnlyFileProposalReplaysBeforeCheckingAnAppliedTarget(t *testing.T) {
	repository := &fakeRepo{}
	targets := &fakeTargets{}
	service := newTestService(repository, targets)
	command := CreateCreateOnlyFileProposalCommand{
		WorkspaceID: "workspace", TargetPath: "notes/new.md", IdempotencyKey: "create-only-replay",
		Content: "# new\n", EvidenceSummary: "authoring revision", RiskLevel: domain.ProposalRiskLevelLow,
		Risk: "creates a new document", RollbackPlan: "delete the created file through compensation",
	}
	created, err := service.CreateCreateOnlyFileProposal(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if created.Replayed {
		t.Fatal("first CREATE_ONLY request was reported as a replay")
	}

	// The approved request may already have created the target when a client
	// retries after losing the original response. The immutable request binding
	// must win before observing the now-present file.
	targets.absenceErr = errors.New("target already exists")
	replayed, err := service.CreateCreateOnlyFileProposal(context.Background(), command)
	if err != nil {
		t.Fatalf("exact replay after target creation failed: %v", err)
	}
	if !replayed.Replayed || replayed.Proposal.ID != created.Proposal.ID || repository.createCalls != 1 || targets.absenceCalls != 1 {
		t.Fatalf("replay=%#v create calls=%d absence calls=%d", replayed, repository.createCalls, targets.absenceCalls)
	}

	command.Content = "# different\n"
	_, err = service.CreateCreateOnlyFileProposal(context.Background(), command)
	var projectErr *foundation.Error
	if !errors.As(err, &projectErr) || projectErr.Code != "IDEMPOTENCY_KEY_REUSED" || projectErr.Kind != foundation.ErrorVersionConflict {
		t.Fatalf("mismatched replay error = %v", err)
	}
	if repository.createCalls != 1 || targets.absenceCalls != 1 {
		t.Fatalf("mismatched replay reached external state: create calls=%d absence calls=%d", repository.createCalls, targets.absenceCalls)
	}
}

func TestCreateCreateOnlyFileProposalDoesNotPersistWhenTargetExists(t *testing.T) {
	occupied := errors.New("target already exists")
	repository := &fakeRepo{}
	targets := &fakeTargets{absenceErr: occupied}
	service := newTestService(repository, targets)
	_, err := service.CreateCreateOnlyFileProposal(context.Background(), CreateCreateOnlyFileProposalCommand{
		WorkspaceID: "workspace", TargetPath: "notes/new.md", IdempotencyKey: "create-only-occupied",
		Content: "# new\n", EvidenceSummary: "authoring revision", RiskLevel: domain.ProposalRiskLevelLow,
		Risk: "creates a new document", RollbackPlan: "delete the created file through compensation",
	})
	if !errors.Is(err, occupied) {
		t.Fatalf("error = %v", err)
	}
	if repository.createCalls != 0 || repository.proposal.ID != "" || targets.absenceCalls != 1 {
		t.Fatalf("proposal persisted before absence proof: calls=%d proposal=%#v absence calls=%d", repository.createCalls, repository.proposal, targets.absenceCalls)
	}
}

func TestCreateProposalRequiresExplicitRiskLevel(t *testing.T) {
	repository := &fakeRepo{}
	service := newTestService(repository, &fakeTargets{})
	_, err := service.CreateProposal(context.Background(), CreateCommand{
		WorkspaceID: "workspace", TargetPath: "notes/a.md", BaseHash: testHash, IdempotencyKey: "legacy-create",
		Content: "new", EvidenceSummary: "evidence", Risk: "LOW", RollbackPlan: "rollback",
	})
	if err == nil || repository.proposal.ID != "" {
		t.Fatalf("CreateProposal() proposal=%#v err=%v", repository.proposal, err)
	}
}

func TestGetProposalCurrentContentReportsBaselineDrift(t *testing.T) {
	baseHash := strings.Repeat("a", 64)
	currentHash := strings.Repeat("b", 64)
	repository := &fakeRepo{proposal: domain.Proposal{
		ID: "proposal", WorkspaceID: "workspace", Type: domain.ProposalTypeFilePatch, TargetPath: "notes/a.md",
		Revision: domain.Revision{BaseHash: baseHash, TargetPath: "notes/a.md"},
	}}
	service := newTestService(repository, &fakeTargets{hash: currentHash, content: []byte("current")})
	result, err := service.GetProposalCurrentContent(context.Background(), "proposal")
	if err != nil {
		t.Fatalf("GetProposalCurrentContent() error = %v", err)
	}
	if result.WorkspaceID != "workspace" || result.TargetMode != domain.TargetModeReplace || result.Content != "current" || result.CurrentHash != currentHash || result.BaseHash != baseHash || result.BaseHashMatch {
		t.Fatalf("result = %#v", result)
	}
}

func TestGetProposalCurrentContentUsesEmptyBaselineForCreateOnly(t *testing.T) {
	absenceToken, err := domain.ComputeAbsenceToken("workspace", "notes/new.md")
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakeRepo{proposal: domain.Proposal{
		ID: "proposal", WorkspaceID: "workspace", Type: domain.ProposalTypeFilePatch, TargetPath: "notes/new.md",
		Revision: domain.Revision{TargetPath: "notes/new.md", TargetMode: domain.TargetModeCreateOnly, BaseHash: absenceToken},
	}}
	targets := &fakeTargets{hash: testHash, content: []byte("must not read")}
	service := newTestService(repository, targets)
	result, err := service.GetProposalCurrentContent(context.Background(), "proposal")
	if err != nil {
		t.Fatalf("GetProposalCurrentContent() error = %v", err)
	}
	if result.TargetMode != domain.TargetModeCreateOnly || result.Content != "" || result.CurrentHash != absenceToken || result.BaseHash != absenceToken || !result.BaseHashMatch {
		t.Fatalf("result = %#v", result)
	}
	if targets.calls != 0 || targets.absenceCalls != 1 || targets.absenceWorkspace != "workspace" || targets.absencePath != "notes/new.md" || targets.absenceToken != absenceToken {
		t.Fatalf("reader calls=%d absence calls=%d binding=%s/%s/%s", targets.calls, targets.absenceCalls, targets.absenceWorkspace, targets.absencePath, targets.absenceToken)
	}
}

func TestGetProposalCurrentContentRejectsPersistedUnsafeTarget(t *testing.T) {
	for _, target := range []string{".env", ".git/config", ".git/README.md", ".knowledge/secret.md", "notes/a.txt"} {
		t.Run(target, func(t *testing.T) {
			repository := &fakeRepo{proposal: domain.Proposal{
				ID: "proposal", WorkspaceID: "workspace", Type: domain.ProposalTypeFilePatch, TargetPath: target,
				Revision: domain.Revision{TargetPath: target, BaseHash: testHash},
			}}
			targets := &fakeTargets{content: []byte("must not read"), hash: testHash}
			service := newTestService(repository, targets)
			if _, err := service.GetProposalCurrentContent(context.Background(), "proposal"); err == nil {
				t.Fatal("GetProposalCurrentContent() accepted persisted unsafe target")
			}
			if targets.calls != 0 {
				t.Fatalf("unsafe target reached reader: %d calls", targets.calls)
			}
		})
	}
}

func TestCreateKnowledgeChangeProposalUsesTypedCanonicalHash(t *testing.T) {
	repository := &fakeRepo{}
	service := newTestService(repository, &fakeTargets{})
	change := knowledgeChangeFixture()
	result, err := service.CreateKnowledgeChangeProposal(context.Background(), CreateKnowledgeChangeCommand{
		WorkspaceID: "workspace", IdempotencyKey: "knowledge-create", KnowledgeChange: change, RiskLevel: domain.ProposalRiskLevelHigh, Risk: " semantic relation impact ", RollbackPlan: " restore relation ",
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
	expectedRequestHash, err := domain.ComputeKnowledgeChangeRequestHashWithRiskLevel("workspace", *proposal.Revision.KnowledgeChange, domain.ProposalRiskLevelHigh, "semantic relation impact", "restore relation")
	if err != nil || proposal.RequestHash != expectedRequestHash {
		t.Fatalf("request hash = %s, want %s, err=%v", proposal.RequestHash, expectedRequestHash, err)
	}
	expectedHash, err := domain.ComputeKnowledgeChangeHash(*proposal.Revision.KnowledgeChange, "semantic relation impact", "restore relation")
	if err != nil || proposal.Revision.ChangeHash != expectedHash {
		t.Fatalf("knowledge change hash = %s want %s err=%v", proposal.Revision.ChangeHash, expectedHash, err)
	}
}

func TestCreateKnowledgeChangeProposalRequiresExplicitHighRiskLevel(t *testing.T) {
	for _, riskLevel := range []domain.ProposalRiskLevel{"", domain.ProposalRiskLevelMedium, "high", " HIGH "} {
		repository := &fakeRepo{}
		service := newTestService(repository, &fakeTargets{})
		_, err := service.CreateKnowledgeChangeProposal(context.Background(), CreateKnowledgeChangeCommand{
			WorkspaceID: "workspace", IdempotencyKey: "knowledge-risk", KnowledgeChange: knowledgeChangeFixture(),
			RiskLevel: riskLevel, Risk: "independent risk explanation", RollbackPlan: "restore relation",
		})
		if err == nil || repository.proposal.ID != "" {
			t.Fatalf("risk level %q proposal=%#v err=%v", riskLevel, repository.proposal, err)
		}
	}
}

func TestCreatePublishArtifactProposalFreezesTypedPayload(t *testing.T) {
	repository := &fakeRepo{}
	service := newTestService(repository, &fakeTargets{})
	publication := publishArtifactFixture()
	result, err := service.CreatePublishArtifactProposal(context.Background(), CreatePublishArtifactCommand{
		WorkspaceID: publication.WorkspaceID, IdempotencyKey: "publish-artifact-create", Publication: publication,
		RiskLevel: domain.ProposalRiskLevelHigh, Risk: "formal knowledge publication", RollbackPlan: "retain the isolated artifact",
	})
	if err != nil {
		t.Fatalf("CreatePublishArtifactProposal() error = %v", err)
	}
	proposal := result.Proposal
	if proposal.Type != domain.ProposalTypePublishArtifact || proposal.Revision.PublishArtifact == nil || proposal.TargetPath != "" || proposal.Revision.KnowledgeChange != nil || proposal.Revision.ChangeHash == "" || proposal.RequestHash == "" {
		t.Fatalf("proposal = %#v", proposal)
	}
	if proposal.Revision.PublishArtifact.ArtifactID != publication.ArtifactID || proposal.Revision.PublishArtifact.RevisionID != publication.RevisionID || len(proposal.Revision.PublishArtifact.SourceCoverage) != 2 {
		t.Fatalf("frozen publication = %#v", proposal.Revision.PublishArtifact)
	}
}

func TestDecideProposalWithDispatchLeavesPublishArtifactUnexecuted(t *testing.T) {
	proposal := publishArtifactProposal()
	repository := &fakeRepo{proposal: proposal}
	targets := &fakeTargets{err: errors.New("must not read target")}
	git := &fakeApprovalGitInspector{err: errors.New("must not inspect git")}
	dispatcher := &fakeApprovalDispatcher{err: errors.New("must not dispatch")}
	service := newTestDispatchService(repository, targets, git, dispatcher)

	result, err := service.DecideProposalWithDispatch(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || result.Workflow != nil || result.Approval.Decision != domain.DecisionApproved || targets.calls != 0 || git.calls != 0 || dispatcher.calls != 0 {
		t.Fatalf("result=%#v target=%d git=%d dispatch=%d err=%v", result, targets.calls, git.calls, dispatcher.calls, err)
	}
}

func TestCreateDownstreamUpdateProposalFreezesFactoryBinding(t *testing.T) {
	update := downstreamUpdateFixture()
	repository := &fakeRepo{downstreamUpdate: update}
	service := newTestService(repository, &fakeTargets{})
	result, err := service.CreateDownstreamUpdateProposal(context.Background(), CreateDownstreamUpdateCommand{
		WorkspaceID: update.WorkspaceID, ReportID: update.ReportID, TargetType: update.TargetType,
		TargetID: update.TargetID, Action: update.Action, IdempotencyKey: "downstream-create",
	})
	if err != nil {
		t.Fatalf("CreateDownstreamUpdateProposal() error=%v", err)
	}
	proposal := result.Proposal
	if result.Replayed || repository.downstreamBuilds != 1 || proposal.Type != domain.ProposalTypeDownstreamUpdate || proposal.RiskLevel != domain.ProposalRiskLevelHigh || proposal.Revision.DownstreamUpdate == nil || proposal.Revision.DownstreamUpdate.ReportID != update.ReportID || proposal.Revision.KnowledgeChange != nil || proposal.Revision.PublishArtifact != nil || proposal.TargetPath != "" || proposal.WorkflowRunID != nil {
		t.Fatalf("proposal=%#v builds=%d", proposal, repository.downstreamBuilds)
	}
	if err := domain.ValidateProposalRevisionForType(domain.ProposalTypeDownstreamUpdate, proposal.Revision); err != nil {
		t.Fatalf("frozen revision error=%v", err)
	}
}

func TestCreateDownstreamUpdateProposalReplaysPersistedBindingBeforeFactory(t *testing.T) {
	proposal := downstreamUpdateProposal()
	repository := &fakeRepo{proposal: proposal, downstreamUpdate: domain.DownstreamUpdate{}}
	service := newTestService(repository, &fakeTargets{})
	update := *proposal.Revision.DownstreamUpdate
	result, err := service.CreateDownstreamUpdateProposal(context.Background(), CreateDownstreamUpdateCommand{
		WorkspaceID: update.WorkspaceID, ReportID: update.ReportID, TargetType: update.TargetType,
		TargetID: update.TargetID, Action: update.Action, IdempotencyKey: proposal.IdempotencyKey,
	})
	if err != nil || !result.Replayed || result.Proposal.ID != proposal.ID || repository.downstreamBuilds != 0 {
		t.Fatalf("result=%#v builds=%d err=%v", result, repository.downstreamBuilds, err)
	}
}

func TestDecideProposalWithDispatchLeavesDownstreamUpdateApprovalOnly(t *testing.T) {
	proposal := downstreamUpdateProposal()
	repository := &fakeRepo{proposal: proposal}
	targets := &fakeTargets{err: errors.New("must not read target")}
	git := &fakeApprovalGitInspector{err: errors.New("must not inspect git")}
	dispatcher := &fakeApprovalDispatcher{err: errors.New("must not dispatch")}
	service := newTestDispatchService(repository, targets, git, dispatcher)

	result, err := service.DecideProposalWithDispatch(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || result.Workflow != nil || result.Approval.Decision != domain.DecisionApproved || result.Approval.ApprovedGitHead != nil || targets.calls != 0 || git.calls != 0 || dispatcher.calls != 0 {
		t.Fatalf("result=%#v target=%d git=%d dispatch=%d err=%v", result, targets.calls, git.calls, dispatcher.calls, err)
	}
}

func TestDownstreamUpdateApplyEntrypointsFailClosedWithoutTargetRead(t *testing.T) {
	proposal := downstreamUpdateProposal()
	approved := proposal
	approved.Status = domain.StatusApproved
	approved.Approval = &domain.Approval{
		ID: "65000000-0000-4000-8000-000000000010", ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, Decision: domain.DecisionApproved,
	}
	repository := &fakeRepo{proposal: approved}
	targets := &fakeTargets{err: errors.New("must not read target")}
	service := newTestService(repository, targets)
	assertUnavailable := func(err error) {
		t.Helper()
		var projectErr *foundation.Error
		if !errors.As(err, &projectErr) || projectErr.Code != "DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE" || projectErr.Retryable || projectErr.Kind != foundation.ErrorVersionConflict {
			t.Fatalf("apply guard error=%v", err)
		}
	}
	_, err := service.CheckApplyPreflight(context.Background(), approved.ID, approved.Revision.ID, approved.Revision.ChangeHash)
	assertUnavailable(err)
	_, err = service.IssueWriteAuthorization(context.Background(), domain.AuthorizationIssue{
		WorkspaceID: approved.WorkspaceID, WorkflowRunID: "65000000-0000-4000-8000-000000000011", NodeRunID: "65000000-0000-4000-8000-000000000012",
		ProposalID: approved.ID, RevisionID: approved.Revision.ID, ApprovalID: approved.Approval.ID,
		ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: "target:any", IdempotencyKey: "downstream-auth", TTL: time.Minute,
	})
	assertUnavailable(err)
	_, err = service.ConsumeWriteAuthorization(context.Background(), domain.AuthorizationConsume{
		Credential: "historical-credential", IdempotencyKey: "downstream-auth", WorkspaceID: approved.WorkspaceID,
		WorkflowRunID: "65000000-0000-4000-8000-000000000011", NodeRunID: "65000000-0000-4000-8000-000000000012",
		ProposalID: approved.ID, RevisionID: approved.Revision.ID, ApprovalID: approved.Approval.ID,
		ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: "target:any",
		ApprovedChangeHash: approved.Revision.ChangeHash, TargetVersion: testHash,
	})
	assertUnavailable(err)
	if targets.calls != 0 || repository.markedNeedsReview || repository.authorizationReads != 0 || repository.consumed.Credential != "" {
		t.Fatalf("downstream apply guard touched a writeback dependency: targets=%d marked=%v authorization_reads=%d consumed=%#v", targets.calls, repository.markedNeedsReview, repository.authorizationReads, repository.consumed)
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
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "../a.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "/tmp/a.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: ".env", BaseHash: testHash, Content: "x", EvidenceSummary: "e", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: ".git/config", BaseHash: testHash, Content: "x", EvidenceSummary: "e", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: ".git/README.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: ".knowledge/secret.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "notes/a.txt", BaseHash: testHash, Content: "x", EvidenceSummary: "e", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "a.md", BaseHash: "zz" + testHash[2:], Content: "x", EvidenceSummary: "e", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "a.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", RiskLevel: domain.ProposalRiskLevelLow, Risk: " ", RollbackPlan: "r"},
		{WorkspaceID: "workspace", TargetPath: "a.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", RiskLevel: domain.ProposalRiskLevelLow, Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "a.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", Risk: "low", RollbackPlan: "r"},
		{WorkspaceID: "workspace", IdempotencyKey: "key", TargetPath: "a.md", BaseHash: testHash, Content: "x", EvidenceSummary: "e", RiskLevel: "low", Risk: "low", RollbackPlan: "r"},
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

func TestDecideProposalRejectsInvalidPersistedRiskLevel(t *testing.T) {
	proposal := approvedProposal(testHash)
	proposal.Status = domain.StatusReady
	proposal.Approval = nil
	proposal.RiskLevel = "high"
	repository := &fakeRepo{proposal: proposal}
	_, err := newTestService(repository, &fakeTargets{hash: testHash}).DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "PROPOSAL_RISK_LEVEL_INVALID" || repository.approval.ID != "" {
		t.Fatalf("approval=%#v err=%v", repository.approval, err)
	}
}

func TestDecideKnowledgeChangeProposalBypassesFileMutableFacts(t *testing.T) {
	proposal := knowledgeChangeProposal()
	repository := &fakeRepo{proposal: proposal}
	targets := &fakeTargets{err: errors.New("must not be called")}
	git := &fakeApprovalGitInspector{err: errors.New("must not be called")}
	applier := &fakeKnowledgeRelationApplier{workspaceID: proposal.WorkspaceID, approvalResult: *proposal.Approval}
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
	if !errors.As(err, &classified) || classified.Code != "KNOWLEDGE_RELATION_APPROVAL_UOW_UNAVAILABLE" {
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
	applier := &fakeKnowledgeRelationApplier{workspaceID: proposal.WorkspaceID, approvalResult: *proposal.Approval}
	service := newTestDispatchServiceWithKnowledgeApply(repository, targets, git, dispatcher, applier)

	result, err := service.DecideProposalWithDispatch(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || result.Workflow != nil || result.Approval.Decision != domain.DecisionApproved || targets.calls != 0 || git.calls != 0 || dispatcher.calls != 0 || applier.calls != 1 {
		t.Fatalf("result=%#v target calls=%d git calls=%d dispatch calls=%d apply calls=%d err=%v", result, targets.calls, git.calls, dispatcher.calls, applier.calls, err)
	}
}

func TestDecideProposalWithDispatchUsesAtomicReplayResult(t *testing.T) {
	proposal := knowledgeChangeProposal()
	proposal.Status = domain.StatusReady
	proposal.Version = 1
	proposal.Approval = nil
	applier := &fakeKnowledgeRelationApplier{workspaceID: proposal.WorkspaceID, result: knowledgeapplication.ApprovedRelationApplyResult{Replayed: true}}
	service := newTestDispatchServiceWithKnowledgeApply(&fakeRepo{proposal: proposal}, &fakeTargets{err: errors.New("must not be called")}, &fakeApprovalGitInspector{err: errors.New("must not be called")}, &fakeApprovalDispatcher{err: errors.New("must not be called")}, applier)

	result, err := service.DecideProposalWithDispatch(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	if err != nil || !result.Replayed || result.Approval.ID == "" || applier.calls != 1 {
		t.Fatalf("result=%#v calls=%d err=%v", result, applier.calls, err)
	}
}

func TestDecideKnowledgeChangeProposalRejectsInvalidAtomicApplyResult(t *testing.T) {
	proposal := knowledgeChangeProposal()
	applier := &fakeKnowledgeRelationApplier{
		workspaceID:    proposal.WorkspaceID,
		approvalResult: *proposal.Approval,
		result: knowledgeapplication.ApprovedRelationApplyResult{
			Relation: knowledge.Relation{
				WorkspaceID: "different-workspace", Status: knowledge.RelationStatusConfirmed,
				Confirmation: &knowledge.Confirmation{Method: knowledge.ConfirmationUserApproval, Reference: string(proposal.Approval.ID)},
			},
			Evidence:        []knowledge.RelationEvidence{{}},
			ProposalStatus:  domain.StatusApplied,
			ProposalVersion: 4,
		},
	}
	service := newTestServiceWithKnowledgeApply(&fakeRepo{proposal: proposal}, &fakeTargets{err: errors.New("must not be called")}, &fakeApprovalGitInspector{err: errors.New("must not be called")}, applier)

	_, err := service.DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "KNOWLEDGE_RELATION_APPLY_RESULT_INVALID" {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateApprovedKnowledgeRelationResultAllowsHistoricalConfirmedEvidence(t *testing.T) {
	proposal := knowledgeChangeProposal()
	result := validFakeKnowledgeApplyResult(proposal.WorkspaceID, proposal.Revision.KnowledgeChange, proposal.Approval.ID, false)
	historicalConfirmation := knowledge.Confirmation{Method: knowledge.ConfirmationUserApproval, Reference: "historical-approval"}
	historicalEvidence := result.Evidence[0]
	historicalEvidence.ID = "70000000-0000-4000-8000-000000000005"
	historicalEvidence.Provenance.SourceSpanID = "70000000-0000-4000-8000-000000000006"
	historicalEvidence.Reason = "historical confirmed evidence"
	historicalEvidence.Confirmation = &historicalConfirmation
	historicalEvidence.EvidenceHash = knowledge.ComputeRelationEvidenceHash(historicalEvidence)
	result.Evidence = append([]knowledge.RelationEvidence{historicalEvidence}, result.Evidence...)
	result.Relation.EvidenceFingerprint = knowledge.ComputeRelationEvidenceFingerprint(result.Evidence)

	if err := validateApprovedKnowledgeRelationResult(proposal, *proposal.Approval, result); err != nil {
		t.Fatalf("historical confirmed evidence should remain valid: %v", err)
	}
}

func TestDecideKnowledgeChangeProposalRejectsSkippedApplyVersion(t *testing.T) {
	proposal := knowledgeChangeProposal()
	result := validFakeKnowledgeApplyResult(proposal.WorkspaceID, proposal.Revision.KnowledgeChange, proposal.Approval.ID, false)
	result.ProposalVersion = 99
	applier := &fakeKnowledgeRelationApplier{workspaceID: proposal.WorkspaceID, approvalResult: *proposal.Approval, result: result}
	service := newTestServiceWithKnowledgeApply(&fakeRepo{proposal: proposal}, &fakeTargets{err: errors.New("must not be called")}, &fakeApprovalGitInspector{err: errors.New("must not be called")}, applier)

	_, err := service.DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "KNOWLEDGE_RELATION_APPLY_RESULT_INVALID" {
		t.Fatalf("err=%v", err)
	}
}

func TestDecideKnowledgeChangeProposalRequiresReplayForAppliedProposal(t *testing.T) {
	proposal := knowledgeChangeProposal()
	proposal.Status = domain.StatusApplied
	proposal.Version = 4
	result := validFakeKnowledgeApplyResult(proposal.WorkspaceID, proposal.Revision.KnowledgeChange, proposal.Approval.ID, false)
	result.ProposalVersion = proposal.Version
	applier := &fakeKnowledgeRelationApplier{workspaceID: proposal.WorkspaceID, approvalResult: *proposal.Approval, result: result}
	service := newTestServiceWithKnowledgeApply(&fakeRepo{proposal: proposal}, &fakeTargets{err: errors.New("must not be called")}, &fakeApprovalGitInspector{err: errors.New("must not be called")}, applier)

	_, err := service.DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "KNOWLEDGE_RELATION_APPLY_RESULT_INVALID" {
		t.Fatalf("err=%v", err)
	}
}

func TestDecideKnowledgeChangeProposalRejectsDifferentPersistedApproval(t *testing.T) {
	proposal := knowledgeChangeProposal()
	result := validFakeKnowledgeApplyResult(proposal.WorkspaceID, proposal.Revision.KnowledgeChange, proposal.Approval.ID, false)
	otherApproval := *proposal.Approval
	otherApproval.ID = "60000000-0000-4000-8000-000000000005"
	applier := &fakeKnowledgeRelationApplier{workspaceID: proposal.WorkspaceID, approvalResult: otherApproval, result: result}
	service := newTestServiceWithKnowledgeApply(&fakeRepo{proposal: proposal}, &fakeTargets{err: errors.New("must not be called")}, &fakeApprovalGitInspector{err: errors.New("must not be called")}, applier)

	_, err := service.DecideProposal(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash, domain.DecisionApproved)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "KNOWLEDGE_RELATION_APPROVAL_BINDING_INVALID" {
		t.Fatalf("err=%v", err)
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
	if err != nil || result.TargetMode != domain.TargetModeReplace || result.BaseHash != testHash || repository.markedNeedsReview {
		t.Fatalf("result = %#v, marked = %v, err = %v", result, repository.markedNeedsReview, err)
	}
}

func TestApplyPreflightRechecksCreateOnlyFilesystemAndApprovedGitTree(t *testing.T) {
	absenceToken, err := domain.ComputeAbsenceToken("workspace", "a.md")
	if err != nil {
		t.Fatal(err)
	}
	proposal := approvedProposal(absenceToken)
	proposal.Revision.TargetMode = domain.TargetModeCreateOnly
	proposal.Revision.ChangeHash, err = domain.ComputeChangeHashForTarget(
		proposal.WorkspaceID,
		proposal.Revision.TargetPath,
		domain.TargetModeCreateOnly,
		absenceToken,
		proposal.Revision.Content,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal.Approval.ChangeHash = proposal.Revision.ChangeHash
	targets := &fakeTargets{}
	git := &fakeApprovalGitInspector{snapshot: domain.GitSnapshot{
		WorkspaceID: "workspace", Branch: "main", Head: testGitHead, ObjectFormat: domain.GitObjectFormatSHA1, Clean: true,
	}}
	service := newTestServiceWithGit(&fakeRepo{proposal: proposal}, targets, git)
	result, err := service.CheckApplyPreflight(context.Background(), proposal.ID, proposal.Revision.ID, proposal.Revision.ChangeHash)
	if err != nil {
		t.Fatalf("CheckApplyPreflight() error = %v", err)
	}
	if result.TargetMode != domain.TargetModeCreateOnly || result.BaseHash != absenceToken {
		t.Fatalf("result = %#v", result)
	}
	if targets.calls != 0 || targets.absenceCalls != 1 || targets.absenceToken != absenceToken {
		t.Fatalf("target reader calls=%d absence calls=%d token=%q", targets.calls, targets.absenceCalls, targets.absenceToken)
	}
	if git.calls != 0 || git.absenceCalls != 1 || git.absenceWorkspace != proposal.WorkspaceID || git.absenceHead != testGitHead || git.absencePath != proposal.TargetPath {
		t.Fatalf("git calls=%d absence calls=%d binding=%s/%s/%s", git.calls, git.absenceCalls, git.absenceWorkspace, git.absenceHead, git.absencePath)
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
		ID: "proposal", WorkspaceID: "workspace", Type: domain.ProposalTypeFilePatch, RiskLevel: domain.ProposalRiskLevelHigh, TargetPath: "a.md", Status: domain.StatusApproved,
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
		ID: "60000000-0000-4000-8000-000000000002", WorkspaceID: "60000000-0000-4000-8000-000000000001", Type: domain.ProposalTypeKnowledgeChange, RiskLevel: domain.ProposalRiskLevelHigh, Status: domain.StatusApproved, Version: 2,
		Revision: domain.Revision{ID: "60000000-0000-4000-8000-000000000003", ProposalID: "60000000-0000-4000-8000-000000000002", Risk: "medium", RollbackPlan: "restore relation", ChangeHash: hash, KnowledgeChange: &change},
		Approval: &domain.Approval{ID: "60000000-0000-4000-8000-000000000004", ProposalID: "60000000-0000-4000-8000-000000000002", RevisionID: "60000000-0000-4000-8000-000000000003", ChangeHash: hash, Decision: domain.DecisionApproved},
	}
}

func publishArtifactFixture() domain.PublishArtifact {
	publication := domain.PublishArtifact{
		WorkspaceID:     "61000000-0000-4000-8000-000000000001",
		ArtifactID:      "61000000-0000-4000-8000-000000000002",
		RevisionID:      "61000000-0000-4000-8000-000000000003",
		RevisionNo:      4,
		ArtifactVersion: 7,
		ContentHash:     strings.ToUpper(strings.Repeat("c", 64)),
		SourceCoverage: []domain.ArtifactSourceCoverage{
			{SectionKey: "outcomes", Status: domain.ArtifactCoveragePartial, Gaps: []domain.ArtifactCoverageGap{{Code: "MISSING_SOURCE", Description: "requires an approved source"}}},
			{SectionKey: "summary", Status: domain.ArtifactCoverageCovered},
		},
		SchemaVersion: domain.PublishArtifactSchemaVersion,
	}
	canonical, err := domain.ValidatePublishArtifact(publication)
	if err != nil {
		panic(err)
	}
	return canonical
}

func publishArtifactProposal() domain.Proposal {
	publication := publishArtifactFixture()
	hash, err := domain.ComputePublishArtifactHash(publication, "formal knowledge publication", "retain the isolated artifact")
	if err != nil {
		panic(err)
	}
	return domain.Proposal{
		ID: "61000000-0000-4000-8000-000000000004", WorkspaceID: publication.WorkspaceID, Type: domain.ProposalTypePublishArtifact, RiskLevel: domain.ProposalRiskLevelHigh, Status: domain.StatusReady, Version: 1,
		Revision: domain.Revision{ID: "61000000-0000-4000-8000-000000000005", ProposalID: "61000000-0000-4000-8000-000000000004", Risk: "formal knowledge publication", RollbackPlan: "retain the isolated artifact", ChangeHash: hash, PublishArtifact: &publication},
	}
}

func downstreamUpdateFixture() domain.DownstreamUpdate {
	return domain.DownstreamUpdate{
		WorkspaceID: "65000000-0000-4000-8000-000000000001", ReportID: "65000000-0000-4000-8000-000000000002",
		AnalysisVersion: knowledge.ImpactAnalysisVersionV2, ReportFingerprint: strings.Repeat("a", 64),
		SourceEventID: "65000000-0000-4000-8000-000000000003", SourceEventVersion: 2,
		TargetType: knowledge.ImpactObjectArtifact, TargetID: "65000000-0000-4000-8000-000000000004", BaseVersion: 5,
		Action: knowledge.ImpactActionRegenerateArtifact,
		OwnerBinding: knowledge.EventOwnerBinding{Artifact: &knowledge.ArtifactImpactBinding{
			ArtifactID: "65000000-0000-4000-8000-000000000004", ArtifactVersion: 5,
			RevisionID: "65000000-0000-4000-8000-000000000005", RevisionNo: 2, ContentHash: strings.Repeat("b", 64),
		}},
		Reason: "cited source changed", SchemaVersion: domain.DownstreamUpdateSchemaVersion,
	}
}

func downstreamUpdateProposal() domain.Proposal {
	update := downstreamUpdateFixture()
	const risk = "Impact report identified an owner-backed downstream dependency"
	const rollback = "No target write has executed; future execution requires a new Proposal revision and owner executor"
	hash, err := domain.ComputeDownstreamUpdateHash(update, risk, rollback)
	if err != nil {
		panic(err)
	}
	requestHash, err := domain.ComputeDownstreamUpdateRequestHash(update.WorkspaceID, update, domain.ProposalRiskLevelHigh, risk, rollback)
	if err != nil {
		panic(err)
	}
	return domain.Proposal{
		ID: "65000000-0000-4000-8000-000000000006", WorkspaceID: update.WorkspaceID, Type: domain.ProposalTypeDownstreamUpdate,
		RiskLevel: domain.ProposalRiskLevelHigh, IdempotencyKey: "downstream-create", RequestHash: requestHash, Status: domain.StatusReady, Version: 1,
		Revision: domain.Revision{
			ID: "65000000-0000-4000-8000-000000000007", ProposalID: "65000000-0000-4000-8000-000000000006", RevisionNo: 1,
			Risk: risk, RollbackPlan: rollback, ChangeHash: hash, DownstreamUpdate: &update,
		},
	}
}
