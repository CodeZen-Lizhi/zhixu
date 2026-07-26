// Package postgres persists immutable Artifact snapshots in PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	artifactSchemaVersion         = "artifact/v1"
	artifactRevisionSchemaVersion = "artifact-revision/v1"
	artifactReceiptSchemaVersion  = "artifact-command-receipt/v1"
)

// DB is the pgx-compatible boundary required by the Artifact repository.
type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// Repository is the PostgreSQL implementation of application.Repository.
type Repository struct{ db DB }

var _ artifactapp.Repository = (*Repository)(nil)

// NewRepository constructs an Artifact PostgreSQL repository.
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, unavailable(errors.New("artifact database is nil"))
	}
	return &Repository{db: db}, nil
}

// FindCommand returns an exact durable response for a matching command receipt.
func (r *Repository) FindCommand(ctx context.Context, binding artifactapp.CommandBinding) (artifactapp.CommandResult, bool, error) {
	if r == nil || r.db == nil {
		return artifactapp.CommandResult{}, false, unavailable(errors.New("artifact repository is unavailable"))
	}
	if err := validateBinding(binding, false); err != nil {
		return artifactapp.CommandResult{}, false, err
	}
	receipt, found, err := loadReceipt(ctx, r.db, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		return artifactapp.CommandResult{}, found, err
	}
	if err := receipt.matches(binding); err != nil {
		return artifactapp.CommandResult{}, false, err
	}
	result := receipt.Result
	result.Replayed = true
	return result, true, nil
}

