package hostcontroller

import (
	"context"
	"errors"
	"net/url"
	"testing"
)

const runtimeWorkspaceID = "10000000-0000-4000-8000-000000000001"

type runtimeStateReader struct {
	state State
	err   error
	calls int
}

func (reader *runtimeStateReader) State(context.Context) (State, error) {
	reader.calls++
	return reader.state, reader.err
}

type runtimeBackendDriver struct {
	backend *url.URL
	err     error
	calls   int
}

func (driver *runtimeBackendDriver) CurrentBackend(context.Context) (*url.URL, error) {
	driver.calls++
	return driver.backend, driver.err
}

func waitingRuntimeState() State {
	return State{
		Runtime: RuntimeState{
			Status: RuntimeWaitingForWorkspace,
			API:    ProcessState{Status: ProcessStopped},
			Worker: ProcessState{Status: ProcessStopped},
		},
		PollAfterMS: 1000,
	}
}

func readyRuntimeState() State {
	return State{
		Runtime: RuntimeState{
			Status: RuntimeReady,
			API:    ProcessState{Status: ProcessReady},
			Worker: ProcessState{Status: ProcessReady},
		},
		ActiveWorkspace: &Workspace{
			WorkspaceID:  runtimeWorkspaceID,
			Availability: AvailabilityAvailable,
		},
		PollAfterMS: 1000,
	}
}

func TestProjectRuntimeAccessStateMatrix(t *testing.T) {
	t.Parallel()

	runningOperation := &Operation{OperationID: "operation-1", Phase: "preparing"}
	terminalOperation := &Operation{OperationID: "operation-1", Phase: "completed", Result: "succeeded"}
	waitingWithActive := waitingRuntimeState()
	waitingWithActive.ActiveWorkspace = readyRuntimeState().ActiveWorkspace
	waitingWithStartingAPI := waitingRuntimeState()
	waitingWithStartingAPI.Runtime.API.Status = ProcessStarting
	readyWithoutActive := readyRuntimeState()
	readyWithoutActive.ActiveWorkspace = nil
	readyWithUnavailableActive := readyRuntimeState()
	readyWithUnavailableActive.ActiveWorkspace.Availability = AvailabilityUnavailable
	readyWithStartingWorker := readyRuntimeState()
	readyWithStartingWorker.Runtime.Worker.Status = ProcessStarting
	readyWithRunningOperation := readyRuntimeState()
	readyWithRunningOperation.Operation = runningOperation
	readyWithTerminalOperation := readyRuntimeState()
	readyWithTerminalOperation.Operation = terminalOperation
	readyWithInvalidID := readyRuntimeState()
	readyWithInvalidID.ActiveWorkspace.WorkspaceID = "workspace-private"
	recoveryFailed := readyRuntimeState()
	recoveryFailed.Runtime.Status = RuntimeRecoveryFailed

	tests := []struct {
		name       string
		state      State
		wantStatus RuntimeAccessStatus
		wantID     string
		wantPollMS int
	}{
		{name: "waiting", state: waitingRuntimeState(), wantStatus: RuntimeAccessWaiting, wantPollMS: 1000},
		{name: "waiting with active workspace", state: waitingWithActive, wantStatus: RuntimeAccessUnavailable, wantPollMS: 1000},
		{name: "waiting with starting api", state: waitingWithStartingAPI, wantStatus: RuntimeAccessUnavailable, wantPollMS: 1000},
		{name: "ready", state: readyRuntimeState(), wantStatus: RuntimeAccessReady, wantID: runtimeWorkspaceID, wantPollMS: 1000},
		{name: "ready without active workspace", state: readyWithoutActive, wantStatus: RuntimeAccessUnavailable, wantPollMS: 1000},
		{name: "ready with unavailable active workspace", state: readyWithUnavailableActive, wantStatus: RuntimeAccessUnavailable, wantPollMS: 1000},
		{name: "ready with starting worker", state: readyWithStartingWorker, wantStatus: RuntimeAccessUnavailable, wantPollMS: 1000},
		{name: "ready with running operation", state: readyWithRunningOperation, wantStatus: RuntimeAccessUnavailable, wantPollMS: 1000},
		{name: "ready with terminal operation", state: readyWithTerminalOperation, wantStatus: RuntimeAccessReady, wantID: runtimeWorkspaceID, wantPollMS: 1000},
		{name: "ready with invalid workspace id", state: readyWithInvalidID, wantStatus: RuntimeAccessUnavailable, wantPollMS: 1000},
		{name: "recovery failed", state: recoveryFailed, wantStatus: RuntimeAccessUnavailable, wantPollMS: 1000},
		{name: "poll lower bound", state: State{PollAfterMS: -1}, wantStatus: RuntimeAccessUnavailable, wantPollMS: runtimeAccessPollMinMS},
		{name: "poll upper bound", state: State{PollAfterMS: 9000}, wantStatus: RuntimeAccessUnavailable, wantPollMS: runtimeAccessPollMaxMS},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := projectRuntimeAccess(test.state)
			if got.Status != test.wantStatus || got.WorkspaceID != test.wantID || got.PollAfterMS != test.wantPollMS || got.Backend != nil {
				t.Fatalf("projectRuntimeAccess() = %+v, want status=%q id=%q poll=%d", got, test.wantStatus, test.wantID, test.wantPollMS)
			}
		})
	}
}

