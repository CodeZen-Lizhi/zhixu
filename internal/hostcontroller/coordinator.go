package hostcontroller

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	controllerLeaseDuration = 45 * time.Second
	leaseRenewInterval      = 15 * time.Second
	switchDeadlineDuration  = 30 * time.Minute
	runtimeFreshWithin      = 20 * time.Second
	candidateFreshWithin    = 5 * time.Minute
	runtimeReadyTimeout     = 45 * time.Second
	quiescenceTimeout       = 90 * time.Second
	runtimePollInterval     = 250 * time.Millisecond
	recoveryInitialBackoff  = 2 * time.Second
	recoveryMaximumBackoff  = 30 * time.Second
	recoveryAttemptTimeout  = 2 * time.Minute
)

// SwitchRuntime is the fixed Docker lifecycle used by the coordinator.
type SwitchRuntime interface {
	PrepareGrant(context.Context, foundation.ID, Grant, bool) error
	ApplyGrant(context.Context, Grant) (*url.URL, error)
	RevokeGrant(context.Context) error
	CurrentBackend(context.Context) (*url.URL, error)
}

// CoordinatorOptions fixes the durable state service and host runtime owner.
type CoordinatorOptions struct {
	Service   *workspaceapplication.ControlService
	Runtime   SwitchRuntime
	Validator PathValidator
	OwnerID   foundation.ID
}

// Coordinator adapts the durable Workspace state machine to the Host Control API.
type Coordinator struct {
	service   *workspaceapplication.ControlService
	runtime   SwitchRuntime
	validator PathValidator
	ownerID   foundation.ID
	lifecycle context.Context
	cancel    context.CancelFunc

	mu            sync.Mutex
	closed        bool
	running       map[foundation.ID]struct{}
	lastOperation *workspacedomain.SwitchOperation
	wg            sync.WaitGroup
	runtimeGate   chan struct{}

	recoveryInitialBackoff time.Duration
	recoveryMaximumBackoff time.Duration
	recoveryAttemptTimeout time.Duration
}

// NewCoordinator creates one process owner for asynchronous Workspace switches.
func NewCoordinator(options CoordinatorOptions) (*Coordinator, error) {
	if options.Service == nil || options.Runtime == nil || options.OwnerID == "" {
		return nil, errors.New("workspace coordinator options are incomplete")
	}
	if parsed, err := foundation.ParseID(string(options.OwnerID)); err != nil || parsed != options.OwnerID {
		return nil, errors.New("workspace coordinator owner is invalid")
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	return &Coordinator{
		service: options.Service, runtime: options.Runtime, validator: options.Validator,
		ownerID: options.OwnerID, lifecycle: lifecycle, cancel: cancel,
		running:                make(map[foundation.ID]struct{}),
		runtimeGate:            make(chan struct{}, 1),
		recoveryInitialBackoff: recoveryInitialBackoff,
		recoveryMaximumBackoff: recoveryMaximumBackoff,
		recoveryAttemptTimeout: recoveryAttemptTimeout,
	}, nil
}

// Close stops in-process switch and recovery work and waits for it to exit.
func (coordinator *Coordinator) Close() {
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return
	}
	coordinator.closed = true
	coordinator.cancel()
	coordinator.mu.Unlock()
	coordinator.wg.Wait()
}

// Reconcile restores a previously committed exact grant after Controller or
// host restart. It never guesses a different root or generation.
func (coordinator *Coordinator) Reconcile(ctx context.Context) error {
	if err := coordinator.lockRuntime(ctx); err != nil {
		return err
	}
	defer coordinator.unlockRuntime()

	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return controllerFault(err, "")
	}
	if snapshot.Operation != nil && snapshot.Operation.Result == "" {
		operation, claimErr := coordinator.renewOrTakeOverSwitch(ctx, snapshot.Operation.ID)
		if claimErr != nil {
			return claimErr
		}
		if err := coordinator.recoverSwitchUntilTerminal(ctx, operation.ID, &Fault{
			Code: "WORKSPACE_CONTROLLER_RESTARTED", Message: "控制器重启后正在恢复 Workspace", Retryable: true, Status: 503,
		}); err != nil {
			return err
		}
		terminal, terminalErr := coordinator.service.GetSwitchOperation(ctx, operation.ID)
		if terminalErr != nil {
			return controllerFault(terminalErr, string(operation.ID))
		}
		if terminal.Result == "" {
			return &Fault{Code: "WORKSPACE_RECOVERY_INCOMPLETE", Message: "Workspace 自动恢复尚未完成", Retryable: true, Status: 503, OperationID: string(operation.ID)}
		}
		snapshot, err = coordinator.service.Snapshot(ctx, runtimeFreshWithin)
		if err != nil {
			return controllerFault(err, "")
		}
	}
	if snapshot.Active == nil {
		return coordinator.runtime.RevokeGrant(ctx)
	}
	validated, err := coordinator.validator.Validate(snapshot.Active.RootPath)
	if err != nil || validated.CanonicalPath != snapshot.Active.RootPath ||
		validated.Fingerprint.Digest() != snapshot.Active.RootFingerprint || validated.Fingerprint.BindingVersion != snapshot.Active.BindingVersion {
		return &Fault{Code: "WORKSPACE_PATH_IDENTITY_CHANGED", Message: "已登记的宿主机目录不可恢复", Status: 409}
	}
	grant := grantForWorkspace(*snapshot.Active, snapshot.State.GrantGeneration)
	if _, err := coordinator.runtime.ApplyGrant(ctx, grant); err != nil {
		return err
	}
	return coordinator.waitForActiveRuntimes(ctx, snapshot.Active.ID, snapshot.State.GrantGeneration)
}

