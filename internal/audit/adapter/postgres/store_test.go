package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	postgresdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestListKeepsGlobalScopeAndUsesStableTimeIDCursor(t *testing.T) {
	database := &capturingDB{rows: &emptyRows{}}
	store := newCapturingAuditStore(t, database)
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
		!strings.Contains(database.query, "(occurred_at,id) < ($3::timestamptz,$4::uuid)") {
		t.Fatalf("List query does not enforce scope and stable cursor: %s", database.query)
	}
	if len(database.args) != 5 || database.args[0] != nil || database.args[1] != before || database.args[2] != before || database.args[3] != string(beforeID) || database.args[4] != int64(25) {
		t.Fatalf("List args=%#v", database.args)
	}
}

func TestListRejectsIncompleteCursorBeforeQuery(t *testing.T) {
	database := &capturingDB{rows: &emptyRows{}}
	store := newCapturingAuditStore(t, database)
	_, err := store.List(context.Background(), domain.ListQuery{
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
	rows  driver.Rows
}

func (database *capturingDB) Connect(context.Context) (driver.Conn, error) { return database, nil }
func (database *capturingDB) Driver() driver.Driver                        { return database }
func (database *capturingDB) Open(string) (driver.Conn, error)             { return database, nil }
func (*capturingDB) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*capturingDB) Close() error              { return nil }
func (*capturingDB) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (database *capturingDB) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	database.query = query
	database.args = make([]any, len(args))
	for index, argument := range args {
		database.args[index] = argument.Value
	}
	return database.rows, nil
}
func (*capturingDB) Within(context.Context, foundation.TransactionOptions, foundation.TransactionFunc) error {
	return errors.New("unexpected transaction")
}

func newCapturingAuditStore(t *testing.T, capture *capturingDB) *GORMStore {
	t.Helper()
	sqlDB := sql.OpenDB(capture)
	t.Cleanup(func() { _ = sqlDB.Close() })
	database, err := gorm.Open(postgresdriver.New(postgresdriver.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return &GORMStore{database: database, unitOfWork: capture}
}

type emptyRows struct{ closed bool }

func (*emptyRows) Columns() []string              { return []string{"id"} }
func (rows *emptyRows) Close() error              { rows.closed = true; return nil }
func (rows *emptyRows) Next([]driver.Value) error { rows.closed = true; return io.EOF }
