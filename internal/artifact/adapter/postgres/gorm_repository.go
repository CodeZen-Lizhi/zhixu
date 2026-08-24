package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// GORMRepository is the staged Artifact persistence implementation. Production
// composition remains on Repository until the TODO9 equivalence gate passes.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

// NewGORMRepository constructs an Artifact repository from the shared Pool.
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	if pool == nil {
		return nil, unavailable(errors.New("artifact PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, unavailable(err)
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, unavailable(err)
	}
	if !validGORMArtifactDatabase(database) || nilGORMArtifactDependency(uow) {
		return nil, unavailable(errors.New("artifact GORM dependencies are unavailable"))
	}
	return &GORMRepository{database: database, unitOfWork: uow}, nil
}

func (repository *GORMRepository) ready() error {
	if repository == nil || !validGORMArtifactDatabase(repository.database) || nilGORMArtifactDependency(repository.unitOfWork) {
		return unavailable(errors.New("artifact GORM repository is unavailable"))
	}
	return nil
}

func (repository *GORMRepository) within(ctx context.Context, options foundation.TransactionOptions, work func(context.Context, *gorm.DB) error, fallback string) error {
	if err := repository.ready(); err != nil {
		return err
	}
	if ctx == nil {
		return requestInvalid(errors.New("artifact context is nil"))
	}
	if work == nil {
		return requestInvalid(errors.New("artifact transaction callback is nil"))
	}
	callbackSucceeded := false
	err := repository.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if err := work(callbackCtx, tx.WithContext(callbackCtx)); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if callbackSucceeded && err != nil {
		fallback = "ARTIFACT_COMMIT_FAILED"
	}
	return classifyGORM(ctx, err, fallback)
}

func classifyGORM(ctx context.Context, err error, fallback string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if gormNoRows(err) {
		return notFound(err)
	}
	if cause := gormArtifactContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, artifactapp.ErrorCodeDependencyUnavailable, false, cause)
		}
		return unavailable(cause)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return unavailable(err)
	}
	_ = fallback
	return classify(err)
}

func gormArtifactContextCause(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		cause := context.Cause(ctx)
		if cause == nil {
			return ctx.Err()
		}
		if !errors.Is(cause, ctx.Err()) {
			return errors.Join(ctx.Err(), cause)
		}
		return cause
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func gormNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func gormRow(database *gorm.DB, query string, args ...any) (*sql.Row, error) {
	if !validGORMArtifactDatabase(database) {
		return nil, errors.New("artifact GORM database is nil")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("artifact GORM returned nil row")
	}
	return row, nil
}

func gormRows(database *gorm.DB, query string, args ...any) (*sql.Rows, error) {
	if !validGORMArtifactDatabase(database) {
		return nil, errors.New("artifact GORM database is nil")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("artifact GORM returned nil rows")
	}
	return rows, nil
}

func gormLockCommand(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key string) error {
	return tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, string(workspaceID)+":"+key).Error
}

func gormStateSelect() string {
	return `SELECT
		a.id::text,a.workspace_id::text,a.artifact_type,a.title,a.status,a.scope_definition,a.source_coverage,a.current_revision_id::text,a.version,a.created_at,a.updated_at,
		r.id::text,r.artifact_id::text,r.revision_no,r.outline,r.sections,r.created_by_type,r.generation_metadata,r.content_hash,r.created_at,r.coverage,r.missing,r.conflicts,r.content_markdown,r.provenance
		FROM learning.artifact a
		JOIN learning.artifact_revision r ON r.id=a.current_revision_id AND r.workspace_id=a.workspace_id AND r.artifact_id=a.id`
}

func gormLoadState(ctx context.Context, database *gorm.DB, workspaceID, artifactID foundation.ID, lock, hideHeld bool) (artifactapp.State, error) {
	query := gormStateSelect() + ` WHERE a.workspace_id=? AND a.id=? AND a.domain_schema_version='artifact/v1' AND r.domain_schema_version IN ('artifact-revision/v1','artifact-revision/v2')`
	if hideHeld {
		query += ` AND NOT EXISTS (SELECT 1 FROM learning.artifact_visibility_hold h WHERE h.workspace_id=a.workspace_id AND h.artifact_id=a.id)`
	}
	if lock {
		query += ` FOR UPDATE OF a`
	}
	row, err := gormRow(database.WithContext(ctx), query, string(workspaceID), string(artifactID))
	if err != nil {
		return artifactapp.State{}, err
	}
	state, err := scanState(row)
	if err != nil {
		return artifactapp.State{}, err
	}
	if err := gormValidateRevisionDocumentSources(ctx, database, workspaceID, state.Revision); err != nil {
		return artifactapp.State{}, err
	}
	return state, nil
}

