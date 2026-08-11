package runtime

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const (
	defaultHotRuntimePollInterval      = 250 * time.Millisecond
	defaultHotRuntimeHeartbeatInterval = 5 * time.Second
	defaultActivationLeaseDuration     = 30 * time.Second
	defaultActivationRenewInterval     = 10 * time.Second
)

// RuntimeOwnershipControl owns the serving role row used by activation CAS checks.
type RuntimeOwnershipControl interface {
	RegisterRuntime(context.Context, modelsettingsapplication.RuntimeRegistration) (modelsettingsdomain.RuntimeRecord, error)
	HeartbeatRuntime(context.Context, modelsettingsapplication.RuntimeHeartbeat) (modelsettingsdomain.RuntimeRecord, error)
	RestoreRuntimeAvailability(context.Context, modelsettingsapplication.RestoreRuntimeAvailabilityCommand) (modelsettingsdomain.RuntimeRecord, error)
}

// HotRuntimeControllerOptions binds one process host to the durable activation protocol.
type HotRuntimeControllerOptions[T any] struct {
	Host       *RuntimeHost[T]
	Activation modelsettingsapplication.ActivationControl
	Revisions  modelsettingsapplication.RevisionLoader
	Runtime    RuntimeOwnershipControl
	// InitialPhase preserves a degraded bootstrap projection until the exact
	// serving revision has been rebuilt and its availability restored. The zero
	// value keeps the normal active startup behavior.
	InitialPhase modelsettingsdomain.RuntimePhase

	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	StaleAfter        time.Duration
}

// HotRuntimeController owns one role's serving heartbeat and participant lifecycle.
// Global phase advancement remains the API ActivationCoordinator's responsibility.
type HotRuntimeController[T any] struct {
	host       *RuntimeHost[T]
	activation modelsettingsapplication.ActivationControl
	revisions  modelsettingsapplication.RevisionLoader
	runtime    RuntimeOwnershipControl

	role         modelsettingsdomain.RuntimeRole
	instanceID   foundation.ID
	runtimePhase modelsettingsdomain.RuntimePhase
	poll         time.Duration
	heartbeat    time.Duration
	staleAfter   time.Duration

	participantMu sync.Mutex
	participant   *modelsettingsdomain.ParticipantRecord
	active        chan struct{}
	activeOnce    sync.Once
}

// NewHotRuntimeController creates one managed role watcher. Runtime ownership
// may be supplied explicitly or by the Activation/Revisions implementation.
func NewHotRuntimeController[T any](options HotRuntimeControllerOptions[T]) (*HotRuntimeController[T], error) {
	if options.Host == nil || nilRuntimeDependency(options.Activation) || nilRuntimeDependency(options.Revisions) {
		return nil, runtimeNotReadyError(errors.New("hot runtime controller dependency is unavailable"))
	}
	if options.Runtime == nil {
		if runtimeControl, ok := options.Activation.(RuntimeOwnershipControl); ok && !nilRuntimeDependency(runtimeControl) {
			options.Runtime = runtimeControl
		} else if runtimeControl, ok := options.Revisions.(RuntimeOwnershipControl); ok && !nilRuntimeDependency(runtimeControl) {
			options.Runtime = runtimeControl
		}
	}
	if nilRuntimeDependency(options.Runtime) {
		return nil, runtimeNotReadyError(errors.New("hot runtime ownership control is unavailable"))
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultHotRuntimePollInterval
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = defaultHotRuntimeHeartbeatInterval
	}
	if options.StaleAfter == 0 {
		options.StaleAfter = modelsettingsapplication.DefaultRuntimeFreshWithin
	}
	if options.InitialPhase == "" {
		options.InitialPhase = modelsettingsdomain.RuntimePhaseActive
	}
	if options.InitialPhase != modelsettingsdomain.RuntimePhaseActive &&
		options.InitialPhase != modelsettingsdomain.RuntimePhaseUnavailable {
		return nil, runtimeBindingError(errors.New("hot runtime initial phase is invalid"))
	}
	if options.PollInterval <= 0 || options.PollInterval > time.Minute ||
		options.HeartbeatInterval <= 0 || options.HeartbeatInterval > time.Minute ||
		options.StaleAfter < time.Second || options.StaleAfter > 5*time.Minute ||
		options.StaleAfter%time.Microsecond != 0 || options.HeartbeatInterval >= options.StaleAfter {
		return nil, runtimeBindingError(errors.New("hot runtime controller timing policy is invalid"))
	}
	binding, ok := options.Host.activeBinding()
	if !ok || binding.Mode != RuntimeModeManaged || !modelsettingsdomain.ValidRuntimeRole(binding.Role) ||
		!validRuntimeID(binding.InstanceID) {
		return nil, runtimeBindingError(errors.New("hot runtime host identity is invalid"))
	}
	return &HotRuntimeController[T]{
		host: options.Host, activation: options.Activation, revisions: options.Revisions, runtime: options.Runtime,
		role: binding.Role, instanceID: binding.InstanceID, runtimePhase: options.InitialPhase, poll: options.PollInterval,
		heartbeat: options.HeartbeatInterval, staleAfter: options.StaleAfter, active: make(chan struct{}),
	}, nil
}

