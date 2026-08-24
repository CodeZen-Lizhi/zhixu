package workflowpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type typedNilJobInserter struct{}

func (*typedNilJobInserter) InsertTx(context.Context, any, riveradapter.NodeJobArgs, riveradapter.InsertOptions) (application.JobReceipt, error) {
	return application.JobReceipt{}, nil
}

type cancellationSafetyGuardStub struct {
	safe bool
	err  error
}

func (g *cancellationSafetyGuardStub) SafeToCancelWorkflowNode(context.Context, any, foundation.ID) (bool, error) {
	return g.safe, g.err
}

type workflowTerminalHookStub struct {
	err error
}

func (h *workflowTerminalHookStub) OnWorkflowNodeTerminal(context.Context, any, application.WorkflowNodeTerminalEvent) error {
	return h.err
}

type workflowControlHookStub struct {
	err error
}

func (h *workflowControlHookStub) OnWorkflowControl(context.Context, any, application.WorkflowControlEvent) error {
	return h.err
}

func TestNewRuntimeRepositoryRejectsTypedNilJobInserter(t *testing.T) {
	var inserter *typedNilJobInserter
	if _, err := NewRuntimeRepository(fakeDB{}, inserter); err == nil {
		t.Fatal("typed nil job inserter was accepted")
	}
}

func TestNewRuntimeRepositoryValidatesCancellationSafetyGuard(t *testing.T) {
	var guard *cancellationSafetyGuardStub
	var inserter *typedNilJobInserter
	if _, err := NewRuntimeRepository(fakeDB{}, inserter, guard); !hasFoundationCode(err, "WORKFLOW_RUNTIME_DATABASE_UNAVAILABLE") {
		t.Fatalf("database dependency error precedence changed: %v", err)
	}
	if _, err := NewRuntimeRepository(fakeDB{}, &typedNilJobInserter{}, guard); err == nil {
		t.Fatal("typed nil cancellation guard was accepted")
	}
	if _, err := NewRuntimeRepository(fakeDB{}, &typedNilJobInserter{}, &cancellationSafetyGuardStub{}, &cancellationSafetyGuardStub{}); err == nil {
		t.Fatal("multiple cancellation guards were accepted")
	}
	repository, err := NewRuntimeRepository(fakeDB{}, &typedNilJobInserter{}, &cancellationSafetyGuardStub{safe: false})
	if err != nil {
		t.Fatal(err)
	}
	if safe, err := repository.safeToCancelWorkflowNode(context.Background(), nil, foundation.ID("node")); err != nil || safe {
		t.Fatalf("safe=%t err=%v", safe, err)
	}
	repository.cancellation = &cancellationSafetyGuardStub{err: errors.New("database unavailable")}
	if _, err := repository.safeToCancelWorkflowNode(context.Background(), nil, foundation.ID("node")); err == nil {
		t.Fatal("guard error was ignored")
	} else {
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != "WORKFLOW_CANCELLATION_SAFETY_UNAVAILABLE" || !classified.Retryable {
			t.Fatalf("guard error=%v", err)
		}
	}
}

func hasFoundationCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}

