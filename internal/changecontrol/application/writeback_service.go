package application

import (
	"context"
	"crypto/sha1" // #nosec G505 -- SHA-1 is required by the UUID v5 format, not used for security.
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
)

const (
	// SafeWritebackSchemaVersion 是固定 Workflow Node 输入输出的当前契约版本。
	SafeWritebackSchemaVersion = 1
	// WritebackIndexStatusPending 表示发布事务已创建重索引 Outbox，但 Retrieval 尚未消费。
	WritebackIndexStatusPending = "pending"
	maxWritebackResumeSteps     = 16
)

// writebackRepository 是 Application Saga 使用的最小持久化端口。
// 刻意不暴露旧 CreateWritebackExecution，避免绕过原子双授权 Begin。
type writebackRepository interface {
	GetProposal(context.Context, foundation.ID) (domain.Proposal, error)
	BeginWriteback(context.Context, domain.BeginWriteback) (domain.WritebackExecution, error)
	ValidateWritebackLease(context.Context, foundation.ID, string) error
	GetWritebackExecution(context.Context, foundation.ID) (domain.WritebackExecution, error)
	CheckpointWritebackExecution(context.Context, domain.CheckpointWriteback) (domain.WritebackExecution, error)
	PublishWriteback(context.Context, domain.PublishWriteback) (domain.PublishWritebackResult, error)
	FinalizeWritebackCleanup(context.Context, foundation.ID, int64, time.Time) (domain.WritebackExecution, error)
}

// WritebackServiceDependencies 是 Safe Writeback Saga 的完整依赖。
type WritebackServiceDependencies struct {
	Repository writebackRepository
	Workspace  domain.WorkspaceStore
	Git        domain.GitRepository
	Audit      WritebackAuditRecorder
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
}

// WritebackService 协调数据库、文件和 Git 的可恢复有序 Saga。
type WritebackService struct {
	repository writebackRepository
	workspace  domain.WorkspaceStore
	git        domain.GitRepository
	audit      WritebackAuditRecorder
	ids        foundation.IDGenerator
	clock      foundation.Clock
}

// NewWritebackService 创建 Safe Writeback Application Saga。
func NewWritebackService(dependencies WritebackServiceDependencies) (*WritebackService, error) {
	if dependencies.Repository == nil || dependencies.Workspace == nil || dependencies.Git == nil || dependencies.Audit == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITEBACK_DEPENDENCY_MISSING", false, errors.New("writeback dependency missing"))
	}
	return &WritebackService{
		repository: dependencies.Repository,
		workspace:  dependencies.Workspace,
		git:        dependencies.Git,
		audit:      dependencies.Audit,
		ids:        dependencies.IDs,
		clock:      dependencies.Clock,
	}, nil
}

// BeginWritebackCommand 只携带受信 Workflow 身份、两份瞬时 Credential 和幂等键。
// 正文、路径、Hash、Scope、Approval 和 Git HEAD 均由服务端持久化 Proposal 派生。
type BeginWritebackCommand struct {
	WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
	ProposalID                            foundation.ID
	LeaseOwner                            string
	IdempotencyKey                        string
	WriteCredential, GitCredential        string
	WriteAuthorizationKey                 string
	GitAuthorizationKey                   string
}

// BeginWritebackResult 返回 Durable Execution 身份；不包含 Credential、正文或路径。
type BeginWritebackResult struct {
	ExecutionID foundation.ID
	Status      domain.WritebackStatus
	Replayed    bool
}

// WritebackResult 是 Node/API 可安全返回的稳定写回摘要。
type WritebackResult struct {
	ExecutionID, WorkspaceID        foundation.ID
	WorkflowRunID, NodeRunID        foundation.ID
	ProposalID, RevisionID          foundation.ID
	Status                          domain.WritebackStatus
	GitCommit, ResultHash, DiffHash string
	IndexStatus                     string
	RecoveryRequired                bool
	CleanupPending                  bool
}

