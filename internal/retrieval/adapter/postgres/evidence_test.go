package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

func TestLoadSourceVersionReferenceUsesBoundParameterizedJoin(t *testing.T) {
	database := &evidenceTestDB{row: evidenceTestRow{values: evidenceSourceRowValues("docs/evidence.md")}}
	repository, err := NewSearchRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := repository.LoadSourceVersionReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if reference.WorkspaceID != evidenceTestWorkspaceID || reference.SourceVersionID != evidenceTestSourceVersionID || reference.RelativePath != "docs/evidence.md" {
		t.Fatalf("reference = %#v", reference)
	}
	for _, predicate := range []string{
		"JOIN core.source AS s",
		"s.workspace_id=$1",
		"JOIN core.content_artifact AS ca",
		"ca.id=sv.content_artifact_id",
		"ca.workspace_id=s.workspace_id",
		"ca.content_hash=sv.content_hash",
		"ca.byte_size=sv.byte_size",
		"sv.original_content_location",
		"WHERE sv.id=$2",
	} {
		if !strings.Contains(database.query, predicate) {
			t.Fatalf("source version query missing %q:\n%s", predicate, database.query)
		}
	}
	if strings.Contains(strings.ToUpper(database.query), "SELECT *") {
		t.Fatal("source version query uses SELECT *")
	}
	if !reflect.DeepEqual(database.args, []any{string(evidenceTestWorkspaceID), string(evidenceTestSourceVersionID)}) {
		t.Fatalf("query args = %#v", database.args)
	}
}

func TestLoadSourceSpanReferenceUsesFullBindingJoin(t *testing.T) {
	database := &evidenceTestDB{row: evidenceTestRow{values: evidenceSpanRowValues("docs/evidence.md")}}
	repository, err := NewSearchRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := repository.LoadSourceSpanReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID, evidenceTestSpanID)
	if err != nil {
		t.Fatal(err)
	}
	if reference.SourceVersion.WorkspaceID != evidenceTestWorkspaceID || reference.SourceVersion.SourceVersionID != evidenceTestSourceVersionID ||
		reference.ParseProjectionID != evidenceTestProjectionID || reference.Span.ID != evidenceTestSpanID {
		t.Fatalf("reference = %#v", reference)
	}
	for _, predicate := range []string{
		"JOIN ingestion.source_version_projection AS svp",
		"svp.source_version_id=sv.id",
		"svp.workspace_id=s.workspace_id",
		"JOIN ingestion.parse_projection AS pp",
		"pp.id=svp.parse_projection_id",
		"pp.content_artifact_id=ca.id",
		"JOIN ingestion.source_span AS sp",
		"sp.id=$3",
		"sp.workspace_id=s.workspace_id",
		"sp.content_artifact_id=ca.id",
		"sp.parse_projection_id=pp.id",
		"sp.parser_version=pp.parser_version",
		"sp.schema_version=pp.schema_version",
		"sv.original_content_location",
		"WHERE sv.id=$2",
	} {
		if !strings.Contains(database.query, predicate) {
			t.Fatalf("source span query missing %q:\n%s", predicate, database.query)
		}
	}
	if strings.Contains(strings.ToUpper(database.query), "SELECT *") {
		t.Fatal("source span query uses SELECT *")
	}
	if !reflect.DeepEqual(database.args, []any{string(evidenceTestWorkspaceID), string(evidenceTestSourceVersionID), string(evidenceTestSpanID)}) {
		t.Fatalf("query args = %#v", database.args)
	}
}

func TestEvidenceReferenceLoadsUnifyNotFoundAndFailClosed(t *testing.T) {
	tests := []struct {
		name string
		row  evidenceTestRow
		load func(*SearchRepository) error
		kind foundation.ErrorKind
		code string
	}{
		{
			name: "source version missing", row: evidenceTestRow{err: pgx.ErrNoRows},
			load: func(repository *SearchRepository) error {
				_, err := repository.LoadSourceVersionReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID)
				return err
			},
			kind: foundation.ErrorNotFound, code: evidenceReferenceNotFoundCode,
		},
		{
			name: "span binding missing", row: evidenceTestRow{err: pgx.ErrNoRows},
			load: func(repository *SearchRepository) error {
				_, err := repository.LoadSourceSpanReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID, evidenceTestSpanID)
				return err
			},
			kind: foundation.ErrorNotFound, code: evidenceReferenceNotFoundCode,
		},
		{
			name: "damaged source version metadata", row: evidenceTestRow{values: evidenceSourceRowValuesWithVersionPath("docs/evidence.md", "docs/different.md")},
			load: func(repository *SearchRepository) error {
				_, err := repository.LoadSourceVersionReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID)
				return err
			},
			kind: foundation.ErrorConsistencyViolation, code: domain.ErrorCodeEvidenceReferenceInvalid,
		},
		{
			name: "damaged span metadata", row: evidenceTestRow{values: evidenceSpanRowValuesWithVersionPath("docs/evidence.md", "docs/different.md")},
			load: func(repository *SearchRepository) error {
				_, err := repository.LoadSourceSpanReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID, evidenceTestSpanID)
				return err
			},
			kind: foundation.ErrorConsistencyViolation, code: domain.ErrorCodeEvidenceReferenceInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, err := NewSearchRepository(&evidenceTestDB{row: test.row})
			if err != nil {
				t.Fatal(err)
			}
			requirePostgresEvidenceError(t, test.load(repository), test.kind, test.code)
		})
	}
}

