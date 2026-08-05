package application

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitoperation"
)

const (
	defaultAttemptLease        = 2 * time.Minute
	gitOperationReleaseTimeout = 5 * time.Second
)

// RunExecutor 恢复并推进一个持久 Git SyncRun，不把 Git 副作用放入数据库事务。
type RunExecutor struct {
	configs ConfigStore
	runs    RunStore
	remote  RemoteRepository
	locker  gitoperation.WorkspaceLocker
	ids     foundation.IDGenerator
	lease   time.Duration
}

// NewRunExecutor 创建可由 durable outbox 重放的运行执行器。
func NewRunExecutor(configs ConfigStore, runs RunStore, remote RemoteRepository, locker gitoperation.WorkspaceLocker, ids foundation.IDGenerator, lease time.Duration) (*RunExecutor, error) {
	if nilInterface(configs) || nilInterface(runs) || nilInterface(remote) || nilInterface(locker) || nilInterface(ids) {
		return nil, unavailable("Git sync executor dependency is unavailable")
	}
	if lease == 0 {
		lease = defaultAttemptLease
	}
	if lease < time.Second || lease > 10*time.Minute {
		return nil, invalid(domain.ErrorCodeInvalid, "Git sync attempt lease is invalid")
	}
	return &RunExecutor{configs: configs, runs: runs, remote: remote, locker: locker, ids: ids, lease: lease}, nil
}

// Execute 推进一个运行；业务失败会持久化为终态并返回 nil error。
func (executor *RunExecutor) Execute(ctx context.Context, workspaceID, runID foundation.ID, owner string) (result domain.SyncRun, returnErr error) {
	if executor == nil || nilInterface(executor.configs) || nilInterface(executor.runs) || nilInterface(executor.remote) ||
		nilInterface(executor.locker) || !validID(workspaceID) || !validID(runID) || !validText(owner, 256) {
		return domain.SyncRun{}, invalid(domain.ErrorCodeInvalid, "Git sync execution request is invalid")
	}
	if err := ctx.Err(); err != nil {
		return domain.SyncRun{}, err
	}
	attemptID, err := executor.ids.New()
	if err != nil {
		return domain.SyncRun{}, err
	}
	begin, err := executor.runs.BeginAttempt(ctx, BeginAttemptCommand{
		WorkspaceID: workspaceID, RunID: runID, AttemptID: attemptID, Owner: owner, LeaseDuration: executor.lease,
	})
	if err != nil {
		if interrupted := interruptedError(ctx, err); interrupted != nil {
			return domain.SyncRun{}, interrupted
		}
		return domain.SyncRun{}, err
	}
	if !begin.Started {
		return begin.Run, nil
	}
	operationLease, err := executor.locker.Acquire(ctx, workspaceID)
	if err != nil {
		return domain.SyncRun{}, err
	}
	if operationLease == nil {
		return domain.SyncRun{}, unavailable("Git operation locker returned an invalid lease")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), gitOperationReleaseTimeout)
		defer cancel()
		returnErr = errors.Join(returnErr, operationLease.Release(releaseCtx))
	}()
	run, attempt := begin.Run, begin.Attempt
	token, err := executor.configs.OpenCredential(ctx, workspaceID, run.ConfigRevision)
	if err != nil {
		if interrupted := interruptedError(ctx, err); interrupted != nil {
			return run, interrupted
		}
		return executor.finishError(ctx, run, attempt, owner, err, nil)
	}
	defer token.Destroy()
	access := GitAccess{
		WorkspaceID: workspaceID, ConfigRevision: run.ConfigRevision,
		RemoteURL: run.RemoteURL, Branch: run.Branch, Token: token,
	}
	switch run.Status {
	case domain.RunFetching:
		if err := executor.remote.Fetch(ctx, access); err != nil {
			if interrupted := interruptedError(ctx, err); interrupted != nil {
				return run, interrupted
			}
			return executor.finishError(ctx, run, attempt, owner, err, nil)
		}
		run, attempt, err = executor.runs.TransitionRun(ctx, TransitionRunCommand{
			WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
			ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version,
			ExpectedStatus: domain.RunFetching, NextStatus: domain.RunComparing,
			Direction: domain.DirectionUnknown, LeaseDuration: executor.lease,
		})
		if err != nil {
			return domain.SyncRun{}, err
		}
	case domain.RunComparing:
		// Compare 只读，可在 Worker 重启后安全重放。
	case domain.RunFastForwarding, domain.RunPushing, domain.RunVerifying:
		return executor.reconcileMutation(ctx, access, run, attempt, owner)
	default:
		return executor.finishError(ctx, run, attempt, owner, corrupt("Git sync run cannot resume from its persisted phase"), nil)
	}
	return executor.compareAndAdvance(ctx, access, run, attempt, owner)
}

