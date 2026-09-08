package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewRepositoryRejectsNilAndTypedNilDatabase(t *testing.T) {
	if _, err := NewGORMRepository(nil, nil, nil); errorCodeForUnit(err) != ErrorCodeDatabaseUnavailable {
		t.Fatalf("nil database code=%s err=%v", errorCodeForUnit(err), err)
	}
	var database *platformpostgres.Pool
	if _, err := NewGORMRepository(database, nil, nil); errorCodeForUnit(err) != ErrorCodeDatabaseUnavailable {
		t.Fatalf("typed nil database code=%s err=%v", errorCodeForUnit(err), err)
	}
}

func TestClassifyPreservesCancellationAndPostgreSQLState(t *testing.T) {
	cancelled := classifyGORMTools(context.Background(), context.Canceled)
	if errorCodeForUnit(cancelled) != ErrorCodeDatabaseCancelled || !errors.Is(cancelled, context.Canceled) {
		t.Fatalf("cancelled=%v", cancelled)
	}
	pgErr := &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}
	classified := classifyGORMTools(context.Background(), pgErr)
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