// State projects the complete durable state without exposing unverified roots.
func (coordinator *Coordinator) State(ctx context.Context) (State, error) {
	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return State{}, controllerFault(err, "")
	}
	operation := snapshot.Operation
	if operation == nil {
		coordinator.mu.Lock()
		if coordinator.lastOperation != nil {
			copy := *coordinator.lastOperation
			operation = &copy
		}
		coordinator.mu.Unlock()
	}
	return projectState(snapshot, operation), nil
}

// BeginSwitch validates or resolves the Registry target and starts one durable operation.
func (coordinator *Coordinator) BeginSwitch(ctx context.Context, command SwitchCommand) (Operation, error) {
	coordinator.mu.Lock()
	closed := coordinator.closed
	coordinator.mu.Unlock()
	if closed {
		return Operation{}, &Fault{Code: "WORKSPACE_CONTROLLER_STOPPING", Message: "Workspace 控制器正在停止", Retryable: true, Status: 503}
	}
	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return Operation{}, controllerFault(err, "")
	}
	if snapshot.State.StateVersion != command.ExpectedStateVersion {
		return Operation{}, &Fault{Code: workspacedomain.ErrorCodeControlStateConflict, Message: "控制状态已变化，请重试", Status: 409}
	}
	controllerID, err := foundation.ParseID(command.ControllerInstanceID)
	if err != nil {
		return Operation{}, &Fault{Code: "CONTROLLER_INSTANCE_INVALID", Message: "控制器实例无效", Status: 409}
	}
	target, err := coordinator.resolveTarget(ctx, snapshot, command)
	if err != nil {
		return Operation{}, err
	}
	operation, err := coordinator.service.BeginSwitch(ctx, workspaceapplication.BeginSwitchCommand{
		ControllerInstanceID: controllerID, IdempotencyKey: command.IdempotencyKey,
		RequestHash: command.RequestHash, TargetWorkspaceID: target.ID,
		ExpectedStateVersion: command.ExpectedStateVersion, LeaseOwnerID: coordinator.ownerID,
		LeaseDuration: controllerLeaseDuration, Deadline: time.Now().UTC().Add(switchDeadlineDuration),
	})
	if err != nil {
		return Operation{}, controllerFault(err, "")
	}
	coordinator.mu.Lock()
	coordinator.lastOperation = nil
	_, alreadyRunning := coordinator.running[operation.ID]
	closed = coordinator.closed
	shouldRun := !alreadyRunning && !closed && operation.Result == ""
	if shouldRun {
		coordinator.running[operation.ID] = struct{}{}
		coordinator.wg.Add(1)
	}
	coordinator.mu.Unlock()
	if shouldRun {
		go coordinator.runSwitch(operation.ID, target, command.InitializeGit)
	}
	return projectOperation(operation, target.Name), nil
}

// Operation returns one durable operation, including terminal history.
func (coordinator *Coordinator) Operation(ctx context.Context, rawID string) (Operation, error) {
	id, err := foundation.ParseID(rawID)
	if err != nil {
		return Operation{}, &Fault{Code: workspacedomain.ErrorCodeSwitchNotFound, Message: "切换操作不存在", Status: 404}
	}
	operation, err := coordinator.service.GetSwitchOperation(ctx, id)
	if err != nil {
		return Operation{}, controllerFault(err, rawID)
	}
	name := coordinator.workspaceName(ctx, operation.TargetWorkspaceID)
	return projectOperation(operation, name), nil
}

// CheckAvailability revalidates the exact persisted physical identity.
func (coordinator *Coordinator) CheckAvailability(ctx context.Context, rawID string, expectedStateVersion int64) (Workspace, error) {
	_, workspace, err := coordinator.versionedWorkspace(ctx, rawID, expectedStateVersion)
	if err != nil {
		return Workspace{}, err
	}
	availability, reason := coordinator.workspaceAvailability(workspace)
	updated, err := coordinator.service.SetWorkspaceAvailability(ctx, workspacedomain.AvailabilityUpdate{
		WorkspaceID: workspace.ID, ExpectedVersion: workspace.Version,
		Availability: availability, Reason: reason, CheckedAt: time.Now().UTC(),
	})
	if err != nil {
		return Workspace{}, controllerFault(err, "")
	}
	return projectWorkspace(updated), nil
}

// RemoveWorkspace soft-removes one inactive Registry identity.
func (coordinator *Coordinator) RemoveWorkspace(ctx context.Context, rawID string, expectedStateVersion int64) error {
	_, workspace, err := coordinator.versionedWorkspace(ctx, rawID, expectedStateVersion)
	if err != nil {
		return err
	}
	_, err = coordinator.service.RemoveWorkspace(ctx, workspacedomain.WorkspaceRemoval{
		WorkspaceID: workspace.ID, ExpectedVersion: workspace.Version, RemovedAt: time.Now().UTC(),
	})
	return controllerFault(err, "")
}

func (coordinator *Coordinator) resolveTarget(ctx context.Context, snapshot workspacedomain.ControlSnapshot, command SwitchCommand) (workspacedomain.Workspace, error) {
	if command.TargetKind == "registered" {
		id, err := foundation.ParseID(command.WorkspaceID)
		if err != nil {
			return workspacedomain.Workspace{}, &Fault{Code: workspacedomain.ErrorCodeWorkspaceNotFound, Message: "Workspace 不存在", Status: 404}
		}
		workspace, found := findWorkspace(snapshot.Registry, id)
		if !found {
			return workspacedomain.Workspace{}, &Fault{Code: workspacedomain.ErrorCodeWorkspaceNotFound, Message: "Workspace 不存在", Status: 404}
		}
		availability, reason := coordinator.workspaceAvailability(workspace)
		if availability != workspacedomain.WorkspaceAvailabilityAvailable {
			if _, updateErr := coordinator.service.SetWorkspaceAvailability(ctx, workspacedomain.AvailabilityUpdate{
				WorkspaceID: workspace.ID, ExpectedVersion: workspace.Version,
				Availability: availability, Reason: reason, CheckedAt: time.Now().UTC(),
			}); updateErr != nil {
				return workspacedomain.Workspace{}, controllerFault(updateErr, "")
			}
			return workspacedomain.Workspace{}, &Fault{
				Code: reason, Message: "该 Workspace 的宿主机目录不可用，请重新选择目录", Status: 409,
			}
		}
		return workspace, nil
	}
	if command.TargetKind != "new" {
		return workspacedomain.Workspace{}, &Fault{Code: "WORKSPACE_SWITCH_TARGET_INVALID", Message: "Workspace 切换目标无效", Status: 400}
	}
	resolution, err := coordinator.service.ResolveWorkspace(ctx, workspaceapplication.RegisterWorkspaceCommand{
		Name: command.Name, CanonicalRoot: command.RootPath, GitRepositoryPath: command.RootPath,
		RootFingerprint: command.RootFingerprint, BindingVersion: command.BindingVersion,
		GitCheckedAt: time.Now().UTC(),
	})
	if err != nil {
		return workspacedomain.Workspace{}, controllerFault(err, "")
	}
	return resolution.Workspace, nil
}

