package postgres

import (
	"context"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// MarkStaleModelCallsUnknown 有界归约 crash 后遗留的 STARTED 调用。
func (r *Repository) MarkStaleModelCallsUnknown(ctx context.Context, query application.UnknownRecoveryQuery) ([]domain.ModelCall, error) {
	if err := validateRecoveryQuery(query); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		WITH candidates AS (
			SELECT call.id
			FROM agent.model_call call
			JOIN agent.model_run run ON run.id=call.model_run_id
			WHERE call.status='STARTED' AND run.status='RUNNING' AND call.started_at < $1
			ORDER BY call.started_at,call.id
			FOR UPDATE OF call SKIP LOCKED
			LIMIT $2
		)
		UPDATE agent.model_call call SET
			status='UNKNOWN',error_code=$3,version=call.version+1,completed_at=$4
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
		return nil, classify(err)
	}
	defer rows.Close()
	result := make([]domain.ModelCall, 0, query.Limit)
	for rows.Next() {
		call, err := scanModelCall(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, call)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
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
func (r *Repository) MarkStaleModelRunsUnknown(ctx context.Context, query application.UnknownRecoveryQuery) ([]domain.ModelRun, error) {
	if err := validateRecoveryQuery(query); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		WITH candidates AS (
			SELECT run.id
			FROM agent.model_run run
			WHERE run.status='RUNNING' AND run.updated_at < $1
			  AND NOT EXISTS (
				SELECT 1 FROM agent.model_call call
				WHERE call.model_run_id=run.id AND call.status='STARTED'
			  )
			ORDER BY run.updated_at,run.id
			FOR UPDATE OF run SKIP LOCKED
			LIMIT $2
		)
		UPDATE agent.model_run run SET
			status='UNKNOWN',final_result_type=NULL,error_code=$3,version=run.version+1,
			updated_at=$4,completed_at=$4
		FROM candidates
		WHERE run.id=candidates.id
		RETURNING run.id::text,run.workspace_id::text,run.workflow_run_id::text,run.node_run_id::text,run.node_attempt_id::text,
			run.adapter_name,run.adapter_version,run.model_id,run.model_version,run.profile_id,run.profile_version,
			run.prompt_template_id,run.prompt_template_version,run.output_schema_id,run.output_schema_version,
			run.reduced_schema_id,run.reduced_schema_version,
			run.retrieval_index_version_id::text,run.embedding_version_id::text,run.rerank_model_version,
			run.status,run.final_result_type,run.error_code,run.version,run.started_at,run.updated_at,run.completed_at`,
		query.Before.UTC(), query.Limit, ErrorCodeModelRunResultUnknown, query.At.UTC())
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	result := make([]domain.ModelRun, 0, query.Limit)
	for rows.Next() {
		run, err := scanModelRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].UpdatedAt.Before(result[right].UpdatedAt)
	})
	return result, nil
}

func validateRecoveryQuery(query application.UnknownRecoveryQuery) error {
	if query.Before.IsZero() || query.At.IsZero() || query.At.Before(query.Before) || query.Limit <= 0 || query.Limit > 500 {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeModelRunInvalid, false, errors.New("agent recovery query is invalid"))
	}
	return nil
}
