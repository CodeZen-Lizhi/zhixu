package workflowpostgres

import (
	"context"
	"testing"

	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
)

type typedNilJobInserter struct{}

func (*typedNilJobInserter) InsertTx(context.Context, any, riveradapter.NodeJobArgs, riveradapter.InsertOptions) (application.JobReceipt, error) {
	return application.JobReceipt{}, nil
}

func TestNewRuntimeRepositoryRejectsTypedNilJobInserter(t *testing.T) {
	var inserter *typedNilJobInserter
	if _, err := NewRuntimeRepository(fakeDB{}, inserter); err == nil {
		t.Fatal("typed nil job inserter was accepted")
	}
}

type fakeDB struct{}

func (fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (fakeDB) Begin(context.Context) (pgx.Tx, error)            { return nil, nil }
