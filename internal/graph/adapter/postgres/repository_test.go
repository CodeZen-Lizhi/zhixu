package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

type nonTransactionalDB struct{}

func (nonTransactionalDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (nonTransactionalDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (nonTransactionalDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
