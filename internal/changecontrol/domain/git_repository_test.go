package domain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	testGitHead    = "1111111111111111111111111111111111111111"
	testGitCommit  = "2222222222222222222222222222222222222222"
	testBaseBlob   = "3333333333333333333333333333333333333333"
	testResultBlob = "4444444444444444444444444444444444444444"
)

func validGitDiffRequest() GitDiffRequest {
	return GitDiffRequest{
		WorkspaceID:     "workspace",
		TargetPath:      "notes/- 安全 写回.md",
		ApprovedGitHead: testGitHead,
		ResultHash:      strings.Repeat("a", 64),
	}
}

func validGitDiffValue() GitDiff {
	request := validGitDiffRequest()
	return GitDiff{
		WorkspaceID: request.WorkspaceID, TargetPath: request.TargetPath, ApprovedGitHead: request.ApprovedGitHead,
		ResultHash: request.ResultHash, DiffHash: strings.Repeat("b", 64),
		BaseBlobID: testBaseBlob, ResultBlobID: testResultBlob, BaseMode: GitFileModeRegular,
	}
}

func validGitCommitRequest() GitCommitRequest {
	diff := validGitDiffValue()
	return GitCommitRequest{
		WorkspaceID: diff.WorkspaceID, WorkflowRunID: "workflow-run", NodeRunID: "node-run",
		WritebackExecutionID: "execution", ProposalID: "proposal", RevisionID: "revision", ApprovalID: "approval",
		Operation: GitOperationApply, TargetPath: diff.TargetPath, ApprovedGitHead: diff.ApprovedGitHead,
		ResultHash: diff.ResultHash, DiffHash: diff.DiffHash, BaseBlobID: diff.BaseBlobID,
		ResultBlobID: diff.ResultBlobID, BaseMode: diff.BaseMode,
	}
}

func commitFromRequest(request GitCommitRequest) GitCommit {
	return GitCommit{
		WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID, NodeRunID: request.NodeRunID,
		WritebackExecutionID: request.WritebackExecutionID, ProposalID: request.ProposalID,
		RevisionID: request.RevisionID, ApprovalID: request.ApprovalID, Operation: request.Operation,
		TargetPath: request.TargetPath, ApprovedGitHead: request.ApprovedGitHead,
		GitCommit: testGitCommit, ParentGitCommit: request.ApprovedGitHead,
		ResultHash: request.ResultHash, DiffHash: request.DiffHash, BaseBlobID: request.BaseBlobID,
		ResultBlobID: request.ResultBlobID, BaseMode: request.BaseMode,
	}
}

func lookupFromRequest(request GitCommitRequest) GitCommitLookup {
	return GitCommitLookup{
		WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID, NodeRunID: request.NodeRunID,
		WritebackExecutionID: request.WritebackExecutionID, ProposalID: request.ProposalID,
		RevisionID: request.RevisionID, ApprovalID: request.ApprovalID, Operation: request.Operation,
		TargetPath: request.TargetPath, ApprovedGitHead: request.ApprovedGitHead,
		ResultHash: request.ResultHash, DiffHash: request.DiffHash, BaseBlobID: request.BaseBlobID,
		ResultBlobID: request.ResultBlobID, BaseMode: request.BaseMode,
	}
}

func TestGitRepositoryContractCompiles(t *testing.T) {
	var _ GitRepository = gitRepositoryStub{}
}

func TestGitStableCommitMetadata(t *testing.T) {
	if GitCommitSubject != "ZHIXU: apply approved proposal" || GitReverseCommitSubject != "ZHIXU: revert approved proposal" {
		t.Fatal("fixed git subjects changed")
	}
	if GitCommitLookupLimit != 256 {
		t.Fatal("bounded git lookup contract changed")
	}
	wantTrailers := []string{
		GitTrailerWritebackID, GitTrailerOperation, GitTrailerProposalID, GitTrailerRevisionID,
		GitTrailerApprovalID, GitTrailerWorkflowRunID, GitTrailerWorkflowNodeID,
		GitTrailerTargetPath, GitTrailerResultSHA256, GitTrailerDiffSHA256, GitTrailerRevertsCommit,
	}
	seen := make(map[string]struct{}, len(wantTrailers))
	for _, trailer := range wantTrailers {
		if trailer == "" {
			t.Fatal("empty stable trailer key")
		}
		if _, exists := seen[trailer]; exists {
			t.Fatalf("duplicate stable trailer key %q", trailer)
		}
		seen[trailer] = struct{}{}
	}
}

