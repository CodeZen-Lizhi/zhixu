package application

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	defaultWorkspaceRuntimeFreshWithin = 20 * time.Second
	maximumWorkspaceLeaseDuration      = 10 * time.Minute
	maximumWorkspaceOperationDuration  = 30 * time.Minute
)

// RegistryStore owns immutable root identities and mutable availability metadata.
type RegistryStore interface {
	ResolveWorkspace(context.Context, domain.WorkspaceReservation) (domain.WorkspaceResolution, error)
	ListRegistry(context.Context, bool) ([]domain.Workspace, error)
	SetWorkspaceAvailability(context.Context, domain.AvailabilityUpdate) (domain.Workspace, error)
	RemoveWorkspace(context.Context, domain.WorkspaceRemoval) (domain.Workspace, error)
	RebindWorkspace(context.Context, domain.WorkspaceBindingMigration) (domain.WorkspaceBindingMigrationResult, error)
}

// ControlStore owns the leased switch state machine and runtime ownership records.
type ControlStore interface {
	ControlSnapshot(context.Context, time.Duration) (domain.ControlSnapshot, error)
	GetSwitchOperation(context.Context, foundation.ID) (domain.SwitchOperation, error)
	BeginSwitch(context.Context, BeginSwitchStoreCommand) (domain.SwitchOperation, error)
	RenewSwitch(context.Context, RenewSwitchCommand) (domain.SwitchOperation, error)
	TakeOverSwitch(context.Context, TakeOverSwitchCommand) (domain.SwitchOperation, error)
	AdvanceSwitch(context.Context, AdvanceSwitchCommand) (domain.SwitchOperation, error)
	RevokeActiveWorkspace(context.Context, RevokeActiveCommand) (domain.ControlState, error)
	CommitTargetWorkspace(context.Context, CommitTargetCommand) (domain.ControlState, error)
	RestorePreviousWorkspace(context.Context, RestorePreviousCommand) (domain.ControlState, error)
	FinishSwitch(context.Context, FinishSwitchCommand) (domain.SwitchOperation, error)
	RegisterRuntime(context.Context, RuntimeRegistration) (domain.RuntimeRecord, error)
	HeartbeatRuntime(context.Context, RuntimeHeartbeat) (domain.RuntimeRecord, error)
	SetRuntimePhase(context.Context, RuntimePhaseCommand) (domain.RuntimeRecord, error)
}

// RegisterWorkspaceCommand contains a host-validated physical directory identity.
type RegisterWorkspaceCommand struct {
	Name              string
	CanonicalRoot     string
	GitRepositoryPath string
	RootFingerprint   string
	BindingVersion    int64
	GitBranch         string
	GitHead           string
	GitDirty          bool
	GitCheckedAt      time.Time
}

// RebindWorkspaceCommand is the explicit recovery command for a changed root identity.
type RebindWorkspaceCommand struct {
	ControllerInstanceID foundation.ID
	WorkspaceID          foundation.ID
	CanonicalRoot        string
	OldRootFingerprint   string
	NewRootFingerprint   string
	IdempotencyKey       string
}

// BeginSwitchCommand fixes idempotency, target and optimistic state ownership.
type BeginSwitchCommand struct {
	ControllerInstanceID foundation.ID
	IdempotencyKey       string
	RequestHash          string
	TargetWorkspaceID    foundation.ID
	ExpectedStateVersion int64
	LeaseOwnerID         foundation.ID
	LeaseDuration        time.Duration
	Deadline             time.Time
}

// BeginSwitchStoreCommand adds the generated operation identity.
type BeginSwitchStoreCommand struct {
	OperationID          foundation.ID
	ControllerInstanceID foundation.ID
	IdempotencyKey       string
	RequestHash          string
	TargetWorkspaceID    foundation.ID
	ExpectedStateVersion int64
	LeaseOwnerID         foundation.ID
	LeaseDuration        time.Duration
	Deadline             time.Time
}

// RenewSwitchCommand extends a matching owner lease without changing phase.
type RenewSwitchCommand struct {
	OperationID              foundation.ID
	LeaseOwnerID             foundation.ID
	ExpectedPhase            domain.SwitchPhase
	ExpectedOperationVersion int64
	ExpectedStateVersion     int64
	LeaseDuration            time.Duration
}

