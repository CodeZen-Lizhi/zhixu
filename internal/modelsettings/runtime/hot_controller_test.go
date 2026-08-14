package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const (
	hotControllerTestRolloutID foundation.ID = "30000000-0000-4000-8000-000000000001"
	hotControllerTestAPIID     foundation.ID = "20000000-0000-4000-8000-000000000011"
	hotControllerTestWorkerID  foundation.ID = "20000000-0000-4000-8000-000000000012"
)

var hotControllerTestNow = time.Date(2026, time.August, 11, 8, 0, 0, 0, time.UTC)

type hotControllerSnapshotBarrier struct {
	arrived chan struct{}
	release chan struct{}
}

type hotControllerTestStore struct {
	mu sync.Mutex

	snapshot     modelsettingsdomain.Snapshot
	runtimes     map[modelsettingsdomain.RuntimeRole]modelsettingsdomain.RuntimeRecord
	participants map[modelsettingsdomain.RuntimeRole]modelsettingsdomain.ParticipantRecord
	now          time.Time

	runtimeRegistrations     []modelsettingsapplication.RuntimeRegistration
	participantRegistrations []modelsettingsapplication.ParticipantRegistration
	advanceAttempts          int
	advanceSuccesses         int
	failCalls                int
	acknowledgeCalls         int
	finalizeCalls            int

	registerRuntimeErr error
	restoreRuntimeErr  error
	onRegisterRuntime  func(modelsettingsapplication.RuntimeRegistration)
	heartbeatStarted   chan<- struct{}
	heartbeatRelease   <-chan struct{}
	heartbeatErr       error
	snapshotBarrier    *hotControllerSnapshotBarrier
	restoreCommands    []modelsettingsapplication.RestoreRuntimeAvailabilityCommand
}

func newHotControllerTestStore(activeRevision int64, rollout modelsettingsdomain.RolloutState) *hotControllerTestStore {
	return &hotControllerTestStore{
		snapshot: modelsettingsdomain.Snapshot{
			DesiredRevision: max(activeRevision, rollout.TargetRevision),
			ActiveRevision:  activeRevision,
			Rollout:         rollout,
		},
		runtimes:     make(map[modelsettingsdomain.RuntimeRole]modelsettingsdomain.RuntimeRecord),
		participants: make(map[modelsettingsdomain.RuntimeRole]modelsettingsdomain.ParticipantRecord),
		now:          hotControllerTestNow,
	}
}

func (store *hotControllerTestStore) Snapshot(ctx context.Context) (modelsettingsdomain.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.Snapshot{}, err
	}
	store.mu.Lock()
	snapshot := store.snapshotLocked()
	barrier := store.snapshotBarrier
	store.mu.Unlock()
	if barrier != nil {
		select {
		case barrier.arrived <- struct{}{}:
		case <-ctx.Done():
			return modelsettingsdomain.Snapshot{}, ctx.Err()
		}
		select {
		case <-barrier.release:
		case <-ctx.Done():
			return modelsettingsdomain.Snapshot{}, ctx.Err()
		}
	}
	return snapshot, nil
}

func (store *hotControllerTestStore) LoadRevision(ctx context.Context, revision int64) (modelsettingsdomain.ResolvedSettings, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.ResolvedSettings{}, err
	}
	return modelsettingsdomain.ResolvedSettings{Revision: revision}, nil
}

func (store *hotControllerTestStore) RegisterRuntime(
	ctx context.Context,
	command modelsettingsapplication.RuntimeRegistration,
) (modelsettingsdomain.RuntimeRecord, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.RuntimeRecord{}, err
	}
	store.mu.Lock()
	store.runtimeRegistrations = append(store.runtimeRegistrations, command)
	configuredErr := store.registerRuntimeErr
	callback := store.onRegisterRuntime
	if configuredErr != nil {
		store.mu.Unlock()
		return modelsettingsdomain.RuntimeRecord{}, configuredErr
	}
	if current, ok := store.runtimes[command.Role]; ok && current.InstanceID != command.InstanceID && current.Fresh {
		store.mu.Unlock()
		return modelsettingsdomain.RuntimeRecord{}, hotControllerOwnershipLost("fresh runtime owner already exists")
	}
	record := modelsettingsdomain.RuntimeRecord{
		Role: command.Role, InstanceID: command.InstanceID, AppliedRevision: command.AppliedRevision,
		Phase: command.Phase, AppliedAt: store.tickLocked(), HeartbeatAt: store.now, Fresh: true,
	}
	store.runtimes[command.Role] = record
	store.mu.Unlock()
	if callback != nil {
		callback(command)
	}
	return record, nil
}

func (store *hotControllerTestStore) HeartbeatRuntime(
	ctx context.Context,
	command modelsettingsapplication.RuntimeHeartbeat,
) (modelsettingsdomain.RuntimeRecord, error) {
	store.mu.Lock()
	started := store.heartbeatStarted
	release := store.heartbeatRelease
	configuredErr := store.heartbeatErr
	store.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return modelsettingsdomain.RuntimeRecord{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.RuntimeRecord{}, err
	}
	if configuredErr != nil {
		return modelsettingsdomain.RuntimeRecord{}, configuredErr
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.runtimes[command.Role]
	if !ok || record.InstanceID != command.InstanceID {
		return modelsettingsdomain.RuntimeRecord{}, hotControllerOwnershipLost("runtime heartbeat owner changed")
	}
	record.HeartbeatAt = store.tickLocked()
	record.Fresh = true
	store.runtimes[command.Role] = record
	return record, nil
}

func (store *hotControllerTestStore) RestoreRuntimeAvailability(
	ctx context.Context,
	command modelsettingsapplication.RestoreRuntimeAvailabilityCommand,
) (modelsettingsdomain.RuntimeRecord, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.RuntimeRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.restoreCommands = append(store.restoreCommands, command)
	if store.restoreRuntimeErr != nil {
		return modelsettingsdomain.RuntimeRecord{}, store.restoreRuntimeErr
	}
	state := store.snapshot.Rollout
	authorized := false
	switch state.Phase {
	case modelsettingsdomain.RolloutPhaseIdle, modelsettingsdomain.RolloutPhaseFailed:
		authorized = store.snapshot.ActiveRevision == command.AppliedRevision
	case modelsettingsdomain.RolloutPhasePreparing, modelsettingsdomain.RolloutPhaseArming:
		authorized = store.snapshot.ActiveRevision == command.AppliedRevision &&
			state.PreviousActiveRevision == command.AppliedRevision
	case modelsettingsdomain.RolloutPhaseActivating:
		authorized = store.snapshot.ActiveRevision == command.AppliedRevision &&
			state.TargetRevision == command.AppliedRevision
	}
	if !authorized {
		return modelsettingsdomain.RuntimeRecord{}, hotControllerActivationConflict("runtime availability restore is not authorized")
	}
	record, ok := store.runtimes[command.Role]
	if !ok || record.InstanceID != command.InstanceID || record.RolloutID != nil {
		return modelsettingsdomain.RuntimeRecord{}, hotControllerOwnershipLost("runtime availability owner changed")
	}
	if record.AppliedRevision != command.AppliedRevision {
		return modelsettingsdomain.RuntimeRecord{}, hotControllerActivationConflict("runtime availability revision changed")
	}
	if record.Phase != modelsettingsdomain.RuntimePhaseUnavailable && record.Phase != modelsettingsdomain.RuntimePhaseActive {
		return modelsettingsdomain.RuntimeRecord{}, hotControllerActivationConflict("runtime availability phase changed")
	}
	record.Phase = modelsettingsdomain.RuntimePhaseActive
	record.HeartbeatAt = store.tickLocked()
	record.Fresh = true
	store.runtimes[command.Role] = record
	return record, nil
}

func (store *hotControllerTestStore) StartActivation(
	ctx context.Context,
	command modelsettingsapplication.StartActivationCommand,
) (modelsettingsapplication.StartActivationResult, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsapplication.StartActivationResult{}, err
	}
	return modelsettingsapplication.StartActivationResult{}, hotControllerActivationConflict("unexpected activation start")
}