func (repository *GORMRepository) FindCommand(ctx context.Context, binding artifactapp.CommandBinding) (artifactapp.CommandResult, bool, error) {
	if err := repository.ready(); err != nil {
		return artifactapp.CommandResult{}, false, err
	}
	if ctx == nil {
		return artifactapp.CommandResult{}, false, requestInvalid(errors.New("artifact context is nil"))
	}
	if err := validateBinding(binding, false); err != nil {
		return artifactapp.CommandResult{}, false, err
	}
	receipt, found, err := gormLoadReceipt(ctx, repository.database.WithContext(ctx), binding.WorkspaceID, binding.IdempotencyKey)
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

func (repository *GORMRepository) ProbeExternalTransition(ctx context.Context, binding artifactapp.CommandBinding) (result artifactapp.State, err error) {
	if err = validateExternalReservationBinding(binding); err != nil {
		return result, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(ctx context.Context, tx *gorm.DB) error {
		current, loadErr := gormLoadState(ctx, tx, binding.WorkspaceID, binding.ArtifactID, false, false)
		if gormNoRows(loadErr) {
			return notFound(loadErr)
		}
		if loadErr != nil {
			return loadErr
		}
		if current.Artifact.Version != binding.ExpectedVersion {
			return versionConflict(errors.New("artifact expected version is stale"))
		}
		_, checkErr := gormLoadReservation(ctx, tx, binding, current.Revision.ID, false)
		if checkErr != nil {
			return checkErr
		}
		result = current
		return nil
	}, "ARTIFACT_EXTERNAL_PROBE_FAILED")
	return result, err
}

func (repository *GORMRepository) ReserveExternalTransition(ctx context.Context, binding artifactapp.CommandBinding) (result artifactapp.State, err error) {
	if err = validateExternalReservationBinding(binding); err != nil {
		return result, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		current, loadErr := gormLoadState(ctx, tx, binding.WorkspaceID, binding.ArtifactID, true, false)
		if gormNoRows(loadErr) {
			return notFound(loadErr)
		}
		if loadErr != nil {
			return loadErr
		}
		if current.Artifact.Version != binding.ExpectedVersion {
			return versionConflict(errors.New("artifact expected version is stale"))
		}
		reserved, checkErr := gormLoadReservation(ctx, tx, binding, current.Revision.ID, true)
		if checkErr != nil {
			return checkErr
		}
		if !reserved {
			if createErr := tx.Exec(`INSERT INTO learning.artifact_external_transition_reservation(workspace_id,artifact_id,current_revision_id,expected_version,command_type,idempotency_key,request_hash,created_at) VALUES(?,?,?,?,?,?,?,?)`, string(binding.WorkspaceID), string(binding.ArtifactID), string(current.Revision.ID), binding.ExpectedVersion, string(binding.CommandType), binding.IdempotencyKey, binding.RequestHash, current.Artifact.UpdatedAt.UTC()).Error; createErr != nil {
				return createErr
			}
		}
		result = current
		return nil
	}, "ARTIFACT_EXTERNAL_RESERVATION_FAILED")
	return result, err
}

func (repository *GORMRepository) Create(ctx context.Context, record artifactapp.CreateRecord) (result artifactapp.CommandResult, err error) {
	if err = validateCreateRecord(record); err != nil {
		return result, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
			return err
		}
		if receipt, found, loadErr := gormLoadReceipt(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); loadErr != nil {
			return loadErr
		} else if found {
			if err := receipt.matches(record.Binding); err != nil {
				return err
			}
			result = receipt.Result
			result.Replayed = true
			return nil
		}
		if e := tx.Exec(`INSERT INTO learning.artifact(id,workspace_id,artifact_type,title,scope,scope_definition,source_coverage,current_revision_id,status,version,domain_schema_version,created_at,updated_at) VALUES(?::uuid,?::uuid,?,?, '{}'::jsonb,?,?::jsonb,?::uuid,?,?, 'artifact/v1',?::timestamptz,?::timestamptz)`, string(record.State.Artifact.ID), string(record.State.Artifact.WorkspaceID), record.State.Artifact.Type, record.State.Artifact.Title, record.State.Artifact.ScopeDefinition, string(marshalJSON(record.State.Artifact.SourceCoverage)), string(record.State.Artifact.CurrentRevisionID), string(record.State.Artifact.Status), record.State.Artifact.Version, record.State.Artifact.CreatedAt.UTC(), record.State.Artifact.UpdatedAt.UTC()).Error; e != nil {
			return e
		}
		if e := gormInsertRevision(ctx, tx, record.State.Artifact.WorkspaceID, record.State.Revision); e != nil {
			return e
		}
		result, err = gormInsertReceipt(ctx, tx, record.Binding, commandResult(record.State, record.Binding, nil, nil))
		if err != nil {
			return err
		}
		if record.VisibilityHold != nil {
			if e := tx.Exec(`INSERT INTO learning.artifact_visibility_hold(workspace_id,artifact_id,owner_type,owner_id,owner_role,attempt_digest,created_at) VALUES(?,?,?,?,?,?,?)`, string(record.State.Artifact.WorkspaceID), string(record.State.Artifact.ID), string(record.VisibilityHold.OwnerType), string(record.VisibilityHold.OwnerID), string(record.VisibilityHold.OwnerRole), record.VisibilityHold.AttemptDigest, record.State.Artifact.CreatedAt.UTC()).Error; e != nil {
				return e
			}
		}
		return nil
	}, "ARTIFACT_CREATE_FAILED")
	return result, err
}

