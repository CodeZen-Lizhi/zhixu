package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
)

const testSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validCreateWriteback() CreateWriteback {
	return CreateWriteback{
		ID: "10000000-0000-4000-8000-000000000001", WorkspaceID: "20000000-0000-4000-8000-000000000001", WorkflowRunID: "30000000-0000-4000-8000-000000000001", NodeRunID: "40000000-0000-4000-8000-000000000001",
		ProposalID: "50000000-0000-4000-8000-000000000001", RevisionID: "60000000-0000-4000-8000-000000000001", ApprovalID: "70000000-0000-4000-8000-000000000001",
		WriteAuthorizationID: "80000000-0000-4000-8000-000000000001", GitAuthorizationID: "90000000-0000-4000-8000-000000000001", TargetPath: "notes/a.md",
		BaseHash: testSHA256, ResultHash: strings.Repeat("a", 64), ApprovedChangeHash: strings.Repeat("b", 64), ApprovedGitHead: strings.Repeat("c", 40),
		IdempotencyKey: "writeback-1", TemporaryRef: ".knowledge/tmp/execution", BackupRef: ".knowledge/backup/execution", CreatedAt: time.Now(),
	}
}

func executionFromCreate(command CreateWriteback, status WritebackStatus) WritebackExecution {
	return WritebackExecution{
		ID: command.ID, WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID, NodeRunID: command.NodeRunID,
		ProposalID: command.ProposalID, RevisionID: command.RevisionID, ApprovalID: command.ApprovalID,
		WriteAuthorizationID: command.WriteAuthorizationID, GitAuthorizationID: command.GitAuthorizationID,
		TargetPath: command.TargetPath, TargetMode: command.TargetMode, BaseHash: command.BaseHash, ResultHash: command.ResultHash, ApprovedChangeHash: command.ApprovedChangeHash,
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

func TestCreateOnlyWritebackUsesBoundAbsenceAndNoGitBaseBlob(t *testing.T) {
	command := validCreateWriteback()
	command.TargetMode = TargetModeCreateOnly
	command.BackupRef = ""
	token, err := ComputeAbsenceToken(command.WorkspaceID, command.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	command.BaseHash = token
	if err := ValidateWritebackCreate(command); err != nil {
		t.Fatal(err)
	}
	command.BaseHash = ComputeWritebackResultHash(nil)
	if !errors.Is(ValidateWritebackCreate(command), ErrWritebackInvalidInput) {
		t.Fatal("CREATE_ONLY accepted an empty-file hash as its base version")
	}

	command.BaseHash = token
	current := executionFromCreate(command, WritebackStatusFileApplied)
	current.TargetMode = TargetModeCreateOnly
	current.BackupRef = ""
	checkpoint := CheckpointWriteback{
		ExecutionID: current.ID, ExpectedVersion: current.Version, Status: WritebackStatusGitPrepared,
		ResultHash: current.ResultHash, DiffHash: strings.Repeat("f", 64), BaseBlobID: "",
		ResultBlobID: strings.Repeat("a", 40), BaseMode: GitFileModeRegular,
	}
	if err := ValidateWritebackCheckpoint(current, checkpoint); err != nil {
		t.Fatalf("CREATE_ONLY git intent with no base blob = %v", err)
	}
	checkpoint.BaseBlobID = token
	if !errors.Is(ValidateWritebackCheckpoint(current, checkpoint), ErrWritebackInvalidInput) {
		t.Fatal("CREATE_ONLY accepted absence token as a Git blob")
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

func TestIsWritebackCancellationSafe(t *testing.T) {
	tests := []struct {
		name             string
		status           WritebackStatus
		cleanupCompleted bool
		want             bool
	}{
		{name: "atomic begin", status: WritebackStatusPrepared},
		{name: "file intent", status: WritebackStatusFilePrepared},
		{name: "file applied", status: WritebackStatusFileApplied},
		{name: "git intent", status: WritebackStatusGitPrepared},
		{name: "compensating", status: WritebackStatusCompensatingFile},
		{name: "git committed before publish", status: WritebackStatusGitCommitted},
		{name: "publish recovery", status: WritebackStatusPublishRecovery},
		{name: "verifying before cleanup", status: WritebackStatusVerifying},
		{name: "verifying after cleanup", status: WritebackStatusVerifying, cleanupCompleted: true, want: true},
		{name: "needs revision", status: WritebackStatusNeedsRevision, want: true},
		{name: "apply failed", status: WritebackStatusApplyFailed, want: true},
		{name: "compensated", status: WritebackStatusCompensated, want: true},
		{name: "manual recovery", status: WritebackStatusManualRecovery, want: true},
		{name: "verify failed", status: WritebackStatusVerifyFailed, want: true},
		{name: "rolled back", status: WritebackStatusRolledBack, want: true},
		{name: "completed", status: WritebackStatusCompleted, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsWritebackCancellationSafe(test.status, test.cleanupCompleted); got != test.want {
				t.Fatalf("IsWritebackCancellationSafe(%q, %t) = %t, want %t", test.status, test.cleanupCompleted, got, test.want)
			}
		})
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
	payload, err := reindexcontract.EncodeCanonical(reindexcontract.RequestV1{
		SchemaVersion: reindexcontract.SchemaVersionV1, WorkspaceID: current.WorkspaceID, WorkflowRunID: current.WorkflowRunID,
		NodeRunID: current.NodeRunID, ProposalID: current.ProposalID, RevisionID: current.RevisionID,
		ApprovalID: current.ApprovalID, WritebackExecutionID: current.ID, TargetPath: current.TargetPath,
		ResultHash: current.ResultHash, GitCommit: current.GitCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := WritebackOutboxEvent{ID: "event", WorkspaceID: current.WorkspaceID, RunID: current.WorkflowRunID, Type: reindexcontract.EventTypeReindexRequested, IdempotencyKey: ExpectedWritebackReindexKey(current), Payload: payload}
	if err := ValidateWritebackPublish(current, PublishWriteback{ExecutionID: current.ID, ExpectedVersion: current.Version, Commit: commit, Event: event}); err != nil {
		t.Fatal(err)
	}
	invalidEvent := event
	invalidEvent.Payload = []byte(strings.Replace(string(event.Payload), `"schema_version":1`, `"schema_version":2`, 1))
	if !errors.Is(ValidateWritebackPublish(current, PublishWriteback{ExecutionID: current.ID, ExpectedVersion: current.Version, Commit: commit, Event: invalidEvent}), ErrWritebackPublishBindingConflict) {
		t.Fatal("unsupported outbox schema version accepted")
	}
	duplicateEvent := event
	duplicateEvent.Payload = append([]byte(`{"schema_version":1,`), event.Payload[1:]...)
	if !errors.Is(ValidateWritebackPublish(current, PublishWriteback{ExecutionID: current.ID, ExpectedVersion: current.Version, Commit: commit, Event: duplicateEvent}), ErrWritebackPublishBindingConflict) {
		t.Fatal("duplicate outbox field accepted")
	}
	nonCanonicalEvent := event
	nonCanonicalEvent.Payload = []byte(strings.Replace(string(event.Payload), `"target_path":"notes/a.md"`, `"target_path":"notes/../a.md"`, 1))
	if !errors.Is(ValidateWritebackPublish(current, PublishWriteback{ExecutionID: current.ID, ExpectedVersion: current.Version, Commit: commit, Event: nonCanonicalEvent}), ErrWritebackPublishBindingConflict) {
		t.Fatal("noncanonical outbox path did not preserve binding conflict")
	}
	commit.ProposalID = "other"
	if !errors.Is(ValidateProposalCommitBinding(current, commit), ErrWritebackPublishBindingConflict) {
		t.Fatal("commit mapping with another proposal accepted")
	}
}

func TestValidateBeginWritebackDoesNotRequireExecutionID(t *testing.T) {
	command := BeginWriteback{
		WorkspaceID: "workspace", WorkflowRunID: "run", NodeRunID: "node", ProposalID: "proposal",
		LeaseOwner: "worker-a", IdempotencyKey: "begin-1",
		WriteAuthorization: AuthorizationConsume{Credential: "write-token", IdempotencyKey: "write-key", ToolName: "ApplyApprovedPatch", Capability: CapabilityWriteKnowledge},
		GitAuthorization:   AuthorizationConsume{Credential: "git-token", IdempotencyKey: "git-key", ToolName: "CreateGitCommit", Capability: CapabilityGitWrite},
	}
	if err := ValidateBeginWriteback(command); err != nil {
		t.Fatal(err)
	}
	command.GitAuthorization.IdempotencyKey = command.WriteAuthorization.IdempotencyKey
	if !errors.Is(ValidateBeginWriteback(command), ErrWritebackInvalidInput) {
		t.Fatal("duplicate authorization idempotency keys accepted")
	}
	command.GitAuthorization.IdempotencyKey = "git-key"
	command.WriteAuthorization.Credential = strings.Repeat("x", MaxAuthorizationCredentialBytes+1)
	if !errors.Is(ValidateBeginWriteback(command), ErrWritebackInvalidInput) {
		t.Fatal("oversized authorization credential accepted")
	}
}

func TestValidatePreparedAndGitIntentFields(t *testing.T) {
	current := executionFromCreate(validCreateWriteback(), WritebackStatusPrepared)
	prepared := CheckpointWriteback{ExecutionID: current.ID, ExpectedVersion: current.Version, Status: WritebackStatusFilePrepared, ResultHash: current.ResultHash, TemporaryRef: "tmp/e", BackupRef: "backup/e", FileByteSize: 10, FileMode: 0o644, FileLockToken: strings.Repeat("1", 64), FileResultLockToken: strings.Repeat("5", 64), FileBackupLockToken: strings.Repeat("6", 64)}
	if err := ValidateWritebackCheckpoint(current, prepared); err != nil {
		t.Fatal(err)
	}
	prepared.FileLockToken = ""
	if !errors.Is(ValidateWritebackCheckpoint(current, prepared), ErrWritebackInvalidInput) {
		t.Fatal("file_prepared without lock token accepted")
	}
	fileApplied := current
	fileApplied.Status = WritebackStatusFileApplied
	gitPrepared := CheckpointWriteback{ExecutionID: current.ID, ExpectedVersion: current.Version, Status: WritebackStatusGitPrepared, ResultHash: current.ResultHash, DiffHash: strings.Repeat("2", 64), BaseBlobID: strings.Repeat("3", 40), ResultBlobID: strings.Repeat("4", 40), BaseMode: GitFileModeRegular}
	if err := ValidateWritebackCheckpoint(fileApplied, gitPrepared); err != nil {
		t.Fatal(err)
	}
}
