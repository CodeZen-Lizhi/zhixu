// Package runtimegrant keeps API and Worker ownership aligned with the
// controller's active Workspace grant.
package runtimegrant

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	workspaceapplication "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	defaultHeartbeatInterval = 5 * time.Second
	defaultFreshWithin       = 20 * time.Second
)

// Service is the runtime ownership subset of Workspace control.
type Service interface {
	Snapshot(context.Context, time.Duration) (workspacedomain.ControlSnapshot, error)
	RegisterRuntime(context.Context, workspaceapplication.RuntimeRegistration) (workspacedomain.RuntimeRecord, error)
	HeartbeatRuntime(context.Context, workspaceapplication.RuntimeHeartbeat) (workspacedomain.RuntimeRecord, error)
	SetRuntimePhase(context.Context, workspaceapplication.RuntimePhaseCommand) (workspacedomain.RuntimeRecord, error)
}

// QuiescenceHooks stop new root-bound work, report a safe checkpoint, and
// resume only when the controller cancels before revoking the grant.
type QuiescenceHooks struct {
	Begin      func(context.Context) error
	IsQuiesced func(context.Context) (bool, error)
	Resume     func(context.Context) error
}

func (hooks QuiescenceHooks) valid() bool {
	return hooks.Begin != nil && hooks.IsQuiesced != nil && hooks.Resume != nil
}

// Lease heartbeats one real process owner until shutdown or authority loss.
type Lease struct {
	service  Service
	record   workspacedomain.RuntimeRecord
	cancel   context.CancelFunc
	done     chan struct{}
	errors   chan error
	interval time.Duration
	mu       sync.Mutex
	hooks    QuiescenceHooks
}

// Start registers a managed API or Worker only after the exact grant is active.
func Start(ctx context.Context, service Service, grant rootgrant.ProcessGrant, role workspacedomain.RuntimeRole) (*Lease, error) {
	if ctx == nil || service == nil || !workspacedomain.ValidRuntimeRole(role) {
		return nil, errors.New("workspace runtime grant dependencies are invalid")
	}
	snapshot, err := service.Snapshot(ctx, defaultFreshWithin)
	if err != nil {
		return nil, err
	}
	if snapshot.Active == nil || snapshot.State.ActiveWorkspaceID == nil ||
		*snapshot.State.ActiveWorkspaceID != grant.WorkspaceID() || snapshot.Active.ID != grant.WorkspaceID() ||
		snapshot.Active.RootPath != grant.CanonicalRoot() || snapshot.State.GrantGeneration != grant.Generation() ||
		snapshot.Active.Availability != workspacedomain.WorkspaceAvailabilityAvailable ||
		snapshot.Active.BindingVersion < 1 || snapshot.Active.RootFingerprint == "" {
		return nil, foundation.NewError(foundation.ErrorPermissionDenied, rootgrant.ErrorCodeRootNotGranted, false, errors.New("workspace process grant is not active"))
	}
	instanceID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return nil, err
	}
	record, err := service.RegisterRuntime(ctx, workspaceapplication.RuntimeRegistration{
		Role: role, InstanceID: instanceID, WorkspaceID: grant.WorkspaceID(),
		GrantGeneration: grant.Generation(), RootFingerprint: snapshot.Active.RootFingerprint,
		BindingVersion: snapshot.Active.BindingVersion, Phase: workspacedomain.RuntimePhaseActive,
	})
	if err != nil {
		return nil, err
	}
	runContext, cancel := context.WithCancel(context.Background())
	lease := &Lease{
		service: service, record: record, cancel: cancel, done: make(chan struct{}),
		errors: make(chan error, 1), interval: defaultHeartbeatInterval,
	}
	go lease.run(runContext)
	return lease, nil
}

// Errors reports the first authority loss. The channel stays open otherwise.
func (lease *Lease) Errors() <-chan error {
	if lease == nil {
		return nil
	}
	return lease.errors
}

// SetQuiescenceHooks attaches the process-local drain boundary before the
// runtime can participate in a Workspace switch.
func (lease *Lease) SetQuiescenceHooks(hooks QuiescenceHooks) error {
	if lease == nil || !hooks.valid() {
		return errors.New("workspace runtime quiescence hooks are invalid")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.hooks = hooks
	return nil
}

func (lease *Lease) run(ctx context.Context) {
	defer close(lease.done)
	ticker := time.NewTicker(lease.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := lease.tick(ctx)
			if err != nil {
				select {
				case lease.errors <- err:
				default:
				}
				return
			}
		}
	}
}

