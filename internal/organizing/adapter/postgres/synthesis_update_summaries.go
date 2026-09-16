package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"gorm.io/gorm"
)

// ReadSynthesisUpdateSummaries 读取单个有界快照，不对每篇笔记分别查询。
func (store *GORMSynthesisStore) ReadSynthesisUpdateSummaries(ctx context.Context, query app.SynthesisUpdateSummaryQuery) (app.SynthesisUpdateSummaries, error) {
	out := app.SynthesisUpdateSummaries{WorkspaceID: query.WorkspaceID, Items: []app.SynthesisNoteUpdateSummary{}}
	if err := store.ready(ctx, query.WorkspaceID); err != nil {
		return out, err
	}
	ids := make([]string, len(query.NoteIDs))
	seen := map[foundation.ID]bool{}
	if len(ids) < 1 || len(ids) > app.MaxSynthesisUpdateSummaryNotes {
		return out, invalid(errors.New("invalid summary batch"))
	}
	for i, id := range query.NoteIDs {
		if !validID(id) || seen[id] {
			return out, invalid(errors.New("invalid summary note IDs"))
		}
		seen[id] = true
		ids[i] = string(id)
	}
	err := store.within(ctx, foundation.TransactionOptions{ReadOnly: true, Isolation: foundation.TransactionIsolationRepeatableRead}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var notes []synthesisNoteModel
		if err := tx.Select(synthesisNoteColumns).Where("workspace_id=? AND id IN ?", string(query.WorkspaceID), ids).Find(&notes).Error; err != nil {
			return err
		}
		if len(notes) != len(ids) {
			return synthesisNotFound()
		}
		documents := make([]string, len(notes))
		currentIDs := make([]string, len(notes))
		for i, n := range notes {
			documents[i] = n.DocumentID
			currentIDs[i] = n.CurrentRevisionID
		}
		docs, _, err := readSynthesisOwnerProjections(tx, query.WorkspaceID, documents)
		if err != nil {
			return err
		}
		publishedIDs := []string{}
		for _, d := range docs {
			if d.PublishedVerified {
				publishedIDs = append(publishedIDs, d.PublishedArticleID)
			}
		}
		var revisions []synthesisRevisionModel
		if err := tx.Select(synthesisRevisionSummaryColumns).Where("workspace_id=?", string(query.WorkspaceID)).Where("id IN ? OR article_revision_id IN ?", currentIDs, publishedIDs).Find(&revisions).Error; err != nil {
			return err
		}
		byID := map[string]synthesisRevisionModel{}
		byArticle := map[string]synthesisRevisionModel{}
		for _, r := range revisions {
			byID[r.ID] = r
			byArticle[r.ArticleRevisionID] = r
		}
		selected := []string{}
		byNote := map[foundation.ID]app.SynthesisNoteUpdateSummary{}
		for _, n := range notes {
			d, ok := docs[n.DocumentID]
			if !ok {
				return synthesisConsistency("summary document missing")
			}
			c, ok := byID[n.CurrentRevisionID]
			if !ok || c.NoteID != n.ID || c.DocumentID != n.DocumentID {
				return synthesisConsistency("summary current revision missing")
			}
			item := app.SynthesisNoteUpdateSummary{NoteID: foundation.ID(n.ID), CurrentRevisionID: foundation.ID(c.ID), Items: []app.SynthesisUpdateSummary{}}
			if d.PublishedVerified {
				p, ok := byArticle[d.PublishedArticleID]
				if !ok || p.NoteID != n.ID || p.DocumentID != n.DocumentID {
					return synthesisConsistency("summary published revision missing")
				}
				item.PublishedRevisionID = foundation.ID(p.ID)
				item.Items = append(item.Items, app.SynthesisUpdateSummary{RevisionID: foundation.ID(p.ID)})
				selected = append(selected, p.ID)
			}
			if c.ID != string(item.PublishedRevisionID) {
				item.Items = append(item.Items, app.SynthesisUpdateSummary{RevisionID: foundation.ID(c.ID)})
				selected = append(selected, c.ID)
			}
			byNote[item.NoteID] = item
		}
		// 每个被引用片段对来源观察计数一次，与详情读取器的展开方式一致；多个条目共享该片段时不重复累计。
		var counts []struct {
			RevisionID                         string
			SourceReviewCount, BodyReviewCount int64
		}
		if len(selected) > 0 {
			if err := tx.Raw(`SELECT r.id AS revision_id,
 (SELECT count(*) FROM (SELECT DISTINCT i.id,ref.source_span_id
 FROM organizing.synthesis_source_impact i
 JOIN core.source s ON s.id=i.source_id AND s.workspace_id=i.workspace_id
 JOIN core.source_version v ON v.id=i.source_version_id AND v.source_id=s.id
 JOIN organizing.synthesis_revision_source ref ON ref.workspace_id=i.workspace_id AND ref.source_version_id=i.source_version_id
 WHERE i.workspace_id=r.workspace_id AND i.note_id=r.note_id AND ref.revision_id=r.id) observations) AS source_review_count,
 (SELECT count(*) FROM organizing.synthesis_body_impact i WHERE i.workspace_id=r.workspace_id AND i.note_id=r.note_id AND i.base_revision_id=r.id) AS body_review_count
 FROM organizing.synthesis_revision r WHERE r.workspace_id=? AND r.id IN ?`, string(query.WorkspaceID), selected).Scan(&counts).Error; err != nil {
				return err
			}
		}
		countMap := map[string]app.SynthesisUpdateSummary{}
		for _, c := range counts {
			if c.SourceReviewCount < 0 || c.BodyReviewCount < 0 {
				return synthesisConsistency("invalid review counts")
			}
			countMap[c.RevisionID] = app.SynthesisUpdateSummary{RevisionID: foundation.ID(c.RevisionID), SourceReviewCount: c.SourceReviewCount, BodyReviewCount: c.BodyReviewCount}
		}
		for _, id := range query.NoteIDs {
			item := byNote[id]
			for i, r := range item.Items {
				c, ok := countMap[string(r.RevisionID)]
				if !ok {
					return synthesisConsistency("summary count missing")
				}
				item.Items[i] = c
			}
			out.Items = append(out.Items, item)
		}
		return nil
	})
	return out, err
}

var _ app.SynthesisUpdateSummaryReader = (*GORMSynthesisStore)(nil)