func (repository *GORMRepository) Transition(ctx context.Context, record artifactapp.TransitionRecord) (result artifactapp.CommandResult, err error) {
	if err = validateTransitionRecord(record); err != nil {
		return result, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
			return err
		}
		if receipt, found, loadErr := gormLoadReceipt(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); loadErr != nil {
			return loadErr
		} else if found {
			if err := receipt.matches(record.Binding); err != nil {
				return err
			}
			result = receipt.Result
			result.Replayed = true
			return nil
		}
		current, loadErr := gormLoadState(ctx, tx, record.Binding.WorkspaceID, record.Binding.ArtifactID, true, false)
		if gormNoRows(loadErr) {
			return notFound(loadErr)
		}
		if loadErr != nil {
			return loadErr
		}
		if current.Artifact.Version != record.Binding.ExpectedVersion || current.Revision.ID != record.CurrentRevisionID {
			return versionConflict(errors.New("artifact expected version or current revision is stale"))
		}
		reserved, reservationErr := gormLoadReservation(ctx, tx, record.Binding, record.CurrentRevisionID, true)
		if reservationErr != nil {
			return reservationErr
		}
		if (record.Binding.CommandType == artifactapp.CommandExportMarkdown || record.Binding.CommandType == artifactapp.CommandPublish) && !reserved {
			return versionConflict(errors.New("artifact external transition has no reservation"))
		}
		if record.NewRevision {
			if record.State.Revision.RevisionNo != current.Revision.RevisionNo+1 {
				return inconsistent(errors.New("artifact next revision number is invalid"))
			}
			if e := gormInsertRevision(ctx, tx, record.State.Artifact.WorkspaceID, record.State.Revision); e != nil {
				return e
			}
		} else if !reflect.DeepEqual(current.Revision, record.State.Revision) {
			return inconsistent(errors.New("artifact transition changed an immutable revision"))
		}
		res := tx.Exec(`UPDATE learning.artifact SET status=?,source_coverage=?::jsonb,current_revision_id=?::uuid,version=?,updated_at=?::timestamptz WHERE id=?::uuid AND workspace_id=?::uuid AND domain_schema_version='artifact/v1' AND version=? AND current_revision_id=?::uuid`, string(record.State.Artifact.Status), string(marshalJSON(record.State.Artifact.SourceCoverage)), string(record.State.Artifact.CurrentRevisionID), record.State.Artifact.Version, record.State.Artifact.UpdatedAt.UTC(), string(record.Binding.ArtifactID), string(record.Binding.WorkspaceID), record.Binding.ExpectedVersion, string(record.CurrentRevisionID))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return versionConflict(errors.New("artifact compare-and-swap did not update a row"))
		}
		if record.Export != nil {
			if e := tx.Exec(`INSERT INTO learning.artifact_export(id,workspace_id,artifact_id,revision_id,artifact_version,revision_no,revision_hash,output_path,output_hash,output_size,exported_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, string(record.Export.ID), string(record.Export.WorkspaceID), string(record.Export.ArtifactID), string(record.Export.RevisionID), record.Export.ArtifactVersion, record.Export.RevisionNo, record.Export.RevisionHash, record.Export.OutputPath, record.Export.OutputHash, record.Export.OutputSize, record.Export.ExportedAt.UTC()).Error; e != nil {
				return e
			}
		}
		if record.Publication != nil {
			if e := tx.Exec(`INSERT INTO learning.artifact_publication(artifact_id,workspace_id,revision_id,artifact_version,revision_no,content_hash,proposal_id,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, string(record.Publication.ArtifactID), string(record.Publication.WorkspaceID), string(record.Publication.RevisionID), record.Publication.ArtifactVersion, record.Publication.RevisionNo, record.Publication.ContentHash, string(record.Publication.ProposalID), record.Publication.IdempotencyKey, record.Publication.CreatedAt.UTC()).Error; e != nil {
				return e
			}
		}
		result, err = gormInsertReceipt(ctx, tx, record.Binding, commandResult(record.State, record.Binding, record.Export, record.Publication))
		if err != nil {
			return err
		}
		if reserved {
			res = tx.Exec(`DELETE FROM learning.artifact_external_transition_reservation WHERE workspace_id=? AND artifact_id=? AND current_revision_id=? AND expected_version=? AND command_type=? AND idempotency_key=? AND request_hash=?`, string(record.Binding.WorkspaceID), string(record.Binding.ArtifactID), string(record.CurrentRevisionID), record.Binding.ExpectedVersion, string(record.Binding.CommandType), record.Binding.IdempotencyKey, record.Binding.RequestHash)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return versionConflict(errors.New("artifact external transition reservation changed before completion"))
			}
		}
		return nil
	}, "ARTIFACT_TRANSITION_FAILED")
	return result, err
}