// Active closes after the serving runtime row has been registered successfully.
func (controller *HotRuntimeController[T]) Active() <-chan struct{} {
	if controller == nil || controller.active == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return controller.active
}

// Run registers serving ownership, reconciles local generations, and keeps both
// the runtime and participant facts fresh until process cancellation.
func (controller *HotRuntimeController[T]) Run(ctx context.Context) error {
	if controller == nil || controller.host == nil {
		return runtimeNotReadyError(errors.New("hot runtime controller is unavailable"))
	}
	if ctx == nil {
		return runtimeBindingError(errors.New("hot runtime controller context is nil"))
	}
	defer controller.host.Close()
	binding, ok := controller.host.activeBinding()
	if !ok {
		return runtimeNotReadyError(errors.New("hot runtime active generation is unavailable"))
	}
	registered, err := controller.runtime.RegisterRuntime(ctx, modelsettingsapplication.RuntimeRegistration{
		Role: controller.role, InstanceID: controller.instanceID, AppliedRevision: binding.Revision,
		Phase: controller.runtimePhase, StaleAfter: controller.staleAfter,
	})
	if err != nil {
		return err
	}
	controller.runtimePhase = registered.Phase

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Do not expose readiness until recovery has installed the durable
	// admission state. In particular, an activating restart must arm its
	// target gate before API or River consumers can begin new work.
	if err := controller.reconcile(runCtx); err != nil {
		if runCtx.Err() != nil {
			return nil
		}
		return err
	}
	if runCtx.Err() != nil {
		return nil
	}
	controller.activeOnce.Do(func() { close(controller.active) })

	heartbeatErrors := make(chan error, 1)
	go controller.runHeartbeats(runCtx, heartbeatErrors)

	ticker := time.NewTicker(controller.poll)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return nil
		case err := <-heartbeatErrors:
			if err != nil {
				return err
			}
		case <-ticker.C:
			if err := controller.reconcile(runCtx); err != nil {
				if runCtx.Err() != nil {
					return nil
				}
				if hotRuntimeFatal(err) {
					return err
				}
			}
		}
	}
}

func (controller *HotRuntimeController[T]) runHeartbeats(ctx context.Context, failures chan<- error) {
	ticker := time.NewTicker(controller.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := controller.runtime.HeartbeatRuntime(ctx, modelsettingsapplication.RuntimeHeartbeat{
			Role: controller.role, InstanceID: controller.instanceID,
		}); err != nil {
			if hotRuntimeFatal(err) {
				sendHotRuntimeFailure(failures, err)
				return
			}
		}
		if err := controller.heartbeatParticipant(ctx); err != nil && hotRuntimeFatal(err) {
			sendHotRuntimeFailure(failures, err)
			return
		}
	}
}

func sendHotRuntimeFailure(failures chan<- error, err error) {
	select {
	case failures <- err:
	default:
	}
}

