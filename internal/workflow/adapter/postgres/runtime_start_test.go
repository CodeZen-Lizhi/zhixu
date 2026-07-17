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

func TestNewRuntimeRepositoryRejectsTypedNilJobInserter(t *testing.T) {
	var inserter *typedNilJobInserter
	if _, err := NewRuntimeRepository(fakeDB{}, inserter); err == nil {
		t.Fatal("typed nil job inserter was accepted")
	}
}

func TestNewRuntimeRepositoryValidatesCancellationSafetyGuard(t *testing.T) {
	var guard *cancellationSafetyGuardStub
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

type fakeDB struct{}

func (fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (fakeDB) Begin(context.Context) (pgx.Tx, error)            { return nil, nil }