func (lease *Lease) tick(ctx context.Context) error {
	lease.mu.Lock()
	record := lease.record
	hooks := lease.hooks
	lease.mu.Unlock()

	snapshot, err := lease.service.Snapshot(ctx, defaultFreshWithin)
	if err != nil {
		return err
	}
	if persisted, found := processRuntime(snapshot.Runtimes, record.Role, record.InstanceID); found && persisted.Version >= record.Version {
		if (record.Phase == workspacedomain.RuntimePhaseQuiescing || record.Phase == workspacedomain.RuntimePhaseQuiesced) &&
			persisted.Phase == workspacedomain.RuntimePhaseActive && persisted.OperationID == nil {
			if !hooks.valid() {
				return errors.New("workspace runtime cannot resume without quiescence hooks")
			}
			if err := hooks.Resume(ctx); err != nil {
				return err
			}
		}
		record = persisted
	}

	operation := snapshot.Operation
	quiescing := operation != nil && operation.Result == "" && operation.Phase == workspacedomain.SwitchPhaseQuiescing &&
		operation.PreviousWorkspaceID != nil && *operation.PreviousWorkspaceID == record.WorkspaceID &&
		record.GrantGeneration == operation.GrantGeneration-1
	if quiescing && record.Phase == workspacedomain.RuntimePhaseActive {
		if !hooks.valid() {
			return errors.New("workspace runtime cannot quiesce without process hooks")
		}
		if err := hooks.Begin(ctx); err != nil {
			return err
		}
		updated, transitionErr := lease.service.SetRuntimePhase(ctx, workspaceapplication.RuntimePhaseCommand{
			Role: record.Role, InstanceID: record.InstanceID, OperationID: &operation.ID,
			ExpectedPhase: record.Phase, NextPhase: workspacedomain.RuntimePhaseQuiescing,
			ExpectedRuntimeVersion: record.Version,
		})
		if transitionErr != nil {
			_ = hooks.Resume(context.WithoutCancel(ctx))
			return transitionErr
		}
		record = updated
	}
	if quiescing && record.Phase == workspacedomain.RuntimePhaseQuiescing {
		quiesced, quiesceErr := hooks.IsQuiesced(ctx)
		if quiesceErr != nil {
			return quiesceErr
		}
		if quiesced {
			updated, transitionErr := lease.service.SetRuntimePhase(ctx, workspaceapplication.RuntimePhaseCommand{
				Role: record.Role, InstanceID: record.InstanceID, OperationID: &operation.ID,
				ExpectedPhase: record.Phase, NextPhase: workspacedomain.RuntimePhaseQuiesced,
				ExpectedRuntimeVersion: record.Version,
			})
			if transitionErr != nil {
				return transitionErr
			}
			record = updated
		}
	}

	updated, err := lease.service.HeartbeatRuntime(ctx, workspaceapplication.RuntimeHeartbeat{
		Role: record.Role, InstanceID: record.InstanceID,
		OperationID: record.OperationID, ExpectedRuntimeVersion: record.Version,
	})
	if err != nil {
		return err
	}
	lease.mu.Lock()
	lease.record = updated
	lease.mu.Unlock()
	return nil
}

func processRuntime(records []workspacedomain.RuntimeRecord, role workspacedomain.RuntimeRole, instanceID foundation.ID) (workspacedomain.RuntimeRecord, bool) {
	for _, record := range records {
		if record.Role == role && record.InstanceID == instanceID {
			return record, true
		}
	}
	return workspacedomain.RuntimeRecord{}, false
}

// Close 在调用方期限内等待心跳停止，再把当前精确进程 owner 标为 unavailable。
// 等待超时不释放仍在使用的依赖；调用方必须保留它们或等待进程退出。
func (lease *Lease) Close(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("workspace runtime shutdown context is nil")
	}
	lease.cancel()
	select {
	case <-lease.done:
	default:
		select {
		case <-lease.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.record.Phase == workspacedomain.RuntimePhaseUnavailable {
		return nil
	}
	record, err := lease.service.SetRuntimePhase(ctx, workspaceapplication.RuntimePhaseCommand{
		Role: lease.record.Role, InstanceID: lease.record.InstanceID,
		OperationID: lease.record.OperationID, ExpectedPhase: lease.record.Phase,
		NextPhase: workspacedomain.RuntimePhaseUnavailable, ExpectedRuntimeVersion: lease.record.Version,
	})
	if err != nil {
		return err
	}
	lease.record = record
	return nil
}
