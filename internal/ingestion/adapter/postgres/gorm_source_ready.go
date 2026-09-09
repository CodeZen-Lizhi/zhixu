package postgres

import (
	"context"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// NewGORMRepositoryWithSourceReady is the production Ingestion constructor.
// Pool owns both GORM and UoW; appender must be constructed from that same pool.
func NewGORMRepositoryWithSourceReady(pool *platformpostgres.Pool, appender domain.SourceReadyAppender) (*GORMRepository, error) {
	if pool == nil || nilIngestionSourceReadyDependency(appender) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_SOURCE_READY_UNAVAILABLE", true, errors.New("source-ready transaction dependencies are unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_DATABASE_UNAVAILABLE", true, err)
	}
	repository, err := NewGORMRepository(database)
	if err != nil {
		return nil, err
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_SOURCE_READY_UNAVAILABLE", true, err)
	}
	repository.unitOfWork = unitOfWork
	repository.sourceReady = appender
	return repository, nil
}

func (r *GORMRepository) transitionAttemptWithSourceReady(ctx context.Context, transition domain.AttemptTransition) (domain.AttemptRecord, error) {
	if nilIngestionSourceReadyDependency(r.unitOfWork) || nilIngestionSourceReadyDependency(r.sourceReady) {
		return domain.AttemptRecord{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_SOURCE_READY_UNAVAILABLE", true, errors.New("source-ready transaction dependencies are unavailable"))
	}
	var result domain.AttemptRecord
	callbackSucceeded := false
	err := r.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return foundation.NewError(foundation.ErrorDependencyUnavailable, "INGESTION_TRANSACTION_UNAVAILABLE", true, err)
		}
		transaction = transaction.WithContext(callbackCtx)
		result, err = gormTransitionAttempt(callbackCtx, transaction, transition)
		if err != nil {
			return err
		}
		if result.Status == domain.AttemptChunked && result.SecurityStatus == domain.SecurityPassed && result.ParseProjectionID != nil {
			ready, err := gormLoadSourceReady(callbackCtx, transaction, result)
			if err != nil {
				return err
			}
			if err := r.sourceReady.AppendSourceReadyScoped(callbackCtx, scope, ready); err != nil {
				return err
			}
		}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		code := "INGESTION_ATTEMPT_TRANSITION_FAILED"
		if callbackSucceeded {
			code = "INGESTION_ATTEMPT_TRANSITION_COMMIT_FAILED"
		}
		return domain.AttemptRecord{}, gormClassify(ctx, err, code)
	}
	return result, nil
}

// All selectors originate from the just-updated Attempt. The source-version
// provenance and immutable parser contract are verified in this same scope.
func gormLoadSourceReady(ctx context.Context, transaction *gorm.DB, attempt domain.AttemptRecord) (domain.SourceReady, error) {
	row, err := gormRawRow(transaction, `SELECT
		attempt.workspace_id::text, source.id::text, version.id::text,
		artifact.id::text, projection.id::text, artifact.content_hash,
		attempt.id::text, attempt.completed_at
		FROM ingestion.attempt AS attempt
		JOIN core.source_version AS version
		  ON version.id=attempt.source_version_id AND version.workspace_id=attempt.workspace_id
		JOIN core.source AS source
		  ON source.id=version.source_id AND source.workspace_id=attempt.workspace_id
		JOIN core.content_artifact AS artifact
		  ON artifact.id=version.content_artifact_id AND artifact.workspace_id=attempt.workspace_id
		 AND artifact.content_hash=version.content_hash AND artifact.byte_size=version.byte_size
		JOIN ingestion.parse_projection AS projection
		  ON projection.id=attempt.parse_projection_id AND projection.workspace_id=attempt.workspace_id
		 AND projection.content_artifact_id=artifact.id
		 AND projection.parser_id=attempt.parser_id AND projection.parser_version=attempt.parser_version
		 AND projection.parser_config_hash=attempt.parser_config_hash AND projection.schema_version=attempt.schema_version
		JOIN ingestion.source_version_projection AS provenance
		  ON provenance.source_version_id=version.id AND provenance.parse_projection_id=projection.id
		 AND provenance.workspace_id=attempt.workspace_id
		WHERE attempt.id=? AND attempt.workspace_id=? AND attempt.source_version_id=?
		  AND attempt.parse_projection_id=? AND attempt.status=? AND attempt.security_status=?`,
		string(attempt.ID), string(attempt.WorkspaceID), string(attempt.SourceVersionID), optionalID(attempt.ParseProjectionID),
		string(domain.AttemptChunked), string(domain.SecurityPassed))
	if err != nil {
		return domain.SourceReady{}, gormClassify(ctx, err, "INGESTION_SOURCE_READY_QUERY_FAILED")
	}
	var ready domain.SourceReady
	err = row.Scan(&ready.WorkspaceID, &ready.SourceID, &ready.SourceVersionID,
		&ready.ContentArtifactID, &ready.ParseProjectionID, &ready.ContentHash,
		&ready.IngestionAttemptID, &ready.OccurredAt)
	if gormNoRows(err) {
		return domain.SourceReady{}, foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_SOURCE_READY_BINDING_INVALID", false, errors.New("completed attempt source provenance is unavailable"))
	}
	if err != nil {
		return domain.SourceReady{}, gormClassify(ctx, err, "INGESTION_SOURCE_READY_QUERY_FAILED")
	}
	if err := ready.Validate(); err != nil {
		return domain.SourceReady{}, foundation.NewError(foundation.ErrorConsistencyViolation, "INGESTION_SOURCE_READY_BINDING_INVALID", false, err)
	}
	return ready, nil
}

func nilIngestionSourceReadyDependency(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}