// ProbeExternalTransition returns the current Artifact without mutating the
// reservation table. A different existing owner wins over later domain
// preflight errors so callers receive a stable concurrency conflict.
func (r *Repository) ProbeExternalTransition(ctx context.Context, binding artifactapp.CommandBinding) (artifactapp.State, error) {
	if r == nil || r.db == nil {
		return artifactapp.State{}, unavailable(errors.New("artifact repository is unavailable"))
	}
	if err := validateExternalReservationBinding(binding); err != nil {
		return artifactapp.State{}, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return artifactapp.State{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	current, err := loadState(ctx, tx, binding.WorkspaceID, binding.ArtifactID, false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return artifactapp.State{}, notFound(err)
		}
		return artifactapp.State{}, err
	}
	if current.Artifact.Version != binding.ExpectedVersion {
		return artifactapp.State{}, versionConflict(errors.New("artifact expected version is stale"))
	}
	var commandType, idempotencyKey, requestHash, revisionID string
	var expectedVersion int64
	err = tx.QueryRow(ctx, `SELECT command_type,idempotency_key,request_hash,current_revision_id::text,expected_version FROM learning.artifact_external_transition_reservation WHERE workspace_id=$1 AND artifact_id=$2`, string(binding.WorkspaceID), string(binding.ArtifactID)).Scan(&commandType, &idempotencyKey, &requestHash, &revisionID, &expectedVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.State{}, classify(err)
	}
	if err == nil && (commandType != string(binding.CommandType) || idempotencyKey != binding.IdempotencyKey || requestHash != binding.RequestHash || revisionID != string(current.Revision.ID) || expectedVersion != binding.ExpectedVersion) {
		return artifactapp.State{}, versionConflict(errors.New("artifact external transition is reserved by another command"))
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactapp.State{}, classify(err)
	}
	return current, nil
}

// ReserveExternalTransition durably serializes Export and Publish before their
// filesystem or Change Control side effects. The exact owner may retry after a
// response loss; a different command is rejected before it can create an
// unbound external fact.
func (r *Repository) ReserveExternalTransition(ctx context.Context, binding artifactapp.CommandBinding) (artifactapp.State, error) {
	if r == nil || r.db == nil {
		return artifactapp.State{}, unavailable(errors.New("artifact repository is unavailable"))
	}
	if err := validateExternalReservationBinding(binding); err != nil {
		return artifactapp.State{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return artifactapp.State{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	current, err := loadState(ctx, tx, binding.WorkspaceID, binding.ArtifactID, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return artifactapp.State{}, notFound(err)
		}
		return artifactapp.State{}, err
	}
	if current.Artifact.Version != binding.ExpectedVersion {
		return artifactapp.State{}, versionConflict(errors.New("artifact expected version is stale"))
	}
	var commandType, idempotencyKey, requestHash, revisionID string
	var expectedVersion int64
	err = tx.QueryRow(ctx, `SELECT command_type,idempotency_key,request_hash,current_revision_id::text,expected_version FROM learning.artifact_external_transition_reservation WHERE workspace_id=$1 AND artifact_id=$2`, string(binding.WorkspaceID), string(binding.ArtifactID)).Scan(&commandType, &idempotencyKey, &requestHash, &revisionID, &expectedVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `INSERT INTO learning.artifact_external_transition_reservation(workspace_id,artifact_id,current_revision_id,expected_version,command_type,idempotency_key,request_hash,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, string(binding.WorkspaceID), string(binding.ArtifactID), string(current.Revision.ID), binding.ExpectedVersion, string(binding.CommandType), binding.IdempotencyKey, binding.RequestHash, current.Artifact.UpdatedAt.UTC()); err != nil {
			return artifactapp.State{}, classify(err)
		}
	} else if err != nil {
		return artifactapp.State{}, classify(err)
	} else if commandType != string(binding.CommandType) || idempotencyKey != binding.IdempotencyKey || requestHash != binding.RequestHash || revisionID != string(current.Revision.ID) || expectedVersion != binding.ExpectedVersion {
		return artifactapp.State{}, versionConflict(errors.New("artifact external transition is reserved by another command"))
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactapp.State{}, classify(err)
	}
	return current, nil
}

// Create atomically persists the initial v1 Artifact, its immutable revision and receipt.
func (r *Repository) Create(ctx context.Context, record artifactapp.CreateRecord) (artifactapp.CommandResult, error) {
	if r == nil || r.db == nil {
		return artifactapp.CommandResult{}, unavailable(errors.New("artifact repository is unavailable"))
	}
	if err := validateCreateRecord(record); err != nil {
		return artifactapp.CommandResult{}, err
	}
	return r.write(ctx, record.Binding, func(tx pgx.Tx) (artifactapp.CommandResult, error) {
		if receipt, found, err := loadReceipt(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
			return artifactapp.CommandResult{}, err
		} else if found {
			if err := receipt.matches(record.Binding); err != nil {
				return artifactapp.CommandResult{}, err
			}
			result := receipt.Result
			result.Replayed = true
			return result, nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO learning.artifact(
				id,workspace_id,artifact_type,title,scope,scope_definition,source_coverage,current_revision_id,
				status,version,domain_schema_version,created_at,updated_at
			) VALUES($1,$2,$3,$4,'{}'::jsonb,$5,$6,$7,$8,$9,$10,$11,$12)`,
			string(record.State.Artifact.ID), string(record.State.Artifact.WorkspaceID), record.State.Artifact.Type,
			record.State.Artifact.Title, record.State.Artifact.ScopeDefinition, marshalJSON(record.State.Artifact.SourceCoverage),
			string(record.State.Artifact.CurrentRevisionID), string(record.State.Artifact.Status), record.State.Artifact.Version,
			artifactSchemaVersion, record.State.Artifact.CreatedAt.UTC(), record.State.Artifact.UpdatedAt.UTC()); err != nil {
			return artifactapp.CommandResult{}, classify(err)
		}
		if err := insertRevision(ctx, tx, record.State.Artifact.WorkspaceID, record.State.Revision); err != nil {
			return artifactapp.CommandResult{}, err
		}
		return insertReceipt(ctx, tx, record.Binding, commandResult(record.State, record.Binding, nil, nil))
	})
}

// Transition persists an optimistic Artifact transition and any immutable side facts.
func (r *Repository) Transition(ctx context.Context, record artifactapp.TransitionRecord) (artifactapp.CommandResult, error) {
	if r == nil || r.db == nil {
		return artifactapp.CommandResult{}, unavailable(errors.New("artifact repository is unavailable"))
	}
	if err := validateTransitionRecord(record); err != nil {
		return artifactapp.CommandResult{}, err
	}
	return r.write(ctx, record.Binding, func(tx pgx.Tx) (artifactapp.CommandResult, error) {
		if receipt, found, err := loadReceipt(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
			return artifactapp.CommandResult{}, err
		} else if found {
			if err := receipt.matches(record.Binding); err != nil {
				return artifactapp.CommandResult{}, err
			}
			result := receipt.Result
			result.Replayed = true
			return result, nil
		}

		current, err := loadState(ctx, tx, record.Binding.WorkspaceID, record.Binding.ArtifactID, true)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return artifactapp.CommandResult{}, notFound(err)
			}
			return artifactapp.CommandResult{}, err
		}
		if current.Artifact.Version != record.Binding.ExpectedVersion || current.Revision.ID != record.CurrentRevisionID {
			return artifactapp.CommandResult{}, versionConflict(errors.New("artifact expected version or current revision is stale"))
		}
		reserved, err := externalReservationMatches(ctx, tx, record.Binding, record.CurrentRevisionID)
		if err != nil {
			return artifactapp.CommandResult{}, err
		}
		if (record.Binding.CommandType == artifactapp.CommandExportMarkdown || record.Binding.CommandType == artifactapp.CommandPublish) && !reserved {
			return artifactapp.CommandResult{}, versionConflict(errors.New("artifact external transition has no reservation"))
		}
		if record.NewRevision {
			if record.State.Revision.RevisionNo != current.Revision.RevisionNo+1 {
				return artifactapp.CommandResult{}, inconsistent(errors.New("artifact next revision number is invalid"))
			}
			if err := insertRevision(ctx, tx, record.State.Artifact.WorkspaceID, record.State.Revision); err != nil {
				return artifactapp.CommandResult{}, err
			}
		} else if !reflect.DeepEqual(current.Revision, record.State.Revision) {
			return artifactapp.CommandResult{}, inconsistent(errors.New("artifact transition changed an immutable revision"))
		}

		tag, err := tx.Exec(ctx, `
			UPDATE learning.artifact
			SET status=$1,source_coverage=$2,current_revision_id=$3,version=$4,updated_at=$5
			WHERE id=$6 AND workspace_id=$7 AND domain_schema_version=$8 AND version=$9 AND current_revision_id=$10`,
			string(record.State.Artifact.Status), marshalJSON(record.State.Artifact.SourceCoverage),
			string(record.State.Artifact.CurrentRevisionID), record.State.Artifact.Version, record.State.Artifact.UpdatedAt.UTC(),
			string(record.Binding.ArtifactID), string(record.Binding.WorkspaceID), artifactSchemaVersion,
			record.Binding.ExpectedVersion, string(record.CurrentRevisionID))
		if err != nil {
			return artifactapp.CommandResult{}, classify(err)
		}
		if tag.RowsAffected() != 1 {
			return artifactapp.CommandResult{}, versionConflict(errors.New("artifact compare-and-swap did not update a row"))
		}
		if record.Export != nil {
			if err := insertExport(ctx, tx, *record.Export); err != nil {
				return artifactapp.CommandResult{}, err
			}
		}
		if record.Publication != nil {
			if err := insertPublication(ctx, tx, *record.Publication); err != nil {
				return artifactapp.CommandResult{}, err
			}
		}
		result, err := insertReceipt(ctx, tx, record.Binding, commandResult(record.State, record.Binding, record.Export, record.Publication))
		if err != nil {
			return artifactapp.CommandResult{}, err
		}
		if reserved {
			tag, err := tx.Exec(ctx, `DELETE FROM learning.artifact_external_transition_reservation WHERE workspace_id=$1 AND artifact_id=$2 AND current_revision_id=$3 AND expected_version=$4 AND command_type=$5 AND idempotency_key=$6 AND request_hash=$7`, string(record.Binding.WorkspaceID), string(record.Binding.ArtifactID), string(record.CurrentRevisionID), record.Binding.ExpectedVersion, string(record.Binding.CommandType), record.Binding.IdempotencyKey, record.Binding.RequestHash)
			if err != nil {
				return artifactapp.CommandResult{}, classify(err)
			}
			if tag.RowsAffected() != 1 {
				return artifactapp.CommandResult{}, versionConflict(errors.New("artifact external transition reservation changed before completion"))
			}
		}
		return result, nil
	})
}

