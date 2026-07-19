package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type nilDatabase struct{}

func (*nilDatabase) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("not implemented") }

func TestNewRepositoryRejectsNilAndTypedNilDatabase(t *testing.T) {
	if _, err := NewRepository(nil); errorCodeForUnit(err) != ErrorCodeDatabaseUnavailable {
		t.Fatalf("nil database code=%s err=%v", errorCodeForUnit(err), err)
	}
	var database *nilDatabase
	if _, err := NewRepository(database); errorCodeForUnit(err) != ErrorCodeDatabaseUnavailable {
		t.Fatalf("typed nil database code=%s err=%v", errorCodeForUnit(err), err)
	}
}

func TestClassifyPreservesCancellationAndPostgreSQLState(t *testing.T) {
	cancelled := classify(context.Canceled)
	if errorCodeForUnit(cancelled) != ErrorCodeDatabaseCancelled || !errors.Is(cancelled, context.Canceled) {
		t.Fatalf("cancelled=%v", cancelled)
	}
	pgErr := &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}
	classified := classify(pgErr)
	var preserved *pgconn.PgError
	if errorCodeForUnit(classified) != ErrorCodeDatabaseUnavailable || !errors.As(classified, &preserved) || preserved.Code != "40P01" {
		t.Fatalf("classified=%v preserved=%#v", classified, preserved)
	}
}

func TestJSONEqualityHandlesAbsentAndCanonicalObjects(t *testing.T) {
	if !jsonEqual(nil, nil) || jsonEqual(nil, []byte(`{}`)) || !jsonEqual([]byte(`{"b":2,"a":1}`), []byte(`{"a":1,"b":2}`)) {
		t.Fatal("json equality does not preserve absent and canonical object semantics")
	}
}

func errorCodeForUnit(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