func TestNewRuntimeRepositoryWithHooksValidatesAndInjectsLifecycleHooks(t *testing.T) {
	var terminal *workflowTerminalHookStub
	if _, err := NewRuntimeRepositoryWithHooks(fakeDB{}, &typedNilJobInserter{}, RuntimeRepositoryHooks{Terminal: terminal}); err == nil {
		t.Fatal("typed nil terminal hook was accepted")
	}
	var control *workflowControlHookStub
	if _, err := NewRuntimeRepositoryWithHooks(fakeDB{}, &typedNilJobInserter{}, RuntimeRepositoryHooks{Control: control}); err == nil {
		t.Fatal("typed nil control hook was accepted")
	}
	cancellation := &cancellationSafetyGuardStub{safe: true}
	terminal = &workflowTerminalHookStub{}
	control = &workflowControlHookStub{}
	repository, err := NewRuntimeRepositoryWithHooks(fakeDB{}, &typedNilJobInserter{}, RuntimeRepositoryHooks{
		CancellationSafety: cancellation,
		Terminal:           terminal,
		Control:            control,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.cancellation != cancellation || repository.terminal != terminal || repository.control != control {
		t.Fatalf("hooks were not injected: cancellation=%T terminal=%T control=%T", repository.cancellation, repository.terminal, repository.control)
	}
	if repository.modelRuntimeFreshWithin != modelsettingsapplication.DefaultRuntimeFreshWithin {
		t.Fatalf("default model runtime freshness=%s want=%s", repository.modelRuntimeFreshWithin, modelsettingsapplication.DefaultRuntimeFreshWithin)
	}
	custom, err := NewRuntimeRepositoryWithHooks(fakeDB{}, &typedNilJobInserter{}, RuntimeRepositoryHooks{ModelRuntimeFreshWithin: 45 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if custom.modelRuntimeFreshWithin != 45*time.Second {
		t.Fatalf("custom model runtime freshness=%s", custom.modelRuntimeFreshWithin)
	}
	if _, err := NewRuntimeRepositoryWithHooks(fakeDB{}, &typedNilJobInserter{}, RuntimeRepositoryHooks{ModelRuntimeFreshWithin: time.Millisecond}); !hasFoundationCode(err, "WORKFLOW_MODEL_RUNTIME_FRESHNESS_INVALID") {
		t.Fatalf("invalid model runtime freshness error=%v", err)
	}
}

func TestClassifyGORMWorkflowPreservesContextContract(t *testing.T) {
	t.Run("cancel cause", func(t *testing.T) {
		shutdown := errors.New("worker shutdown")
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(shutdown)

		err := classifyGORMWorkflow(ctx, errors.New("driver stopped"), "WORKFLOW_TEST_DATABASE_UNAVAILABLE")
		assertGORMWorkflowError(t, err, foundation.ErrorNonRetryableFailure, "WORKFLOW_TEST_DATABASE_UNAVAILABLE", false)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, shutdown) {
			t.Fatalf("cancel cause chain=%v", err)
		}
	})

	t.Run("deadline cause", func(t *testing.T) {
		timeout := errors.New("query budget exhausted")
		ctx, cancel := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Second), timeout)
		defer cancel()
		<-ctx.Done()

		err := classifyGORMWorkflow(ctx, errors.New("driver timed out"), "WORKFLOW_TEST_DATABASE_UNAVAILABLE")
		assertGORMWorkflowError(t, err, foundation.ErrorRetryableFailure, "WORKFLOW_TEST_DATABASE_UNAVAILABLE", true)
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, timeout) {
			t.Fatalf("deadline cause chain=%v", err)
		}
	})

	t.Run("wrapped raw cancellation", func(t *testing.T) {
		raw := fmt.Errorf("driver aborted: %w", context.Canceled)
		err := classifyGORMWorkflow(context.Background(), raw, "WORKFLOW_TEST_DATABASE_UNAVAILABLE")
		assertGORMWorkflowError(t, err, foundation.ErrorNonRetryableFailure, "WORKFLOW_TEST_DATABASE_UNAVAILABLE", false)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, raw) {
			t.Fatalf("wrapped cancel cause chain=%v", err)
		}
	})
}

func TestClassifyGORMWorkflowPreservesSQLStateAndUnknownCause(t *testing.T) {
	tests := []struct {
		name      string
		sqlState  string
		kind      foundation.ErrorKind
		code      string
		retryable bool
	}{
		{name: "unique", sqlState: "23505", kind: foundation.ErrorVersionConflict, code: "WORKFLOW_CONFLICT"},
		{name: "foreign key", sqlState: "23503", kind: foundation.ErrorConsistencyViolation, code: "WORKFLOW_REFERENCE_INVALID"},
		{name: "check", sqlState: "23514", kind: foundation.ErrorInvalidInput, code: "WORKFLOW_DATA_INVALID"},
		{name: "invalid text", sqlState: "22P02", kind: foundation.ErrorInvalidInput, code: "WORKFLOW_DATA_INVALID"},
		{name: "serialization", sqlState: "40001", kind: foundation.ErrorRetryableFailure, code: "WORKFLOW_TEST_DATABASE_UNAVAILABLE", retryable: true},
		{name: "deadlock", sqlState: "40P01", kind: foundation.ErrorRetryableFailure, code: "WORKFLOW_TEST_DATABASE_UNAVAILABLE", retryable: true},
		{name: "lock unavailable", sqlState: "55P03", kind: foundation.ErrorDependencyUnavailable, code: "WORKFLOW_TEST_DATABASE_UNAVAILABLE", retryable: true},
		{name: "object state", sqlState: "55000", kind: foundation.ErrorDependencyUnavailable, code: "WORKFLOW_TEST_DATABASE_UNAVAILABLE", retryable: true},
		{name: "query canceled without caller cancellation", sqlState: "57014", kind: foundation.ErrorDependencyUnavailable, code: "WORKFLOW_TEST_DATABASE_UNAVAILABLE", retryable: true},
		{name: "unknown", sqlState: "ZZ999", kind: foundation.ErrorDependencyUnavailable, code: "WORKFLOW_TEST_DATABASE_UNAVAILABLE", retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			postgresCause := &pgconn.PgError{Code: test.sqlState}
			err := classifyGORMWorkflow(context.Background(), postgresCause, "WORKFLOW_TEST_DATABASE_UNAVAILABLE")
			assertGORMWorkflowError(t, err, test.kind, test.code, test.retryable)
			if !errors.Is(err, postgresCause) {
				t.Fatalf("SQLSTATE %s cause was lost: %v", test.sqlState, err)
			}
		})
	}

	raw := errors.New("private database detail")
	err := classifyGORMWorkflow(context.Background(), raw, "WORKFLOW_TEST_DATABASE_UNAVAILABLE")
	assertGORMWorkflowError(t, err, foundation.ErrorDependencyUnavailable, "WORKFLOW_TEST_DATABASE_UNAVAILABLE", true)
	if !errors.Is(err, raw) {
		t.Fatalf("unknown database cause was lost: %v", err)
	}
	if strings.Contains(err.Error(), raw.Error()) {
		t.Fatalf("public error leaked cause: %q", err.Error())
	}
}

