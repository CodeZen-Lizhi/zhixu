package workspacecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	controlLeaseDuration   = 45 * time.Second
	leaseRenewInterval     = 15 * time.Second
	switchDeadlineDuration = 30 * time.Minute
	runtimeFreshWithin     = 20 * time.Second
	candidateFreshWithin   = 5 * time.Minute
	runtimeReadyTimeout    = 45 * time.Second
	quiescenceTimeout      = 90 * time.Second
	runtimePollInterval    = 250 * time.Millisecond
	recoveryInitialBackoff = 2 * time.Second
	recoveryMaximumBackoff = 30 * time.Second
	recoveryAttemptTimeout = 2 * time.Minute
)

// SwitchRuntime is the fixed Docker lifecycle used by the coordinator.
type SwitchRuntime interface {
	PrepareGrant(context.Context, foundation.ID, Grant, bool) error
	ApplyGrant(context.Context, Grant) error
	RevokeGrant(context.Context) error
}

// CoordinatorOptions separates the stable idempotency namespace from this process's lease owner.
type CoordinatorOptions struct {
	Service           *workspaceapplication.ControlService
	Runtime           SwitchRuntime
	Validator         PathValidator
	ControlInstanceID foundation.ID
	LeaseOwnerID      foundation.ID
}

// Coordinator adapts the durable Workspace state machine to one-shot host operations.
type Coordinator struct {
	service           *workspaceapplication.ControlService
	runtime           SwitchRuntime
	validator         PathValidator
	controlInstanceID foundation.ID
	leaseOwnerID      foundation.ID
	runtimeGate       chan struct{}

	recoveryInitialBackoff time.Duration
	recoveryMaximumBackoff time.Duration
	recoveryAttemptTimeout time.Duration
}

// NewCoordinator creates a synchronous coordinator for one command process.
func NewCoordinator(options CoordinatorOptions) (*Coordinator, error) {
	if options.Service == nil || options.Runtime == nil || options.ControlInstanceID == "" || options.LeaseOwnerID == "" {
		return nil, errors.New("workspace coordinator options are incomplete")
	}
	if parsed, err := foundation.ParseID(string(options.ControlInstanceID)); err != nil || parsed != options.ControlInstanceID {
		return nil, errors.New("workspace coordinator control instance is invalid")
	}
	if parsed, err := foundation.ParseID(string(options.LeaseOwnerID)); err != nil || parsed != options.LeaseOwnerID {
		return nil, errors.New("workspace coordinator lease owner is invalid")
	}
	return &Coordinator{
		service: options.Service, runtime: options.Runtime, validator: options.Validator,
		controlInstanceID:      options.ControlInstanceID,
		leaseOwnerID:           options.LeaseOwnerID,
		runtimeGate:            make(chan struct{}, 1),
		recoveryInitialBackoff: recoveryInitialBackoff,
		recoveryMaximumBackoff: recoveryMaximumBackoff,
		recoveryAttemptTimeout: recoveryAttemptTimeout,
	}, nil
}

// Reconcile restores a previously committed exact grant after a host restart.
// It never guesses a different root or generation.
func (coordinator *Coordinator) Reconcile(ctx context.Context) error {
	if err := coordinator.lockRuntime(ctx); err != nil {
		return err
	}
	defer coordinator.unlockRuntime()
	return coordinator.reconcileLocked(ctx)
}

func (coordinator *Coordinator) reconcileLocked(ctx context.Context) error {
	snapshot, err := coordinator.recoverPendingLocked(ctx)
	if err != nil {
		return err
	}
	return coordinator.reapplyActiveLocked(ctx, snapshot)
}

