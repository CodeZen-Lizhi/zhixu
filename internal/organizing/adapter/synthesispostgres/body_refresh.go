package synthesispostgres

import (
	"context"
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
	"time"
)

func (store *Store) ClaimSynthesisBodyRefreshGenerationScoped(ctx context.Context, scope foundation.TransactionScope, workspaceID foundation.ID) (app.SynthesisBodyRefreshGenerationSeed, bool, error) {
	var empty app.SynthesisBodyRefreshGenerationSeed
	if !validID(workspaceID) {
		return empty, false, invalid("synthesis body refresh generation workspace is invalid")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return empty, false, err
	}

	var row bodyRefreshGenerationSeedModel
	statement := tx.Raw(claimSynthesisBodyRefreshGenerationSQL, string(workspaceID)).Scan(&row)
	if statement.Error != nil {
		return empty, false, classify(ctx, statement.Error)
	}
	if statement.RowsAffected == 0 {
		return empty, false, nil
	}

	seed := app.SynthesisBodyRefreshGenerationSeed{
		BodyRefreshRequestID: foundation.ID(row.BodyRefreshRequestID),
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
	if !validID(seed.BodyRefreshRequestID) || seed.SourceEvent.Validate() != nil {
		return empty, false, invalid("synthesis body refresh generation claim returned an invalid source-ready binding")
	}
	return seed, true, nil
}

type bodyRefreshGenerationSeedModel struct {
	BodyRefreshRequestID, SourceEventID                string
	SourceID, SourceVersionID, ContentArtifactID       string
	ParseProjectionID, ContentHash, IngestionAttemptID string
	SourceOccurredAt                                   time.Time
}

const claimSynthesisBodyRefreshGenerationSQL = `
WITH requested AS (
 SELECT r.id,r.workspace_id,r.impact_id
 FROM organizing.synthesis_body_refresh_request r
 WHERE r.workspace_id=? AND NOT EXISTS (
 SELECT 1 FROM organizing.synthesis_processing p WHERE p.workspace_id=r.workspace_id AND p.body_refresh_request_id=r.id)
 AND EXISTS (SELECT 1 FROM organizing.synthesis_note n JOIN organizing.synthesis_body_impact i
 ON i.note_id=n.id AND i.workspace_id=n.workspace_id AND i.base_revision_id=n.current_revision_id
 WHERE n.id=r.note_id AND n.workspace_id=r.workspace_id AND i.publication_id=r.publication_id)
 AND NOT EXISTS (SELECT 1 FROM organizing.synthesis_note n JOIN organizing.synthesis_pending_body_impact pending
 ON pending.note_id=n.id AND pending.workspace_id=n.workspace_id AND pending.base_revision_id=n.current_revision_id
 WHERE n.id=r.note_id AND n.workspace_id=r.workspace_id AND pending.publication_id=r.publication_id
 AND NOT EXISTS (SELECT 1 FROM organizing.synthesis_body_impact saved WHERE saved.base_revision_id=pending.base_revision_id
 AND saved.item_id=pending.item_id AND saved.publication_id=pending.publication_id))
 ORDER BY r.created_at,r.id LIMIT 1 FOR UPDATE SKIP LOCKED
)
SELECT r.id AS body_refresh_request_id,e.id AS source_event_id,
 e.payload->>'source_id' AS source_id,e.payload->>'source_version_id' AS source_version_id,
 e.payload->>'content_artifact_id' AS content_artifact_id,e.payload->>'parse_projection_id' AS parse_projection_id,
 e.payload->>'content_hash' AS content_hash,e.payload->>'ingestion_attempt_id' AS ingestion_attempt_id,
 e.occurred_at AS source_occurred_at
 FROM requested r JOIN organizing.synthesis_body_impact i ON i.id=r.impact_id AND i.workspace_id=r.workspace_id
 JOIN organizing.synthesis_revision upstream ON upstream.id=i.published_revision_id AND upstream.workspace_id=i.workspace_id
 JOIN workflow.outbox_event e ON e.id=upstream.source_event_id AND e.workspace_id=r.workspace_id
 WHERE e.event_type='ingestion.source.ready' AND e.run_id IS NULL
 FOR KEY SHARE OF e SKIP LOCKED`

func (store *Store) FindSynthesisBodyRefreshProcessingScoped(ctx context.Context, scope foundation.TransactionScope, workspaceID, requestID foundation.ID) (app.SynthesisProcessing, bool, error) {
	if !validID(workspaceID) || !validID(requestID) {
		return app.SynthesisProcessing{}, false, invalid("body refresh identity is invalid")
	}
	tx, err := store.transaction(ctx, scope)
	if err != nil {
		return app.SynthesisProcessing{}, false, err
	}
	var row processingModel
	err = tx.Where("workspace_id=? AND body_refresh_request_id=?", string(workspaceID), string(requestID)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return app.SynthesisProcessing{}, false, nil
	}
	if err != nil {
		return app.SynthesisProcessing{}, false, classify(ctx, err)
	}
	result, err := row.projection()
	return result, err == nil, err
}

func matchesBodyRefresh(id foundation.ID, binding *app.SynthesisBodyRefreshBinding) bool {
	if id == "" {
		return binding == nil
	}
	return binding != nil && binding.Request.ID == id
}
