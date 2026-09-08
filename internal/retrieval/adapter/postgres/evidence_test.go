package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLoadSourceVersionReferenceUsesBoundParameterizedJoin(t *testing.T) {
	database := &evidenceTestDB{row: evidenceTestRow{values: evidenceSourceRowValues("docs/evidence.md")}}
	repository, err := newEvidenceSearchRepository(t, database)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := repository.LoadSourceVersionReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if reference.WorkspaceID != evidenceTestWorkspaceID || reference.SourceVersionID != evidenceTestSourceVersionID || reference.RelativePath != "docs/evidence.md" ||
		reference.IngestionStatus != "parsed" || reference.WorkflowStatus != "running" || reference.IndexStatus != "included" {
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
		"LEFT JOIN LATERAL",
		"FROM ingestion.attempt",
		"COALESCE(attempt.security_status,sv.security_status)",
		"LEFT JOIN workflow.run AS run",
		"active_index.status='active'",
		"manifest.selection_status='excluded' OR manifest.source_version_id=sv.id",
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
	repository, err := newEvidenceSearchRepository(t, database)
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
		"sp.id=$2",
		"sp.workspace_id=s.workspace_id",
		"sp.content_artifact_id=ca.id",
		"sp.parse_projection_id=pp.id",
		"sp.parser_version=pp.parser_version",
		"sp.schema_version=pp.schema_version",
		"sv.original_content_location",
		"WHERE sv.id=$3",
	} {
		if !strings.Contains(database.query, predicate) {
			t.Fatalf("source span query missing %q:\n%s", predicate, database.query)
		}
	}
	if strings.Contains(strings.ToUpper(database.query), "SELECT *") {
		t.Fatal("source span query uses SELECT *")
	}
	if !reflect.DeepEqual(database.args, []any{string(evidenceTestWorkspaceID), string(evidenceTestSpanID), string(evidenceTestSourceVersionID)}) {
		t.Fatalf("query args = %#v", database.args)
	}
}