func (coordinator *Coordinator) workspaceAvailability(workspace workspacedomain.Workspace) (workspacedomain.WorkspaceAvailability, string) {
	validated, err := coordinator.validator.Validate(workspace.RootPath)
	if err != nil {
		return workspacedomain.WorkspaceAvailabilityUnavailable, pathErrorCode(err)
	}
	if validated.CanonicalPath != workspace.RootPath || validated.Fingerprint.Digest() != workspace.RootFingerprint ||
		validated.Fingerprint.BindingVersion != workspace.BindingVersion {
		return workspacedomain.WorkspaceAvailabilityUnavailable, "WORKSPACE_PATH_IDENTITY_CHANGED"
	}
	return workspacedomain.WorkspaceAvailabilityAvailable, ""
}

func (coordinator *Coordinator) runSwitch(operationID foundation.ID, target workspacedomain.Workspace, initializeGit bool) {
	defer func() {
		coordinator.mu.Lock()
		delete(coordinator.running, operationID)
		coordinator.mu.Unlock()
		coordinator.wg.Done()
	}()
	if err := coordinator.lockRuntime(coordinator.lifecycle); err != nil {
		return
	}
	defer coordinator.unlockRuntime()

	ctx, cancel := context.WithTimeout(coordinator.lifecycle, switchDeadlineDuration)
	defer cancel()
	operation, err := coordinator.service.GetSwitchOperation(ctx, operationID)
	if err == nil {
		err = coordinator.executeSwitch(ctx, operation, target, initializeGit)
	}
	if err != nil && coordinator.lifecycle.Err() == nil {
		_ = coordinator.recoverSwitchUntilTerminal(coordinator.lifecycle, operationID, err)
	}
}

func (coordinator *Coordinator) lockRuntime(ctx context.Context) error {
	select {
	case coordinator.runtimeGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (coordinator *Coordinator) unlockRuntime() {
	<-coordinator.runtimeGate
}

func (coordinator *Coordinator) executeSwitch(ctx context.Context, operation workspacedomain.SwitchOperation, target workspacedomain.Workspace, initializeGit bool) error {
	var err error
	operation, err = coordinator.advance(ctx, operation, workspacedomain.SwitchPhaseQuiescing)
	if err != nil {
		return err
	}
	if operation.PreviousWorkspaceID != nil {
		operation, err = coordinator.runLeasedAction(ctx, operation, func(actionContext context.Context) error {
			return coordinator.waitForQuiescedRuntimes(actionContext, *operation.PreviousWorkspaceID, operation.GrantGeneration-1)
		})
		if err != nil {
			return err
		}
	}
	operation, err = coordinator.advance(ctx, operation, workspacedomain.SwitchPhaseRevoking)
	if err != nil {
		return err
	}
	operation, err = coordinator.runLeasedAction(ctx, operation, coordinator.runtime.RevokeGrant)
	if err != nil {
		return err
	}
	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return err
	}
	if _, err := coordinator.service.RevokeActiveWorkspace(ctx, workspaceapplication.RevokeActiveCommand{
		OperationID: operation.ID, LeaseOwnerID: coordinator.ownerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: controllerLeaseDuration,
	}); err != nil {
		return err
	}
	operation, err = coordinator.service.GetSwitchOperation(ctx, operation.ID)
	if err != nil {
		return err
	}
	operation, err = coordinator.advance(ctx, operation, workspacedomain.SwitchPhaseApplyingGrant)
	if err != nil {
		return err
	}
	grant := grantForWorkspace(target, operation.GrantGeneration)
	operation, err = coordinator.runLeasedAction(ctx, operation, func(actionContext context.Context) error {
		return coordinator.runtime.PrepareGrant(actionContext, operation.ID, grant, initializeGit)
	})
	if err != nil {
		return err
	}
	operation, err = coordinator.advance(ctx, operation, workspacedomain.SwitchPhasePreparing)
	if err != nil {
		return err
	}
	prepared, err := coordinator.preparedRuntimes(ctx, operation, target, operation.GrantGeneration)
	if err != nil {
		return err
	}
	operation, err = coordinator.advance(ctx, operation, workspacedomain.SwitchPhaseVerifying)
	if err != nil {
		return err
	}
	for _, runtime := range prepared {
		if _, err := coordinator.service.SetRuntimePhase(ctx, workspaceapplication.RuntimePhaseCommand{
			Role: runtime.Role, InstanceID: runtime.InstanceID, OperationID: &operation.ID,
			ExpectedPhase: workspacedomain.RuntimePhasePrepared, NextPhase: workspacedomain.RuntimePhaseVerifying,
			ExpectedRuntimeVersion: runtime.Version,
		}); err != nil {
			return err
		}
	}
	operation, err = coordinator.advance(ctx, operation, workspacedomain.SwitchPhaseCommitting)
	if err != nil {
		return err
	}
	if err := coordinator.validateGrantIdentity(grant); err != nil {
		return err
	}
	snapshot, err = coordinator.service.Snapshot(ctx, candidateFreshWithin)
	if err != nil {
		return err
	}
	if _, err := coordinator.service.CommitTargetWorkspace(ctx, workspaceapplication.CommitTargetCommand{
		OperationID: operation.ID, LeaseOwnerID: coordinator.ownerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: controllerLeaseDuration, RuntimeFreshWithin: candidateFreshWithin,
	}); err != nil {
		return err
	}
	operation, err = coordinator.service.GetSwitchOperation(ctx, operation.ID)
	if err != nil {
		return err
	}
	operation, err = coordinator.advance(ctx, operation, workspacedomain.SwitchPhaseActivating)
	if err != nil {
		return err
	}
	operation, err = coordinator.runLeasedAction(ctx, operation, func(actionContext context.Context) error {
		_, applyErr := coordinator.runtime.ApplyGrant(actionContext, grant)
		return applyErr
	})
	if err != nil {
		return err
	}
	operation, err = coordinator.runLeasedAction(ctx, operation, func(actionContext context.Context) error {
		return coordinator.waitForActiveRuntimes(actionContext, target.ID, operation.GrantGeneration)
	})
	if err != nil {
		return err
	}
	return coordinator.finish(ctx, operation, workspacedomain.SwitchResultSucceeded, "")
}

func (coordinator *Coordinator) recoverSwitchUntilTerminal(ctx context.Context, operationID foundation.ID, cause error) error {
	backoff := coordinator.recoveryInitialBackoff
	for {
		attemptContext, cancelAttempt := context.WithTimeout(ctx, coordinator.recoveryAttemptTimeout)
		_, recoveryErr := coordinator.renewOrTakeOverSwitch(attemptContext, operationID)
		if recoveryErr == nil {
			recoveryErr = coordinator.recoverSwitch(attemptContext, operationID, cause)
		}
		cancelAttempt()
		if recoveryErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return errors.Join(recoveryErr, ctx.Err())
		}
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return errors.Join(recoveryErr, ctx.Err())
		}
		backoff = min(backoff*2, coordinator.recoveryMaximumBackoff)
	}
}

