package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const testSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validCreateWriteback() CreateWriteback {
	return CreateWriteback{
		ID: "execution", WorkspaceID: "workspace", WorkflowRunID: "run", NodeRunID: "node",
		ProposalID: "proposal", RevisionID: "revision", ApprovalID: "approval",
		WriteAuthorizationID: "write-auth", GitAuthorizationID: "git-auth", TargetPath: "notes/a.md",
		BaseHash: testSHA256, ResultHash: strings.Repeat("a", 64), ApprovedChangeHash: strings.Repeat("b", 64), ApprovedGitHead: strings.Repeat("c", 40),
		IdempotencyKey: "writeback-1", TemporaryRef: ".knowledge/tmp/execution", BackupRef: ".knowledge/backup/execution", CreatedAt: time.Now(),
	}
}

func executionFromCreate(command CreateWriteback, status WritebackStatus) WritebackExecution {
	return WritebackExecution{
		ID: command.ID, WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID, NodeRunID: command.NodeRunID,
		ProposalID: command.ProposalID, RevisionID: command.RevisionID, ApprovalID: command.ApprovalID,
		WriteAuthorizationID: command.WriteAuthorizationID, GitAuthorizationID: command.GitAuthorizationID,
		TargetPath: command.TargetPath, BaseHash: command.BaseHash, ResultHash: command.ResultHash, ApprovedChangeHash: command.ApprovedChangeHash,
		ApprovedGitHead: command.ApprovedGitHead, Status: status, IdempotencyKey: command.IdempotencyKey,
		GitCommit: strings.Repeat("d", 40), ParentGitCommit: strings.Repeat("e", 40), DiffHash: strings.Repeat("f", 64),
		TemporaryRef: command.TemporaryRef, BackupRef: command.BackupRef, Version: 1,
		CreatedAt: command.CreatedAt, UpdatedAt: command.CreatedAt,
	}
}

func TestValidateProposalTransition(t *testing.T) {
	valid := [][2]ProposalStatus{
		{StatusReady, StatusApproved}, {StatusReady, StatusRejected}, {StatusReady, StatusNeedsRevision},
		{StatusApproved, StatusApplying}, {StatusApproved, StatusNeedsRevision},
		{StatusApplying, StatusApplied}, {StatusApplying, StatusApplyFailed}, {StatusApplying, StatusNeedsRevision},
		{StatusApplied, StatusVerifying},
		{StatusVerifying, StatusCompleted}, {StatusVerifying, StatusVerifyFailed}, {StatusVerifying, StatusRolledBack},
		{StatusVerifyFailed, StatusVerifying}, {StatusVerifyFailed, StatusRolledBack},
		{StatusApplyFailed, StatusApplying}, {StatusApplyFailed, StatusRolledBack},
		{StatusNeedsRevision, StatusDraft}, {StatusNeedsRevision, StatusCancelled},
	}
	for _, pair := range valid {
		if err := ValidateProposalTransition(pair[0], pair[1]); err != nil {
			t.Fatalf("expected proposal transition %s -> %s: %v", pair[0], pair[1], err)
		}
	}
	for _, pair := range [][2]ProposalStatus{{StatusApproved, StatusCompleted}, {StatusReady, StatusApplying}, {StatusCompleted, StatusVerifying}, {StatusApproved, StatusApproved}} {
		if err := ValidateProposalTransition(pair[0], pair[1]); err == nil {
			t.Fatalf("invalid proposal transition accepted: %s -> %s", pair[0], pair[1])
		}
	}
}

func TestValidateWritebackCreate(t *testing.T) {
	command := validCreateWriteback()
	if err := ValidateWritebackCreate(command); err != nil {
		t.Fatal(err)
	}
	invalid := command
	invalid.TargetPath = "/absolute.md"
	if !errors.Is(ValidateWritebackCreate(invalid), ErrWritebackInvalidInput) {
		t.Fatal("absolute target path accepted")
	}
	invalid = command
	invalid.ApprovedGitHead = "not-a-git-head"
	if !errors.Is(ValidateWritebackCreate(invalid), ErrWritebackInvalidInput) {
		t.Fatal("invalid approved git head accepted")
	}
	invalid = command
	invalid.TemporaryRef = "../outside"
	if !errors.Is(ValidateWritebackCreate(invalid), ErrWritebackInvalidInput) {
		t.Fatal("unsafe temporary ref accepted")
	}
}