// TakeOverSwitchCommand claims an expired operation lease through CAS.
type TakeOverSwitchCommand struct {
	OperationID              foundation.ID
	NewLeaseOwnerID          foundation.ID
	ExpectedPhase            domain.SwitchPhase
	ExpectedOperationVersion int64
	ExpectedStateVersion     int64
	LeaseDuration            time.Duration
}

// AdvanceSwitchCommand changes one legal phase and renews the same lease owner.
type AdvanceSwitchCommand struct {
	OperationID              foundation.ID
	LeaseOwnerID             foundation.ID
	ExpectedPhase            domain.SwitchPhase
	NextPhase                domain.SwitchPhase
	ExpectedOperationVersion int64
	ExpectedStateVersion     int64
	LeaseDuration            time.Duration
}

// RevokeActiveCommand clears the old active identity after quiescence.
type RevokeActiveCommand struct {
	OperationID              foundation.ID
	LeaseOwnerID             foundation.ID
	ExpectedOperationVersion int64
	ExpectedStateVersion     int64
	LeaseDuration            time.Duration
}

// CommitTargetCommand publishes target only after both candidate roles are prepared.
type CommitTargetCommand struct {
	OperationID              foundation.ID
	LeaseOwnerID             foundation.ID
	ExpectedOperationVersion int64
	ExpectedStateVersion     int64
	LeaseDuration            time.Duration
	RuntimeFreshWithin       time.Duration
}

// RestorePreviousCommand restores the exact previous identity at recovery generation.
type RestorePreviousCommand struct {
	OperationID              foundation.ID
	LeaseOwnerID             foundation.ID
	ExpectedOperationVersion int64
	ExpectedStateVersion     int64
	LeaseDuration            time.Duration
	RuntimeFreshWithin       time.Duration
}

// FinishSwitchCommand records a terminal outcome and releases shared mutation ownership.
type FinishSwitchCommand struct {
	OperationID              foundation.ID
	LeaseOwnerID             foundation.ID
	Result                   domain.SwitchResult
	ErrorCode                string
	ExpectedOperationVersion int64
	ExpectedStateVersion     int64
	RuntimeFreshWithin       time.Duration
}

// RuntimeRegistration claims one role for an authorized active or candidate binding.
type RuntimeRegistration struct {
	Role            domain.RuntimeRole
	InstanceID      foundation.ID
	WorkspaceID     foundation.ID
	OperationID     *foundation.ID
	GrantGeneration int64
	RootFingerprint string
	BindingVersion  int64
	Phase           domain.RuntimePhase
}

// RuntimeHeartbeat is a strict role, instance, operation and version CAS.
type RuntimeHeartbeat struct {
	Role                   domain.RuntimeRole
	InstanceID             foundation.ID
	OperationID            *foundation.ID
	ExpectedRuntimeVersion int64
}

// RuntimePhaseCommand changes one current runtime owner's phase.
type RuntimePhaseCommand struct {
	Role                   domain.RuntimeRole
	InstanceID             foundation.ID
	OperationID            *foundation.ID
	ExpectedPhase          domain.RuntimePhase
	NextPhase              domain.RuntimePhase
	ExpectedRuntimeVersion int64
}

// ControlService validates controller commands before they reach persistence.
type ControlService struct {
	registry RegistryStore
	control  ControlStore
	ids      foundation.IDGenerator
	clock    foundation.Clock
}

// NewControlService creates the Workspace Registry and switch application boundary.
func NewControlService(registry RegistryStore, control ControlStore, ids foundation.IDGenerator, clock foundation.Clock) (*ControlService, error) {
	if nilInterfaceValue(registry) || nilInterfaceValue(control) || nilInterfaceValue(ids) || nilInterfaceValue(clock) {
		return nil, controlUnavailable(errors.New("workspace control dependency is unavailable"))
	}
	return &ControlService{registry: registry, control: control, ids: ids, clock: clock}, nil
}

