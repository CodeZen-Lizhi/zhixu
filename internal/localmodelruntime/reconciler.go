package localmodelruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	defaultManagerLease       = 30 * time.Second
	defaultManagerHeartbeat   = 10 * time.Second
	defaultReconcileInterval  = 2 * time.Second
	defaultReconcileBackoff   = 2 * time.Second
	defaultHealthWait         = 30 * time.Second
	defaultOperationLease     = 10 * time.Minute
	defaultOperationTimeout   = 6 * time.Hour
	defaultPullAttempts       = 3
	defaultProgressMinSpacing = time.Second
	defaultRecoveryCheckEvery = time.Second
	defaultRecoveryCheckLimit = 750 * time.Millisecond
)

var (
	ErrReconcilerLeaseLost          = errors.New("local model runtime manager lease is unavailable")
	ErrRuntimeModeMismatch          = errors.New("local model runtime database mode does not match managed mode")
	ErrPreparationAttemptsExhausted = errors.New("local model runtime preparation attempts are exhausted")
	ErrPreparationOperationRequired = errors.New("local model runtime model preparation requires a persisted operation")
)

// ReconcilerOptions contains only deployment-owned timing and identity knobs.
// Settings and database input cannot alter the child endpoint or executable.
type ReconcilerOptions struct {
	OwnerID             foundation.ID
	ManagerLease        time.Duration
	HeartbeatInterval   time.Duration
	ReconcileInterval   time.Duration
	ErrorBackoff        time.Duration
	HealthWait          time.Duration
	OperationLease      time.Duration
	OperationTimeout    time.Duration
	PullAttempts        int
	ProgressMinInterval time.Duration
	RecoveryCheckEvery  time.Duration
	RecoveryCheckLimit  time.Duration
	OnError             func(error)
}

// Reconciler converges durable effective demand onto one local child.
// Database reads and writes are never held across child or Ollama I/O.
type Reconciler struct {
	store   LifecycleStore
	child   ChildController
	ollama  OllamaControlClient
	options ReconcilerOptions

	mu                   sync.RWMutex
	writeMu              sync.Mutex
	lease                ManagerLease
	settingsStateVersion int64
	workCancel           context.CancelFunc
}

// NewReconciler validates the managed-runtime dependencies and bounds.
func NewReconciler(store LifecycleStore, child ChildController, ollama OllamaControlClient, options ReconcilerOptions) (*Reconciler, error) {
	if store == nil || child == nil || ollama == nil {
		return nil, errors.New("local model runtime reconciler dependencies are incomplete")
	}
	if !validID(options.OwnerID) {
		return nil, errors.New("local model runtime reconciler owner is invalid")
	}
	applyReconcilerDefaults(&options)
	if options.ManagerLease <= 0 || options.ManagerLease > 10*time.Minute ||
		options.HeartbeatInterval <= 0 || options.HeartbeatInterval*2 >= options.ManagerLease ||
		options.ReconcileInterval <= 0 || options.ReconcileInterval > time.Minute ||
		options.ErrorBackoff <= 0 || options.ErrorBackoff > time.Minute ||
		options.HealthWait <= 0 || options.HealthWait > 5*time.Minute ||
		options.OperationLease <= 0 || options.OperationLease > time.Hour ||
		options.OperationTimeout <= 0 || options.OperationTimeout > 24*time.Hour ||
		options.PullAttempts < 1 || options.PullAttempts > 3 ||
		options.ProgressMinInterval < 0 || options.ProgressMinInterval > time.Minute ||
		options.RecoveryCheckEvery <= 0 || options.RecoveryCheckEvery > time.Minute ||
		options.RecoveryCheckLimit <= 0 || options.RecoveryCheckLimit >= options.RecoveryCheckEvery {
		return nil, errors.New("local model runtime reconciler limits are invalid")
	}
	return &Reconciler{store: store, child: child, ollama: ollama, options: options}, nil
}

func applyReconcilerDefaults(options *ReconcilerOptions) {
	if options.ManagerLease == 0 {
		options.ManagerLease = defaultManagerLease
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = defaultManagerHeartbeat
	}
	if options.ReconcileInterval == 0 {
		options.ReconcileInterval = defaultReconcileInterval
	}
	if options.ErrorBackoff == 0 {
		options.ErrorBackoff = defaultReconcileBackoff
	}
	if options.HealthWait == 0 {
		options.HealthWait = defaultHealthWait
	}
	if options.OperationLease == 0 {
		options.OperationLease = defaultOperationLease
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = defaultOperationTimeout
	}
	if options.PullAttempts == 0 {
		options.PullAttempts = defaultPullAttempts
	}
	if options.ProgressMinInterval == 0 {
		options.ProgressMinInterval = defaultProgressMinSpacing
	}
	if options.RecoveryCheckEvery == 0 {
		options.RecoveryCheckEvery = defaultRecoveryCheckEvery
	}
	if options.RecoveryCheckLimit == 0 {
		options.RecoveryCheckLimit = defaultRecoveryCheckLimit
	}
}