func (store *hotControllerTestStore) RenewActivation(
	ctx context.Context,
	command modelsettingsapplication.RenewActivationCommand,
) (modelsettingsdomain.RolloutState, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireStateLocked(command.RolloutID, command.ExpectedPhase, command.ExpectedVersion); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	store.snapshot.Rollout.Version++
	store.snapshot.Rollout.LeaseExpiresAt = store.tickLocked().Add(command.LeaseDuration)
	return store.snapshot.Rollout, nil
}

func (store *hotControllerTestStore) AdvanceActivation(
	ctx context.Context,
	command modelsettingsapplication.AdvanceActivationCommand,
) (modelsettingsdomain.RolloutState, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.advanceAttempts++
	if err := store.requireStateLocked(command.RolloutID, command.ExpectedPhase, command.ExpectedVersion); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	if err := modelsettingsdomain.ValidateActivationTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	if command.NextPhase == modelsettingsdomain.RolloutPhaseArming &&
		!store.participantsReadyLocked(store.snapshot.Rollout.TargetRevision, modelsettingsdomain.ParticipantPhasePrepared) {
		return modelsettingsdomain.RolloutState{}, hotControllerActivationConflict("participants are not prepared")
	}
	store.snapshot.Rollout.Phase = command.NextPhase
	store.snapshot.Rollout.Version++
	store.snapshot.Rollout.LeaseExpiresAt = store.tickLocked().Add(command.LeaseDuration)
	store.advanceSuccesses++
	return store.snapshot.Rollout, nil
}

func (store *hotControllerTestStore) FailActivation(
	ctx context.Context,
	command modelsettingsapplication.FailActivationCommand,
) (modelsettingsdomain.RolloutState, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireStateLocked(command.RolloutID, command.ExpectedPhase, command.ExpectedVersion); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	if err := modelsettingsdomain.ValidateActivationTransition(command.ExpectedPhase, modelsettingsdomain.RolloutPhaseFailed); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	store.snapshot.Rollout.Phase = modelsettingsdomain.RolloutPhaseFailed
	store.snapshot.Rollout.LastErrorCode = command.ErrorCode
	store.snapshot.Rollout.Version++
	store.failCalls++
	return store.snapshot.Rollout, nil
}

func (store *hotControllerTestStore) CommitActivation(
	ctx context.Context,
	command modelsettingsapplication.CommitActivationCommand,
) (modelsettingsdomain.RolloutState, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireStateLocked(command.RolloutID, modelsettingsdomain.RolloutPhaseArming, command.ExpectedVersion); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	target := store.snapshot.Rollout.TargetRevision
	if !store.participantsReadyLocked(target, modelsettingsdomain.ParticipantPhaseArmed) || !store.runtimeOwnersReadyLocked() {
		return modelsettingsdomain.RolloutState{}, hotControllerActivationConflict("activation barrier is not armed")
	}
	store.snapshot.ActiveRevision = target
	store.snapshot.Rollout.Phase = modelsettingsdomain.RolloutPhaseActivating
	store.snapshot.Rollout.Version++
	store.snapshot.Rollout.LeaseExpiresAt = store.tickLocked().Add(command.LeaseDuration)
	return store.snapshot.Rollout, nil
}

func (store *hotControllerTestStore) AcknowledgeActivation(
	ctx context.Context,
	command modelsettingsapplication.ActivationAcknowledgement,
) (modelsettingsdomain.ParticipantRecord, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.ParticipantRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireStateLocked(command.RolloutID, modelsettingsdomain.RolloutPhaseActivating, command.ExpectedStateVersion); err != nil {
		return modelsettingsdomain.ParticipantRecord{}, err
	}
	participant, ok := store.participants[command.Role]
	if !ok || participant.RolloutID != command.RolloutID || participant.InstanceID != command.InstanceID ||
		participant.TargetRevision != command.TargetRevision || participant.Version != command.ExpectedParticipantVersion ||
		participant.Phase != modelsettingsdomain.ParticipantPhaseArmed {
		return modelsettingsdomain.ParticipantRecord{}, hotControllerParticipantConflict("participant acknowledgement CAS failed")
	}
	runtimeRecord, ok := store.runtimes[command.Role]
	if !ok || runtimeRecord.InstanceID != command.InstanceID || runtimeRecord.Phase != modelsettingsdomain.RuntimePhaseActive {
		return modelsettingsdomain.ParticipantRecord{}, hotControllerOwnershipLost("runtime owner changed before acknowledgement")
	}
	runtimeRecord.AppliedRevision = command.TargetRevision
	runtimeRecord.Phase = modelsettingsdomain.RuntimePhaseActive
	runtimeRecord.AppliedAt = store.tickLocked()
	runtimeRecord.HeartbeatAt = store.now
	store.runtimes[command.Role] = runtimeRecord
	participant.Phase = modelsettingsdomain.ParticipantPhaseActivated
	participant.Version++
	participant.ActivatedAt = store.tickLocked()
	participant.HeartbeatAt = store.now
	store.participants[command.Role] = participant
	store.acknowledgeCalls++
	return participant, nil
}

func (store *hotControllerTestStore) FinalizeActivation(
	ctx context.Context,
	command modelsettingsapplication.FinalizeActivationCommand,
) (modelsettingsdomain.RolloutState, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireStateLocked(command.RolloutID, modelsettingsdomain.RolloutPhaseActivating, command.ExpectedVersion); err != nil {
		return modelsettingsdomain.RolloutState{}, err
	}
	target := store.snapshot.Rollout.TargetRevision
	if !store.participantsReadyLocked(target, modelsettingsdomain.ParticipantPhaseActivated) {
		return modelsettingsdomain.RolloutState{}, hotControllerActivationConflict("participants are not activated")
	}
	for _, role := range []modelsettingsdomain.RuntimeRole{RuntimeRoleAPI, RuntimeRoleWorker} {
		runtimeRecord := store.runtimes[role]
		if !runtimeRecord.Fresh || runtimeRecord.AppliedRevision != target {
			return modelsettingsdomain.RolloutState{}, hotControllerActivationConflict("serving runtimes have not converged")
		}
	}
	version := store.snapshot.Rollout.Version + 1
	store.snapshot.Rollout = modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle, Version: version}
	store.finalizeCalls++
	return store.snapshot.Rollout, nil
}