// ResolveWorkspace reuses the exact binding or creates one inactive Registry identity.
func (service *ControlService) ResolveWorkspace(ctx context.Context, command RegisterWorkspaceCommand) (domain.WorkspaceResolution, error) {
	if err := service.ready(ctx); err != nil {
		return domain.WorkspaceResolution{}, err
	}
	name := strings.TrimSpace(command.Name)
	gitRoot := command.GitRepositoryPath
	if gitRoot == "" {
		gitRoot = command.CanonicalRoot
	}
	if name == "" || len(name) > 256 || !validCanonicalRoot(command.CanonicalRoot) || gitRoot != command.CanonicalRoot ||
		!validFingerprint(command.RootFingerprint) || command.BindingVersion < 1 || command.GitCheckedAt.IsZero() {
		return domain.WorkspaceResolution{}, registryInvalid(errors.New("workspace registry command is invalid"))
	}
	id, err := service.ids.New()
	if err != nil {
		return domain.WorkspaceResolution{}, err
	}
	if !validID(id) {
		return domain.WorkspaceResolution{}, controlUnavailable(errors.New("workspace id generation returned an invalid id"))
	}
	now := service.clock.Now()
	return service.registry.ResolveWorkspace(ctx, domain.WorkspaceReservation{
		ID: id, Name: name, Now: now,
		Binding: domain.RootBinding{
			CanonicalPath: command.CanonicalRoot, GitRepositoryPath: gitRoot,
			Fingerprint: command.RootFingerprint, BindingVersion: command.BindingVersion,
		},
		Git: domain.GitBaseline{
			RepositoryPath: gitRoot, Branch: command.GitBranch, Head: command.GitHead,
			Dirty: command.GitDirty, CheckedAt: command.GitCheckedAt.UTC(),
		},
	})
}

// ListRegistry returns recent identities in persistence-defined stable order.
func (service *ControlService) ListRegistry(ctx context.Context, includeRemoved bool) ([]domain.Workspace, error) {
	if err := service.ready(ctx); err != nil {
		return nil, err
	}
	return service.registry.ListRegistry(ctx, includeRemoved)
}

// SetWorkspaceAvailability records a host metadata check without changing identity.
func (service *ControlService) SetWorkspaceAvailability(ctx context.Context, update domain.AvailabilityUpdate) (domain.Workspace, error) {
	if err := service.ready(ctx); err != nil {
		return domain.Workspace{}, err
	}
	validShape := update.Availability == domain.WorkspaceAvailabilityAvailable && update.Reason == "" ||
		update.Availability == domain.WorkspaceAvailabilityUnavailable && validErrorCode(update.Reason)
	if !validID(update.WorkspaceID) || update.ExpectedVersion < 1 || update.CheckedAt.IsZero() || !validShape {
		return domain.Workspace{}, registryInvalid(errors.New("workspace availability command is invalid"))
	}
	return service.registry.SetWorkspaceAvailability(ctx, update)
}

// RemoveWorkspace soft-removes one inactive Registry identity.
func (service *ControlService) RemoveWorkspace(ctx context.Context, removal domain.WorkspaceRemoval) (domain.Workspace, error) {
	if err := service.ready(ctx); err != nil {
		return domain.Workspace{}, err
	}
	if !validID(removal.WorkspaceID) || removal.ExpectedVersion < 1 || removal.RemovedAt.IsZero() {
		return domain.Workspace{}, registryInvalid(errors.New("workspace removal command is invalid"))
	}
	return service.registry.RemoveWorkspace(ctx, removal)
}

// RebindWorkspace performs one explicit physical-identity migration and keeps
// ordinary ResolveWorkspace fail-closed on future mismatches.
func (service *ControlService) RebindWorkspace(ctx context.Context, command RebindWorkspaceCommand) (domain.WorkspaceBindingMigrationResult, error) {
	if err := service.ready(ctx); err != nil {
		return domain.WorkspaceBindingMigrationResult{}, err
	}
	if !validID(command.ControllerInstanceID) || !validID(command.WorkspaceID) ||
		!validCanonicalRoot(command.CanonicalRoot) || !validFingerprint(command.OldRootFingerprint) ||
		!validFingerprint(command.NewRootFingerprint) || command.OldRootFingerprint == command.NewRootFingerprint ||
		!validIdempotencyKey(command.IdempotencyKey) {
		return domain.WorkspaceBindingMigrationResult{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeBindingRebindInvalid, false, errors.New("workspace rebind command is invalid"))
	}
	eventID, err := service.ids.New()
	if err != nil {
		return domain.WorkspaceBindingMigrationResult{}, err
	}
	if !validID(eventID) {
		return domain.WorkspaceBindingMigrationResult{}, controlUnavailable(errors.New("workspace rebind event id is invalid"))
	}
	return service.registry.RebindWorkspace(ctx, domain.WorkspaceBindingMigration{
		ID: eventID, ControllerInstanceID: command.ControllerInstanceID,
		IdempotencyKey: command.IdempotencyKey, WorkspaceID: command.WorkspaceID,
		CanonicalRoot: command.CanonicalRoot, OldRootFingerprint: command.OldRootFingerprint,
		NewRootFingerprint: command.NewRootFingerprint,
	})
}