func validateExternalReservationBinding(binding artifactapp.CommandBinding) error {
	if err := validateBinding(binding, true); err != nil {
		return err
	}
	if binding.CommandType != artifactapp.CommandExportMarkdown && binding.CommandType != artifactapp.CommandPublish {
		return requestInvalid(errors.New("artifact external transition command is invalid"))
	}
	return nil
}

func externalReservationMatches(ctx context.Context, tx pgx.Tx, binding artifactapp.CommandBinding, revisionID foundation.ID) (bool, error) {
	var commandType, idempotencyKey, requestHash, reservedRevision string
	var expectedVersion int64
	err := tx.QueryRow(ctx, `SELECT command_type,idempotency_key,request_hash,current_revision_id::text,expected_version FROM learning.artifact_external_transition_reservation WHERE workspace_id=$1 AND artifact_id=$2 FOR UPDATE`, string(binding.WorkspaceID), string(binding.ArtifactID)).Scan(&commandType, &idempotencyKey, &requestHash, &reservedRevision, &expectedVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, classify(err)
	}
	if commandType != string(binding.CommandType) || idempotencyKey != binding.IdempotencyKey || requestHash != binding.RequestHash || reservedRevision != string(revisionID) || expectedVersion != binding.ExpectedVersion {
		return false, versionConflict(errors.New("artifact external transition is reserved by another command"))
	}
	return true, nil
}

