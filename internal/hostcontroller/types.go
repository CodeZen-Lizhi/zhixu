// Package hostcontroller owns the host-only Workspace grant and control-plane boundary.
package hostcontroller

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// RuntimeStatus describes the effective business runtime exposed by the controller.
type RuntimeStatus string

const (
	RuntimeWaitingForWorkspace RuntimeStatus = "waiting_for_workspace"
	RuntimeSwitching           RuntimeStatus = "switching"
	RuntimeReady               RuntimeStatus = "ready"
	RuntimeRecoveryFailed      RuntimeStatus = "recovery_failed"
)

// ProcessStatus describes one business process without implying a Workspace grant.
type ProcessStatus string

const (
	ProcessStopped     ProcessStatus = "stopped"
	ProcessStarting    ProcessStatus = "starting"
	ProcessPrepared    ProcessStatus = "prepared"
	ProcessReady       ProcessStatus = "ready"
	ProcessUnavailable ProcessStatus = "unavailable"
)

// Availability describes whether a registered host root can be selected safely.
type Availability string

const (
	AvailabilityAvailable         Availability = "available"
	AvailabilityUnavailable       Availability = "unavailable"
	AvailabilityMigrationRequired Availability = "migration_required"
)

// RuntimeState is the controller-owned view of API and Worker readiness.
type RuntimeState struct {
	Status RuntimeStatus `json:"status"`
	API    ProcessState  `json:"api"`
	Worker ProcessState  `json:"worker"`
}

// ProcessState is the bounded status for one runtime role.
type ProcessState struct {
	Status ProcessStatus `json:"status"`
}

// Workspace is the controller-safe registry projection returned to an authenticated browser.
type Workspace struct {
	WorkspaceID        string       `json:"workspace_id"`
	Name               string       `json:"name"`
	RootPath           string       `json:"root_path"`
	Availability       Availability `json:"availability"`
	AvailabilityReason string       `json:"availability_reason,omitempty"`
	LastOpenedAt       *time.Time   `json:"last_opened_at,omitempty"`
}

