package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"strings"
	"time"
)

func validateBeginAuthorization(auth domain.ToolAuthorization, request domain.AuthorizationConsume) error {
	if err := domain.ValidateAuthorizationConsumeBinding(auth, request); err != nil {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_AUTHORIZATION_BINDING_CONFLICT", false, err)
	}
	if auth.Status != domain.AuthorizationIssued && auth.Status != domain.AuthorizationConsumed {
		return foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_AUTHORIZATION_NOT_USABLE", false, errors.New("authorization is not issued or consumed"))
	}
	return nil
}

func hashWritebackCredential(credential string) string {
	digest := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(digest[:])
}

const writebackColumns = `id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,proposal_id::text,revision_id::text,approval_id::text,write_authorization_id::text,git_authorization_id::text,target_path,target_mode,base_hash,result_hash,approved_change_hash,approved_git_head,git_commit,parent_git_commit,diff_hash,status,idempotency_key,failure_code,manual_recovery_required,temporary_ref,backup_ref,file_byte_size,file_mode,file_lock_token,file_result_lock_token,file_backup_lock_token,base_blob_id,result_blob_id,base_mode,version,created_at,updated_at,completed_at,cleanup_completed_at`

const proposalCommitColumns = `id::text,workspace_id::text,writeback_execution_id::text,proposal_id::text,revision_id::text,approval_id::text,git_commit,parent_git_commit,target_path,target_mode,diff_hash,result_hash,created_at`

func scanWritebackExecution(row interface{ Scan(...any) error }) (domain.WritebackExecution, error) {
	var execution domain.WritebackExecution
	var id, workspaceID, runID, nodeID, proposalID, revisionID, approvalID, writeAuthorizationID, gitAuthorizationID string
	var status string
	var gitCommit, parentGitCommit, diffHash, failureCode, temporaryRef, backupRef, fileLockToken, fileResultLockToken, fileBackupLockToken, baseBlobID, resultBlobID, baseMode *string
	var fileByteSize *int64
	var fileMode *int32
	err := row.Scan(
		&id, &workspaceID, &runID, &nodeID, &proposalID, &revisionID, &approvalID, &writeAuthorizationID, &gitAuthorizationID,
		&execution.TargetPath, &execution.TargetMode, &execution.BaseHash, &execution.ResultHash, &execution.ApprovedChangeHash, &execution.ApprovedGitHead,
		&gitCommit, &parentGitCommit, &diffHash, &status, &execution.IdempotencyKey, &failureCode, &execution.ManualRecoveryRequired,
		&temporaryRef, &backupRef, &fileByteSize, &fileMode, &fileLockToken, &fileResultLockToken, &fileBackupLockToken, &baseBlobID, &resultBlobID, &baseMode,
		&execution.Version, &execution.CreatedAt, &execution.UpdatedAt, &execution.CompletedAt, &execution.CleanupCompletedAt,
	)
	if err != nil {
		return domain.WritebackExecution{}, err
	}
	execution.ID, execution.WorkspaceID, execution.WorkflowRunID, execution.NodeRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(runID), foundation.ID(nodeID)
	execution.ProposalID, execution.RevisionID, execution.ApprovalID = foundation.ID(proposalID), foundation.ID(revisionID), foundation.ID(approvalID)
	execution.WriteAuthorizationID, execution.GitAuthorizationID = foundation.ID(writeAuthorizationID), foundation.ID(gitAuthorizationID)
	execution.Status = domain.WritebackStatus(status)
	execution.TargetMode = domain.NormalizeTargetMode(execution.TargetMode)
	execution.GitCommit, execution.ParentGitCommit, execution.DiffHash = stringValue(gitCommit), stringValue(parentGitCommit), stringValue(diffHash)
	execution.FailureCode, execution.TemporaryRef, execution.BackupRef = stringValue(failureCode), stringValue(temporaryRef), stringValue(backupRef)
	execution.FileLockToken = stringValue(fileLockToken)
	execution.FileResultLockToken, execution.FileBackupLockToken = stringValue(fileResultLockToken), stringValue(fileBackupLockToken)
	execution.BaseBlobID, execution.ResultBlobID, execution.BaseMode = stringValue(baseBlobID), stringValue(resultBlobID), stringValue(baseMode)
	if fileByteSize != nil {
		execution.FileByteSize = *fileByteSize
	}
	if fileMode != nil {
		execution.FileMode = uint32(*fileMode)
	}
	return execution, nil
}

