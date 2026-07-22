package domain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func validPrepareWrite(targetPath string) PrepareWrite {
	content := []byte("# updated\n")
	baseHash := strings.Repeat("a", 64)
	return PrepareWrite{
		ExecutionID:        "execution",
		ExpectedBaseHash:   baseHash,
		ApprovedChangeHash: ComputeChangeHash(targetPath, baseHash, string(content)),
		Content:            content,
	}
}

func preparedFromCommand(command PrepareWrite) PreparedWrite {
	lockToken := strings.Repeat("b", 64)
	prefix, err := WritebackLocatorPrefix(command.ExecutionID, lockToken)
	if err != nil {
		panic(err)
	}
	return PreparedWrite{
		ExecutionID:        command.ExecutionID,
		TemporaryRef:       "notes/" + prefix + strings.Repeat("1", 32) + ".tmp",
		BackupRef:          "notes/" + prefix + strings.Repeat("2", 32) + ".bak",
		ExpectedBaseHash:   command.ExpectedBaseHash,
		ApprovedChangeHash: command.ApprovedChangeHash,
		ResultHash:         ComputeWritebackResultHash(command.Content),
		ByteSize:           int64(len(command.Content)),
		Mode:               0o640,
		LockToken:          lockToken,
		ResultLockToken:    strings.Repeat("c", 64),
		BackupLockToken:    strings.Repeat("d", 64),
	}
}

func appliedFromPrepared(prepared PreparedWrite) AppliedWrite {
	return AppliedWrite{
		ExecutionID:        prepared.ExecutionID,
		TemporaryRef:       prepared.TemporaryRef,
		BackupRef:          prepared.BackupRef,
		BaseHash:           prepared.ExpectedBaseHash,
		ApprovedChangeHash: prepared.ApprovedChangeHash,
		ResultHash:         prepared.ResultHash,
		ByteSize:           prepared.ByteSize,
		Mode:               prepared.Mode,
		LockToken:          prepared.LockToken,
		ResultLockToken:    prepared.ResultLockToken,
		BackupLockToken:    prepared.BackupLockToken,
	}
}

func TestWorkspaceStoreContractsCompile(t *testing.T) {
	var _ WorkspaceStore = workspaceStoreStub{}
	var _ TargetLock = targetLockStub{}
	var _ ContentValidator = contentValidatorStub{}
}

func TestValidateWorkspaceTarget(t *testing.T) {
	for _, targetPath := range []string{"a.md", "notes/guide.markdown", "notes/UPPER.MD"} {
		if err := ValidateWorkspaceTarget("workspace", targetPath); err != nil {
			t.Fatalf("valid target %q rejected: %v", targetPath, err)
		}
	}
	for _, targetPath := range []string{"", "/tmp/a.md", "../a.md", "notes\\a.md", "notes/./a.md", "a.txt", ".git/README.md", ".knowledge/a.md", ".knowledge/writeback/a.md"} {
		if !errors.Is(ValidateWorkspaceTarget("workspace", targetPath), ErrWritebackInvalidInput) {
			t.Fatalf("invalid target %q accepted", targetPath)
		}
	}
	if !errors.Is(ValidateWorkspaceTarget("", "a.md"), ErrWritebackInvalidInput) {
		t.Fatal("empty workspace accepted")
	}
}

func TestValidatePrepareWrite(t *testing.T) {
	targetPath := "notes/a.md"
	command := validPrepareWrite(targetPath)
	wantResultHash := ComputeWritebackResultHash(command.Content)
	resultHash, err := ValidatePrepareWrite(targetPath, command)
	if err != nil || resultHash != wantResultHash {
		t.Fatalf("valid prepare rejected: hash=%q err=%v", resultHash, err)
	}

	tests := []struct {
		name    string
		mutate  func(*PrepareWrite)
		wantErr error
	}{
		{name: "execution", mutate: func(value *PrepareWrite) { value.ExecutionID = "" }, wantErr: ErrWritebackInvalidInput},
		{name: "base hash", mutate: func(value *PrepareWrite) { value.ExpectedBaseHash = "invalid" }, wantErr: ErrWritebackInvalidInput},
		{name: "change hash", mutate: func(value *PrepareWrite) { value.ApprovedChangeHash = strings.Repeat("f", 64) }, wantErr: ErrWritebackIdentityConflict},
		{name: "empty content", mutate: func(value *PrepareWrite) { value.Content = nil }, wantErr: ErrWritebackInvalidInput},
		{name: "oversized content", mutate: func(value *PrepareWrite) { value.Content = make([]byte, MaxWritebackContentBytes+1) }, wantErr: ErrWritebackInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := command
			invalid.Content = append([]byte(nil), command.Content...)
			test.mutate(&invalid)
			if _, err := ValidatePrepareWrite(targetPath, invalid); !errors.Is(err, test.wantErr) {
				t.Fatalf("got %v, want %v", err, test.wantErr)
			}
		})
	}
	if _, err := ValidatePrepareWrite("notes/./a.md", command); !errors.Is(err, ErrWritebackInvalidInput) {
		t.Fatalf("non-canonical target accepted: %v", err)
	}
}

