package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ariga.io/atlas/sql/migrate"
)

// atlasRevisionSchema and atlasRevisionTable reproduce the Atlas CLI revision
// storage layout so CLI tooling (migrate status/diff) stays interoperable with
// databases managed by the in-process runner.
const (
	atlasRevisionSchema = "atlas_schema_revisions"
	atlasRevisionTable  = "atlas_schema_revisions"
)

// atlasDBTX is satisfied by *sql.DB and *sql.Tx so revision writes can join the
// per-file transaction owned by the Atlas execution engine.
type atlasDBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// atlasRevisionStore is a PostgreSQL-backed migrate.RevisionReadWriter.
type atlasRevisionStore struct {
	db atlasDBTX
}

// newAtlasRevisionStore binds revision storage to db, which may already be a
// transaction when the caller executes a migration file atomically.
func newAtlasRevisionStore(db atlasDBTX) *atlasRevisionStore {
	return &atlasRevisionStore{db: db}
}

// atlasRevisionTableDDL matches the ent-managed CLI table definition.
const atlasRevisionTableDDL = `CREATE TABLE IF NOT EXISTS atlas_schema_revisions.atlas_schema_revisions (
    version character varying NOT NULL PRIMARY KEY,
    description character varying NOT NULL,
    type bigint NOT NULL DEFAULT 2,
    applied bigint NOT NULL DEFAULT 0,
    total bigint NOT NULL DEFAULT 0,
    executed_at timestamptz NOT NULL,
    execution_time bigint NOT NULL,
    error text,
    error_stmt text,
    hash character varying NOT NULL DEFAULT '',
    partial_hashes jsonb,
    operator_version character varying NOT NULL DEFAULT ''
)`

// ensureAtlasRevisionStorage creates the revision schema and table when absent.
func ensureAtlasRevisionStorage(ctx context.Context, db atlasDBTX) error {
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+atlasRevisionSchema); err != nil {
		return fmt.Errorf("create atlas revision schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, atlasRevisionTableDDL); err != nil {
		return fmt.Errorf("create atlas revision table: %w", err)
	}
	// Early Atlas worktree builds created the same table without these defaults.
	// Normalize that compatible legacy shape so the declarative baseline and CLI
	// see one stable revision schema on every runner invocation.
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+atlasRevisionSchema+"."+atlasRevisionTable+
		" ALTER COLUMN hash SET DEFAULT '', ALTER COLUMN operator_version SET DEFAULT ''"); err != nil {
		return fmt.Errorf("normalize atlas revision table defaults: %w", err)
	}
	return nil
}

// Ident implements migrate.RevisionReadWriter.
func (s *atlasRevisionStore) Ident() *migrate.TableIdent {
	return &migrate.TableIdent{Schema: atlasRevisionSchema, Name: atlasRevisionTable}
}

const atlasRevisionColumns = "version, description, type, applied, total, executed_at, execution_time, error, error_stmt, hash, partial_hashes, operator_version"

func scanAtlasRevision(row interface{ Scan(...any) error }) (*migrate.Revision, error) {
	var (
		rev           migrate.Revision
		revType       int64
		execTime      int64
		errText       sql.NullString
		errStmt       sql.NullString
		partialHashes []byte
	)
	if err := row.Scan(&rev.Version, &rev.Description, &revType, &rev.Applied, &rev.Total,
		&rev.ExecutedAt, &execTime, &errText, &errStmt, &rev.Hash, &partialHashes, &rev.OperatorVersion); err != nil {
		return nil, err
	}
	rev.Type = migrate.RevisionType(revType)
	rev.ExecutionTime = time.Duration(execTime)
	if errText.Valid {
		rev.Error = errText.String
	}
	if errStmt.Valid {
		rev.ErrorStmt = errStmt.String
	}
	if len(partialHashes) > 0 {
		if err := json.Unmarshal(partialHashes, &rev.PartialHashes); err != nil {
			return nil, fmt.Errorf("decode partial hashes for revision %s: %w", rev.Version, err)
		}
	}
	return &rev, nil
}

// ReadRevisions implements migrate.RevisionReadWriter ordered by version.
func (s *atlasRevisionStore) ReadRevisions(ctx context.Context) ([]*migrate.Revision, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+atlasRevisionColumns+" FROM "+atlasRevisionSchema+"."+atlasRevisionTable+" ORDER BY version ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revisions []*migrate.Revision
	for rows.Next() {
		rev, err := scanAtlasRevision(rows)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, rev)
	}
	return revisions, rows.Err()
}

// ReadRevision implements migrate.RevisionReadWriter.
func (s *atlasRevisionStore) ReadRevision(ctx context.Context, version string) (*migrate.Revision, error) {
	rev, err := scanAtlasRevision(s.db.QueryRowContext(ctx,
		"SELECT "+atlasRevisionColumns+" FROM "+atlasRevisionSchema+"."+atlasRevisionTable+" WHERE version = $1", version))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("revision %s: %w", version, migrate.ErrRevisionNotExist)
	}
	if err != nil {
		return nil, err
	}
	return rev, nil
}

// WriteRevision implements migrate.RevisionReadWriter as an upsert.
func (s *atlasRevisionStore) WriteRevision(ctx context.Context, rev *migrate.Revision) error {
	if rev == nil {
		return errors.New("atlas revision is nil")
	}
	var partial any
	if rev.PartialHashes != nil {
		encoded, err := json.Marshal(rev.PartialHashes)
		if err != nil {
			return fmt.Errorf("encode partial hashes for revision %s: %w", rev.Version, err)
		}
		partial = encoded
	}
	var errText, errStmt any
	if rev.Error != "" {
		errText = rev.Error
	}
	if rev.ErrorStmt != "" {
		errStmt = rev.ErrorStmt
	}
	executedAt := rev.ExecutedAt
	if executedAt.IsZero() {
		executedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO atlas_schema_revisions.atlas_schema_revisions
    (`+atlasRevisionColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
    ON CONFLICT (version) DO UPDATE SET
    description = EXCLUDED.description, type = EXCLUDED.type, applied = EXCLUDED.applied,
    total = EXCLUDED.total, executed_at = EXCLUDED.executed_at, execution_time = EXCLUDED.execution_time,
    error = EXCLUDED.error, error_stmt = EXCLUDED.error_stmt, hash = EXCLUDED.hash,
    partial_hashes = EXCLUDED.partial_hashes, operator_version = EXCLUDED.operator_version`,
		rev.Version, rev.Description, int64(rev.Type), rev.Applied, rev.Total,
		executedAt, int64(rev.ExecutionTime), errText, errStmt, rev.Hash, partial, rev.OperatorVersion)
	if err != nil {
		return fmt.Errorf("write atlas revision %s: %w", rev.Version, err)
	}
	return nil
}

// DeleteRevision implements migrate.RevisionReadWriter.
func (s *atlasRevisionStore) DeleteRevision(ctx context.Context, version string) error {
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM "+atlasRevisionSchema+"."+atlasRevisionTable+" WHERE version = $1", version); err != nil {
		return fmt.Errorf("delete atlas revision %s: %w", version, err)
	}
	return nil
}
