package workspacepostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
)

func TestBuildSourceVersionListQueryExcludesTombstonedSources(t *testing.T) {
	query, _ := buildSourceVersionListQuery(domain.SourceVersionListQuery{
		WorkspaceID: foundation.ID("00000000-0000-4000-8000-000000000001"),
		Limit:       1,
	})
	if !strings.Contains(query, "s.removed_at IS NULL") {
		t.Fatalf("source version list query includes tombstoned sources:\n%s", query)
	}
}

func TestManagedRepositoryRejectsRootListingBeforeDatabaseAccess(t *testing.T) {
	repository := &Repository{managed: true}

	_, err := repository.ListWorkspaceRoots(context.Background())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorPermissionDenied || classified.Code != rootgrant.ErrorCodeRootNotGranted {
		t.Fatalf("ListWorkspaceRoots() error=%v", err)
	}
}

func TestGORMClassifiersRejectCompletedTransactions(t *testing.T) {
	requireRepositoryError(t,
		classifyGORMWorkspace(context.Background(), sql.ErrTxDone, "SOURCE_VERSION_TRANSACTION_FAILED"),
		foundation.ErrorDependencyUnavailable,
		"WORKSPACE_DATABASE_UNAVAILABLE",
	)
	requireRepositoryError(t,
		classifyGORMControl(context.Background(), sql.ErrTxDone, domain.ErrorCodeControlDatabaseUnavailable),
		foundation.ErrorDependencyUnavailable,
		domain.ErrorCodeControlDatabaseUnavailable,
	)
}

func TestRepositoryGetActiveWorkspaceRequiresUniqueActiveRow(t *testing.T) {
	t.Run("unique", func(t *testing.T) {
		database := &activeWorkspaceTestDB{row: activeWorkspaceTestRow{values: activeWorkspaceValues(1)}}
		repository, err := NewRepository(database)
		if err != nil {
			t.Fatal(err)
		}
		workspace, err := repository.GetActiveWorkspace(context.Background())
		if err != nil {
			t.Fatalf("GetActiveWorkspace() error=%v", err)
		}
		if workspace.ID != foundation.ID("10000000-0000-4000-8000-000000000001") || workspace.Status != domain.WorkspaceStatusActive || workspace.Availability != domain.WorkspaceAvailabilityAvailable {
			t.Fatalf("workspace=%#v", workspace)
		}
		if !strings.Contains(database.query, "count(*) OVER ()") || !strings.Contains(database.query, "WHERE status = $1") || !strings.Contains(database.query, "LIMIT 2") ||
			!reflect.DeepEqual(database.arguments, []any{string(domain.WorkspaceStatusActive)}) {
			t.Fatalf("query=%q arguments=%#v", database.query, database.arguments)
		}
	})

	t.Run("none", func(t *testing.T) {
		database := &activeWorkspaceTestDB{row: activeWorkspaceTestRow{err: pgx.ErrNoRows}}
		repository, err := NewRepository(database)
		if err != nil {
			t.Fatal(err)
		}
		_, err = repository.GetActiveWorkspace(context.Background())
		requireRepositoryError(t, err, foundation.ErrorNotFound, domain.ErrorCodeActiveWorkspaceNotFound)
	})

	t.Run("multiple", func(t *testing.T) {
		database := &activeWorkspaceTestDB{row: activeWorkspaceTestRow{values: activeWorkspaceValues(2)}}
		resolver := &activeWorkspaceGrantResolver{}
		repository, err := NewRepository(database, WithRootGrantResolver(resolver, true))
		if err != nil {
			t.Fatal(err)
		}
		_, err = repository.GetActiveWorkspace(context.Background())
		requireRepositoryError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeActiveWorkspaceNotUnique)
		if resolver.calls != 0 {
			t.Fatalf("resolver calls=%d, want 0", resolver.calls)
		}
	})
}

func TestRepositoryGetActiveWorkspaceUsesManagedGrantResolver(t *testing.T) {
	grantErr := foundation.NewError(foundation.ErrorPermissionDenied, rootgrant.ErrorCodeRootNotGranted, false, errors.New("grant mismatch"))
	resolver := &activeWorkspaceGrantResolver{err: grantErr}
	database := &activeWorkspaceTestDB{row: activeWorkspaceTestRow{values: activeWorkspaceValues(1)}}
	repository, err := NewRepository(database, WithRootGrantResolver(resolver, true))
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.GetActiveWorkspace(context.Background())
	requireRepositoryError(t, err, foundation.ErrorPermissionDenied, rootgrant.ErrorCodeRootNotGranted)
	if resolver.calls != 1 || resolver.workspaceID != foundation.ID("10000000-0000-4000-8000-000000000001") {
		t.Fatalf("resolver calls=%d workspace_id=%q", resolver.calls, resolver.workspaceID)
	}
}

func TestRepositoryGetActiveWorkspaceFailsClosedWithoutManagedGrantResolver(t *testing.T) {
	database := &activeWorkspaceTestDB{row: activeWorkspaceTestRow{values: activeWorkspaceValues(1)}}
	repository := &Repository{db: database, managed: true}
	_, err := repository.GetActiveWorkspace(context.Background())
	requireRepositoryError(t, err, foundation.ErrorDependencyUnavailable, rootgrant.ErrorCodeGrantStale)
}

func requireRepositoryError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error=%v, want kind=%q code=%q", err, kind, code)
	}
}

type activeWorkspaceTestDB struct {
	row       pgx.Row
	query     string
	arguments []any
}

func (database *activeWorkspaceTestDB) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	database.query = query
	database.arguments = append([]any(nil), arguments...)
	return database.row
}

func (*activeWorkspaceTestDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query call")
}

func (*activeWorkspaceTestDB) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unexpected Begin call")
}

type activeWorkspaceTestRow struct {
	values []any
	err    error
}

func (row activeWorkspaceTestRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != len(row.values) {
		return fmt.Errorf("scan destinations=%d values=%d", len(destinations), len(row.values))
	}
	for index, value := range row.values {
		destination := reflect.ValueOf(destinations[index])
		if destination.Kind() != reflect.Pointer || destination.IsNil() {
			return fmt.Errorf("destination %d is not a pointer", index)
		}
		source := reflect.ValueOf(value)
		if !source.IsValid() || !source.Type().AssignableTo(destination.Elem().Type()) {
			return fmt.Errorf("value %d type %T cannot scan into %T", index, value, destinations[index])
		}
		destination.Elem().Set(source)
	}
	return nil
}

func activeWorkspaceValues(count int64) []any {
	now := time.Date(2026, time.August, 8, 8, 0, 0, 0, time.UTC)
	return []any{
		"10000000-0000-4000-8000-000000000001", "Active", "/workspace",
		sql.NullString{String: strings.Repeat("f", 64), Valid: true}, int64(1),
		"/workspace", "main", strings.Repeat("a", 40), false, now,
		string(domain.WorkspaceStatusActive), string(domain.WorkspaceAvailabilityAvailable),
		sql.NullString{}, sql.NullTime{Time: now, Valid: true}, sql.NullTime{}, sql.NullTime{},
		int64(1), now, now, count,
	}
}

type activeWorkspaceGrantResolver struct {
	err         error
	calls       int
	workspaceID foundation.ID
}

func (resolver *activeWorkspaceGrantResolver) Resolve(_ context.Context, workspaceID foundation.ID) (*rootgrant.Capability, error) {
	resolver.calls++
	resolver.workspaceID = workspaceID
	return nil, resolver.err
}

var _ DB = (*activeWorkspaceTestDB)(nil)
var _ RootGrantResolver = (*activeWorkspaceGrantResolver)(nil)