func scanProposalCommit(row interface{ Scan(...any) error }) (domain.ProposalCommit, error) {
	var commit domain.ProposalCommit
	var id, workspaceID, executionID, proposalID, revisionID, approvalID string
	if err := row.Scan(&id, &workspaceID, &executionID, &proposalID, &revisionID, &approvalID, &commit.GitCommit, &commit.ParentGitCommit, &commit.TargetPath, &commit.TargetMode, &commit.DiffHash, &commit.ResultHash, &commit.CreatedAt); err != nil {
		return domain.ProposalCommit{}, err
	}
	commit.ID, commit.WorkspaceID, commit.WritebackExecutionID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(executionID)
	commit.ProposalID, commit.RevisionID, commit.ApprovalID = foundation.ID(proposalID), foundation.ID(revisionID), foundation.ID(approvalID)
	commit.TargetMode = domain.NormalizeTargetMode(commit.TargetMode)
	return commit, nil
}

func proposalStatusForCheckpoint(status domain.WritebackStatus) (domain.ProposalStatus, bool) {
	switch status {
	case domain.WritebackStatusGitCommitted:
		return domain.StatusApplied, true
	case domain.WritebackStatusNeedsRevision:
		return domain.StatusNeedsRevision, true
	case domain.WritebackStatusApplyFailed:
		return domain.StatusApplyFailed, true
	default:
		return "", false
	}
}

func executionFromCreate(command domain.CreateWriteback, databaseNow time.Time) domain.WritebackExecution {
	return domain.WritebackExecution{
		ID: command.ID, WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID, NodeRunID: command.NodeRunID,
		ProposalID: command.ProposalID, RevisionID: command.RevisionID, ApprovalID: command.ApprovalID,
		WriteAuthorizationID: command.WriteAuthorizationID, GitAuthorizationID: command.GitAuthorizationID,
		TargetPath: command.TargetPath, TargetMode: domain.NormalizeTargetMode(command.TargetMode), BaseHash: command.BaseHash, ResultHash: command.ResultHash,
		ApprovedChangeHash: command.ApprovedChangeHash, ApprovedGitHead: command.ApprovedGitHead,
		Status: domain.WritebackStatusPrepared, IdempotencyKey: command.IdempotencyKey,
		TemporaryRef: command.TemporaryRef, BackupRef: command.BackupRef, Version: 1,
		CreatedAt: databaseNow, UpdatedAt: databaseNow,
	}
}

func normalizeCreateWriteback(command domain.CreateWriteback) domain.CreateWriteback {
	command.BaseHash = strings.ToLower(command.BaseHash)
	command.ResultHash = strings.ToLower(command.ResultHash)
	command.ApprovedChangeHash = strings.ToLower(command.ApprovedChangeHash)
	command.ApprovedGitHead = strings.ToLower(command.ApprovedGitHead)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	return command
}