// Run claims the singleton manager lease, keeps it fresh independently from
// long pulls, and retries transient reconcile failures until cancellation.
// While PostgreSQL is uncertain it performs no process or readiness change.
func (reconciler *Reconciler) Run(ctx context.Context) error {
	if reconciler == nil || ctx == nil {
		return errors.New("local model runtime reconciler context is invalid")
	}
	var lease ManagerLease
	for {
		claimed, err := reconciler.claim(ctx)
		if err == nil {
			if claimed.Mode != RuntimeModeManaged {
				return ErrRuntimeModeMismatch
			}
			lease = claimed
			break
		}
		if ctx.Err() != nil {
			return nil
		}
		reconciler.report(fmt.Errorf("%w: %w", ErrReconcilerLeaseLost, err))
		if !waitContext(ctx, reconciler.options.ErrorBackoff) {
			return nil
		}
	}
	reconciler.setLease(lease)

	leaseAvailable := make(chan bool, 1)
	leaseAvailable <- true
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		reconciler.heartbeatLoop(heartbeatCtx, leaseAvailable)
	}()
	defer func() {
		cancelHeartbeat()
		<-heartbeatDone
	}()

	owned := true
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case owned = <-leaseAvailable:
			if !owned {
				timer.Reset(reconciler.options.ErrorBackoff)
			}
		case <-timer.C:
			if !owned {
				timer.Reset(reconciler.options.ErrorBackoff)
				continue
			}
			workCtx, cancelWork := context.WithCancel(ctx)
			reconciler.setWorkCancel(cancelWork)
			err := reconciler.ReconcileOnce(workCtx)
			reconciler.setWorkCancel(nil)
			cancelWork()
			if err != nil {
				reconciler.report(err)
				timer.Reset(reconciler.options.ErrorBackoff)
			} else {
				timer.Reset(reconciler.options.ReconcileInterval)
			}
		}
	}
}

func (reconciler *Reconciler) heartbeatLoop(ctx context.Context, available chan bool) {
	ticker := time.NewTicker(reconciler.options.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		reconciler.writeMu.Lock()
		lease := reconciler.currentLease()
		updated, err := reconciler.store.HeartbeatManager(ctx, ManagerHeartbeatCommand{
			OwnerID: reconciler.options.OwnerID, OwnerEpoch: lease.OwnerEpoch,
			ExpectedVersion: lease.Version, LeaseDuration: reconciler.options.ManagerLease,
		})
		if err == nil && updated.Mode == RuntimeModeManaged {
			reconciler.setLease(updated)
			reconciler.writeMu.Unlock()
			publishLatest(available, true)
			continue
		}
		reconciler.writeMu.Unlock()
		if err == nil {
			err = ErrRuntimeModeMismatch
		}
		reconciler.report(fmt.Errorf("%w: %v", ErrReconcilerLeaseLost, err))
		reconciler.cancelWork()
		publishLatest(available, false)
		for ctx.Err() == nil {
			reconciler.writeMu.Lock()
			reclaimed, claimErr := reconciler.claim(ctx)
			if claimErr == nil && reclaimed.Mode == RuntimeModeManaged {
				reconciler.setLease(reclaimed)
				reconciler.writeMu.Unlock()
				publishLatest(available, true)
				break
			}
			reconciler.writeMu.Unlock()
			if claimErr == nil {
				claimErr = ErrRuntimeModeMismatch
			}
			reconciler.report(fmt.Errorf("%w: %v", ErrReconcilerLeaseLost, claimErr))
			if !waitContext(ctx, reconciler.options.ErrorBackoff) {
				return
			}
		}
	}
}

func (reconciler *Reconciler) claim(ctx context.Context) (ManagerLease, error) {
	return reconciler.store.ClaimManager(ctx, ManagerClaimCommand{
		OwnerID: reconciler.options.OwnerID, LeaseDuration: reconciler.options.ManagerLease,
		StaleAfter: reconciler.options.ManagerLease,
	})
}

func (reconciler *Reconciler) currentLease() ManagerLease {
	reconciler.mu.RLock()
	defer reconciler.mu.RUnlock()
	return reconciler.lease
}

func (reconciler *Reconciler) setLease(lease ManagerLease) {
	reconciler.mu.Lock()
	reconciler.lease = lease
	reconciler.mu.Unlock()
}

func (reconciler *Reconciler) setLeaseVersion(version int64) {
	if version <= 0 {
		return
	}
	reconciler.mu.Lock()
	reconciler.lease.Version = version
	reconciler.mu.Unlock()
}

func (reconciler *Reconciler) currentSettingsStateVersion() int64 {
	reconciler.mu.RLock()
	defer reconciler.mu.RUnlock()
	return reconciler.settingsStateVersion
}

func (reconciler *Reconciler) setSettingsStateVersion(version int64) {
	if version <= 0 {
		return
	}
	reconciler.mu.Lock()
	reconciler.settingsStateVersion = version
	reconciler.mu.Unlock()
}

func (reconciler *Reconciler) setWorkCancel(cancel context.CancelFunc) {
	reconciler.mu.Lock()
	reconciler.workCancel = cancel
	reconciler.mu.Unlock()
}