// Begin 在单一 PostgreSQL 事务中消费两份授权并创建或重放 Durable Execution。
func (s *WritebackService) Begin(ctx context.Context, command BeginWritebackCommand) (BeginWritebackResult, error) {
	if ctx == nil || command.WorkspaceID == "" || command.WorkflowRunID == "" || command.NodeRunID == "" || command.ProposalID == "" || strings.TrimSpace(command.LeaseOwner) == "" || strings.TrimSpace(command.IdempotencyKey) == "" || strings.TrimSpace(command.WriteCredential) == "" || strings.TrimSpace(command.GitCredential) == "" || strings.TrimSpace(command.WriteAuthorizationKey) == "" || strings.TrimSpace(command.GitAuthorizationKey) == "" {
		return BeginWritebackResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_BEGIN_INVALID", false, domain.ErrWritebackInvalidInput)
	}
	if len(strings.TrimSpace(command.IdempotencyKey)) > 128 || len(strings.TrimSpace(command.WriteAuthorizationKey)) > 128 || len(strings.TrimSpace(command.GitAuthorizationKey)) > 128 || len(command.WriteCredential) > domain.MaxAuthorizationCredentialBytes || len(command.GitCredential) > domain.MaxAuthorizationCredentialBytes || command.WriteAuthorizationKey == command.GitAuthorizationKey {
		return BeginWritebackResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_BEGIN_INVALID", false, domain.ErrWritebackInvalidInput)
	}
	proposal, err := s.repository.GetProposal(ctx, command.ProposalID)
	if err != nil {
		return BeginWritebackResult{}, err
	}
	if err := validateBeginProposal(command.WorkspaceID, proposal); err != nil {
		return BeginWritebackResult{}, err
	}
	executionID, err := s.ids.New()
	if err != nil {
		return BeginWritebackResult{}, err
	}
	consume := func(credential, key, tool string, capability domain.Capability) domain.AuthorizationConsume {
		return domain.AuthorizationConsume{
			Credential: credential, IdempotencyKey: strings.TrimSpace(key),
			WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID, NodeRunID: command.NodeRunID,
			ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID,
			ToolName: tool, Capability: capability, Scope: domain.ExpectedAuthorizationScope(proposal.TargetPath),
			ApprovedChangeHash: proposal.Revision.ChangeHash, TargetVersion: proposal.Revision.BaseHash,
		}
	}
	execution, err := s.repository.BeginWriteback(ctx, domain.BeginWriteback{
		ExecutionID: executionID, WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID, NodeRunID: command.NodeRunID,
		ProposalID: command.ProposalID, LeaseOwner: strings.TrimSpace(command.LeaseOwner), IdempotencyKey: strings.TrimSpace(command.IdempotencyKey),
		WriteAuthorization: consume(command.WriteCredential, command.WriteAuthorizationKey, "ApplyApprovedPatch", domain.CapabilityWriteKnowledge),
		GitAuthorization:   consume(command.GitCredential, command.GitAuthorizationKey, "CreateGitCommit", domain.CapabilityGitWrite),
	})
	if err != nil {
		return BeginWritebackResult{}, err
	}
	return BeginWritebackResult{ExecutionID: execution.ID, Status: execution.Status, Replayed: execution.ID != executionID}, nil
}

// Resume 从持久化 Execution 检查点继续，直到 verifying/index_pending、终态或需要稍后重试。
func (s *WritebackService) Resume(ctx context.Context, executionID foundation.ID, identity WritebackResumeIdentity) (WritebackResult, error) {
	if ctx == nil || executionID == "" || identity.Validate() != nil {
		return WritebackResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_RESUME_INVALID", false, domain.ErrWritebackInvalidInput)
	}
	identity.LeaseOwner = strings.TrimSpace(identity.LeaseOwner)
	// 历史 Execution 也必须先回读 Proposal 类型，才能接触任何文件、Git 或恢复检查点。
	initial, err := s.repository.GetWritebackExecution(ctx, executionID)
	if err != nil {
		return WritebackResult{}, err
	}
	proposal, err := s.repository.GetProposal(ctx, initial.ProposalID)
	if err != nil {
		return resultFromExecution(initial), err
	}
	if domain.NormalizeProposalType(proposal.Type) == domain.ProposalTypeDownstreamUpdate {
		return resultFromExecution(initial), domain.NewDownstreamUpdateApplyUnavailableError()
	}
	for range maxWritebackResumeSteps {
		execution, err := s.repository.GetWritebackExecution(ctx, executionID)
		if err != nil {
			return WritebackResult{}, err
		}
		if execution.ID != executionID || execution.WorkspaceID != identity.WorkspaceID || execution.WorkflowRunID != identity.WorkflowRunID || execution.NodeRunID != identity.NodeRunID {
			return resultFromExecution(execution), foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_RESUME_BINDING_INVALID", false, domain.ErrWritebackIdentityConflict)
		}
		switch execution.Status {
		case domain.WritebackStatusPrepared:
			if execution, err = s.resumePrepared(ctx, execution, identity); err != nil {
				return resultFromExecution(execution), err
			}
		case domain.WritebackStatusFilePrepared:
			if execution, err = s.resumeFilePrepared(ctx, execution, identity); err != nil {
				return resultFromExecution(execution), err
			}
		case domain.WritebackStatusFileApplied:
			if execution, err = s.resumeFileApplied(ctx, execution, identity); err != nil {
				return resultFromExecution(execution), err
			}
		case domain.WritebackStatusGitPrepared:
			if execution, err = s.resumeGitPrepared(ctx, execution, identity); err != nil {
				return resultFromExecution(execution), err
			}
		case domain.WritebackStatusCompensatingFile:
			if execution, err = s.resumeCompensatingFile(ctx, execution, identity.LeaseOwner); err != nil {
				return resultFromExecution(execution), err
			}
		case domain.WritebackStatusGitCommitted, domain.WritebackStatusPublishRecovery:
			if err = s.audit.RecordSucceeded(ctx, identity, execution, WritebackAuditGit); err != nil {
				return resultFromExecution(execution), err
			}
			if execution, err = s.resumePublish(ctx, execution, identity.LeaseOwner); err != nil {
				return resultFromExecution(execution), err
			}
		case domain.WritebackStatusVerifying:
			if err = s.audit.RecordSucceeded(ctx, identity, execution, WritebackAuditApply); err != nil {
				return resultFromExecution(execution), err
			}
			if err = s.audit.RecordSucceeded(ctx, identity, execution, WritebackAuditGit); err != nil {
				return resultFromExecution(execution), err
			}
			return s.resumeCleanup(ctx, execution, identity.LeaseOwner)
		case domain.WritebackStatusNeedsRevision:
			return resultFromExecution(execution), terminalWritebackError(execution, foundation.ErrorVersionConflict, "WRITEBACK_NEEDS_REVISION")
		case domain.WritebackStatusApplyFailed:
			return resultFromExecution(execution), terminalWritebackError(execution, foundation.ErrorNonRetryableFailure, "WRITEBACK_APPLY_FAILED")
		case domain.WritebackStatusCompensated:
			return resultFromExecution(execution), terminalWritebackError(execution, foundation.ErrorNonRetryableFailure, "WRITEBACK_COMPENSATED")
		case domain.WritebackStatusManualRecovery:
			return resultFromExecution(execution), terminalWritebackError(execution, foundation.ErrorManualRecoveryRequired, "WRITEBACK_MANUAL_RECOVERY_REQUIRED")
		default:
			return resultFromExecution(execution), foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_STATUS_NOT_RESUMABLE", false, domain.ErrWritebackInvalidTransition)
		}
	}
	return WritebackResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_RESUME_STEP_LIMIT", false, errors.New("writeback resume exceeded state transition limit"))
}