func (repository *GORMRepository) GetCommandState(ctx context.Context, workspaceID, artifactID foundation.ID) (artifactapp.State, error) {
	if err := repository.ready(); err != nil {
		return artifactapp.State{}, err
	}
	if ctx == nil {
		return artifactapp.State{}, requestInvalid(errors.New("artifact context is nil"))
	}
	if !validID(workspaceID) || !validID(artifactID) {
		return artifactapp.State{}, requestInvalid(errors.New("artifact query identity is invalid"))
	}
	state, err := gormLoadState(ctx, repository.database.WithContext(ctx), workspaceID, artifactID, false, false)
	if gormNoRows(err) {
		return artifactapp.State{}, notFound(err)
	}
	if err != nil {
		return artifactapp.State{}, classifyGORM(ctx, err, "ARTIFACT_COMMAND_STATE_QUERY_FAILED")
	}
	return state, nil
}

func (repository *GORMRepository) Get(ctx context.Context, workspaceID, artifactID foundation.ID) (artifactapp.State, error) {
	if err := repository.ready(); err != nil {
		return artifactapp.State{}, err
	}
	if ctx == nil {
		return artifactapp.State{}, requestInvalid(errors.New("artifact context is nil"))
	}
	if !validID(workspaceID) || !validID(artifactID) {
		return artifactapp.State{}, requestInvalid(errors.New("artifact query identity is invalid"))
	}
	state, err := gormLoadState(ctx, repository.database.WithContext(ctx), workspaceID, artifactID, false, true)
	if gormNoRows(err) {
		return artifactapp.State{}, notFound(err)
	}
	if err != nil {
		return artifactapp.State{}, classifyGORM(ctx, err, "ARTIFACT_QUERY_FAILED")
	}
	return state, nil
}

func (repository *GORMRepository) List(ctx context.Context, query artifactapp.ListQuery) (page artifactapp.ArtifactPage, err error) {
	if err = repository.ready(); err != nil {
		return page, err
	}
	if ctx == nil {
		return page, requestInvalid(errors.New("artifact context is nil"))
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 {
		return page, requestInvalid(errors.New("artifact list query is invalid"))
	}
	conditions := []string{"a.workspace_id=?", "a.domain_schema_version='artifact/v1'", "r.domain_schema_version IN ('artifact-revision/v1','artifact-revision/v2')", "NOT EXISTS (SELECT 1 FROM learning.artifact_visibility_hold h WHERE h.workspace_id=a.workspace_id AND h.artifact_id=a.id)"}
	args := []any{string(query.WorkspaceID)}
	if query.After != nil {
		conditions = append(conditions, "(a.updated_at,a.id)<(?,?)")
		args = append(args, query.After.UpdatedAt.UTC(), string(query.After.ID))
	}
	args = append(args, query.Limit+1)
	rows, err := gormRows(repository.database.WithContext(ctx), gormStateSelect()+` WHERE `+strings.Join(conditions, " AND ")+` ORDER BY a.updated_at DESC,a.id DESC LIMIT ?`, args...)
	if err != nil {
		return page, classifyGORM(ctx, err, "ARTIFACT_LIST_FAILED")
	}
	items := make([]artifactapp.State, 0, query.Limit+1)
	for rows.Next() {
		state, scanErr := scanState(rows)
		if scanErr != nil {
			return page, gormCloseArtifactRows(rows, inconsistent(fmt.Errorf("scan artifact list state: %w", scanErr)))
		}
		items = append(items, state)
	}
	if err := rows.Err(); err != nil {
		return page, gormCloseArtifactRows(rows, classifyGORM(ctx, err, "ARTIFACT_LIST_FAILED"))
	}
	if err := rows.Close(); err != nil {
		return page, classifyGORM(ctx, err, "ARTIFACT_LIST_FAILED")
	}
	revisions := make([]domain.Revision, len(items))
	for index := range items {
		revisions[index] = items[index].Revision
	}
	if err := gormValidateRevisionDocumentSourcesBatch(ctx, repository.database, query.WorkspaceID, revisions); err != nil {
		return page, err
	}
	page.Items = items
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		last := page.Items[len(page.Items)-1].Artifact
		page.Next = &artifactapp.ArtifactCursor{UpdatedAt: last.UpdatedAt.UTC(), ID: last.ID}
	}
	return page, nil
}

func (repository *GORMRepository) GetExport(ctx context.Context, workspaceID, artifactID, exportID foundation.ID) (artifactapp.ExportRecord, error) {
	if err := repository.ready(); err != nil {
		return artifactapp.ExportRecord{}, err
	}
	if ctx == nil {
		return artifactapp.ExportRecord{}, requestInvalid(errors.New("artifact context is nil"))
	}
	if !validID(workspaceID) || !validID(artifactID) || !validID(exportID) {
		return artifactapp.ExportRecord{}, requestInvalid(errors.New("artifact export query identity is invalid"))
	}
	row, err := gormRow(repository.database.WithContext(ctx), `SELECT id::text,workspace_id::text,artifact_id::text,revision_id::text,artifact_version,revision_no,revision_hash,output_path,output_hash,output_size,exported_at FROM learning.artifact_export WHERE workspace_id=? AND artifact_id=? AND id=?`, string(workspaceID), string(artifactID), string(exportID))
	if err != nil {
		return artifactapp.ExportRecord{}, classifyGORM(ctx, err, "ARTIFACT_EXPORT_QUERY_FAILED")
	}
	var record artifactapp.ExportRecord
	if err := row.Scan(&record.ID, &record.WorkspaceID, &record.ArtifactID, &record.RevisionID, &record.ArtifactVersion, &record.RevisionNo, &record.RevisionHash, &record.OutputPath, &record.OutputHash, &record.OutputSize, &record.ExportedAt); gormNoRows(err) {
		return artifactapp.ExportRecord{}, notFound(err)
	} else if err != nil {
		return artifactapp.ExportRecord{}, classifyGORM(ctx, err, "ARTIFACT_EXPORT_QUERY_FAILED")
	}
	record.ExportedAt = record.ExportedAt.UTC()
	if err := artifactapp.ValidateExportRecord(record); err != nil {
		return artifactapp.ExportRecord{}, inconsistent(fmt.Errorf("decode artifact export: %w", err))
	}
	return record, nil
}

