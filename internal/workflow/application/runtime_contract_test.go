package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestRuntimeCoordinatorClaimUsesLeaseDurationAndStableDelivery(t *testing.T) {
	t.Parallel()

	command := ClaimCommand{NodeRunID: id(1), DispatchNo: 2, DeliveryID: "job-91-attempt-0", RiverJobID: 91, RiverJobAttempt: 0, LeaseOwner: " worker-a ", LeaseDuration: 30 * time.Second}
	port := &fakeRuntimeStatePort{claimResult: validRuntimeClaimResult(command)}
	coordinator, err := NewRuntimeCoordinator(port)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Claim(context.Background(), command)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if result.Disposition != ClaimDispositionClaimed || port.claim.LeaseOwner != "worker-a" || port.claim.LeaseDuration != 30*time.Second || port.claim.DispatchNo != 2 {
		t.Fatalf("Claim() result=%+v command=%+v", result, port.claim)
	}
}

func TestRuntimeCoordinatorClaimAcceptsBenignStaleDelivery(t *testing.T) {
	t.Parallel()

	port := &fakeRuntimeStatePort{claimResult: ClaimResult{Disposition: ClaimDispositionStale}}
	coordinator, _ := NewRuntimeCoordinator(port)
	result, err := coordinator.Claim(context.Background(), validClaimCommand())
	if err != nil || result.Disposition != ClaimDispositionStale {
		t.Fatalf("Claim() = %+v, %v", result, err)
	}
}

func TestRuntimeCoordinatorValidatesClaimObservabilityFacts(t *testing.T) {
	t.Parallel()
	port := &fakeRuntimeStatePort{claimResult: ClaimResult{Disposition: ClaimDispositionStale, DuplicateDelivery: true}}
	coordinator, _ := NewRuntimeCoordinator(port)
	if _, err := coordinator.Claim(context.Background(), validClaimCommand()); workflowErrorCode(err) != "WORKFLOW_CLAIM_RESULT_INVALID" {
		t.Fatalf("missing duplicate node kind err=%v", err)
	}
	claimed := validRuntimeClaimResult(validClaimCommand())
	claimed.LeaseReclaimed = true
	port.claimResult = claimed
	if _, err := coordinator.Claim(context.Background(), validClaimCommand()); workflowErrorCode(err) != "WORKFLOW_CLAIM_RESULT_INVALID" {
		t.Fatalf("reclaim without duplicate err=%v", err)
	}
	claimed = validRuntimeClaimResult(validClaimCommand())
	claimed.Definition.Graph = json.RawMessage(`{"nodes":[{"key":"test-node","kind":"test","input_schema_version":1,"output_schema_version":1,"retry_policy":{"max_retries":0,"base_delay":0,"max_delay":0},"permissions_from_transport":["READ_LOCAL"]}]}`)
	port.claimResult = claimed
	if _, err := coordinator.Claim(context.Background(), validClaimCommand()); workflowErrorCode(err) != "WORKFLOW_CLAIM_RESULT_INVALID" {
		t.Fatalf("unknown persisted graph field err=%v", err)
	}
}

func validRuntimeClaimResult(command ClaimCommand) ClaimResult {
	graph := domain.CanonicalGraph{Nodes: []domain.NodeDefinition{{Key: "test-node", Kind: "test", InputSchemaVersion: 1, OutputSchemaVersion: 1}}}
	graphJSON, _ := json.Marshal(graph)
	graphHash, _ := ComputeCanonicalGraphHash(graph)
	return ClaimResult{
		Disposition: ClaimDispositionClaimed,
		Definition:  domain.Definition{ID: id(3), WorkspaceID: id(4), Key: "test-workflow", Version: 1, Graph: graphJSON, GraphHash: graphHash},
		Run:         domain.Run{ID: id(2), WorkspaceID: id(4), DefinitionID: id(3), Status: domain.RunStatusRunning},
		Node: domain.NodeRun{ID: command.NodeRunID, RunID: id(2), NodeKey: "test-node", NodeType: "test", Status: domain.NodeStatusRunning,
			InputSchemaVersion: 1, OutputSchemaVersion: 1},
		Attempt: domain.NodeAttempt{ID: id(5), NodeRunID: command.NodeRunID, AttemptNo: 1, DispatchNo: command.DispatchNo, DeliveryID: strings.TrimSpace(command.DeliveryID), RiverJobID: command.RiverJobID,
			RiverJobAttempt: command.RiverJobAttempt, LeaseOwner: strings.TrimSpace(command.LeaseOwner), Status: domain.AttemptStatusRunning},
	}
}

