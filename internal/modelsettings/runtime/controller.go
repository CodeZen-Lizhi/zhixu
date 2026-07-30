package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const (
	defaultControlPollInterval = time.Second
	defaultHeartbeatInterval   = 5 * time.Second
)

// ControlService is the runtime ownership boundary used by API and Worker watchers.
type ControlService interface {
	Snapshot(context.Context) (modelsettingsdomain.Snapshot, error)
	RegisterRuntime(context.Context, modelsettingsapplication.RuntimeRegistration) (modelsettingsdomain.RuntimeRecord, error)
	HeartbeatRuntime(context.Context, modelsettingsapplication.RuntimeHeartbeat) (modelsettingsdomain.RuntimeRecord, error)
	SetRuntimePhase(context.Context, modelsettingsapplication.RuntimePhaseCommand) (modelsettingsdomain.RuntimeRecord, error)
}

// DrainHooks stop local producers and report when already claimed work is finished.
type DrainHooks struct {
	Begin      func(context.Context) error
	IsQuiesced func(context.Context) (bool, error)
	Resume     func(context.Context) error
}

// ControllerOptions fixes one process role, instance, revision, and rollout binding.
type ControllerOptions struct {
	Service           ControlService
	Role              modelsettingsdomain.RuntimeRole
	InstanceID        foundation.ID
	Loaded            LoadedSettings
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	Drain             DrainHooks
}

// Controller owns one process runtime record and fails closed when its CAS ownership is lost.
type Controller struct {
	service           ControlService
	role              modelsettingsdomain.RuntimeRole
	instanceID        foundation.ID
	revision          int64
	rolloutID         *foundation.ID
	phase             modelsettingsdomain.RuntimePhase
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	drain             DrainHooks
	drainStarted      bool
	active            chan struct{}
	activeOnce        sync.Once
}

// NewController creates a managed runtime watcher.
func NewController(options ControllerOptions) (*Controller, error) {
	if options.Service == nil || !modelsettingsdomain.ValidRuntimeRole(options.Role) || !validRuntimeID(options.InstanceID) ||
		options.Loaded.Revision < 0 || !modelsettingsdomain.ValidRuntimePhase(options.Loaded.InitialPhase) {
		return nil, invalid(errors.New("model runtime controller options are invalid"))
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultControlPollInterval
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = defaultHeartbeatInterval
	}
	if options.PollInterval < 100*time.Millisecond || options.PollInterval > time.Minute ||
		options.HeartbeatInterval < time.Second || options.HeartbeatInterval > time.Minute {
		return nil, invalid(errors.New("model runtime controller intervals are invalid"))
	}
	controller := &Controller{
		service: options.Service, role: options.Role, instanceID: options.InstanceID,
		revision: options.Loaded.Revision, rolloutID: cloneID(options.Loaded.RolloutID), phase: options.Loaded.InitialPhase,
		pollInterval: options.PollInterval, heartbeatInterval: options.HeartbeatInterval, drain: options.Drain, active: make(chan struct{}),
	}
	return controller, nil
}

// Active closes after this process is allowed to start external work.
func (controller *Controller) Active() <-chan struct{} {
	if controller == nil || controller.active == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return controller.active
}

// Run registers ownership, heartbeats it, and reconciles rollout phase until cancellation or ownership loss.
func (controller *Controller) Run(ctx context.Context) error {
	if controller == nil {
		return invalid(errors.New("model runtime controller is nil"))
	}
	if _, err := controller.service.RegisterRuntime(ctx, modelsettingsapplication.RuntimeRegistration{
		Role: controller.role, InstanceID: controller.instanceID, AppliedRevision: controller.revision,
		RolloutID: cloneID(controller.rolloutID), Phase: controller.phase,
	}); err != nil {
		return err
	}
	if controller.phase == modelsettingsdomain.RuntimePhaseActive || controller.phase == modelsettingsdomain.RuntimePhaseUnavailable {
		controller.markActive()
	}
	poll := time.NewTicker(controller.pollInterval)
	heartbeat := time.NewTicker(controller.heartbeatInterval)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			if err := controller.reconcile(ctx); err != nil {
				return err
			}
		case <-heartbeat.C:
			if err := controller.heartbeat(ctx); err != nil {
				return err
			}
		}
	}
}

