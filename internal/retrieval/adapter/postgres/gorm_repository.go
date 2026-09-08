package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"gorm.io/gorm"
)

const gormRetrievalBuildStatusSQL = `SELECT i.workspace_id::text,i.id::text,i.status,i.version,i.expected_chunk_count,
	i.source_manifest_hash,i.expected_source_count,i.degraded_capabilities,
	(SELECT count(*) FROM retrieval.index_manifest_chunk m WHERE m.index_version_id=i.id),
	(SELECT count(*) FROM retrieval.index_manifest_source s WHERE s.index_version_id=i.id),
	(SELECT count(*) FROM retrieval.index_manifest_source s WHERE s.index_version_id=i.id AND s.selection_status='included'),
	(SELECT count(*) FROM retrieval.index_manifest_source s WHERE s.index_version_id=i.id AND s.selection_status='excluded'),
	count(p.index_version_id),
	count(*) FILTER(WHERE p.lexical_status='pending'),count(*) FILTER(WHERE p.lexical_status='ready'),count(*) FILTER(WHERE p.lexical_status='failed'),
	count(*) FILTER(WHERE p.vector_status='disabled'),count(*) FILTER(WHERE p.vector_status='pending'),count(*) FILTER(WHERE p.vector_status='ready'),
	count(*) FILTER(WHERE p.vector_status='skipped_oversized'),count(*) FILTER(WHERE p.vector_status='failed')
	FROM retrieval.index_version i LEFT JOIN retrieval.chunk_projection p ON p.index_version_id=i.id
	WHERE i.workspace_id=? AND i.id=? GROUP BY i.id`