func TestLoadCitationSourceSpanReferenceUsesFullFrozenTupleInOneQuery(t *testing.T) {
	database := &evidenceTestDB{row: evidenceTestRow{values: []any{citationReferenceJSON(t, "docs/evidence.md")}}}
	repository, err := newEvidenceSearchRepository(t, database)
	if err != nil {
		t.Fatal(err)
	}
	query := domain.CitationReferenceQuery{
		WorkspaceID: evidenceTestWorkspaceID, IndexVersionID: evidenceTestIndexID, ChunkID: evidenceTestChunkID,
		SourceVersionID: evidenceTestSourceVersionID, SourceSpanID: evidenceTestSpanID,
	}
	reference, err := repository.LoadCitationSourceSpanReference(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if reference.SourceVersion.SourceVersionID != evidenceTestSourceVersionID || reference.Span.ID != evidenceTestSpanID {
		t.Fatalf("reference=%+v", reference)
	}
	for _, predicate := range []string{
		"FROM unnest($1::uuid[],$2::uuid[],$3::uuid[],$4::uuid[],$5::uuid[])",
		"JOIN retrieval.index_version AS idx", "manifest.index_version_id=idx.id", "manifest.chunk_id=requested.chunk_id",
		"chunk.id=manifest.chunk_id", "source_manifest.source_version_id=requested.source_version_id",
		"source_manifest.parse_projection_id=chunk.parse_projection_id", "source_manifest.selection_status='included'",
		"sp.id=chunk.source_span_id", "sp.id=requested.source_span_id", "idx.id=requested.index_version_id", "idx.workspace_id=requested.workspace_id",
	} {
		if !strings.Contains(database.query, predicate) {
			t.Fatalf("citation query missing %q:\n%s", predicate, database.query)
		}
	}
	wantArgs := []any{
		evidenceArrayArgument(evidenceTestWorkspaceID), evidenceArrayArgument(evidenceTestIndexID), evidenceArrayArgument(evidenceTestChunkID),
		evidenceArrayArgument(evidenceTestSourceVersionID), evidenceArrayArgument(evidenceTestSpanID),
	}
	if !reflect.DeepEqual(database.args, wantArgs) {
		t.Fatalf("args=%#v", database.args)
	}
}

func TestResolveProvenanceCitationReferencesUsesActiveIndexInOneQuery(t *testing.T) {
	encoded, err := json.Marshal([]map[string]string{{
		"WorkspaceID": string(evidenceTestWorkspaceID), "IndexVersionID": string(evidenceTestIndexID),
		"ChunkID": string(evidenceTestChunkID), "SourceVersionID": string(evidenceTestSourceVersionID),
		"SourceSpanID": string(evidenceTestSpanID),
	}})
	if err != nil {
		t.Fatal(err)
	}
	database := &evidenceTestDB{row: evidenceTestRow{values: []any{encoded}}}
	repository, err := newEvidenceSearchRepository(t, database)
	if err != nil {
		t.Fatal(err)
	}
	query := domain.ProvenanceReferenceQuery{WorkspaceID: evidenceTestWorkspaceID, SourceVersionID: evidenceTestSourceVersionID, SourceSpanID: evidenceTestSpanID}
	result, err := repository.ResolveProvenanceCitationReferences(context.Background(), []domain.ProvenanceReferenceQuery{query})
	if err != nil || len(result) != 1 || result[0].IndexVersionID != evidenceTestIndexID || result[0].ChunkID != evidenceTestChunkID {
		t.Fatalf("provenance result=%#v err=%v", result, err)
	}
	for _, predicate := range []string{"idx.status='active'", "source_manifest.selection_status='included'", "chunk.source_span_id=requested.source_span_id", "manifest.chunk_id=chunk.id", "row_number() OVER"} {
		if !strings.Contains(database.query, predicate) {
			t.Fatalf("provenance query missing %q:\n%s", predicate, database.query)
		}
	}
	wantArgs := []any{evidenceArrayArgument(evidenceTestWorkspaceID), evidenceArrayArgument(evidenceTestSourceVersionID), evidenceArrayArgument(evidenceTestSpanID)}
	if !reflect.DeepEqual(database.args, wantArgs) {
		t.Fatalf("provenance args=%#v", database.args)
	}
}

func citationReferenceJSON(t *testing.T, relativePath string) []byte {
	t.Helper()
	encoded, err := json.Marshal([]map[string]any{{
		"WorkspaceID": string(evidenceTestWorkspaceID), "IndexVersionID": string(evidenceTestIndexID), "ChunkID": string(evidenceTestChunkID),
		"SourceID": string(evidenceTestSourceID), "SourceVersionID": string(evidenceTestSourceVersionID), "ArtifactID": string(evidenceTestArtifactID),
		"ProjectionID": string(evidenceTestProjectionID), "SpanID": string(evidenceTestSpanID), "SourceType": "file", "LogicalName": "Evidence",
		"RelativePath": relativePath, "VersionRelativePath": relativePath, "ContentHash": strings.Repeat("a", 64), "ByteSize": int64(7),
		"MediaType": "text/plain", "SecurityStatus": "passed", "CapturedAt": time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC),
		"StartLine": int32(1), "EndLine": int32(1), "StartByte": int64(0), "EndByte": int64(7), "SpanType": "paragraph",
		"Selector": json.RawMessage(`{"kind":"paragraph"}`), "ExcerptHash": strings.Repeat("b", 64), "ParserVersion": "parser-v1", "SchemaVersion": "schema-v1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestEvidenceReferenceLoadsUnifyNotFoundAndFailClosed(t *testing.T) {
	tests := []struct {
		name string
		row  evidenceTestRow
		load func(*GORMSearchRepository) error
		kind foundation.ErrorKind
		code string
	}{
		{
			name: "source version missing", row: evidenceTestRow{err: sql.ErrNoRows},
			load: func(repository *GORMSearchRepository) error {
				_, err := repository.LoadSourceVersionReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID)
				return err
			},
			kind: foundation.ErrorNotFound, code: evidenceReferenceNotFoundCode,
		},
		{
			name: "span binding missing", row: evidenceTestRow{err: sql.ErrNoRows},
			load: func(repository *GORMSearchRepository) error {
				_, err := repository.LoadSourceSpanReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID, evidenceTestSpanID)
				return err
			},
			kind: foundation.ErrorNotFound, code: evidenceReferenceNotFoundCode,
		},
		{
			name: "damaged source version metadata", row: evidenceTestRow{values: evidenceSourceRowValuesWithVersionPath("docs/evidence.md", "docs/different.md")},
			load: func(repository *GORMSearchRepository) error {
				_, err := repository.LoadSourceVersionReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID)
				return err
			},
			kind: foundation.ErrorConsistencyViolation, code: domain.ErrorCodeEvidenceReferenceInvalid,
		},
		{
			name: "damaged span metadata", row: evidenceTestRow{values: evidenceSpanRowValuesWithVersionPath("docs/evidence.md", "docs/different.md")},
			load: func(repository *GORMSearchRepository) error {
				_, err := repository.LoadSourceSpanReference(context.Background(), evidenceTestWorkspaceID, evidenceTestSourceVersionID, evidenceTestSpanID)
				return err
			},
			kind: foundation.ErrorConsistencyViolation, code: domain.ErrorCodeEvidenceReferenceInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, err := newEvidenceSearchRepository(t, &evidenceTestDB{row: test.row})
			if err != nil {
				t.Fatal(err)
			}
			requirePostgresEvidenceError(t, test.load(repository), test.kind, test.code)
		})
	}
}