func TestValidateWritebackTransition(t *testing.T) {
	valid := [][2]WritebackStatus{
		{WritebackStatusPrepared, WritebackStatusFileApplied},
		{WritebackStatusPrepared, WritebackStatusNeedsRevision},
		{WritebackStatusPrepared, WritebackStatusApplyFailed},
		{WritebackStatusFileApplied, WritebackStatusCompensatingFile},
		{WritebackStatusFileApplied, WritebackStatusGitCommitted},
		{WritebackStatusCompensatingFile, WritebackStatusCompensated},
		{WritebackStatusCompensatingFile, WritebackStatusManualRecovery},
		{WritebackStatusGitCommitted, WritebackStatusVerifying},
		{WritebackStatusGitCommitted, WritebackStatusPublishRecovery},
		{WritebackStatusPublishRecovery, WritebackStatusVerifying},
		{WritebackStatusPublishRecovery, WritebackStatusManualRecovery},
		{WritebackStatusVerifying, WritebackStatusCompleted},
		{WritebackStatusVerifying, WritebackStatusVerifyFailed},
		{WritebackStatusVerifying, WritebackStatusRolledBack},
		{WritebackStatusVerifyFailed, WritebackStatusVerifying},
		{WritebackStatusVerifyFailed, WritebackStatusRolledBack},
	}
	for _, pair := range valid {
		if err := ValidateWritebackTransition(pair[0], pair[1]); err != nil {
			t.Fatalf("expected writeback transition %s -> %s: %v", pair[0], pair[1], err)
		}
	}
	for _, pair := range [][2]WritebackStatus{{WritebackStatusPrepared, WritebackStatusPrepared}, {WritebackStatusFileApplied, WritebackStatusCompleted}, {WritebackStatusCompleted, WritebackStatusVerifying}, {WritebackStatusNeedsRevision, WritebackStatusPrepared}} {
		if err := ValidateWritebackTransition(pair[0], pair[1]); err == nil {
			t.Fatalf("invalid writeback transition accepted: %s -> %s", pair[0], pair[1])
		}
	}
}

func TestValidateWritebackIdentityAndCheckpoint(t *testing.T) {
	command := validCreateWriteback()
	current := executionFromCreate(command, WritebackStatusPrepared)
	if err := ValidateWritebackIdentity(current, current); err != nil {
		t.Fatal(err)
	}
	changed := current
	changed.GitAuthorizationID = "other"
	if !errors.Is(ValidateWritebackIdentity(current, changed), ErrWritebackIdentityConflict) {
		t.Fatal("authorization identity mismatch accepted")
	}
	checkpoint := CheckpointWriteback{ExecutionID: current.ID, ExpectedVersion: 1, Status: WritebackStatusFileApplied, ResultHash: current.ResultHash, At: time.Now()}
	if err := ValidateWritebackCheckpoint(current, checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint.ExpectedVersion = 2
	if !errors.Is(ValidateWritebackCheckpoint(current, checkpoint), ErrWritebackVersionConflict) {
		t.Fatal("stale checkpoint version accepted")
	}
	checkpoint = CheckpointWriteback{ExecutionID: current.ID, ExpectedVersion: 1, Status: WritebackStatusApplyFailed}
	if !errors.Is(ValidateWritebackCheckpoint(current, checkpoint), ErrWritebackInvalidInput) {
		t.Fatal("failed checkpoint without failure code accepted")
	}
	checkpoint = CheckpointWriteback{ExecutionID: current.ID, ExpectedVersion: 1, Status: WritebackStatusFileApplied, ResultHash: strings.Repeat("9", 64)}
	if !errors.Is(ValidateWritebackCheckpoint(current, checkpoint), ErrWritebackIdentityConflict) {
		t.Fatal("checkpoint changed immutable result hash")
	}
}

func TestValidateWritebackPublishBinding(t *testing.T) {
	command := validCreateWriteback()
	current := executionFromCreate(command, WritebackStatusGitCommitted)
	commit := ProposalCommit{
		ID: "mapping", WorkspaceID: current.WorkspaceID, WritebackExecutionID: current.ID,
		ProposalID: current.ProposalID, RevisionID: current.RevisionID, ApprovalID: current.ApprovalID,
		GitCommit: strings.Repeat("d", 40), ParentGitCommit: strings.Repeat("e", 40), TargetPath: current.TargetPath,
		DiffHash: strings.Repeat("f", 64), ResultHash: current.ResultHash,
	}
	event := WritebackOutboxEvent{ID: "event", WorkspaceID: current.WorkspaceID, RunID: current.WorkflowRunID, Type: "retrieval.revision.reindex_requested", IdempotencyKey: ExpectedWritebackReindexKey(current), Payload: json.RawMessage(`{"schema_version":1,"workspace_id":"workspace","workflow_run_id":"run","node_run_id":"node","proposal_id":"proposal","revision_id":"revision","approval_id":"approval","writeback_execution_id":"execution","target_path":"notes/a.md","result_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","git_commit":"dddddddddddddddddddddddddddddddddddddddd"}`)}
	if err := ValidateWritebackPublish(current, PublishWriteback{ExecutionID: current.ID, ExpectedVersion: current.Version, Commit: commit, Event: event}); err != nil {
		t.Fatal(err)
	}
	invalidEvent := event
	invalidEvent.Payload = json.RawMessage(strings.Replace(string(event.Payload), `"schema_version":1`, `"schema_version":2`, 1))
	if !errors.Is(ValidateWritebackPublish(current, PublishWriteback{ExecutionID: current.ID, ExpectedVersion: current.Version, Commit: commit, Event: invalidEvent}), ErrWritebackPublishBindingConflict) {
		t.Fatal("unsupported outbox schema version accepted")
	}
	commit.ProposalID = "other"
	if !errors.Is(ValidateProposalCommitBinding(current, commit), ErrWritebackPublishBindingConflict) {
		t.Fatal("commit mapping with another proposal accepted")
	}
}