func (reconciler *Reconciler) cancelWork() {
	reconciler.mu.RLock()
	cancel := reconciler.workCancel
	reconciler.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// ReconcileOnce executes one externally visible convergence attempt. It is
// exported to provide a deterministic integration-test seam.
func (reconciler *Reconciler) ReconcileOnce(ctx context.Context) error {
	if reconciler == nil || ctx == nil {
		return errors.New("local model runtime reconcile context is invalid")
	}
	if _, err := reconciler.sweepExpiredOperations(ctx); err != nil {
		return fmt.Errorf("sweep local model preparation: %w", err)
	}
	snapshot, err := reconciler.store.ReadDemand(ctx)
	if err != nil {
		// An unreadable database is not evidence of empty demand.
		return fmt.Errorf("read local model runtime demand: %w", err)
	}
	reconciler.setSettingsStateVersion(snapshot.SettingsStateVersion)
	requirement, err := ParseRequirement(snapshot.Requirement.Models, snapshot.Requirement.Hash)
	if err != nil {
		return fmt.Errorf("validate local model runtime demand: %w", err)
	}
	if snapshot.Runtime.Mode != "" && snapshot.Runtime.Mode != RuntimeModeManaged {
		return ErrRuntimeModeMismatch
	}
	if !snapshot.MayStop && len(requirement.Models) == 0 {
		return errors.New("local model runtime demand projection is not safe to stop")
	}
	previousRuntime := snapshot.Runtime
	if !sameRequirement(snapshot.Runtime.Requirement, requirement) {
		lease := reconciler.currentLease()
		reconciler.writeMu.Lock()
		lease = reconciler.currentLease()
		published, publishErr := reconciler.store.PublishDemand(ctx, DemandCASCommand{
			OwnerID: reconciler.options.OwnerID, OwnerEpoch: lease.OwnerEpoch,
			ExpectedVersion: lease.Version, ExpectedRequirementVersion: snapshot.RequirementVersion,
			ExpectedSettingsStateVersion: snapshot.SettingsStateVersion,
			Requirement:                  requirement,
		})
		reconciler.writeMu.Unlock()
		if publishErr != nil {
			return fmt.Errorf("publish local model runtime demand: %w", publishErr)
		}
		snapshot.Runtime = published
		snapshot.RequirementVersion = published.RequirementVersion
		reconciler.setLeaseVersion(published.Version)
	}
	if len(requirement.Models) == 0 {
		return reconciler.reconcileStopped(ctx, snapshot)
	}
	return reconciler.reconcileReady(ctx, snapshot, requirement, previousRuntime.Requirement, previousRuntime.ReadyHash)
}

func (reconciler *Reconciler) reconcileStopped(ctx context.Context, snapshot DemandSnapshot) error {
	if !authoritativeEmptyDemand(snapshot) {
		return errors.New("local model runtime empty demand is not safe to stop")
	}
	if reconciler.child.Snapshot().State == StateStopped {
		_ = reconciler.child.SetInferenceReady(false)
		if snapshot.Runtime.Phase == RuntimePhaseStopped {
			return nil
		}
		if snapshot.Runtime.Phase != RuntimePhaseFailed && snapshot.Runtime.Phase != RuntimePhaseStopping {
			runtime, err := reconciler.advanceRuntime(ctx, snapshot.Runtime, RuntimePhaseStopping, Requirement{}, "", snapshot.Runtime.ChildEpoch, "", false)
			if err != nil {
				return err
			}
			snapshot.Runtime = runtime
		}
		return reconciler.transitionRuntime(ctx, snapshot.Runtime, RuntimePhaseStopped, Requirement{}, "", snapshot.Runtime.ChildEpoch, "", false)
	}
	runtime, err := reconciler.transitionRuntimeRecord(ctx, snapshot.Runtime, RuntimePhaseStopping, Requirement{}, "", persistedChildEpoch(snapshot.Runtime.ChildEpoch, reconciler.child.Snapshot()), "", false)
	if err != nil {
		return err
	}
	if err := reconciler.child.SetInferenceReady(false); err != nil && !errors.Is(err, ErrChildUnavailable) {
		return err
	}
	if err := reconciler.child.Stop(ctx); err != nil {
		return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_STOP_FAILED", true, err)
	}
	return reconciler.transitionRuntime(ctx, runtime, RuntimePhaseStopped, Requirement{}, "", runtime.ChildEpoch, "", false)
}

func authoritativeEmptyDemand(snapshot DemandSnapshot) bool {
	if len(snapshot.Requirement.Models) != 0 || snapshot.Requirement.Hash != "" || len(snapshot.Sources) != 0 || len(snapshot.Holds) != 0 || len(snapshot.Operations) != 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(snapshot.RolloutPhase)) {
	case "", "idle", "failed":
		return true
	default:
		return false
	}
}

func sameRequirement(left, right Requirement) bool {
	if left.Hash != right.Hash || len(left.Models) != len(right.Models) {
		return false
	}
	for i := range left.Models {
		if left.Models[i] != right.Models[i] {
			return false
		}
	}
	return true
}

func containsReadyRequirementModel(missing []ModelRef, ready Requirement) bool {
	if len(ready.Models) == 0 {
		return false
	}
	for _, absent := range missing {
		for _, required := range ready.Models {
			if sameModelRef(absent, required) {
				return true
			}
		}
	}
	return false
}

func runtimePhaseForStart(current RuntimePhase) RuntimePhase {
	switch current {
	case RuntimePhaseStarting, RuntimePhaseChecking, RuntimePhasePulling:
		return current
	default:
		return RuntimePhaseStarting
	}
}