func TestValidatePreparedWriteBinding(t *testing.T) {
	targetPath := "notes/a.md"
	command := validPrepareWrite(targetPath)
	prepared := preparedFromCommand(command)
	if err := ValidatePreparedWriteBinding(targetPath, command, prepared); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*PreparedWrite)
	}{
		{name: "execution", mutate: func(value *PreparedWrite) { value.ExecutionID = "other" }},
		{name: "temporary locator", mutate: func(value *PreparedWrite) { value.TemporaryRef = "../outside.tmp" }},
		{name: "backup locator", mutate: func(value *PreparedWrite) { value.BackupRef = "notes/.zhixu-writeback-other.bak" }},
		{name: "same locator", mutate: func(value *PreparedWrite) { value.BackupRef = value.TemporaryRef }},
		{name: "temporary parent", mutate: func(value *PreparedWrite) { value.TemporaryRef = "other/.zhixu-writeback-execution-random.tmp" }},
		{name: "base hash", mutate: func(value *PreparedWrite) { value.ExpectedBaseHash = strings.Repeat("c", 64) }},
		{name: "change hash", mutate: func(value *PreparedWrite) { value.ApprovedChangeHash = strings.Repeat("d", 64) }},
		{name: "result hash", mutate: func(value *PreparedWrite) { value.ResultHash = strings.Repeat("e", 64) }},
		{name: "byte size", mutate: func(value *PreparedWrite) { value.ByteSize++ }},
		{name: "mode", mutate: func(value *PreparedWrite) { value.Mode = 0o10000 }},
		{name: "lock token", mutate: func(value *PreparedWrite) { value.LockToken = "token" }},
		{name: "result lock token", mutate: func(value *PreparedWrite) { value.ResultLockToken = "token" }},
		{name: "backup lock token", mutate: func(value *PreparedWrite) { value.BackupLockToken = "token" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := prepared
			test.mutate(&invalid)
			if err := ValidatePreparedWriteBinding(targetPath, command, invalid); !errors.Is(err, ErrWritebackIdentityConflict) {
				t.Fatalf("got %v, want identity conflict", err)
			}
		})
	}
}

func TestValidateAppliedWriteBinding(t *testing.T) {
	targetPath := "notes/a.md"
	command := validPrepareWrite(targetPath)
	prepared := preparedFromCommand(command)
	applied := appliedFromPrepared(prepared)
	if err := ValidateAppliedWriteBinding(prepared, applied); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*AppliedWrite)
	}{
		{name: "execution", mutate: func(value *AppliedWrite) { value.ExecutionID = "other" }},
		{name: "temporary locator", mutate: func(value *AppliedWrite) { value.TemporaryRef = "notes/.zhixu-writeback-other.tmp" }},
		{name: "backup locator", mutate: func(value *AppliedWrite) { value.BackupRef = "notes/backup.bak" }},
		{name: "backup parent", mutate: func(value *AppliedWrite) { value.BackupRef = "other/.zhixu-writeback-execution-random.bak" }},
		{name: "base hash", mutate: func(value *AppliedWrite) { value.BaseHash = strings.Repeat("c", 64) }},
		{name: "change hash", mutate: func(value *AppliedWrite) { value.ApprovedChangeHash = strings.Repeat("d", 64) }},
		{name: "result hash", mutate: func(value *AppliedWrite) { value.ResultHash = strings.Repeat("e", 64) }},
		{name: "byte size", mutate: func(value *AppliedWrite) { value.ByteSize++ }},
		{name: "mode", mutate: func(value *AppliedWrite) { value.Mode = 0o600 }},
		{name: "lock token", mutate: func(value *AppliedWrite) { value.LockToken = strings.Repeat("f", 64) }},
		{name: "result lock token", mutate: func(value *AppliedWrite) { value.ResultLockToken = strings.Repeat("f", 64) }},
		{name: "backup lock token", mutate: func(value *AppliedWrite) { value.BackupLockToken = strings.Repeat("f", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := applied
			test.mutate(&invalid)
			if err := ValidateAppliedWriteBinding(prepared, invalid); !errors.Is(err, ErrWritebackIdentityConflict) {
				t.Fatalf("got %v, want identity conflict", err)
			}
		})
	}
}