func TestValidateGitSnapshotBinding(t *testing.T) {
	valid := GitSnapshot{WorkspaceID: "workspace", Branch: "main", Head: testGitHead, ObjectFormat: GitObjectFormatSHA1, Clean: true}
	if err := ValidateGitSnapshotBinding("workspace", strings.ToUpper(testGitHead), valid); err != nil {
		t.Fatal(err)
	}
	sha256Head := strings.Repeat("a", 64)
	if err := ValidateGitSnapshotBinding("workspace", sha256Head, GitSnapshot{WorkspaceID: "workspace", Branch: "main", Head: sha256Head, ObjectFormat: GitObjectFormatSHA256, Clean: true}); err != nil {
		t.Fatal(err)
	}
	dirty := valid
	dirty.Clean = false
	if !errors.Is(ValidateGitSnapshotBinding("workspace", testGitHead, dirty), ErrGitVersionConflict) {
		t.Fatal("dirty snapshot accepted")
	}
	drifted := valid
	drifted.Head = strings.Repeat("f", 40)
	if !errors.Is(ValidateGitSnapshotBinding("workspace", testGitHead, drifted), ErrGitVersionConflict) {
		t.Fatal("head drift accepted")
	}
	wrongFormat := valid
	wrongFormat.ObjectFormat = GitObjectFormatSHA256
	if !errors.Is(ValidateGitSnapshotBinding("workspace", testGitHead, wrongFormat), ErrGitInvalidInput) {
		t.Fatal("object format and oid width mismatch accepted")
	}
}

func TestValidateGitDiffBinding(t *testing.T) {
	request := validGitDiffRequest()
	diff := validGitDiffValue()
	if err := ValidateGitDiffBinding(request, diff); err != nil {
		t.Fatal(err)
	}

	for _, targetPath := range []string{"notes/a.txt", "notes/line\nbreak.md", "notes/tab\tname.md", ".knowledge/a.md", "notes/./a.md"} {
		invalid := request
		invalid.TargetPath = targetPath
		if !errors.Is(ValidateGitDiffRequest(invalid), ErrGitInvalidInput) {
			t.Fatalf("unsafe git target %q accepted", targetPath)
		}
	}

	changed := diff
	changed.ResultHash = strings.Repeat("c", 64)
	if !errors.Is(ValidateGitDiffBinding(request, changed), ErrGitConsistencyViolation) {
		t.Fatal("diff result hash mismatch accepted")
	}
	changed = diff
	changed.ResultBlobID = strings.Repeat("d", 64)
	if !errors.Is(ValidateGitDiffBinding(request, changed), ErrGitConsistencyViolation) {
		t.Fatal("mixed sha-1 and sha-256 object ids accepted")
	}
	changed = diff
	changed.BaseMode = "120000"
	if !errors.Is(ValidateGitDiffBinding(request, changed), ErrGitConsistencyViolation) {
		t.Fatal("symlink git mode accepted")
	}
}