func TestStateBackendLocatesReadyRuntimeOnce(t *testing.T) {
	t.Parallel()
	backendURL, err := url.Parse("http://127.0.0.1:32100")
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	reader := &runtimeStateReader{state: readyRuntimeState()}
	driver := &runtimeBackendDriver{backend: backendURL}

	got, err := (StateBackend{State: reader, Runtime: driver}).LocateRuntime(context.Background())
	if err != nil {
		t.Fatalf("LocateRuntime() error: %v", err)
	}
	if reader.calls != 1 || driver.calls != 1 || got.Status != RuntimeAccessReady || got.WorkspaceID != runtimeWorkspaceID || got.Backend.String() != backendURL.String() {
		t.Fatalf("LocateRuntime() = %+v, state calls=%d backend calls=%d", got, reader.calls, driver.calls)
	}
}

func TestStateBackendSkipsDiscoveryUntilReady(t *testing.T) {
	t.Parallel()
	reader := &runtimeStateReader{state: waitingRuntimeState()}
	driver := &runtimeBackendDriver{err: errors.New("must not be called")}

	got, err := (StateBackend{State: reader, Runtime: driver}).LocateRuntime(context.Background())
	if err != nil {
		t.Fatalf("LocateRuntime() error: %v", err)
	}
	if reader.calls != 1 || driver.calls != 0 || got.Status != RuntimeAccessWaiting {
		t.Fatalf("LocateRuntime() = %+v, state calls=%d backend calls=%d", got, reader.calls, driver.calls)
	}
}

func TestStateBackendPropagatesDependencyFailures(t *testing.T) {
	t.Parallel()
	stateFailure := errors.New("state failed")
	runtimeFailure := errors.New("runtime failed")

	tests := []struct {
		name        string
		reader      *runtimeStateReader
		driver      *runtimeBackendDriver
		wantError   error
		wantRuntime int
	}{
		{
			name:      "state",
			reader:    &runtimeStateReader{err: stateFailure},
			driver:    &runtimeBackendDriver{},
			wantError: stateFailure,
		},
		{
			name:        "backend",
			reader:      &runtimeStateReader{state: readyRuntimeState()},
			driver:      &runtimeBackendDriver{err: runtimeFailure},
			wantError:   runtimeFailure,
			wantRuntime: 1,
		},
		{
			name:        "invalid backend",
			reader:      &runtimeStateReader{state: readyRuntimeState()},
			driver:      &runtimeBackendDriver{},
			wantRuntime: 1,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := (StateBackend{State: test.reader, Runtime: test.driver}).LocateRuntime(context.Background())
			if test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("LocateRuntime() error = %v, want %v", err, test.wantError)
			}
			if test.wantError == nil && err == nil {
				t.Fatal("LocateRuntime() accepted an invalid backend")
			}
			if test.reader.calls != 1 || test.driver.calls != test.wantRuntime {
				t.Fatalf("state calls=%d backend calls=%d", test.reader.calls, test.driver.calls)
			}
		})
	}
}