// ListSectionGenerations reads the same recovery projection as the legacy
// repository while using the shared GORM transaction boundary.
func (repository *GORMRepository) ListSectionGenerations(ctx context.Context, workspaceID, artifactID foundation.ID) (artifactapp.SectionGenerationSnapshot, error) {
	if err := repository.ready(); err != nil {
		return artifactapp.SectionGenerationSnapshot{}, err
	}
	if ctx == nil || !validID(workspaceID) || !validID(artifactID) {
		return artifactapp.SectionGenerationSnapshot{}, requestInvalid(errors.New("artifact section generation query is invalid"))
	}
	var snapshot artifactapp.SectionGenerationSnapshot
	err := repository.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(ctx context.Context, tx *gorm.DB) error {
		state, err := gormLoadState(ctx, tx, workspaceID, artifactID, false, false)
		if gormNoRows(err) {
			return notFound(err)
		}
		if err != nil {
			return err
		}
		keys, err := artifactapp.SectionGenerationProjectionKeys(state)
		if err != nil {
			return inconsistent(fmt.Errorf("resolve artifact generation projection: %w", err))
		}
		items := make([]artifactapp.SectionGeneration, 0, len(keys))
		if len(keys) > 0 {
			rows, rowsErr := gormRows(tx.WithContext(ctx), `SELECT DISTINCT ON (section_key) `+sectionGenerationColumns+` FROM learning.artifact_section_generation WHERE workspace_id=? AND artifact_id=? AND section_key=ANY(?::text[]) AND status IN ('PENDING','FAILED','CANCELLED','RECOVERY_REQUIRED') ORDER BY section_key,CASE WHEN status IN ('PENDING','RECOVERY_REQUIRED') THEN 0 ELSE 1 END,source_artifact_version DESC,updated_at DESC,created_at DESC,id DESC`, string(workspaceID), string(artifactID), pq.Array(keys))
			if rowsErr != nil {
				return rowsErr
			}
			selected := make(map[string]artifactapp.SectionGeneration, len(keys))
			for rows.Next() {
				generation, scanErr := scanSectionGeneration(rows)
				if scanErr != nil {
					return gormCloseArtifactRows(rows, inconsistent(fmt.Errorf("scan artifact generation projection: %w", scanErr)))
				}
				if _, duplicate := selected[generation.SectionKey]; duplicate {
					return gormCloseArtifactRows(rows, inconsistent(errors.New("artifact generation projection contains duplicate sections")))
				}
				selected[generation.SectionKey] = generation
			}
			if rowsErr := rows.Err(); rowsErr != nil {
				return gormCloseArtifactRows(rows, rowsErr)
			}
			if closeErr := rows.Close(); closeErr != nil {
				return classifyGORM(ctx, closeErr, "ARTIFACT_GENERATION_QUERY_FAILED")
			}
			for _, key := range keys {
				if generation, found := selected[key]; found {
					items = append(items, generation)
				}
			}
		}
		snapshot = artifactapp.SectionGenerationSnapshot{State: state, Items: items}
		return artifactapp.ValidateSectionGenerationSnapshot(snapshot, workspaceID, artifactID)
	}, "ARTIFACT_GENERATION_QUERY_FAILED")
	if err != nil {
		return artifactapp.SectionGenerationSnapshot{}, err
	}
	return snapshot, nil
}

func gormLoadReservation(ctx context.Context, tx *gorm.DB, binding artifactapp.CommandBinding, revisionID foundation.ID, lock bool) (bool, error) {
	query := `SELECT command_type,idempotency_key,request_hash,current_revision_id::text,expected_version FROM learning.artifact_external_transition_reservation WHERE workspace_id=? AND artifact_id=?`
	if lock {
		query += ` FOR UPDATE`
	}
	row, err := gormRow(tx.WithContext(ctx), query, string(binding.WorkspaceID), string(binding.ArtifactID))
	if err != nil {
		return false, err
	}
	var commandType, key, hash, reservedRevision string
	var expected int64
	if err := row.Scan(&commandType, &key, &hash, &reservedRevision, &expected); gormNoRows(err) {
		return false, nil
	} else if err != nil {
		return false, classifyGORM(ctx, err, "ARTIFACT_EXTERNAL_RESERVATION_QUERY_FAILED")
	}
	if commandType != string(binding.CommandType) || key != binding.IdempotencyKey || hash != binding.RequestHash || reservedRevision != string(revisionID) || expected != binding.ExpectedVersion {
		return false, versionConflict(errors.New("artifact external transition is reserved by another command"))
	}
	return true, nil
}