func TestRuntimeCoordinatorRejectsInvalidClaimBeforePort(t *testing.T) {
	t.Parallel()

	tests := []ClaimCommand{
		{},
		{NodeRunID: id(1), DispatchNo: 0, DeliveryID: "delivery", RiverJobID: 1, LeaseOwner: "worker", LeaseDuration: time.Second},
		{NodeRunID: id(1), DispatchNo: 1, DeliveryID: "", RiverJobID: 1, LeaseOwner: "worker", LeaseDuration: time.Second},
		{NodeRunID: id(1), DispatchNo: 1, DeliveryID: "delivery", RiverJobID: 0, LeaseOwner: "worker", LeaseDuration: time.Second},
		{NodeRunID: id(1), DispatchNo: 1, DeliveryID: "delivery", RiverJobID: 1, RiverJobAttempt: -1, LeaseOwner: "worker", LeaseDuration: time.Second},
		{NodeRunID: id(1), DispatchNo: 1, DeliveryID: "delivery", RiverJobID: 1, LeaseOwner: " ", LeaseDuration: time.Second},
		{NodeRunID: id(1), DispatchNo: 1, DeliveryID: "delivery", RiverJobID: 1, LeaseOwner: "worker", LeaseDuration: 0},
	}
	for i, command := range tests {
		port := &fakeRuntimeStatePort{}
		coordinator, _ := NewRuntimeCoordinator(port)
		if _, err := coordinator.Claim(context.Background(), command); workflowErrorCode(err) != "WORKFLOW_CLAIM_INVALID" {
			t.Errorf("case %d Claim() error = %v", i, err)
		}
		if port.claimCalls != 0 {
			t.Errorf("case %d called port", i)
		}
	}
}

func TestRuntimeCoordinatorHeartbeatCarriesFullFenceWithoutCallerTime(t *testing.T) {
	t.Parallel()

	port := &fakeRuntimeStatePort{heartbeatResult: HeartbeatResult{Node: domain.NodeRun{ID: id(1), Status: domain.NodeStatusRunning, Version: 8}, Attempt: domain.NodeAttempt{NodeRunID: id(1), AttemptNo: 3, LeaseOwner: "worker-a", Status: domain.AttemptStatusRunning}}}
	coordinator, _ := NewRuntimeCoordinator(port)
	command := HeartbeatCommand{NodeRunID: id(1), Fence: domain.LeaseFence{Owner: " worker-a ", AttemptNo: 3, NodeVersion: 7}, LeaseDuration: 30 * time.Second}
	result, err := coordinator.Heartbeat(context.Background(), command)
	if err != nil || result.Node.Version != 8 {
		t.Fatalf("Heartbeat() = %+v, %v", result, err)
	}
	if port.heartbeat.Fence.Owner != "worker-a" || port.heartbeat.Fence.AttemptNo != 3 || port.heartbeat.Fence.NodeVersion != 7 || port.heartbeat.LeaseDuration != 30*time.Second {
		t.Fatalf("Heartbeat command = %+v", port.heartbeat)
	}
}

