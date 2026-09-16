package postgres

import (
	"context"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
)

var _ app.SynthesisSourceGraphReader = (*GORMSynthesisStore)(nil)

func (store *GORMSynthesisStore) ReadSynthesisSourceGraph(ctx context.Context, query app.SynthesisSourceGraphQuery) (app.SynthesisSourceGraph, error) {
	out := app.SynthesisSourceGraph{WorkspaceID: query.WorkspaceID, NoteID: query.NoteID, RevisionID: query.RevisionID,
		Sources: []domain.SynthesisSourceRef{}, SharedNotes: []app.SynthesisSharedSourceNote{}}
	if err := store.ready(ctx, query.WorkspaceID, query.NoteID, query.RevisionID); err != nil {
		return out, err
	}
	if query.Limit < 1 || query.Limit > 20 || query.AfterNoteID != "" && !validID(query.AfterNoteID) {
		return out, invalid(errors.New("source graph page is invalid"))
	}
	err := store.within(ctx, foundation.TransactionOptions{ReadOnly: true, Isolation: foundation.TransactionIsolationRepeatableRead}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		revision, err := loadSynthesisRevision(tx, query.WorkspaceID, query.NoteID, query.RevisionID)
		if err != nil {
			return err
		}
		bySpan := map[foundation.ID]domain.SynthesisSourceRef{}
		bySource := map[foundation.ID]bool{}
		for _, item := range revision.Items {
			for _, ref := range item.SourceReferences() {
				if existing, ok := bySpan[ref.SourceSpanID]; ok && existing != ref {
					return synthesisConsistency("graph reference changed")
				}
				bySpan[ref.SourceSpanID] = ref
				bySource[ref.Source.SourceID] = true
			}
		}
		for _, ref := range bySpan {
			out.Sources = append(out.Sources, ref)
		}
		sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].SourceSpanID < out.Sources[j].SourceSpanID })
		if len(out.Sources) == 0 {
			return nil
		}
		sourceIDs := make([]string, 0, len(bySource))
		for id := range bySource {
			sourceIDs = append(sourceIDs, string(id))
		}
		var rows []struct {
			NoteID     string `gorm:"column:note_id"`
			DocumentID string `gorm:"column:document_id"`
			RevisionID string `gorm:"column:revision_id"`
			ArticleID  string `gorm:"column:article_id"`
			RevisionNo int64  `gorm:"column:revision_no"`
			Title      string `gorm:"column:title"`
		}
		q := tx.Table("organizing.synthesis_note AS n").Select("n.id AS note_id,n.document_id,r.id AS revision_id,r.article_revision_id AS article_id,r.revision_no,r.title").
			Joins("JOIN core.document d ON d.id=n.document_id AND d.workspace_id=n.workspace_id").
			Joins("JOIN organizing.synthesis_revision r ON r.note_id=n.id AND r.workspace_id=n.workspace_id AND r.article_revision_id=d.current_published_revision_id").
			Where("n.workspace_id=? AND n.id<>? AND d.lifecycle_status='PUBLISHED'", string(query.WorkspaceID), string(query.NoteID)).
			Where("EXISTS (SELECT 1 FROM organizing.synthesis_revision_source s WHERE s.workspace_id=n.workspace_id AND s.revision_id=r.id AND s.source_id IN ?)", sourceIDs)
		if query.AfterNoteID != "" {
			q = q.Where("n.id>?", string(query.AfterNoteID))
		}
		if err := q.Order("n.id").Limit(query.Limit + 1).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) > query.Limit {
			rows = rows[:query.Limit]
			id := foundation.ID(rows[len(rows)-1].NoteID)
			out.NextAfterNoteID = &id
		}
		if len(rows) == 0 {
			return nil
		}
		documentIDs, revisionIDs := make([]string, len(rows)), make([]string, len(rows))
		for i, row := range rows {
			documentIDs[i] = row.DocumentID
			revisionIDs[i] = row.RevisionID
		}
		docs, _, err := readSynthesisOwnerProjections(tx, query.WorkspaceID, documentIDs)
		if err != nil {
			return err
		}
		var refs []synthesisSourceModel
		if err := tx.Where("workspace_id=? AND revision_id IN ? AND source_id IN ?", string(query.WorkspaceID), revisionIDs, sourceIDs).Order("revision_id,source_span_id").Find(&refs).Error; err != nil {
			return err
		}
		grouped := map[string][]domain.SynthesisSourceRef{}
		for _, row := range refs {
			ref := domain.SynthesisSourceRef{Source: domain.SynthesisSourceVersion{WorkspaceID: foundation.ID(row.WorkspaceID), SourceID: foundation.ID(row.SourceID), SourceVersionID: foundation.ID(row.SourceVersionID), ContentArtifactID: foundation.ID(row.ContentArtifactID), ParseProjectionID: foundation.ID(row.ParseProjectionID), ContentHash: row.ContentHash}, SourceSpanID: foundation.ID(row.SourceSpanID), ExcerptHash: row.ExcerptHash, Title: row.Title}
			if ref.Validate() != nil {
				return synthesisConsistency("shared graph reference is invalid")
			}
			grouped[row.RevisionID] = append(grouped[row.RevisionID], ref)
		}
		for _, row := range rows {
			doc, ok := docs[row.DocumentID]
			if !ok || !doc.PublishedVerified || doc.PublishedArticleID != row.ArticleID {
				return synthesisConsistency("shared note publication is unverified")
			}
			shared := grouped[row.RevisionID]
			if len(shared) == 0 || len(shared) > domain.MaxSynthesisSources {
				return synthesisConsistency("shared source graph binding is invalid")
			}
			out.SharedNotes = append(out.SharedNotes, app.SynthesisSharedSourceNote{NoteID: foundation.ID(row.NoteID), RevisionID: foundation.ID(row.RevisionID), RevisionNo: row.RevisionNo, Title: row.Title, Sources: shared})
		}
		return nil
	})
	return out, err
}
