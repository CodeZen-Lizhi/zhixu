package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5"
)

func TestNewReadRepositoryRejectsNilDatabase(t *testing.T) {
	if _, err := NewReadRepository(nil); err == nil {
		t.Fatal("expected nil database rejection")
	}
}

func TestStringSlicePreservesEnumValues(t *testing.T) {
	got := stringSlice([]domain.IssueStatus{domain.IssueStatusOpen, domain.IssueStatusResolved})
	if len(got) != 2 || got[0] != "OPEN" || got[1] != "RESOLVED" {
		t.Fatalf("got=%v", got)
	}
}

func TestReadRepositoryClassifiesMissingIssueWithoutLeakingWorkspaceExistence(t *testing.T) {
	repository, err := NewReadRepository(missingIssueReadDB{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.GetIssueDetail(context.Background(),
		foundation.ID("10000000-0000-4000-8000-000000000001"),
		foundation.ID("10000000-0000-4000-8000-000000000002"),
	)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNotFound || classified.Code != "HEALTH_NOT_FOUND" || classified.Retryable || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing issue error=%v", err)
	}
}

type missingIssueReadDB struct{}

func (missingIssueReadDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (missingIssueReadDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return missingIssueRow{}
}

type missingIssueRow struct{}

func (missingIssueRow) Scan(...any) error { return pgx.ErrNoRows }