func TestEvidenceReferenceLoadsRejectInvalidIDsBeforeQuery(t *testing.T) {
	database := &evidenceTestDB{row: evidenceTestRow{err: errors.New("must not query")}}
	repository, err := NewSearchRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.LoadSourceVersionReference(context.Background(), "invalid", evidenceTestSourceVersionID)
	requirePostgresEvidenceError(t, err, foundation.ErrorInvalidInput, domain.ErrorCodeEvidenceReferenceInvalid)
	if database.calls != 0 {
		t.Fatalf("query calls = %d", database.calls)
	}
}

const (
	evidenceTestWorkspaceID     foundation.ID = "96000000-0000-4000-8000-000000000001"
	evidenceTestSourceID        foundation.ID = "96000000-0000-4000-8000-000000000002"
	evidenceTestSourceVersionID foundation.ID = "96000000-0000-4000-8000-000000000003"
	evidenceTestArtifactID      foundation.ID = "96000000-0000-4000-8000-000000000004"
	evidenceTestProjectionID    foundation.ID = "96000000-0000-4000-8000-000000000005"
	evidenceTestSpanID          foundation.ID = "96000000-0000-4000-8000-000000000006"
)

func evidenceSourceRowValues(relativePath string) []any {
	return evidenceSourceRowValuesWithVersionPath(relativePath, relativePath)
}

func evidenceSourceRowValuesWithVersionPath(relativePath, versionRelativePath string) []any {
	return []any{
		string(evidenceTestWorkspaceID),
		string(evidenceTestSourceID),
		string(evidenceTestSourceVersionID),
		string(evidenceTestArtifactID),
		"file",
		"Evidence",
		relativePath,
		versionRelativePath,
		strings.Repeat("a", 64),
		int64(7),
		"text/plain",
		"passed",
		time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC),
	}
}

func evidenceSpanRowValues(relativePath string) []any {
	return evidenceSpanRowValuesWithVersionPath(relativePath, relativePath)
}

func evidenceSpanRowValuesWithVersionPath(relativePath, versionRelativePath string) []any {
	values := evidenceSourceRowValuesWithVersionPath(relativePath, versionRelativePath)
	return append(values,
		string(evidenceTestProjectionID),
		string(evidenceTestSpanID),
		int32(1),
		int32(1),
		int64(0),
		int64(7),
		"paragraph",
		[]byte(`{"kind":"paragraph"}`),
		strings.Repeat("b", 64),
		"parser-v1",
		"schema-v1",
	)
}

type evidenceTestDB struct {
	row   pgx.Row
	query string
	args  []any
	calls int
}

func (database *evidenceTestDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	database.calls++
	database.query = query
	database.args = append([]any(nil), args...)
	return database.row
}

func (*evidenceTestDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not implemented")
}

func (*evidenceTestDB) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("not implemented")
}

type evidenceTestRow struct {
	values []any
	err    error
}

func (row evidenceTestRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != len(row.values) {
		return errors.New("unexpected scan destination count")
	}
	for index, destination := range destinations {
		value := reflect.ValueOf(destination)
		if value.Kind() != reflect.Pointer || value.IsNil() {
			return errors.New("scan destination is not a pointer")
		}
		source := reflect.ValueOf(row.values[index])
		if !source.Type().AssignableTo(value.Elem().Type()) {
			return errors.New("scan value type does not match destination")
		}
		value.Elem().Set(source)
	}
	return nil
}

func requirePostgresEvidenceError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error = %#v, want kind=%q code=%q", err, kind, code)
	}
}
