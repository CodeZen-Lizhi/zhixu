package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

type rowScanner interface{ Scan(...any) error }
type rowQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}
type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanModelRun(row rowScanner) (domain.ModelRun, error) {
	var run domain.ModelRun
	var id, workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
	var adapterName, adapterVersion, modelID, modelVersion string
	var profileID, profileVersion, promptID, promptVersion, schemaID, schemaVersion string
	var reducedSchemaID, reducedSchemaVersion string
	var indexVersionID string
	var embeddingVersionID, rerankVersion, finalResultType, errorCode *string
	var status string
	if err := row.Scan(
		&id, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID,
		&adapterName, &adapterVersion, &modelID, &modelVersion,
		&profileID, &profileVersion, &promptID, &promptVersion, &schemaID, &schemaVersion,
		&reducedSchemaID, &reducedSchemaVersion,
		&indexVersionID, &embeddingVersionID, &rerankVersion, &status, &finalResultType, &errorCode,
		&run.Version, &run.CreatedAt, &run.UpdatedAt, &run.CompletedAt,
	); err != nil {
		return domain.ModelRun{}, err
	}
	run.ID, run.WorkspaceID = foundation.ID(id), foundation.ID(workspaceID)
	run.WorkflowRunID, run.NodeRunID, run.NodeAttemptID = foundation.ID(workflowRunID), foundation.ID(nodeRunID), foundation.ID(nodeAttemptID)
	run.Model = domain.ModelRef{AdapterName: adapterName, AdapterVersion: adapterVersion, ModelID: modelID, ModelVersion: modelVersion}
	run.Profile = domain.ModelProfileRef{ID: profileID, Version: profileVersion}
	run.Prompt = domain.PromptRef{ID: promptID, Version: promptVersion}
	run.Schema = domain.SchemaRef{ID: schemaID, Version: schemaVersion}
	run.ReducedSchema = domain.SchemaRef{ID: reducedSchemaID, Version: reducedSchemaVersion}
	run.Retrieval = domain.RetrievalRef{IndexVersionID: foundation.ID(indexVersionID)}
	if embeddingVersionID != nil {
		value := foundation.ID(*embeddingVersionID)
		run.Retrieval.EmbeddingVersionID = &value
	}
	if rerankVersion != nil {
		run.Retrieval.RerankModelVersion = *rerankVersion
	}
	run.Status = domain.ModelRunStatus(status)
	if finalResultType != nil {
		run.FinalResultType = *finalResultType
	}
	if errorCode != nil {
		run.FinalErrorCode = *errorCode
	}
	if err := domain.ValidateModelRun(run); err != nil {
		return domain.ModelRun{}, consistency(err)
	}
	return run, nil
}

func scanModelCall(row rowScanner) (domain.ModelCall, error) {
	var call domain.ModelCall
	var id, modelRunID, phase, status string
	var adapterName, adapterVersion, modelID, modelVersion string
	var profileID, profileVersion, promptID, promptVersion, schemaID, schemaVersion string
	var responseHash, errorCode *string
	if err := row.Scan(
		&id, &modelRunID, &call.CallNo, &phase,
		&adapterName, &adapterVersion, &modelID, &modelVersion,
		&profileID, &profileVersion, &promptID, &promptVersion, &schemaID, &schemaVersion, &call.MaxOutputTokens,
		&call.RequestHash, &responseHash,
		&call.RequestBytes, &call.ResponseBytes, &call.Usage.InputTokens, &call.Usage.OutputTokens,
		&call.LatencyMillis, &status, &errorCode, &call.Version, &call.StartedAt, &call.CompletedAt,
	); err != nil {
		return domain.ModelCall{}, err
	}
	call.ID, call.ModelRunID = foundation.ID(id), foundation.ID(modelRunID)
	call.Phase, call.Status = domain.ModelCallPhase(phase), domain.ModelCallStatus(status)
	call.Model = domain.ModelRef{AdapterName: adapterName, AdapterVersion: adapterVersion, ModelID: modelID, ModelVersion: modelVersion}
	call.Profile = domain.ModelProfileRef{ID: profileID, Version: profileVersion}
	call.Prompt = domain.PromptRef{ID: promptID, Version: promptVersion}
	call.Schema = domain.SchemaRef{ID: schemaID, Version: schemaVersion}
	call.Usage.TotalTokens = call.Usage.InputTokens + call.Usage.OutputTokens
	if responseHash != nil {
		call.ResponseHash = *responseHash
	}
	if errorCode != nil {
		call.ErrorCode = *errorCode
	}
	if err := domain.ValidateModelCall(call); err != nil {
		return domain.ModelCall{}, consistency(err)
	}
	return call, nil
}