func TestRuntimeCoordinatorRejectsInvalidHeartbeatBeforePort(t *testing.T) {
	t.Parallel()

	for i, command := range []HeartbeatCommand{
		{},
		{NodeRunID: id(1), Fence: domain.LeaseFence{Owner: "worker", AttemptNo: 0, NodeVersion: 1}, LeaseDuration: time.Second},
		{NodeRunID: id(1), Fence: domain.LeaseFence{Owner: "worker", AttemptNo: 1, NodeVersion: 0}, LeaseDuration: time.Second},
		{NodeRunID: id(1), Fence: domain.LeaseFence{Owner: "", AttemptNo: 1, NodeVersion: 1}, LeaseDuration: time.Second},
		{NodeRunID: id(1), Fence: domain.LeaseFence{Owner: "worker", AttemptNo: 1, NodeVersion: 1}, LeaseDuration: -time.Second},
	} {
		port := &fakeRuntimeStatePort{}
		coordinator, _ := NewRuntimeCoordinator(port)
		if _, err := coordinator.Heartbeat(context.Background(), command); workflowErrorCode(err) != "WORKFLOW_HEARTBEAT_INVALID" {
			t.Errorf("case %d Heartbeat() error = %v", i, err)
		}
		if port.heartbeatCalls != 0 {
			t.Errorf("case %d called port", i)
		}
	}
}

func TestRuntimeCoordinatorCompletesAndClassifiesFailure(t *testing.T) {
	t.Parallel()

	port := &fakeRuntimeStatePort{transitionResult: DeliveryTransitionResult{Run: domain.Run{ID: id(2), Status: domain.RunStatusRunning}, Node: domain.NodeRun{ID: id(1), RunID: id(2), Status: domain.NodeStatusSucceeded}, Attempt: domain.NodeAttempt{NodeRunID: id(1), AttemptNo: 1, Status: domain.AttemptStatusSucceeded}}}
	coordinator, _ := NewRuntimeCoordinator(port)
	binding := validDeliveryBinding()
	if _, err := coordinator.Complete(context.Background(), CompleteDeliveryCommand{Binding: binding, Output: json.RawMessage(`{"ok":true}`), OutputSchemaVersion: 1}); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if port.transition.Result.Failure != nil || string(port.transition.Result.Output) != `{"ok":true}` {
		t.Fatalf("Complete transition = %+v", port.transition)
	}

	port.transitionResult = DeliveryTransitionResult{Run: domain.Run{ID: id(2), Status: domain.RunStatusRetryWait}, Node: domain.NodeRun{ID: id(1), RunID: id(2), Status: domain.NodeStatusRetryWait}, Attempt: domain.NodeAttempt{NodeRunID: id(1), AttemptNo: 1, Status: domain.AttemptStatusRetryScheduled}}
	_, err := coordinator.Fail(context.Background(), FailDeliveryCommand{Binding: binding, Failure: domain.FailureInput{Err: foundation.NewError(foundation.ErrorRetryableFailure, "DEPENDENCY_BUSY", true, errors.New("raw")), RetryAfter: time.Minute}})
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if port.transition.Result.Failure == nil || port.transition.Result.Failure.Class != domain.FailureClassRetryable || port.transition.Result.Failure.Code != "DEPENDENCY_BUSY" || port.transition.Result.Output != nil {
		t.Fatalf("Fail transition = %+v", port.transition)
	}
}

