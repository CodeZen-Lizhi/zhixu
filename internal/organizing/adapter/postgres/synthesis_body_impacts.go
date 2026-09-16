package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"gorm.io/gorm"
)

func (store *GORMSynthesisStore) ReconcileSynthesisBodyImpacts(ctx context.Context, limit int) (int, error) {
	if err := store.ready(ctx); err != nil {
		return 0, err
	}
	if limit < 1 || limit > app.MaxSynthesisListLimit {
		return 0, invalid(errors.New("body impact batch limit is invalid"))
	}
	result := store.database.WithContext(ctx).Exec(`WITH candidates AS (
 SELECT p.workspace_id,p.note_id,p.base_revision_id,p.item_id,p.upstream_note_id,p.upstream_revision_id,
 p.upstream_item_id,p.upstream_publication_id,p.publication_id,p.published_revision_id,p.event_id,p.reason
 FROM organizing.synthesis_pending_body_impact p
 WHERE NOT EXISTS (SELECT 1 FROM organizing.synthesis_body_impact i
 WHERE i.base_revision_id=p.base_revision_id AND i.item_id=p.item_id AND i.publication_id=p.publication_id)
 ORDER BY p.published_at,p.publication_id,p.base_revision_id,p.item_id LIMIT ?
 ) INSERT INTO organizing.synthesis_body_impact(workspace_id,note_id,base_revision_id,item_id,
 upstream_note_id,upstream_revision_id,upstream_item_id,upstream_publication_id,publication_id,
 published_revision_id,event_id,reason)
 SELECT workspace_id,note_id,base_revision_id,item_id,upstream_note_id,upstream_revision_id,
 upstream_item_id,upstream_publication_id,publication_id,published_revision_id,event_id,reason
 FROM candidates ON CONFLICT (base_revision_id,item_id,publication_id) DO NOTHING`, limit)
	return int(result.RowsAffected), synthesisDBError(ctx, result.Error)
}

func (store *GORMSynthesisStore) ReadSynthesisBodyImpacts(ctx context.Context, query app.SynthesisBodyImpactQuery) (app.SynthesisBodyImpacts, error) {
	out := app.SynthesisBodyImpacts{Items: []app.SynthesisBodyImpact{}}
	if err := store.ready(ctx, query.WorkspaceID, query.NoteID, query.BaseRevisionID); err != nil {
		return out, err
	}
	if query.Limit < 1 || query.Limit > app.MaxSynthesisListLimit || (query.AfterID != "" && !validID(query.AfterID)) {
		return out, invalid(errors.New("body impact page is invalid"))
	}
	err := store.within(ctx, foundation.TransactionOptions{ReadOnly: true, Isolation: foundation.TransactionIsolationRepeatableRead}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		if _, err := loadSynthesisRevision(tx, query.WorkspaceID, query.NoteID, query.BaseRevisionID); err != nil {
			return err
		}
		var rows []app.SynthesisBodyImpact
		if err := tx.Raw(`SELECT id,workspace_id,note_id,base_revision_id,item_id,upstream_note_id,upstream_revision_id,
 upstream_item_id,upstream_publication_id,publication_id,published_revision_id,event_id,reason,detected_at
 FROM organizing.synthesis_body_impact WHERE workspace_id=? AND note_id=? AND base_revision_id=?
 AND (?='' OR id>NULLIF(?,'')::uuid) ORDER BY id LIMIT ?`, string(query.WorkspaceID), string(query.NoteID), string(query.BaseRevisionID), string(query.AfterID), string(query.AfterID), query.Limit+1).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) > query.Limit {
			rows = rows[:query.Limit]
			out.NextAfterID = rows[len(rows)-1].ID
		}
		for i := range rows {
			rows[i].DetectedAt = rows[i].DetectedAt.UTC()
		}
		out.Items = append(out.Items, rows...)
		return nil
	})
	return out, err
}

var _ app.SynthesisBodyImpactReader = (*GORMSynthesisStore)(nil)
var _ app.SynthesisBodyImpactReconciler = (*GORMSynthesisStore)(nil)