func gormInsertRevision(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, revision domain.Revision) error {
	coverage := coverageFromSections(revision.Sections)
	metadata := any(nil)
	if revision.Metadata != nil {
		metadata = string(marshalJSON(revision.Metadata))
	}
	schemaVersion, err := domain.RevisionSchemaVersion(revision)
	if err != nil {
		return inconsistent(fmt.Errorf("select artifact revision schema: %w", err))
	}
	if err := tx.WithContext(ctx).Exec(`INSERT INTO learning.artifact_revision(id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,domain_schema_version,content_hash,created_by_type,generation_metadata,created_at) VALUES(?::uuid,?::uuid,?::uuid,?,'SNAPSHOT',?::jsonb,?::jsonb,?::jsonb,'[]'::jsonb,'[]'::jsonb,?,?::jsonb,?,?,?,?::jsonb,?::timestamptz)`, string(revision.ID), string(revision.ArtifactID), string(workspaceID), revision.RevisionNo, string(marshalJSON(revision.Outline)), string(marshalJSON(revision.Sections)), string(marshalJSON(coverage)), markdownFromSections(revision.Sections), string(marshalJSON(map[string]string{"schema_version": schemaVersion})), schemaVersion, revision.ContentHash, string(revision.CreatedBy), metadata, revision.CreatedAt.UTC()).Error; err != nil {
		return classifyGORM(ctx, err, "ARTIFACT_REVISION_INSERT_FAILED")
	}
	if err := gormValidateRevisionDocumentSources(ctx, tx, workspaceID, revision); err != nil {
		return err
	}
	return gormInsertRevisionSelectors(ctx, tx, workspaceID, revision)
}

func gormInsertRevisionSelectors(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, revision domain.Revision) error {
	var enabled bool
	row, err := gormRow(tx.WithContext(ctx), `SELECT EXISTS(SELECT 1 FROM core.schema_meta WHERE key='timeline_impact' AND value='m7-v2')`)
	if err != nil {
		return classifyGORM(ctx, err, "ARTIFACT_CITATION_SELECTOR_GATE_FAILED")
	}
	if err := row.Scan(&enabled); err != nil {
		return classifyGORM(ctx, err, "ARTIFACT_CITATION_SELECTOR_GATE_FAILED")
	}
	if !enabled {
		return nil
	}
	selectors := revisionCitationSelectors(revision)
	if len(selectors) > 0 {
		versions := make([]string, len(selectors))
		spans := make([]string, len(selectors))
		for i, selector := range selectors {
			versions[i], spans[i] = string(selector.sourceVersionID), string(selector.sourceSpanID)
		}
		if err := tx.WithContext(ctx).Exec(`INSERT INTO learning.artifact_revision_citation_selector(workspace_id,artifact_id,revision_id,source_version_id,source_span_id) SELECT ?::uuid,?::uuid,?::uuid,citation.source_version_id,citation.source_span_id FROM unnest(?::uuid[],?::uuid[]) AS citation(source_version_id,source_span_id) ON CONFLICT (workspace_id,revision_id,source_version_id,source_span_id) DO NOTHING`, string(workspaceID), string(revision.ArtifactID), string(revision.ID), pq.Array(versions), pq.Array(spans)).Error; err != nil {
			return classifyGORM(ctx, err, "ARTIFACT_CITATION_SELECTOR_INSERT_FAILED")
		}
	}
	return gormValidateRevisionSelectors(ctx, tx, workspaceID, revision, selectors)
}