func (executor *RunExecutor) compareAndAdvance(ctx context.Context, access GitAccess, run domain.SyncRun, attempt domain.SyncAttempt, owner string) (domain.SyncRun, error) {
	comparison, err := executor.remote.Compare(ctx, access)
	if err != nil {
		if interrupted := interruptedError(ctx, err); interrupted != nil {
			return run, interrupted
		}
		return executor.finishError(ctx, run, attempt, owner, err, nil)
	}
	if err := comparison.Validate(); err != nil {
		return executor.finishError(ctx, run, attempt, owner, corrupt("Git adapter returned an invalid comparison"), nil)
	}
	if !comparison.Attached {
		return executor.finishConflict(ctx, run, attempt, owner, comparison, domain.FailureDetached, domain.ErrorCodeRunConflict)
	}
	if comparison.Branch != run.Branch {
		return executor.finishConflict(ctx, run, attempt, owner, comparison, domain.FailureRefDrift, domain.ErrorCodeRefDrift)
	}
	if !comparison.WorktreeClean {
		return executor.finishConflict(ctx, run, attempt, owner, comparison, domain.FailureDirty, domain.ErrorCodeRunConflict)
	}
	switch comparison.Relation {
	case domain.RelationSame:
		known := true
		return executor.runs.CompleteRun(ctx, CompleteRunCommand{
			WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
			ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version, ExpectedStatus: domain.RunComparing,
			Status: domain.RunSucceeded, Direction: domain.DirectionNone,
			ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID, ChangedFiles: comparison.ChangedFiles,
			FailureClass: domain.FailureNone, VerifiedHeadOID: comparison.HeadOID, VerifiedRemoteOID: comparison.RemoteOID,
			IndexStatus: domain.IndexNotRequired, ResultKnown: &known,
		})
	case domain.RelationDiverged:
		return executor.finishConflict(ctx, run, attempt, owner, comparison, domain.FailureDiverged, domain.ErrorCodeRunConflict)
	case domain.RelationRemoteAhead:
		return executor.fastForward(ctx, access, run, attempt, owner, comparison)
	case domain.RelationLocalAhead:
		return executor.push(ctx, access, run, attempt, owner, comparison)
	default:
		return executor.finishError(ctx, run, attempt, owner, corrupt("Git comparison relation is invalid"), nil)
	}
}