func TestRuntimeCoordinatorPreservesTransitionReplayFact(t *testing.T) {
	t.Parallel()
	port := &fakeRuntimeStatePort{transitionResult: DeliveryTransitionResult{Run: domain.Run{ID: id(2), Status: domain.RunStatusRunning}, Node: domain.NodeRun{ID: id(1), RunID: id(2), Status: domain.NodeStatusSucceeded}, Attempt: domain.NodeAttempt{NodeRunID: id(1), AttemptNo: 1, Status: domain.AttemptStatusSucceeded}, Replayed: true}}
	coordinator, _ := NewRuntimeCoordinator(port)
	result, err := coordinator.Complete(context.Background(), CompleteDeliveryCommand{Binding: validDeliveryBinding(), Output: json.RawMessage(`{"ok":true}`), OutputSchemaVersion: 1})
	if err != nil || !result.Replayed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRuntimeCoordinatorRejectsInvalidDeliveryResultBeforePort(t *testing.T) {
	t.Parallel()

	port := &fakeRuntimeStatePort{}
	coordinator, _ := NewRuntimeCoordinator(port)
	if _, err := coordinator.Complete(context.Background(), CompleteDeliveryCommand{Binding: validDeliveryBinding(), Output: json.RawMessage(`{`), OutputSchemaVersion: 1}); workflowErrorCode(err) != "WORKFLOW_DELIVERY_RESULT_INVALID" {
		t.Fatalf("Complete() error = %v", err)
	}
	badBinding := validDeliveryBinding()
	badBinding.Fence.AttemptNo = 0
	if _, err := coordinator.Fail(context.Background(), FailDeliveryCommand{Binding: badBinding, Failure: domain.FailureInput{Err: errors.New("failure")}}); workflowErrorCode(err) != "WORKFLOW_DELIVERY_RESULT_INVALID" {
		t.Fatalf("Fail() error = %v", err)
	}
	if port.transitionCalls != 0 {
		t.Fatalf("transition calls = %d", port.transitionCalls)
	}
}

func TestRuntimeCoordinatorRejectsPortResultThatContradictsRequestedOutcome(t *testing.T) {
	t.Parallel()

	port := &fakeRuntimeStatePort{transitionResult: DeliveryTransitionResult{
		Run:     domain.Run{ID: id(2), Status: domain.RunStatusRunning},
		Node:    domain.NodeRun{ID: id(1), RunID: id(2), Status: domain.NodeStatusFailed},
		Attempt: domain.NodeAttempt{NodeRunID: id(1), AttemptNo: 1, Status: domain.AttemptStatusFailed},
	}}
	coordinator, _ := NewRuntimeCoordinator(port)
	_, err := coordinator.Complete(context.Background(), CompleteDeliveryCommand{Binding: validDeliveryBinding(), Output: json.RawMessage(`{"ok":true}`), OutputSchemaVersion: 1})
	if workflowErrorCode(err) != "WORKFLOW_DELIVERY_TRANSITION_RESULT_INVALID" {
		t.Fatalf("Complete() error = %v", err)
	}
}

func TestRuntimeCoordinatorAcceptsControlCheckpointOutcome(t *testing.T) {
	port := &fakeRuntimeStatePort{transitionResult: DeliveryTransitionResult{
		Run:     domain.Run{ID: id(2), Status: domain.RunStatusPaused},
		Node:    domain.NodeRun{ID: id(1), RunID: id(2), Status: domain.NodeStatusPaused},
		Attempt: domain.NodeAttempt{NodeRunID: id(1), AttemptNo: 1, Status: domain.AttemptStatusCancelled},
	}}
	coordinator, _ := NewRuntimeCoordinator(port)
	if _, err := coordinator.Complete(context.Background(), CompleteDeliveryCommand{Binding: validDeliveryBinding(), Output: json.RawMessage(`{"ok":true}`), OutputSchemaVersion: 1}); err != nil {
		t.Fatalf("control checkpoint was rejected: %v", err)
	}
}

func TestRuntimeCoordinatorPauseResumeCancelBuildStableControlCommand(t *testing.T) {
	t.Parallel()

	port := &fakeRuntimeStatePort{controlResult: ControlPersistenceResult{WorkflowRunID: id(2), Status: domain.RunStatusPaused, Version: 4}}
	coordinator, _ := NewRuntimeCoordinator(port)
	command := RunControlCommand{WorkflowRunID: id(2), ExpectedVersion: 3, IdempotencyKey: " control-1 "}
	result, err := coordinator.Pause(context.Background(), command)
	if err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if port.control.Action != ControlActionPause || port.control.IdempotencyKey != "control-1" || port.control.RequestHash == "" || result.StatusURL != "/api/v1/workflows/"+string(id(2)) || result.Version != 4 {
		t.Fatalf("Pause() result=%+v command=%+v", result, port.control)
	}
	firstHash := port.control.RequestHash
	port.controlResult.Status = domain.RunStatusRunning
	if _, err := coordinator.Resume(context.Background(), command); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if port.control.Action != ControlActionResume || port.control.RequestHash == firstHash {
		t.Fatalf("Resume command = %+v", port.control)
	}
	port.controlResult.Status = domain.RunStatusCancelled
	if _, err := coordinator.Cancel(context.Background(), command); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if port.control.Action != ControlActionCancel {
		t.Fatalf("Cancel command = %+v", port.control)
	}
}

func TestRuntimeCoordinatorRejectsInvalidControlBeforePort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command RunControlCommand
		code    string
	}{
		{command: RunControlCommand{WorkflowRunID: id(2), ExpectedVersion: 1}, code: "IDEMPOTENCY_KEY_REQUIRED"},
		{command: RunControlCommand{WorkflowRunID: id(2), ExpectedVersion: 1, IdempotencyKey: string(make([]byte, MaxIdempotencyKeyLength+1))}, code: "WORKFLOW_CONTROL_INVALID"},
		{command: RunControlCommand{ExpectedVersion: 1, IdempotencyKey: "key"}, code: "WORKFLOW_CONTROL_INVALID"},
		{command: RunControlCommand{WorkflowRunID: id(2), ExpectedVersion: 0, IdempotencyKey: "key"}, code: "WORKFLOW_CONTROL_INVALID"},
	}
	for i, test := range tests {
		port := &fakeRuntimeStatePort{}
		coordinator, _ := NewRuntimeCoordinator(port)
		if _, err := coordinator.Pause(context.Background(), test.command); workflowErrorCode(err) != test.code {
			t.Errorf("case %d Pause() error = %v", i, err)
		}
		if port.controlCalls != 0 {
			t.Errorf("case %d called port", i)
		}
	}
}

