package postgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
)

func (store *GORMSynthesisStore) ReconcileSynthesisSourceImpacts(ctx context.Context, limit int) (int, error) {
	if err := store.ready(ctx); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 100 {
		return 0, invalid(errors.New("source impact batch limit is invalid"))
	}
	result := store.database.WithContext(ctx).Exec(`WITH candidates AS (
 SELECT DISTINCT ref.workspace_id,ref.note_id,ref.source_id,ref.source_version_id,reason.value AS reason
 FROM organizing.synthesis_revision_source ref
 JOIN core.workspace w ON w.id=ref.workspace_id AND w.status='active'
 JOIN core.source s ON s.id=ref.source_id AND s.workspace_id=ref.workspace_id
 JOIN core.source_version v ON v.id=ref.source_version_id AND v.source_id=s.id
 CROSS JOIN (VALUES ('SOURCE_REMOVED'),('SOURCE_QUARANTINED')) reason(value)
 WHERE CASE reason.value WHEN 'SOURCE_REMOVED' THEN s.removed_at IS NOT NULL
 ELSE v.security_status='quarantined' OR EXISTS (SELECT 1 FROM ingestion.attempt a
 WHERE a.workspace_id=ref.workspace_id AND a.source_version_id=v.id AND a.security_status='quarantined'
 AND a.completed_at >= (SELECT min(r.created_at) FROM organizing.synthesis_revision_source prior
 JOIN organizing.synthesis_revision r ON r.id=prior.revision_id AND r.workspace_id=prior.workspace_id
 WHERE prior.workspace_id=ref.workspace_id AND prior.note_id=ref.note_id AND prior.source_version_id=v.id)) END
 AND NOT EXISTS (SELECT 1 FROM organizing.synthesis_source_impact impact WHERE impact.workspace_id=ref.workspace_id
 AND impact.note_id=ref.note_id AND impact.source_version_id=ref.source_version_id AND impact.reason=reason.value)
 ORDER BY ref.workspace_id,ref.note_id,ref.source_version_id,ref.source_id,reason.value LIMIT ?
 ) INSERT INTO organizing.synthesis_source_impact(workspace_id,note_id,source_id,source_version_id,reason)
 SELECT workspace_id,note_id,source_id,source_version_id,reason FROM candidates ON CONFLICT DO NOTHING`, limit)
	return int(result.RowsAffected), synthesisDBError(ctx, result.Error)
}

func (store *GORMSynthesisStore) ReadSynthesisSourceImpacts(ctx context.Context, query app.SynthesisSourceImpactQuery) (app.SynthesisSourceImpacts, error) {
	out := app.SynthesisSourceImpacts{WorkspaceID: query.WorkspaceID, NoteID: query.NoteID, RevisionID: query.RevisionID, Items: []app.SynthesisSourceImpact{}}
	if err := store.ready(ctx, query.WorkspaceID, query.NoteID, query.RevisionID); err != nil {
		return out, err
	}
	err := store.within(ctx, foundation.TransactionOptions{ReadOnly: true, Isolation: foundation.TransactionIsolationRepeatableRead}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		revision, err := loadSynthesisRevision(tx, query.WorkspaceID, query.NoteID, query.RevisionID)
		if err != nil {
			return err
		}
		var rows []struct {
			ID, SourceVersionID, Reason string
			DetectedAt                  time.Time
			CurrentlyUnavailable        bool
		}
		if err := tx.Raw(`SELECT impact.id::text,impact.source_version_id::text,impact.reason,impact.detected_at,
 CASE impact.reason WHEN 'SOURCE_REMOVED' THEN s.removed_at IS NOT NULL ELSE v.security_status='quarantined'
 OR EXISTS (SELECT 1 FROM ingestion.attempt a WHERE a.workspace_id=impact.workspace_id
 AND a.source_version_id=v.id AND a.security_status='quarantined') END AS currently_unavailable
 FROM organizing.synthesis_source_impact impact
 JOIN core.source s ON s.id=impact.source_id AND s.workspace_id=impact.workspace_id
 JOIN core.source_version v ON v.id=impact.source_version_id AND v.source_id=s.id
 WHERE impact.workspace_id=? AND impact.note_id=? AND EXISTS (
 SELECT 1 FROM organizing.synthesis_revision_source ref WHERE ref.workspace_id=impact.workspace_id
 AND ref.revision_id=? AND ref.source_version_id=impact.source_version_id)
 ORDER BY impact.id LIMIT ?`, string(query.WorkspaceID), string(query.NoteID), string(query.RevisionID), domain.MaxSynthesisSources*2+1).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) > domain.MaxSynthesisSources*2 {
			return synthesisConsistency("source impact projection exceeds bound")
		}
		for _, row := range rows {
			if !validID(foundation.ID(row.ID)) || row.DetectedAt.IsZero() || (row.Reason != "SOURCE_REMOVED" && row.Reason != "SOURCE_QUARANTINED") {
				return synthesisConsistency("invalid source impact")
			}
			refs := map[foundation.ID]domain.SynthesisSourceRef{}
			items := map[foundation.ID][]foundation.ID{}
			for _, item := range revision.Items {
				seen := map[foundation.ID]bool{}
				for _, ref := range item.SourceReferences() {
					if string(ref.Source.SourceVersionID) == row.SourceVersionID && !seen[ref.SourceSpanID] {
						seen[ref.SourceSpanID] = true
						refs[ref.SourceSpanID] = ref
						items[ref.SourceSpanID] = append(items[ref.SourceSpanID], item.ID)
					}
				}
			}
			spans := make([]foundation.ID, 0, len(refs))
			for span := range refs {
				spans = append(spans, span)
			}
			sort.Slice(spans, func(i, j int) bool { return spans[i] < spans[j] })
			for _, span := range spans {
				out.Items = append(out.Items, app.SynthesisSourceImpact{ID: foundation.ID(row.ID), Reason: row.Reason, DetectedAt: row.DetectedAt.UTC(), CurrentlyUnavailable: row.CurrentlyUnavailable, Reference: refs[span], ItemIDs: items[span]})
			}
		}
		return nil
	})
	return out, err
}