func gormValidateRevisionSelectors(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, revision domain.Revision, expected []revisionCitationSelector) error {
	rows, err := gormRows(tx.WithContext(ctx), `SELECT artifact_id::text,revision_id::text,source_version_id::text,source_span_id::text FROM learning.artifact_revision_citation_selector WHERE workspace_id=? AND revision_id=? ORDER BY artifact_id,revision_id,source_version_id,source_span_id`, string(workspaceID), string(revision.ID))
	if err != nil {
		return classifyGORM(ctx, err, "ARTIFACT_CITATION_SELECTOR_VALIDATE_FAILED")
	}
	actual := make(map[string]struct{}, len(expected))
	for rows.Next() {
		var artifactID, revisionID, versionID, spanID string
		if err := rows.Scan(&artifactID, &revisionID, &versionID, &spanID); err != nil {
			return gormCloseArtifactRows(rows, classifyGORM(ctx, err, "ARTIFACT_CITATION_SELECTOR_VALIDATE_FAILED"))
		}
		key := citationSelectorIdentity(foundation.ID(artifactID), foundation.ID(revisionID), foundation.ID(versionID), foundation.ID(spanID))
		if _, duplicate := actual[key]; duplicate {
			return gormCloseArtifactRows(rows, inconsistent(errors.New("artifact citation selector validation found a duplicate")))
		}
		actual[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return gormCloseArtifactRows(rows, classifyGORM(ctx, err, "ARTIFACT_CITATION_SELECTOR_VALIDATE_FAILED"))
	}
	if err := rows.Close(); err != nil {
		return classifyGORM(ctx, err, "ARTIFACT_CITATION_SELECTOR_VALIDATE_FAILED")
	}
	if len(actual) != len(expected) {
		return inconsistent(fmt.Errorf("artifact citation selector validation count mismatch: expected %d, got %d", len(expected), len(actual)))
	}
	for _, selector := range expected {
		key := citationSelectorIdentity(revision.ArtifactID, revision.ID, selector.sourceVersionID, selector.sourceSpanID)
		if _, found := actual[key]; !found {
			return inconsistent(errors.New("artifact citation selector validation found a binding mismatch"))
		}
	}
	return nil
}

// gormValidateRevisionDocumentSources verifies the trigger-maintained v2
// projection in the same transaction as the immutable revision insert.  The
// projection intentionally omits section ordinal, so validation compares the
// deduplicated durable identity set that the trigger writes.
func gormValidateRevisionDocumentSources(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, revision domain.Revision) error {
	return gormValidateRevisionDocumentSourcesBatch(ctx, tx, workspaceID, []domain.Revision{revision})
}

func gormValidateRevisionDocumentSourcesBatch(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, revisions []domain.Revision) error {
	if len(revisions) == 0 {
		return nil
	}
	type revisionSchemaBinding struct {
		artifactID foundation.ID
		version    string
	}
	expectedSchemas := make(map[foundation.ID]revisionSchemaBinding, len(revisions))
	expected := make(map[string]struct{})
	revisionIDs := make([]string, len(revisions))
	for index, revision := range revisions {
		if revision.ArtifactID == "" || revision.ID == "" {
			return inconsistent(errors.New("artifact document source validation revision identity is invalid"))
		}
		if _, duplicate := expectedSchemas[revision.ID]; duplicate {
			return inconsistent(errors.New("artifact document source validation contains a duplicate revision"))
		}
		schemaVersion, err := domain.RevisionSchemaVersion(revision)
		if err != nil {
			return inconsistent(fmt.Errorf("resolve artifact document source revision schema: %w", err))
		}
		expectedSchemas[revision.ID] = revisionSchemaBinding{artifactID: revision.ArtifactID, version: schemaVersion}
		revisionIDs[index] = string(revision.ID)
		for _, section := range revision.Sections {
			for _, source := range section.DocumentSources {
				key := documentSourceIdentity(revision.ArtifactID, revision.ID, source)
				expected[key] = struct{}{}
			}
		}
	}
	schemaRows, err := gormRows(tx.WithContext(ctx), `SELECT id::text,artifact_id::text,domain_schema_version FROM learning.artifact_revision WHERE workspace_id=?::uuid AND id=ANY(?::uuid[]) ORDER BY id`, string(workspaceID), pq.Array(revisionIDs))
	if err != nil {
		return classifyGORM(ctx, err, "ARTIFACT_DOCUMENT_SOURCE_SCHEMA_QUERY_FAILED")
	}
	actualSchemas := make(map[foundation.ID]struct{}, len(expectedSchemas))
	for schemaRows.Next() {
		var revisionID, artifactID, schemaVersion string
		if scanErr := schemaRows.Scan(&revisionID, &artifactID, &schemaVersion); scanErr != nil {
			return gormCloseArtifactRows(schemaRows, classifyGORM(ctx, scanErr, "ARTIFACT_DOCUMENT_SOURCE_SCHEMA_QUERY_FAILED"))
		}
		id := foundation.ID(revisionID)
		expectedSchema, found := expectedSchemas[id]
		if !found || expectedSchema.artifactID != foundation.ID(artifactID) || expectedSchema.version != schemaVersion {
			return gormCloseArtifactRows(schemaRows, inconsistent(errors.New("artifact revision schema binding mismatch")))
		}
		if _, duplicate := actualSchemas[id]; duplicate {
			return gormCloseArtifactRows(schemaRows, inconsistent(errors.New("artifact revision schema validation found a duplicate")))
		}
		actualSchemas[id] = struct{}{}
	}
	if scanErr := schemaRows.Err(); scanErr != nil {
		return gormCloseArtifactRows(schemaRows, classifyGORM(ctx, scanErr, "ARTIFACT_DOCUMENT_SOURCE_SCHEMA_QUERY_FAILED"))
	}
	if closeErr := schemaRows.Close(); closeErr != nil {
		return classifyGORM(ctx, closeErr, "ARTIFACT_DOCUMENT_SOURCE_SCHEMA_QUERY_FAILED")
	}
	if len(actualSchemas) != len(expectedSchemas) {
		return inconsistent(fmt.Errorf("artifact revision schema validation count mismatch: expected %d, got %d", len(expectedSchemas), len(actualSchemas)))
	}
	rows, err := gormRows(tx.WithContext(ctx), `SELECT artifact_id::text,revision_id::text,document_id::text,article_revision_id::text,revision_no,content_hash FROM learning.artifact_revision_document_source WHERE workspace_id=?::uuid AND revision_id=ANY(?::uuid[]) ORDER BY artifact_id,revision_id,document_id,article_revision_id,revision_no,content_hash`, string(workspaceID), pq.Array(revisionIDs))
	if err != nil {
		return classifyGORM(ctx, err, "ARTIFACT_DOCUMENT_SOURCE_QUERY_FAILED")
	}
	actual := make(map[string]struct{}, len(expected))
	for rows.Next() {
		var artifactID, revisionID, documentID, articleRevisionID, contentHash string
		var revisionNo int64
		if scanErr := rows.Scan(&artifactID, &revisionID, &documentID, &articleRevisionID, &revisionNo, &contentHash); scanErr != nil {
			return gormCloseArtifactRows(rows, classifyGORM(ctx, scanErr, "ARTIFACT_DOCUMENT_SOURCE_QUERY_FAILED"))
		}
		source := domain.DocumentSource{
			DocumentID: foundation.ID(documentID), ArticleRevisionID: foundation.ID(articleRevisionID),
			RevisionNo: revisionNo, VerifiedContentHash: contentHash, Verified: true,
		}
		key := documentSourceIdentity(foundation.ID(artifactID), foundation.ID(revisionID), source)
		if _, duplicate := actual[key]; duplicate {
			return gormCloseArtifactRows(rows, inconsistent(errors.New("artifact document source projection contains a duplicate")))
		}
		actual[key] = struct{}{}
	}
	if scanErr := rows.Err(); scanErr != nil {
		return gormCloseArtifactRows(rows, classifyGORM(ctx, scanErr, "ARTIFACT_DOCUMENT_SOURCE_QUERY_FAILED"))
	}
	if closeErr := rows.Close(); closeErr != nil {
		return classifyGORM(ctx, closeErr, "ARTIFACT_DOCUMENT_SOURCE_QUERY_FAILED")
	}
	if len(actual) != len(expected) {
		return inconsistent(fmt.Errorf("artifact document source projection count mismatch: expected %d, got %d", len(expected), len(actual)))
	}
	for key := range expected {
		if _, found := actual[key]; !found {
			return inconsistent(errors.New("artifact document source projection binding mismatch"))
		}
	}
	return nil
}

func validGORMArtifactDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilGORMArtifactDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func documentSourceIdentity(artifactID, revisionID foundation.ID, source domain.DocumentSource) string {
	return string(artifactID) + "\x00" + string(revisionID) + "\x00" + string(source.DocumentID) + "\x00" + string(source.ArticleRevisionID) + "\x00" + fmt.Sprint(source.RevisionNo) + "\x00" + source.VerifiedContentHash
}

func gormCloseArtifactRows(rows *sql.Rows, current error) error {
	if rows == nil {
		return current
	}
	closeErr := rows.Close()
	if current != nil {
		if closeErr != nil {
			return errors.Join(current, classify(closeErr))
		}
		return current
	}
	if closeErr != nil {
		return classify(closeErr)
	}
	return nil
}

func gormLoadReceipt(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key string) (commandReceipt, bool, error) {
	row, err := gormRow(database.WithContext(ctx), `SELECT response FROM learning.artifact_command WHERE workspace_id=? AND idempotency_key=?`, string(workspaceID), key)
	if err != nil {
		return commandReceipt{}, false, err
	}
	var raw []byte
	if err := row.Scan(&raw); gormNoRows(err) {
		return commandReceipt{}, false, nil
	} else if err != nil {
		return commandReceipt{}, false, classifyGORM(ctx, err, "ARTIFACT_COMMAND_QUERY_FAILED")
	}
	var receipt commandReceipt
	if err := decodeJSON(raw, &receipt); err != nil {
		return commandReceipt{}, false, inconsistent(fmt.Errorf("decode artifact command receipt: %w", err))
	}
	if err := receipt.validate(); err != nil {
		return commandReceipt{}, false, err
	}
	return receipt, true, nil
}

func gormInsertReceipt(ctx context.Context, tx *gorm.DB, binding artifactapp.CommandBinding, result artifactapp.CommandResult) (artifactapp.CommandResult, error) {
	receipt := commandReceipt{SchemaVersion: artifactReceiptSchemaVersion, WorkspaceID: binding.WorkspaceID, ArtifactID: result.State.Artifact.ID, IdempotencyKey: binding.IdempotencyKey, RequestHash: binding.RequestHash, CommandType: binding.CommandType, ExpectedVersion: binding.ExpectedVersion, Result: result}
	if err := receipt.validate(); err != nil {
		return artifactapp.CommandResult{}, err
	}
	if err := tx.WithContext(ctx).Exec(`INSERT INTO learning.artifact_command(workspace_id,idempotency_key,request_hash,command_type,artifact_id,artifact_version,response,created_at) VALUES(?::uuid,?,?,?,?,?,?::jsonb,?::timestamptz)`, string(binding.WorkspaceID), binding.IdempotencyKey, binding.RequestHash, string(binding.CommandType), string(result.State.Artifact.ID), result.CommandVersion, string(marshalJSON(receipt)), result.State.Artifact.UpdatedAt.UTC()).Error; err != nil {
		return artifactapp.CommandResult{}, classifyGORM(ctx, err, "ARTIFACT_COMMAND_INSERT_FAILED")
	}
	return result, nil
}

var _ artifactapp.Repository = (*GORMRepository)(nil)