// Snapshot loads Registry, control, operation and runtime facts together.
func (service *ControlService) Snapshot(ctx context.Context, freshWithin time.Duration) (domain.ControlSnapshot, error) {
	if err := service.ready(ctx); err != nil {
		return domain.ControlSnapshot{}, err
	}
	if freshWithin == 0 {
		freshWithin = defaultWorkspaceRuntimeFreshWithin
	}
	if !validFreshWithin(freshWithin) {
		return domain.ControlSnapshot{}, registryInvalid(errors.New("workspace runtime freshness is invalid"))
	}
	return service.control.ControlSnapshot(ctx, freshWithin)
}

// GetSwitchOperation loads one durable operation by ID.
func (service *ControlService) GetSwitchOperation(ctx context.Context, operationID foundation.ID) (domain.SwitchOperation, error) {
	if err := service.ready(ctx); err != nil {
		return domain.SwitchOperation{}, err
	}
	if !validID(operationID) {
		return domain.SwitchOperation{}, registryInvalid(errors.New("workspace operation id is invalid"))
	}
	return service.control.GetSwitchOperation(ctx, operationID)
}

// BeginSwitch creates one idempotent validating operation under the global mutation gate.
func (service *ControlService) BeginSwitch(ctx context.Context, command BeginSwitchCommand) (domain.SwitchOperation, error) {
	if err := service.ready(ctx); err != nil {
		return domain.SwitchOperation{}, err
	}
	now := service.clock.Now()
	if !validID(command.ControllerInstanceID) || !validID(command.TargetWorkspaceID) || !validID(command.LeaseOwnerID) ||
		!validIdempotencyKey(command.IdempotencyKey) || !validFingerprint(command.RequestHash) ||
		command.ExpectedStateVersion < 1 || !validLease(command.LeaseDuration) ||
		!command.Deadline.After(now) || command.Deadline.After(now.Add(maximumWorkspaceOperationDuration)) {
		return domain.SwitchOperation{}, registryInvalid(errors.New("workspace switch begin command is invalid"))
	}
	operationID, err := service.ids.New()
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	if !validID(operationID) {
		return domain.SwitchOperation{}, controlUnavailable(errors.New("workspace operation id generation returned an invalid id"))
	}
	return service.control.BeginSwitch(ctx, BeginSwitchStoreCommand{
		OperationID: operationID, ControllerInstanceID: command.ControllerInstanceID,
		IdempotencyKey: command.IdempotencyKey, RequestHash: command.RequestHash,
		TargetWorkspaceID: command.TargetWorkspaceID, ExpectedStateVersion: command.ExpectedStateVersion,
		LeaseOwnerID: command.LeaseOwnerID, LeaseDuration: command.LeaseDuration, Deadline: command.Deadline,
	})
}

// RenewSwitch renews a matching owner lease.
func (service *ControlService) RenewSwitch(ctx context.Context, command RenewSwitchCommand) (domain.SwitchOperation, error) {
	if err := service.readySwitchCommand(ctx, command.OperationID, command.LeaseOwnerID, command.ExpectedPhase,
		command.ExpectedOperationVersion, command.ExpectedStateVersion, command.LeaseDuration); err != nil {
		return domain.SwitchOperation{}, err
	}
	return service.control.RenewSwitch(ctx, command)
}

// TakeOverSwitch claims an expired operation lease.
func (service *ControlService) TakeOverSwitch(ctx context.Context, command TakeOverSwitchCommand) (domain.SwitchOperation, error) {
	if err := service.readySwitchCommand(ctx, command.OperationID, command.NewLeaseOwnerID, command.ExpectedPhase,
		command.ExpectedOperationVersion, command.ExpectedStateVersion, command.LeaseDuration); err != nil {
		return domain.SwitchOperation{}, err
	}
	return service.control.TakeOverSwitch(ctx, command)
}