func TestValidateResumeWrite(t *testing.T) {
	targetPath := "notes/a.md"
	prepared := preparedFromCommand(validPrepareWrite(targetPath))
	applied := appliedFromPrepared(prepared)
	for _, resume := range []ResumeWrite{
		{Prepared: prepared},
		{Prepared: prepared, Applied: &applied},
		{Prepared: prepared, Applied: &applied, CleanupMayHaveCompleted: true},
		{Prepared: prepared, Applied: &applied, RestoreMayHaveCompleted: true},
	} {
		if err := ValidateResumeWrite(targetPath, resume); err != nil {
			t.Fatalf("valid resume rejected: %+v: %v", resume, err)
		}
	}
	invalid := applied
	invalid.BackupRef = "notes/.zhixu-writeback-other.bak"
	if !errors.Is(ValidateResumeWrite(targetPath, ResumeWrite{Prepared: prepared, Applied: &invalid}), ErrWritebackIdentityConflict) {
		t.Fatal("mismatched applied resume accepted")
	}
	if !errors.Is(ValidateResumeWrite("other/a.md", ResumeWrite{Prepared: prepared}), ErrWritebackIdentityConflict) {
		t.Fatal("wrong target parent accepted")
	}
	if !errors.Is(ValidateResumeWrite(targetPath, ResumeWrite{Prepared: prepared, CleanupMayHaveCompleted: true}), ErrWritebackInvalidInput) {
		t.Fatal("cleanup replay without applied binding accepted")
	}
	if !errors.Is(ValidateResumeWrite(targetPath, ResumeWrite{Prepared: prepared, RestoreMayHaveCompleted: true}), ErrWritebackInvalidInput) {
		t.Fatal("restore replay without applied binding accepted")
	}
	if !errors.Is(ValidateResumeWrite(targetPath, ResumeWrite{Prepared: prepared, Applied: &applied, CleanupMayHaveCompleted: true, RestoreMayHaveCompleted: true}), ErrWritebackInvalidInput) {
		t.Fatal("ambiguous cleanup and restore replay accepted")
	}
}

func TestValidateRestoreResult(t *testing.T) {
	for _, result := range []RestoreResult{{Restored: true}, {Replayed: true}} {
		if err := ValidateRestoreResult(result); err != nil {
			t.Fatalf("valid restore result rejected: %+v: %v", result, err)
		}
	}
	for _, result := range []RestoreResult{{}, {Restored: true, Replayed: true}} {
		if !errors.Is(ValidateRestoreResult(result), ErrWritebackInvalidInput) {
			t.Fatalf("invalid restore result accepted: %+v", result)
		}
	}
}

type workspaceStoreStub struct{}

func (workspaceStoreStub) AcquireTarget(context.Context, foundation.ID, string) (TargetLock, error) {
	return targetLockStub{}, nil
}

func (workspaceStoreStub) ResumeTarget(context.Context, foundation.ID, string, ResumeWrite) (TargetLock, PreparedWrite, *AppliedWrite, error) {
	return targetLockStub{}, PreparedWrite{}, nil, nil
}

type targetLockStub struct{}

func (targetLockStub) Prepare(context.Context, PrepareWrite) (PreparedWrite, error) {
	return PreparedWrite{}, nil
}

func (targetLockStub) CommitCAS(context.Context, PreparedWrite) (AppliedWrite, error) {
	return AppliedWrite{}, nil
}

func (targetLockStub) RestoreCAS(context.Context, AppliedWrite) (RestoreResult, error) {
	return RestoreResult{}, nil
}

func (targetLockStub) Cleanup(context.Context, AppliedWrite) error { return nil }
func (targetLockStub) Close() error                                { return nil }

type contentValidatorStub struct{}

func (contentValidatorStub) Validate(context.Context, []byte) error { return nil }