func (coordinator *Coordinator) renewOrTakeOverSwitch(ctx context.Context, operationID foundation.ID) (workspacedomain.SwitchOperation, error) {
	operation, err := coordinator.service.GetSwitchOperation(ctx, operationID)
	if err != nil || operation.Result != "" {
		return operation, err
	}
	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return workspacedomain.SwitchOperation{}, err
	}
	renewed, err := coordinator.service.RenewSwitch(ctx, workspaceapplication.RenewSwitchCommand{
		OperationID: operation.ID, LeaseOwnerID: coordinator.ownerID, ExpectedPhase: operation.Phase,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: controllerLeaseDuration,
	})
	if err == nil {
		return renewed, nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) &&
		(classified.Code == workspacedomain.ErrorCodeSwitchLeaseExpired || classified.Code == workspacedomain.ErrorCodeSwitchLeaseHeld) {
		return coordinator.takeOverSwitch(ctx, operation)
	}
	return workspacedomain.SwitchOperation{}, err
}

func (coordinator *Coordinator) recoverSwitch(ctx context.Context, operationID foundation.ID, cause error) error {
	operation, err := coordinator.service.GetSwitchOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if operation.Result != "" {
		return nil
	}
	errorCode := stableOperationError(cause)
	if operation.Phase == workspacedomain.SwitchPhaseValidating {
		return coordinator.finish(ctx, operation, workspacedomain.SwitchResultRejected, errorCode)
	}
	if operation.Phase == workspacedomain.SwitchPhaseQuiescing {
		return coordinator.cancelQuiescence(ctx, operation)
	}
	if operation.Phase == workspacedomain.SwitchPhaseRevoking {
		snapshot, snapshotErr := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
		if snapshotErr != nil {
			return snapshotErr
		}
		if snapshot.State.ActiveWorkspaceID != nil {
			if _, revokeErr := coordinator.service.RevokeActiveWorkspace(ctx, workspaceapplication.RevokeActiveCommand{
				OperationID: operation.ID, LeaseOwnerID: coordinator.ownerID,
				ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
				LeaseDuration: controllerLeaseDuration,
			}); revokeErr != nil {
				return revokeErr
			}
			operation, err = coordinator.service.GetSwitchOperation(ctx, operation.ID)
			if err != nil {
				return err
			}
		}
	}
	operation, err = coordinator.runLeasedAction(ctx, operation, coordinator.runtime.RevokeGrant)
	if err != nil {
		return err
	}
	if operation.Phase != workspacedomain.SwitchPhaseRollingBack && operation.Phase != workspacedomain.SwitchPhaseRecovering {
		operation, err = coordinator.advance(ctx, operation, workspacedomain.SwitchPhaseRollingBack)
		if err != nil {
			return err
		}
	}
	if operation.Phase == workspacedomain.SwitchPhaseRollingBack {
		operation, err = coordinator.advance(ctx, operation, workspacedomain.SwitchPhaseRecovering)
		if err != nil {
			return err
		}
	}
	if operation.PreviousWorkspaceID == nil {
		return coordinator.finish(ctx, operation, workspacedomain.SwitchResultFailed, errorCode)
	}
	previous, found := coordinator.registryWorkspace(ctx, *operation.PreviousWorkspaceID)
	if !found {
		return coordinator.finish(ctx, operation, workspacedomain.SwitchResultFailed, errorCode)
	}
	validated, validationErr := coordinator.validator.Validate(previous.RootPath)
	if validationErr != nil || validated.Fingerprint.Digest() != previous.RootFingerprint {
		return coordinator.finish(ctx, operation, workspacedomain.SwitchResultFailed, errorCode)
	}
	grant := grantForWorkspace(previous, operation.RecoveryGeneration)
	snapshot, err := coordinator.service.Snapshot(ctx, candidateFreshWithin)
	if err != nil {
		return err
	}
	if snapshot.State.ActiveWorkspaceID != nil && *snapshot.State.ActiveWorkspaceID == previous.ID &&
		snapshot.State.GrantGeneration == operation.RecoveryGeneration {
		return coordinator.activateRecoveredGrant(ctx, operation, previous, grant, errorCode)
	}
	operation, err = coordinator.runLeasedAction(ctx, operation, func(actionContext context.Context) error {
		return coordinator.runtime.PrepareGrant(actionContext, operation.ID, grant, false)
	})
	if err != nil {
		return coordinator.finishFailedAfterRevocation(ctx, operation.ID, errorCode)
	}
	if _, err := coordinator.preparedRuntimes(ctx, operation, previous, operation.RecoveryGeneration); err != nil {
		return coordinator.finishFailedAfterRevocation(ctx, operation.ID, errorCode)
	}
	if err := coordinator.validateGrantIdentity(grant); err != nil {
		return coordinator.finishFailedAfterRevocation(ctx, operation.ID, errorCode)
	}
	snapshot, err = coordinator.service.Snapshot(ctx, candidateFreshWithin)
	if err != nil {
		return err
	}
	if _, err := coordinator.service.RestorePreviousWorkspace(ctx, workspaceapplication.RestorePreviousCommand{
		OperationID: operation.ID, LeaseOwnerID: coordinator.ownerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: controllerLeaseDuration, RuntimeFreshWithin: candidateFreshWithin,
	}); err != nil {
		return coordinator.finishFailedAfterRevocation(ctx, operation.ID, errorCode)
	}
	operation, err = coordinator.service.GetSwitchOperation(ctx, operation.ID)
	if err != nil {
		return err
	}
	return coordinator.activateRecoveredGrant(ctx, operation, previous, grant, errorCode)
}