func (coordinator *Coordinator) recoverPendingLocked(ctx context.Context) (workspacedomain.ControlSnapshot, error) {
	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return workspacedomain.ControlSnapshot{}, controlFault(err, "")
	}
	if snapshot.Operation != nil && snapshot.Operation.Result == "" {
		operation, claimErr := coordinator.renewOrTakeOverSwitch(ctx, snapshot.Operation.ID)
		if claimErr != nil {
			return workspacedomain.ControlSnapshot{}, claimErr
		}
		if err := coordinator.recoverSwitchUntilTerminal(ctx, operation.ID, &Fault{
			Code: "WORKSPACE_CONTROL_RESTARTED", Message: "控制进程重启后正在恢复 Workspace", Retryable: true,
		}); err != nil {
			return workspacedomain.ControlSnapshot{}, controlFault(err, string(operation.ID))
		}
		terminal, terminalErr := coordinator.service.GetSwitchOperation(ctx, operation.ID)
		if terminalErr != nil {
			return workspacedomain.ControlSnapshot{}, controlFault(terminalErr, string(operation.ID))
		}
		if terminal.Result == "" {
			return workspacedomain.ControlSnapshot{}, &Fault{Code: "WORKSPACE_RECOVERY_INCOMPLETE", Message: "Workspace 自动恢复尚未完成", Retryable: true, OperationID: string(operation.ID)}
		}
		snapshot, err = coordinator.service.Snapshot(ctx, runtimeFreshWithin)
		if err != nil {
			return workspacedomain.ControlSnapshot{}, controlFault(err, "")
		}
	}
	return snapshot, nil
}

func (coordinator *Coordinator) reapplyActiveLocked(ctx context.Context, snapshot workspacedomain.ControlSnapshot) error {
	if snapshot.Active == nil {
		return coordinator.runtime.RevokeGrant(ctx)
	}
	availability, reason := coordinator.workspaceAvailability(*snapshot.Active)
	if availability != workspacedomain.WorkspaceAvailabilityAvailable {
		fault := &Fault{Code: reason, Message: "已登记的宿主机目录不可恢复"}
		revokeErr := coordinator.runtime.RevokeGrant(ctx)
		_, availabilityErr := coordinator.service.SetWorkspaceAvailability(ctx, workspacedomain.AvailabilityUpdate{
			WorkspaceID: snapshot.Active.ID, ExpectedVersion: snapshot.Active.Version,
			Availability: availability, Reason: reason, CheckedAt: time.Now().UTC(),
		})
		if revokeErr != nil {
			return errors.Join(revokeErr, fault, controlFault(availabilityErr, ""))
		}
		if availabilityErr != nil {
			return errors.Join(controlFault(availabilityErr, ""), fault)
		}
		return fault
	}
	grant := grantForWorkspace(*snapshot.Active, snapshot.State.GrantGeneration)
	if err := coordinator.runtime.ApplyGrant(ctx, grant); err != nil {
		return err
	}
	return coordinator.waitForActiveRuntimes(ctx, snapshot.Active.ID, snapshot.State.GrantGeneration)
}

// Switch validates one host root and completes the durable switch or its recovery before returning.
func (coordinator *Coordinator) Switch(ctx context.Context, command SwitchCommand) (SwitchOutcome, error) {
	if err := validateSwitchCommand(command); err != nil {
		return SwitchOutcome{}, err
	}
	validated, err := coordinator.validator.Validate(command.RootPath)
	if err != nil {
		return SwitchOutcome{}, controlFault(err, "")
	}
	name := strings.TrimSpace(command.Name)
	if name == "" {
		name = filepath.Base(validated.CanonicalPath)
	}
	if err := coordinator.lockRuntime(ctx); err != nil {
		return SwitchOutcome{}, err
	}
	defer coordinator.unlockRuntime()
	if _, err := coordinator.recoverPendingLocked(ctx); err != nil {
		return SwitchOutcome{}, err
	}

	resolution, err := coordinator.service.ResolveWorkspace(ctx, workspaceapplication.RegisterWorkspaceCommand{
		Name: name, CanonicalRoot: validated.CanonicalPath, GitRepositoryPath: validated.CanonicalPath,
		RootFingerprint: validated.Fingerprint.Digest(), BindingVersion: 1,
		GitCheckedAt: time.Now().UTC(),
	})
	if err != nil {
		return SwitchOutcome{}, controlFault(err, "")
	}
	target := resolution.Workspace
	snapshot, err := coordinator.service.Snapshot(ctx, runtimeFreshWithin)
	if err != nil {
		return SwitchOutcome{Workspace: target}, controlFault(err, "")
	}
	if snapshot.Active != nil && snapshot.Active.ID == target.ID {
		if err := coordinator.reapplyActiveLocked(ctx, snapshot); err != nil {
			return SwitchOutcome{Workspace: target, GrantGeneration: snapshot.State.GrantGeneration}, err
		}
		return SwitchOutcome{Workspace: target, GrantGeneration: snapshot.State.GrantGeneration, Changed: false}, nil
	}

	requestHash, err := switchRequestHash(target, name, command.InitializeGit)
	if err != nil {
		return SwitchOutcome{Workspace: target}, &Fault{Code: "WORKSPACE_SWITCH_REQUEST_INVALID", Message: "Workspace 切换请求无效"}
	}
	operation, err := coordinator.service.BeginSwitch(ctx, workspaceapplication.BeginSwitchCommand{
		ControllerInstanceID: coordinator.controlInstanceID, IdempotencyKey: command.IdempotencyKey,
		RequestHash: requestHash, TargetWorkspaceID: target.ID,
		ExpectedStateVersion: snapshot.State.StateVersion, LeaseOwnerID: coordinator.leaseOwnerID,
		LeaseDuration: controlLeaseDuration, Deadline: time.Now().UTC().Add(switchDeadlineDuration),
	})
	if err != nil {
		return SwitchOutcome{Workspace: target}, controlFault(err, "")
	}
	outcome := SwitchOutcome{Operation: operation, Workspace: target, Changed: true}
	if operation.Result == "" {
		if err := coordinator.executeSwitch(ctx, operation, target, command.InitializeGit); err != nil {
			if recoveryErr := coordinator.recoverSwitchUntilTerminal(ctx, operation.ID, err); recoveryErr != nil {
				return outcome, controlFault(recoveryErr, string(operation.ID))
			}
		}
	}
	terminal, err := coordinator.service.GetSwitchOperation(ctx, operation.ID)
	if err != nil {
		return outcome, controlFault(err, string(operation.ID))
	}
	outcome.Operation = terminal
	outcome.GrantGeneration = terminal.GrantGeneration
	if terminal.Result != workspacedomain.SwitchResultSucceeded {
		return outcome, switchOutcomeFault(terminal)
	}
	return outcome, nil
}

