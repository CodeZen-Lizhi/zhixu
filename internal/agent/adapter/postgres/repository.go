package postgres

import "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"

func modelRunArgs(run domain.ModelRun) []any {
	return []any{
		string(run.ID), string(run.WorkspaceID), string(run.WorkflowRunID), string(run.NodeRunID), string(run.NodeAttemptID),
		nullableInt64(run.ModelSettingsRevision),
		run.Model.AdapterName, run.Model.AdapterVersion, run.Model.ModelID, run.Model.ModelVersion,
		run.Profile.ID, run.Profile.Version, run.Prompt.ID, run.Prompt.Version, run.Schema.ID, run.Schema.Version,
		run.ReducedSchema.ID, run.ReducedSchema.Version,
		optionalFoundationID(run.Retrieval.IndexVersionID), optionalID(run.Retrieval.EmbeddingVersionID), optionalText(run.Retrieval.RerankModelVersion),
		optionalFoundationID(run.MemoryContext.SnapshotID), optionalText(run.MemoryContext.SchemaVersion), optionalText(run.MemoryContext.Digest),
		optionalInt(run.MemoryContext.IsBound(), run.MemoryContext.ItemCount), optionalInt64(run.MemoryContext.IsBound(), run.MemoryContext.ByteCount),
		string(run.Status), optionalText(run.FinalResultType), optionalText(run.FinalErrorCode), run.Version,
		run.CreatedAt.UTC(), run.UpdatedAt.UTC(), optionalTime(run.CompletedAt),
	}
}
