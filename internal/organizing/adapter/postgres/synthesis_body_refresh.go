package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"gorm.io/gorm"
)

// ReconcileSynthesisBodyRefreshRequests 对已保存影响做有界、可重放的分组。可见基线和影响身份都不是请求去重键；P 与 C 的观察共享唯一的持久化请求。
func (store *GORMSynthesisStore) ReconcileSynthesisBodyRefreshRequests(ctx context.Context, limit int) (int, error) {
	if err := store.ready(ctx); err != nil {
		return 0, err
	}
	if limit < 1 || limit > app.MaxSynthesisListLimit {
		return 0, invalid(errors.New("body refresh batch limit is invalid"))
	}
	result := store.database.WithContext(ctx).Exec(`WITH candidates AS (
 SELECT DISTINCT ON (i.workspace_id,i.note_id,i.publication_id)
 i.workspace_id,i.note_id,i.publication_id,i.id AS impact_id
 FROM organizing.synthesis_body_impact i
 WHERE NOT EXISTS (SELECT 1 FROM organizing.synthesis_body_refresh_request r
 WHERE r.workspace_id=i.workspace_id AND r.note_id=i.note_id AND r.publication_id=i.publication_id)
 ORDER BY i.workspace_id,i.note_id,i.publication_id,i.id LIMIT ?
 ) INSERT INTO organizing.synthesis_body_refresh_request(workspace_id,note_id,publication_id,impact_id)
 SELECT workspace_id,note_id,publication_id,impact_id FROM candidates
 ON CONFLICT (workspace_id,note_id,publication_id) DO NOTHING`, limit)
	return int(result.RowsAffected), synthesisDBError(ctx, result.Error)
}

var _ app.SynthesisBodyRefreshReconciler = (*GORMSynthesisStore)(nil)

// PrepareSynthesisBodyRefresh 在同一快照中读取当前可编辑基线和精确的历史发布记录。请求中的代表性 ImpactID 不用于选择基线，因为它可能属于仍在发布的 P。
func (store *GORMSynthesisStore) PrepareSynthesisBodyRefresh(ctx context.Context, workspaceID, requestID foundation.ID) (app.SynthesisBodyRefreshPreparation, error) {
	var result app.SynthesisBodyRefreshPreparation
	if err := store.ready(ctx, workspaceID, requestID); err != nil {
		return result, err
	}
	err := store.within(ctx, foundation.TransactionOptions{ReadOnly: true, Isolation: foundation.TransactionIsolationRepeatableRead}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		row := tx.Raw(`SELECT id,workspace_id,note_id,publication_id,impact_id,created_at
 FROM organizing.synthesis_body_refresh_request WHERE workspace_id=? AND id=?`, string(workspaceID), string(requestID)).Scan(&result.Request)
		if row.Error != nil {
			return row.Error
		}
		result.Request.CreatedAt = result.Request.CreatedAt.UTC()
		if row.RowsAffected != 1 || result.Request.Validate() != nil {
			return invalid(errors.New("body refresh request is unavailable"))
		}
		note, err := loadSynthesisNote(tx, workspaceID, result.Request.NoteID, false)
		if err != nil {
			return err
		}
		revision, err := loadSynthesisRevision(tx, workspaceID, note.ID, note.CurrentRevisionID)
		if err != nil {
			return err
		}
		result.Target = app.SynthesisGenerationNote{Note: note, Revision: revision}
		var impacts []app.SynthesisBodyImpact
		// 只选择当前基线的观察，不通过旧发布基线恢复已移除的关系。协调流程可稍后补入 C 的影响；空准备结果不得将请求标为已消费。
		if err := tx.Raw(`SELECT * FROM organizing.synthesis_body_impact
 WHERE workspace_id=? AND note_id=? AND base_revision_id=? AND publication_id=?
 ORDER BY item_id LIMIT ?`, string(workspaceID), string(note.ID), string(revision.ID), string(result.Request.PublicationID), len(revision.Items)+1).Scan(&impacts).Error; err != nil {
			return err
		}
		if len(impacts) == 0 || len(impacts) > len(revision.Items) {
			return invalid(errors.New("body refresh current baseline needs reconciliation"))
		}
		var expected int64
		if err := tx.Raw(`SELECT count(*) FROM organizing.synthesis_pending_body_impact
 WHERE workspace_id=? AND note_id=? AND base_revision_id=? AND publication_id=?`, string(workspaceID), string(note.ID), string(revision.ID), string(result.Request.PublicationID)).Scan(&expected).Error; err != nil {
			return err
		}
		if expected != int64(len(impacts)) {
			return invalid(errors.New("body refresh current impact group is incomplete"))
		}
		for _, impact := range impacts {
			var supplements int64
			if err := tx.Table("organizing.synthesis_source_supplement").Where("workspace_id=? AND note_id=? AND item_id=?", string(workspaceID), string(note.ID), string(impact.ItemID)).Count(&supplements).Error; err != nil {
				return err
			}
			if supplements != 0 {
				return synthesisConflict("body refresh target has local supplementary evidence requiring review")
			}
			original, err := loadSynthesisRevision(tx, workspaceID, impact.UpstreamNoteID, impact.UpstreamRevisionID)
			if err != nil {
				return err
			}
			updated, err := loadSynthesisRevision(tx, workspaceID, impact.UpstreamNoteID, impact.PublishedRevisionID)
			if err != nil {
				return err
			}
			var proven int64
			if err := tx.Raw(`SELECT count(*) FROM organizing.synthesis_proven_publication
 WHERE workspace_id=? AND note_id=? AND ((revision_id=? AND publication_id=? AND projection_hash=?)
 OR (revision_id=? AND publication_id=? AND projection_hash=?))`, string(workspaceID), string(impact.UpstreamNoteID), string(original.ID), string(impact.UpstreamPublicationID), original.Hash, string(updated.ID), string(impact.PublicationID), updated.Hash).Scan(&proven).Error; err != nil {
				return err
			}
			if proven != 2 {
				return invalid(errors.New("body refresh historical publication proof changed"))
			}
			matched := false
			for _, item := range revision.Items {
				if item.ID == impact.ItemID && item.BodyReference != nil {
					ref := item.BodyReference
					matched = ref.WorkspaceID == workspaceID && ref.NoteID == original.NoteID && ref.RevisionID == original.ID &&
						ref.PublicationID == impact.UpstreamPublicationID && ref.ItemID == impact.UpstreamItemID && ref.ProjectionHash == original.Hash
					break
				}
			}
			if !matched {
				return invalid(errors.New("body refresh current reference differs from saved impact"))
			}
			result.Items = append(result.Items, app.SynthesisBodyRefreshItem{Impact: impact, Original: original, Updated: updated})
		}
		return nil
	})
	if err != nil {
		return app.SynthesisBodyRefreshPreparation{}, err
	}
	return result, nil
}

var _ app.SynthesisBodyRefreshPreparer = (*GORMSynthesisStore)(nil)