func (coordinator *Coordinator) activateRecoveredGrant(
	ctx context.Context,
	operation workspacedomain.SwitchOperation,
	previous workspacedomain.Workspace,
	grant Grant,
	errorCode string,
) error {
	var err error
	operation, err = coordinator.runLeasedAction(ctx, operation, func(actionContext context.Context) error {
		_, applyErr := coordinator.runtime.ApplyGrant(actionContext, grant)
		return applyErr
	})
	if err != nil {
		return coordinator.finishFailedAfterRevocation(ctx, operation.ID, errorCode)
	}
	operation, err = coordinator.runLeasedAction(ctx, operation, func(actionContext context.Context) error {
		return coordinator.waitForActiveRuntimes(actionContext, previous.ID, operation.RecoveryGeneration)
	})
	if err != nil {
		return coordinator.finishFailedAfterRevocation(ctx, operation.ID, errorCode)
	}
	return coordinator.finish(ctx, operation, workspacedomain.SwitchResultRolledBack, errorCode)
}

func (coordinator *Coordinator) finishFailedAfterRevocation(ctx context.Context, operationID foundation.ID, errorCode string) error {
	operation, err := coordinator.service.GetSwitchOperation(ctx, operationID)
	if err != nil {
		return err
	}
	operation, err = coordinator.runLeasedAction(ctx, operation, coordinator.runtime.RevokeGrant)
	if err != nil {
		return err
	}
	return coordinator.finish(ctx, operation, workspacedomain.SwitchResultFailed, errorCode)
}

func (coordinator *Coordinator) runLeasedAction(
	ctx context.Context,
	operation workspacedomain.SwitchOperation,
	action func(context.Context) error,
) (workspacedomain.SwitchOperation, error) {
	actionContext, cancelAction := context.WithCancel(ctx)
	defer cancelAction()
	result := make(chan error, 1)
	go func() { result <- action(actionContext) }()
	ticker := time.NewTicker(leaseRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return operation, err
		case <-ticker.C:
			snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
			if err != nil {
				cancelAction()
				<-result
				return operation, err
			}
			operation, err = coordinator.service.RenewSwitch(ctx, workspaceapplication.RenewSwitchCommand{
				OperationID: operation.ID, LeaseOwnerID: coordinator.ownerID, ExpectedPhase: operation.Phase,
				ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
				LeaseDuration: controllerLeaseDuration,
			})
			if err != nil {
				cancelAction()
				<-result
				return operation, err
			}
		case <-ctx.Done():
			cancelAction()
			<-result
			return operation, ctx.Err()
		}
	}
}

func (coordinator *Coordinator) waitForQuiescedRuntimes(ctx context.Context, workspaceID foundation.ID, generation int64) error {
	waitContext, cancel := context.WithTimeout(ctx, quiescenceTimeout)
	defer cancel()
	ticker := time.NewTicker(runtimePollInterval)
	defer ticker.Stop()
	for {
		snapshot, err := coordinator.service.Snapshot(waitContext, runtimeFreshWithin)
		if err == nil && snapshot.Operation != nil && quiescedRuntimesReady(snapshot.Runtimes, snapshot.Operation.ID, workspaceID, generation) {
			return nil
		}
		select {
		case <-waitContext.Done():
			return &Fault{Code: "WORKSPACE_QUIESCENCE_TIMEOUT", Message: "旧 Workspace 未能在安全检查点暂停", Retryable: true, Status: 503}
		case <-ticker.C:
		}
	}
}

func quiescedRuntimesReady(records []workspacedomain.RuntimeRecord, operationID, workspaceID foundation.ID, generation int64) bool {
	ready := map[workspacedomain.RuntimeRole]bool{}
	for _, record := range records {
		if record.WorkspaceID == workspaceID && record.GrantGeneration == generation && record.OperationID != nil &&
			*record.OperationID == operationID && record.Phase == workspacedomain.RuntimePhaseQuiesced && record.Fresh {
			ready[record.Role] = true
		}
	}
	return ready[workspacedomain.RuntimeRoleAPI] && ready[workspacedomain.RuntimeRoleWorker]
}