func (controller *HotRuntimeController[T]) reconcile(ctx context.Context) error {
	snapshot, err := controller.revisions.Snapshot(ctx)
	if err != nil {
		return err
	}
	state := snapshot.Rollout
	switch state.Phase {
	case modelsettingsdomain.RolloutPhaseIdle:
		return controller.reconcileIdle(ctx, snapshot.ActiveRevision)
	case modelsettingsdomain.RolloutPhaseFailed:
		return controller.reconcileFailed(ctx, snapshot.ActiveRevision, state)
	case modelsettingsdomain.RolloutPhasePreparing:
		return controller.reconcilePreparing(ctx, state)
	case modelsettingsdomain.RolloutPhaseArming:
		return controller.reconcileArming(ctx, state)
	case modelsettingsdomain.RolloutPhaseActivating:
		return controller.reconcileActivating(ctx, state)
	default:
		return foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeCorrupt, false,
			errors.New("hot runtime observed an unsupported activation phase"))
	}
}

func (controller *HotRuntimeController[T]) reconcileIdle(ctx context.Context, activeRevision int64) error {
	binding, ok := controller.host.activeBinding()
	if !ok || binding.Revision != activeRevision {
		return runtimeNotReadyError(errors.New("hot runtime active revision does not match durable active"))
	}
	if controller.runtimePhase == modelsettingsdomain.RuntimePhaseUnavailable {
		if activeRevision == 0 {
			controller.clearParticipant()
			return nil
		}
		if err := controller.restoreRuntimeAvailability(ctx, activeRevision, false); err != nil {
			return err
		}
	}
	if err := controller.host.reopen(activeRevision); err != nil && controller.host.gateIsClosed() {
		return err
	}
	controller.clearParticipant()
	return nil
}

func (controller *HotRuntimeController[T]) reconcileFailed(
	ctx context.Context,
	activeRevision int64,
	state modelsettingsdomain.RolloutState,
) error {
	if state.TargetRevision > 0 {
		if err := controller.host.abort(state.TargetRevision); err != nil {
			return err
		}
	}
	controller.clearParticipant()
	return controller.reconcileIdle(ctx, activeRevision)
}

func (controller *HotRuntimeController[T]) reconcilePreparing(ctx context.Context, state modelsettingsdomain.RolloutState) error {
	if err := controller.restoreRuntimeAvailability(ctx, state.PreviousActiveRevision, false); err != nil {
		return err
	}
	record, err := controller.ensureParticipant(ctx, state, modelsettingsdomain.ParticipantPhasePreparing)
	if err != nil {
		return err
	}
	if terminalParticipant(record.Phase) {
		_ = controller.host.abort(state.TargetRevision)
		return nil
	}
	if err := controller.host.prepare(ctx, state.TargetRevision); err != nil {
		return controller.failPreparation(ctx, state, record, err)
	}
	if record.Phase == modelsettingsdomain.ParticipantPhasePreparing {
		_, err = controller.transitionParticipant(ctx, record, modelsettingsdomain.ParticipantPhasePrepared, "", false)
	}
	return err
}

func (controller *HotRuntimeController[T]) reconcileArming(ctx context.Context, state modelsettingsdomain.RolloutState) error {
	if err := controller.restoreRuntimeAvailability(ctx, state.PreviousActiveRevision, true); err != nil {
		return err
	}
	record, err := controller.ensureParticipant(ctx, state, modelsettingsdomain.ParticipantPhasePreparing)
	if err != nil {
		return err
	}
	if terminalParticipant(record.Phase) {
		_ = controller.host.abort(state.TargetRevision)
		return nil
	}
	if err := controller.host.prepare(ctx, state.TargetRevision); err != nil {
		_ = controller.host.reopen(state.PreviousActiveRevision)
		return controller.failPreparation(ctx, state, record, err)
	}
	if record.Phase == modelsettingsdomain.ParticipantPhasePreparing {
		record, err = controller.transitionParticipant(ctx, record, modelsettingsdomain.ParticipantPhasePrepared, "", false)
		if err != nil {
			return err
		}
	}
	if record.Phase == modelsettingsdomain.ParticipantPhasePrepared {
		if err := controller.armPreparedTarget(state.PreviousActiveRevision, state.TargetRevision); err != nil {
			return controller.failPreparation(ctx, state, record, err)
		}
		_, err = controller.transitionParticipant(ctx, record, modelsettingsdomain.ParticipantPhaseArmed, "", false)
		return err
	}
	if record.Phase == modelsettingsdomain.ParticipantPhaseArmed {
		return controller.armPreparedTarget(state.PreviousActiveRevision, state.TargetRevision)
	}
	return nil
}

