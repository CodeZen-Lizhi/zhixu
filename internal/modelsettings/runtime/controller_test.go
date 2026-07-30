package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

type controlServiceStub struct {
	snapshot    domain.Snapshot
	transitions []application.RuntimePhaseCommand
}

func (stub *controlServiceStub) Snapshot(context.Context) (domain.Snapshot, error) {
	return stub.snapshot, nil
}
func (stub *controlServiceStub) RegisterRuntime(context.Context, application.RuntimeRegistration) (domain.RuntimeRecord, error) {
	return domain.RuntimeRecord{}, nil
}
func (stub *controlServiceStub) HeartbeatRuntime(context.Context, application.RuntimeHeartbeat) (domain.RuntimeRecord, error) {
	return domain.RuntimeRecord{}, nil
}
func (stub *controlServiceStub) SetRuntimePhase(_ context.Context, command application.RuntimePhaseCommand) (domain.RuntimeRecord, error) {
	stub.transitions = append(stub.transitions, command)
	return domain.RuntimeRecord{Phase: command.NextPhase}, nil
}

type lifecycleControlService struct {
	snapshotFn   func(context.Context) (domain.Snapshot, error)
	registerFn   func(context.Context, application.RuntimeRegistration) (domain.RuntimeRecord, error)
	heartbeatFn  func(context.Context, application.RuntimeHeartbeat) (domain.RuntimeRecord, error)
	transitionFn func(context.Context, application.RuntimePhaseCommand) (domain.RuntimeRecord, error)
}

func (service *lifecycleControlService) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	if service.snapshotFn != nil {
		return service.snapshotFn(ctx)
	}
	return domain.Snapshot{}, nil
}

func (service *lifecycleControlService) RegisterRuntime(ctx context.Context, registration application.RuntimeRegistration) (domain.RuntimeRecord, error) {
	if service.registerFn != nil {
		return service.registerFn(ctx, registration)
	}
	return domain.RuntimeRecord{Phase: registration.Phase}, nil
}

func (service *lifecycleControlService) HeartbeatRuntime(ctx context.Context, heartbeat application.RuntimeHeartbeat) (domain.RuntimeRecord, error) {
	if service.heartbeatFn != nil {
		return service.heartbeatFn(ctx, heartbeat)
	}
	return domain.RuntimeRecord{}, nil
}

func (service *lifecycleControlService) SetRuntimePhase(ctx context.Context, command application.RuntimePhaseCommand) (domain.RuntimeRecord, error) {
	if service.transitionFn != nil {
		return service.transitionFn(ctx, command)
	}
	return domain.RuntimeRecord{Phase: command.NextPhase}, nil
}