func (s *WritebackService) resumePrepared(ctx context.Context, execution domain.WritebackExecution, identity WritebackResumeIdentity) (result domain.WritebackExecution, retErr error) {
	leaseOwner := identity.LeaseOwner
	proposal, err := s.repository.GetProposal(ctx, execution.ProposalID)
	if err != nil {
		return execution, err
	}
	if domain.NormalizeProposalType(proposal.Type) == domain.ProposalTypeDownstreamUpdate {
		return execution, domain.NewDownstreamUpdateApplyUnavailableError()
	}
	if err := validateExecutionProposal(execution, proposal); err != nil {
		return s.checkpointFailure(ctx, execution, domain.WritebackStatusApplyFailed, "WRITEBACK_PROPOSAL_BINDING_INVALID", err, false)
	}
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	if _, err := s.git.Inspect(ctx, execution.WorkspaceID, execution.ApprovedGitHead); err != nil {
		return s.checkpointPreparedError(ctx, execution, err)
	}
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	lock, err := s.workspace.AcquireTarget(ctx, execution.WorkspaceID, execution.TargetPath)
	if err != nil {
		return s.checkpointPreparedError(ctx, execution, err)
	}
	defer joinTargetCloseError(lock, &retErr)
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	prepared, err := lock.Prepare(ctx, domain.PrepareWrite{
		ExecutionID: execution.ID, ExpectedBaseHash: execution.BaseHash,
		ApprovedChangeHash: execution.ApprovedChangeHash, Content: []byte(proposal.Revision.Content),
	})
	if err != nil {
		return s.checkpointPreparedError(ctx, execution, err)
	}
	execution, err = s.repository.CheckpointWritebackExecution(ctx, domain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: execution.Version, Status: domain.WritebackStatusFilePrepared,
		ResultHash: prepared.ResultHash, TemporaryRef: prepared.TemporaryRef, BackupRef: prepared.BackupRef,
		FileByteSize: prepared.ByteSize, FileMode: prepared.Mode, FileLockToken: prepared.LockToken,
		FileResultLockToken: prepared.ResultLockToken, FileBackupLockToken: prepared.BackupLockToken, At: s.clock.Now(),
	})
	if err != nil {
		return execution, err
	}
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	if err := s.audit.EnsureStarted(ctx, identity, execution, WritebackAuditApply); err != nil {
		return execution, err
	}
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	applied, err := lock.CommitCAS(ctx, prepared)
	if err != nil {
		return s.checkpointFileError(ctx, execution, err)
	}
	return s.checkpointFileApplied(ctx, execution, applied, identity)
}

func (s *WritebackService) resumeFilePrepared(ctx context.Context, execution domain.WritebackExecution, identity WritebackResumeIdentity) (result domain.WritebackExecution, retErr error) {
	leaseOwner := identity.LeaseOwner
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	lock, prepared, applied, err := s.workspace.ResumeTarget(ctx, execution.WorkspaceID, execution.TargetPath, domain.ResumeWrite{Prepared: preparedFromExecution(execution)})
	if err != nil {
		return s.checkpointFileError(ctx, execution, err)
	}
	defer joinTargetCloseError(lock, &retErr)
	if applied == nil {
		if err := s.audit.EnsureStarted(ctx, identity, execution, WritebackAuditApply); err != nil {
			return execution, err
		}
		if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
			return execution, err
		}
		value, commitErr := lock.CommitCAS(ctx, prepared)
		if commitErr != nil {
			return s.checkpointFileError(ctx, execution, commitErr)
		}
		applied = &value
	} else if err := s.audit.RequireStarted(ctx, identity, execution, WritebackAuditApply); err != nil {
		return execution, err
	}
	return s.checkpointFileApplied(ctx, execution, *applied, identity)
}