func (controller *HotRuntimeController[T]) reconcileActivating(ctx context.Context, state modelsettingsdomain.RolloutState) error {
	binding, ok := controller.host.activeBinding()
	if !ok {
		return runtimeNotReadyError(errors.New("hot runtime active generation is unavailable after commit"))
	}
	if binding.Revision == state.TargetRevision {
		if err := controller.restoreRuntimeAvailability(ctx, state.TargetRevision, true); err != nil {
			return err
		}
		if err := controller.host.arm(state.TargetRevision); err != nil {
			return err
		}
	}
	record, err := controller.ensureParticipant(ctx, state, modelsettingsdomain.ParticipantPhaseActivated)
	if err != nil {
		return err
	}
	if record.Phase == modelsettingsdomain.ParticipantPhaseActivated {
		return nil
	}
	if record.Phase != modelsettingsdomain.ParticipantPhaseArmed {
		return runtimeLifecycleConflict(errors.New("post-commit participant is not armed"))
	}
	if binding.Revision != state.TargetRevision {
		if err := controller.host.prepare(ctx, state.TargetRevision); err != nil {
			return err
		}
		if err := controller.host.arm(state.TargetRevision); err != nil {
			return err
		}
		if err := controller.host.activate(state.TargetRevision); err != nil {
			return err
		}
	} else if err := controller.host.activate(state.TargetRevision); err != nil {
		return err
	}
	_, err = controller.acknowledgeParticipant(ctx, state, record)
	return err
}

func (controller *HotRuntimeController[T]) restoreRuntimeAvailability(
	ctx context.Context,
	revision int64,
	keepGated bool,
) error {
	if controller.runtimePhase != modelsettingsdomain.RuntimePhaseUnavailable {
		return nil
	}
	if revision <= 0 {
		return runtimeRevisionError(errors.New("unavailable canonical revision cannot be rebuilt"))
	}
	binding, ok := controller.host.activeBinding()
	if !ok || binding.Revision != revision {
		return runtimeNotReadyError(errors.New("unavailable runtime revision does not match durable serving revision"))
	}
	if err := controller.host.prepare(ctx, revision); err != nil {
		return err
	}
	if err := controller.host.arm(revision); err != nil {
		return err
	}
	if err := controller.host.activate(revision); err != nil {
		return err
	}
	record, err := controller.runtime.RestoreRuntimeAvailability(ctx, modelsettingsapplication.RestoreRuntimeAvailabilityCommand{
		Role: controller.role, InstanceID: controller.instanceID, AppliedRevision: revision,
	})
	if err != nil {
		return err
	}
	if record.Role != controller.role || record.InstanceID != controller.instanceID || record.AppliedRevision != revision ||
		record.RolloutID != nil || record.Phase != modelsettingsdomain.RuntimePhaseActive {
		return foundation.NewError(
			foundation.ErrorConsistencyViolation,
			modelsettingsdomain.ErrorCodeCorrupt,
			false,
			errors.New("restored runtime availability does not match local serving generation"),
		)
	}
	controller.runtimePhase = modelsettingsdomain.RuntimePhaseActive
	if keepGated {
		return nil
	}
	return controller.host.reopen(revision)
}

func (controller *HotRuntimeController[T]) armPreparedTarget(servingRevision, targetRevision int64) error {
	if err := controller.host.arm(targetRevision); err == nil {
		return nil
	}
	return controller.host.retargetClosedGate(servingRevision, targetRevision)
}

