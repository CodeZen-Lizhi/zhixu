package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestListKeepsGlobalScopeAndUsesStableTimeIDCursor(t *testing.T) {
	database := &capturingDB{rows: &emptyRows{}}
	store, err := NewStore(database)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	before := time.Date(2026, 7, 23, 8, 9, 10, 123456000, time.UTC)
	beforeID := foundation.ID("10000000-0000-4000-8000-000000000030")
	items, err := store.List(context.Background(), domain.ListQuery{Before: before, BeforeID: beforeID, Limit: 25})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items=%#v, want empty", items)
	}
	if !strings.Contains(database.query, "workspace_id IS NOT DISTINCT FROM $1::uuid") ||
		!strings.Contains(database.query, "(occurred_at,id) < ($2::timestamptz,$3::uuid)") {
		t.Fatalf("List query does not enforce scope and stable cursor: %s", database.query)
	}
	if len(database.args) != 4 || database.args[0] != nil || database.args[1] != before || database.args[2] != string(beforeID) || database.args[3] != 25 {
		t.Fatalf("List args=%#v", database.args)
	}
}

func TestListRejectsIncompleteCursorBeforeQuery(t *testing.T) {
	database := &capturingDB{rows: &emptyRows{}}
	store, err := NewStore(database)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	_, err = store.List(context.Background(), domain.ListQuery{
		Before: time.Date(2026, 7, 23, 8, 9, 10, 0, time.UTC),
		Limit:  25,
	})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeInvalid {
		t.Fatalf("error=%v, want invalid cursor", err)
	}
	if database.query != "" {
		t.Fatalf("invalid cursor reached database: %s", database.query)
	}
}

type capturingDB struct {
	query string
	args  []any
	rows  pgx.Rows
}

func (database *capturingDB) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	database.query = query
	database.args = append([]any(nil), args...)
	return database.rows, nil
}

func (*capturingDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeScanner{err: pgx.ErrNoRows}
}

type emptyRows struct{ closed bool }

func (rows *emptyRows) Close()                                  { rows.closed = true }
func (*emptyRows) Err() error                                   { return nil }
func (*emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (rows *emptyRows) Next() bool                              { rows.closed = true; return false }
func (*emptyRows) Scan(...any) error                            { return errors.New("no current row") }
func (*emptyRows) Values() ([]any, error)                       { return nil, errors.New("no current row") }
func (*emptyRows) RawValues() [][]byte                          { return nil }
func (*emptyRows) Conn() *pgx.Conn                              { return nil }