func TestStaleGORMToolCallExecution(t *testing.T) {
	databaseNow := time.Unix(1_700_000_000, 0).UTC()
	fresh := application.ToolCallRecoveryFenceResult{
		Found: true, WorkflowStatus: domain.RunStatusRunning,
		NodeStatus: domain.NodeStatusRunning, NodeAttempt: 2,
		NodeLeaseOwner: "worker-1", NodeLeaseOwnerSet: true,
		NodeLeaseUntil: databaseNow.Add(time.Minute), NodeLeaseUntilSet: true,
		AttemptStatus: domain.AttemptStatusRunning, AttemptNo: 2,
		AttemptLeaseOwner: "worker-1", AttemptLeaseOwnerSet: true,
		AttemptLeaseUntil: databaseNow.Add(time.Minute), AttemptLeaseUntilSet: true,
		DatabaseNow: databaseNow,
	}
	if staleGORMToolCallExecution(fresh) {
		t.Fatal("fresh execution was classified stale")
	}

	tests := []struct {
		name   string
		mutate func(*application.ToolCallRecoveryFenceResult)
	}{
		{name: "run stopped", mutate: func(result *application.ToolCallRecoveryFenceResult) { result.WorkflowStatus = domain.RunStatusPaused }},
		{name: "node stopped", mutate: func(result *application.ToolCallRecoveryFenceResult) { result.NodeStatus = domain.NodeStatusFailed }},
		{name: "attempt stopped", mutate: func(result *application.ToolCallRecoveryFenceResult) {
			result.AttemptStatus = domain.AttemptStatusFailed
		}},
		{name: "attempt fence differs", mutate: func(result *application.ToolCallRecoveryFenceResult) { result.AttemptNo++ }},
		{name: "node owner absent", mutate: func(result *application.ToolCallRecoveryFenceResult) { result.NodeLeaseOwnerSet = false }},
		{name: "attempt owner absent", mutate: func(result *application.ToolCallRecoveryFenceResult) { result.AttemptLeaseOwnerSet = false }},
		{name: "owners differ", mutate: func(result *application.ToolCallRecoveryFenceResult) { result.AttemptLeaseOwner = "worker-2" }},
		{name: "node lease absent", mutate: func(result *application.ToolCallRecoveryFenceResult) { result.NodeLeaseUntilSet = false }},
		{name: "attempt lease absent", mutate: func(result *application.ToolCallRecoveryFenceResult) { result.AttemptLeaseUntilSet = false }},
		{name: "leases differ", mutate: func(result *application.ToolCallRecoveryFenceResult) {
			result.AttemptLeaseUntil = result.AttemptLeaseUntil.Add(time.Second)
		}},
		{name: "lease expired", mutate: func(result *application.ToolCallRecoveryFenceResult) {
			result.NodeLeaseUntil = databaseNow
			result.AttemptLeaseUntil = databaseNow
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := fresh
			test.mutate(&result)
			if !staleGORMToolCallExecution(result) {
				t.Fatalf("stale execution was accepted: %+v", result)
			}
		})
	}
}

func assertGORMWorkflowError(t *testing.T, err error, kind foundation.ErrorKind, code string, retryable bool) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error is not classified: %v", err)
	}
	if classified.Kind != kind || classified.Code != code || classified.Retryable != retryable {
		t.Fatalf("error=%+v want kind=%s code=%s retryable=%t", classified, kind, code, retryable)
	}
}

type fakeDB struct{}

func (fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (fakeDB) Begin(context.Context) (pgx.Tx, error)            { return nil, nil }