func TestControllerRunRegistersBeforeSignalingActive(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registrations := make(chan application.RuntimeRegistration, 1)
	releaseRegistration := make(chan struct{})
	service := &lifecycleControlService{
		registerFn: func(ctx context.Context, registration application.RuntimeRegistration) (domain.RuntimeRecord, error) {
			registrations <- registration
			select {
			case <-releaseRegistration:
				return domain.RuntimeRecord{Phase: registration.Phase}, nil
			case <-ctx.Done():
				return domain.RuntimeRecord{}, ctx.Err()
			}
		},
	}
	instanceID := foundation.ID("20000000-0000-4000-8000-000000000002")
	controller, err := NewController(ControllerOptions{
		Service: service, Role: domain.RuntimeRoleAPI, InstanceID: instanceID,
		Loaded:       LoadedSettings{Revision: 3, InitialPhase: domain.RuntimePhaseActive},
		PollInterval: time.Minute, HeartbeatInterval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	runResult := make(chan error, 1)
	go func() { runResult <- controller.Run(ctx) }()

	select {
	case registration := <-registrations:
		if registration.Role != domain.RuntimeRoleAPI || registration.InstanceID != instanceID ||
			registration.AppliedRevision != 3 || registration.RolloutID != nil || registration.Phase != domain.RuntimePhaseActive {
			t.Fatalf("registration=%+v", registration)
		}
	case <-time.After(time.Second):
		t.Fatal("controller did not attempt runtime registration")
	}
	select {
	case <-controller.Active():
		t.Fatal("controller activated before runtime registration completed")
	default:
	}

	close(releaseRegistration)
	select {
	case <-controller.Active():
	case err := <-runResult:
		t.Fatalf("controller stopped before activation: %v", err)
	case <-time.After(time.Second):
		t.Fatal("controller did not activate after runtime registration")
	}
	cancel()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("controller shutdown error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("controller did not stop after cancellation")
	}
}

func TestControllerRunKeepsCandidateInactiveUntilCommittedRevision(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rolloutID := foundation.ID("10000000-0000-4000-8000-000000000001")
	registered := make(chan application.RuntimeRegistration, 1)
	snapshots := make(chan domain.Snapshot, 2)
	transitions := make(chan application.RuntimePhaseCommand, 1)
	service := &lifecycleControlService{
		registerFn: func(_ context.Context, registration application.RuntimeRegistration) (domain.RuntimeRecord, error) {
			registered <- registration
			return domain.RuntimeRecord{Phase: registration.Phase}, nil
		},
		snapshotFn: func(ctx context.Context) (domain.Snapshot, error) {
			select {
			case snapshot := <-snapshots:
				return snapshot, nil
			case <-ctx.Done():
				return domain.Snapshot{}, ctx.Err()
			}
		},
		transitionFn: func(_ context.Context, command application.RuntimePhaseCommand) (domain.RuntimeRecord, error) {
			transitions <- command
			return domain.RuntimeRecord{Phase: command.NextPhase}, nil
		},
	}
	controller, err := NewController(ControllerOptions{
		Service: service, Role: domain.RuntimeRoleAPI,
		InstanceID:   foundation.ID("20000000-0000-4000-8000-000000000002"),
		Loaded:       LoadedSettings{Revision: 5, RolloutID: &rolloutID, InitialPhase: domain.RuntimePhasePrepared},
		PollInterval: 100 * time.Millisecond, HeartbeatInterval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	runResult := make(chan error, 1)
	go func() { runResult <- controller.Run(ctx) }()

	select {
	case registration := <-registered:
		if registration.RolloutID == nil || *registration.RolloutID != rolloutID || registration.Phase != domain.RuntimePhasePrepared {
			t.Fatalf("registration=%+v", registration)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate runtime was not registered")
	}
	assertControllerInactive(t, controller.Active(), "candidate registration")

	snapshots <- domain.Snapshot{Rollout: domain.RolloutState{
		ID: rolloutID, TargetRevision: 5, Phase: domain.RolloutPhaseVerifying,
	}}
	select {
	case transition := <-transitions:
		if transition.ExpectedPhase != domain.RuntimePhasePrepared || transition.NextPhase != domain.RuntimePhaseVerifying {
			t.Fatalf("transition=%+v", transition)
		}
	case err := <-runResult:
		t.Fatalf("candidate stopped before verifying: %v", err)
	case <-time.After(time.Second):
		t.Fatal("candidate did not enter verifying")
	}
	assertControllerInactive(t, controller.Active(), "candidate verification")

	snapshots <- domain.Snapshot{ActiveRevision: 5, Rollout: domain.RolloutState{Phase: domain.RolloutPhaseIdle}}
	select {
	case <-controller.Active():
	case err := <-runResult:
		t.Fatalf("candidate stopped before commit activation: %v", err)
	case <-time.After(time.Second):
		t.Fatal("candidate did not activate after commit")
	}
	cancel()
	select {
	case err := <-runResult:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("candidate shutdown error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate did not stop after cancellation")
	}
}

func TestControllerRunReturnsAndStopsAfterHeartbeatOwnershipLoss(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ownershipLost := foundation.NewError(
		foundation.ErrorVersionConflict,
		domain.ErrorCodeRuntimeConflict,
		false,
		errors.New("runtime row belongs to another instance"),
	)
	heartbeats := make(chan application.RuntimeHeartbeat, 3)
	service := &lifecycleControlService{
		heartbeatFn: func(_ context.Context, heartbeat application.RuntimeHeartbeat) (domain.RuntimeRecord, error) {
			heartbeats <- heartbeat
			return domain.RuntimeRecord{}, ownershipLost
		},
	}
	instanceID := foundation.ID("20000000-0000-4000-8000-000000000002")
	controller, err := NewController(ControllerOptions{
		Service: service, Role: domain.RuntimeRoleAPI, InstanceID: instanceID,
		Loaded:       LoadedSettings{Revision: 3, InitialPhase: domain.RuntimePhaseActive},
		PollInterval: time.Minute, HeartbeatInterval: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runResult := make(chan error, 1)
	go func() { runResult <- controller.Run(ctx) }()
	select {
	case <-controller.Active():
	case <-time.After(time.Second):
		t.Fatal("controller did not activate after registration")
	}

	select {
	case err := <-runResult:
		if !errors.Is(err, ownershipLost) {
			t.Fatalf("controller error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("controller did not stop after ownership loss")
	}
	if got := len(heartbeats); got != 2 {
		t.Fatalf("heartbeat attempts=%d, want initial CAS plus one reconciliation retry", got)
	}
	for len(heartbeats) > 0 {
		heartbeat := <-heartbeats
		if heartbeat.Role != domain.RuntimeRoleAPI || heartbeat.InstanceID != instanceID || heartbeat.RolloutID != nil {
			t.Fatalf("heartbeat=%+v", heartbeat)
		}
	}
	select {
	case heartbeat := <-heartbeats:
		t.Fatalf("controller continued heartbeating after return: %+v", heartbeat)
	case <-time.After(150 * time.Millisecond):
	}
}

func assertControllerInactive(t *testing.T, active <-chan struct{}, stage string) {
	t.Helper()
	select {
	case <-active:
		t.Fatalf("controller activated during %s", stage)
	default:
	}
}

func TestControllerDrainsOldRuntimeToQuiesced(t *testing.T) {
	t.Parallel()

	rolloutID := foundation.ID("10000000-0000-4000-8000-000000000001")
	service := &controlServiceStub{snapshot: domain.Snapshot{ActiveRevision: 3, Rollout: domain.RolloutState{ID: rolloutID, Phase: domain.RolloutPhaseDraining}}}
	began := false
	controller, err := NewController(ControllerOptions{
		Service: service, Role: domain.RuntimeRoleWorker, InstanceID: foundation.ID("20000000-0000-4000-8000-000000000002"),
		Loaded: LoadedSettings{Revision: 3, InitialPhase: domain.RuntimePhaseActive},
		Drain: DrainHooks{
			Begin:      func(context.Context) error { began = true; return nil },
			IsQuiesced: func(context.Context) (bool, error) { return true, nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !began || controller.phase != domain.RuntimePhaseQuiesced || len(service.transitions) != 2 ||
		service.transitions[0].ExpectedPhase != domain.RuntimePhaseActive || service.transitions[1].NextPhase != domain.RuntimePhaseQuiesced {
		t.Fatalf("phase=%s transitions=%+v began=%t", controller.phase, service.transitions, began)
	}
}

func TestControllerAdvancesCandidateAndObservesCommit(t *testing.T) {
	t.Parallel()

	rolloutID := foundation.ID("10000000-0000-4000-8000-000000000001")
	service := &controlServiceStub{snapshot: domain.Snapshot{Rollout: domain.RolloutState{ID: rolloutID, TargetRevision: 5, Phase: domain.RolloutPhaseVerifying}}}
	controller, err := NewController(ControllerOptions{
		Service: service, Role: domain.RuntimeRoleAPI, InstanceID: foundation.ID("20000000-0000-4000-8000-000000000002"),
		Loaded: LoadedSettings{Revision: 5, RolloutID: &rolloutID, InitialPhase: domain.RuntimePhasePrepared},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if controller.phase != domain.RuntimePhaseVerifying || len(service.transitions) != 1 {
		t.Fatalf("phase=%s transitions=%+v", controller.phase, service.transitions)
	}
	service.snapshot = domain.Snapshot{ActiveRevision: 5, Rollout: domain.RolloutState{Phase: domain.RolloutPhaseIdle}}
	if err := controller.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if controller.phase != domain.RuntimePhaseActive || controller.rolloutID != nil {
		t.Fatalf("phase=%s rollout=%v", controller.phase, controller.rolloutID)
	}
}

func TestControllerResumesDrainedRuntimeOnlyWhileItsRevisionIsActive(t *testing.T) {
	t.Parallel()

	for _, runtimePhase := range []domain.RuntimePhase{domain.RuntimePhaseQuiescing, domain.RuntimePhaseQuiesced} {
		for _, rolloutPhase := range []domain.RolloutPhase{domain.RolloutPhaseFailed, domain.RolloutPhaseIdle} {
			t.Run(string(runtimePhase)+"_"+string(rolloutPhase), func(t *testing.T) {
				t.Parallel()
				resumeCalls := 0
				service := &controlServiceStub{snapshot: domain.Snapshot{ActiveRevision: 3, Rollout: domain.RolloutState{Phase: rolloutPhase}}}
				controller := newDrainedController(t, service, runtimePhase, func(context.Context) error {
					resumeCalls++
					return nil
				})

				if err := controller.reconcile(context.Background()); err != nil {
					t.Fatal(err)
				}
				if resumeCalls != 1 || controller.phase != domain.RuntimePhaseActive || controller.rolloutID != nil || controller.drainStarted {
					t.Fatalf("resume calls=%d phase=%s rollout=%v drain_started=%t", resumeCalls, controller.phase, controller.rolloutID, controller.drainStarted)
				}
			})
		}
	}
}

func TestControllerRejectsCommittedRevisionBeforeResumingOldRuntime(t *testing.T) {
	t.Parallel()

	for _, runtimePhase := range []domain.RuntimePhase{domain.RuntimePhaseQuiescing, domain.RuntimePhaseQuiesced} {
		for _, rolloutPhase := range []domain.RolloutPhase{domain.RolloutPhaseFailed, domain.RolloutPhaseIdle} {
			t.Run(string(runtimePhase)+"_"+string(rolloutPhase), func(t *testing.T) {
				t.Parallel()
				resumeCalls := 0
				service := &controlServiceStub{snapshot: domain.Snapshot{ActiveRevision: 5, Rollout: domain.RolloutState{Phase: rolloutPhase}}}
				controller := newDrainedController(t, service, runtimePhase, func(context.Context) error {
					resumeCalls++
					return nil
				})

				err := controller.reconcile(context.Background())
				var classified *foundation.Error
				if !errors.As(err, &classified) || classified.Kind != foundation.ErrorVersionConflict || classified.Code != domain.ErrorCodeRuntimeConflict {
					t.Fatalf("error = %v", err)
				}
				if resumeCalls != 0 || controller.phase != runtimePhase || controller.rolloutID == nil || !controller.drainStarted {
					t.Fatalf("resume calls=%d phase=%s rollout=%v drain_started=%t", resumeCalls, controller.phase, controller.rolloutID, controller.drainStarted)
				}
			})
		}
	}
}

func newDrainedController(t *testing.T, service ControlService, phase domain.RuntimePhase, resume func(context.Context) error) *Controller {
	t.Helper()
	rolloutID := foundation.ID("10000000-0000-4000-8000-000000000001")
	controller, err := NewController(ControllerOptions{
		Service: service, Role: domain.RuntimeRoleWorker, InstanceID: foundation.ID("20000000-0000-4000-8000-000000000002"),
		Loaded: LoadedSettings{Revision: 3, RolloutID: &rolloutID, InitialPhase: phase},
		Drain:  DrainHooks{Resume: resume},
	})
	if err != nil {
		t.Fatal(err)
	}
	controller.drainStarted = true
	return controller
}
