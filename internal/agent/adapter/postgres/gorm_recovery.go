package postgres

import (
	"context"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

// MarkStaleModelCallsUnknown 有界归约 crash 后遗留的 STARTED 调用。
func (repository *GORMRepository) MarkStaleModelCallsUnknown(ctx context.Context, query application.UnknownRecoveryQuery) (result []domain.ModelCall, err error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if err := validateRecoveryQuery(query); err != nil {
		return nil, err
	}
	rows, err := gormRawRows(repository.database.WithContext(ctx), `
		WITH candidates AS (
			SELECT call.id
			FROM agent.model_call call
			JOIN agent.model_run run ON run.id=call.model_run_id
			WHERE call.status='STARTED' AND run.status='RUNNING' AND call.started_at < ?
			ORDER BY call.started_at,call.id
			FOR UPDATE OF call SKIP LOCKED
			LIMIT ?
		)
		UPDATE agent.model_call call SET
			status='UNKNOWN',error_code=?,version=call.version+1,completed_at=?
		FROM candidates
		WHERE call.id=candidates.id
		RETURNING call.id::text,call.model_run_id::text,call.call_no,call.phase,
			call.adapter_name,call.adapter_version,call.model_id,call.model_version,
			call.profile_id,call.profile_version,call.prompt_template_id,call.prompt_template_version,
			call.output_schema_id,call.output_schema_version,call.max_output_tokens,
			call.request_hash,call.response_hash,
			call.request_bytes,call.response_bytes,call.input_tokens,call.output_tokens,call.latency_ms,
			call.status,call.error_code,call.version,call.started_at,call.completed_at`,
		query.Before.UTC(), query.Limit, ErrorCodeModelCallResultUnknown, query.At.UTC())
	if err != nil {
		return nil, classifyGORM(ctx, err)
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			err = classifyGORM(ctx, closeErr)
		}
	}()

	result = make([]domain.ModelCall, 0, query.Limit)
	for rows.Next() {
		call, scanErr := scanModelCall(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, call)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, classifyGORM(ctx, rowsErr)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].StartedAt.Equal(result[right].StartedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].StartedAt.Before(result[right].StartedAt)
	})
	return result, nil
}

// MarkStaleModelRunsUnknown 有界归约不再包含活动调用的 RUNNING Model Run。
func (repository *GORMRepository) MarkStaleModelRunsUnknown(ctx context.Context, query application.UnknownRecoveryQuery) (result []domain.ModelRun, err error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if err := validateRecoveryQuery(query); err != nil {
		return nil, err
	}
	rows, err := gormRawRows(repository.database.WithContext(ctx), `
		WITH candidates AS (
			SELECT run.id
			FROM agent.model_run run
			WHERE run.status='RUNNING' AND run.updated_at < ?
			  AND NOT EXISTS (
				SELECT 1 FROM agent.model_call call
				WHERE call.model_run_id=run.id AND call.status='STARTED'
			  )
			ORDER BY run.updated_at,run.id
			FOR UPDATE OF run SKIP LOCKED
			LIMIT ?
		)
		UPDATE agent.model_run run SET
			status='UNKNOWN',final_result_type=NULL,error_code=?,version=run.version+1,
			updated_at=?,completed_at=?
		FROM candidates
		WHERE run.id=candidates.id
		RETURNING run.id::text,run.workspace_id::text,run.workflow_run_id::text,run.node_run_id::text,run.node_attempt_id::text,
			run.model_settings_revision,
			run.adapter_name,run.adapter_version,run.model_id,run.model_version,run.profile_id,run.profile_version,
			run.prompt_template_id,run.prompt_template_version,run.output_schema_id,run.output_schema_version,
			run.reduced_schema_id,run.reduced_schema_version,
			run.retrieval_index_version_id::text,run.embedding_version_id::text,run.rerank_model_version,
			run.memory_snapshot_id::text,run.memory_context_schema_version,run.memory_context_digest,
			run.memory_context_item_count,run.memory_context_bytes,
			run.status,run.final_result_type,run.error_code,run.version,run.started_at,run.updated_at,run.completed_at`,
		query.Before.UTC(), query.Limit, ErrorCodeModelRunResultUnknown, query.At.UTC(), query.At.UTC())
	if err != nil {
		return nil, classifyGORM(ctx, err)
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			err = classifyGORM(ctx, closeErr)
		}
	}()

	result = make([]domain.ModelRun, 0, query.Limit)
	for rows.Next() {
		run, scanErr := scanModelRun(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, run)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, classifyGORM(ctx, rowsErr)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].UpdatedAt.Before(result[right].UpdatedAt)
	})
	return result, nil
}

var _ application.ModelRunRepository = (*GORMRepository)(nil)