func (coordinator *Coordinator) cancelQuiescence(ctx context.Context, operation workspacedomain.SwitchOperation) error {
	if operation.PreviousWorkspaceID == nil {
		return coordinator.finish(ctx, operation, workspacedomain.SwitchResultCancelled, "")
	}
	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return err
	}
	expected := make(map[workspacedomain.RuntimeRole]runtimeResumeExpectation, 2)
	for _, record := range snapshot.Runtimes {
		if record.WorkspaceID != *operation.PreviousWorkspaceID || record.GrantGeneration != operation.GrantGeneration-1 {
			continue
		}
		if record.Phase == workspacedomain.RuntimePhaseActive && record.OperationID == nil {
			expected[record.Role] = runtimeResumeExpectation{InstanceID: record.InstanceID, Version: record.Version}
			continue
		}
		if record.OperationID == nil || *record.OperationID != operation.ID ||
			(record.Phase != workspacedomain.RuntimePhaseQuiescing && record.Phase != workspacedomain.RuntimePhaseQuiesced) {
			continue
		}
		updated, err := coordinator.service.SetRuntimePhase(ctx, workspaceapplication.RuntimePhaseCommand{
			Role: record.Role, InstanceID: record.InstanceID, OperationID: &operation.ID,
			ExpectedPhase: record.Phase, NextPhase: workspacedomain.RuntimePhaseActive,
			ExpectedRuntimeVersion: record.Version,
		})
		if err != nil {
			return err
		}
		expected[record.Role] = runtimeResumeExpectation{InstanceID: updated.InstanceID, Version: updated.Version}
	}
	if len(expected) != 2 {
		return &Fault{Code: "WORKSPACE_QUIESCENCE_RESUME_FAILED", Message: "旧 Workspace 运行时无法恢复", Retryable: true, Status: 503, OperationID: string(operation.ID)}
	}
	if err := coordinator.waitForResumedRuntimes(ctx, *operation.PreviousWorkspaceID, operation.GrantGeneration-1, expected); err != nil {
		return err
	}
	operation, err = coordinator.service.GetSwitchOperation(ctx, operation.ID)
	if err != nil {
		return err
	}
	return coordinator.finish(ctx, operation, workspacedomain.SwitchResultCancelled, "")
}

type runtimeResumeExpectation struct {
	InstanceID foundation.ID
	Version    int64
}

func (coordinator *Coordinator) waitForResumedRuntimes(
	ctx context.Context,
	workspaceID foundation.ID,
	generation int64,
	expected map[workspacedomain.RuntimeRole]runtimeResumeExpectation,
) error {
	waitContext, cancel := context.WithTimeout(ctx, runtimeReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(runtimePollInterval)
	defer ticker.Stop()
	for {
		snapshot, err := coordinator.service.Snapshot(waitContext, runtimeFreshWithin)
		if err == nil && resumedRuntimesReady(snapshot.Runtimes, workspaceID, generation, expected) {
			return nil
		}
		select {
		case <-waitContext.Done():
			return &Fault{Code: "WORKSPACE_QUIESCENCE_RESUME_TIMEOUT", Message: "旧 Workspace 运行时未能恢复", Retryable: true, Status: 503}
		case <-ticker.C:
		}
	}
}

func resumedRuntimesReady(
	records []workspacedomain.RuntimeRecord,
	workspaceID foundation.ID,
	generation int64,
	expected map[workspacedomain.RuntimeRole]runtimeResumeExpectation,
) bool {
	ready := make(map[workspacedomain.RuntimeRole]bool, 2)
	for _, record := range records {
		expectation, found := expected[record.Role]
		if found && record.InstanceID == expectation.InstanceID && record.Version > expectation.Version &&
			record.WorkspaceID == workspaceID && record.GrantGeneration == generation && record.OperationID == nil &&
			record.Phase == workspacedomain.RuntimePhaseActive && record.Fresh {
			ready[record.Role] = true
		}
	}
	return ready[workspacedomain.RuntimeRoleAPI] && ready[workspacedomain.RuntimeRoleWorker]
}

func (coordinator *Coordinator) takeOverSwitch(ctx context.Context, operation workspacedomain.SwitchOperation) (workspacedomain.SwitchOperation, error) {
	for {
		wait := time.Until(operation.LeaseExpiresAt)
		if wait > 0 {
			timer := time.NewTimer(wait + runtimePollInterval)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return workspacedomain.SwitchOperation{}, ctx.Err()
			}
		}
		snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
		if err != nil {
			return workspacedomain.SwitchOperation{}, controllerFault(err, string(operation.ID))
		}
		if snapshot.Operation == nil || snapshot.Operation.ID != operation.ID || snapshot.Operation.Result != "" {
			return workspacedomain.SwitchOperation{}, &Fault{Code: workspacedomain.ErrorCodeControlStateConflict, Message: "Workspace 切换状态已变化", Status: 409}
		}
		operation = *snapshot.Operation
		taken, err := coordinator.service.TakeOverSwitch(ctx, workspaceapplication.TakeOverSwitchCommand{
			OperationID: operation.ID, NewLeaseOwnerID: coordinator.ownerID, ExpectedPhase: operation.Phase,
			ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
			LeaseDuration: controllerLeaseDuration,
		})
		if err == nil {
			return taken, nil
		}
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Code == workspacedomain.ErrorCodeSwitchLeaseHeld {
			continue
		}
		return workspacedomain.SwitchOperation{}, controllerFault(err, string(operation.ID))
	}
}

