package application

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	restoreTestWorkspaceID  foundation.ID = "51000000-0000-4000-8000-000000000001"
	restoreTestDocumentID   foundation.ID = "51000000-0000-4000-8000-000000000002"
	restoreTestTargetCommit               = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	restoreTestHead                       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestCreateRestoreDocumentProposalRevalidatesFileGitAndReplays(t *testing.T) {
	repository := &fakeRepo{}
	targets := &fakeTargets{hash: domain.ComputeContentHash([]byte("current\n"))}
	git := &fakeApprovalGitInspector{snapshot: restoreTestSnapshot(restoreTestHead, true)}
	service := newTestServiceWithGit(repository, targets, git)
	command := restoreTestCreateCommand()

	created, err := service.CreateRestoreDocumentProposal(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	proposal := created.Proposal
	if created.Replayed || repository.createCalls != 1 || targets.calls != 1 || git.calls != 1 ||
		proposal.Type != domain.ProposalTypeRestoreDocument || proposal.RiskLevel != domain.ProposalRiskLevelHigh ||
		proposal.Status != domain.StatusReady || proposal.Revision.RestoreDocument == nil || proposal.Revision.Content != "target\n" {
		t.Fatalf("created=%+v targetCalls=%d gitCalls=%d", created, targets.calls, git.calls)
	}

	targetCalls, gitCalls := targets.calls, git.calls
	replayed, err := service.CreateRestoreDocumentProposal(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Proposal.ID != proposal.ID || repository.createCalls != 1 || targets.calls != targetCalls || git.calls != gitCalls {
		t.Fatalf("replayed=%+v create=%d target=%d git=%d", replayed, repository.createCalls, targets.calls, git.calls)
	}
}

func TestCreateRestoreDocumentProposalFailsClosedOnPathFileAndGitDrift(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*CreateRestoreDocumentCommand, *fakeTargets, *fakeApprovalGitInspector)
		wantCode string
	}{
		{name: "reserved path", mutate: func(command *CreateRestoreDocumentCommand, _ *fakeTargets, _ *fakeApprovalGitInspector) {
			command.TargetPath = ".git/config.md"
		}, wantCode: "DOCUMENT_RESTORE_PROPOSAL_INVALID"},
		{name: "file drift", mutate: func(_ *CreateRestoreDocumentCommand, targets *fakeTargets, _ *fakeApprovalGitInspector) {
			targets.hash = domain.ComputeContentHash([]byte("drift\n"))
		}, wantCode: "DOCUMENT_RESTORE_STALE"},
		{name: "head drift", mutate: func(_ *CreateRestoreDocumentCommand, _ *fakeTargets, git *fakeApprovalGitInspector) {
			git.snapshot.Head = "cccccccccccccccccccccccccccccccccccccccc"
		}, wantCode: "DOCUMENT_RESTORE_STALE"},
		{name: "dirty", mutate: func(_ *CreateRestoreDocumentCommand, _ *fakeTargets, git *fakeApprovalGitInspector) {
			git.snapshot.Clean = false
		}, wantCode: "DOCUMENT_RESTORE_STALE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepo{}
			targets := &fakeTargets{hash: domain.ComputeContentHash([]byte("current\n"))}
			git := &fakeApprovalGitInspector{snapshot: restoreTestSnapshot(restoreTestHead, true)}
			command := restoreTestCreateCommand()
			test.mutate(&command, targets, git)
			service := newTestServiceWithGit(repository, targets, git)
			_, err := service.CreateRestoreDocumentProposal(context.Background(), command)
			assertRestoreApplicationError(t, err, test.wantCode)
			if repository.createCalls != 0 {
				t.Fatalf("repository called %d times", repository.createCalls)
			}
		})
	}
}

func TestRestoreApprovalRevalidatesExpectedHeadBeforeDispatch(t *testing.T) {
	repository := &fakeRepo{}
	targets := &fakeTargets{hash: domain.ComputeContentHash([]byte("current\n"))}
	git := &fakeApprovalGitInspector{snapshot: restoreTestSnapshot(restoreTestHead, true)}
	service := newTestServiceWithGit(repository, targets, git)
	created, err := service.CreateRestoreDocumentProposal(context.Background(), restoreTestCreateCommand())
	if err != nil {
		t.Fatal(err)
	}
	git.snapshot.Head = "cccccccccccccccccccccccccccccccccccccccc"
	_, err = service.DecideProposal(context.Background(), created.Proposal.ID, created.Proposal.Revision.ID, created.Proposal.Revision.ChangeHash, domain.DecisionApproved)
	assertRestoreApplicationError(t, err, "DOCUMENT_RESTORE_STALE")
	if repository.approval.ID != "" {
		t.Fatalf("approval persisted after stale HEAD: %+v", repository.approval)
	}

	dispatcher := &fakeApprovalDispatcher{}
	dispatchService := newTestDispatchService(repository, targets, git, dispatcher)
	_, err = dispatchService.DecideProposalWithDispatch(context.Background(), created.Proposal.ID, created.Proposal.Revision.ID, created.Proposal.Revision.ChangeHash, domain.DecisionApproved)
	assertRestoreApplicationError(t, err, "DOCUMENT_RESTORE_STALE")
	if dispatcher.calls != 0 {
		t.Fatalf("dispatcher called %d times after stale HEAD", dispatcher.calls)
	}
}

func restoreTestCreateCommand() CreateRestoreDocumentCommand {
	currentHash := domain.ComputeContentHash([]byte("current\n"))
	targetHash := domain.ComputeContentHash([]byte("target\n"))
	return CreateRestoreDocumentCommand{
		WorkspaceID: restoreTestWorkspaceID, TargetPath: "notes/java-ai.md", IdempotencyKey: "restore-document-test",
		Content: "target\n", EvidenceSummary: "server verified history target", Risk: "newer information may be removed", RollbackPlan: "restore the previous HEAD through a new proposal",
		Restore: domain.RestoreDocument{
			WorkspaceID: restoreTestWorkspaceID, DocumentID: restoreTestDocumentID,
			TargetCommit: restoreTestTargetCommit, ExpectedHead: restoreTestHead,
			ExpectedDocumentVersion: 7, PreviewHash: domain.ComputeContentHash([]byte("preview")),
			CurrentContentHash: currentHash, TargetContentHash: targetHash, SchemaVersion: domain.RestoreDocumentSchemaVersion,
		},
	}
}

func restoreTestSnapshot(head string, clean bool) domain.GitSnapshot {
	return domain.GitSnapshot{
		WorkspaceID: restoreTestWorkspaceID, Branch: "main", Head: head,
		ObjectFormat: domain.GitObjectFormatSHA1, Clean: clean,
	}
}

func assertRestoreApplicationError(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v, want code=%s", err, code)
	}
}