func (store *hotControllerTestStore) RecoverActivation(
	ctx context.Context,
	_ modelsettingsapplication.RecoverActivationCommand,
) (modelsettingsdomain.ActivationRecovery, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.ActivationRecovery{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return modelsettingsdomain.ActivationRecovery{State: store.snapshot.Rollout, Action: modelsettingsdomain.ActivationRecoveryNone}, nil
}

func (store *hotControllerTestStore) RegisterParticipant(
	ctx context.Context,
	command modelsettingsapplication.ParticipantRegistration,
) (modelsettingsdomain.ParticipantRecord, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.ParticipantRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.participantRegistrations = append(store.participantRegistrations, command)
	state := store.snapshot.Rollout
	if state.ID != command.RolloutID || state.TargetRevision != command.TargetRevision {
		return modelsettingsdomain.ParticipantRecord{}, hotControllerParticipantConflict("participant rollout binding changed")
	}
	runtimeRecord, ok := store.runtimes[command.Role]
	if !ok || runtimeRecord.InstanceID != command.InstanceID || runtimeRecord.Phase != modelsettingsdomain.RuntimePhaseActive {
		return modelsettingsdomain.ParticipantRecord{}, hotControllerOwnershipLost("participant serving owner changed")
	}
	if current, ok := store.participants[command.Role]; ok && current.RolloutID == command.RolloutID &&
		current.TargetRevision == command.TargetRevision {
		if current.InstanceID != command.InstanceID {
			return modelsettingsdomain.ParticipantRecord{}, hotControllerOwnershipLost("fresh participant owner already exists")
		}
		return current, nil
	}
	if command.InitialPhase == modelsettingsdomain.ParticipantPhaseActivated {
		if state.Phase != modelsettingsdomain.RolloutPhaseActivating || runtimeRecord.AppliedRevision != command.TargetRevision {
			return modelsettingsdomain.ParticipantRecord{}, hotControllerParticipantConflict("startup participant cannot adopt target")
		}
	} else if command.InitialPhase != modelsettingsdomain.ParticipantPhasePreparing ||
		(state.Phase != modelsettingsdomain.RolloutPhasePreparing && state.Phase != modelsettingsdomain.RolloutPhaseArming) {
		return modelsettingsdomain.ParticipantRecord{}, hotControllerParticipantConflict("participant initial phase is invalid")
	}
	record := modelsettingsdomain.ParticipantRecord{
		RolloutID: command.RolloutID, Role: command.Role, InstanceID: command.InstanceID,
		TargetRevision: command.TargetRevision, Phase: command.InitialPhase, Version: 1,
		HeartbeatAt: store.tickLocked(), Fresh: true,
	}
	if command.InitialPhase == modelsettingsdomain.ParticipantPhaseActivated {
		record.ActivatedAt = record.HeartbeatAt
	}
	store.participants[command.Role] = record
	return record, nil
}

func (store *hotControllerTestStore) HeartbeatParticipant(
	ctx context.Context,
	command modelsettingsapplication.ParticipantHeartbeat,
) (modelsettingsdomain.ParticipantRecord, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.ParticipantRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.participants[command.Role]
	if !ok || record.RolloutID != command.RolloutID || record.InstanceID != command.InstanceID ||
		record.TargetRevision != command.TargetRevision || record.Phase != command.ExpectedPhase ||
		record.Version != command.ExpectedVersion {
		return modelsettingsdomain.ParticipantRecord{}, hotControllerParticipantConflict("participant heartbeat CAS failed")
	}
	record.Version++
	record.HeartbeatAt = store.tickLocked()
	record.Fresh = true
	store.participants[command.Role] = record
	return record, nil
}

func (store *hotControllerTestStore) TransitionParticipant(
	ctx context.Context,
	command modelsettingsapplication.ParticipantTransitionCommand,
) (modelsettingsdomain.ParticipantRecord, error) {
	if err := ctx.Err(); err != nil {
		return modelsettingsdomain.ParticipantRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.participants[command.Role]
	if !ok || record.RolloutID != command.RolloutID || record.InstanceID != command.InstanceID ||
		record.TargetRevision != command.TargetRevision || record.Phase != command.ExpectedPhase ||
		record.Version != command.ExpectedVersion {
		return modelsettingsdomain.ParticipantRecord{}, hotControllerParticipantConflict("participant transition CAS failed")
	}
	if err := modelsettingsdomain.ValidateParticipantTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return modelsettingsdomain.ParticipantRecord{}, err
	}
	record.Phase = command.NextPhase
	record.Version++
	record.HeartbeatAt = store.tickLocked()
	record.LastErrorCode = command.ErrorCode
	record.ErrorRetryable = command.ErrorRetryable
	if command.NextPhase == modelsettingsdomain.ParticipantPhasePrepared {
		record.PreparedAt = record.HeartbeatAt
	}
	store.participants[command.Role] = record
	return record, nil
}

func (store *hotControllerTestStore) snapshotLocked() modelsettingsdomain.Snapshot {
	snapshot := store.snapshot
	if runtimeRecord, ok := store.runtimes[RuntimeRoleAPI]; ok {
		snapshot.Runtime.API = hotControllerRuntimeSummary(runtimeRecord)
	}
	if runtimeRecord, ok := store.runtimes[RuntimeRoleWorker]; ok {
		snapshot.Runtime.Worker = hotControllerRuntimeSummary(runtimeRecord)
	}
	if snapshot.Rollout.ID != "" {
		if participant, ok := store.participants[RuntimeRoleAPI]; ok && participant.RolloutID == snapshot.Rollout.ID {
			snapshot.Participants.API = hotControllerParticipantSummary(participant)
		}
		if participant, ok := store.participants[RuntimeRoleWorker]; ok && participant.RolloutID == snapshot.Rollout.ID {
			snapshot.Participants.Worker = hotControllerParticipantSummary(participant)
		}
	}
	return snapshot
}

func (store *hotControllerTestStore) currentSnapshot() modelsettingsdomain.Snapshot {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.snapshotLocked()
}

func (store *hotControllerTestStore) requireStateLocked(
	rolloutID foundation.ID,
	phase modelsettingsdomain.RolloutPhase,
	version int64,
) error {
	state := store.snapshot.Rollout
	if state.ID != rolloutID || state.Phase != phase || state.Version != version {
		return hotControllerActivationConflict("activation state CAS failed")
	}
	return nil
}

func (store *hotControllerTestStore) participantsReadyLocked(target int64, phase modelsettingsdomain.ParticipantPhase) bool {
	for _, role := range []modelsettingsdomain.RuntimeRole{RuntimeRoleAPI, RuntimeRoleWorker} {
		participant, ok := store.participants[role]
		if !ok || !participant.Fresh || participant.RolloutID != store.snapshot.Rollout.ID ||
			participant.TargetRevision != target || participant.Phase != phase {
			return false
		}
	}
	return true
}

func (store *hotControllerTestStore) runtimeOwnersReadyLocked() bool {
	for _, role := range []modelsettingsdomain.RuntimeRole{RuntimeRoleAPI, RuntimeRoleWorker} {
		runtimeRecord, ok := store.runtimes[role]
		if !ok || !runtimeRecord.Fresh {
			return false
		}
	}
	return true
}

func (store *hotControllerTestStore) tickLocked() time.Time {
	store.now = store.now.Add(time.Millisecond)
	return store.now
}

func hotControllerRuntimeSummary(record modelsettingsdomain.RuntimeRecord) modelsettingsdomain.RuntimeSummary {
	return modelsettingsdomain.RuntimeSummary{
		AppliedRevision: record.AppliedRevision,
		Phase:           record.Phase,
		Fresh:           record.Fresh,
	}
}

func hotControllerParticipantSummary(record modelsettingsdomain.ParticipantRecord) modelsettingsdomain.ParticipantSummary {
	return modelsettingsdomain.ParticipantSummary{
		Present: true, TargetRevision: record.TargetRevision, Phase: record.Phase, Fresh: record.Fresh,
		LastErrorCode: record.LastErrorCode, ErrorRetryable: record.ErrorRetryable,
	}
}

func hotControllerActivationConflict(message string) error {
	return foundation.NewError(
		foundation.ErrorVersionConflict,
		modelsettingsdomain.ErrorCodeActivationConflict,
		true,
		errors.New(message),
	)
}

func hotControllerParticipantConflict(message string) error {
	return foundation.NewError(
		foundation.ErrorVersionConflict,
		modelsettingsdomain.ErrorCodeParticipantConflict,
		true,
		errors.New(message),
	)
}

func hotControllerOwnershipLost(message string) error {
	return foundation.NewError(
		foundation.ErrorVersionConflict,
		modelsettingsdomain.ErrorCodeRuntimeOwnershipLost,
		false,
		errors.New(message),
	)
}

func TestHotRuntimeControllerAndCoordinatorActivateRevisionZeroToOne(t *testing.T) {
	ctx := context.Background()
	store := newHotControllerTestStore(0, hotControllerRolloutState(modelsettingsdomain.RolloutPhasePreparing, 1, 0, 1))
	apiFactory := newRuntimeHostTestFactory()
	workerFactory := newRuntimeHostTestFactory()
	apiHost := newHotControllerTestHost(t, apiFactory, RuntimeRoleAPI, hotControllerTestAPIID, 0)
	workerHost := newHotControllerTestHost(t, workerFactory, RuntimeRoleWorker, hotControllerTestWorkerID, 0)
	defer apiHost.Close()
	defer workerHost.Close()
	apiController := newHotControllerTestController(t, store, apiHost, modelsettingsdomain.RuntimePhaseActive)
	workerController := newHotControllerTestController(t, store, workerHost, modelsettingsdomain.RuntimePhaseActive)
	registerHotControllerServing(t, store, apiHost, modelsettingsdomain.RuntimePhaseActive)
	registerHotControllerServing(t, store, workerHost, modelsettingsdomain.RuntimePhaseActive)
	coordinator := newHotControllerTestCoordinator(t, store)

	if err := apiController.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := workerController.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := store.currentSnapshot()
	assertHotControllerParticipantPhase(t, snapshot.Participants.API, modelsettingsdomain.ParticipantPhasePrepared)
	assertHotControllerParticipantPhase(t, snapshot.Participants.Worker, modelsettingsdomain.ParticipantPhasePrepared)
	if snapshot.ActiveRevision != 0 || snapshot.Rollout.Phase != modelsettingsdomain.RolloutPhasePreparing {
		t.Fatalf("prepared snapshot = active %d phase %q", snapshot.ActiveRevision, snapshot.Rollout.Phase)
	}

	if err := coordinator.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := apiController.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := workerController.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot = store.currentSnapshot()
	assertHotControllerParticipantPhase(t, snapshot.Participants.API, modelsettingsdomain.ParticipantPhaseArmed)
	assertHotControllerParticipantPhase(t, snapshot.Participants.Worker, modelsettingsdomain.ParticipantPhaseArmed)
	if !apiHost.gateIsClosed() || !workerHost.gateIsClosed() {
		t.Fatal("both runtime gates must be closed before commit")
	}

	if err := coordinator.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot = store.currentSnapshot()
	if snapshot.ActiveRevision != 1 || snapshot.Rollout.Phase != modelsettingsdomain.RolloutPhaseActivating {
		t.Fatalf("committed snapshot = active %d phase %q", snapshot.ActiveRevision, snapshot.Rollout.Phase)
	}
	if snapshot.Runtime.API.AppliedRevision != 0 || snapshot.Runtime.Worker.AppliedRevision != 0 {
		t.Fatalf("commit falsely acknowledged runtimes: %+v", snapshot.Runtime)
	}

	if err := apiController.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := workerController.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot = store.currentSnapshot()
	assertHotControllerParticipantPhase(t, snapshot.Participants.API, modelsettingsdomain.ParticipantPhaseActivated)
	assertHotControllerParticipantPhase(t, snapshot.Participants.Worker, modelsettingsdomain.ParticipantPhaseActivated)
	if snapshot.Runtime.API.AppliedRevision != 1 || snapshot.Runtime.Worker.AppliedRevision != 1 {
		t.Fatalf("acknowledged runtimes = %+v", snapshot.Runtime)
	}
	if !apiHost.gateIsClosed() || !workerHost.gateIsClosed() {
		t.Fatal("runtime gate reopened before durable finalization")
	}

	if err := coordinator.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := apiController.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := workerController.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot = store.currentSnapshot()
	if snapshot.Rollout.Phase != modelsettingsdomain.RolloutPhaseIdle || snapshot.ActiveRevision != 1 {
		t.Fatalf("final snapshot = active %d phase %q", snapshot.ActiveRevision, snapshot.Rollout.Phase)
	}
	assertHotControllerCurrentRevision(t, apiHost, 1)
	assertHotControllerCurrentRevision(t, workerHost, 1)
	if builds, probes := apiFactory.counts(1); builds != 1 || probes != 1 {
		t.Fatalf("api target build/probe calls = %d/%d", builds, probes)
	}
	if builds, probes := workerFactory.counts(1); builds != 1 || probes != 1 {
		t.Fatalf("worker target build/probe calls = %d/%d", builds, probes)
	}
}

func TestHotRuntimeControllerPrepareAndProbeFailureKeepRevisionZeroServing(t *testing.T) {
	for _, test := range []struct {
		name             string
		configure        func(*runtimeHostTestFactory)
		wantBuilds       int
		wantProbes       int
		wantClosedTarget bool
	}{
		{
			name: "build",
			configure: func(factory *runtimeHostTestFactory) {
				factory.buildErrs[1] = errors.New("target build failed")
			},
			wantBuilds: 1,
		},
		{
			name: "probe",
			configure: func(factory *runtimeHostTestFactory) {
				factory.probeErrs[1] = errors.New("target probe failed")
			},
			wantBuilds: 1, wantProbes: 1, wantClosedTarget: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newHotControllerTestStore(0, hotControllerRolloutState(modelsettingsdomain.RolloutPhasePreparing, 1, 0, 1))
			factory := newRuntimeHostTestFactory()
			test.configure(factory)
			host := newHotControllerTestHost(t, factory, RuntimeRoleWorker, hotControllerTestWorkerID, 0)
			defer host.Close()
			controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseActive)
			registerHotControllerServing(t, store, host, modelsettingsdomain.RuntimePhaseActive)

			if err := controller.reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			snapshot := store.currentSnapshot()
			if snapshot.ActiveRevision != 0 || snapshot.Rollout.Phase != modelsettingsdomain.RolloutPhaseFailed ||
				snapshot.Rollout.LastErrorCode != modelsettingsdomain.ErrorCodeActivationPrepareFailed {
				t.Fatalf("failure snapshot = active %d rollout %+v", snapshot.ActiveRevision, snapshot.Rollout)
			}
			participant := store.participants[RuntimeRoleWorker]
			if participant.Phase != modelsettingsdomain.ParticipantPhaseFailed ||
				participant.LastErrorCode != modelsettingsdomain.ErrorCodeActivationPrepareFailed {
				t.Fatalf("failed participant = %+v", participant)
			}
			assertHotControllerCurrentRevision(t, host, 0)
			if builds, probes := factory.counts(1); builds != test.wantBuilds || probes != test.wantProbes {
				t.Fatalf("target build/probe calls = %d/%d, want %d/%d", builds, probes, test.wantBuilds, test.wantProbes)
			}
			if test.wantClosedTarget {
				failed := findClosedRuntimeHostGeneration(t, factory, 1)
				if factory.closeCount(failed) != 1 {
					t.Fatalf("failed target close count = %d", factory.closeCount(failed))
				}
			}
		})
	}
}