func validateSwitchCommand(command SwitchCommand) error {
	key := command.IdempotencyKey
	if key == "" || len(key) > 256 || key != strings.TrimSpace(key) || !utf8.ValidString(key) || strings.ContainsAny(key, "\x00\r\n") {
		return &Fault{Code: "WORKSPACE_SWITCH_IDEMPOTENCY_KEY_INVALID", Message: "Workspace 切换幂等键无效"}
	}
	name := strings.TrimSpace(command.Name)
	if len(name) > 256 || !utf8.ValidString(name) {
		return &Fault{Code: "WORKSPACE_NAME_INVALID", Message: "Workspace 名称无效"}
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return &Fault{Code: "WORKSPACE_NAME_INVALID", Message: "Workspace 名称无效"}
		}
	}
	return nil
}

func switchRequestHash(target workspacedomain.Workspace, requestedName string, initializeGit bool) (string, error) {
	document := struct {
		Schema            string `json:"schema"`
		TargetWorkspaceID string `json:"target_workspace_id"`
		Name              string `json:"name"`
		RootFingerprint   string `json:"root_fingerprint"`
		BindingVersion    int64  `json:"binding_version"`
		InitializeGit     bool   `json:"initialize_git"`
	}{
		Schema: "workspace-switch/v1", TargetWorkspaceID: string(target.ID),
		Name: requestedName, RootFingerprint: target.RootFingerprint, BindingVersion: target.BindingVersion,
		InitializeGit: initializeGit,
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func switchOutcomeFault(operation workspacedomain.SwitchOperation) error {
	code := operation.ErrorCode
	if code == "" {
		code = "WORKSPACE_SWITCH_" + strings.ToUpper(string(operation.Result))
	}
	return &Fault{
		Code: code, Message: "Workspace 切换未完成", Retryable: operation.Result == workspacedomain.SwitchResultFailed,
		OperationID: string(operation.ID),
	}
}

func (coordinator *Coordinator) workspaceAvailability(workspace workspacedomain.Workspace) (workspacedomain.WorkspaceAvailability, string) {
	validated, err := coordinator.validator.Validate(workspace.RootPath)
	if err != nil {
		return workspacedomain.WorkspaceAvailabilityUnavailable, pathErrorCode(err)
	}
	if validated.CanonicalPath != workspace.RootPath || validated.Fingerprint.Digest() != workspace.RootFingerprint {
		return workspacedomain.WorkspaceAvailabilityUnavailable, "WORKSPACE_PATH_IDENTITY_CHANGED"
	}
	return workspacedomain.WorkspaceAvailabilityAvailable, ""
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
		OperationID: operation.ID, LeaseOwnerID: coordinator.leaseOwnerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: controlLeaseDuration,
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
		OperationID: operation.ID, LeaseOwnerID: coordinator.leaseOwnerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: controlLeaseDuration, RuntimeFreshWithin: candidateFreshWithin,
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
		return coordinator.runtime.ApplyGrant(actionContext, grant)
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
		OperationID: operation.ID, LeaseOwnerID: coordinator.leaseOwnerID, ExpectedPhase: operation.Phase,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: controlLeaseDuration,
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
				OperationID: operation.ID, LeaseOwnerID: coordinator.leaseOwnerID,
				ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
				LeaseDuration: controlLeaseDuration,
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
		availability, reason := coordinator.workspaceAvailability(previous)
		if previous.Availability != workspacedomain.WorkspaceAvailabilityMigrationRequired &&
			(previous.Availability != availability || previous.AvailabilityReason != reason) {
			if _, updateErr := coordinator.service.SetWorkspaceAvailability(ctx, workspacedomain.AvailabilityUpdate{
				WorkspaceID: previous.ID, ExpectedVersion: previous.Version,
				Availability: availability, Reason: reason, CheckedAt: time.Now().UTC(),
			}); updateErr != nil {
				return updateErr
			}
		}
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
		OperationID: operation.ID, LeaseOwnerID: coordinator.leaseOwnerID,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: controlLeaseDuration, RuntimeFreshWithin: candidateFreshWithin,
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
		return coordinator.runtime.ApplyGrant(actionContext, grant)
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
				OperationID: operation.ID, LeaseOwnerID: coordinator.leaseOwnerID, ExpectedPhase: operation.Phase,
				ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
				LeaseDuration: controlLeaseDuration,
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
			return &Fault{Code: "WORKSPACE_QUIESCENCE_TIMEOUT", Message: "旧 Workspace 未能在安全检查点暂停", Retryable: true}
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
			if !record.Fresh {
				continue
			}
			expected[record.Role] = runtimeResumeExpectation{InstanceID: record.InstanceID, Version: record.Version}
			continue
		}
		if record.OperationID == nil || *record.OperationID != operation.ID ||
			(record.Phase != workspacedomain.RuntimePhaseQuiescing && record.Phase != workspacedomain.RuntimePhaseQuiesced) {
			continue
		}
		if !record.Fresh {
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
		return coordinator.restorePreviousGrantBeforeCancellation(ctx, operation, snapshot)
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

func (coordinator *Coordinator) restorePreviousGrantBeforeCancellation(
	ctx context.Context,
	operation workspacedomain.SwitchOperation,
	snapshot workspacedomain.ControlSnapshot,
) error {
	previousGeneration := operation.GrantGeneration - 1
	previous, found := findWorkspace(snapshot.Registry, *operation.PreviousWorkspaceID)
	if previousGeneration < 1 || !found || previous.Availability != workspacedomain.WorkspaceAvailabilityAvailable ||
		!previous.RemovedAt.IsZero() || snapshot.State.ActiveWorkspaceID == nil ||
		*snapshot.State.ActiveWorkspaceID != previous.ID || snapshot.State.GrantGeneration != previousGeneration {
		return &Fault{Code: "WORKSPACE_QUIESCENCE_RESUME_FAILED", Message: "旧 Workspace 运行时无法恢复", Retryable: true, OperationID: string(operation.ID)}
	}
	grant := grantForWorkspace(previous, previousGeneration)
	if err := coordinator.validateGrantIdentity(grant); err != nil {
		var validationError *ValidationError
		if errors.As(err, &validationError) && validationError.Code == "WORKSPACE_PATH_IDENTITY_CHANGED" {
			return coordinator.failClosedAfterPreviousIdentityChange(ctx, operation)
		}
		return err
	}
	var err error
	operation, err = coordinator.runLeasedAction(ctx, operation, func(actionContext context.Context) error {
		return coordinator.runtime.ApplyGrant(actionContext, grant)
	})
	if err != nil {
		return err
	}
	operation, err = coordinator.runLeasedAction(ctx, operation, func(actionContext context.Context) error {
		return coordinator.waitForActiveRuntimes(actionContext, previous.ID, previousGeneration)
	})
	if err != nil {
		return err
	}
	return coordinator.finish(ctx, operation, workspacedomain.SwitchResultCancelled, "")
}

func (coordinator *Coordinator) failClosedAfterPreviousIdentityChange(
	ctx context.Context,
	operation workspacedomain.SwitchOperation,
) error {
	operation, err := coordinator.advance(ctx, operation, workspacedomain.SwitchPhaseRevoking)
	if err != nil {
		return err
	}
	return coordinator.recoverSwitch(ctx, operation.ID, &Fault{
		Code: "WORKSPACE_PATH_IDENTITY_CHANGED", Message: "已登记的宿主机目录身份已变化",
		OperationID: string(operation.ID),
	})
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
			return &Fault{Code: "WORKSPACE_QUIESCENCE_RESUME_TIMEOUT", Message: "旧 Workspace 运行时未能恢复", Retryable: true}
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
			return workspacedomain.SwitchOperation{}, controlFault(err, string(operation.ID))
		}
		if snapshot.Operation == nil || snapshot.Operation.ID != operation.ID || snapshot.Operation.Result != "" {
			return workspacedomain.SwitchOperation{}, &Fault{Code: workspacedomain.ErrorCodeControlStateConflict, Message: "Workspace 切换状态已变化"}
		}
		operation = *snapshot.Operation
		taken, err := coordinator.service.TakeOverSwitch(ctx, workspaceapplication.TakeOverSwitchCommand{
			OperationID: operation.ID, NewLeaseOwnerID: coordinator.leaseOwnerID, ExpectedPhase: operation.Phase,
			ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
			LeaseDuration: controlLeaseDuration,
		})
		if err == nil {
			return taken, nil
		}
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Code == workspacedomain.ErrorCodeSwitchLeaseHeld {
			continue
		}
		return workspacedomain.SwitchOperation{}, controlFault(err, string(operation.ID))
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
			return nil, &Fault{Code: "WORKSPACE_CANDIDATE_NOT_PREPARED", Message: "Workspace 候选运行时尚未准备完成", Retryable: true, OperationID: string(operation.ID)}
		}
		records = append(records, record)
	}
	return records, nil
}