func (s *WritebackService) resumeFileApplied(ctx context.Context, execution domain.WritebackExecution, identity WritebackResumeIdentity) (result domain.WritebackExecution, retErr error) {
	leaseOwner := identity.LeaseOwner
	if err := s.audit.RecordSucceeded(ctx, identity, execution, WritebackAuditApply); err != nil {
		return execution, err
	}
	lock, _, applied, err := s.resumeAppliedTarget(ctx, execution, leaseOwner)
	if err != nil {
		if retryWithoutCheckpoint(err) {
			return execution, err
		}
		return s.checkpointManual(ctx, execution, "WRITEBACK_FILE_RECOVERY_INVALID", err)
	}
	defer joinTargetCloseError(lock, &retErr)
	if applied == nil {
		return s.checkpointManual(ctx, execution, "WRITEBACK_FILE_RECOVERY_INVALID", domain.ErrWritebackManualRecoveryRequired)
	}
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	diff, err := s.git.DiffApproved(ctx, domain.GitDiffRequest{
		WorkspaceID: execution.WorkspaceID, TargetPath: execution.TargetPath,
		ApprovedGitHead: execution.ApprovedGitHead, ResultHash: execution.ResultHash,
	})
	if err != nil {
		if retryWithoutCheckpoint(err) {
			return execution, err
		}
		if requiresManualRecovery(err) {
			return s.checkpointManual(ctx, execution, errorCode(err, "GIT_DIFF_RECOVERY_REQUIRED"), err)
		}
		return s.beginCompensation(ctx, execution, err)
	}
	return s.repository.CheckpointWritebackExecution(ctx, domain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: execution.Version, Status: domain.WritebackStatusGitPrepared,
		ResultHash: diff.ResultHash, DiffHash: diff.DiffHash, BaseBlobID: diff.BaseBlobID,
		ResultBlobID: diff.ResultBlobID, BaseMode: diff.BaseMode, At: s.clock.Now(),
	})
}

func (s *WritebackService) resumeGitPrepared(ctx context.Context, execution domain.WritebackExecution, identity WritebackResumeIdentity) (result domain.WritebackExecution, retErr error) {
	leaseOwner := identity.LeaseOwner
	lock, _, applied, err := s.resumeAppliedTarget(ctx, execution, leaseOwner)
	if err != nil {
		if retryWithoutCheckpoint(err) {
			return execution, err
		}
		return s.checkpointManual(ctx, execution, "WRITEBACK_FILE_RECOVERY_INVALID", err)
	}
	defer joinTargetCloseError(lock, &retErr)
	if applied == nil {
		return s.checkpointManual(ctx, execution, "WRITEBACK_FILE_RECOVERY_INVALID", domain.ErrWritebackManualRecoveryRequired)
	}
	lookup := gitLookupFromExecution(execution)
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	commit, err := s.git.FindWritebackCommit(ctx, lookup)
	if err != nil {
		if !errors.Is(err, domain.ErrGitNotFound) {
			if retryWithoutCheckpoint(err) {
				return execution, err
			}
			return s.checkpointManual(ctx, execution, errorCode(err, "GIT_COMMIT_LOOKUP_UNVERIFIED"), err)
		}
		if err := s.audit.EnsureStarted(ctx, identity, execution, WritebackAuditGit); err != nil {
			return execution, err
		}
		if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
			return execution, err
		}
		commit, err = s.git.CommitApproved(ctx, gitCommitRequestFromExecution(execution))
		if err != nil {
			if retryWithoutCheckpoint(err) {
				return execution, err
			}
			// 一旦进入 git_prepared，bounded lookup 的 NotFound 不能证明历史中
			// 从未发布过同一 Commit；任何 Commit 错误都不得再恢复文件。
			return s.checkpointManual(ctx, execution, errorCode(err, "GIT_COMMIT_RESULT_UNKNOWN"), err)
		}
	} else if err := s.audit.RequireStarted(ctx, identity, execution, WritebackAuditGit); err != nil {
		return execution, err
	}
	if commit.Recovered || commit.Replayed {
		if err := domain.ValidateGitCommitLookupBinding(lookup, commit); err != nil {
			return s.checkpointManual(ctx, execution, "GIT_COMMIT_BINDING_INVALID", err)
		}
	} else if err := domain.ValidateGitCommitBinding(gitCommitRequestFromExecution(execution), commit); err != nil {
		return s.checkpointManual(ctx, execution, "GIT_COMMIT_BINDING_INVALID", err)
	}
	checkpointed, err := s.repository.CheckpointWritebackExecution(ctx, domain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: execution.Version, Status: domain.WritebackStatusGitCommitted,
		ResultHash: commit.ResultHash, GitCommit: commit.GitCommit, ParentGitCommit: commit.ParentGitCommit,
		DiffHash: commit.DiffHash, BaseBlobID: commit.BaseBlobID, ResultBlobID: commit.ResultBlobID, BaseMode: commit.BaseMode,
		At: s.clock.Now(),
	})
	if err != nil {
		return execution, err
	}
	if err := s.audit.RecordSucceeded(ctx, identity, checkpointed, WritebackAuditGit); err != nil {
		return checkpointed, err
	}
	return checkpointed, nil
}

