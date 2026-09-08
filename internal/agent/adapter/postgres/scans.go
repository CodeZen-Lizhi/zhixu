package postgres

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type rowScanner interface{ Scan(...any) error }

func scanModelRun(row rowScanner) (domain.ModelRun, error) {
	var run domain.ModelRun
	var id, workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
	var adapterName, adapterVersion, modelID, modelVersion string
	var profileID, profileVersion, promptID, promptVersion, schemaID, schemaVersion string
	var reducedSchemaID, reducedSchemaVersion string
	var indexVersionID, embeddingVersionID, rerankVersion, finalResultType, errorCode *string
	var memorySnapshotID, memorySchemaVersion, memoryDigest *string
	var memoryItemCount *int
	var memoryBytes *int64
	var status string
	if err := row.Scan(
		&id, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID,
		&run.ModelSettingsRevision,
		&adapterName, &adapterVersion, &modelID, &modelVersion,
		&profileID, &profileVersion, &promptID, &promptVersion, &schemaID, &schemaVersion,
		&reducedSchemaID, &reducedSchemaVersion,
		&indexVersionID, &embeddingVersionID, &rerankVersion,
		&memorySnapshotID, &memorySchemaVersion, &memoryDigest, &memoryItemCount, &memoryBytes,
		&status, &finalResultType, &errorCode,
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
	if indexVersionID != nil {
		run.Retrieval.IndexVersionID = foundation.ID(*indexVersionID)
	}
	if embeddingVersionID != nil {
		value := foundation.ID(*embeddingVersionID)
		run.Retrieval.EmbeddingVersionID = &value
	}
	if rerankVersion != nil {
		run.Retrieval.RerankModelVersion = *rerankVersion
	}
	if memorySnapshotID != nil {
		run.MemoryContext.SnapshotID = foundation.ID(*memorySnapshotID)
	}
	if memorySchemaVersion != nil {
		run.MemoryContext.SchemaVersion = *memorySchemaVersion
	}
	if memoryDigest != nil {
		run.MemoryContext.Digest = *memoryDigest
	}
	if memoryItemCount != nil {
		run.MemoryContext.ItemCount = *memoryItemCount
	}
	if memoryBytes != nil {
		run.MemoryContext.ByteCount = *memoryBytes
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

func sameModelRunBinding(left, right domain.ModelRun) bool {
	return left.WorkspaceID == right.WorkspaceID && left.WorkflowRunID == right.WorkflowRunID && left.NodeRunID == right.NodeRunID &&
		left.NodeAttemptID == right.NodeAttemptID && sameOptionalInt64(left.ModelSettingsRevision, right.ModelSettingsRevision) &&
		left.Model == right.Model && left.Profile == right.Profile && left.Prompt == right.Prompt &&
		left.Schema == right.Schema && left.ReducedSchema == right.ReducedSchema && left.MemoryContext == right.MemoryContext &&
		sameRetrieval(left.Retrieval, right.Retrieval) && left.CreatedAt.Equal(right.CreatedAt)
}

func sameModelRunCreateBinding(existing, requested domain.ModelRun) bool {
	if !sameModelRunBindingWithoutRetrieval(existing, requested) {
		return false
	}
	return sameRetrieval(existing.Retrieval, requested.Retrieval) ||
		(!requested.Retrieval.IsBound() && requested.Schema.ID == domain.RAGAnswerSchemaID)
}

func sameModelRunBindingWithoutRetrieval(left, right domain.ModelRun) bool {
	return left.WorkspaceID == right.WorkspaceID && left.WorkflowRunID == right.WorkflowRunID && left.NodeRunID == right.NodeRunID &&
		left.NodeAttemptID == right.NodeAttemptID && sameOptionalInt64(left.ModelSettingsRevision, right.ModelSettingsRevision) &&
		left.Model == right.Model && left.Profile == right.Profile && left.Prompt == right.Prompt &&
		left.Schema == right.Schema && left.ReducedSchema == right.ReducedSchema && left.MemoryContext == right.MemoryContext &&
		left.CreatedAt.Equal(right.CreatedAt)
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

func optionalFoundationID(value foundation.ID) any {
	if value == "" {
		return nil
	}
	return string(value)
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

func optionalInt(bound bool, value int) any {
	if !bound {
		return nil
	}
	return value
}

func optionalInt64(bound bool, value int64) any {
	if !bound {
		return nil
	}
	return value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
