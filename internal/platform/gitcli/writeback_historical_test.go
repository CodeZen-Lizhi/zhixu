package gitcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	changecontrol "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
)

func TestHistoricalSameBytesRequiresExactAuthorityAndRecoversCommit(t *testing.T) {
	repository := newWritebackTestRepository(t, "", "notes/history.md")
	content, err := os.ReadFile(filepath.Join(repository.root, repository.target))
	if err != nil {
		t.Fatal(err)
	}
	resultHash := changecontrol.ComputeWritebackResultHash(content)
	if _, err := repository.client.DiffApproved(context.Background(), repository.diffRequest(resultHash)); err == nil {
		t.Fatal("ordinary unchanged writeback accepted")
	}
	authority := changecontrol.HistoricalRepublishGitAuthority{ReceiptID: "historical-receipt", WorkspaceID: writebackTestWorkspaceID, WorkflowRunID: "workflow-run", NodeRunID: "node-run", ExecutionID: "execution", ProposalID: "proposal", RevisionID: "revision", ApprovalID: "approval", TargetPath: repository.target, ApprovedGitHead: repository.head, ContentHash: resultHash}
	ctx, err := changecontrol.WithHistoricalRepublishGitAuthority(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := repository.client.DiffApproved(ctx, repository.diffRequest(resultHash))
	if err != nil {
		t.Fatal(err)
	}
	request := changecontrol.GitCommitRequest{WorkspaceID: writebackTestWorkspaceID, WorkflowRunID: authority.WorkflowRunID, NodeRunID: authority.NodeRunID, WritebackExecutionID: authority.ExecutionID, ProposalID: authority.ProposalID, RevisionID: authority.RevisionID, ApprovalID: authority.ApprovalID, Operation: changecontrol.GitOperationApply, TargetMode: changecontrol.TargetModeReplace, TargetPath: diff.TargetPath, ApprovedGitHead: diff.ApprovedGitHead, ResultHash: diff.ResultHash, DiffHash: diff.DiffHash, BaseBlobID: diff.BaseBlobID, ResultBlobID: diff.ResultBlobID, BaseMode: diff.BaseMode}
	wrong := request
	wrong.WritebackExecutionID = "other-execution"
	if _, err := repository.client.CommitApproved(ctx, wrong); err == nil {
		t.Fatal("authority accepted another execution")
	}
	if _, err := repository.client.CommitApproved(context.Background(), request); err == nil {
		t.Fatal("empty commit accepted without authority")
	}
	commit, err := newCommitFaultClient(t, repository, true).CommitApproved(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !commit.Recovered || commit.GitCommit == repository.head {
		t.Fatalf("commit did not recover distinct identity: %#v", commit)
	}
	if paths := runWritebackGit(t, repository.root, "diff", "--name-only", repository.head, commit.GitCommit); paths != "" {
		t.Fatalf("tree changed: %q", paths)
	}
	object, err := repository.client.readCommitObject(ctx, repository.root, commit.GitCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(object.message, "Zhixu-Historical-Republish-Receipt: historical-receipt\n") {
		t.Fatalf("missing receipt: %s", object.message)
	}
	replay, err := repository.client.CommitApproved(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.GitCommit != commit.GitCommit {
		t.Fatalf("replay: %#v", replay)
	}
	authority.ReceiptID = "other-receipt"
	wrongCtx, err := changecontrol.WithHistoricalRepublishGitAuthority(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.client.CommitApproved(wrongCtx, request); err == nil {
		t.Fatal("replay accepted another receipt")
	}
	if _, err := repository.client.CommitApproved(context.Background(), request); err == nil {
		t.Fatal("replay accepted without historical authority")
	}
}
