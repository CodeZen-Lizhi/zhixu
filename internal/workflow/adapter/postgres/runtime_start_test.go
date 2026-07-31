package workflowpostgres

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
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
	cancellation := &cancellationSafetyGuardStub{safe: true}
	terminal = &workflowTerminalHookStub{}
	repository, err := NewRuntimeRepositoryWithHooks(fakeDB{}, &typedNilJobInserter{}, RuntimeRepositoryHooks{
		CancellationSafety: cancellation,
		Terminal:           terminal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.cancellation != cancellation || repository.terminal != terminal {
		t.Fatalf("hooks were not injected: cancellation=%T terminal=%T", repository.cancellation, repository.terminal)
	}
}

type fakeDB struct{}

func (fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (fakeDB) Begin(context.Context) (pgx.Tx, error)            { return nil, nil }