func (s *WritebackService) resumeCompensatingFile(ctx context.Context, execution domain.WritebackExecution, leaseOwner string) (result domain.WritebackExecution, retErr error) {
	lock, _, applied, err := s.resumeAppliedTarget(ctx, execution, leaseOwner)
	if err != nil {
		if retryWithoutCheckpoint(err) {
			return execution, err
		}
		return s.checkpointManual(ctx, execution, "WRITEBACK_COMPENSATION_RECOVERY_INVALID", err)
	}
	defer joinTargetCloseError(lock, &retErr)
	if applied == nil {
		return s.checkpointManual(ctx, execution, "WRITEBACK_COMPENSATION_RECOVERY_INVALID", domain.ErrWritebackManualRecoveryRequired)
	}
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	if _, err := lock.RestoreCAS(ctx, *applied); err != nil {
		if retryWithoutCheckpoint(err) {
			return execution, err
		}
		return s.checkpointManual(ctx, execution, errorCode(err, "WRITEBACK_COMPENSATION_FAILED"), err)
	}
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	if err := lock.Cleanup(ctx, *applied); err != nil {
		if retryWithoutCheckpoint(err) {
			return execution, err
		}
		return s.checkpointManual(ctx, execution, errorCode(err, "WRITEBACK_COMPENSATION_CLEANUP_FAILED"), err)
	}
	return s.repository.CheckpointWritebackExecution(ctx, domain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: execution.Version, Status: domain.WritebackStatusCompensated,
		FailureCode: execution.FailureCode, At: s.clock.Now(),
	})
}

func (s *WritebackService) resumePublish(ctx context.Context, execution domain.WritebackExecution, leaseOwner string) (domain.WritebackExecution, error) {
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return execution, err
	}
	command, err := buildPublishWriteback(execution, s.clock.Now())
	if err != nil {
		return execution, err
	}
	published, err := s.repository.PublishWriteback(ctx, command)
	if err == nil {
		return published.Execution, nil
	}
	current, readErr := s.repository.GetWritebackExecution(ctx, execution.ID)
	if readErr == nil && current.Status == domain.WritebackStatusVerifying {
		return current, nil
	}
	if retryWithoutCheckpoint(err) {
		return execution, err
	}
	if execution.Status == domain.WritebackStatusGitCommitted {
		return s.checkpointFailure(ctx, execution, domain.WritebackStatusPublishRecovery, errorCode(err, "WRITEBACK_PUBLISH_FAILED"), err, false)
	}
	return s.checkpointManual(ctx, execution, errorCode(err, "WRITEBACK_PUBLISH_RECOVERY_FAILED"), err)
}

func (s *WritebackService) resumeCleanup(ctx context.Context, execution domain.WritebackExecution, leaseOwner string) (result WritebackResult, retErr error) {
	if execution.CleanupCompletedAt != nil {
		return resultFromExecution(execution), nil
	}
	lock, _, applied, err := s.resumeAppliedTarget(ctx, execution, leaseOwner)
	if err != nil {
		return resultFromExecution(execution), err
	}
	defer joinTargetCloseError(lock, &retErr)
	if applied == nil {
		return resultFromExecution(execution), foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_CLEANUP_EVIDENCE_INVALID", false, domain.ErrWritebackManualRecoveryRequired)
	}
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return resultFromExecution(execution), err
	}
	if err := lock.Cleanup(ctx, *applied); err != nil {
		return resultFromExecution(execution), err
	}
	updated, err := s.repository.FinalizeWritebackCleanup(ctx, execution.ID, execution.Version, s.clock.Now())
	if err != nil {
		return resultFromExecution(execution), err
	}
	return resultFromExecution(updated), nil
}

func (s *WritebackService) resumeAppliedTarget(ctx context.Context, execution domain.WritebackExecution, leaseOwner string) (domain.TargetLock, domain.PreparedWrite, *domain.AppliedWrite, error) {
	if err := s.validateLease(ctx, execution, leaseOwner); err != nil {
		return nil, domain.PreparedWrite{}, nil, err
	}
	prepared := preparedFromExecution(execution)
	applied := appliedFromExecution(execution)
	return s.workspace.ResumeTarget(ctx, execution.WorkspaceID, execution.TargetPath, domain.ResumeWrite{
		Prepared:                prepared,
		Applied:                 &applied,
		CleanupMayHaveCompleted: execution.Status == domain.WritebackStatusVerifying,
		RestoreMayHaveCompleted: execution.Status == domain.WritebackStatusCompensatingFile,
	})
}