func (coordinator *Coordinator) preparedRuntimes(
	ctx context.Context,
	operation workspacedomain.SwitchOperation,
	workspace workspacedomain.Workspace,
	generation int64,
) ([]workspacedomain.RuntimeRecord, error) {
	snapshot, err := coordinator.service.Snapshot(ctx, candidateFreshWithin)
	if err != nil {
		return nil, err
	}
	byRole := make(map[workspacedomain.RuntimeRole]workspacedomain.RuntimeRecord, 2)
	for _, record := range snapshot.Runtimes {
		if !record.Fresh || record.OperationID == nil || *record.OperationID != operation.ID ||
			record.WorkspaceID != workspace.ID || record.GrantGeneration != generation ||
			record.RootFingerprint != workspace.RootFingerprint || record.BindingVersion != workspace.BindingVersion ||
			record.Phase != workspacedomain.RuntimePhasePrepared {
			continue
		}
		byRole[record.Role] = record
	}
	records := make([]workspacedomain.RuntimeRecord, 0, 2)
	for _, role := range []workspacedomain.RuntimeRole{workspacedomain.RuntimeRoleAPI, workspacedomain.RuntimeRoleWorker} {
		record, found := byRole[role]
		if !found {
			return nil, &Fault{Code: "WORKSPACE_CANDIDATE_NOT_PREPARED", Message: "Workspace 候选运行时尚未准备完成", Retryable: true, Status: 503, OperationID: string(operation.ID)}
		}
		records = append(records, record)
	}
	return records, nil
}

func (coordinator *Coordinator) validateGrantIdentity(grant Grant) error {
	validated, err := coordinator.validator.Validate(grant.Root)
	if err != nil || validated.CanonicalPath != grant.Root ||
		validated.Fingerprint.Digest() != grant.RootFingerprint || validated.Fingerprint.BindingVersion != grant.BindingVersion {
		return &ValidationError{Code: "WORKSPACE_PATH_IDENTITY_CHANGED"}
	}
	return nil
}

func (coordinator *Coordinator) advance(ctx context.Context, operation workspacedomain.SwitchOperation, next workspacedomain.SwitchPhase) (workspacedomain.SwitchOperation, error) {
	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return workspacedomain.SwitchOperation{}, err
	}
	return coordinator.service.AdvanceSwitch(ctx, workspaceapplication.AdvanceSwitchCommand{
		OperationID: operation.ID, LeaseOwnerID: coordinator.ownerID,
		ExpectedPhase: operation.Phase, NextPhase: next,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: controllerLeaseDuration,
	})
}

func (coordinator *Coordinator) finish(ctx context.Context, operation workspacedomain.SwitchOperation, result workspacedomain.SwitchResult, errorCode string) error {
	current, err := coordinator.service.GetSwitchOperation(ctx, operation.ID)
	if err != nil {
		return err
	}
	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return err
	}
	terminal, err := coordinator.service.FinishSwitch(ctx, workspaceapplication.FinishSwitchCommand{
		OperationID: current.ID, LeaseOwnerID: coordinator.ownerID, Result: result, ErrorCode: errorCode,
		ExpectedOperationVersion: current.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		RuntimeFreshWithin: runtimeFreshWithin,
	})
	if err == nil {
		coordinator.mu.Lock()
		coordinator.lastOperation = &terminal
		coordinator.mu.Unlock()
	}
	return err
}

func (coordinator *Coordinator) waitForActiveRuntimes(ctx context.Context, workspaceID foundation.ID, generation int64) error {
	waitContext, cancel := context.WithTimeout(ctx, runtimeReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(runtimePollInterval)
	defer ticker.Stop()
	for {
		snapshot, err := coordinator.service.Snapshot(waitContext, runtimeFreshWithin)
		if err == nil && activeRuntimesReady(snapshot.Runtimes, workspaceID, generation) {
			return nil
		}
		select {
		case <-waitContext.Done():
			return &Fault{Code: "WORKSPACE_RUNTIME_READY_TIMEOUT", Message: "Workspace 运行时未能就绪", Retryable: true, Status: 503}
		case <-ticker.C:
		}
	}
}

func activeRuntimesReady(records []workspacedomain.RuntimeRecord, workspaceID foundation.ID, generation int64) bool {
	ready := map[workspacedomain.RuntimeRole]bool{}
	for _, record := range records {
		if record.WorkspaceID == workspaceID && record.GrantGeneration == generation && record.OperationID == nil &&
			record.Phase == workspacedomain.RuntimePhaseActive && record.Fresh {
			ready[record.Role] = true
		}
	}
	return ready[workspacedomain.RuntimeRoleAPI] && ready[workspacedomain.RuntimeRoleWorker]
}

func (coordinator *Coordinator) versionedWorkspace(ctx context.Context, rawID string, expectedStateVersion int64) (workspacedomain.ControlSnapshot, workspacedomain.Workspace, error) {
	id, err := foundation.ParseID(rawID)
	if err != nil {
		return workspacedomain.ControlSnapshot{}, workspacedomain.Workspace{}, &Fault{Code: workspacedomain.ErrorCodeWorkspaceNotFound, Message: "Workspace 不存在", Status: 404}
	}
	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return workspacedomain.ControlSnapshot{}, workspacedomain.Workspace{}, controllerFault(err, "")
	}
	if snapshot.State.StateVersion != expectedStateVersion {
		return workspacedomain.ControlSnapshot{}, workspacedomain.Workspace{}, &Fault{Code: workspacedomain.ErrorCodeControlStateConflict, Message: "控制状态已变化，请重试", Status: 409}
	}
	workspace, found := findWorkspace(snapshot.Registry, id)
	if !found {
		return workspacedomain.ControlSnapshot{}, workspacedomain.Workspace{}, &Fault{Code: workspacedomain.ErrorCodeWorkspaceNotFound, Message: "Workspace 不存在", Status: 404}
	}
	return snapshot, workspace, nil
}

func (coordinator *Coordinator) workspaceName(ctx context.Context, id foundation.ID) string {
	workspace, found := coordinator.registryWorkspace(ctx, id)
	if !found {
		return ""
	}
	return workspace.Name
}

