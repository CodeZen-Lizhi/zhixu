package postgres

import (
	"context"
	"errors"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
)

func (store *GORMSynthesisStore) ListSynthesisCandidates(ctx context.Context, workspaceID foundation.ID) ([]organizingapp.SynthesisGenerationNote, error) {
	if err := store.ready(ctx, workspaceID); err != nil {
		return nil, err
	}
	result := []organizingapp.SynthesisGenerationNote{}
	err := store.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var rows []synthesisNoteModel
		if err := tx.Select(synthesisNoteColumns).Where("workspace_id=?", string(workspaceID)).Order("updated_at DESC,id DESC").Limit(organizingapp.MaxSynthesisCandidateNotes).Find(&rows).Error; err != nil {
			return err
		}
		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i] = row.CurrentRevisionID
		}
		if len(ids) == 0 {
			return nil
		}
		var revisions []synthesisRevisionModel
		if err := tx.Select(synthesisRevisionColumns).Where("workspace_id=? AND id IN ?", string(workspaceID), ids).Find(&revisions).Error; err != nil {
			return err
		}
		byID := map[string]domain.SynthesisRevision{}
		for _, row := range revisions {
			revision, err := row.domain()
			if err != nil {
				return err
			}
			byID[row.ID] = revision
		}
		for _, row := range rows {
			note, err := row.domain()
			if err != nil {
				return err
			}
			revision, found := byID[row.CurrentRevisionID]
			if !found || revision.NoteID != note.ID || revision.DocumentID != note.DocumentID {
				return synthesisConsistency("synthesis current revision is missing")
			}
			result = append(result, organizingapp.SynthesisGenerationNote{Note: note, Revision: revision})
		}
		return nil
	})
	return result, err
}

func (store *GORMSynthesisStore) GetSynthesisRevision(ctx context.Context, workspaceID, noteID, revisionID foundation.ID) (domain.SynthesisRevision, error) {
	if err := store.ready(ctx, workspaceID, noteID, revisionID); err != nil {
		return domain.SynthesisRevision{}, err
	}
	revision, err := loadSynthesisRevision(store.database.WithContext(ctx), workspaceID, noteID, revisionID)
	return revision, synthesisDBError(ctx, err)
}

func (store *GORMSynthesisStore) ListSynthesisRevisions(ctx context.Context, query organizingapp.SynthesisRevisionListQuery) (organizingapp.SynthesisRevisionPage, error) {
	if err := store.ready(ctx, query.WorkspaceID, query.NoteID); err != nil {
		return organizingapp.SynthesisRevisionPage{}, err
	}
	if query.Limit < 1 || query.Limit > organizingapp.MaxSynthesisListLimit || query.BeforeRevisionNo < 0 {
		return organizingapp.SynthesisRevisionPage{}, invalid(errors.New("synthesis revision page is invalid"))
	}
	page := organizingapp.SynthesisRevisionPage{Items: []domain.SynthesisRevision{}}
	err := store.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		if _, err := loadSynthesisNote(tx, query.WorkspaceID, query.NoteID, false); err != nil {
			return err
		}
		q := tx.Select(synthesisRevisionColumns).Where("workspace_id=? AND note_id=?", string(query.WorkspaceID), string(query.NoteID))
		if query.BeforeRevisionNo > 0 {
			q = q.Where("revision_no<?", query.BeforeRevisionNo)
		}
		var rows []synthesisRevisionModel
		if err := q.Order("revision_no DESC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > query.Limit {
			rows = rows[:query.Limit]
			page.NextBeforeRevisionNo = rows[len(rows)-1].RevisionNo
		}
		for _, row := range rows {
			revision, err := row.domain()
			if err != nil {
				return err
			}
			page.Items = append(page.Items, revision)
		}
		return nil
	})
	return page, err
}

type synthesisDocumentProjection struct {
	ID                 string `gorm:"column:id"`
	Lifecycle          string `gorm:"column:lifecycle"`
	LatestArticleID    string `gorm:"column:latest_article_id"`
	PublishedArticleID string `gorm:"column:published_article_id"`
	PublishedVerified  bool   `gorm:"column:published_verified"`
}

type synthesisPublicationProjection struct {
	ArticleRevisionID         string `gorm:"column:article_revision_id"`
	ProposalID                string `gorm:"column:proposal_id"`
	ProposalRevisionID        string `gorm:"column:proposal_revision_id"`
	ContentHash               string `gorm:"column:content_hash"`
	Status                    string `gorm:"column:status"`
	ErrorCode                 string `gorm:"column:error_code"`
	ProposalCurrentRevisionID string `gorm:"column:proposal_current_revision_id"`
}