func (controller *Controller) reconcile(ctx context.Context) error {
	snapshot, err := controller.service.Snapshot(ctx)
	if err != nil {
		return err
	}
	if controller.rolloutID != nil && (controller.phase == modelsettingsdomain.RuntimePhasePrepared || controller.phase == modelsettingsdomain.RuntimePhaseVerifying) {
		return controller.reconcileCandidate(ctx, snapshot)
	}
	if controller.phase == modelsettingsdomain.RuntimePhaseQuiescing || controller.phase == modelsettingsdomain.RuntimePhaseQuiesced {
		if snapshot.Rollout.Phase == modelsettingsdomain.RolloutPhaseFailed || snapshot.Rollout.Phase == modelsettingsdomain.RolloutPhaseIdle {
			if snapshot.ActiveRevision != controller.revision {
				return foundation.NewError(foundation.ErrorVersionConflict, modelsettingsdomain.ErrorCodeRuntimeConflict, false, errors.New("drained runtime revision is no longer active"))
			}
			if controller.drain.Resume != nil {
				if err := controller.drain.Resume(ctx); err != nil {
					return err
				}
			}
			controller.phase = modelsettingsdomain.RuntimePhaseActive
			controller.rolloutID = nil
			controller.drainStarted = false
			return nil
		}
	}
	if controller.phase == modelsettingsdomain.RuntimePhaseActive && snapshot.Rollout.Phase == modelsettingsdomain.RolloutPhaseDraining && snapshot.ActiveRevision == controller.revision {
		if controller.drain.Begin != nil && !controller.drainStarted {
			if err := controller.drain.Begin(ctx); err != nil {
				return err
			}
		}
		controller.drainStarted = true
		rolloutID := snapshot.Rollout.ID
		if !validRuntimeID(rolloutID) {
			return foundation.NewError(foundation.ErrorConsistencyViolation, modelsettingsdomain.ErrorCodeCorrupt, false, errors.New("draining rollout identifier is invalid"))
		}
		if _, err := controller.service.SetRuntimePhase(ctx, modelsettingsapplication.RuntimePhaseCommand{
			Role: controller.role, InstanceID: controller.instanceID, RolloutID: &rolloutID,
			ExpectedPhase: modelsettingsdomain.RuntimePhaseActive, NextPhase: modelsettingsdomain.RuntimePhaseQuiescing,
		}); err != nil {
			return err
		}
		controller.rolloutID = &rolloutID
		controller.phase = modelsettingsdomain.RuntimePhaseQuiescing
	}
	if controller.phase == modelsettingsdomain.RuntimePhaseQuiescing && controller.drain.IsQuiesced != nil {
		quiesced, err := controller.drain.IsQuiesced(ctx)
		if err != nil {
			return err
		}
		if quiesced {
			if _, err := controller.service.SetRuntimePhase(ctx, modelsettingsapplication.RuntimePhaseCommand{
				Role: controller.role, InstanceID: controller.instanceID, RolloutID: cloneID(controller.rolloutID),
				ExpectedPhase: modelsettingsdomain.RuntimePhaseQuiescing, NextPhase: modelsettingsdomain.RuntimePhaseQuiesced,
			}); err != nil {
				return err
			}
			controller.phase = modelsettingsdomain.RuntimePhaseQuiesced
		}
	}
	return nil
}

func (controller *Controller) reconcileCandidate(ctx context.Context, snapshot modelsettingsdomain.Snapshot) error {
	if snapshot.Rollout.Phase == modelsettingsdomain.RolloutPhaseIdle && snapshot.ActiveRevision == controller.revision {
		controller.rolloutID = nil
		controller.phase = modelsettingsdomain.RuntimePhaseActive
		controller.markActive()
		return nil
	}
	if snapshot.Rollout.ID != *controller.rolloutID || snapshot.Rollout.TargetRevision != controller.revision || snapshot.Rollout.Phase == modelsettingsdomain.RolloutPhaseFailed {
		return foundation.NewError(foundation.ErrorVersionConflict, modelsettingsdomain.ErrorCodeRuntimeConflict, false, errors.New("candidate runtime lost rollout ownership"))
	}
	if controller.phase == modelsettingsdomain.RuntimePhasePrepared && snapshot.Rollout.Phase == modelsettingsdomain.RolloutPhaseVerifying {
		if _, err := controller.service.SetRuntimePhase(ctx, modelsettingsapplication.RuntimePhaseCommand{
			Role: controller.role, InstanceID: controller.instanceID, RolloutID: cloneID(controller.rolloutID),
			ExpectedPhase: modelsettingsdomain.RuntimePhasePrepared, NextPhase: modelsettingsdomain.RuntimePhaseVerifying,
		}); err != nil {
			return err
		}
		controller.phase = modelsettingsdomain.RuntimePhaseVerifying
	}
	return nil
}

func (controller *Controller) markActive() {
	controller.activeOnce.Do(func() { close(controller.active) })
}

func (controller *Controller) heartbeat(ctx context.Context) error {
	_, err := controller.service.HeartbeatRuntime(ctx, modelsettingsapplication.RuntimeHeartbeat{
		Role: controller.role, InstanceID: controller.instanceID, RolloutID: cloneID(controller.rolloutID),
	})
	if err == nil {
		return nil
	}
	if reconcileErr := controller.reconcile(ctx); reconcileErr != nil {
		return err
	}
	_, retryErr := controller.service.HeartbeatRuntime(ctx, modelsettingsapplication.RuntimeHeartbeat{
		Role: controller.role, InstanceID: controller.instanceID, RolloutID: cloneID(controller.rolloutID),
	})
	return retryErr
}

func cloneID(id *foundation.ID) *foundation.ID {
	if id == nil {
		return nil
	}
	cloned := *id
	return &cloned
}

func validRuntimeID(id foundation.ID) bool {
	parsed, err := foundation.ParseID(string(id))
	return err == nil && parsed == id
}