func TestHotRuntimeControllerWaitsForLocalPreparationBeforeProbe(t *testing.T) {
	ctx := context.Background()
	store := newHotControllerTestStore(0, hotControllerRolloutState(modelsettingsdomain.RolloutPhasePreparing, 1, 0, 1))
	settings := modelsettingsdomain.CanonicalDisabledSettings()
	settings.Chat.Provider = modelsettingsdomain.ChatProviderOllama
	settings.Chat.BaseURL = modelsettingsdomain.ManagedOllamaBaseURL
	settings.Chat.Model = "smollm2:135m"
	requirement := modelsettingsdomain.RequiresManagedOllama(settings)
	operationID := foundation.ID("30000000-0000-4000-8000-000000000101")
	store.snapshot.DesiredSettings.Settings = settings
	store.snapshot.LocalRuntime = modelsettingsdomain.LocalRuntimeSummary{
		Mode: string(localmodelruntime.RuntimeModeManaged), Phase: string(localmodelruntime.RuntimePhaseStarting), Fresh: true,
		RequirementHash: requirement.Hash, OperationID: &operationID,
		OperationPhase: string(localmodelruntime.OperationPhaseStarting),
	}

	factory := newRuntimeHostTestFactory()
	host := newHotControllerTestHost(t, factory, RuntimeRoleAPI, hotControllerTestAPIID, 0)
	defer host.Close()
	controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseActive)
	registerHotControllerServing(t, store, host, modelsettingsdomain.RuntimePhaseActive)

	if err := controller.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if builds, probes := factory.counts(1); builds != 0 || probes != 0 {
		t.Fatalf("pending local preparation build/probe calls = %d/%d, want 0/0", builds, probes)
	}
	assertHotControllerParticipantPhase(t, store.currentSnapshot().Participants.API, modelsettingsdomain.ParticipantPhasePreparing)

	store.mu.Lock()
	store.snapshot.LocalRuntime.OperationPhase = string(localmodelruntime.OperationPhaseReady)
	store.mu.Unlock()
	if err := controller.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if builds, probes := factory.counts(1); builds != 0 || probes != 0 {
		t.Fatalf("ready operation before runtime build/probe calls = %d/%d, want 0/0", builds, probes)
	}

	store.mu.Lock()
	store.snapshot.LocalRuntime.Phase = string(localmodelruntime.RuntimePhaseReady)
	store.snapshot.LocalRuntime.ReadyHash = requirement.Hash
	store.mu.Unlock()
	if err := controller.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if builds, probes := factory.counts(1); builds != 1 || probes != 1 {
		t.Fatalf("ready local preparation build/probe calls = %d/%d, want 1/1", builds, probes)
	}
	assertHotControllerParticipantPhase(t, store.currentSnapshot().Participants.API, modelsettingsdomain.ParticipantPhasePrepared)
}

