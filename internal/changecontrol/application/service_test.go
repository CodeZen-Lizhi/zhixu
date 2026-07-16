package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const testHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeRepo struct {
	proposal          domain.Proposal
	approval          domain.Approval
	err               error
	markedNeedsReview bool
}

func (f *fakeRepo) CreateProposal(_ context.Context, proposal domain.Proposal) (domain.Proposal, error) {
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

type fakeTargets struct {
	hash string
	err  error
}

func (f *fakeTargets) CurrentHash(context.Context, foundation.ID, string) (string, error) {
	return f.hash, f.err
}

type seqIDs struct{ n int }

func (s *seqIDs) New() (foundation.ID, error) {
	s.n++
	return foundation.ID("00000000-0000-4000-8000-00000000000" + string(rune('0'+s.n))), nil
}

func newTestService(repository *fakeRepo, targets *fakeTargets) *Service {
	service, err := NewService(repository, &seqIDs{}, foundation.FixedClock{Value: time.Unix(1, 0)}, targets)
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
	if proposal.TargetPath != "notes/a.md" || proposal.Revision.TargetPath != "notes/a.md" || proposal.Status != domain.StatusReady || proposal.IdempotencyKey != "create-1" || proposal.RequestHash == "" {
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
	if err != nil || repository.approval.ChangeHash != proposal.Revision.ChangeHash || repository.approval.Decision != domain.DecisionApproved {
		t.Fatalf("approval = %#v, err = %v", repository.approval, err)
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

func approvedProposal(baseHash string) domain.Proposal {
	changeHash := domain.ComputeChangeHash("a.md", baseHash, "new content")
	return domain.Proposal{
		ID: "proposal", WorkspaceID: "workspace", TargetPath: "a.md", Status: domain.StatusApproved,
		Revision: domain.Revision{ID: "revision", ProposalID: "proposal", TargetPath: "a.md", BaseHash: baseHash, Content: "new content", ChangeHash: changeHash},
		Approval: &domain.Approval{ID: "approval", ProposalID: "proposal", RevisionID: "revision", ChangeHash: changeHash, Decision: domain.DecisionApproved},
	}
}