func normalizeCheckpointWriteback(current domain.WritebackExecution, command domain.CheckpointWriteback) domain.CheckpointWriteback {
	command.ResultHash = strings.ToLower(command.ResultHash)
	command.FileLockToken = strings.ToLower(command.FileLockToken)
	command.FileResultLockToken = strings.ToLower(command.FileResultLockToken)
	command.FileBackupLockToken = strings.ToLower(command.FileBackupLockToken)
	command.BaseBlobID = strings.ToLower(command.BaseBlobID)
	command.ResultBlobID = strings.ToLower(command.ResultBlobID)
	command.GitCommit = strings.ToLower(command.GitCommit)
	command.ParentGitCommit = strings.ToLower(command.ParentGitCommit)
	command.DiffHash = strings.ToLower(command.DiffHash)
	if command.ResultHash == "" {
		command.ResultHash = current.ResultHash
	}
	if command.TemporaryRef == "" {
		command.TemporaryRef = current.TemporaryRef
	}
	if command.BackupRef == "" {
		command.BackupRef = current.BackupRef
	}
	if command.FileByteSize == 0 {
		command.FileByteSize = current.FileByteSize
	}
	if command.FileMode == 0 {
		command.FileMode = current.FileMode
	}
	if command.FileLockToken == "" {
		command.FileLockToken = current.FileLockToken
	}
	if command.FileResultLockToken == "" {
		command.FileResultLockToken = current.FileResultLockToken
	}
	if command.FileBackupLockToken == "" {
		command.FileBackupLockToken = current.FileBackupLockToken
	}
	if command.BaseBlobID == "" {
		command.BaseBlobID = current.BaseBlobID
	}
	if command.ResultBlobID == "" {
		command.ResultBlobID = current.ResultBlobID
	}
	if command.BaseMode == "" {
		command.BaseMode = current.BaseMode
	}
	if command.GitCommit == "" {
		command.GitCommit = current.GitCommit
	}
	if command.ParentGitCommit == "" {
		command.ParentGitCommit = current.ParentGitCommit
	}
	if command.DiffHash == "" {
		command.DiffHash = current.DiffHash
	}
	if command.FailureCode == "" {
		command.FailureCode = current.FailureCode
	}
	command.ManualRecoveryRequired = command.ManualRecoveryRequired || current.ManualRecoveryRequired
	if command.CompletedAt == nil {
		command.CompletedAt = current.CompletedAt
	}
	return command
}

func normalizePublishWriteback(command domain.PublishWriteback) domain.PublishWriteback {
	command.Commit.GitCommit = strings.ToLower(command.Commit.GitCommit)
	command.Commit.ParentGitCommit = strings.ToLower(command.Commit.ParentGitCommit)
	command.Commit.DiffHash = strings.ToLower(command.Commit.DiffHash)
	command.Commit.ResultHash = strings.ToLower(command.Commit.ResultHash)
	command.Event.IdempotencyKey = strings.TrimSpace(command.Event.IdempotencyKey)
	return command
}

func sameProposalCommitIdentity(left, right domain.ProposalCommit) bool {
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID && left.WritebackExecutionID == right.WritebackExecutionID &&
		left.ProposalID == right.ProposalID && left.RevisionID == right.RevisionID && left.ApprovalID == right.ApprovalID &&
		strings.EqualFold(left.GitCommit, right.GitCommit) && strings.EqualFold(left.ParentGitCommit, right.ParentGitCommit) &&
		left.TargetPath == right.TargetPath && domain.NormalizeTargetMode(left.TargetMode) == domain.NormalizeTargetMode(right.TargetMode) && strings.EqualFold(left.DiffHash, right.DiffHash) && strings.EqualFold(left.ResultHash, right.ResultHash)
}

func sameJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
	}
	leftJSON, leftErr := json.Marshal(leftValue)
	rightJSON, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableUint32(value uint32) any {
	if value == 0 {
		return nil
	}
	return int32(value)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func writebackDomainError(err error) error {
	switch {
	case errors.Is(err, domain.ErrWritebackInvalidInput):
		return foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_INVALID", false, err)
	case errors.Is(err, domain.ErrWritebackIdentityConflict):
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_IDENTITY_CONFLICT", false, err)
	case errors.Is(err, domain.ErrWritebackVersionConflict):
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_VERSION_CONFLICT", false, err)
	case errors.Is(err, domain.ErrWritebackInvalidTransition):
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_STATUS_CONFLICT", false, err)
	case errors.Is(err, domain.ErrWritebackPublishBindingConflict):
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_PUBLISH_BINDING_CONFLICT", false, err)
	case errors.Is(err, domain.ErrWritebackLeaseLost):
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_LEASE_LOST", true, err)
	default:
		return foundation.NewError(foundation.ErrorNonRetryableFailure, "WRITEBACK_FAILED", false, err)
	}
}

func classifyWriteback(err error, code string) error {
	if err == nil {
		return nil
	}
	if sqlState := platformpostgres.SQLState(err); sqlState != "" {
		switch sqlState {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_IDENTITY_CONFLICT", false, err)
		case "23503", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "57P01", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
}