func TestValidateGitCommitBinding(t *testing.T) {
	request := validGitCommitRequest()
	commit := commitFromRequest(request)
	if err := ValidateGitCommitBinding(request, commit); err != nil {
		t.Fatal(err)
	}

	idMutations := []struct {
		name   string
		mutate func(*GitCommitRequest)
	}{
		{name: "workspace", mutate: func(value *GitCommitRequest) { value.WorkspaceID = "" }},
		{name: "workflow", mutate: func(value *GitCommitRequest) { value.WorkflowRunID = "workflow\nrun" }},
		{name: "node", mutate: func(value *GitCommitRequest) { value.NodeRunID = "" }},
		{name: "execution", mutate: func(value *GitCommitRequest) { value.WritebackExecutionID = "" }},
		{name: "proposal", mutate: func(value *GitCommitRequest) { value.ProposalID = "" }},
		{name: "revision", mutate: func(value *GitCommitRequest) { value.RevisionID = "" }},
		{name: "approval", mutate: func(value *GitCommitRequest) { value.ApprovalID = "" }},
	}
	for _, test := range idMutations {
		t.Run("invalid id "+test.name, func(t *testing.T) {
			invalid := request
			test.mutate(&invalid)
			if !errors.Is(ValidateGitCommitRequest(invalid), ErrGitInvalidInput) {
				t.Fatal("invalid trailer id accepted")
			}
		})
	}
	wrongOperation := request
	wrongOperation.Operation = GitOperationRevert
	if !errors.Is(ValidateGitCommitRequest(wrongOperation), ErrGitInvalidInput) {
		t.Fatal("revert accepted by forward commit request")
	}

	mutations := []struct {
		name   string
		mutate func(*GitCommit)
	}{
		{name: "execution", mutate: func(value *GitCommit) { value.WritebackExecutionID = "other" }},
		{name: "proposal", mutate: func(value *GitCommit) { value.ProposalID = "other" }},
		{name: "revision", mutate: func(value *GitCommit) { value.RevisionID = "other" }},
		{name: "approval", mutate: func(value *GitCommit) { value.ApprovalID = "other" }},
		{name: "workflow", mutate: func(value *GitCommit) { value.WorkflowRunID = "other" }},
		{name: "node", mutate: func(value *GitCommit) { value.NodeRunID = "other" }},
		{name: "target", mutate: func(value *GitCommit) { value.TargetPath = "other.md" }},
		{name: "parent", mutate: func(value *GitCommit) { value.ParentGitCommit = strings.Repeat("5", 40) }},
		{name: "result", mutate: func(value *GitCommit) { value.ResultHash = strings.Repeat("5", 64) }},
		{name: "diff", mutate: func(value *GitCommit) { value.DiffHash = strings.Repeat("6", 64) }},
		{name: "base blob", mutate: func(value *GitCommit) { value.BaseBlobID = strings.Repeat("7", 40) }},
		{name: "result blob", mutate: func(value *GitCommit) { value.ResultBlobID = strings.Repeat("8", 40) }},
		{name: "mode", mutate: func(value *GitCommit) { value.BaseMode = GitFileModeExecutable }},
		{name: "operation", mutate: func(value *GitCommit) { value.Operation = GitOperationRevert; value.RevertsCommit = testGitCommit }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := commit
			test.mutate(&changed)
			if !errors.Is(ValidateGitCommitBinding(request, changed), ErrGitConsistencyViolation) {
				t.Fatalf("mutated commit accepted: %+v", changed)
			}
		})
	}
	both := commit
	both.Recovered, both.Replayed = true, true
	if !errors.Is(ValidateGitCommitBinding(request, both), ErrGitConsistencyViolation) {
		t.Fatal("commit marked recovered and replayed accepted")
	}

	sha256Request := request
	sha256Request.ApprovedGitHead = strings.Repeat("1", 64)
	sha256Request.BaseBlobID = strings.Repeat("3", 64)
	sha256Request.ResultBlobID = strings.Repeat("4", 64)
	sha256Commit := commitFromRequest(sha256Request)
	sha256Commit.GitCommit = strings.Repeat("2", 64)
	if err := ValidateGitCommitBinding(sha256Request, sha256Commit); err != nil {
		t.Fatalf("sha-256 commit binding rejected: %v", err)
	}
}

func TestValidateGitCommitLookupSeparatesOperations(t *testing.T) {
	request := validGitCommitRequest()
	applyLookup := lookupFromRequest(request)
	apply := commitFromRequest(request)
	apply.Replayed = true
	if err := ValidateGitCommitLookupBinding(applyLookup, apply); err != nil {
		t.Fatal(err)
	}

	revertLookup := applyLookup
	revertLookup.Operation = GitOperationRevert
	revertLookup.RevertsCommit = apply.GitCommit
	revertLookup.ResultHash = strings.Repeat("c", 64)
	revertLookup.DiffHash = strings.Repeat("d", 64)
	revertLookup.BaseBlobID, revertLookup.ResultBlobID = apply.ResultBlobID, apply.BaseBlobID
	reverse := apply
	reverse.Operation = GitOperationRevert
	reverse.GitCommit = strings.Repeat("5", 40)
	reverse.ParentGitCommit = apply.GitCommit
	reverse.RevertsCommit = apply.GitCommit
	reverse.ResultHash = revertLookup.ResultHash
	reverse.DiffHash = revertLookup.DiffHash
	reverse.BaseBlobID, reverse.ResultBlobID = revertLookup.BaseBlobID, revertLookup.ResultBlobID
	reverse.Replayed = false
	reverse.Recovered = true
	if err := ValidateGitCommitLookupBinding(revertLookup, reverse); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ValidateGitCommitLookupBinding(applyLookup, reverse), ErrGitConsistencyViolation) {
		t.Fatal("revert commit matched apply lookup")
	}
	missingOutcome := apply
	missingOutcome.Replayed = false
	if !errors.Is(ValidateGitCommitLookupBinding(applyLookup, missingOutcome), ErrGitConsistencyViolation) {
		t.Fatal("lookup result without replay/recovery marker accepted")
	}
}