func loadModelRunByAttempt(ctx context.Context, queryer queryRower, attemptID foundation.ID) (domain.ModelRun, error) {
	return scanModelRun(queryer.QueryRow(ctx, modelRunSelect+` WHERE node_attempt_id=$1`, string(attemptID)))
}

func loadModelRunByID(ctx context.Context, queryer queryRower, workspaceID, runID foundation.ID) (domain.ModelRun, error) {
	return scanModelRun(queryer.QueryRow(ctx, modelRunSelect+` WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(runID)))
}

func loadModelCalls(ctx context.Context, queryer rowQueryer, workspaceID, runID foundation.ID) ([]domain.ModelCall, error) {
	rows, err := queryer.Query(ctx, modelCallSelect+`
		JOIN agent.model_run run ON run.id=call.model_run_id
		WHERE run.workspace_id=$1 AND call.model_run_id=$2
		ORDER BY call.call_no,call.id`, string(workspaceID), string(runID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.ModelCall, 0)
	previous := 0
	for rows.Next() {
		call, err := scanModelCall(rows)
		if err != nil {
			return nil, err
		}
		if call.CallNo <= previous {
			return nil, consistency(errors.New("model calls are not stably ordered"))
		}
		previous = call.CallNo
		result = append(result, call)
	}
	return result, rows.Err()
}

const modelRunSelect = `
	SELECT id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,
		retrieval_index_version_id::text,embedding_version_id::text,rerank_model_version,
		status,final_result_type,error_code,version,started_at,updated_at,completed_at
	FROM agent.model_run`

const modelCallSelect = `
	SELECT call.id::text,call.model_run_id::text,call.call_no,call.phase,
		call.adapter_name,call.adapter_version,call.model_id,call.model_version,
		call.profile_id,call.profile_version,call.prompt_template_id,call.prompt_template_version,
		call.output_schema_id,call.output_schema_version,call.max_output_tokens,
		call.request_hash,call.response_hash,
		call.request_bytes,call.response_bytes,call.input_tokens,call.output_tokens,call.latency_ms,
		call.status,call.error_code,call.version,call.started_at,call.completed_at
	FROM agent.model_call call `

func sameModelRunBinding(left, right domain.ModelRun) bool {
	return left.WorkspaceID == right.WorkspaceID && left.WorkflowRunID == right.WorkflowRunID && left.NodeRunID == right.NodeRunID &&
		left.NodeAttemptID == right.NodeAttemptID && left.Model == right.Model && left.Profile == right.Profile && left.Prompt == right.Prompt &&
		left.Schema == right.Schema && left.ReducedSchema == right.ReducedSchema &&
		sameRetrieval(left.Retrieval, right.Retrieval) && left.CreatedAt.Equal(right.CreatedAt)
}

func sameRetrieval(left, right domain.RetrievalRef) bool {
	return left.IndexVersionID == right.IndexVersionID && optionalID(left.EmbeddingVersionID) == optionalID(right.EmbeddingVersionID) &&
		left.RerankModelVersion == right.RerankModelVersion
}

func sameModelCallBinding(left, right domain.ModelCall) bool {
	return left.ModelRunID == right.ModelRunID && left.CallNo == right.CallNo && left.Phase == right.Phase &&
		left.Model == right.Model && left.Profile == right.Profile && left.Prompt == right.Prompt && left.Schema == right.Schema &&
		left.MaxOutputTokens == right.MaxOutputTokens &&
		left.RequestHash == right.RequestHash && left.RequestBytes == right.RequestBytes && left.StartedAt.Equal(right.StartedAt)
}

func sameModelCallResult(left, right domain.ModelCall) bool {
	return sameModelCallBinding(left, right) && left.Status == right.Status && left.ResponseHash == right.ResponseHash &&
		left.ResponseBytes == right.ResponseBytes && left.Usage == right.Usage && left.LatencyMillis == right.LatencyMillis &&
		left.ErrorCode == right.ErrorCode && left.Version == right.Version && sameTime(left.CompletedAt, right.CompletedAt)
}

func sameModelRunResult(left, right domain.ModelRun) bool {
	return sameModelRunBinding(left, right) && left.Status == right.Status && left.FinalResultType == right.FinalResultType &&
		left.FinalErrorCode == right.FinalErrorCode && left.Version == right.Version && left.UpdatedAt.Equal(right.UpdatedAt) &&
		sameTime(left.CompletedAt, right.CompletedAt)
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func optionalID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func sameTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