func (executor *RunExecutor) reconcileMutation(ctx context.Context, access GitAccess, run domain.SyncRun, attempt domain.SyncAttempt, owner string) (domain.SyncRun, error) {
	if run.ExpectedHeadOID == "" || run.ExpectedRemoteOID == "" ||
		(run.Direction != domain.DirectionPull && run.Direction != domain.DirectionPush) {
		return executor.finishError(ctx, run, attempt, owner, corrupt("resumable Git mutation lacks frozen refs"), nil)
	}
	target := run.ExpectedHeadOID
	indexStatus := domain.IndexNotRequired
	if run.Direction == domain.DirectionPull {
		target = run.ExpectedRemoteOID
		indexStatus = domain.IndexPending
	}
	verification, err := executor.remote.Verify(ctx, VerifyCommand{
		Access: access, ExpectedHeadOID: target, ExpectedRemoteOID: target,
	})
	if interrupted := interruptedError(ctx, err); interrupted != nil {
		return run, interrupted
	}
	if err == nil && verification.Validate() == nil && verification.HeadOID == target &&
		verification.RemoteOID == target && verification.WorktreeClean {
		return executor.runs.CompleteRun(ctx, CompleteRunCommand{
			WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
			ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version, ExpectedStatus: run.Status,
			Status: domain.RunSucceeded, Direction: run.Direction,
			ExpectedHeadOID: run.ExpectedHeadOID, ExpectedRemoteOID: run.ExpectedRemoteOID, ChangedFiles: run.ChangedFiles,
			FailureClass: domain.FailureNone, VerifiedHeadOID: verification.HeadOID, VerifiedRemoteOID: verification.RemoteOID,
			IndexStatus: indexStatus, ResultKnown: boolPointer(true),
		})
	}
	if retryableVerificationError(err, attempt.ResultKnown) {
		return run, err
	}
	unknown := foundation.NewError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeResultUnknown, false, errors.New("restarted Git mutation could not be proven by refs"))
	if err != nil {
		unknown = foundation.NewError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeResultUnknown, false, err)
	}
	known := false
	return executor.finishError(ctx, run, attempt, owner, unknown, &known)
}

func (executor *RunExecutor) fastForward(ctx context.Context, access GitAccess, run domain.SyncRun, attempt domain.SyncAttempt, owner string, comparison domain.Comparison) (domain.SyncRun, error) {
	run, attempt, err := executor.runs.TransitionRun(ctx, TransitionRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version,
		ExpectedStatus: domain.RunComparing, NextStatus: domain.RunFastForwarding, Direction: domain.DirectionPull,
		ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID,
		ChangedFiles: comparison.ChangedFiles, LeaseDuration: executor.lease,
	})
	if err != nil {
		return domain.SyncRun{}, err
	}
	mutation, mutationErr := executor.remote.FastForward(ctx, FastForwardCommand{
		Access: access, ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID,
	})
	if interrupted := interruptedError(ctx, mutationErr); interrupted != nil {
		return run, interrupted
	}
	known := mutation.ResultKnown
	run, attempt, err = executor.runs.TransitionRun(ctx, TransitionRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version,
		ExpectedStatus: domain.RunFastForwarding, NextStatus: domain.RunVerifying, Direction: domain.DirectionPull,
		ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID,
		ChangedFiles: comparison.ChangedFiles, ResultKnown: &known, LeaseDuration: executor.lease,
	})
	if err != nil {
		return domain.SyncRun{}, err
	}
	if err := ctx.Err(); err != nil {
		return run, err
	}
	verification, verifyErr := executor.remote.Verify(ctx, VerifyCommand{
		Access: access, ExpectedHeadOID: comparison.RemoteOID, ExpectedRemoteOID: comparison.RemoteOID,
	})
	if interrupted := interruptedError(ctx, verifyErr); interrupted != nil {
		return run, interrupted
	}
	if verifyErr == nil && verification.Validate() == nil && verification.HeadOID == comparison.RemoteOID &&
		verification.RemoteOID == comparison.RemoteOID && verification.WorktreeClean {
		return executor.runs.CompleteRun(ctx, CompleteRunCommand{
			WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
			ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version, ExpectedStatus: domain.RunVerifying,
			Status: domain.RunSucceeded, Direction: domain.DirectionPull,
			ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID, ChangedFiles: comparison.ChangedFiles,
			FailureClass: domain.FailureNone, VerifiedHeadOID: verification.HeadOID, VerifiedRemoteOID: verification.RemoteOID,
			IndexStatus: domain.IndexPending, ResultKnown: boolPointer(true),
		})
	}
	if mutationErr == nil && retryableVerificationError(verifyErr, &known) {
		return run, verifyErr
	}
	return executor.finishMutationFailure(ctx, run, attempt, owner, mutationErr, verifyErr, known)
}