// PublishedVerified duplicates no lifecycle: it checks the Authoring published
// pointer against its exact ArticleRevision, immutable binding and Git commit.
func readSynthesisOwnerProjections(tx *gorm.DB, workspaceID foundation.ID, documentIDs []string) (map[string]synthesisDocumentProjection, map[string]synthesisPublicationProjection, error) {
	documents := map[string]synthesisDocumentProjection{}
	publications := map[string]synthesisPublicationProjection{}
	if len(documentIDs) == 0 {
		return documents, publications, nil
	}
	var docs []synthesisDocumentProjection
	err := tx.Raw(`SELECT d.id,d.lifecycle_status AS lifecycle,COALESCE(latest.id::text,'') AS latest_article_id,
	 COALESCE(d.current_published_revision_id::text,'') AS published_article_id,
	 EXISTS(SELECT 1 FROM core.article_revision ar
	  JOIN authoring.generated_article_revision gr ON gr.article_revision_id=ar.id AND gr.workspace_id=ar.workspace_id AND gr.document_id=ar.document_id AND gr.content_hash=ar.content_hash
	  JOIN authoring.document_publication_binding b ON b.article_revision_id=ar.id AND b.workspace_id=ar.workspace_id AND b.document_id=d.id AND b.status='PUBLISHED'
	  JOIN change_control.proposal_commit pc ON pc.proposal_id=b.proposal_id AND pc.revision_id=b.proposal_revision_id AND pc.workspace_id=b.workspace_id
	   AND pc.target_path=b.target_path AND pc.target_mode=b.target_mode AND pc.result_hash=b.content_hash AND pc.git_commit=b.git_commit
	  WHERE ar.id=d.current_published_revision_id AND ar.document_id=d.id AND ar.workspace_id=d.workspace_id AND ar.status='PUBLISHED'
	   AND ar.created_by_type='AGENT' AND ar.content_hash=b.content_hash AND ar.git_commit=b.git_commit AND d.canonical_path=b.target_path AND d.lifecycle_status='PUBLISHED') AS published_verified
	 FROM core.document d LEFT JOIN LATERAL (SELECT ar.id FROM core.article_revision ar WHERE ar.document_id=d.id AND ar.workspace_id=d.workspace_id ORDER BY ar.revision_no DESC LIMIT 1) latest ON true
	 WHERE d.workspace_id=? AND d.id IN ?`, string(workspaceID), documentIDs).Scan(&docs).Error
	if err != nil {
		return nil, nil, err
	}
	for _, doc := range docs {
		documents[doc.ID] = doc
	}
	var bindings []synthesisPublicationProjection
	err = tx.Raw(`SELECT b.article_revision_id,b.proposal_id,b.proposal_revision_id,b.content_hash,b.status,b.error_code,COALESCE(p.current_revision_id::text,'') AS proposal_current_revision_id
	 FROM authoring.document_publication_binding b JOIN change_control.proposal p ON p.id=b.proposal_id AND p.workspace_id=b.workspace_id
	 JOIN organizing.synthesis_note n ON n.workspace_id=b.workspace_id AND n.document_id=b.document_id
	 JOIN organizing.synthesis_revision r ON r.id=n.current_revision_id AND r.workspace_id=n.workspace_id AND r.article_revision_id=b.article_revision_id
	 WHERE b.workspace_id=? AND b.document_id IN ?`, string(workspaceID), documentIDs).Scan(&bindings).Error
	if err != nil {
		return nil, nil, err
	}
	for _, binding := range bindings {
		publications[binding.ArticleRevisionID] = binding
	}
	return documents, publications, nil
}

func applySynthesisOwnerState(note *domain.SynthesisNote, currentArticleID foundation.ID, doc synthesisDocumentProjection, publication synthesisPublicationProjection) {
	if doc.LatestArticleID != string(currentArticleID) || doc.Lifecycle == "ARCHIVED" || doc.Lifecycle == "DELETED" {
		note.Status = domain.SynthesisConflict
		note.Failure = &domain.SynthesisFailure{Code: "SYNTHESIS_DOCUMENT_CHANGED"}
		return
	}
	if doc.PublishedVerified && doc.PublishedArticleID == string(currentArticleID) {
		note.Status = domain.SynthesisReady
		note.Failure = nil
		return
	}
	if publication.Status == "RECOVERY_REQUIRED" {
		note.Status = domain.SynthesisRecoveryRequired
		note.Failure = &domain.SynthesisFailure{Code: "SYNTHESIS_PUBLICATION_RECOVERY_REQUIRED"}
		return
	}
	if publication.Status == "CLOSED" || publication.ProposalID != "" && publication.ProposalCurrentRevisionID != publication.ProposalRevisionID {
		note.Status = domain.SynthesisConflict
		note.Failure = &domain.SynthesisFailure{Code: "SYNTHESIS_PUBLICATION_CHANGED"}
		return
	}
	if publication.ProposalID == "" {
		note.Status = domain.SynthesisGenerating
		note.Failure = nil
		return
	}
	note.Status = domain.SynthesisPendingApproval
	note.Failure = nil
}