func TestEvidenceReferenceLoadsRejectInvalidIDsBeforeQuery(t *testing.T) {
	database := &evidenceTestDB{row: evidenceTestRow{err: errors.New("must not query")}}
	repository, err := newEvidenceSearchRepository(t, database)
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
	evidenceTestIndexID         foundation.ID = "96000000-0000-4000-8000-000000000007"
	evidenceTestChunkID         foundation.ID = "96000000-0000-4000-8000-000000000008"
)

func evidenceSourceRowValues(relativePath string) []any {
	return evidenceSourceRowValuesWithVersionPath(relativePath, relativePath)
}

func evidenceSourceRowValuesWithVersionPath(relativePath, versionRelativePath string) []any {
	values := evidenceImmutableSourceRowValues(relativePath, versionRelativePath)
	capturedAt := values[len(values)-1]
	return append(values[:len(values)-1], "parsed", "running", "included", capturedAt)
}

func evidenceImmutableSourceRowValues(relativePath, versionRelativePath string) []any {
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
	values := evidenceImmutableSourceRowValues(relativePath, versionRelativePath)
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
		domain.EvidenceKind("raw_bytes"),
		"",
		"parser-v1",
		"schema-v1",
	)
}

// evidenceTestDB 通过标准 database/sql driver 保留 SQL 和绑定参数断言。
type evidenceTestDB struct {
	row   evidenceTestRow
	query string
	args  []any
	calls int
}

func newEvidenceSearchRepository(t *testing.T, database *evidenceTestDB) (*GORMSearchRepository, error) {
	t.Helper()
	sqlDB := sql.OpenDB(database)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close evidence SQL fixture: %v", err)
		}
	})
	root, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	return &GORMSearchRepository{database: root, unitOfWork: evidenceUnusedUnitOfWork{}}, nil
}

type evidenceUnusedUnitOfWork struct{}

func (evidenceUnusedUnitOfWork) Within(context.Context, foundation.TransactionOptions, foundation.TransactionFunc) error {
	return errors.New("evidence reference reads must not open a transaction")
}

func (database *evidenceTestDB) Connect(context.Context) (driver.Conn, error) {
	return database, nil
}

func (database *evidenceTestDB) Driver() driver.Driver            { return database }
func (database *evidenceTestDB) Open(string) (driver.Conn, error) { return database, nil }
func (*evidenceTestDB) Close() error                              { return nil }
func (*evidenceTestDB) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (*evidenceTestDB) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }

func (database *evidenceTestDB) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	database.calls++
	database.query = query
	database.args = make([]any, len(arguments))
	for index, argument := range arguments {
		database.args[index] = argument.Value
	}
	if errors.Is(database.row.err, sql.ErrNoRows) {
		return &evidenceDriverRows{read: true}, nil
	}
	if database.row.err != nil {
		return nil, database.row.err
	}
	values := make([]driver.Value, len(database.row.values))
	for index, value := range database.row.values {
		converted, err := driver.DefaultParameterConverter.ConvertValue(value)
		if err != nil {
			return nil, err
		}
		values[index] = converted
	}
	return &evidenceDriverRows{values: values}, nil
}

type evidenceTestRow struct {
	values []any
	err    error
}

type evidenceDriverRows struct {
	values []driver.Value
	read   bool
}

func (rows *evidenceDriverRows) Columns() []string {
	columns := make([]string, len(rows.values))
	for index := range columns {
		columns[index] = fmt.Sprintf("column_%d", index)
	}
	return columns
}

func (*evidenceDriverRows) Close() error { return nil }

func (rows *evidenceDriverRows) Next(destinations []driver.Value) error {
	if rows.read {
		return io.EOF
	}
	rows.read = true
	copy(destinations, rows.values)
	return nil
}

func evidenceArrayArgument(id foundation.ID) string {
	return "{\"" + string(id) + "\"}"
}

func requirePostgresEvidenceError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error = %#v, want kind=%q code=%q", err, kind, code)
	}
}