func (executor *RunExecutor) push(ctx context.Context, access GitAccess, run domain.SyncRun, attempt domain.SyncAttempt, owner string, comparison domain.Comparison) (domain.SyncRun, error) {
	run, attempt, err := executor.runs.TransitionRun(ctx, TransitionRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version,
		ExpectedStatus: domain.RunComparing, NextStatus: domain.RunPushing, Direction: domain.DirectionPush,
		ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID,
		ChangedFiles: comparison.ChangedFiles, LeaseDuration: executor.lease,
	})
	if err != nil {
		return domain.SyncRun{}, err
	}
	mutation, mutationErr := executor.remote.Push(ctx, PushCommand{
		Access: access, ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID,
	})
	if interrupted := interruptedError(ctx, mutationErr); interrupted != nil {
		return run, interrupted
	}
	known := mutation.ResultKnown
	run, attempt, err = executor.runs.TransitionRun(ctx, TransitionRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version,
		ExpectedStatus: domain.RunPushing, NextStatus: domain.RunVerifying, Direction: domain.DirectionPush,
		ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID,
		ChangedFiles: comparison.ChangedFiles, ResultKnown: &known, LeaseDuration: executor.lease,
	})
	if err != nil {
		return domain.SyncRun{}, err
	}
	if err := ctx.Err(); err != nil {
		return run, err
	}
	verification, verifyErr := executor.remote.Verify(ctx, VerifyCommand{
		Access: access, ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.HeadOID,
	})
	if interrupted := interruptedError(ctx, verifyErr); interrupted != nil {
		return run, interrupted
	}
	if verifyErr == nil && verification.Validate() == nil && verification.HeadOID == comparison.HeadOID &&
		verification.RemoteOID == comparison.HeadOID && verification.WorktreeClean {
		return executor.runs.CompleteRun(ctx, CompleteRunCommand{
			WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
			ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version, ExpectedStatus: domain.RunVerifying,
			Status: domain.RunSucceeded, Direction: domain.DirectionPush,
			ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID, ChangedFiles: comparison.ChangedFiles,
			FailureClass: domain.FailureNone, VerifiedHeadOID: verification.HeadOID, VerifiedRemoteOID: verification.RemoteOID,
			IndexStatus: domain.IndexNotRequired, ResultKnown: boolPointer(true),
		})
	}
	if mutationErr == nil && retryableVerificationError(verifyErr, &known) {
		return run, verifyErr
	}
	return executor.finishMutationFailure(ctx, run, attempt, owner, mutationErr, verifyErr, known)
}

func retryableVerificationError(err error, resultKnown *bool) bool {
	if err == nil || resultKnown == nil || !*resultKnown {
		return false
	}
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Retryable
}

func (executor *RunExecutor) finishConflict(ctx context.Context, run domain.SyncRun, attempt domain.SyncAttempt, owner string, comparison domain.Comparison, class domain.FailureClass, code string) (domain.SyncRun, error) {
	known := true
	return executor.runs.CompleteRun(ctx, CompleteRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version, ExpectedStatus: run.Status,
		Status: domain.RunConflict, Direction: domain.DirectionNone,
		ExpectedHeadOID: comparison.HeadOID, ExpectedRemoteOID: comparison.RemoteOID, ChangedFiles: comparison.ChangedFiles,
		FailureClass: class, ErrorCode: code, IndexStatus: domain.IndexNotRequired, ResultKnown: &known,
	})
}