// AdvanceSwitch performs one legal durable phase transition.
func (service *ControlService) AdvanceSwitch(ctx context.Context, command AdvanceSwitchCommand) (domain.SwitchOperation, error) {
	if err := service.readySwitchCommand(ctx, command.OperationID, command.LeaseOwnerID, command.ExpectedPhase,
		command.ExpectedOperationVersion, command.ExpectedStateVersion, command.LeaseDuration); err != nil {
		return domain.SwitchOperation{}, err
	}
	if command.NextPhase == command.ExpectedPhase || domain.ValidateSwitchTransition(command.ExpectedPhase, command.NextPhase) != nil {
		return domain.SwitchOperation{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeSwitchPhaseConflict, false, errors.New("workspace switch transition is invalid"))
	}
	return service.control.AdvanceSwitch(ctx, command)
}

// RevokeActiveWorkspace clears old active after the operation reaches revoking.
func (service *ControlService) RevokeActiveWorkspace(ctx context.Context, command RevokeActiveCommand) (domain.ControlState, error) {
	if err := service.readyVersionedSwitch(ctx, command.OperationID, command.LeaseOwnerID,
		command.ExpectedOperationVersion, command.ExpectedStateVersion, command.LeaseDuration); err != nil {
		return domain.ControlState{}, err
	}
	return service.control.RevokeActiveWorkspace(ctx, command)
}

// CommitTargetWorkspace publishes target only after candidate readiness.
func (service *ControlService) CommitTargetWorkspace(ctx context.Context, command CommitTargetCommand) (domain.ControlState, error) {
	if err := service.readyVersionedSwitch(ctx, command.OperationID, command.LeaseOwnerID,
		command.ExpectedOperationVersion, command.ExpectedStateVersion, command.LeaseDuration); err != nil {
		return domain.ControlState{}, err
	}
	if !validFreshWithin(command.RuntimeFreshWithin) {
		return domain.ControlState{}, registryInvalid(errors.New("workspace runtime freshness is invalid"))
	}
	return service.control.CommitTargetWorkspace(ctx, command)
}

// RestorePreviousWorkspace publishes the previous identity after recovery readiness.
func (service *ControlService) RestorePreviousWorkspace(ctx context.Context, command RestorePreviousCommand) (domain.ControlState, error) {
	if err := service.readyVersionedSwitch(ctx, command.OperationID, command.LeaseOwnerID,
		command.ExpectedOperationVersion, command.ExpectedStateVersion, command.LeaseDuration); err != nil {
		return domain.ControlState{}, err
	}
	if !validFreshWithin(command.RuntimeFreshWithin) {
		return domain.ControlState{}, registryInvalid(errors.New("workspace runtime freshness is invalid"))
	}
	return service.control.RestorePreviousWorkspace(ctx, command)
}

// FinishSwitch persists one terminal outcome and releases global mutation ownership.
func (service *ControlService) FinishSwitch(ctx context.Context, command FinishSwitchCommand) (domain.SwitchOperation, error) {
	if err := service.ready(ctx); err != nil {
		return domain.SwitchOperation{}, err
	}
	if !validID(command.OperationID) || !validID(command.LeaseOwnerID) || command.ExpectedOperationVersion < 1 ||
		command.ExpectedStateVersion < 1 || !validFreshWithin(command.RuntimeFreshWithin) || !domain.ValidSwitchResult(command.Result) {
		return domain.SwitchOperation{}, registryInvalid(errors.New("workspace switch finish command is invalid"))
	}
	return service.control.FinishSwitch(ctx, command)
}