func synthesisPublicRef(revision domain.SynthesisRevision, binding synthesisPublicationProjection) (*organizingapp.SynthesisPublicationRef, error) {
	if binding.ProposalID == "" {
		return nil, nil
	}
	if binding.ArticleRevisionID != string(revision.ArticleRevisionID) || binding.ContentHash != revision.ContentHash || !validID(foundation.ID(binding.ProposalID)) || !validID(foundation.ID(binding.ProposalRevisionID)) {
		return nil, synthesisConsistency("synthesis publication identity changed")
	}
	return &organizingapp.SynthesisPublicationRef{RevisionID: revision.ID, ArticleRevisionID: revision.ArticleRevisionID, ProposalID: foundation.ID(binding.ProposalID), ProposalRevisionID: foundation.ID(binding.ProposalRevisionID), ContentHash: binding.ContentHash}, nil
}

func (store *GORMSynthesisStore) GetSynthesisNote(ctx context.Context, workspaceID, noteID foundation.ID) (organizingapp.SynthesisNoteDetail, error) {
	if err := store.ready(ctx, workspaceID, noteID); err != nil {
		return organizingapp.SynthesisNoteDetail{}, err
	}
	initial, err := loadSynthesisNote(store.database.WithContext(ctx), workspaceID, noteID, false)
	if err != nil {
		return organizingapp.SynthesisNoteDetail{}, synthesisDBError(ctx, err)
	}
	if _, err := store.dependencies.Authoring.ReconcilePublications(ctx, authoringapp.ReconcileQuery{WorkspaceID: workspaceID, DocumentID: initial.DocumentID, Limit: 10, Now: time.Now().UTC().Truncate(time.Microsecond)}); err != nil {
		return organizingapp.SynthesisNoteDetail{}, err
	}
	var detail organizingapp.SynthesisNoteDetail
	err = store.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		note, err := loadSynthesisNote(tx, workspaceID, noteID, false)
		if err != nil {
			return err
		}
		current, err := loadSynthesisRevision(tx, workspaceID, noteID, note.CurrentRevisionID)
		if err != nil {
			return err
		}
		docs, bindings, err := readSynthesisOwnerProjections(tx, workspaceID, []string{string(note.DocumentID)})
		if err != nil {
			return err
		}
		doc, found := docs[string(note.DocumentID)]
		if !found {
			return synthesisConsistency("synthesis document is missing")
		}
		binding := bindings[string(current.ArticleRevisionID)]
		applySynthesisOwnerState(&note, current.ArticleRevisionID, doc, binding)
		detail.Note = note
		detail.CurrentRevision = &current
		detail.Publication, err = synthesisPublicRef(current, binding)
		if err != nil {
			return err
		}
		if doc.PublishedVerified {
			var row synthesisRevisionModel
			err := tx.Select(synthesisRevisionColumns).Where("workspace_id=? AND note_id=? AND article_revision_id=?", string(workspaceID), string(noteID), doc.PublishedArticleID).Take(&row).Error
			if err != nil {
				return err
			}
			published, err := row.domain()
			if err != nil {
				return err
			}
			detail.PublishedRevision = &published
		}
		return nil
	})
	return detail, err
}

func (store *GORMSynthesisStore) ReadPublishedSynthesisNote(ctx context.Context, workspaceID, noteID foundation.ID) (domain.SynthesisNoteSnapshot, error) {
	detail, err := store.GetSynthesisNote(ctx, workspaceID, noteID)
	if err != nil {
		return domain.SynthesisNoteSnapshot{}, err
	}
	if detail.PublishedRevision == nil {
		return domain.SynthesisNoteSnapshot{}, foundation.NewError(foundation.ErrorVersionConflict, "SYNTHESIS_NOTE_NOT_PUBLISHED", false, errors.New("synthesis note has no proven published revision"))
	}
	return domain.SynthesisSnapshotFromRevision(*detail.PublishedRevision)
}