// Operation is the resumable Workspace switch projection.
type Operation struct {
	OperationID       string    `json:"operation_id"`
	Phase             string    `json:"phase"`
	Result            string    `json:"result,omitempty"`
	TargetWorkspaceID string    `json:"target_workspace_id,omitempty"`
	TargetName        string    `json:"target_name,omitempty"`
	ErrorCode         string    `json:"error_code,omitempty"`
	Retryable         bool      `json:"retryable"`
	StartedAt         time.Time `json:"started_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// State is the authenticated browser bootstrap source for full control state.
type State struct {
	ControllerInstanceID string       `json:"controller_instance_id"`
	StateVersion         int64        `json:"state_version"`
	Runtime              RuntimeState `json:"runtime"`
	ActiveWorkspace      *Workspace   `json:"active_workspace"`
	RecentWorkspaces     []Workspace  `json:"recent_workspaces"`
	Operation            *Operation   `json:"operation"`
	PollAfterMS          int          `json:"poll_after_ms"`
}

// SwitchCommand is the validated, typed input passed to the persistent state owner.
type SwitchCommand struct {
	ExpectedStateVersion int64
	ControllerInstanceID string
	IdempotencyKey       string
	RequestHash          string
	TargetKind           string
	WorkspaceID          string
	Name                 string
	RootPath             string
	RootFingerprint      string
	BindingVersion       int64
	InitializeGit        bool
}

// Store is the narrow persistent-state seam used by the native controller.
// Implementations own leases, CAS transitions, registry identity, and durable operations.
type Store interface {
	State(context.Context) (State, error)
	BeginSwitch(context.Context, SwitchCommand) (Operation, error)
	Operation(context.Context, string) (Operation, error)
	CheckAvailability(context.Context, string, int64) (Workspace, error)
	RemoveWorkspace(context.Context, string, int64) error
}

// StateReader is the read-only subset needed to gate the business proxy.
type StateReader interface {
	State(context.Context) (State, error)
}

// RuntimeAccessStatus is the path-free browser discovery state.
type RuntimeAccessStatus string

const (
	RuntimeAccessWaiting     RuntimeAccessStatus = "waiting"
	RuntimeAccessReady       RuntimeAccessStatus = "ready"
	RuntimeAccessUnavailable RuntimeAccessStatus = "unavailable"

	runtimeAccessPollMinMS = 500
	runtimeAccessPollMaxMS = 5000
)

// RuntimeAccess combines one authoritative state projection with backend discovery.
// Backend is internal to the host proxy and must never be serialized to browsers.
type RuntimeAccess struct {
	Status      RuntimeAccessStatus
	WorkspaceID string
	PollAfterMS int
	Backend     *url.URL `json:"-"`
}

// RuntimeDriver accepts only typed grant lifecycle operations.
type RuntimeDriver interface {
	ApplyGrant(context.Context, Grant) (*url.URL, error)
	RevokeGrant(context.Context) error
	CurrentBackend(context.Context) (*url.URL, error)
}

// BackendLocator resolves the public discovery state and ready-only business backend in one read.
type BackendLocator interface {
	LocateRuntime(context.Context) (RuntimeAccess, error)
}

// Fault is a stable controller problem without sensitive implementation detail.
type Fault struct {
	Code        string
	Message     string
	Retryable   bool
	OperationID string
	FieldErrors map[string]string
	Status      int
}

func (fault *Fault) Error() string {
	if fault == nil || fault.Code == "" {
		return "controller request failed"
	}
	return fault.Code
}

// AsFault returns a stable fault when err was produced by a controller seam.
func AsFault(err error) (*Fault, bool) {
	var fault *Fault
	ok := errors.As(err, &fault)
	return fault, ok
}

// WaitingStore is the fail-closed zero-grant state used before a durable store is wired.
type WaitingStore struct{}

func (WaitingStore) State(context.Context) (State, error) {
	return State{
		StateVersion: 1,
		Runtime: RuntimeState{
			Status: RuntimeWaitingForWorkspace,
			API:    ProcessState{Status: ProcessStopped},
			Worker: ProcessState{Status: ProcessStopped},
		},
		RecentWorkspaces: []Workspace{},
		PollAfterMS:      2000,
	}, nil
}

func (WaitingStore) BeginSwitch(context.Context, SwitchCommand) (Operation, error) {
	return Operation{}, unavailableStoreFault()
}

func (WaitingStore) Operation(context.Context, string) (Operation, error) {
	return Operation{}, unavailableStoreFault()
}

func (WaitingStore) CheckAvailability(context.Context, string, int64) (Workspace, error) {
	return Workspace{}, unavailableStoreFault()
}

func (WaitingStore) RemoveWorkspace(context.Context, string, int64) error {
	return unavailableStoreFault()
}

func unavailableStoreFault() error {
	return &Fault{Code: "WORKSPACE_CONTROL_UNAVAILABLE", Message: "Workspace 控制状态尚不可用", Retryable: true, Status: 503}
}

// StateBackend gates Docker port discovery behind the authoritative ready state.
type StateBackend struct {
	State   StateReader
	Runtime interface {
		CurrentBackend(context.Context) (*url.URL, error)
	}
}

// LocateRuntime reads one state snapshot and discovers a backend only for a ready projection.
func (backend StateBackend) LocateRuntime(ctx context.Context) (RuntimeAccess, error) {
	if backend.State == nil || backend.Runtime == nil {
		return RuntimeAccess{}, &Fault{Code: "RUNTIME_NOT_READY", Message: "业务运行时尚未就绪", Retryable: true, Status: 503}
	}
	state, err := backend.State.State(ctx)
	if err != nil {
		return RuntimeAccess{}, err
	}
	access := projectRuntimeAccess(state)
	if access.Status != RuntimeAccessReady {
		return access, nil
	}
	access.Backend, err = backend.Runtime.CurrentBackend(ctx)
	if err != nil {
		return RuntimeAccess{}, err
	}
	if !validBackendURL(access.Backend) {
		return RuntimeAccess{}, runtimeFault("RUNTIME_BACKEND_INVALID")
	}
	return access, nil
}

func projectRuntimeAccess(state State) RuntimeAccess {
	access := RuntimeAccess{Status: RuntimeAccessUnavailable, PollAfterMS: clampRuntimeAccessPoll(state.PollAfterMS)}
	operationRunning := state.Operation != nil && state.Operation.Result == ""
	if state.Runtime.Status == RuntimeWaitingForWorkspace && state.ActiveWorkspace == nil && !operationRunning &&
		state.Runtime.API.Status == ProcessStopped && state.Runtime.Worker.Status == ProcessStopped {
		access.Status = RuntimeAccessWaiting
		return access
	}
	if state.Runtime.Status != RuntimeReady || state.Runtime.API.Status != ProcessReady || state.Runtime.Worker.Status != ProcessReady ||
		state.ActiveWorkspace == nil || state.ActiveWorkspace.Availability != AvailabilityAvailable || operationRunning {
		return access
	}
	workspaceID, err := foundation.ParseID(state.ActiveWorkspace.WorkspaceID)
	if err != nil {
		return access
	}
	access.Status = RuntimeAccessReady
	access.WorkspaceID = string(workspaceID)
	return access
}

func clampRuntimeAccessPoll(value int) int {
	if value < runtimeAccessPollMinMS {
		return runtimeAccessPollMinMS
	}
	if value > runtimeAccessPollMaxMS {
		return runtimeAccessPollMaxMS
	}
	return value
}

func validRuntimeAccess(access RuntimeAccess) bool {
	if access.PollAfterMS < runtimeAccessPollMinMS || access.PollAfterMS > runtimeAccessPollMaxMS {
		return false
	}
	switch access.Status {
	case RuntimeAccessWaiting, RuntimeAccessUnavailable:
		return access.WorkspaceID == "" && access.Backend == nil
	case RuntimeAccessReady:
		workspaceID, err := foundation.ParseID(access.WorkspaceID)
		return err == nil && string(workspaceID) == access.WorkspaceID && validBackendURL(access.Backend)
	default:
		return false
	}
}
