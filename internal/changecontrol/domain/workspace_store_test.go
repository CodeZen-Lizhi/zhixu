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
	return PreparedWrite{
		ExecutionID:        command.ExecutionID,
		TemporaryRef:       "notes/.zhixu-writeback-execution-random.tmp",
		ExpectedBaseHash:   command.ExpectedBaseHash,
		ApprovedChangeHash: command.ApprovedChangeHash,
		ResultHash:         ComputeWritebackResultHash(command.Content),
		ByteSize:           int64(len(command.Content)),
		Mode:               0o640,
		LockToken:          strings.Repeat("b", 64),
	}
}

func appliedFromPrepared(prepared PreparedWrite) AppliedWrite {
	return AppliedWrite{
		ExecutionID:        prepared.ExecutionID,
		TemporaryRef:       prepared.TemporaryRef,
		BackupRef:          "notes/.zhixu-writeback-execution-random.bak",
		BaseHash:           prepared.ExpectedBaseHash,
		ApprovedChangeHash: prepared.ApprovedChangeHash,
		ResultHash:         prepared.ResultHash,
		ByteSize:           prepared.ByteSize,
		Mode:               prepared.Mode,
		LockToken:          prepared.LockToken,
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
	for _, targetPath := range []string{"", "/tmp/a.md", "../a.md", "notes\\a.md", "notes/./a.md", "a.txt", ".knowledge/a.md", ".knowledge/writeback/a.md"} {
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
		{name: "temporary parent", mutate: func(value *PreparedWrite) { value.TemporaryRef = "other/.zhixu-writeback-execution-random.tmp" }},
		{name: "base hash", mutate: func(value *PreparedWrite) { value.ExpectedBaseHash = strings.Repeat("c", 64) }},
		{name: "change hash", mutate: func(value *PreparedWrite) { value.ApprovedChangeHash = strings.Repeat("d", 64) }},
		{name: "result hash", mutate: func(value *PreparedWrite) { value.ResultHash = strings.Repeat("e", 64) }},
		{name: "byte size", mutate: func(value *PreparedWrite) { value.ByteSize++ }},
		{name: "mode", mutate: func(value *PreparedWrite) { value.Mode = 0o10000 }},
		{name: "lock token", mutate: func(value *PreparedWrite) { value.LockToken = "token" }},
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