func (reconciler *Reconciler) reconcileReady(ctx context.Context, snapshot DemandSnapshot, requirement, previousReadyRequirement Requirement, previousReadyHash string) error {
	currentChild := reconciler.child.Snapshot()
	retainExistingReady := currentChild.State == StateRunning && currentChild.Ready && previousReadyHash != "" &&
		snapshot.Runtime.Phase != RuntimePhaseStopped && snapshot.Runtime.Phase != RuntimePhaseStopping
	if retainExistingReady {
		if err := reconciler.ollama.Health(ctx); err == nil {
			_, missing, inspectErr := reconciler.inspectModels(ctx, requirement)
			if inspectErr == nil && len(missing) == 0 && snapshot.Runtime.Phase == RuntimePhaseReady &&
				snapshot.Runtime.ReadyHash == requirement.Hash && len(pendingOperations(snapshot.Operations)) == 0 {
				return nil
			}
			if inspectErr == nil && previousReadyHash == requirement.Hash {
				retainExistingReady = false
			} else if inspectErr == nil && !containsReadyRequirementModel(missing, previousReadyRequirement) {
				// A candidate/test model may be absent while the old verified
				// requirement remains available through the same child.
				retainExistingReady = true
			} else if inspectErr != nil {
				// A transient inventory failure is not evidence that the old
				// generation is unsafe; health still proves the child endpoint.
				retainExistingReady = true
			} else {
				retainExistingReady = false
			}
		} else {
			retainExistingReady = false
		}
	}
	if !retainExistingReady {
		if err := reconciler.child.SetInferenceReady(false); err != nil && !errors.Is(err, ErrChildUnavailable) {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	currentChild = reconciler.child.Snapshot()
	if currentChild.State == StateStopped {
		if snapshot.Runtime.ChildEpoch < 0 {
			return reconciler.failRuntime(ctx, snapshot.Runtime, "LOCAL_MODEL_RUNTIME_CHILD_EPOCH_INVALID", false,
				errors.New("local model runtime durable child epoch is negative"))
		}
		if err := reconciler.child.SetGenerationFloor(uint64(snapshot.Runtime.ChildEpoch)); err != nil {
			return reconciler.failRuntime(ctx, snapshot.Runtime, "LOCAL_MODEL_RUNTIME_START_FAILED", true, err)
		}
	}
	startPhase := runtimePhaseForStart(snapshot.Runtime.Phase)
	startEpoch := persistedChildEpoch(snapshot.Runtime.ChildEpoch, currentChild)
	if currentChild.State != StateRunning || startEpoch == 0 {
		startEpoch = snapshot.Runtime.ChildEpoch
	}
	runtime, err := reconciler.advanceRuntime(ctx, snapshot.Runtime, startPhase, requirement, "", startEpoch, "", false)
	if err != nil {
		return err
	}
	if err := reconciler.child.Ensure(ctx); err != nil {
		return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_START_FAILED", true, err)
	}
	child := reconciler.child.Snapshot()
	if child.State != StateRunning || child.Generation == 0 {
		return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_CHILD_UNAVAILABLE", true, ErrChildUnavailable)
	}
	if err := reconciler.waitHealthy(ctx); err != nil {
		return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_HEALTH_FAILED", true, err)
	}
	if runtime.Phase != RuntimePhaseChecking {
		runtime, err = reconciler.advanceRuntime(ctx, runtime, RuntimePhaseChecking, requirement, "", persistedChildEpoch(runtime.ChildEpoch, child), "", false)
	}
	if err != nil {
		return err
	}

	operations := pendingOperations(snapshot.Operations)
	resolved, missing, err := reconciler.inspectModels(ctx, requirement)
	if err != nil {
		return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_CHECK_FAILED", true, err)
	}
	if len(missing) > 0 {
		uncovered := modelsNotCoveredByOperations(missing, operations)
		if active, ok := activeRequirement(snapshot.Sources); ok && modelsBelongToRequirement(uncovered, active) {
			seeded, seedErr := reconciler.seedActiveRecovery(ctx, snapshot.SettingsStateVersion)
			if seedErr != nil {
				return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_ACTIVE_RECOVERY_SEED_FAILED", true, seedErr)
			}
			if seeded.TerminalAt != nil {
				code := seeded.ErrorCode
				if code == "" {
					code = "LOCAL_MODEL_RUNTIME_PREPARATION_OPERATION_REQUIRED"
				}
				return reconciler.failRuntime(ctx, runtime, code, seeded.Retryable,
					fmt.Errorf("active local model recovery is terminal: %s", seeded.Phase))
			}
			operations = pendingOperations(append(operations, seeded))
		}
		runtime, err = reconciler.advanceRuntime(ctx, runtime, RuntimePhasePulling, requirement, "", persistedChildEpoch(runtime.ChildEpoch, child), "", false)
		if err != nil {
			return err
		}
	}

	var operation *OperationRecord
	if len(operations) > 0 {
		claimed, claimErr := reconciler.claimOperation(ctx, operations[0])
		if claimErr != nil {
			return claimErr
		}
		operation = &claimed
		// A recovered queued/starting operation must pass through checking before
		// verification, even when the requested model was already present.
		if operation.Phase == OperationPhaseStarting {
			updated, phaseErr := reconciler.operationPhase(ctx, *operation, OperationPhaseChecking, nil, operation.CompletedBytes, operation.TotalBytes, operation.ProgressKnown)
			if phaseErr != nil {
				return phaseErr
			}
			operation = &updated
		}
	}
	if len(missing) > 0 && operation == nil {
		return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_PREPARATION_OPERATION_REQUIRED", false, ErrPreparationOperationRequired)
	}
	operationResolved := resolved
	if operation != nil {
		operationResolved = nil
		for _, model := range resolved {
			for _, required := range operation.Requirement.Models {
				if sameModelRef(model.Model, required) {
					operationResolved = append(operationResolved, model)
					break
				}
			}
		}
		missing = modelsInRequirement(missing, operation.Requirement)
	}
	resolved, err = reconciler.prepareMissing(ctx, operationResolved, missing, operation)
	if err != nil {
		if operation != nil && operation.TerminalAt == nil {
			_ = reconciler.completeFailed(ctx, *operation, err)
		}
		retryable := !errors.Is(err, ErrPreparationAttemptsExhausted) &&
			!errors.Is(err, context.DeadlineExceeded)
		return reconciler.failRuntime(ctx, runtime, preparationErrorCode(err), retryable, err)
	}

	if operation != nil {
		operationResolved, missing, err = reconciler.inspectModels(ctx, operation.Requirement)
		if err != nil || len(missing) != 0 {
			if err == nil {
				err = ErrOllamaModelMissing
			}
			_ = reconciler.completeFailed(ctx, *operation, err)
			return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_VERIFY_FAILED", true, err)
		}
		updated, progressErr := reconciler.operationPhase(ctx, *operation, OperationPhaseVerifying, operationResolved, operation.CompletedBytes, operation.TotalBytes, operation.ProgressKnown)
		if progressErr != nil {
			return progressErr
		}
		operation = &updated
		if completeErr := reconciler.completeReady(ctx, *operation, operationResolved); completeErr != nil {
			return completeErr
		}
	}
	resolved, missing, err = reconciler.inspectModels(ctx, requirement)
	if err != nil {
		return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_VERIFY_FAILED", true, err)
	}
	if len(missing) != 0 {
		remainingOperations := operations
		if operation != nil {
			remainingOperations = operations[1:]
		}
		if modelsCoveredByOperations(missing, remainingOperations) {
			return nil
		}
		return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_PREPARATION_OPERATION_REQUIRED", false, ErrPreparationOperationRequired)
	}
	runtime, err = reconciler.advanceRuntime(ctx, runtime, RuntimePhaseChecking, requirement, "", persistedChildEpoch(runtime.ChildEpoch, child), "", false)
	if err != nil {
		return err
	}
	runtime, err = reconciler.advanceRuntime(ctx, runtime, RuntimePhaseReady, requirement, requirement.Hash, persistedChildEpoch(runtime.ChildEpoch, child), "", false)
	if err != nil {
		return err
	}
	if err := reconciler.child.SetInferenceReady(true); err != nil {
		return reconciler.failRuntime(ctx, runtime, "LOCAL_MODEL_RUNTIME_CHILD_UNAVAILABLE", true, err)
	}
	return nil
}

func modelsInRequirement(models []ModelRef, requirement Requirement) []ModelRef {
	filtered := make([]ModelRef, 0, len(models))
	for _, model := range models {
		for _, required := range requirement.Models {
			if sameModelRef(model, required) {
				filtered = append(filtered, model)
				break
			}
		}
	}
	return filtered
}

func modelsCoveredByOperations(models []ModelRef, operations []OperationRecord) bool {
	for _, model := range models {
		covered := false
		for _, operation := range operations {
			if len(modelsInRequirement([]ModelRef{model}, operation.Requirement)) != 0 {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func modelsNotCoveredByOperations(models []ModelRef, operations []OperationRecord) []ModelRef {
	uncovered := make([]ModelRef, 0, len(models))
	for _, model := range models {
		if !modelsCoveredByOperations([]ModelRef{model}, operations) {
			uncovered = append(uncovered, model)
		}
	}
	return uncovered
}

func activeRequirement(sources []DemandSource) (Requirement, bool) {
	for _, source := range sources {
		if source.Kind == "active" && len(source.Requirement.Models) > 0 {
			return source.Requirement, true
		}
	}
	return Requirement{}, false
}

func modelsBelongToRequirement(models []ModelRef, requirement Requirement) bool {
	return len(models) > 0 && len(modelsInRequirement(models, requirement)) == len(models)
}

func (reconciler *Reconciler) seedActiveRecovery(ctx context.Context, settingsStateVersion int64) (OperationRecord, error) {
	reconciler.writeMu.Lock()
	defer reconciler.writeMu.Unlock()
	operation, err := reconciler.store.SeedActiveRecovery(ctx, ActiveRecoveryCommand{
		OwnerID: reconciler.options.OwnerID, OwnerEpoch: reconciler.currentLease().OwnerEpoch,
		ExpectedSettingsStateVersion: settingsStateVersion, LeaseDuration: reconciler.options.OperationLease,
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("seed active local model recovery: %w", err)
	}
	return operation, nil
}

func (reconciler *Reconciler) waitHealthy(ctx context.Context) error {
	deadline := time.NewTimer(reconciler.options.HealthWait)
	defer deadline.Stop()
	for {
		if err := reconciler.ollama.Health(ctx); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrOllamaUnavailable
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (reconciler *Reconciler) inspectModels(ctx context.Context, requirement Requirement) ([]ResolvedModel, []ModelRef, error) {
	tags, err := reconciler.ollama.Tags(ctx)
	if err != nil {
		return nil, nil, err
	}
	installed := make(map[ModelRef]OllamaModel, len(tags))
	for _, model := range tags {
		installed[model.Name] = model
		installed[withDefaultTag(model.Name)] = model
	}
	resolved := make([]ResolvedModel, 0, len(requirement.Models))
	missing := make([]ModelRef, 0)
	for _, model := range requirement.Models {
		manifest, ok := installed[model]
		if !ok {
			manifest, ok = installed[withDefaultTag(model)]
		}
		if !ok {
			missing = append(missing, model)
			continue
		}
		if _, err := reconciler.ollama.Show(ctx, model); err != nil {
			if errors.Is(err, ErrOllamaModelMissing) {
				missing = append(missing, model)
				continue
			}
			return nil, nil, err
		}
		resolved = append(resolved, resolvedManifest(manifest))
	}
	return resolved, missing, nil
}

func (reconciler *Reconciler) prepareMissing(ctx context.Context, resolved []ResolvedModel, missing []ModelRef, operation *OperationRecord) ([]ResolvedModel, error) {
	operationCtx, cancel := context.WithTimeout(ctx, reconciler.options.OperationTimeout)
	defer cancel()
	for _, model := range missing {
		var pullErr error
		for attempt := 0; attempt < reconciler.options.PullAttempts; attempt++ {
			tracker := newProgressTracker(reconciler.options.ProgressMinInterval)
			pullCtx := operationCtx
			var pullCancel context.CancelFunc
			if operation != nil {
				attemptResult, attemptErr := reconciler.beginPullAttempt(operationCtx, *operation)
				if attemptErr != nil {
					return nil, attemptErr
				}
				*operation = attemptResult.Operation
				if operation.Phase == OperationPhaseFailed {
					return nil, ErrPreparationAttemptsExhausted
				}
				if attemptResult.Remaining <= 0 {
					return nil, context.DeadlineExceeded
				}
				pullCtx, pullCancel = context.WithTimeout(operationCtx, attemptResult.Remaining)
				if operation.Kind == OperationKindActiveRecovery {
					pullCtx, pullCancel = reconciler.watchActiveRecovery(pullCtx, pullCancel, operation.OperationID)
				}
			}
			_, pullErr = reconciler.ollama.Pull(pullCtx, model, func(progress PullProgress) error {
				if operation == nil || !tracker.shouldPersist(progress) {
					return nil
				}
				updated, err := reconciler.persistPullProgress(pullCtx, *operation, resolved, progress)
				if err == nil {
					*operation = updated
				}
				return err
			})
			if pullCancel != nil {
				pullCancel()
			}
			if pullErr == nil {
				break
			}
			if operationCtx.Err() != nil {
				return nil, operationCtx.Err()
			}
			if operation != nil && operation.AttemptNo >= reconciler.options.PullAttempts {
				return nil, ErrPreparationAttemptsExhausted
			}
			// Ollama can resume an interrupted pull from its durable model store;
			// retain the same operation identity while retrying the bounded attempt.
		}
		if pullErr != nil {
			return nil, pullErr
		}
		manifests, stillMissing, err := reconciler.inspectModels(operationCtx, Requirement{Models: []ModelRef{model}, Hash: mustRequirementHash(model)})
		if err != nil {
			return nil, err
		}
		if len(stillMissing) != 0 || len(manifests) != 1 {
			return nil, ErrOllamaModelMissing
		}
		resolved = append(resolved, manifests[0])
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Model < resolved[j].Model })
	return resolved, nil
}

func (reconciler *Reconciler) watchActiveRecovery(
	ctx context.Context,
	cancel context.CancelFunc,
	operationID foundation.ID,
) (context.Context, context.CancelFunc) {
	watchCtx, watchCancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(reconciler.options.RecoveryCheckEvery)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
			}
			lease := reconciler.currentLease()
			checkCtx, checkCancel := context.WithTimeout(watchCtx, reconciler.options.RecoveryCheckLimit)
			current, err := reconciler.store.ActiveRecoveryCurrent(checkCtx, ActiveRecoveryCheckCommand{
				OperationID: operationID, OwnerID: reconciler.options.OwnerID, OwnerEpoch: lease.OwnerEpoch,
			})
			checkCancel()
			if err != nil {
				if watchCtx.Err() != nil {
					return
				}
				reconciler.report(fmt.Errorf("check active recovery currency: %w", err))
				continue
			}
			if !current {
				cancel()
				return
			}
		}
	}()
	return watchCtx, func() {
		watchCancel()
		cancel()
	}
}

func mustRequirementHash(model ModelRef) string {
	requirement, _ := NewRequirement([]ModelRef{model})
	return requirement.Hash
}

func resolvedManifest(model OllamaModel) ResolvedModel {
	var total *int64
	if model.Bytes > 0 {
		value := model.Bytes
		total = &value
	}
	return ResolvedModel{Model: model.Name, Digest: model.Digest, Bytes: model.Bytes, TotalBytes: total}
}

func pendingOperations(operations []OperationRecord) []OperationRecord {
	pending := make([]OperationRecord, 0, len(operations))
	for _, operation := range operations {
		if operation.Phase == OperationPhaseReady || operation.Phase == OperationPhaseProbing || terminalOperationPhase(operation.Phase) {
			continue
		}
		pending = append(pending, operation)
	}
	sort.SliceStable(pending, func(i, j int) bool {
		left, right := operationPriority(pending[i].Kind), operationPriority(pending[j].Kind)
		if left != right {
			return left < right
		}
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})
	return pending
}

func operationPriority(kind OperationKind) int {
	switch kind {
	case OperationKindActiveRecovery:
		return 0
	case OperationKindActivation:
		return 1
	case OperationKindTest:
		return 2
	default:
		return 3
	}
}

func (reconciler *Reconciler) claimOperation(ctx context.Context, operation OperationRecord) (OperationRecord, error) {
	requirement := operation.Requirement
	if len(requirement.Models) == 0 && len(operation.ResolvedModels) > 0 {
		models := make([]ModelRef, 0, len(operation.ResolvedModels))
		for _, resolved := range operation.ResolvedModels {
			models = append(models, resolved.Model)
		}
		var requirementErr error
		requirement, requirementErr = NewRequirement(models)
		if requirementErr != nil {
			return OperationRecord{}, requirementErr
		}
	}
	reconciler.writeMu.Lock()
	claimed, err := reconciler.store.ClaimOperation(ctx, OperationClaimCommand{
		OperationID: operation.OperationID, Kind: operation.Kind, IdempotencyKey: operation.IdempotencyKey,
		RequestHash: operation.RequestHash, TargetRevision: operation.TargetRevision, RolloutID: operation.RolloutID,
		Requirement: requirement,
		OwnerID:     reconciler.options.OwnerID, OwnerEpoch: reconciler.currentLease().OwnerEpoch,
		LeaseDuration: reconciler.options.OperationLease,
	})
	reconciler.writeMu.Unlock()
	if err != nil {
		return OperationRecord{}, fmt.Errorf("claim local model preparation: %w", err)
	}
	if claimed.Phase == OperationPhaseQueued {
		return reconciler.operationPhase(ctx, claimed, OperationPhaseStarting, nil, 0, nil, false)
	}
	return claimed, nil
}

func (reconciler *Reconciler) beginPullAttempt(ctx context.Context, operation OperationRecord) (PullAttemptResult, error) {
	reconciler.writeMu.Lock()
	defer reconciler.writeMu.Unlock()
	started, err := reconciler.store.BeginPullAttempt(ctx, PullAttemptCommand{
		OperationID: operation.OperationID, OwnerID: reconciler.options.OwnerID,
		OwnerEpoch: reconciler.currentLease().OwnerEpoch, ExpectedVersion: operation.Version,
		LeaseDuration: reconciler.options.OperationLease,
	})
	if err != nil {
		return PullAttemptResult{}, fmt.Errorf("begin local model pull attempt: %w", err)
	}
	return started, nil
}

func (reconciler *Reconciler) sweepExpiredOperations(ctx context.Context) (int64, error) {
	reconciler.writeMu.Lock()
	defer reconciler.writeMu.Unlock()
	lease := reconciler.currentLease()
	return reconciler.store.SweepExpiredOperations(ctx, OperationExpirySweepCommand{
		OwnerID: reconciler.options.OwnerID, OwnerEpoch: lease.OwnerEpoch,
	})
}

func (reconciler *Reconciler) persistPullProgress(ctx context.Context, operation OperationRecord, resolved []ResolvedModel, progress PullProgress) (OperationRecord, error) {
	completed := operation.CompletedBytes
	if progress.Completed > completed {
		completed = progress.Completed
	}
	total := progress.Total
	if total == nil {
		total = operation.TotalBytes
	}
	return reconciler.operationPhase(ctx, operation, OperationPhasePulling, resolved, completed, total, total != nil)
}

func (reconciler *Reconciler) operationPhase(ctx context.Context, operation OperationRecord, next OperationPhase, resolved []ResolvedModel, completed int64, total *int64, known bool) (OperationRecord, error) {
	reconciler.writeMu.Lock()
	defer reconciler.writeMu.Unlock()
	updated, err := reconciler.store.RecordOperationProgress(ctx, OperationProgressCommand{
		OperationID: operation.OperationID, OwnerID: reconciler.options.OwnerID,
		OwnerEpoch: reconciler.currentLease().OwnerEpoch, ExpectedVersion: operation.Version,
		ExpectedPhase: operation.Phase, NextPhase: next, CompletedModels: completedModels(resolved),
		CompletedBytes: completed, TotalBytes: cloneInt64(total), ProgressKnown: known,
		ResolvedModels: append([]ResolvedModel(nil), resolved...), LeaseDuration: reconciler.options.OperationLease,
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("record local model preparation progress: %w", err)
	}
	return updated, nil
}

func (reconciler *Reconciler) completeReady(ctx context.Context, operation OperationRecord, resolved []ResolvedModel) error {
	reconciler.writeMu.Lock()
	defer reconciler.writeMu.Unlock()
	completed, err := reconciler.store.CompleteOperation(ctx, OperationTerminalCommand{
		OperationID: operation.OperationID, OwnerID: reconciler.options.OwnerID,
		OwnerEpoch: reconciler.currentLease().OwnerEpoch, ExpectedVersion: operation.Version,
		Phase: OperationPhaseReady, CompletedModels: completedModels(resolved),
		CompletedBytes: operation.CompletedBytes, TotalBytes: cloneInt64(operation.TotalBytes),
	})
	if err != nil {
		return err
	}
	if completed.Phase == OperationPhaseFailed && completed.ErrorCode == "LOCAL_MODEL_RUNTIME_PREPARATION_TIMEOUT" {
		return context.DeadlineExceeded
	}
	return nil
}

func (reconciler *Reconciler) completeFailed(ctx context.Context, operation OperationRecord, cause error) error {
	reconciler.writeMu.Lock()
	defer reconciler.writeMu.Unlock()
	_, err := reconciler.store.CompleteOperation(ctx, OperationTerminalCommand{
		OperationID: operation.OperationID, OwnerID: reconciler.options.OwnerID,
		OwnerEpoch: reconciler.currentLease().OwnerEpoch, ExpectedVersion: operation.Version,
		Phase: OperationPhaseFailed, ErrorCode: preparationErrorCode(cause),
		Retryable:       !errors.Is(cause, ErrPreparationAttemptsExhausted) && !errors.Is(cause, context.DeadlineExceeded),
		CompletedModels: append([]ModelRef(nil), operation.CompletedModels...),
		CompletedBytes:  operation.CompletedBytes, TotalBytes: cloneInt64(operation.TotalBytes),
	})
	return err
}

func completedModels(resolved []ResolvedModel) []ModelRef {
	models := make([]ModelRef, 0, len(resolved))
	for _, model := range resolved {
		models = append(models, model.Model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i] < models[j] })
	return models
}

func (reconciler *Reconciler) transitionRuntime(ctx context.Context, current RuntimeRecord, next RuntimePhase, requirement Requirement, readyHash string, epoch int64, errorCode string, retryable bool) error {
	_, err := reconciler.advanceRuntime(ctx, current, next, requirement, readyHash, epoch, errorCode, retryable)
	return err
}

func (reconciler *Reconciler) advanceRuntime(ctx context.Context, current RuntimeRecord, next RuntimePhase, requirement Requirement, readyHash string, epoch int64, errorCode string, retryable bool) (RuntimeRecord, error) {
	if current.Version <= 0 {
		lease := reconciler.currentLease()
		current.Version = lease.Version
	}
	return reconciler.transitionRuntimeRecord(ctx, current, next, requirement, readyHash, epoch, errorCode, retryable)
}

func (reconciler *Reconciler) transitionRuntimeRecord(ctx context.Context, current RuntimeRecord, next RuntimePhase, requirement Requirement, readyHash string, epoch int64, errorCode string, retryable bool) (RuntimeRecord, error) {
	if current.Phase == next && current.Requirement.Hash == requirement.Hash && current.ReadyHash == readyHash && current.ChildEpoch == epoch && current.ErrorCode == errorCode && current.Retryable == retryable {
		return current, nil
	}
	lease := reconciler.currentLease()
	reconciler.writeMu.Lock()
	defer reconciler.writeMu.Unlock()
	lease = reconciler.currentLease()
	updated, err := reconciler.store.CompareAndSetRuntimePhase(ctx, RuntimePhaseCommand{
		OwnerID: reconciler.options.OwnerID, OwnerEpoch: lease.OwnerEpoch,
		ExpectedVersion: lease.Version, ExpectedPhase: current.Phase, NextPhase: next,
		ExpectedSettingsStateVersion: reconciler.currentSettingsStateVersion(),
		RequirementHash:              requirement.Hash, ReadyHash: readyHash, ChildEpoch: epoch,
		ErrorCode: errorCode, Retryable: retryable,
	})
	if err != nil {
		return RuntimeRecord{}, fmt.Errorf("transition local model runtime phase: %w", err)
	}
	reconciler.setLeaseVersion(updated.Version)
	return updated, nil
}

func (reconciler *Reconciler) failRuntime(ctx context.Context, current RuntimeRecord, code string, retryable bool, cause error) error {
	if current.Phase != RuntimePhaseFailed {
		_, _ = reconciler.transitionRuntimeRecord(ctx, current, RuntimePhaseFailed, current.Requirement, "", current.ChildEpoch, code, retryable)
	}
	return cause
}

func childEpoch(snapshot Snapshot) int64 {
	if snapshot.Generation > uint64(^uint64(0)>>1) {
		return 0
	}
	return int64(snapshot.Generation)
}

func persistedChildEpoch(current int64, snapshot Snapshot) int64 {
	observed := childEpoch(snapshot)
	if observed > current {
		return observed
	}
	return current
}

func preparationErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrPreparationAttemptsExhausted):
		return "LOCAL_MODEL_RUNTIME_PULL_ATTEMPTS_EXHAUSTED"
	case errors.Is(err, ErrOllamaPullStalled):
		return "LOCAL_MODEL_RUNTIME_PULL_STALLED"
	case errors.Is(err, context.DeadlineExceeded):
		return "LOCAL_MODEL_RUNTIME_PREPARATION_TIMEOUT"
	case errors.Is(err, ErrOllamaRequestLimit), errors.Is(err, ErrOllamaResponse):
		return "LOCAL_MODEL_RUNTIME_RESPONSE_INVALID"
	default:
		return "LOCAL_MODEL_RUNTIME_PREPARATION_FAILED"
	}
}

func (reconciler *Reconciler) report(err error) {
	if err != nil && reconciler.options.OnError != nil {
		reconciler.options.OnError(err)
	}
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func publishLatest(channel chan bool, value bool) {
	select {
	case channel <- value:
	default:
		select {
		case <-channel:
		default:
		}
		channel <- value
	}
}

func equalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type progressTracker struct {
	lastPersisted time.Time
	completed     int64
	total         *int64
	minimum       time.Duration
}

func newProgressTracker(minimum time.Duration) *progressTracker {
	return &progressTracker{minimum: minimum}
}

func (tracker *progressTracker) shouldPersist(progress PullProgress) bool {
	now := time.Now()
	changedTotal := !equalInt64(tracker.total, progress.Total)
	terminal := strings.EqualFold(strings.TrimSpace(progress.Status), "success")
	if !terminal && !changedTotal && progress.Completed <= tracker.completed && now.Sub(tracker.lastPersisted) < tracker.minimum {
		return false
	}
	if !terminal && !tracker.lastPersisted.IsZero() && now.Sub(tracker.lastPersisted) < tracker.minimum && progress.Completed <= tracker.completed {
		return false
	}
	if progress.Completed > tracker.completed {
		tracker.completed = progress.Completed
	}
	tracker.total = cloneInt64(progress.Total)
	tracker.lastPersisted = now
	return true
}