// RegisterEmbeddingVersion precisely replays an immutable embedding contract.
func (repository *GORMRepository) RegisterEmbeddingVersion(ctx context.Context, value domain.EmbeddingVersion) (domain.EmbeddingVersionResult, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.EmbeddingVersionResult{}, err
	}
	if err := domain.ValidateEmbeddingVersion(value); err != nil {
		return domain.EmbeddingVersionResult{}, err
	}

	byID, idErr := gormRetrievalLoadEmbedding(ctx, repository.database, value.ID)
	if idErr == nil {
		if !domain.SameEmbeddingBinding(byID, value) {
			return domain.EmbeddingVersionResult{}, consistency("RETRIEVAL_EMBEDDING_IDENTITY_CONFLICT", errors.New("embedding id is bound to a different contract"))
		}
		return domain.EmbeddingVersionResult{EmbeddingVersion: byID, Replayed: true}, nil
	}
	if !gormRetrievalNoRows(idErr) {
		return domain.EmbeddingVersionResult{}, classifyGORMRetrieval(ctx, idErr, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
	}

	row, err := gormRetrievalRawRow(ctx, repository.database, `INSERT INTO retrieval.embedding_version
		(id,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,model_settings_revision,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING RETURNING `+embeddingColumns,
		string(value.ID), value.Provider, value.AdapterName, value.AdapterVersion, value.Model, value.Dimensions,
		string(value.Normalization), string(value.DistanceMetric), value.ConfigHash,
		gormRetrievalNullableInt64(value.ModelSettingsRevision), value.CreatedAt.UTC())
	if err != nil {
		return domain.EmbeddingVersionResult{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_EMBEDDING_CREATE_FAILED")
	}
	persisted, err := scanGORMRetrievalEmbedding(row)
	if err == nil {
		return domain.EmbeddingVersionResult{EmbeddingVersion: persisted, Created: true}, nil
	}
	if !gormRetrievalNoRows(err) {
		return domain.EmbeddingVersionResult{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_EMBEDDING_CREATE_FAILED")
	}

	persisted, err = gormRetrievalLoadEmbeddingByContract(ctx, repository.database, value)
	if gormRetrievalNoRows(err) {
		byID, idErr = gormRetrievalLoadEmbedding(ctx, repository.database, value.ID)
		if idErr != nil {
			return domain.EmbeddingVersionResult{}, classifyGORMRetrieval(ctx, idErr, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
		}
		if !domain.SameEmbeddingBinding(byID, value) {
			return domain.EmbeddingVersionResult{}, consistency("RETRIEVAL_EMBEDDING_IDENTITY_CONFLICT", errors.New("embedding id is bound to a different contract"))
		}
		return domain.EmbeddingVersionResult{EmbeddingVersion: byID, Replayed: true}, nil
	}
	if err != nil {
		return domain.EmbeddingVersionResult{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
	}
	if !domain.SameEmbeddingBinding(persisted, value) {
		return domain.EmbeddingVersionResult{}, conflict("RETRIEVAL_EMBEDDING_IDEMPOTENCY_CONFLICT", errors.New("embedding identity is bound to different adapter settings"))
	}
	if persisted.ID != value.ID {
		byID, idErr = gormRetrievalLoadEmbedding(ctx, repository.database, value.ID)
		if idErr == nil {
			if !domain.SameEmbeddingBinding(byID, value) {
				return domain.EmbeddingVersionResult{}, consistency("RETRIEVAL_EMBEDDING_IDENTITY_CONFLICT", errors.New("embedding id is bound to a different contract"))
			}
			return domain.EmbeddingVersionResult{EmbeddingVersion: byID, Replayed: true}, nil
		}
		if !gormRetrievalNoRows(idErr) {
			return domain.EmbeddingVersionResult{}, classifyGORMRetrieval(ctx, idErr, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
		}
	}
	return domain.EmbeddingVersionResult{EmbeddingVersion: persisted, Replayed: true}, nil
}

// GetEmbeddingVersion returns one immutable embedding version.
func (repository *GORMRepository) GetEmbeddingVersion(ctx context.Context, id foundation.ID) (domain.EmbeddingVersion, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.EmbeddingVersion{}, err
	}
	value, err := gormRetrievalLoadEmbedding(ctx, repository.database, id)
	if gormRetrievalNoRows(err) {
		return domain.EmbeddingVersion{}, notFound("RETRIEVAL_EMBEDDING_NOT_FOUND", err)
	}
	if err != nil {
		return domain.EmbeddingVersion{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_EMBEDDING_QUERY_FAILED")
	}
	return value, nil
}

// BuildLexical creates the complete lexical projection from the frozen manifest.
func (repository *GORMRepository) BuildLexical(ctx context.Context, command domain.LexicalBuildCommand) (result domain.ProjectionBatchResult, err error) {
	err = repository.within(ctx, foundation.TransactionOptions{}, "RETRIEVAL_LEXICAL_TRANSACTION_FAILED", "RETRIEVAL_LEXICAL_COMMIT_FAILED",
		func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
			index, queryErr := gormRetrievalLoadIndex(callbackCtx, transaction, command.WorkspaceID, command.IndexVersionID, true)
			if gormRetrievalNoRows(queryErr) {
				return notFound("RETRIEVAL_INDEX_NOT_FOUND", queryErr)
			}
			if queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "RETRIEVAL_INDEX_QUERY_FAILED")
			}
			if validateErr := domain.ValidateLexicalBuildCommand(index, command); validateErr != nil {
				return validateErr
			}
			inserted, execErr := gormRetrievalExec(callbackCtx, transaction, `WITH lexical AS (
				SELECT m.index_version_id,m.chunk_id,m.workspace_id,i.embedding_version_id,
				setweight(to_tsvector('simple',coalesce(c.heading_path::text,'')),'A') || setweight(to_tsvector('simple',c.content),'B') AS search_vector
				FROM retrieval.index_manifest_chunk m JOIN retrieval.index_version i ON i.id=m.index_version_id
				JOIN ingestion.canonical_chunk c ON c.id=m.chunk_id AND c.workspace_id=m.workspace_id
				WHERE m.index_version_id=? AND m.workspace_id=? AND c.content_hash=m.content_hash
				  AND c.sequence=m.sequence AND c.parser_version=m.parser_version
				  AND c.chunk_strategy_version=m.chunk_strategy_version AND c.schema_version=m.schema_version
				  AND NOT EXISTS(SELECT 1 FROM retrieval.chunk_projection p WHERE p.index_version_id=m.index_version_id AND p.chunk_id=m.chunk_id)
			)
				INSERT INTO retrieval.chunk_projection
				(index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,embedding,token_count,lexical_status,vector_status,failure_code,created_at,updated_at)
				SELECT index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,NULL,length(search_vector),
				'ready',CASE WHEN embedding_version_id IS NULL THEN 'disabled' ELSE 'pending' END,NULL,?,? FROM lexical
				ON CONFLICT(index_version_id,chunk_id) DO NOTHING`, string(command.IndexVersionID), string(command.WorkspaceID), command.At.UTC(), command.At.UTC())
			if execErr != nil {
				return classifyGORMRetrieval(callbackCtx, execErr, "RETRIEVAL_LEXICAL_BUILD_FAILED")
			}
			var total, exact int64
			embeddingID := gormRetrievalOptionalID(index.EmbeddingVersionID)
			row, rowErr := gormRetrievalRawRow(callbackCtx, transaction, `SELECT count(*),count(*) FILTER(WHERE p.lexical_status='ready' AND p.search_vector=(
				setweight(to_tsvector('simple',coalesce(c.heading_path::text,'')),'A') || setweight(to_tsvector('simple',c.content),'B'))
				AND p.token_count=length(p.search_vector) AND p.embedding_version_id IS NOT DISTINCT FROM ?
				AND ((?::uuid IS NULL AND p.vector_status='disabled') OR (?::uuid IS NOT NULL AND p.vector_status IN ('pending','ready','skipped_oversized','failed'))))
				FROM retrieval.chunk_projection p JOIN retrieval.index_manifest_chunk m ON m.index_version_id=p.index_version_id AND m.chunk_id=p.chunk_id
				JOIN ingestion.canonical_chunk c ON c.id=m.chunk_id AND c.workspace_id=m.workspace_id
				WHERE p.index_version_id=? AND p.workspace_id=?`, embeddingID, embeddingID, embeddingID,
				string(command.IndexVersionID), string(command.WorkspaceID))
			if rowErr != nil {
				return classifyGORMRetrieval(callbackCtx, rowErr, "RETRIEVAL_LEXICAL_VERIFY_FAILED")
			}
			if scanErr := row.Scan(&total, &exact); scanErr != nil {
				return classifyGORMRetrieval(callbackCtx, scanErr, "RETRIEVAL_LEXICAL_VERIFY_FAILED")
			}
			if total != index.ExpectedChunkCount || exact != index.ExpectedChunkCount {
				return consistency("RETRIEVAL_LEXICAL_INCOMPLETE", errors.New("lexical projection does not exactly cover manifest"))
			}
			result = domain.ProjectionBatchResult{
				IndexVersionID: index.ID, AttemptedCount: index.ExpectedChunkCount,
				InsertedCount: inserted, ReplayedCount: index.ExpectedChunkCount - inserted,
			}
			return nil
		})
	if err != nil {
		return domain.ProjectionBatchResult{}, err
	}
	return result, nil
}

// TransitionIndex applies a non-activation lifecycle transition with a version CAS.
func (repository *GORMRepository) TransitionIndex(ctx context.Context, command domain.IndexTransition) (updated domain.IndexVersion, err error) {
	err = repository.within(ctx, foundation.TransactionOptions{}, "RETRIEVAL_INDEX_TRANSACTION_FAILED", "RETRIEVAL_INDEX_COMMIT_FAILED",
		func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
			current, queryErr := gormRetrievalLoadIndex(callbackCtx, transaction, command.WorkspaceID, command.IndexVersionID, true)
			if gormRetrievalNoRows(queryErr) {
				return notFound("RETRIEVAL_INDEX_NOT_FOUND", queryErr)
			}
			if queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "RETRIEVAL_INDEX_QUERY_FAILED")
			}
			if validateErr := domain.ValidateIndexTransitionCommand(current, command); validateErr != nil {
				return validateErr
			}
			capabilities := command.DegradedCapabilities
			if capabilities == nil {
				capabilities = current.DegradedCapabilities
			}
			degraded, marshalErr := marshalGORMRetrievalCapabilities(capabilities)
			if marshalErr != nil {
				return consistency("RETRIEVAL_DEGRADED_CAPABILITIES_INVALID", marshalErr)
			}
			status := string(command.Status)
			at := command.At.UTC()
			row, rowErr := gormRetrievalRawRow(callbackCtx, transaction, `UPDATE retrieval.index_version
				SET status=?,degraded_capabilities=?::jsonb,failure_code=?,version=version+1,updated_at=?,
				built_at=CASE WHEN ?='ready' THEN ? ELSE built_at END,
				failed_at=CASE WHEN ?='failed' THEN ? ELSE failed_at END,
				retired_at=CASE WHEN ?='retiring' THEN ? ELSE retired_at END,
				archived_at=CASE WHEN ?='archived' THEN ? ELSE archived_at END
				WHERE id=? AND workspace_id=? AND version=? RETURNING `+indexColumns,
				status, degraded, gormRetrievalNullableString(command.FailureCode), at,
				status, at, status, at, status, at, status, at,
				string(command.IndexVersionID), string(command.WorkspaceID), command.ExpectedVersion)
			if rowErr != nil {
				return classifyGORMRetrieval(callbackCtx, rowErr, "RETRIEVAL_INDEX_TRANSITION_FAILED")
			}
			updated, rowErr = scanGORMRetrievalIndex(row)
			if gormRetrievalNoRows(rowErr) {
				return conflict("RETRIEVAL_INDEX_VERSION_CONFLICT", rowErr)
			}
			if rowErr != nil {
				return classifyGORMRetrieval(callbackCtx, rowErr, "RETRIEVAL_INDEX_TRANSITION_FAILED")
			}
			return nil
		})
	if err != nil {
		return domain.IndexVersion{}, err
	}
	return updated, nil
}

