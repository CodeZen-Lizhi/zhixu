package postgres

import (
	"context"
	"reflect"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"gorm.io/gorm"
)

// Resume 不重复合并或应用；持久化发布命令是恢复已有预留的唯一依据。
func (h *synthesisCandidateRemerge) Resume(ctx context.Context, c app.ResumeSynthesisCandidateRemerge) (app.SynthesisCandidateRemergeReview, error) {
	var out app.SynthesisCandidateRemergeReview
	if !validID(c.WorkspaceID) || !validID(c.NoteID) || !validID(c.AttemptID) {
		return out, manuscriptStoreInvalid("invalid remerge recovery")
	}
	var applied candidateRemergeApplied
	err := h.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		root, err := h.authorize(ctx, scope, c.WorkspaceID)
		if err != nil {
			return err
		}
		note, err := loadSynthesisNote(tx, c.WorkspaceID, c.NoteID, false)
		if err != nil {
			return err
		}
		var attempt candidateRemergeAttempt
		found, err := readRemergeEvent(tx, c.WorkspaceID, c.NoteID, "BEGIN", "", c.AttemptID, &attempt)
		if err != nil {
			return err
		}
		if !found {
			return synthesisNotFound()
		}
		if err = verifyRemergeRoot(attempt, root); err != nil {
			return err
		}
		found, err = readRemergeEvent(tx, c.WorkspaceID, c.NoteID, "APPLY", "", c.AttemptID, &applied)
		if err != nil {
			return err
		}
		if !found {
			return manuscriptStoreConflict("candidate has not been applied")
		}
		r, err := loadSynthesisRevision(tx, c.WorkspaceID, c.NoteID, applied.Revision.ID)
		if err != nil {
			return err
		}
		p := applied.Publication
		if !reflect.DeepEqual(r, applied.Revision) || r.DocumentID != note.DocumentID || r.Remerge == nil || r.Remerge.AttemptID != c.AttemptID || applied.Command.WorkspaceID != c.WorkspaceID || applied.Command.NoteID != c.NoteID || applied.Command.AttemptID != c.AttemptID || p.NoteID != c.NoteID || p.RevisionID != r.ID || p.DocumentID != r.DocumentID || p.ArticleRevisionID != r.ArticleRevisionID || p.ContentHash != r.ContentHash || p.IdempotencyKey != "synthesis-publish:"+string(r.ID) {
			return synthesisConsistency("remerge recovery result differs")
		}
		hash, err := authoringdomain.ComputePublishRequestHash(c.WorkspaceID, p.DocumentID, p.ArticleRevisionID)
		if err != nil {
			return err
		}
		var exact bool
		err = tx.Raw(`SELECT EXISTS(SELECT 1 FROM authoring.document_publication_reservation WHERE id=? AND workspace_id=? AND document_id=? AND article_revision_id=? AND content_hash=? AND idempotency_key=? AND request_hash=? AND merge_capture_id=? AND merge_receipt_id=? AND base_version=? AND merge_published_revision_id=? AND merge_published_content_hash=?)`, string(applied.ReservationID), string(c.WorkspaceID), string(p.DocumentID), string(p.ArticleRevisionID), p.ContentHash, p.IdempotencyKey, hash, string(attempt.Capture.ID), string(attempt.ReceiptID), attempt.Capture.ContentHash, string(attempt.PublishedRevisionID), attempt.PublishedContentHash).Scan(&exact).Error
		if err != nil {
			return err
		}
		if !exact {
			return synthesisConsistency("remerge recovery reservation differs")
		}
		out, err = h.project(tx, attempt, true)
		return err
	})
	if err != nil {
		return out, err
	}
	p := applied.Publication
	published, err := h.runtime.dependencies.Service.Publications.PublishArticleRevision(ctx, authoringapp.PublishCommand{WorkspaceID: c.WorkspaceID, DocumentID: p.DocumentID, RevisionID: p.ArticleRevisionID, IdempotencyKey: p.IdempotencyKey})
	if err != nil {
		return out, err
	}
	b := published.Publication
	if b.WorkspaceID != c.WorkspaceID || b.DocumentID != p.DocumentID || b.ArticleRevisionID != p.ArticleRevisionID || b.ContentHash != p.ContentHash {
		return out, synthesisConsistency("remerge publication result differs")
	}
	out.Result = &app.SynthesisCandidateRemergeResult{RevisionID: p.RevisionID, ArticleRevisionID: p.ArticleRevisionID, PublicationID: b.ID, ProposalID: b.ProposalID, ProposalRevisionID: b.ProposalRevisionID}
	return out, nil
}