func (s *WritebackService) validateLease(ctx context.Context, execution domain.WritebackExecution, leaseOwner string) error {
	return s.repository.ValidateWritebackLease(ctx, execution.ID, leaseOwner)
}

func joinTargetCloseError(lock domain.TargetLock, target *error) {
	if closeErr := lock.Close(); closeErr != nil {
		*target = errors.Join(*target, closeErr)
	}
}

func (s *WritebackService) checkpointPreparedError(ctx context.Context, execution domain.WritebackExecution, err error) (domain.WritebackExecution, error) {
	switch {
	case retryWithoutCheckpoint(err):
		return execution, err
	case errors.Is(err, domain.ErrTargetBaseHashConflict), errors.Is(err, domain.ErrTargetIdentityConflict), errorKind(err) == foundation.ErrorVersionConflict:
		return s.checkpointFailure(ctx, execution, domain.WritebackStatusNeedsRevision, errorCode(err, "WRITEBACK_BASE_CONFLICT"), err, false)
	default:
		return s.checkpointFailure(ctx, execution, domain.WritebackStatusApplyFailed, errorCode(err, "WRITEBACK_PREPARE_FAILED"), err, false)
	}
}

func (s *WritebackService) checkpointFileError(ctx context.Context, execution domain.WritebackExecution, err error) (domain.WritebackExecution, error) {
	switch {
	case retryWithoutCheckpoint(err):
		return execution, err
	case errors.Is(err, domain.ErrTargetBaseHashConflict), errors.Is(err, domain.ErrTargetIdentityConflict):
		return s.checkpointFailure(ctx, execution, domain.WritebackStatusNeedsRevision, errorCode(err, "WRITEBACK_BASE_CONFLICT"), err, false)
	case requiresManualRecovery(err), errorKind(err) == foundation.ErrorConsistencyViolation:
		return s.checkpointManual(ctx, execution, errorCode(err, "WRITEBACK_FILE_RESULT_UNKNOWN"), err)
	default:
		return s.checkpointFailure(ctx, execution, domain.WritebackStatusApplyFailed, errorCode(err, "WRITEBACK_FILE_APPLY_FAILED"), err, false)
	}
}

func (s *WritebackService) checkpointFileApplied(ctx context.Context, execution domain.WritebackExecution, applied domain.AppliedWrite, identity WritebackResumeIdentity) (domain.WritebackExecution, error) {
	checkpointed, err := s.repository.CheckpointWritebackExecution(ctx, domain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: execution.Version, Status: domain.WritebackStatusFileApplied,
		ResultHash: applied.ResultHash, TemporaryRef: applied.TemporaryRef, BackupRef: applied.BackupRef,
		FileByteSize: applied.ByteSize, FileMode: applied.Mode, FileLockToken: applied.LockToken,
		FileResultLockToken: applied.ResultLockToken, FileBackupLockToken: applied.BackupLockToken, At: s.clock.Now(),
	})
	if err != nil {
		return execution, err
	}
	if err := s.audit.RecordSucceeded(ctx, identity, checkpointed, WritebackAuditApply); err != nil {
		return checkpointed, err
	}
	return checkpointed, nil
}

func (s *WritebackService) beginCompensation(ctx context.Context, execution domain.WritebackExecution, err error) (domain.WritebackExecution, error) {
	updated, checkpointErr := s.repository.CheckpointWritebackExecution(ctx, domain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: execution.Version, Status: domain.WritebackStatusCompensatingFile,
		FailureCode: errorCode(err, "WRITEBACK_GIT_PRECONDITION_FAILED"), At: s.clock.Now(),
	})
	if checkpointErr != nil {
		return execution, checkpointErr
	}
	return updated, nil
}

func (s *WritebackService) checkpointManual(ctx context.Context, execution domain.WritebackExecution, code string, cause error) (domain.WritebackExecution, error) {
	return s.checkpointFailure(ctx, execution, domain.WritebackStatusManualRecovery, code, cause, true)
}

func (s *WritebackService) checkpointFailure(ctx context.Context, execution domain.WritebackExecution, status domain.WritebackStatus, code string, cause error, manual bool) (domain.WritebackExecution, error) {
	if len(code) > 128 {
		code = code[:128]
	}
	updated, err := s.repository.CheckpointWritebackExecution(ctx, domain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: execution.Version, Status: status,
		FailureCode: code, ManualRecoveryRequired: manual, At: s.clock.Now(),
	})
	if err != nil {
		return execution, err
	}
	return updated, cause
}