func (coordinator *Coordinator) registryWorkspace(ctx context.Context, id foundation.ID) (workspacedomain.Workspace, bool) {
	registry, err := coordinator.service.ListRegistry(ctx, true)
	if err != nil {
		return workspacedomain.Workspace{}, false
	}
	return findWorkspace(registry, id)
}

func findWorkspace(registry []workspacedomain.Workspace, id foundation.ID) (workspacedomain.Workspace, bool) {
	for _, workspace := range registry {
		if workspace.ID == id {
			return workspace, true
		}
	}
	return workspacedomain.Workspace{}, false
}

func grantForWorkspace(workspace workspacedomain.Workspace, generation int64) Grant {
	return Grant{
		WorkspaceID: string(workspace.ID), Root: workspace.RootPath,
		RootFingerprint: workspace.RootFingerprint, BindingVersion: workspace.BindingVersion,
		Generation: generation,
	}
}

func projectState(snapshot workspacedomain.ControlSnapshot, operation *workspacedomain.SwitchOperation) State {
	state := State{StateVersion: snapshot.State.StateVersion, RecentWorkspaces: make([]Workspace, 0, len(snapshot.Registry)), PollAfterMS: 750}
	for _, workspace := range snapshot.Registry {
		state.RecentWorkspaces = append(state.RecentWorkspaces, projectWorkspace(workspace))
	}
	if snapshot.Active != nil {
		active := projectWorkspace(*snapshot.Active)
		state.ActiveWorkspace = &active
	}
	if operation != nil {
		name := ""
		if target, found := findWorkspace(snapshot.Registry, operation.TargetWorkspaceID); found {
			name = target.Name
		}
		projected := projectOperation(*operation, name)
		state.Operation = &projected
	}
	ready := snapshot.Active != nil && activeRuntimesReady(snapshot.Runtimes, snapshot.Active.ID, snapshot.State.GrantGeneration)
	switch {
	case snapshot.Operation != nil && snapshot.Operation.Result == "":
		state.Runtime = RuntimeState{Status: RuntimeSwitching, API: ProcessState{Status: ProcessStarting}, Worker: ProcessState{Status: ProcessStarting}}
	case ready:
		state.Runtime = RuntimeState{Status: RuntimeReady, API: ProcessState{Status: ProcessReady}, Worker: ProcessState{Status: ProcessReady}}
	case snapshot.Active == nil && snapshot.State.LastErrorCode != "":
		state.Runtime = RuntimeState{Status: RuntimeRecoveryFailed, API: ProcessState{Status: ProcessUnavailable}, Worker: ProcessState{Status: ProcessUnavailable}}
	default:
		state.Runtime = RuntimeState{Status: RuntimeWaitingForWorkspace, API: ProcessState{Status: ProcessStopped}, Worker: ProcessState{Status: ProcessStopped}}
	}
	return state
}

func projectWorkspace(workspace workspacedomain.Workspace) Workspace {
	projected := Workspace{
		WorkspaceID: string(workspace.ID), Name: workspace.Name, RootPath: workspace.RootPath,
		Availability: Availability(workspace.Availability), AvailabilityReason: workspace.AvailabilityReason,
	}
	if !workspace.LastOpenedAt.IsZero() {
		opened := workspace.LastOpenedAt.UTC()
		projected.LastOpenedAt = &opened
	}
	return projected
}

func projectOperation(operation workspacedomain.SwitchOperation, targetName string) Operation {
	return Operation{
		OperationID: string(operation.ID), Phase: string(operation.Phase), Result: string(operation.Result),
		TargetWorkspaceID: string(operation.TargetWorkspaceID), TargetName: targetName,
		ErrorCode: operation.ErrorCode, Retryable: operation.Result == workspacedomain.SwitchResultFailed,
		StartedAt: operation.CreatedAt.UTC(), UpdatedAt: operation.UpdatedAt.UTC(),
	}
}

func pathErrorCode(err error) string {
	var pathError *PathError
	if errors.As(err, &pathError) && pathError.Code != "" {
		return pathError.Code
	}
	return "WORKSPACE_PATH_UNAVAILABLE"
}

func stableOperationError(err error) string {
	if fault, ok := AsFault(err); ok && fault.Code != "" {
		return fault.Code
	}
	var pathError *PathError
	if errors.As(err, &pathError) && pathError.Code != "" {
		return pathError.Code
	}
	var classified *foundation.Error
	if errors.As(err, &classified) && classified.Code != "" {
		return classified.Code
	}
	return "WORKSPACE_SWITCH_FAILED"
}

func controllerFault(err error, operationID string) error {
	if err == nil {
		return nil
	}
	if fault, ok := AsFault(err); ok {
		return fault
	}
	var pathError *PathError
	if errors.As(err, &pathError) {
		return &Fault{Code: pathError.Code, Message: "Workspace 路径不可用", Status: 400, OperationID: operationID}
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return &Fault{Code: "WORKSPACE_CONTROL_UNAVAILABLE", Message: "Workspace 控制服务暂不可用", Retryable: true, Status: 503, OperationID: operationID}
	}
	status := 500
	message := "Workspace 控制操作失败"
	switch classified.Kind {
	case foundation.ErrorInvalidInput:
		status, message = 400, "Workspace 控制请求无效"
	case foundation.ErrorNotFound:
		status, message = 404, "Workspace 或切换操作不存在"
	case foundation.ErrorVersionConflict:
		status, message = 409, "Workspace 状态已变化，请重试"
	case foundation.ErrorPermissionDenied:
		status, message = 403, "Workspace 目录未获授权"
	case foundation.ErrorDependencyUnavailable, foundation.ErrorRetryableFailure:
		status, message = 503, "Workspace 控制依赖暂不可用"
	case foundation.ErrorManualRecoveryRequired:
		status, message = 503, "Workspace 需要人工恢复"
	}
	return &Fault{Code: classified.Code, Message: message, Retryable: classified.Retryable, Status: status, OperationID: operationID}
}