func (coordinator *Coordinator) validateGrantIdentity(grant Grant) error {
	validated, err := coordinator.validator.Validate(grant.Root)
	if err != nil || validated.CanonicalPath != grant.Root ||
		validated.Fingerprint.Digest() != grant.RootFingerprint {
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
		OperationID: operation.ID, LeaseOwnerID: coordinator.leaseOwnerID,
		ExpectedPhase: operation.Phase, NextPhase: next,
		ExpectedOperationVersion: operation.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		LeaseDuration: controlLeaseDuration,
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
	_, err = coordinator.service.FinishSwitch(ctx, workspaceapplication.FinishSwitchCommand{
		OperationID: current.ID, LeaseOwnerID: coordinator.leaseOwnerID, Result: result, ErrorCode: errorCode,
		ExpectedOperationVersion: current.Version, ExpectedStateVersion: snapshot.State.StateVersion,
		RuntimeFreshWithin: runtimeFreshWithin,
	})
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
			return &Fault{Code: "WORKSPACE_RUNTIME_READY_TIMEOUT", Message: "Workspace 运行时未能就绪", Retryable: true}
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

func controlFault(err error, operationID string) error {
	if err == nil {
		return nil
	}
	if fault, ok := AsFault(err); ok {
		if operationID != "" && fault.OperationID == "" {
			copy := *fault
			copy.OperationID = operationID
			copy.cause = err
			return &copy
		}
		return fault
	}
	var pathError *PathError
	if errors.As(err, &pathError) {
		return &Fault{Code: pathError.Code, Message: "Workspace 路径不可用", OperationID: operationID, cause: err}
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return &Fault{Code: "WORKSPACE_CONTROL_UNAVAILABLE", Message: "Workspace 控制服务暂不可用", Retryable: true, OperationID: operationID, cause: err}
	}
	message := "Workspace 控制操作失败"
	switch classified.Kind {
	case foundation.ErrorInvalidInput:
		message = "Workspace 控制请求无效"
	case foundation.ErrorNotFound:
		message = "Workspace 或切换操作不存在"
	case foundation.ErrorVersionConflict:
		message = "Workspace 状态已变化，请重试"
	case foundation.ErrorPermissionDenied:
		message = "Workspace 目录未获授权"
	case foundation.ErrorDependencyUnavailable, foundation.ErrorRetryableFailure:
		message = "Workspace 控制依赖暂不可用"
	case foundation.ErrorManualRecoveryRequired:
		message = "Workspace 需要人工恢复"
	}
	return &Fault{Code: classified.Code, Message: message, Retryable: classified.Retryable, OperationID: operationID, cause: err}
}