func validateBeginProposal(workspaceID foundation.ID, proposal domain.Proposal) error {
	if domain.NormalizeProposalType(proposal.Type) == domain.ProposalTypeDownstreamUpdate {
		return domain.NewDownstreamUpdateApplyUnavailableError()
	}
	// Proposal 状态由 Atomic Begin 在同一事务内判定。Application 允许已进入
	// applying/verifying 的同一请求抵达 Repository，以便返回既有 Execution。
	if proposal.ID == "" || proposal.WorkspaceID != workspaceID || proposal.TargetPath != proposal.Revision.TargetPath || proposal.Approval == nil || proposal.Approval.Decision != domain.DecisionApproved || proposal.Approval.ApprovedGitHead == nil || !domain.ValidGitHead(*proposal.Approval.ApprovedGitHead) || proposal.Approval.RevisionID != proposal.Revision.ID || !strings.EqualFold(proposal.Approval.ChangeHash, proposal.Revision.ChangeHash) || !strings.EqualFold(proposal.Revision.ChangeHash, domain.ComputeChangeHash(proposal.TargetPath, proposal.Revision.BaseHash, proposal.Revision.Content)) {
		return foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_APPROVAL_REQUIRED", false, domain.ErrWritebackIdentityConflict)
	}
	return nil
}

func validateExecutionProposal(execution domain.WritebackExecution, proposal domain.Proposal) error {
	if domain.NormalizeProposalType(proposal.Type) == domain.ProposalTypeDownstreamUpdate {
		return domain.NewDownstreamUpdateApplyUnavailableError()
	}
	if proposal.ID != execution.ProposalID || proposal.WorkspaceID != execution.WorkspaceID || proposal.Status != domain.StatusApplying || proposal.Revision.ID != execution.RevisionID || proposal.Revision.TargetPath != execution.TargetPath || !strings.EqualFold(proposal.Revision.BaseHash, execution.BaseHash) || !strings.EqualFold(proposal.Revision.ChangeHash, execution.ApprovedChangeHash) || !strings.EqualFold(domain.ComputeWritebackResultHash([]byte(proposal.Revision.Content)), execution.ResultHash) || proposal.Approval == nil || proposal.Approval.ID != execution.ApprovalID || proposal.Approval.Decision != domain.DecisionApproved || proposal.Approval.ApprovedGitHead == nil || !strings.EqualFold(*proposal.Approval.ApprovedGitHead, execution.ApprovedGitHead) {
		return domain.ErrWritebackIdentityConflict
	}
	return nil
}

func preparedFromExecution(execution domain.WritebackExecution) domain.PreparedWrite {
	return domain.PreparedWrite{
		ExecutionID: execution.ID, TemporaryRef: execution.TemporaryRef, BackupRef: execution.BackupRef,
		ExpectedBaseHash: execution.BaseHash, ApprovedChangeHash: execution.ApprovedChangeHash,
		ResultHash: execution.ResultHash, ByteSize: execution.FileByteSize, Mode: execution.FileMode, LockToken: execution.FileLockToken,
		ResultLockToken: execution.FileResultLockToken, BackupLockToken: execution.FileBackupLockToken,
	}
}

func appliedFromExecution(execution domain.WritebackExecution) domain.AppliedWrite {
	return domain.AppliedWrite{
		ExecutionID: execution.ID, TemporaryRef: execution.TemporaryRef, BackupRef: execution.BackupRef,
		BaseHash: execution.BaseHash, ApprovedChangeHash: execution.ApprovedChangeHash,
		ResultHash: execution.ResultHash, ByteSize: execution.FileByteSize, Mode: execution.FileMode, LockToken: execution.FileLockToken,
		ResultLockToken: execution.FileResultLockToken, BackupLockToken: execution.FileBackupLockToken,
	}
}

func gitLookupFromExecution(execution domain.WritebackExecution) domain.GitCommitLookup {
	return domain.GitCommitLookup{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.WorkflowRunID, NodeRunID: execution.NodeRunID,
		WritebackExecutionID: execution.ID, ProposalID: execution.ProposalID, RevisionID: execution.RevisionID, ApprovalID: execution.ApprovalID,
		Operation: domain.GitOperationApply, TargetPath: execution.TargetPath, ApprovedGitHead: execution.ApprovedGitHead,
		ResultHash: execution.ResultHash, DiffHash: execution.DiffHash, BaseBlobID: execution.BaseBlobID,
		ResultBlobID: execution.ResultBlobID, BaseMode: execution.BaseMode,
	}
}

func gitCommitRequestFromExecution(execution domain.WritebackExecution) domain.GitCommitRequest {
	lookup := gitLookupFromExecution(execution)
	return domain.GitCommitRequest{
		WorkspaceID: lookup.WorkspaceID, WorkflowRunID: lookup.WorkflowRunID, NodeRunID: lookup.NodeRunID,
		WritebackExecutionID: lookup.WritebackExecutionID, ProposalID: lookup.ProposalID, RevisionID: lookup.RevisionID, ApprovalID: lookup.ApprovalID,
		Operation: lookup.Operation, TargetPath: lookup.TargetPath, ApprovedGitHead: lookup.ApprovedGitHead,
		ResultHash: lookup.ResultHash, DiffHash: lookup.DiffHash, BaseBlobID: lookup.BaseBlobID,
		ResultBlobID: lookup.ResultBlobID, BaseMode: lookup.BaseMode,
	}
}