func TestHotRuntimeControllerAcceptsActiveAndTargetLocalDemandUnion(t *testing.T) {
	store := newHotControllerTestStore(1, hotControllerRolloutState(modelsettingsdomain.RolloutPhasePreparing, 2, 1, 1))
	active := modelsettingsdomain.CanonicalDisabledSettings()
	active.Chat.Provider = modelsettingsdomain.ChatProviderOllama
	active.Chat.BaseURL = modelsettingsdomain.ManagedOllamaBaseURL
	active.Chat.Model = "smollm2:135m"
	target := modelsettingsdomain.CanonicalDisabledSettings()
	target.Chat.Provider = modelsettingsdomain.ChatProviderOpenAICompatible
	target.Chat.BaseURL = "https://online.example.test/v1"
	target.Chat.Model = "online-chat"
	target.Chat.ModelVersion = "online-chat"
	target.Chat.AdapterVersion = "v1"
	target.Embedding.Provider = modelsettingsdomain.EmbeddingProviderOllama
	target.Embedding.BaseURL = modelsettingsdomain.ManagedOllamaBaseURL
	target.Embedding.Model = "all-minilm:latest"
	target.Embedding.Dimensions = 384
	store.snapshot.ActiveSettings.Settings = active
	store.snapshot.DesiredSettings.Settings = target
	operationID := foundation.ID("30000000-0000-4000-8000-000000000102")
	union, err := localActivationRuntimeRequirement(store.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	store.snapshot.LocalRuntime = modelsettingsdomain.LocalRuntimeSummary{
		Mode: string(localmodelruntime.RuntimeModeManaged), Phase: string(localmodelruntime.RuntimePhaseReady), Fresh: true,
		RequirementHash: union.Hash, ReadyHash: union.Hash, OperationID: &operationID,
		OperationPhase: string(localmodelruntime.OperationPhaseReady),
	}
	factory := newRuntimeHostTestFactory()
	host := newHotControllerTestHost(t, factory, RuntimeRoleAPI, hotControllerTestAPIID, 1)
	defer host.Close()
	controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseActive)
	registerHotControllerServing(t, store, host, modelsettingsdomain.RuntimePhaseActive)

	if err := controller.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if builds, probes := factory.counts(2); builds != 1 || probes != 1 {
		t.Fatalf("active+target union build/probe calls = %d/%d, want 1/1", builds, probes)
	}
	assertHotControllerParticipantPhase(t, store.currentSnapshot().Participants.API, modelsettingsdomain.ParticipantPhasePrepared)
}

func TestHotRuntimeControllerStartupActivatingRecoveryStaysFencedUntilFinalize(t *testing.T) {
	ctx := context.Background()
	store := newHotControllerTestStore(1, hotControllerRolloutState(modelsettingsdomain.RolloutPhaseActivating, 1, 0, 7))
	store.seedRuntime(RuntimeRoleWorker, hotControllerTestWorkerID, 1, modelsettingsdomain.RuntimePhaseActive)
	store.seedParticipant(RuntimeRoleWorker, hotControllerTestWorkerID, 1, modelsettingsdomain.ParticipantPhaseActivated)
	factory := newRuntimeHostTestFactory()
	host := newHotControllerTestHost(t, factory, RuntimeRoleAPI, hotControllerTestAPIID, 1)
	defer host.Close()
	controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseActive)
	registerHotControllerServing(t, store, host, modelsettingsdomain.RuntimePhaseActive)

	if err := controller.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !host.gateIsClosed() {
		t.Fatal("startup target admitted work before global finalization")
	}
	snapshot := store.currentSnapshot()
	assertHotControllerParticipantPhase(t, snapshot.Participants.API, modelsettingsdomain.ParticipantPhaseActivated)
	if len(store.participantRegistrations) != 1 ||
		store.participantRegistrations[0].InitialPhase != modelsettingsdomain.ParticipantPhaseActivated {
		t.Fatalf("startup participant registrations = %+v", store.participantRegistrations)
	}
	if store.acknowledgeCalls != 0 {
		t.Fatalf("startup adoption acknowledgements = %d, want 0", store.acknowledgeCalls)
	}
	if builds, probes := factory.counts(1); builds != 0 || probes != 0 {
		t.Fatalf("startup target was rebuilt/probed = %d/%d", builds, probes)
	}

	coordinator := newHotControllerTestCoordinator(t, store)
	if err := coordinator.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if host.gateIsClosed() {
		t.Fatal("startup target gate stayed closed after durable finalization")
	}
	assertHotControllerCurrentRevision(t, host, 1)
}

func TestHotRuntimeControllerRepairsUnavailableServingBeforePreCommitParticipant(t *testing.T) {
	for _, test := range []struct {
		name             string
		phase            modelsettingsdomain.RolloutPhase
		wantParticipant  modelsettingsdomain.ParticipantPhase
		wantGateClosed   bool
		wantGateRevision int64
	}{
		{
			name: "preparing", phase: modelsettingsdomain.RolloutPhasePreparing,
			wantParticipant: modelsettingsdomain.ParticipantPhasePrepared,
		},
		{
			name: "arming", phase: modelsettingsdomain.RolloutPhaseArming,
			wantParticipant: modelsettingsdomain.ParticipantPhaseArmed,
			wantGateClosed:  true, wantGateRevision: 4,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newHotControllerTestStore(3, hotControllerRolloutState(test.phase, 4, 3, 2))
			factory := newRuntimeHostTestFactory()
			fallback := &runtimeHostTestGeneration{revision: 3}
			host := newHotControllerTestHostWithAvailability(
				t, factory, RuntimeRoleAPI, hotControllerTestAPIID, fallback, true,
			)
			defer host.Close()
			controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseUnavailable)
			registerHotControllerServing(t, store, host, modelsettingsdomain.RuntimePhaseUnavailable)

			if err := controller.reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			snapshot := store.currentSnapshot()
			if snapshot.Runtime.API.Phase != modelsettingsdomain.RuntimePhaseActive ||
				snapshot.Runtime.API.AppliedRevision != 3 {
				t.Fatalf("repaired serving runtime = %+v", snapshot.Runtime.API)
			}
			assertHotControllerParticipantPhase(t, snapshot.Participants.API, test.wantParticipant)
			assertHotControllerRestore(t, store, RuntimeRoleAPI, hotControllerTestAPIID, 3, 1)
			if controller.runtimePhase != modelsettingsdomain.RuntimePhaseActive {
				t.Fatalf("controller runtime phase = %q", controller.runtimePhase)
			}
			if builds, probes := factory.counts(3); builds != 1 || probes != 1 {
				t.Fatalf("serving repair build/probe calls = %d/%d", builds, probes)
			}
			if builds, probes := factory.counts(4); builds != 1 || probes != 1 {
				t.Fatalf("target build/probe calls = %d/%d", builds, probes)
			}
			if factory.closeCount(fallback) != 1 {
				t.Fatalf("unavailable fallback close count = %d", factory.closeCount(fallback))
			}
			gateClosed, gateRevision := hotControllerGateState(host)
			if gateClosed != test.wantGateClosed || gateRevision != test.wantGateRevision {
				t.Fatalf("gate closed/revision = %t/%d, want %t/%d", gateClosed, gateRevision, test.wantGateClosed, test.wantGateRevision)
			}
			if test.phase == modelsettingsdomain.RolloutPhasePreparing {
				assertHotControllerCurrentRevision(t, host, 3)
			}
		})
	}
}

