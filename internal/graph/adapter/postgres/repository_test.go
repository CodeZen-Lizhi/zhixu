package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestNewRepositoryFailsClosedWithoutDatabase(t *testing.T) {
	repository, err := NewRepository(nil)
	if repository != nil {
		t.Fatal("nil database unexpectedly created repository")
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != graphdomain.ErrorCodeDependencyUnavailable {
		t.Fatalf("err=%v", err)
	}
}

func TestNewRepositoryRejectsDatabaseWithoutOwnedTransactions(t *testing.T) {
	repository, err := NewRepository(nonTransactionalDB{})
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

func TestRenderGORMPositional(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		arguments []any
		wantQuery string
		wantArgs  []any
		wantError bool
	}{
		{name: "repeated and out of order", query: `SELECT $2,$1,$2`, arguments: []any{"first", "second"}, wantQuery: `SELECT ?,?,?`, wantArgs: []any{"second", "first", "second"}},
		{name: "multi digit", query: `SELECT $10,$1,$2,$3,$4,$5,$6,$7,$8,$9`, arguments: []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, wantQuery: `SELECT ?,?,?,?,?,?,?,?,?,?`, wantArgs: []any{10, 1, 2, 3, 4, 5, 6, 7, 8, 9}},
		{name: "single quotes", query: `SELECT '$2',E'it\'s $3',$1`, arguments: []any{"value"}, wantQuery: `SELECT '$2',E'it\'s $3',?`, wantArgs: []any{"value"}},
		{name: "double quotes", query: `SELECT "$2","a""$3",$1`, arguments: []any{"value"}, wantQuery: `SELECT "$2","a""$3",?`, wantArgs: []any{"value"}},
		{name: "line comment", query: "SELECT $1 -- $2\n", arguments: []any{"value"}, wantQuery: "SELECT ? -- $2\n", wantArgs: []any{"value"}},
		{name: "nested block comment", query: `SELECT /* $2 /* $3 */ still */ $1`, arguments: []any{"value"}, wantQuery: `SELECT /* $2 /* $3 */ still */ ?`, wantArgs: []any{"value"}},
		{name: "dollar quotes", query: `SELECT $$ $2 $$,$tag$ $3 $tag$,$1`, arguments: []any{"value"}, wantQuery: `SELECT $$ $2 $$,$tag$ $3 $tag$,?`, wantArgs: []any{"value"}},
		{name: "zero marker", query: `SELECT $0`, arguments: []any{"value"}, wantError: true},
		{name: "out of range", query: `SELECT $2`, arguments: []any{"value"}, wantError: true},
		{name: "named marker", query: `SELECT $name`, wantError: true},
		{name: "dangling marker", query: `SELECT $`, wantError: true},
		{name: "raw GORM marker", query: `SELECT '?',$1`, arguments: []any{"value"}, wantError: true},
		{name: "marker identifier suffix", query: `SELECT $1suffix`, arguments: []any{"value"}, wantError: true},
		{name: "unused argument", query: `SELECT $1`, arguments: []any{"value", "unused"}, wantError: true},
		{name: "unterminated single quote", query: `SELECT '$1`, wantError: true},
		{name: "unterminated double quote", query: `SELECT "$1`, wantError: true},
		{name: "unterminated block comment", query: `SELECT /* $1`, wantError: true},
		{name: "unterminated dollar quote", query: `SELECT $tag$ $1`, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, arguments, err := renderGORMPositional(test.query, test.arguments)
			if test.wantError {
				if err == nil {
					t.Fatalf("renderGORMPositional() query=%q args=%#v, want error", query, arguments)
				}
				return
			}
			if err != nil {
				t.Fatalf("renderGORMPositional() error = %v", err)
			}
			if query != test.wantQuery || !reflect.DeepEqual(arguments, test.wantArgs) {
				t.Fatalf("renderGORMPositional() = (%q, %#v), want (%q, %#v)", query, arguments, test.wantQuery, test.wantArgs)
			}
		})
	}
}

func TestRenderGORMPositionalUsesSingleValueCarriers(t *testing.T) {
	query, arguments, err := renderGORMPositional(`SELECT $1::text[],$2::uuid[],$3::int[],$4::jsonb`, []any{
		[]string{"alpha", "beta"},
		[]foundation.ID{"10000000-0000-4000-8000-000000000001"},
		[]int{1, 2},
		json.RawMessage(`{"key":"value"}`),
	})
	if err != nil {
		t.Fatalf("renderGORMPositional() error = %v", err)
	}
	if query != `SELECT ?::text[],?::uuid[],?::int[],?::jsonb` || len(arguments) != 4 {
		t.Fatalf("renderGORMPositional() = (%q, %#v)", query, arguments)
	}
	for index, argument := range arguments {
		valuer, ok := argument.(driver.Valuer)
		if !ok {
			t.Fatalf("argument %d type = %T, want driver.Valuer", index, argument)
		}
		value, valueErr := valuer.Value()
		if valueErr != nil {
			t.Fatalf("argument %d Value() error = %v", index, valueErr)
		}
		if _, ok := value.(string); !ok {
			t.Fatalf("argument %d Value() type = %T, want string", index, value)
		}
	}
	if _, _, err := renderGORMPositional(`SELECT $1`, []any{[]byte(`{"key":"value"}`)}); err == nil {
		t.Fatal("raw byte slice unexpectedly accepted")
	}
	var absentJSON *graphJSONB
	_, nullableArguments, err := renderGORMPositional(`SELECT $1::jsonb`, []any{absentJSON})
	if err != nil || len(nullableArguments) != 1 || nullableArguments[0] != nil {
		t.Fatalf("typed nil carrier = (%#v, %v), want one nil binding", nullableArguments, err)
	}
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

type nonTransactionalDB struct{}

func (nonTransactionalDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (nonTransactionalDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (nonTransactionalDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
