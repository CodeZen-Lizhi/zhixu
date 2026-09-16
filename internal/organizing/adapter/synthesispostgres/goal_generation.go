package synthesispostgres

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// ClaimSynthesisGoalGenerationScoped 在调用方既有事务中锁定一个已完成全部选择的目标，并持锁直到调度器创建处理行，形成持久化领取。来源事件仅用于追溯，绝不消费或重新发布原 source-ready outbox 事件。对来源事件获取非阻塞键共享锁，避免后续外键检查等待已持有 outbox 锁且正在等待工作区锁的来源消费者。
func (store *Store) ClaimSynthesisGoalGenerationScoped(ctx context.Context, scope foundation.TransactionScope, workspaceID foundation.ID) (app.SynthesisGoalGenerationSeed, bool, error) {
	var empty app.SynthesisGoalGenerationSeed
	if !validID(workspaceID) {
		return empty, false, invalid("synthesis goal generation workspace is invalid")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return empty, false, err
	}

	var row goalGenerationSeedModel
	statement := tx.Raw(claimSynthesisGoalGenerationSQL, string(workspaceID)).Scan(&row)
	if statement.Error != nil {
		return empty, false, classify(ctx, statement.Error)
	}
	if statement.RowsAffected == 0 {
		return empty, false, nil
	}

	seed := app.SynthesisGoalGenerationSeed{
		GoalRequestID: foundation.ID(row.GoalRequestID),
		SourceEvent: domain.SynthesisSourceReady{
			ID: foundation.ID(row.SourceEventID),
			Source: domain.SynthesisSourceVersion{
				WorkspaceID:       workspaceID,
				SourceID:          foundation.ID(row.SourceID),
				SourceVersionID:   foundation.ID(row.SourceVersionID),
				ContentArtifactID: foundation.ID(row.ContentArtifactID),
				ParseProjectionID: foundation.ID(row.ParseProjectionID),
				ContentHash:       row.ContentHash,
			},
			IngestionAttemptID: foundation.ID(row.IngestionAttemptID),
			ProcessorVersion:   domain.SynthesisProcessorVersion,
			CreatedAt:          canonical(row.SourceOccurredAt),
		},
	}
	if !validID(seed.GoalRequestID) || seed.SourceEvent.Validate() != nil {
		return empty, false, invalid("synthesis goal generation claim returned an invalid source-ready binding")
	}
	return seed, true, nil
}

type goalGenerationSeedModel struct {
	GoalRequestID, SourceEventID                       string
	SourceID, SourceVersionID, ContentArtifactID       string
	ParseProjectionID, ContentHash, IngestionAttemptID string
	SourceOccurredAt                                   time.Time
}

