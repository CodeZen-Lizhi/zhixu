package postgres

import (
	"context"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
	"reflect"
)

func (h *synthesisCandidateRemerge) Apply(ctx context.Context, c app.ApplySynthesisCandidateRemerge) (app.SynthesisCandidateRemergeReview, error) {
	var out app.SynthesisCandidateRemergeReview
	if !validID(c.WorkspaceID) || !validID(c.NoteID) || !validID(c.AttemptID) || !validRemergeKey(c.IdempotencyKey) {
		return out, manuscriptStoreInvalid("invalid remerge application")
	}
	var applied candidateRemergeApplied
	var attempt candidateRemergeAttempt
	err := h.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		root, err := h.authorize(ctx, scope, c.WorkspaceID)
		if err != nil {
			return err
		}
		if _, err = loadSynthesisNote(tx, c.WorkspaceID, c.NoteID, true); err != nil {
			return err
		}
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
		if found {
			if !reflect.DeepEqual(applied.Command, c) {
				return synthesisConflict("remerge application command differs")
			}
			out, err = h.project(tx, attempt, true)
			return err
		}
		revision, owner, err := h.current(ctx, scope, tx, attempt.Command)
		if err != nil {
			return err
		}
		if revision.Hash != attempt.Original.Hash || owner.Document.CurrentPublishedRevisionID != attempt.PublishedRevisionID {
			return manuscriptStoreConflict("candidate or current publication changed")
		}
		if err = h.verifyFile(ctx, attempt); err != nil {
			return err
		}
		preview, err := app.PreviewSynthesisCandidateRemerge(ctx, revision, string(attempt.BaseCapture.Bytes), string(attempt.Capture.Bytes), h.runtime.dependencies.Storage.Merge, h.runtime.dependencies.Storage.Mapper, c.Resolution)
		if err != nil {
			return err
		}
		if preview.Manuscript == nil || preview.Review != nil {
			return manuscriptStoreInvalid("every conflict must be explicitly resolved")
		}
		ids := make([]foundation.ID, 4)
		for i := range ids {
			ids[i], err = h.runtime.dependencies.Storage.IDs.New()
			if err != nil {
				return err
			}
		}
		at := canonicalTime(h.runtime.dependencies.Storage.Clock.Now())
		if owner.Document.UpdatedAt.After(at) {
			at = owner.Document.UpdatedAt
		}
		if owner.Publication.UpdatedAt.After(at) {
			at = owner.Publication.UpdatedAt
		}
		r := revision
		r.ID = ids[0]
		r.ArticleRevisionID = ids[1]
		r.ParentRevisionID = revision.ID
		r.RevisionNo++
		r.ArticleRevisionNo++
		r.CreatedAt = at
		r.Manuscript = preview.Manuscript
		r.ContentHash = preview.Manuscript.ContentHash
		r.Remerge = &domain.SynthesisCandidateRemergeProvenance{AttemptID: attempt.ID, SourceRevisionID: revision.ID}
		r.Items, err = r.Manuscript.TrustedItems(r.Manuscript.Machine, h.runtime.dependencies.Storage.Mapper)
		if err != nil {
			return err
		}
		r.Hash, err = domain.ComputeSynthesisRevisionHash(r)
		if err != nil {
			return err
		}
		publication := app.SynthesisPublicationCommand{NoteID: c.NoteID, RevisionID: r.ID, DocumentID: r.DocumentID, ArticleRevisionID: r.ArticleRevisionID, ContentHash: r.ContentHash, IdempotencyKey: "synthesis-publish:" + string(r.ID)}
		applied = candidateRemergeApplied{Command: c, Revision: r, Publication: publication, ReservationID: ids[2]}
		// 先插入不可变应用记录，再插入依赖候选；延迟 SQL 约束要求整个事务中的记录完整落地。
		if err = h.insert(tx, ids[3], c.WorkspaceID, c.NoteID, attempt.ID, "APPLY", c.IdempotencyKey, applied); err != nil {
			return err
		}
		retirement := authoringdomain.GeneratedPublicationRetirementRequest{WorkspaceID: c.WorkspaceID, DocumentID: r.DocumentID, ArticleRevisionID: revision.ArticleRevisionID, PublicationID: owner.Publication.ID, OriginKind: authoringdomain.GeneratedOriginSynthesisNote, OriginID: c.NoteID, OriginRevisionID: revision.ID, ProjectionHash: revision.Hash, ExpectedDocumentVersion: owner.Document.Version, ExpectedProposalVersion: owner.ProposalVersion}
		retirementHash, err := authoringdomain.ComputeGeneratedPublicationRetirementRequestHash(retirement)
		if err != nil {
			return err
		}
		dependencies := h.runtime.dependencies.Candidates.dependencies
		if _, err = dependencies.Authoring.RetireGeneratedPublicationScoped(ctx, scope, authoringapp.RetireGeneratedPublicationRecord{Request: retirement, RequestHash: retirementHash, IdempotencyKey: "synthesis-remerge-retire:" + string(attempt.ID), RetiredAt: at}, dependencies.Retirer); err != nil {
			return err
		}
		request := authoringdomain.GeneratedRevisionRequest{WorkspaceID: c.WorkspaceID, DocumentID: r.DocumentID, ArticleRevisionID: r.ArticleRevisionID, OriginKind: authoringdomain.GeneratedOriginSynthesisNote, OriginID: c.NoteID, OriginRevisionID: r.ID, ProjectionHash: r.Hash, ExpectedDocumentVersion: owner.Document.Version, RevisionNo: int(r.ArticleRevisionNo), Title: r.Title, TargetPath: owner.Document.CanonicalPath, Content: r.Manuscript.FullContent, ParentRevisionID: revision.ArticleRevisionID}
		hash, err := authoringdomain.ComputeGeneratedRevisionRequestHash(request)
		if err != nil {
			return err
		}
		appended, err := dependencies.Authoring.AppendGeneratedRevisionScoped(ctx, scope, authoringapp.GeneratedRevisionRecord{Request: request, RequestHash: hash, IdempotencyKey: "synthesis-revision:" + string(r.ID), CreatedAt: at})
		if err != nil {
			return err
		}
		if appended.Revision.ID != r.ArticleRevisionID || appended.Revision.ContentHash != r.ContentHash {
			return synthesisConsistency("remerge authoring result differs")
		}
		row, err := synthesisRevisionRecord(r)
		if err != nil {
			return err
		}
		row.ManuscriptReceiptID = synthesisIDPointer(attempt.ReceiptID)
		row.CandidateRemergeID = synthesisIDPointer(ids[3])
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		if err = insertSynthesisRevisionSources(tx, r); err != nil {
			return err
		}
		changed := tx.Model(&synthesisNoteModel{}).Where("workspace_id=? AND id=? AND version=? AND current_revision_id=?", string(c.WorkspaceID), string(c.NoteID), attempt.Command.ExpectedNoteVersion, string(revision.ID)).Updates(map[string]any{"current_revision_id": string(r.ID), "version": attempt.Command.ExpectedNoteVersion + 1, "status": string(domain.SynthesisPendingApproval), "failure": nil, "updated_at": at})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return manuscriptStoreConflict("candidate changed")
		}
		publishHash, err := authoringdomain.ComputePublishRequestHash(c.WorkspaceID, r.DocumentID, r.ArticleRevisionID)
		if err != nil {
			return err
		}
		if _, err = dependencies.Authoring.ReservePublicationScoped(ctx, scope, authoringapp.ReservePublicationRecord{Binding: authoringapp.PublishBinding{WorkspaceID: c.WorkspaceID, DocumentID: r.DocumentID, RevisionID: r.ArticleRevisionID, IdempotencyKey: publication.IdempotencyKey, RequestHash: publishHash}, ReservationID: ids[2], ReservedAt: at}); err != nil {
			return err
		}
		if err = h.verifyFile(ctx, attempt); err != nil {
			return err
		}
		out, err = h.project(tx, attempt, false)
		return err
	})
	if err != nil {
		return out, err
	}
	resumed, err := h.Resume(ctx, app.ResumeSynthesisCandidateRemerge{WorkspaceID: c.WorkspaceID, NoteID: c.NoteID, AttemptID: c.AttemptID})
	if err != nil && resumed.State == "" {
		return out, err
	}
	resumed.Replayed = out.Replayed
	return resumed, err
}