// RegisterRuntime claims one role for an exact authorized binding.
func (service *ControlService) RegisterRuntime(ctx context.Context, registration RuntimeRegistration) (domain.RuntimeRecord, error) {
	if err := service.ready(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !validRuntimeRegistration(registration) {
		return domain.RuntimeRecord{}, registryInvalid(errors.New("workspace runtime registration is invalid"))
	}
	return service.control.RegisterRuntime(ctx, registration)
}

// HeartbeatRuntime renews a current runtime owner through version CAS.
func (service *ControlService) HeartbeatRuntime(ctx context.Context, heartbeat RuntimeHeartbeat) (domain.RuntimeRecord, error) {
	if err := service.ready(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !domain.ValidRuntimeRole(heartbeat.Role) || !validID(heartbeat.InstanceID) ||
		!validOptionalID(heartbeat.OperationID) || heartbeat.ExpectedRuntimeVersion < 1 {
		return domain.RuntimeRecord{}, registryInvalid(errors.New("workspace runtime heartbeat is invalid"))
	}
	return service.control.HeartbeatRuntime(ctx, heartbeat)
}

// SetRuntimePhase changes one matching runtime owner through version CAS.
func (service *ControlService) SetRuntimePhase(ctx context.Context, command RuntimePhaseCommand) (domain.RuntimeRecord, error) {
	if err := service.ready(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !domain.ValidRuntimeRole(command.Role) || !validID(command.InstanceID) || !validOptionalID(command.OperationID) ||
		command.ExpectedRuntimeVersion < 1 || command.ExpectedPhase == command.NextPhase ||
		domain.ValidateRuntimeTransition(command.ExpectedPhase, command.NextPhase) != nil {
		return domain.RuntimeRecord{}, registryInvalid(errors.New("workspace runtime phase command is invalid"))
	}
	return service.control.SetRuntimePhase(ctx, command)
}

func (service *ControlService) ready(ctx context.Context) error {
	if service == nil || nilInterfaceValue(service.registry) || nilInterfaceValue(service.control) ||
		nilInterfaceValue(service.ids) || nilInterfaceValue(service.clock) {
		return controlUnavailable(errors.New("workspace control service is unavailable"))
	}
	if ctx == nil {
		return registryInvalid(errors.New("workspace control context is nil"))
	}
	return nil
}

func (service *ControlService) readySwitchCommand(ctx context.Context, operationID, ownerID foundation.ID,
	phase domain.SwitchPhase, operationVersion, stateVersion int64, lease time.Duration,
) error {
	if err := service.readyVersionedSwitch(ctx, operationID, ownerID, operationVersion, stateVersion, lease); err != nil {
		return err
	}
	if !domain.ValidSwitchPhase(phase) {
		return registryInvalid(errors.New("workspace switch phase is invalid"))
	}
	return nil
}

func (service *ControlService) readyVersionedSwitch(ctx context.Context, operationID, ownerID foundation.ID,
	operationVersion, stateVersion int64, lease time.Duration,
) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if !validID(operationID) || !validID(ownerID) || operationVersion < 1 || stateVersion < 1 || !validLease(lease) {
		return registryInvalid(errors.New("workspace switch ownership command is invalid"))
	}
	return nil
}

func validRuntimeRegistration(registration RuntimeRegistration) bool {
	if !domain.ValidRuntimeRole(registration.Role) || !validID(registration.InstanceID) ||
		!validID(registration.WorkspaceID) || !validOptionalID(registration.OperationID) ||
		registration.GrantGeneration < 1 || !validFingerprint(registration.RootFingerprint) ||
		registration.BindingVersion < 1 || !domain.ValidRuntimePhase(registration.Phase) {
		return false
	}
	if registration.OperationID == nil {
		return registration.Phase == domain.RuntimePhaseActive || registration.Phase == domain.RuntimePhaseUnavailable
	}
	return registration.Phase == domain.RuntimePhaseQuiescing || registration.Phase == domain.RuntimePhaseQuiesced ||
		registration.Phase == domain.RuntimePhasePrepared || registration.Phase == domain.RuntimePhaseVerifying
}

func validCanonicalRoot(value string) bool {
	return value != "" && len(value) <= 4096 && utf8.ValidString(value) && filepath.IsAbs(value) &&
		filepath.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validIdempotencyKey(value string) bool {
	if value == "" || len(value) > 256 || value != strings.TrimSpace(value) || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsAny(value, "\x00\r\n")
}

func validErrorCode(value string) bool {
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

func validLease(value time.Duration) bool {
	return value >= time.Second && value <= maximumWorkspaceLeaseDuration && value%time.Microsecond == 0
}

func validFreshWithin(value time.Duration) bool {
	return value >= time.Second && value <= 5*time.Minute && value%time.Microsecond == 0
}

func validID(id foundation.ID) bool {
	parsed, err := foundation.ParseID(string(id))
	return err == nil && parsed == id
}

func validOptionalID(id *foundation.ID) bool { return id == nil || validID(*id) }

func nilInterfaceValue(value any) bool {
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

func registryInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeRegistryInvalid, false, cause)
}

func controlUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeControlDatabaseUnavailable, true, cause)
}