// claimSynthesisGoalGenerationSQL 先选请求，再选来源事件，以保留稳定请求顺序，并使 SKIP LOCKED 作用于请求行，而非任意选择行或 outbox 行。成功切片可明确为空；只有不可变 bytea 输出中的 selections 数组非空时，该来源才可作为追溯起点。
const claimSynthesisGoalGenerationSQL = `
WITH claimed_goal AS (
    SELECT request.id,request.workspace_id
      FROM organizing.synthesis_goal_request request
     WHERE request.workspace_id=?
       AND request.status='CATALOG_READY'
       AND request.catalog_batches=(
            SELECT count(*)
              FROM organizing.synthesis_goal_catalog_batch batch
              JOIN organizing.synthesis_goal_selection_manifest manifest
                ON manifest.batch_id=batch.id
               AND manifest.workspace_id=batch.workspace_id
               AND manifest.sealed
             WHERE batch.workspace_id=request.workspace_id
               AND batch.request_id=request.id
       )
       AND NOT EXISTS (
            SELECT 1
              FROM organizing.synthesis_goal_catalog_batch batch
              LEFT JOIN organizing.synthesis_goal_selection_manifest manifest
                ON manifest.batch_id=batch.id
               AND manifest.workspace_id=batch.workspace_id
             WHERE batch.workspace_id=request.workspace_id
               AND batch.request_id=request.id
               AND (manifest.batch_id IS NULL OR NOT manifest.sealed)
       )
       AND NOT EXISTS (
            SELECT 1
              FROM organizing.synthesis_goal_selection selection
             WHERE selection.workspace_id=request.workspace_id
               AND selection.request_id=request.id
               AND selection.status<>'SUCCEEDED'
       )
       AND NOT EXISTS (
            SELECT 1
              FROM organizing.synthesis_processing processing
             WHERE processing.workspace_id=request.workspace_id
               AND processing.goal_request_id=request.id
       )
       AND EXISTS (
            SELECT 1
              FROM organizing.synthesis_goal_selection selection
              JOIN organizing.synthesis_goal_catalog_item item
                ON item.batch_id=selection.catalog_batch_id
               AND item.workspace_id=selection.workspace_id
               AND item.ordinal=selection.source_ordinal+1
              JOIN workflow.outbox_event event
                ON event.workspace_id=item.workspace_id
               AND event.run_id IS NULL
               AND event.event_type='ingestion.source.ready'
               AND event.payload->>'source_id'=item.source_id::text
               AND event.payload->>'source_version_id'=item.source_version_id::text
               AND event.payload->>'content_artifact_id'=item.content_artifact_id::text
               AND event.payload->>'parse_projection_id'=item.parse_projection_id::text
               AND event.payload->>'content_hash'=item.content_hash
             WHERE selection.workspace_id=request.workspace_id
               AND selection.request_id=request.id
               AND selection.status='SUCCEEDED'
               AND selection.model_output IS NOT NULL
               AND octet_length(selection.model_output)>0
               AND jsonb_typeof(convert_from(selection.model_output,'UTF8')::jsonb->'selections')='array'
               AND jsonb_array_length(convert_from(selection.model_output,'UTF8')::jsonb->'selections')>0
       )
     ORDER BY request.created_at,request.id
     LIMIT 1
     FOR UPDATE SKIP LOCKED
), selected_seed AS (
    SELECT goal.id AS goal_request_id,
           event.id AS source_event_id,
           item.source_id,
           item.source_version_id,
           item.content_artifact_id,
           item.parse_projection_id,
           item.content_hash,
           event.payload->>'ingestion_attempt_id' AS ingestion_attempt_id,
           event.occurred_at AS source_occurred_at
      FROM claimed_goal goal
      JOIN organizing.synthesis_goal_catalog_batch batch
        ON batch.request_id=goal.id
	   AND batch.workspace_id=goal.workspace_id
      JOIN organizing.synthesis_goal_selection selection
        ON selection.catalog_batch_id=batch.id
       AND selection.workspace_id=batch.workspace_id
       AND selection.request_id=batch.request_id
      JOIN organizing.synthesis_goal_catalog_item item
        ON item.batch_id=selection.catalog_batch_id
       AND item.workspace_id=selection.workspace_id
       AND item.ordinal=selection.source_ordinal+1
      JOIN workflow.outbox_event event
        ON event.workspace_id=item.workspace_id
       AND event.run_id IS NULL
       AND event.event_type='ingestion.source.ready'
       AND event.payload->>'source_id'=item.source_id::text
       AND event.payload->>'source_version_id'=item.source_version_id::text
       AND event.payload->>'content_artifact_id'=item.content_artifact_id::text
       AND event.payload->>'parse_projection_id'=item.parse_projection_id::text
       AND event.payload->>'content_hash'=item.content_hash
     WHERE selection.status='SUCCEEDED'
       AND selection.model_output IS NOT NULL
       AND octet_length(selection.model_output)>0
       AND jsonb_typeof(convert_from(selection.model_output,'UTF8')::jsonb->'selections')='array'
       AND jsonb_array_length(convert_from(selection.model_output,'UTF8')::jsonb->'selections')>0
     ORDER BY batch.batch_no,item.ordinal,selection.point_offset,selection.id,event.occurred_at,event.id
     LIMIT 1
     FOR KEY SHARE OF event SKIP LOCKED
)
SELECT * FROM selected_seed`