func TestHotRuntimeControllerRepairsUnavailableServingInTerminalPhase(t *testing.T) {
	for _, test := range []struct {
		name    string
		rollout modelsettingsdomain.RolloutState
	}{
		{name: "idle", rollout: modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle}},
		{name: "failed", rollout: hotControllerRolloutState(modelsettingsdomain.RolloutPhaseFailed, 4, 3, 2)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newHotControllerTestStore(3, test.rollout)
			factory := newRuntimeHostTestFactory()
			fallback := &runtimeHostTestGeneration{revision: 3}
			host := newHotControllerTestHostWithAvailability(
				t, factory, RuntimeRoleWorker, hotControllerTestWorkerID, fallback, true,
			)
			defer host.Close()
			controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseUnavailable)
			registerHotControllerServing(t, store, host, modelsettingsdomain.RuntimePhaseUnavailable)

			if err := controller.reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			snapshot := store.currentSnapshot()
			if snapshot.Runtime.Worker.Phase != modelsettingsdomain.RuntimePhaseActive ||
				snapshot.Runtime.Worker.AppliedRevision != 3 {
				t.Fatalf("repaired serving runtime = %+v", snapshot.Runtime.Worker)
			}
			assertHotControllerRestore(t, store, RuntimeRoleWorker, hotControllerTestWorkerID, 3, 1)
			assertHotControllerCurrentRevision(t, host, 3)
			if gateClosed, gateRevision := hotControllerGateState(host); gateClosed || gateRevision != 0 {
				t.Fatalf("terminal repair gate closed/revision = %t/%d", gateClosed, gateRevision)
			}
			if builds, probes := factory.counts(3); builds != 1 || probes != 1 {
				t.Fatalf("serving repair build/probe calls = %d/%d", builds, probes)
			}
			if factory.closeCount(fallback) != 1 {
				t.Fatalf("unavailable fallback close count = %d", factory.closeCount(fallback))
			}
		})
	}
}

func TestHotRuntimeControllerRepairsUnavailableActivatingTargetBeforeAdoption(t *testing.T) {
	ctx := context.Background()
	store := newHotControllerTestStore(3, hotControllerRolloutState(modelsettingsdomain.RolloutPhaseActivating, 3, 2, 7))
	store.seedRuntime(RuntimeRoleWorker, hotControllerTestWorkerID, 3, modelsettingsdomain.RuntimePhaseActive)
	store.seedParticipant(RuntimeRoleWorker, hotControllerTestWorkerID, 3, modelsettingsdomain.ParticipantPhaseActivated)
	factory := newRuntimeHostTestFactory()
	fallback := &runtimeHostTestGeneration{revision: 3}
	host := newHotControllerTestHostWithAvailability(
		t, factory, RuntimeRoleAPI, hotControllerTestAPIID, fallback, true,
	)
	defer host.Close()
	controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseUnavailable)
	registerHotControllerServing(t, store, host, modelsettingsdomain.RuntimePhaseUnavailable)

	if err := controller.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := store.currentSnapshot()
	if snapshot.Runtime.API.Phase != modelsettingsdomain.RuntimePhaseActive || snapshot.Runtime.API.AppliedRevision != 3 {
		t.Fatalf("repaired activating runtime = %+v", snapshot.Runtime.API)
	}
	assertHotControllerParticipantPhase(t, snapshot.Participants.API, modelsettingsdomain.ParticipantPhaseActivated)
	assertHotControllerRestore(t, store, RuntimeRoleAPI, hotControllerTestAPIID, 3, 1)
	if len(store.participantRegistrations) != 1 ||
		store.participantRegistrations[0].InitialPhase != modelsettingsdomain.ParticipantPhaseActivated {
		t.Fatalf("activating participant registrations = %+v", store.participantRegistrations)
	}
	if store.acknowledgeCalls != 0 {
		t.Fatalf("startup adoption acknowledgements = %d", store.acknowledgeCalls)
	}
	if builds, probes := factory.counts(3); builds != 1 || probes != 1 {
		t.Fatalf("activating repair build/probe calls = %d/%d", builds, probes)
	}
	if factory.closeCount(fallback) != 1 {
		t.Fatalf("unavailable fallback close count = %d", factory.closeCount(fallback))
	}
	if gateClosed, gateRevision := hotControllerGateState(host); !gateClosed || gateRevision != 3 {
		t.Fatalf("activating repair gate closed/revision = %t/%d", gateClosed, gateRevision)
	}

	coordinator := newHotControllerTestCoordinator(t, store)
	if err := coordinator.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	assertHotControllerCurrentRevision(t, host, 3)
}

func TestHotRuntimeControllerUnavailableRepairFailureDoesNotAdoptTarget(t *testing.T) {
	for _, test := range []struct {
		name         string
		configure    func(*hotControllerTestStore, *runtimeHostTestFactory)
		wantRestores int
	}{
		{
			name: "build",
			configure: func(_ *hotControllerTestStore, factory *runtimeHostTestFactory) {
				factory.buildErrs[3] = errors.New("serving rebuild failed")
			},
		},
		{
			name: "availability publish",
			configure: func(store *hotControllerTestStore, _ *runtimeHostTestFactory) {
				store.restoreRuntimeErr = errors.New("availability publish failed")
			},
			wantRestores: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newHotControllerTestStore(3, hotControllerRolloutState(modelsettingsdomain.RolloutPhaseActivating, 3, 2, 7))
			factory := newRuntimeHostTestFactory()
			test.configure(store, factory)
			host := newHotControllerTestHostWithAvailability(
				t, factory, RuntimeRoleAPI, hotControllerTestAPIID, &runtimeHostTestGeneration{revision: 3}, true,
			)
			defer host.Close()
			controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseUnavailable)
			registerHotControllerServing(t, store, host, modelsettingsdomain.RuntimePhaseUnavailable)

			if err := controller.reconcile(context.Background()); err == nil {
				t.Fatal("degraded activating recovery unexpectedly succeeded")
			}
			snapshot := store.currentSnapshot()
			if snapshot.Runtime.API.Phase != modelsettingsdomain.RuntimePhaseUnavailable ||
				snapshot.Runtime.API.AppliedRevision != 3 {
				t.Fatalf("failed repair runtime = %+v", snapshot.Runtime.API)
			}
			if len(store.restoreCommands) != test.wantRestores {
				t.Fatalf("availability restore calls = %d, want %d", len(store.restoreCommands), test.wantRestores)
			}
			if len(store.participantRegistrations) != 0 || store.acknowledgeCalls != 0 {
				t.Fatalf("failed repair registered/acknowledged target: registrations=%d acknowledgements=%d",
					len(store.participantRegistrations), store.acknowledgeCalls)
			}
			if controller.runtimePhase != modelsettingsdomain.RuntimePhaseUnavailable {
				t.Fatalf("controller runtime phase = %q", controller.runtimePhase)
			}
		})
	}
}

func TestHotRuntimeControllerUnavailableRevisionZeroRemainsUnavailable(t *testing.T) {
	for _, test := range []struct {
		name        string
		rollout     modelsettingsdomain.RolloutState
		wantErrCode string
	}{
		{
			name: "idle", rollout: modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle},
		},
		{
			name: "preparing", rollout: hotControllerRolloutState(modelsettingsdomain.RolloutPhasePreparing, 1, 0, 1),
			wantErrCode: ErrorCodeRuntimeRevisionUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newHotControllerTestStore(0, test.rollout)
			factory := newRuntimeHostTestFactory()
			host := newHotControllerTestHostWithAvailability(
				t, factory, RuntimeRoleWorker, hotControllerTestWorkerID, &runtimeHostTestGeneration{revision: 0}, true,
			)
			defer host.Close()
			controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseUnavailable)
			registerHotControllerServing(t, store, host, modelsettingsdomain.RuntimePhaseUnavailable)

			err := controller.reconcile(context.Background())
			if test.wantErrCode == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertHotControllerErrorCode(t, err, test.wantErrCode)
			}
			snapshot := store.currentSnapshot()
			if snapshot.Runtime.Worker.Phase != modelsettingsdomain.RuntimePhaseUnavailable ||
				snapshot.Runtime.Worker.AppliedRevision != 0 {
				t.Fatalf("revision zero runtime = %+v", snapshot.Runtime.Worker)
			}
			if len(store.restoreCommands) != 0 || len(store.participantRegistrations) != 0 {
				t.Fatalf("revision zero restore/participant calls = %d/%d", len(store.restoreCommands), len(store.participantRegistrations))
			}
			if builds, probes := factory.counts(0); builds != 0 || probes != 0 {
				t.Fatalf("revision zero build/probe calls = %d/%d", builds, probes)
			}
			_, acquireErr := host.Acquire(context.Background(), CurrentRuntime())
			assertRuntimeHostCode(t, acquireErr, ErrorCodeRuntimeNotReady)
		})
	}
}

