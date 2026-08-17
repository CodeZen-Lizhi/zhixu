package workflowpostgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
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

type fakeDB struct{}

func (fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (fakeDB) Begin(context.Context) (pgx.Tx, error)            { return nil, nil }