// Get returns the v1 current revision, always constrained by Workspace.
func (r *Repository) Get(ctx context.Context, workspaceID, artifactID foundation.ID) (artifactapp.State, error) {
	if r == nil || r.db == nil {
		return artifactapp.State{}, unavailable(errors.New("artifact repository is unavailable"))
	}
	state, err := loadState(ctx, r.db, workspaceID, artifactID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.State{}, notFound(err)
	}
	return state, err
}

// List returns a keyset page ordered by current update time and Artifact ID.
func (r *Repository) List(ctx context.Context, query artifactapp.ListQuery) (artifactapp.ArtifactPage, error) {
	if r == nil || r.db == nil {
		return artifactapp.ArtifactPage{}, unavailable(errors.New("artifact repository is unavailable"))
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 {
		return artifactapp.ArtifactPage{}, requestInvalid(errors.New("artifact list query is invalid"))
	}
	sql := stateSelect + ` WHERE a.workspace_id=$1 AND a.domain_schema_version='artifact/v1' AND r.domain_schema_version='artifact-revision/v1'`
	args := []any{string(query.WorkspaceID)}
	if query.After != nil {
		args = append(args, query.After.UpdatedAt.UTC(), string(query.After.ID))
		sql += fmt.Sprintf(` AND (a.updated_at,a.id)<($%d,$%d)`, len(args)-1, len(args))
	}
	args = append(args, query.Limit+1)
	sql += fmt.Sprintf(` ORDER BY a.updated_at DESC,a.id DESC LIMIT $%d`, len(args))
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return artifactapp.ArtifactPage{}, classify(err)
	}
	defer rows.Close()
	items := make([]artifactapp.State, 0, query.Limit)
	for rows.Next() {
		state, err := scanState(rows)
		if err != nil {
			return artifactapp.ArtifactPage{}, err
		}
		items = append(items, state)
	}
	if err := rows.Err(); err != nil {
		return artifactapp.ArtifactPage{}, classify(err)
	}
	page := artifactapp.ArtifactPage{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		last := page.Items[len(page.Items)-1].Artifact
		page.Next = &artifactapp.ArtifactCursor{UpdatedAt: last.UpdatedAt.UTC(), ID: last.ID}
	}
	return page, nil
}

// GetExport returns an export only when its Artifact and Workspace bindings match.
func (r *Repository) GetExport(ctx context.Context, workspaceID, artifactID, exportID foundation.ID) (artifactapp.ExportRecord, error) {
	if r == nil || r.db == nil {
		return artifactapp.ExportRecord{}, unavailable(errors.New("artifact repository is unavailable"))
	}
	var record artifactapp.ExportRecord
	err := r.db.QueryRow(ctx, `
		SELECT id::text,workspace_id::text,artifact_id::text,revision_id::text,artifact_version,revision_no,
		       revision_hash,output_path,output_hash,output_size,exported_at
		FROM learning.artifact_export
		WHERE workspace_id=$1 AND artifact_id=$2 AND id=$3`,
		string(workspaceID), string(artifactID), string(exportID)).Scan(
		&record.ID, &record.WorkspaceID, &record.ArtifactID, &record.RevisionID, &record.ArtifactVersion,
		&record.RevisionNo, &record.RevisionHash, &record.OutputPath, &record.OutputHash, &record.OutputSize, &record.ExportedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.ExportRecord{}, notFound(err)
	}
	if err != nil {
		return artifactapp.ExportRecord{}, classify(err)
	}
	record.ExportedAt = record.ExportedAt.UTC()
	if err := artifactapp.ValidateExportRecord(record); err != nil {
		return artifactapp.ExportRecord{}, inconsistent(fmt.Errorf("decode artifact export: %w", err))
	}
	return record, nil
}