func TestActivationCoordinatorCancellationDoesNotFailDurableActivation(t *testing.T) {
	store := newHotControllerTestStore(0, hotControllerRolloutState(modelsettingsdomain.RolloutPhasePreparing, 1, 0, 1))
	store.seedParticipant(RuntimeRoleAPI, hotControllerTestAPIID, 1, modelsettingsdomain.ParticipantPhasePrepared)
	store.seedParticipant(RuntimeRoleWorker, hotControllerTestWorkerID, 1, modelsettingsdomain.ParticipantPhasePrepared)
	coordinator := newHotControllerTestCoordinator(t, store)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := coordinator.Run(cancelled); err != nil {
		t.Fatal(err)
	}
	snapshot := store.currentSnapshot()
	if snapshot.Rollout.Phase != modelsettingsdomain.RolloutPhasePreparing || store.failCalls != 0 {
		t.Fatalf("cancelled coordinator changed durable activation: rollout %+v fail calls %d", snapshot.Rollout, store.failCalls)
	}

	replacement := newHotControllerTestCoordinator(t, store)
	if err := replacement.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot = store.currentSnapshot(); snapshot.Rollout.Phase != modelsettingsdomain.RolloutPhaseArming {
		t.Fatalf("replacement coordinator phase = %q, want arming", snapshot.Rollout.Phase)
	}
}

func TestHotRuntimeControllerInitialPhaseAndActiveSignal(t *testing.T) {
	for _, test := range []struct {
		name  string
		phase modelsettingsdomain.RuntimePhase
		want  modelsettingsdomain.RuntimePhase
	}{
		{name: "zero value defaults active", want: modelsettingsdomain.RuntimePhaseActive},
		{name: "unavailable stays unavailable", phase: modelsettingsdomain.RuntimePhaseUnavailable, want: modelsettingsdomain.RuntimePhaseUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newHotControllerTestStore(0, modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle})
			factory := newRuntimeHostTestFactory()
			host := newHotControllerTestHost(t, factory, RuntimeRoleAPI, hotControllerTestAPIID, 0)
			ctx, cancel := context.WithCancel(context.Background())
			store.onRegisterRuntime = func(modelsettingsapplication.RuntimeRegistration) { cancel() }
			controller := newHotControllerTestController(t, store, host, test.phase)
			assertHotControllerSignalOpen(t, controller.Active())
			if err := controller.Run(ctx); err != nil {
				t.Fatal(err)
			}
			assertHotControllerSignalOpen(t, controller.Active())
			if len(store.runtimeRegistrations) != 1 || store.runtimeRegistrations[0].Phase != test.want {
				t.Fatalf("runtime registrations = %+v, want phase %q", store.runtimeRegistrations, test.want)
			}
			snapshot := store.currentSnapshot()
			if snapshot.Runtime.API.Phase != test.want {
				t.Fatalf("serving phase = %q, want %q", snapshot.Runtime.API.Phase, test.want)
			}
		})
	}

	t.Run("registration failure leaves signal open", func(t *testing.T) {
		store := newHotControllerTestStore(0, modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle})
		store.registerRuntimeErr = errors.New("registration failed")
		host := newHotControllerTestHost(t, newRuntimeHostTestFactory(), RuntimeRoleAPI, hotControllerTestAPIID, 0)
		controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseActive)
		if err := controller.Run(context.Background()); !errors.Is(err, store.registerRuntimeErr) {
			t.Fatalf("Run error = %v", err)
		}
		assertHotControllerSignalOpen(t, controller.Active())
	})

	t.Run("cancelled registration leaves signal open", func(t *testing.T) {
		store := newHotControllerTestStore(0, modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle})
		host := newHotControllerTestHost(t, newRuntimeHostTestFactory(), RuntimeRoleAPI, hotControllerTestAPIID, 0)
		controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseActive)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := controller.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context canceled", err)
		}
		assertHotControllerSignalOpen(t, controller.Active())
	})

	t.Run("invalid initial phase is rejected", func(t *testing.T) {
		store := newHotControllerTestStore(0, modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle})
		host := newHotControllerTestHost(t, newRuntimeHostTestFactory(), RuntimeRoleAPI, hotControllerTestAPIID, 0)
		defer host.Close()
		_, err := NewHotRuntimeController(HotRuntimeControllerOptions[*runtimeHostTestGeneration]{
			Host: host, Activation: store, Revisions: store, Runtime: store,
			InitialPhase: modelsettingsdomain.RuntimePhasePrepared,
		})
		assertRuntimeHostCode(t, err, ErrorCodeRuntimeBindingMismatch)
	})
}

func TestHotRuntimeControllerInitialReconcileFencesActivatingBeforeActiveSignal(t *testing.T) {
	store := newHotControllerTestStore(1, hotControllerRolloutState(modelsettingsdomain.RolloutPhaseActivating, 1, 0, 7))
	barrier := &hotControllerSnapshotBarrier{arrived: make(chan struct{}, 1), release: make(chan struct{})}
	store.snapshotBarrier = barrier
	host := newHotControllerTestHost(t, newRuntimeHostTestFactory(), RuntimeRoleAPI, hotControllerTestAPIID, 1)
	defer host.Close()
	controller := newHotControllerTestController(t, store, host, modelsettingsdomain.RuntimePhaseActive)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- controller.Run(ctx) }()

	waitHotControllerSignal(t, barrier.arrived, "initial durable reconcile")
	assertHotControllerSignalOpen(t, controller.Active())
	close(barrier.release)
	waitHotControllerSignal(t, controller.Active(), "activation recovery readiness")
	if !host.gateIsClosed() {
		t.Fatal("activating recovery exposed readiness before closing the target gate")
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("controller run error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("controller did not stop after cancellation")
	}
}

func TestHotRuntimeControllerStopsOnRuntimeOwnershipLoss(t *testing.T) {
	store := newHotControllerTestStore(0, modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle})
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	store.heartbeatStarted = started
	store.heartbeatRelease = release
	store.heartbeatErr = hotControllerOwnershipLost("runtime owner was replaced")
	factory := newRuntimeHostTestFactory()
	initial := &runtimeHostTestGeneration{revision: 0}
	host := newHotControllerTestHostWithValue(t, factory, RuntimeRoleWorker, hotControllerTestWorkerID, initial)
	controller, err := NewHotRuntimeController(HotRuntimeControllerOptions[*runtimeHostTestGeneration]{
		Host: host, Activation: store, Revisions: store, Runtime: store,
		PollInterval: 100 * time.Millisecond, HeartbeatInterval: time.Millisecond, StaleAfter: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- controller.Run(context.Background()) }()
	waitHotControllerSignal(t, controller.Active(), "runtime registration")
	waitHotControllerSignal(t, started, "runtime heartbeat")
	close(release)
	select {
	case err := <-result:
		assertHotControllerErrorCode(t, err, modelsettingsdomain.ErrorCodeRuntimeOwnershipLost)
	case <-time.After(time.Second):
		t.Fatal("controller did not stop after ownership loss")
	}
	if factory.closeCount(initial) != 1 {
		t.Fatalf("owned generation close count = %d", factory.closeCount(initial))
	}
}