// Activate atomically switches the workspace's unique active index.
func (repository *GORMRepository) Activate(ctx context.Context, command domain.ActivationCommand) (domain.ActivationResult, error) {
	return repository.gormActivate(ctx, command, domain.ActivationKindActivate)
}

// RollbackActivate atomically restores one retiring index.
func (repository *GORMRepository) RollbackActivate(ctx context.Context, command domain.RollbackActivationCommand) (domain.ActivationResult, error) {
	converted := domain.ActivationCommand{
		ActivationID: command.ActivationID, WorkspaceID: command.WorkspaceID,
		TargetIndexVersionID: command.TargetIndexVersionID, ExpectedTargetVersion: command.ExpectedTargetVersion,
		ExpectedCurrentIndexVersionID: &command.ExpectedCurrentIndexVersionID, ExpectedCurrentVersion: &command.ExpectedCurrentVersion,
		IdempotencyKey: command.IdempotencyKey, ReasonCode: command.ReasonCode, At: command.At,
	}
	return repository.gormActivate(ctx, converted, domain.ActivationKindRollback)
}

func (repository *GORMRepository) gormActivate(ctx context.Context, command domain.ActivationCommand, kind domain.ActivationKind) (result domain.ActivationResult, err error) {
	err = repository.within(ctx, foundation.TransactionOptions{}, "RETRIEVAL_ACTIVATION_TRANSACTION_FAILED", "RETRIEVAL_ACTIVATION_COMMIT_FAILED",
		func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
			if _, lockErr := gormRetrievalExec(callbackCtx, transaction,
				`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, string(command.WorkspaceID)); lockErr != nil {
				return classifyGORMRetrieval(callbackCtx, lockErr, "RETRIEVAL_ACTIVATION_LOCK_FAILED")
			}
			var activateErr error
			result, activateErr = gormRetrievalActivateTx(callbackCtx, transaction, command, kind, nil)
			return activateErr
		})
	if err != nil {
		return domain.ActivationResult{}, err
	}
	return result, nil
}

// locked 非空时沿用 Completion 已按跨 owner 锁序取得的 Index 行锁。
func gormRetrievalActivateTx(ctx context.Context, transaction *gorm.DB, command domain.ActivationCommand, kind domain.ActivationKind, locked *activationLockedIndexes) (domain.ActivationResult, error) {
	if existing, found, err := gormRetrievalLoadActivation(ctx, transaction, command.WorkspaceID, command.IdempotencyKey); err != nil {
		return domain.ActivationResult{}, err
	} else if found {
		return gormRetrievalReplayActivation(ctx, transaction, command, kind, existing)
	}

	var target domain.IndexVersion
	var current *domain.IndexVersion
	if locked != nil {
		target, current = locked.target, locked.current
	} else {
		var err error
		target, err = gormRetrievalLoadIndex(ctx, transaction, command.WorkspaceID, command.TargetIndexVersionID, true)
		if gormRetrievalNoRows(err) {
			return domain.ActivationResult{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
		}
		if err != nil {
			return domain.ActivationResult{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_INDEX_QUERY_FAILED")
		}
		row, rowErr := gormRetrievalRawRow(ctx, transaction, `SELECT `+indexColumns+`
			FROM retrieval.index_version WHERE workspace_id=? AND status='active' FOR UPDATE`, string(command.WorkspaceID))
		if rowErr != nil {
			return domain.ActivationResult{}, classifyGORMRetrieval(ctx, rowErr, "RETRIEVAL_ACTIVE_QUERY_FAILED")
		}
		active, activeErr := scanGORMRetrievalIndex(row)
		if activeErr == nil {
			current = &active
		} else if !gormRetrievalNoRows(activeErr) {
			return domain.ActivationResult{}, classifyGORMRetrieval(ctx, activeErr, "RETRIEVAL_ACTIVE_QUERY_FAILED")
		}
	}

	if kind == domain.ActivationKindRollback {
		if current == nil {
			return domain.ActivationResult{}, notFound("RETRIEVAL_ACTIVE_NOT_FOUND", errors.New("active index is absent"))
		}
		rollbackCommand := domain.RollbackActivationCommand{
			ActivationID: command.ActivationID, WorkspaceID: command.WorkspaceID,
			TargetIndexVersionID: command.TargetIndexVersionID, ExpectedTargetVersion: command.ExpectedTargetVersion,
			ExpectedCurrentIndexVersionID: *command.ExpectedCurrentIndexVersionID, ExpectedCurrentVersion: *command.ExpectedCurrentVersion,
			IdempotencyKey: command.IdempotencyKey, ReasonCode: command.ReasonCode, At: command.At,
		}
		if err := domain.ValidateRollbackActivationCommand(*current, target, rollbackCommand); err != nil {
			return domain.ActivationResult{}, err
		}
	} else if err := domain.ValidateActivationCommand(current, target, command); err != nil {
		return domain.ActivationResult{}, err
	}

	activation := domain.Activation{
		ID: command.ActivationID, Kind: kind, WorkspaceID: command.WorkspaceID,
		TargetIndexVersionID: command.TargetIndexVersionID, PreviousIndexVersionID: command.ExpectedCurrentIndexVersionID,
		TargetVersion: command.ExpectedTargetVersion + 1, IdempotencyKey: command.IdempotencyKey,
		ReasonCode: command.ReasonCode, CreatedAt: command.At.UTC(),
	}
	var previousNext any
	if command.ExpectedCurrentVersion != nil {
		value := *command.ExpectedCurrentVersion + 1
		activation.PreviousVersion = &value
		previousNext = value
	}
	inserted, err := gormRetrievalExec(ctx, transaction, `INSERT INTO retrieval.index_activation(
		id,kind,workspace_id,target_index_version_id,previous_index_version_id,target_version,previous_version,idempotency_key,reason_code,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, string(command.ActivationID), string(kind), string(command.WorkspaceID),
		string(command.TargetIndexVersionID), gormRetrievalOptionalID(command.ExpectedCurrentIndexVersionID), command.ExpectedTargetVersion+1,
		previousNext, command.IdempotencyKey, command.ReasonCode, command.At.UTC())
	if err != nil {
		return domain.ActivationResult{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_ACTIVATION_CREATE_FAILED")
	}
	if inserted != 1 {
		return domain.ActivationResult{}, consistency("RETRIEVAL_ACTIVATION_CREATE_FAILED", errors.New("activation insert did not affect one row"))
	}

	var previous *domain.IndexVersion
	if current != nil {
		row, rowErr := gormRetrievalRawRow(ctx, transaction, `UPDATE retrieval.index_version
			SET status='retiring',version=version+1,updated_at=?,retired_at=?
			WHERE id=? AND workspace_id=? AND status='active' AND version=? RETURNING `+indexColumns,
			command.At.UTC(), command.At.UTC(), string(current.ID), string(command.WorkspaceID), current.Version)
		if rowErr != nil {
			return domain.ActivationResult{}, classifyGORMRetrieval(ctx, rowErr, "RETRIEVAL_PREVIOUS_RETIRE_FAILED")
		}
		updated, scanErr := scanGORMRetrievalIndex(row)
		if scanErr != nil {
			return domain.ActivationResult{}, classifyGORMRetrieval(ctx, scanErr, "RETRIEVAL_PREVIOUS_RETIRE_FAILED")
		}
		previous = &updated
	}
	row, rowErr := gormRetrievalRawRow(ctx, transaction, `UPDATE retrieval.index_version
		SET status='active',version=version+1,updated_at=?,activated_at=?
		WHERE id=? AND workspace_id=? AND status=? AND version=? RETURNING `+indexColumns,
		command.At.UTC(), command.At.UTC(), string(target.ID), string(command.WorkspaceID), string(target.Status), target.Version)
	if rowErr != nil {
		return domain.ActivationResult{}, classifyGORMRetrieval(ctx, rowErr, "RETRIEVAL_TARGET_ACTIVATE_FAILED")
	}
	updatedTarget, err := scanGORMRetrievalIndex(row)
	if err != nil {
		return domain.ActivationResult{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_TARGET_ACTIVATE_FAILED")
	}
	return domain.ActivationResult{Activation: activation, ActiveIndexVersion: updatedTarget, PreviousIndexVersion: previous}, nil
}

func gormRetrievalReplayActivation(ctx context.Context, transaction *gorm.DB, command domain.ActivationCommand, kind domain.ActivationKind, existing domain.Activation) (domain.ActivationResult, error) {
	expectedTargetNext := command.ExpectedTargetVersion + 1
	var expectedPreviousNext *int64
	if command.ExpectedCurrentVersion != nil {
		value := *command.ExpectedCurrentVersion + 1
		expectedPreviousNext = &value
	}
	if existing.Kind != kind || existing.TargetIndexVersionID != command.TargetIndexVersionID ||
		!gormRetrievalSameOptionalID(existing.PreviousIndexVersionID, command.ExpectedCurrentIndexVersionID) ||
		existing.ReasonCode != command.ReasonCode || existing.TargetVersion != expectedTargetNext ||
		!gormRetrievalSameOptionalInt64(existing.PreviousVersion, expectedPreviousNext) {
		return domain.ActivationResult{}, conflict("RETRIEVAL_ACTIVATION_IDEMPOTENCY_CONFLICT", errors.New("activation idempotency key has a different binding"))
	}
	activeCurrent, err := gormRetrievalLoadIndex(ctx, transaction, command.WorkspaceID, existing.TargetIndexVersionID, false)
	if gormRetrievalNoRows(err) {
		return domain.ActivationResult{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.ActivationResult{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_INDEX_QUERY_FAILED")
	}
	active := activationIndexSnapshot(activeCurrent, domain.IndexStatusActive, existing.TargetVersion, existing.CreatedAt)
	if err := domain.ValidateIndexVersion(active); err != nil {
		return domain.ActivationResult{}, consistency("RETRIEVAL_ACTIVATION_REPLAY_INVALID", err)
	}
	var previous *domain.IndexVersion
	if existing.PreviousIndexVersionID != nil {
		currentPrevious, queryErr := gormRetrievalLoadIndex(ctx, transaction, command.WorkspaceID, *existing.PreviousIndexVersionID, false)
		if gormRetrievalNoRows(queryErr) {
			return domain.ActivationResult{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", queryErr)
		}
		if queryErr != nil {
			return domain.ActivationResult{}, classifyGORMRetrieval(ctx, queryErr, "RETRIEVAL_INDEX_QUERY_FAILED")
		}
		value := activationIndexSnapshot(currentPrevious, domain.IndexStatusRetiring, *existing.PreviousVersion, existing.CreatedAt)
		if err := domain.ValidateIndexVersion(value); err != nil {
			return domain.ActivationResult{}, consistency("RETRIEVAL_ACTIVATION_REPLAY_INVALID", err)
		}
		previous = &value
	}
	return domain.ActivationResult{Activation: existing, ActiveIndexVersion: active, PreviousIndexVersion: previous, Replayed: true}, nil
}

// GetIndex returns one workspace-scoped index version.
func (repository *GORMRepository) GetIndex(ctx context.Context, workspaceID, indexID foundation.ID) (domain.IndexVersion, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.IndexVersion{}, err
	}
	value, err := gormRetrievalLoadIndex(ctx, repository.database, workspaceID, indexID, false)
	if gormRetrievalNoRows(err) {
		return domain.IndexVersion{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.IndexVersion{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_INDEX_QUERY_FAILED")
	}
	return value, nil
}

// GetIndexByIdempotencyKey loads an index build by its workspace key.
func (repository *GORMRepository) GetIndexByIdempotencyKey(ctx context.Context, workspaceID foundation.ID, key string) (domain.IndexVersion, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.IndexVersion{}, err
	}
	row, err := gormRetrievalRawRow(ctx, repository.database, `SELECT `+indexColumns+`
		FROM retrieval.index_version WHERE workspace_id=? AND idempotency_key=?`, string(workspaceID), key)
	if err != nil {
		return domain.IndexVersion{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_INDEX_QUERY_FAILED")
	}
	value, err := scanGORMRetrievalIndex(row)
	if gormRetrievalNoRows(err) {
		return domain.IndexVersion{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.IndexVersion{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_INDEX_QUERY_FAILED")
	}
	return value, nil
}

// GetActive returns only the unique active index without fallback.
func (repository *GORMRepository) GetActive(ctx context.Context, workspaceID foundation.ID) (domain.IndexVersion, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.IndexVersion{}, err
	}
	row, err := gormRetrievalRawRow(ctx, repository.database, `SELECT `+indexColumns+`
		FROM retrieval.index_version WHERE workspace_id=? AND status='active'`, string(workspaceID))
	if err != nil {
		return domain.IndexVersion{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_ACTIVE_QUERY_FAILED")
	}
	value, err := scanGORMRetrievalIndex(row)
	if gormRetrievalNoRows(err) {
		return domain.IndexVersion{}, notFound("RETRIEVAL_ACTIVE_NOT_FOUND", err)
	}
	if err != nil {
		return domain.IndexVersion{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_ACTIVE_QUERY_FAILED")
	}
	return value, nil
}

// GetBuildStatus returns one set-based manifest/projection aggregate.
func (repository *GORMRepository) GetBuildStatus(ctx context.Context, workspaceID, indexID foundation.ID) (domain.BuildStatus, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.BuildStatus{}, err
	}
	row, err := gormRetrievalRawRow(ctx, repository.database, gormRetrievalBuildStatusSQL, string(workspaceID), string(indexID))
	if err != nil {
		return domain.BuildStatus{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_BUILD_STATUS_QUERY_FAILED")
	}
	status, err := scanGORMRetrievalBuildStatus(row, "RETRIEVAL_DEGRADED_CAPABILITIES_INVALID")
	if gormRetrievalNoRows(err) {
		return domain.BuildStatus{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.BuildStatus{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_BUILD_STATUS_QUERY_FAILED")
	}
	return status, nil
}

func gormRetrievalLoadEmbedding(ctx context.Context, database *gorm.DB, id foundation.ID) (domain.EmbeddingVersion, error) {
	row, err := gormRetrievalRawRow(ctx, database, `SELECT `+embeddingColumns+` FROM retrieval.embedding_version WHERE id=?`, string(id))
	if err != nil {
		return domain.EmbeddingVersion{}, err
	}
	return scanGORMRetrievalEmbedding(row)
}

func gormRetrievalLoadEmbeddingByContract(ctx context.Context, database *gorm.DB, value domain.EmbeddingVersion) (domain.EmbeddingVersion, error) {
	row, err := gormRetrievalRawRow(ctx, database, `SELECT `+embeddingColumns+` FROM retrieval.embedding_version
		WHERE provider=? AND model=? AND dimensions=? AND config_hash=? AND model_settings_revision IS NOT DISTINCT FROM ?`,
		value.Provider, value.Model, value.Dimensions, value.ConfigHash, gormRetrievalNullableInt64(value.ModelSettingsRevision))
	if err != nil {
		return domain.EmbeddingVersion{}, err
	}
	return scanGORMRetrievalEmbedding(row)
}

func gormRetrievalLoadIndex(ctx context.Context, database *gorm.DB, workspaceID, indexID foundation.ID, forUpdate bool) (domain.IndexVersion, error) {
	query := `SELECT ` + indexColumns + ` FROM retrieval.index_version WHERE workspace_id=? AND id=?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormRetrievalRawRow(ctx, database, query, string(workspaceID), string(indexID))
	if err != nil {
		return domain.IndexVersion{}, err
	}
	return scanGORMRetrievalIndex(row)
}

func gormRetrievalLoadActivation(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key string) (domain.Activation, bool, error) {
	row, err := gormRetrievalRawRow(ctx, database, `SELECT id::text,kind,workspace_id::text,target_index_version_id::text,
		previous_index_version_id::text,target_version,previous_version,idempotency_key,reason_code,created_at
		FROM retrieval.index_activation WHERE workspace_id=? AND idempotency_key=?`, string(workspaceID), key)
	if err != nil {
		return domain.Activation{}, false, classifyGORMRetrieval(ctx, err, "RETRIEVAL_ACTIVATION_QUERY_FAILED")
	}
	value, err := scanGORMRetrievalActivation(row)
	if gormRetrievalNoRows(err) {
		return domain.Activation{}, false, nil
	}
	if err != nil {
		return domain.Activation{}, false, classifyGORMRetrieval(ctx, err, "RETRIEVAL_ACTIVATION_QUERY_FAILED")
	}
	return value, true, nil
}