// retargetClosedGate preserves an arming fence when a degraded serving
// generation had to be repaired before its already-prepared target can be armed.
func (host *RuntimeHost[T]) retargetClosedGate(servingRevision, targetRevision int64) error {
	if host == nil {
		return runtimeNotReadyError(errors.New("runtime host is unavailable"))
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed {
		return runtimeNotReadyError(errors.New("runtime host is closed"))
	}
	if !host.gateClosed || host.gateRevision != servingRevision || host.active == nil || !host.active.ready ||
		host.active.binding.Revision != servingRevision || host.candidate == nil ||
		host.candidate.generation.binding.Revision != targetRevision {
		return runtimeLifecycleConflict(errors.New("runtime acquisition gate cannot move to the prepared target"))
	}
	host.gateRevision = targetRevision
	return nil
}

func (controller *HotRuntimeController[T]) ensureParticipant(
	ctx context.Context,
	state modelsettingsdomain.RolloutState,
	initial modelsettingsdomain.ParticipantPhase,
) (modelsettingsdomain.ParticipantRecord, error) {
	if !validRuntimeID(state.ID) || state.TargetRevision <= 0 {
		return modelsettingsdomain.ParticipantRecord{}, foundation.NewError(
			foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeCorrupt, false,
			errors.New("hot runtime activation binding is invalid"),
		)
	}
	controller.participantMu.Lock()
	defer controller.participantMu.Unlock()
	if controller.participant != nil && controller.participant.RolloutID == state.ID &&
		controller.participant.TargetRevision == state.TargetRevision {
		return *controller.participant, nil
	}
	record, err := controller.activation.RegisterParticipant(ctx, modelsettingsapplication.ParticipantRegistration{
		RolloutID: state.ID, Role: controller.role, InstanceID: controller.instanceID,
		TargetRevision: state.TargetRevision, InitialPhase: initial, StaleAfter: controller.staleAfter,
	})
	if err != nil {
		return modelsettingsdomain.ParticipantRecord{}, err
	}
	controller.participant = cloneParticipant(record)
	return record, nil
}

func (controller *HotRuntimeController[T]) heartbeatParticipant(ctx context.Context) error {
	controller.participantMu.Lock()
	defer controller.participantMu.Unlock()
	if controller.participant == nil || terminalParticipant(controller.participant.Phase) {
		return nil
	}
	record, err := controller.activation.HeartbeatParticipant(ctx, modelsettingsapplication.ParticipantHeartbeat{
		RolloutID: controller.participant.RolloutID, Role: controller.role, InstanceID: controller.instanceID,
		TargetRevision: controller.participant.TargetRevision, ExpectedPhase: controller.participant.Phase,
		ExpectedVersion: controller.participant.Version,
	})
	if err != nil {
		return err
	}
	controller.participant = cloneParticipant(record)
	return nil
}

func (controller *HotRuntimeController[T]) transitionParticipant(
	ctx context.Context,
	record modelsettingsdomain.ParticipantRecord,
	next modelsettingsdomain.ParticipantPhase,
	errorCode string,
	retryable bool,
) (modelsettingsdomain.ParticipantRecord, error) {
	controller.participantMu.Lock()
	defer controller.participantMu.Unlock()
	current := controller.participant
	if current == nil || current.RolloutID != record.RolloutID || current.TargetRevision != record.TargetRevision {
		return modelsettingsdomain.ParticipantRecord{}, runtimeLifecycleConflict(errors.New("hot runtime participant binding changed"))
	}
	updated, err := controller.activation.TransitionParticipant(ctx, modelsettingsapplication.ParticipantTransitionCommand{
		RolloutID: current.RolloutID, Role: controller.role, InstanceID: controller.instanceID,
		TargetRevision: current.TargetRevision, ExpectedPhase: current.Phase, ExpectedVersion: current.Version,
		NextPhase: next, ErrorCode: errorCode, ErrorRetryable: retryable,
	})
	if err != nil {
		return modelsettingsdomain.ParticipantRecord{}, err
	}
	controller.participant = cloneParticipant(updated)
	return updated, nil
}

func (controller *HotRuntimeController[T]) acknowledgeParticipant(
	ctx context.Context,
	state modelsettingsdomain.RolloutState,
	record modelsettingsdomain.ParticipantRecord,
) (modelsettingsdomain.ParticipantRecord, error) {
	controller.participantMu.Lock()
	defer controller.participantMu.Unlock()
	current := controller.participant
	if current == nil || current.RolloutID != record.RolloutID || current.TargetRevision != record.TargetRevision {
		return modelsettingsdomain.ParticipantRecord{}, runtimeLifecycleConflict(errors.New("hot runtime participant binding changed"))
	}
	updated, err := controller.activation.AcknowledgeActivation(ctx, modelsettingsapplication.ActivationAcknowledgement{
		RolloutID: state.ID, Role: controller.role, InstanceID: controller.instanceID,
		TargetRevision: state.TargetRevision, ExpectedStateVersion: state.Version,
		ExpectedParticipantVersion: current.Version,
	})
	if err != nil {
		return modelsettingsdomain.ParticipantRecord{}, err
	}
	controller.participant = cloneParticipant(updated)
	return updated, nil
}

func (controller *HotRuntimeController[T]) failPreparation(
	ctx context.Context,
	state modelsettingsdomain.RolloutState,
	record modelsettingsdomain.ParticipantRecord,
	cause error,
) error {
	code, retryable := safeActivationError(cause)
	if record.Phase == modelsettingsdomain.ParticipantPhasePreparing || record.Phase == modelsettingsdomain.ParticipantPhasePrepared ||
		record.Phase == modelsettingsdomain.ParticipantPhaseArmed {
		_, _ = controller.transitionParticipant(ctx, record, modelsettingsdomain.ParticipantPhaseFailed, code, retryable)
	}
	_ = controller.host.abort(state.TargetRevision)
	_, err := controller.activation.FailActivation(ctx, modelsettingsapplication.FailActivationCommand{
		RolloutID: state.ID, ExpectedPhase: state.Phase, ExpectedVersion: state.Version, ErrorCode: code,
	})
	if err != nil && hotRuntimeFatal(err) {
		return err
	}
	return nil
}

func (controller *HotRuntimeController[T]) clearParticipant() {
	controller.participantMu.Lock()
	controller.participant = nil
	controller.participantMu.Unlock()
}

func cloneParticipant(record modelsettingsdomain.ParticipantRecord) *modelsettingsdomain.ParticipantRecord {
	copy := record
	return &copy
}

func terminalParticipant(phase modelsettingsdomain.ParticipantPhase) bool {
	return phase == modelsettingsdomain.ParticipantPhaseFailed || phase == modelsettingsdomain.ParticipantPhaseAborted ||
		phase == modelsettingsdomain.ParticipantPhaseRetired
}

func safeActivationError(err error) (string, bool) {
	var classified *foundation.Error
	if errors.As(err, &classified) && canonicalRuntimeErrorCode(classified.Code) {
		return classified.Code, classified.Retryable
	}
	return modelsettingsdomain.ErrorCodeActivationPrepareFailed, true
}

func canonicalRuntimeErrorCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func hotRuntimeFatal(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return false
	}
	return classified.Code == modelsettingsdomain.ErrorCodeRuntimeOwnershipLost ||
		(classified.Kind == foundation.ErrorConsistencyViolation && !classified.Retryable)
}

func nilRuntimeDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (host *RuntimeHost[T]) activeBinding() (RuntimeBinding, bool) {
	if host == nil {
		return RuntimeBinding{}, false
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed || host.active == nil {
		return RuntimeBinding{}, false
	}
	return host.active.binding, true
}

func (host *RuntimeHost[T]) gateIsClosed() bool {
	if host == nil {
		return false
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.gateClosed
}

// ActivationCoordinatorOptions owns API-side lease and polling policy.
type ActivationCoordinatorOptions struct {
	Control   modelsettingsapplication.ActivationControl
	Revisions modelsettingsapplication.RevisionLoader

	PollInterval  time.Duration
	LeaseDuration time.Duration
	FreshWithin   time.Duration
	RenewInterval time.Duration
	Clock         foundation.Clock
}

// ActivationCoordinator advances the durable two-role activation barrier.
// It never owns a generation and never performs role-local lifecycle calls.
type ActivationCoordinator struct {
	control   modelsettingsapplication.ActivationControl
	revisions modelsettingsapplication.RevisionLoader
	poll      time.Duration
	lease     time.Duration
	fresh     time.Duration
	renew     time.Duration
	clock     foundation.Clock

	lastRollout foundation.ID
	lastRenewal time.Time
}

// NewActivationCoordinator creates the API process's background activation owner.
func NewActivationCoordinator(options ActivationCoordinatorOptions) (*ActivationCoordinator, error) {
	if nilRuntimeDependency(options.Control) || nilRuntimeDependency(options.Revisions) {
		return nil, runtimeNotReadyError(errors.New("activation coordinator dependency is unavailable"))
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultHotRuntimePollInterval
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = defaultActivationLeaseDuration
	}
	if options.FreshWithin == 0 {
		options.FreshWithin = modelsettingsapplication.DefaultRuntimeFreshWithin
	}
	if options.RenewInterval == 0 {
		options.RenewInterval = defaultActivationRenewInterval
	}
	if nilRuntimeDependency(options.Clock) {
		options.Clock = foundation.SystemClock{}
	}
	if options.PollInterval <= 0 || options.PollInterval > time.Minute ||
		!validActivationDuration(options.LeaseDuration) || !validActivationDuration(options.FreshWithin) ||
		options.RenewInterval < time.Second || options.RenewInterval >= options.LeaseDuration ||
		options.RenewInterval%time.Microsecond != 0 {
		return nil, runtimeBindingError(errors.New("activation coordinator timing policy is invalid"))
	}
	return &ActivationCoordinator{
		control: options.Control, revisions: options.Revisions, poll: options.PollInterval,
		lease: options.LeaseDuration, fresh: options.FreshWithin, renew: options.RenewInterval, clock: options.Clock,
	}, nil
}

// Run performs startup recovery and then continuously advances or renews live work.
func (coordinator *ActivationCoordinator) Run(ctx context.Context) error {
	if coordinator == nil {
		return runtimeNotReadyError(errors.New("activation coordinator is unavailable"))
	}
	if ctx == nil {
		return runtimeBindingError(errors.New("activation coordinator context is nil"))
	}
	if _, err := coordinator.Recover(ctx); err != nil && hotRuntimeFatal(err) {
		return err
	}
	ticker := time.NewTicker(coordinator.poll)
	defer ticker.Stop()
	for {
		if err := coordinator.Reconcile(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if hotRuntimeFatal(err) {
				return err
			}
			if activationLeaseExpired(err) {
				_, _ = coordinator.Recover(ctx)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Recover applies the durable pre-commit abort/post-commit adoption rule once.
func (coordinator *ActivationCoordinator) Recover(ctx context.Context) (modelsettingsdomain.ActivationRecovery, error) {
	if coordinator == nil || ctx == nil {
		return modelsettingsdomain.ActivationRecovery{}, runtimeNotReadyError(errors.New("activation coordinator recovery is unavailable"))
	}
	recovery, err := coordinator.control.RecoverActivation(ctx, modelsettingsapplication.RecoverActivationCommand{
		LeaseDuration: coordinator.lease,
	})
	if err == nil && recovery.Action != modelsettingsdomain.ActivationRecoveryNone {
		coordinator.lastRollout = recovery.State.ID
		coordinator.lastRenewal = coordinator.clock.Now()
	}
	return recovery, err
}

// Reconcile performs at most one global CAS transition or lease renewal.
func (coordinator *ActivationCoordinator) Reconcile(ctx context.Context) error {
	if coordinator == nil || ctx == nil {
		return runtimeNotReadyError(errors.New("activation coordinator reconciliation is unavailable"))
	}
	snapshot, err := coordinator.revisions.Snapshot(ctx)
	if err != nil {
		return err
	}
	state := snapshot.Rollout
	switch state.Phase {
	case modelsettingsdomain.RolloutPhaseIdle, modelsettingsdomain.RolloutPhaseFailed:
		coordinator.lastRollout = ""
		coordinator.lastRenewal = time.Time{}
		return nil
	case modelsettingsdomain.RolloutPhasePreparing:
		if code, failed := activationParticipantFailure(snapshot.Participants); failed {
			_, err = coordinator.control.FailActivation(ctx, modelsettingsapplication.FailActivationCommand{
				RolloutID: state.ID, ExpectedPhase: state.Phase, ExpectedVersion: state.Version, ErrorCode: code,
			})
			return err
		}
		if bothParticipantsReady(snapshot.Participants, state.TargetRevision, modelsettingsdomain.ParticipantPhasePrepared) {
			_, err = coordinator.control.AdvanceActivation(ctx, modelsettingsapplication.AdvanceActivationCommand{
				RolloutID: state.ID, ExpectedPhase: state.Phase, ExpectedVersion: state.Version,
				NextPhase: modelsettingsdomain.RolloutPhaseArming, LeaseDuration: coordinator.lease, FreshWithin: coordinator.fresh,
			})
			coordinator.recordRenewal(state.ID, err)
			return err
		}
	case modelsettingsdomain.RolloutPhaseArming:
		if code, failed := activationParticipantFailure(snapshot.Participants); failed {
			_, err = coordinator.control.FailActivation(ctx, modelsettingsapplication.FailActivationCommand{
				RolloutID: state.ID, ExpectedPhase: state.Phase, ExpectedVersion: state.Version, ErrorCode: code,
			})
			return err
		}
		if bothParticipantsReady(snapshot.Participants, state.TargetRevision, modelsettingsdomain.ParticipantPhaseArmed) {
			_, err = coordinator.control.CommitActivation(ctx, modelsettingsapplication.CommitActivationCommand{
				RolloutID: state.ID, ExpectedVersion: state.Version,
				FreshWithin: coordinator.fresh, LeaseDuration: coordinator.lease,
			})
			coordinator.recordRenewal(state.ID, err)
			return err
		}
	case modelsettingsdomain.RolloutPhaseActivating:
		if bothParticipantsReady(snapshot.Participants, state.TargetRevision, modelsettingsdomain.ParticipantPhaseActivated) {
			_, err = coordinator.control.FinalizeActivation(ctx, modelsettingsapplication.FinalizeActivationCommand{
				RolloutID: state.ID, ExpectedVersion: state.Version, FreshWithin: coordinator.fresh,
			})
			return err
		}
	default:
		return foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeCorrupt, false,
			errors.New("activation coordinator observed an unsupported phase"))
	}
	if coordinator.renewalDue(state.ID) {
		_, err = coordinator.control.RenewActivation(ctx, modelsettingsapplication.RenewActivationCommand{
			RolloutID: state.ID, ExpectedPhase: state.Phase, ExpectedVersion: state.Version,
			LeaseDuration: coordinator.lease,
		})
		coordinator.recordRenewal(state.ID, err)
	}
	return err
}

func (coordinator *ActivationCoordinator) renewalDue(rolloutID foundation.ID) bool {
	return coordinator.lastRollout != rolloutID || coordinator.lastRenewal.IsZero() ||
		coordinator.clock.Now().Sub(coordinator.lastRenewal) >= coordinator.renew
}

func (coordinator *ActivationCoordinator) recordRenewal(rolloutID foundation.ID, err error) {
	if err != nil {
		return
	}
	coordinator.lastRollout = rolloutID
	coordinator.lastRenewal = coordinator.clock.Now()
}

func bothParticipantsReady(
	participants modelsettingsdomain.ParticipantSummaries,
	target int64,
	phase modelsettingsdomain.ParticipantPhase,
) bool {
	return participantReady(participants.API, target, phase) && participantReady(participants.Worker, target, phase)
}

func participantReady(summary modelsettingsdomain.ParticipantSummary, target int64, phase modelsettingsdomain.ParticipantPhase) bool {
	return summary.Present && summary.Fresh && summary.TargetRevision == target && summary.Phase == phase
}

func activationParticipantFailure(participants modelsettingsdomain.ParticipantSummaries) (string, bool) {
	for _, summary := range []modelsettingsdomain.ParticipantSummary{participants.API, participants.Worker} {
		if summary.Phase != modelsettingsdomain.ParticipantPhaseFailed && summary.Phase != modelsettingsdomain.ParticipantPhaseAborted {
			continue
		}
		if canonicalRuntimeErrorCode(summary.LastErrorCode) {
			return summary.LastErrorCode, true
		}
		return modelsettingsdomain.ErrorCodeActivationPrepareFailed, true
	}
	return "", false
}

func activationLeaseExpired(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == modelsettingsdomain.ErrorCodeActivationLeaseExpired
}

func validActivationDuration(duration time.Duration) bool {
	return duration >= time.Second && duration <= 5*time.Minute && duration%time.Microsecond == 0
}