func TestActivationCoordinatorConcurrentReconcileUsesStateVersionCAS(t *testing.T) {
	store := newHotControllerTestStore(0, hotControllerRolloutState(modelsettingsdomain.RolloutPhasePreparing, 1, 0, 1))
	store.seedParticipant(RuntimeRoleAPI, hotControllerTestAPIID, 1, modelsettingsdomain.ParticipantPhasePrepared)
	store.seedParticipant(RuntimeRoleWorker, hotControllerTestWorkerID, 1, modelsettingsdomain.ParticipantPhasePrepared)
	barrier := &hotControllerSnapshotBarrier{arrived: make(chan struct{}, 2), release: make(chan struct{})}
	store.snapshotBarrier = barrier
	first := newHotControllerTestCoordinator(t, store)
	second := newHotControllerTestCoordinator(t, store)
	results := make(chan error, 2)
	go func() { results <- first.Reconcile(context.Background()) }()
	go func() { results <- second.Reconcile(context.Background()) }()
	waitHotControllerSignal(t, barrier.arrived, "first snapshot")
	waitHotControllerSignal(t, barrier.arrived, "second snapshot")
	close(barrier.release)

	nilCount := 0
	conflictCount := 0
	for range 2 {
		err := <-results
		if err == nil {
			nilCount++
			continue
		}
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Code == modelsettingsdomain.ErrorCodeActivationConflict {
			conflictCount++
			continue
		}
		t.Fatalf("unexpected concurrent reconcile error: %v", err)
	}
	snapshot := store.currentSnapshot()
	if nilCount != 1 || conflictCount != 1 || store.advanceAttempts != 2 || store.advanceSuccesses != 1 ||
		snapshot.Rollout.Phase != modelsettingsdomain.RolloutPhaseArming || snapshot.Rollout.Version != 2 {
		t.Fatalf(
			"concurrent result nil/conflict=%d/%d attempts/success=%d/%d rollout=%+v",
			nilCount, conflictCount, store.advanceAttempts, store.advanceSuccesses, snapshot.Rollout,
		)
	}
}

func (store *hotControllerTestStore) seedRuntime(
	role modelsettingsdomain.RuntimeRole,
	instanceID foundation.ID,
	revision int64,
	phase modelsettingsdomain.RuntimePhase,
) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record := modelsettingsdomain.RuntimeRecord{
		Role: role, InstanceID: instanceID, AppliedRevision: revision, Phase: phase,
		AppliedAt: store.tickLocked(), HeartbeatAt: store.now, Fresh: true,
	}
	store.runtimes[role] = record
}

func (store *hotControllerTestStore) seedParticipant(
	role modelsettingsdomain.RuntimeRole,
	instanceID foundation.ID,
	target int64,
	phase modelsettingsdomain.ParticipantPhase,
) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record := modelsettingsdomain.ParticipantRecord{
		RolloutID: hotControllerTestRolloutID, Role: role, InstanceID: instanceID,
		TargetRevision: target, Phase: phase, Version: 1, HeartbeatAt: store.tickLocked(), Fresh: true,
	}
	if phase == modelsettingsdomain.ParticipantPhasePrepared {
		record.PreparedAt = record.HeartbeatAt
	}
	if phase == modelsettingsdomain.ParticipantPhaseActivated {
		record.ActivatedAt = record.HeartbeatAt
	}
	store.participants[role] = record
}

func hotControllerRolloutState(
	phase modelsettingsdomain.RolloutPhase,
	target int64,
	previous int64,
	version int64,
) modelsettingsdomain.RolloutState {
	return modelsettingsdomain.RolloutState{
		ID: hotControllerTestRolloutID, TargetRevision: target, PreviousActiveRevision: previous,
		Phase: phase, LeaseExpiresAt: hotControllerTestNow.Add(time.Minute), Version: version,
	}
}

func newHotControllerTestHost(
	t *testing.T,
	factory *runtimeHostTestFactory,
	role modelsettingsdomain.RuntimeRole,
	instanceID foundation.ID,
	revision int64,
) *RuntimeHost[*runtimeHostTestGeneration] {
	t.Helper()
	return newHotControllerTestHostWithValue(t, factory, role, instanceID, &runtimeHostTestGeneration{revision: revision})
}

func newHotControllerTestHostWithValue(
	t *testing.T,
	factory *runtimeHostTestFactory,
	role modelsettingsdomain.RuntimeRole,
	instanceID foundation.ID,
	initial *runtimeHostTestGeneration,
) *RuntimeHost[*runtimeHostTestGeneration] {
	t.Helper()
	return newHotControllerTestHostWithAvailability(t, factory, role, instanceID, initial, false)
}

func newHotControllerTestHostWithAvailability(
	t *testing.T,
	factory *runtimeHostTestFactory,
	role modelsettingsdomain.RuntimeRole,
	instanceID foundation.ID,
	initial *runtimeHostTestGeneration,
	unavailable bool,
) *RuntimeHost[*runtimeHostTestGeneration] {
	t.Helper()
	host, err := NewRuntimeHost(RuntimeHostOptions[*runtimeHostTestGeneration]{
		Initial: InitialRuntime[*runtimeHostTestGeneration]{
			Binding: RuntimeBinding{
				Mode: RuntimeModeManaged, Role: role, Revision: initial.revision, InstanceID: instanceID,
			},
			Value: initial, Unavailable: unavailable,
		},
		Factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func newHotControllerTestController(
	t *testing.T,
	store *hotControllerTestStore,
	host *RuntimeHost[*runtimeHostTestGeneration],
	initialPhase modelsettingsdomain.RuntimePhase,
) *HotRuntimeController[*runtimeHostTestGeneration] {
	t.Helper()
	controller, err := NewHotRuntimeController(HotRuntimeControllerOptions[*runtimeHostTestGeneration]{
		Host: host, Activation: store, Revisions: store, Runtime: store, InitialPhase: initialPhase,
		PollInterval: 10 * time.Millisecond, HeartbeatInterval: 100 * time.Millisecond, StaleAfter: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func newHotControllerTestCoordinator(t *testing.T, store *hotControllerTestStore) *ActivationCoordinator {
	t.Helper()
	coordinator, err := NewActivationCoordinator(ActivationCoordinatorOptions{
		Control: store, Revisions: store, PollInterval: 10 * time.Millisecond,
		LeaseDuration: 3 * time.Second, FreshWithin: 2 * time.Second, RenewInterval: time.Second,
		Clock: foundation.FixedClock{Value: hotControllerTestNow},
	})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func registerHotControllerServing(
	t *testing.T,
	store *hotControllerTestStore,
	host *RuntimeHost[*runtimeHostTestGeneration],
	phase modelsettingsdomain.RuntimePhase,
) {
	t.Helper()
	binding, ok := host.activeBinding()
	if !ok {
		t.Fatal("test host has no active binding")
	}
	_, err := store.RegisterRuntime(context.Background(), modelsettingsapplication.RuntimeRegistration{
		Role: binding.Role, InstanceID: binding.InstanceID, AppliedRevision: binding.Revision,
		Phase: phase, StaleAfter: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertHotControllerParticipantPhase(
	t *testing.T,
	summary modelsettingsdomain.ParticipantSummary,
	want modelsettingsdomain.ParticipantPhase,
) {
	t.Helper()
	if !summary.Present || !summary.Fresh || summary.Phase != want {
		t.Fatalf("participant summary = %+v, want fresh %q", summary, want)
	}
}

func assertHotControllerCurrentRevision(
	t *testing.T,
	host *RuntimeHost[*runtimeHostTestGeneration],
	want int64,
) {
	t.Helper()
	lease, err := host.Acquire(context.Background(), CurrentRuntime())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.Binding().Revision != want || lease.Value().revision != want {
		t.Fatalf("current lease = %v payload revision %d, want %d", lease.Binding(), lease.Value().revision, want)
	}
}

func assertHotControllerRestore(
	t *testing.T,
	store *hotControllerTestStore,
	role modelsettingsdomain.RuntimeRole,
	instanceID foundation.ID,
	revision int64,
	wantCalls int,
) {
	t.Helper()
	if len(store.restoreCommands) != wantCalls {
		t.Fatalf("availability restore calls = %d, want %d", len(store.restoreCommands), wantCalls)
	}
	if wantCalls == 0 {
		return
	}
	command := store.restoreCommands[wantCalls-1]
	if command.Role != role || command.InstanceID != instanceID || command.AppliedRevision != revision {
		t.Fatalf("availability restore command = %+v", command)
	}
}

func hotControllerGateState(host *RuntimeHost[*runtimeHostTestGeneration]) (bool, int64) {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.gateClosed, host.gateRevision
}

func assertHotControllerErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}

func assertHotControllerSignalOpen(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
		t.Fatal("signal closed unexpectedly")
	default:
	}
}

func assertHotControllerSignalClosed(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	default:
		t.Fatal("signal is still open")
	}
}

func waitHotControllerSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