func (executor *RunExecutor) finishMutationFailure(ctx context.Context, run domain.SyncRun, attempt domain.SyncAttempt, owner string, mutationErr, verifyErr error, resultKnown bool) (domain.SyncRun, error) {
	selected := mutationErr
	if selected == nil {
		selected = verifyErr
	}
	if selected == nil {
		selected = foundation.NewError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeResultUnknown, false, errors.New("Git post-check did not prove the requested refs"))
	}
	if !resultKnown {
		selected = foundation.NewError(foundation.ErrorManualRecoveryRequired, domain.ErrorCodeResultUnknown, false, selected)
	}
	return executor.finishError(ctx, run, attempt, owner, selected, &resultKnown)
}

func (executor *RunExecutor) finishError(ctx context.Context, run domain.SyncRun, attempt domain.SyncAttempt, owner string, cause error, resultKnown *bool) (domain.SyncRun, error) {
	status, class, code, retryable := classifyFailure(cause, run.Status)
	return executor.runs.CompleteRun(ctx, CompleteRunCommand{
		WorkspaceID: run.WorkspaceID, RunID: run.ID, AttemptID: attempt.ID, Owner: owner,
		ExpectedRunVersion: run.Version, ExpectedAttemptVersion: attempt.Version, ExpectedStatus: run.Status,
		Status: status, Direction: run.Direction,
		ExpectedHeadOID: run.ExpectedHeadOID, ExpectedRemoteOID: run.ExpectedRemoteOID, ChangedFiles: run.ChangedFiles,
		FailureClass: class, ErrorCode: code, Retryable: retryable,
		IndexStatus: domain.IndexNotRequired, ResultKnown: resultKnown,
	})
}

func classifyFailure(err error, phase domain.RunStatus) (domain.RunStatus, domain.FailureClass, string, bool) {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return domain.RunFailed, domain.FailureInternal, domain.ErrorCodeUnavailable, false
	}
	switch classified.Code {
	case domain.ErrorCodeConfigStale, domain.ErrorCodeConfigRevisionConflict:
		return domain.RunStale, domain.FailureStaleConfig, domain.ErrorCodeConfigStale, false
	case domain.ErrorCodeAuthenticationFailed:
		return domain.RunFailed, domain.FailureAuthentication, classified.Code, false
	case domain.ErrorCodeOffline:
		return domain.RunFailed, domain.FailureOffline, classified.Code, true
	case domain.ErrorCodeRefDrift:
		return domain.RunConflict, domain.FailureRefDrift, classified.Code, false
	case domain.ErrorCodeNonFastForward:
		return domain.RunConflict, domain.FailureNonFastForward, classified.Code, false
	case domain.ErrorCodeResultUnknown:
		if classified.Kind == foundation.ErrorManualRecoveryRequired || mutationPhase(phase) {
			return domain.RunManualRecoveryRequired, domain.FailureResultUnknown, classified.Code, false
		}
		return domain.RunFailed, domain.FailureInternal, domain.ErrorCodeCorrupt, false
	default:
		if classified.Kind == foundation.ErrorManualRecoveryRequired {
			return domain.RunManualRecoveryRequired, domain.FailureResultUnknown, domain.ErrorCodeResultUnknown, false
		}
		if classified.Kind == foundation.ErrorConsistencyViolation {
			if phase == domain.RunFastForwarding || phase == domain.RunPushing || phase == domain.RunVerifying {
				return domain.RunManualRecoveryRequired, domain.FailureResultUnknown, domain.ErrorCodeResultUnknown, false
			}
			code := classified.Code
			if !validText(code, 128) {
				code = domain.ErrorCodeCorrupt
			}
			return domain.RunFailed, domain.FailureInternal, code, false
		}
		return domain.RunFailed, domain.FailureDependency, classified.Code, classified.Retryable
	}
}

func mutationPhase(phase domain.RunStatus) bool {
	return phase == domain.RunFastForwarding || phase == domain.RunPushing || phase == domain.RunVerifying
}

func boolPointer(value bool) *bool { return &value }

func interruptedError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}