func (r *Repository) write(ctx context.Context, binding artifactapp.CommandBinding, operation func(pgx.Tx) (artifactapp.CommandResult, error)) (artifactapp.CommandResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return artifactapp.CommandResult{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(binding.WorkspaceID)+":"+binding.IdempotencyKey); err != nil {
		return artifactapp.CommandResult{}, classify(err)
	}
	result, err := operation(tx)
	if err != nil {
		return artifactapp.CommandResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactapp.CommandResult{}, classify(err)
	}
	return result, nil
}

func validateCreateRecord(record artifactapp.CreateRecord) error {
	if err := validateBinding(record.Binding, true); err != nil {
		return err
	}
	if record.Binding.CommandType != artifactapp.CommandPlan || record.Binding.ExpectedVersion != 0 ||
		record.Binding.WorkspaceID != record.State.Artifact.WorkspaceID || record.Binding.ArtifactID != record.State.Artifact.ID ||
		record.State.Artifact.Version != 1 || record.State.Revision.RevisionNo != 1 {
		return requestInvalid(errors.New("artifact create record is invalid"))
	}
	if err := artifactapp.ValidateState(record.State); err != nil {
		return requestInvalid(fmt.Errorf("artifact create state is invalid: %w", err))
	}
	return nil
}

func validateTransitionRecord(record artifactapp.TransitionRecord) error {
	if err := validateBinding(record.Binding, true); err != nil {
		return err
	}
	if record.Binding.CommandType == artifactapp.CommandPlan || record.Binding.ExpectedVersion < 1 ||
		!validID(record.CurrentRevisionID) || record.State.Artifact.ID != record.Binding.ArtifactID ||
		record.State.Artifact.WorkspaceID != record.Binding.WorkspaceID ||
		record.State.Artifact.Version != record.Binding.ExpectedVersion+1 {
		return requestInvalid(errors.New("artifact transition record is invalid"))
	}
	if err := artifactapp.ValidateState(record.State); err != nil {
		return requestInvalid(fmt.Errorf("artifact transition state is invalid: %w", err))
	}
	switch record.Binding.CommandType {
	case artifactapp.CommandExportMarkdown:
		if record.Export == nil || record.Publication != nil {
			return requestInvalid(errors.New("artifact export transition must contain only an export fact"))
		}
	case artifactapp.CommandPublish:
		if record.Export != nil || record.Publication == nil {
			return requestInvalid(errors.New("artifact publish transition must contain only a publication fact"))
		}
	default:
		if record.Export != nil || record.Publication != nil {
			return requestInvalid(errors.New("artifact non-external transition must not contain a side fact"))
		}
	}
	if record.Export != nil {
		if err := artifactapp.ValidateExportRecord(*record.Export); err != nil || record.Export.WorkspaceID != record.State.Artifact.WorkspaceID || record.Export.ArtifactID != record.State.Artifact.ID || record.Export.RevisionID != record.State.Revision.ID || record.Export.ArtifactVersion != record.State.Artifact.Version || record.Export.RevisionNo != record.State.Revision.RevisionNo || record.Export.RevisionHash != record.State.Revision.ContentHash {
			return requestInvalid(errors.New("artifact transition export binding is invalid"))
		}
	}
	if record.Publication != nil {
		if err := artifactapp.ValidatePublicationRecord(*record.Publication); err != nil || record.Publication.WorkspaceID != record.State.Artifact.WorkspaceID || record.Publication.ArtifactID != record.State.Artifact.ID || record.Publication.RevisionID != record.State.Revision.ID || record.Publication.ArtifactVersion != record.State.Artifact.Version || record.Publication.RevisionNo != record.State.Revision.RevisionNo || record.Publication.ContentHash != record.State.Revision.ContentHash || record.Publication.IdempotencyKey != record.Binding.IdempotencyKey {
			return requestInvalid(errors.New("artifact transition publication binding is invalid"))
		}
	}
	return nil
}

func validateBinding(binding artifactapp.CommandBinding, requireArtifact bool) error {
	if !validID(binding.WorkspaceID) || (requireArtifact && !validID(binding.ArtifactID)) || binding.IdempotencyKey == "" || len(binding.IdempotencyKey) > 128 || strings.ContainsAny(binding.IdempotencyKey, "\r\n") || !validHash(binding.RequestHash) || binding.CommandType == "" || len(binding.CommandType) > 64 || binding.ExpectedVersion < 0 {
		return requestInvalid(errors.New("artifact command binding is invalid"))
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
