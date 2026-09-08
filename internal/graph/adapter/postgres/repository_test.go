package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestNewGORMRepositoryFailsClosedWithoutDatabase(t *testing.T) {
	repository, err := NewGORMRepository(nil)
	if repository != nil {
		t.Fatal("nil database unexpectedly created repository")
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != graphdomain.ErrorCodeDependencyUnavailable {
		t.Fatalf("err=%v", err)
	}
}

func TestNewGORMRepositoryRejectsIncompletePool(t *testing.T) {
	repository, err := NewGORMRepository(&platformpostgres.Pool{})
	var classified *foundation.Error
	if repository != nil || !errors.As(err, &classified) || classified.Code != graphdomain.ErrorCodeDependencyUnavailable {
		t.Fatalf("repository=%#v err=%v", repository, err)
	}
}

func TestDisplaySummaryPreservesUTF8Boundary(t *testing.T) {
	value := strings.Repeat("知", 300)
	summary := displaySummary(value, 512)
	if !utf8.ValidString(summary) || len(summary) > 512 || summary == value {
		t.Fatalf("summary bytes=%d valid=%v", len(summary), utf8.ValidString(summary))
	}
}

func TestClassifyProjectionScanSeparatesProjectionDamageFromTimeout(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		err       error
		kind      foundation.ErrorKind
		code      string
		retryable bool
	}{
		{name: "projection damage", err: errors.New("invalid applicability"), kind: foundation.ErrorConsistencyViolation, code: graphdomain.ErrorCodeProjectionInconsistent},
		{name: "cancel", err: context.Canceled, kind: foundation.ErrorNonRetryableFailure, code: graphdomain.ErrorCodeQueryCanceled},
		{name: "timeout", err: context.DeadlineExceeded, kind: foundation.ErrorDependencyUnavailable, code: graphdomain.ErrorCodeQueryTimeout, retryable: true},
		{name: "postgres statement timeout", err: &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}, kind: foundation.ErrorDependencyUnavailable, code: graphdomain.ErrorCodeQueryTimeout, retryable: true},
		{name: "postgres explicit cancel", err: &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"}, kind: foundation.ErrorNonRetryableFailure, code: graphdomain.ErrorCodeQueryCanceled},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var classified *foundation.Error
			if err := classifyProjectionScan(testCase.err); !errors.As(err, &classified) || classified.Kind != testCase.kind || classified.Code != testCase.code || classified.Retryable != testCase.retryable {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRelationEvidenceHrefCarriesWorkspaceScope(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	relationID := foundation.ID("10000000-0000-4000-8000-000000000002")
	want := "/api/v1/graph/relations/10000000-0000-4000-8000-000000000002/evidence?workspace_id=10000000-0000-4000-8000-000000000001"
	if got := relationEvidenceHref(workspaceID, relationID); got != want {
		t.Fatalf("relationEvidenceHref() = %q, want %q", got, want)
	}
}

func TestGORMNamedArgumentsPreserveRepeatedBindings(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		arguments []any
		wantQuery string
		wantArgs  []any
	}{
		{name: "repeated and out of order", query: `SELECT (@second),(@first),(@second)`, arguments: []any{sql.Named("first", "first"), sql.Named("second", "second")}, wantQuery: `SELECT ($1),($2),($3)`, wantArgs: []any{"second", "first", "second"}},
		{name: "cast boundary", query: `SELECT (@value)::uuid,(@value)::text`, arguments: []any{sql.Named("value", "10000000-0000-4000-8000-000000000001")}, wantQuery: `SELECT ($1)::uuid,($2)::text`, wantArgs: []any{"10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000001"}},
		{name: "SQL literals and comments", query: "SELECT '$2',\"$3\",$$ $4 $$,(@value) /* $5 */ -- $6\n", arguments: []any{sql.Named("value", "value")}, wantQuery: "SELECT '$2',\"$3\",$$ $4 $$,($1) /* $5 */ -- $6\n", wantArgs: []any{"value"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statement := graphNamedStatement(test.query, test.arguments...)
			if statement.SQL.String() != test.wantQuery || !reflect.DeepEqual(statement.Vars, test.wantArgs) {
				t.Fatalf("named statement = (%q, %#v), want (%q, %#v)", statement.SQL.String(), statement.Vars, test.wantQuery, test.wantArgs)
			}
		})
	}
}

func TestGORMNamedArgumentsUseSingleValueCarriers(t *testing.T) {
	statement := graphNamedStatement(`SELECT (@strings)::text[],(@ids)::uuid[],(@numbers)::int[],(@document)::jsonb`,
		sql.Named("strings", pq.Array([]string{"alpha", "beta"})),
		sql.Named("ids", pq.Array([]string{"10000000-0000-4000-8000-000000000001"})),
		sql.Named("numbers", pq.Array([]int{1, 2})),
		sql.Named("document", graphJSONB(`{"key":"value"}`)),
	)
	if statement.SQL.String() != `SELECT ($1)::text[],($2)::uuid[],($3)::int[],($4)::jsonb` || len(statement.Vars) != 4 {
		t.Fatalf("named statement = (%q, %#v)", statement.SQL.String(), statement.Vars)
	}
	for index, argument := range statement.Vars {
		valuer, ok := argument.(driver.Valuer)
		if !ok {
			t.Fatalf("argument %d type = %T, want driver.Valuer", index, argument)
		}
		value, err := valuer.Value()
		if err != nil {
			t.Fatalf("argument %d Value() error = %v", index, err)
		}
		if _, ok := value.(string); !ok {
			t.Fatalf("argument %d Value() type = %T, want string", index, value)
		}
	}
	if _, err := graphJSONB(`{"invalid"`).Value(); err == nil {
		t.Fatal("invalid JSON carrier unexpectedly accepted")
	}
	empty := graphNamedStatement(`SELECT (@values)::uuid[]`, sql.Named("values", pq.Array([]string{})))
	if len(empty.Vars) != 1 {
		t.Fatalf("empty array expanded into %d arguments", len(empty.Vars))
	}
	value, err := empty.Vars[0].(driver.Valuer).Value()
	if err != nil || value != "{}" {
		t.Fatalf("empty array carrier = (%#v, %v)", value, err)
	}
}

func graphNamedStatement(query string, arguments ...any) *gorm.Statement {
	database := &gorm.DB{Config: &gorm.Config{Dialector: gormpostgres.New(gormpostgres.Config{})}}
	database.Statement = &gorm.Statement{DB: database}
	return database.Raw(query, arguments...).Statement
}

func TestClassifyGORMPreservesCancellationCause(t *testing.T) {
	customCause := errors.New("graph caller stopped")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(customCause)
	err := classifyGORM(ctx, errors.New("driver canceled"))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNonRetryableFailure || classified.Code != graphdomain.ErrorCodeQueryCanceled {
		t.Fatalf("err=%v", err)
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(err, customCause) {
		t.Fatalf("cancellation chain does not preserve sentinel and custom cause: %v", err)
	}
}

func TestGORMGraphNoRowsRecognizesDatabaseBoundaries(t *testing.T) {
	for _, err := range []error{sql.ErrNoRows, pgx.ErrNoRows, gorm.ErrRecordNotFound} {
		if !gormGraphNoRows(err) {
			t.Fatalf("gormGraphNoRows(%v) = false", err)
		}
	}
	if gormGraphNoRows(errors.New("other database error")) {
		t.Fatal("gormGraphNoRows() accepted an unrelated error")
	}
}
