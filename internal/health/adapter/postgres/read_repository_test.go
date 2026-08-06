package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	_, err = repository.GetIssue(context.Background(),
		foundation.ID("10000000-0000-4000-8000-000000000001"),
		foundation.ID("10000000-0000-4000-8000-000000000002"),
	)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNotFound || classified.Code != "HEALTH_NOT_FOUND" || classified.Retryable || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing issue error=%v", err)
	}
}

func TestReadRepositoryObservationPageTrimsSentinelBeforeEvidenceBatch(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	issueID := foundation.ID("20000000-0000-4000-8000-000000000001")
	firstID := "30000000-0000-4000-8000-000000000001"
	secondID := "30000000-0000-4000-8000-000000000002"
	sentinelID := "30000000-0000-4000-8000-000000000003"
	observationRows := &scriptedRows{rows: [][]any{
		observationRow(firstID, now),
		observationRow(secondID, now.Add(-time.Minute)),
		observationRow(sentinelID, now.Add(-2*time.Minute)),
	}}
	database := &scriptedReadDB{rows: []pgx.Rows{observationRows, &scriptedRows{}}}
	repository, err := NewReadRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.ListIssueObservations(context.Background(), healthapp.IssueObservationQuery{WorkspaceID: workspaceID, IssueID: issueID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || !result.HasMore || result.Next == nil || result.Next.ID != foundation.ID(secondID) {
		t.Fatalf("result=%+v", result)
	}
	if len(database.queries) != 2 || !strings.Contains(database.queries[0], "ORDER BY observation.observed_at DESC,observation.id ASC LIMIT $3") {
		t.Fatalf("queries=%q", database.queries)
	}
	if !strings.Contains(database.queries[1], "evidence.observation_id=ANY($2::text[]::uuid[])") || !strings.Contains(database.queries[1], "ORDER BY evidence.observation_id,evidence.evidence_no,evidence.id") {
		t.Fatalf("evidence query casts the indexed column: %s", database.queries[1])
	}
	if got := database.args[0][2]; got != 3 {
		t.Fatalf("observation limit argument=%v want=3", got)
	}
	evidenceIDs, ok := database.args[1][1].([]string)
	if !ok || !reflect.DeepEqual(evidenceIDs, []string{firstID, secondID}) {
		t.Fatalf("evidence ids=%#v", database.args[1][1])
	}
}

func TestReadRepositoryDecisionPageUsesMixedDirectionKeyset(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	issueID := foundation.ID("20000000-0000-4000-8000-000000000001")
	position := healthapp.IssueHistoryPosition{At: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC), ID: foundation.ID("30000000-0000-4000-8000-000000000001")}
	database := &scriptedReadDB{rows: []pgx.Rows{&scriptedRows{}}}
	repository, err := NewReadRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.ListIssueDecisions(context.Background(), healthapp.IssueDecisionQuery{WorkspaceID: workspaceID, IssueID: issueID, Limit: 25, After: &position})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || result.HasMore || result.Next != nil {
		t.Fatalf("result=%+v", result)
	}
	query := database.queries[0]
	for _, fragment := range []string{"decision.created_at <= $3", "decision.created_at < $3", "decision.created_at = $3 AND decision.id > $4", "ORDER BY decision.created_at DESC,decision.id ASC LIMIT $5"} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query missing %q: %s", fragment, query)
		}
	}
	if got := database.args[0][4]; got != 26 {
		t.Fatalf("decision limit argument=%v want=26", got)
	}
}

func observationRow(id string, observedAt time.Time) []any {
	return []any{id, int64(1), "40000000-0000-4000-8000-000000000001", "detector/v1", strings.Repeat("a", 64), strings.Repeat("b", 64), []byte(`[]`), string(domain.SeverityHigh), observedAt}
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

type scriptedReadDB struct {
	queries []string
	args    [][]any
	rows    []pgx.Rows
}

func (database *scriptedReadDB) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	database.queries = append(database.queries, query)
	database.args = append(database.args, append([]any(nil), args...))
	index := len(database.queries) - 1
	if index >= len(database.rows) {
		return nil, errors.New("unexpected query")
	}
	return database.rows[index], nil
}

func (*scriptedReadDB) QueryRow(context.Context, string, ...any) pgx.Row { return missingIssueRow{} }

type scriptedRows struct {
	rows   [][]any
	index  int
	closed bool
}

func (rows *scriptedRows) Close()                                  { rows.closed = true }
func (*scriptedRows) Err() error                                   { return nil }
func (*scriptedRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*scriptedRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (rows *scriptedRows) Next() bool                              { return rows.index < len(rows.rows) }
func (rows *scriptedRows) Scan(destinations ...any) error {
	if rows.index >= len(rows.rows) {
		return errors.New("no current row")
	}
	values := rows.rows[rows.index]
	rows.index++
	if len(values) != len(destinations) {
		return errors.New("scan destination count mismatch")
	}
	for index, value := range values {
		target := reflect.ValueOf(destinations[index])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return errors.New("scan destination is invalid")
		}
		target = target.Elem()
		if value == nil {
			target.Set(reflect.Zero(target.Type()))
			continue
		}
		source := reflect.ValueOf(value)
		if !source.Type().AssignableTo(target.Type()) {
			return errors.New("scan value type mismatch")
		}
		target.Set(source)
	}
	return nil
}
func (*scriptedRows) Values() ([]any, error) { return nil, errors.New("values are unavailable") }
func (*scriptedRows) RawValues() [][]byte    { return nil }
func (*scriptedRows) Conn() *pgx.Conn        { return nil }