func (store *GORMSynthesisStore) ListSynthesisNotes(ctx context.Context, query organizingapp.SynthesisListQuery) (organizingapp.SynthesisNotePage, error) {
	if err := store.ready(ctx, query.WorkspaceID); err != nil {
		return organizingapp.SynthesisNotePage{}, err
	}
	if query.Limit < 1 || query.Limit > organizingapp.MaxSynthesisListLimit || (query.BeforeTime == nil) != (query.BeforeID == "") || query.BeforeTime != nil && (query.BeforeTime.IsZero() || !validID(query.BeforeID)) {
		return organizingapp.SynthesisNotePage{}, invalid(errors.New("synthesis note page is invalid"))
	}
	page := organizingapp.SynthesisNotePage{Items: []organizingapp.SynthesisNoteSummary{}}
	err := store.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		q := tx.Select(synthesisNoteColumns).Where("workspace_id=?", string(query.WorkspaceID))
		if query.BeforeTime != nil {
			q = q.Where("(updated_at,id)<(?,?)", query.BeforeTime.UTC(), string(query.BeforeID))
		}
		var rows []synthesisNoteModel
		if err := q.Order("updated_at DESC,id DESC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > query.Limit {
			rows = rows[:query.Limit]
			at := rows[len(rows)-1].UpdatedAt.UTC()
			page.NextTime = &at
			page.NextID = foundation.ID(rows[len(rows)-1].ID)
		}
		if len(rows) == 0 {
			return nil
		}
		documentIDs := make([]string, len(rows))
		currentIDs := make([]string, len(rows))
		for i, row := range rows {
			documentIDs[i] = row.DocumentID
			currentIDs[i] = row.CurrentRevisionID
		}
		docs, bindings, err := readSynthesisOwnerProjections(tx, query.WorkspaceID, documentIDs)
		if err != nil {
			return err
		}
		publishedIDs := []string{}
		for _, doc := range docs {
			if doc.PublishedVerified {
				publishedIDs = append(publishedIDs, doc.PublishedArticleID)
			}
		}
		var revisions []synthesisRevisionModel
		rq := tx.Select(synthesisRevisionSummaryColumns).Where("workspace_id=?", string(query.WorkspaceID)).Where("id IN ? OR article_revision_id IN ?", currentIDs, publishedIDs)
		if err := rq.Find(&revisions).Error; err != nil {
			return err
		}
		byID := map[string]synthesisRevisionModel{}
		byArticle := map[string]synthesisRevisionModel{}
		for _, row := range revisions {
			byID[row.ID] = row
			byArticle[row.ArticleRevisionID] = row
		}
		for _, row := range rows {
			note, err := row.domain()
			if err != nil {
				return err
			}
			current, found := byID[row.CurrentRevisionID]
			if !found || current.NoteID != row.ID || current.DocumentID != row.DocumentID {
				return synthesisConsistency("synthesis summary current revision is missing")
			}
			doc, found := docs[row.DocumentID]
			if !found {
				return synthesisConsistency("synthesis summary document is missing")
			}
			binding := bindings[current.ArticleRevisionID]
			applySynthesisOwnerState(&note, foundation.ID(current.ArticleRevisionID), doc, binding)
			publication, err := synthesisPublicRef(domain.SynthesisRevision{ID: foundation.ID(current.ID), ArticleRevisionID: foundation.ID(current.ArticleRevisionID), ContentHash: current.ContentHash}, binding)
			if err != nil {
				return err
			}
			item := organizingapp.SynthesisNoteSummary{Note: note, CurrentRevision: synthesisRevisionSummary(current), Publication: publication, ItemCount: current.ItemCount, ConflictCount: current.ConflictCount, GapCount: current.GapCount, OpenGapCount: current.OpenGapCount}
			if doc.PublishedVerified {
				published, found := byArticle[doc.PublishedArticleID]
				if !found || published.NoteID != row.ID || published.DocumentID != row.DocumentID {
					return synthesisConsistency("synthesis summary published revision is missing")
				}
				item.PublishedRevision = synthesisRevisionSummary(published)
			}
			page.Items = append(page.Items, item)
		}
		return nil
	})
	return page, err
}

func synthesisRevisionSummary(row synthesisRevisionModel) *organizingapp.SynthesisRevisionSummary {
	return &organizingapp.SynthesisRevisionSummary{ID: foundation.ID(row.ID), RevisionNo: row.RevisionNo, ArticleRevisionID: foundation.ID(row.ArticleRevisionID), ArticleRevisionNo: row.ArticleRevisionNo, ContentHash: row.ContentHash, CreatedAt: row.CreatedAt.UTC()}
}