func buildPublishWriteback(execution domain.WritebackExecution, at time.Time) (domain.PublishWriteback, error) {
	commitID, err := deterministicWritebackID(execution.ID, "proposal-commit")
	if err != nil {
		return domain.PublishWriteback{}, err
	}
	eventID, err := deterministicWritebackID(execution.ID, "reindex-outbox")
	if err != nil {
		return domain.PublishWriteback{}, err
	}
	payload, err := reindexcontract.EncodeCanonical(reindexcontract.RequestV1{
		SchemaVersion: reindexcontract.SchemaVersionV1, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.WorkflowRunID,
		NodeRunID: execution.NodeRunID, ProposalID: execution.ProposalID, RevisionID: execution.RevisionID,
		ApprovalID: execution.ApprovalID, WritebackExecutionID: execution.ID, TargetPath: execution.TargetPath,
		ResultHash: strings.ToLower(execution.ResultHash), GitCommit: strings.ToLower(execution.GitCommit),
	})
	if err != nil {
		return domain.PublishWriteback{}, foundation.NewError(foundation.ErrorNonRetryableFailure, "WRITEBACK_OUTBOX_ENCODING_FAILED", false, err)
	}
	return domain.PublishWriteback{
		ExecutionID: execution.ID, ExpectedVersion: execution.Version,
		Commit: domain.ProposalCommit{
			ID: commitID, WorkspaceID: execution.WorkspaceID, WritebackExecutionID: execution.ID,
			ProposalID: execution.ProposalID, RevisionID: execution.RevisionID, ApprovalID: execution.ApprovalID,
			GitCommit: execution.GitCommit, ParentGitCommit: execution.ParentGitCommit, TargetPath: execution.TargetPath,
			DiffHash: execution.DiffHash, ResultHash: execution.ResultHash, CreatedAt: at,
		},
		Event: domain.WritebackOutboxEvent{
			ID: eventID, WorkspaceID: execution.WorkspaceID, RunID: execution.WorkflowRunID,
			Type: reindexcontract.EventTypeReindexRequested, IdempotencyKey: domain.ExpectedWritebackReindexKey(execution),
			Payload: payload, OccurredAt: at,
		},
	}, nil
}

func deterministicWritebackID(executionID foundation.ID, label string) (foundation.ID, error) {
	parsed, err := foundation.ParseID(string(executionID))
	if err != nil || strings.TrimSpace(label) == "" {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_ID_DERIVATION_FAILED", false, errors.Join(err, domain.ErrWritebackInvalidInput))
	}
	namespace, err := hex.DecodeString(strings.ReplaceAll(string(parsed), "-", ""))
	if err != nil {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_ID_DERIVATION_FAILED", false, err)
	}
	sum := sha1.Sum(append(namespace, []byte(label)...)) // #nosec G401 -- UUID v5 requires SHA-1.
	raw := sum[:16]
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	return foundation.ParseID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]))
}

func retryWithoutCheckpoint(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Retryable || errors.Is(err, domain.ErrTargetLockUnavailable) || errors.Is(err, domain.ErrWritebackLeaseLost)
}

func requiresManualRecovery(err error) bool {
	kind := errorKind(err)
	return kind == foundation.ErrorManualRecoveryRequired || kind == foundation.ErrorConsistencyViolation || errors.Is(err, domain.ErrWritebackManualRecoveryRequired) || errors.Is(err, domain.ErrGitManualRecoveryRequired) || errors.Is(err, domain.ErrGitConsistencyViolation)
}

func errorKind(err error) foundation.ErrorKind {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Kind
	}
	return ""
}

func errorCode(err error, fallback string) string {
	var classified *foundation.Error
	if errors.As(err, &classified) && strings.TrimSpace(classified.Code) != "" {
		return classified.Code
	}
	return fallback
}

func terminalWritebackError(execution domain.WritebackExecution, kind foundation.ErrorKind, fallback string) error {
	code := strings.TrimSpace(execution.FailureCode)
	if code == "" {
		code = fallback
	}
	return foundation.NewError(kind, code, false, errors.New("writeback reached a terminal state"))
}

func resultFromExecution(execution domain.WritebackExecution) WritebackResult {
	indexStatus := ""
	if execution.Status == domain.WritebackStatusVerifying {
		indexStatus = WritebackIndexStatusPending
	}
	return WritebackResult{
		ExecutionID: execution.ID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.WorkflowRunID, NodeRunID: execution.NodeRunID,
		ProposalID: execution.ProposalID, RevisionID: execution.RevisionID,
		Status: execution.Status, GitCommit: execution.GitCommit, ResultHash: execution.ResultHash, DiffHash: execution.DiffHash,
		IndexStatus: indexStatus, RecoveryRequired: execution.ManualRecoveryRequired || execution.Status == domain.WritebackStatusManualRecovery || execution.Status == domain.WritebackStatusPublishRecovery,
		CleanupPending: execution.Status == domain.WritebackStatusVerifying && execution.CleanupCompletedAt == nil,
	}
}