func TestValidateReverseCommitBinding(t *testing.T) {
	forward := commitFromRequest(validGitCommitRequest())
	request := ReverseCommitRequest{Commit: forward, ExpectedBaseHash: strings.Repeat("e", 64)}
	reverse := forward
	reverse.Operation = GitOperationRevert
	reverse.GitCommit = strings.Repeat("5", 40)
	reverse.ParentGitCommit = forward.GitCommit
	reverse.ResultHash = request.ExpectedBaseHash
	reverse.DiffHash = strings.Repeat("6", 64)
	reverse.BaseBlobID, reverse.ResultBlobID = forward.ResultBlobID, forward.BaseBlobID
	reverse.RevertsCommit = forward.GitCommit
	if err := ValidateReverseCommitBinding(request, reverse); err != nil {
		t.Fatal(err)
	}

	changed := reverse
	changed.ParentGitCommit = testGitHead
	if !errors.Is(ValidateReverseCommitBinding(request, changed), ErrGitConsistencyViolation) {
		t.Fatal("reverse parent mismatch accepted")
	}
	changed = reverse
	changed.RevertsCommit = testGitHead
	if !errors.Is(ValidateReverseCommitBinding(request, changed), ErrGitConsistencyViolation) {
		t.Fatal("reverse reverts binding mismatch accepted")
	}
	changed = reverse
	changed.ResultHash = strings.Repeat("f", 64)
	if !errors.Is(ValidateReverseCommitBinding(request, changed), ErrGitConsistencyViolation) {
		t.Fatal("reverse base hash mismatch accepted")
	}
}

func TestGitObjectAndModeValidation(t *testing.T) {
	for _, oid := range []string{strings.Repeat("a", 40), strings.Repeat("B", 64)} {
		if !ValidGitObjectID(oid) {
			t.Fatalf("valid oid rejected: %q", oid)
		}
	}
	for _, oid := range []string{"", strings.Repeat("a", 39), strings.Repeat("g", 40), strings.Repeat("a", 65)} {
		if ValidGitObjectID(oid) {
			t.Fatalf("invalid oid accepted: %q", oid)
		}
	}
	for _, mode := range []string{GitFileModeRegular, GitFileModeExecutable} {
		if !ValidGitFileMode(mode) {
			t.Fatalf("valid mode rejected: %q", mode)
		}
	}
	for _, mode := range []string{"", "100664", "120000", "160000"} {
		if ValidGitFileMode(mode) {
			t.Fatalf("unsafe mode accepted: %q", mode)
		}
	}
}

type gitRepositoryStub struct{}

func (gitRepositoryStub) Inspect(context.Context, foundation.ID, string) (GitSnapshot, error) {
	return GitSnapshot{}, nil
}

func (gitRepositoryStub) DiffApproved(context.Context, GitDiffRequest) (GitDiff, error) {
	return GitDiff{}, nil
}

func (gitRepositoryStub) CommitApproved(context.Context, GitCommitRequest) (GitCommit, error) {
	return GitCommit{}, nil
}

func (gitRepositoryStub) FindWritebackCommit(context.Context, GitCommitLookup) (GitCommit, error) {
	return GitCommit{}, nil
}

func (gitRepositoryStub) CreateReverseCommit(context.Context, ReverseCommitRequest) (GitCommit, error) {
	return GitCommit{}, nil
}