func TestNewRuntimeCoordinatorRejectsNilAndTypedNilPort(t *testing.T) {
	t.Parallel()

	if _, err := NewRuntimeCoordinator(nil); workflowErrorCode(err) != "WORKFLOW_RUNTIME_STATE_PORT_MISSING" {
		t.Fatalf("nil port error = %v", err)
	}
	var typedNil *fakeRuntimeStatePort
	if _, err := NewRuntimeCoordinator(typedNil); workflowErrorCode(err) != "WORKFLOW_RUNTIME_STATE_PORT_MISSING" {
		t.Fatalf("typed nil port error = %v", err)
	}
}

func validClaimCommand() ClaimCommand {
	return ClaimCommand{NodeRunID: id(1), DispatchNo: 1, DeliveryID: "job-1-attempt-0", RiverJobID: 1, RiverJobAttempt: 0, LeaseOwner: "worker", LeaseDuration: time.Minute}
}

func validDeliveryBinding() DeliveryBinding {
	return DeliveryBinding{NodeRunID: id(1), DispatchNo: 1, DeliveryID: "job-1-attempt-0", Fence: domain.LeaseFence{Owner: "worker", AttemptNo: 1, NodeVersion: 2}}
}

func workflowErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

type fakeRuntimeStatePort struct {
	claim            ClaimCommand
	claimResult      ClaimResult
	claimCalls       int
	heartbeat        HeartbeatCommand
	heartbeatResult  HeartbeatResult
	heartbeatCalls   int
	transition       DeliveryTransition
	transitionResult DeliveryTransitionResult
	transitionCalls  int
	control          ControlTransition
	controlResult    ControlPersistenceResult
	controlCalls     int
	err              error
}

func (f *fakeRuntimeStatePort) Claim(_ context.Context, command ClaimCommand) (ClaimResult, error) {
	f.claim, f.claimCalls = command, f.claimCalls+1
	return f.claimResult, f.err
}

func (f *fakeRuntimeStatePort) Heartbeat(_ context.Context, command HeartbeatCommand) (HeartbeatResult, error) {
	f.heartbeat, f.heartbeatCalls = command, f.heartbeatCalls+1
	return f.heartbeatResult, f.err
}

func (f *fakeRuntimeStatePort) TransitionDelivery(_ context.Context, command DeliveryTransition) (DeliveryTransitionResult, error) {
	f.transition, f.transitionCalls = command, f.transitionCalls+1
	return f.transitionResult, f.err
}

func (f *fakeRuntimeStatePort) Control(_ context.Context, command ControlTransition) (ControlPersistenceResult, error) {
	f.control, f.controlCalls = command, f.controlCalls+1
	return f.controlResult, f.err
}
