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

func (h *synthesisHistoricalRepublish) Apply(ctx context.Context, c app.ApplySynthesisHistoricalRepublish) (app.SynthesisHistoricalRepublishReview, error) {
	var out app.SynthesisHistoricalRepublishReview
	if !validID(c.WorkspaceID) || !validID(c.NoteID) || !validID(c.AttemptID) || !validRemergeKey(c.IdempotencyKey) {
		return out, manuscriptStoreInvalid("invalid historical application")
	}
	var applied historicalRepublishApplied
	var attempt historicalRepublishAttempt
	err := h.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		root, err := h.authorize(ctx, scope, c.WorkspaceID)
		if err != nil {
			return err
		}
		if _, err = loadSynthesisNote(tx, c.WorkspaceID, c.NoteID, true); err != nil {
			return err
		}
		found, err := readHistoricalEvent(tx, c.WorkspaceID, c.NoteID, "BEGIN", "", c.AttemptID, &attempt)
		if err != nil {
			return err
		}
		if !found {
			return synthesisNotFound()
		}
		if err = historicalRoot(attempt, root); err != nil {
			return err
		}
		found, err = readHistoricalEvent(tx, c.WorkspaceID, c.NoteID, "APPLY", "", c.AttemptID, &applied)
		if err != nil {
			return err
		}
		if found {
			if !reflect.DeepEqual(applied.Command, c) {
				return manuscriptStoreConflict("historical application command differs")
			}
			out, err = h.project(tx, attempt, true)
			return err
		}
		if !c.ConfirmExactRestore || c.PreviewFingerprint != attempt.Fingerprint || c.RetireCurrentCandidate != attempt.Command.RequiresRetirement {
			return manuscriptStoreInvalid("exact historical restore confirmation required")
		}
		target, _, revision, owner, err := h.target(ctx, scope, tx, c.WorkspaceID, c.NoteID, attempt.Selected.ID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(target, attempt.Command.SynthesisHistoricalRepublishTarget) || revision.Hash != attempt.Latest.Hash {
			return manuscriptStoreConflict("historical owner changed")
		}
		currentScope, err := historicalScope(tx, c.WorkspaceID, c.NoteID)
		if err != nil {
			return err
		}
		if string(currentScope) != string(attempt.Scope) {
			return manuscriptStoreConflict("anchor scope changed")
		}
		if err = h.verifyFile(ctx, attempt); err != nil {
			return err
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
		if owner.Publication != nil && owner.Publication.UpdatedAt.After(at) {
			at = owner.Publication.UpdatedAt
		}
		r := attempt.Selected
		r.ID = ids[0]
		r.ArticleRevisionID = ids[1]
		r.ParentRevisionID = revision.ID
		r.RevisionNo = revision.RevisionNo + 1
		r.ArticleRevisionNo = revision.ArticleRevisionNo + 1
		r.CreatedAt = at
		r.Remerge = nil
		r.HistoricalRepublish = &domain.SynthesisHistoricalRepublishProvenance{AttemptID: attempt.ID, SelectedRevisionID: attempt.Selected.ID, SelectedPublicationID: attempt.Command.SelectedPublicationID, SelectedProposalCommitID: attempt.Command.SelectedProposalCommitID}
		r.Hash, err = domain.ComputeSynthesisRevisionHash(r)
		if err != nil {
			return err
		}
		publication := app.SynthesisPublicationCommand{NoteID: c.NoteID, RevisionID: r.ID, DocumentID: r.DocumentID, ArticleRevisionID: r.ArticleRevisionID, ContentHash: r.ContentHash, IdempotencyKey: "synthesis-publish:" + string(r.ID)}
		applied = historicalRepublishApplied{Command: c, Revision: r, Publication: publication, ReservationID: ids[2], ApplyID: ids[3]}
		// 先插入不可变应用记录，再插入依赖候选；延迟 SQL 约束要求整个事务中的记录完整落地。
		if err = h.insert(tx, ids[3], c.WorkspaceID, c.NoteID, attempt.ID, "APPLY", c.IdempotencyKey, applied); err != nil {
			return err
		}
		dependencies := h.runtime.dependencies.Candidates.dependencies
		if attempt.Command.RequiresRetirement {
			retirement := authoringdomain.GeneratedPublicationRetirementRequest{WorkspaceID: c.WorkspaceID, DocumentID: r.DocumentID, ArticleRevisionID: revision.ArticleRevisionID, PublicationID: owner.Publication.ID, OriginKind: authoringdomain.GeneratedOriginSynthesisNote, OriginID: c.NoteID, OriginRevisionID: revision.ID, ProjectionHash: revision.Hash, ExpectedDocumentVersion: owner.Document.Version, ExpectedProposalVersion: owner.ProposalVersion}
			retirementHash, err := authoringdomain.ComputeGeneratedPublicationRetirementRequestHash(retirement)
			if err != nil {
				return err
			}
			if _, err = dependencies.Authoring.RetireGeneratedPublicationScoped(ctx, scope, authoringapp.RetireGeneratedPublicationRecord{Request: retirement, RequestHash: retirementHash, IdempotencyKey: "synthesis-history-retire:" + string(attempt.ID), RetiredAt: at}, dependencies.Retirer); err != nil {
				return err
			}
		}
		request := authoringdomain.GeneratedRevisionRequest{WorkspaceID: c.WorkspaceID, DocumentID: r.DocumentID, ArticleRevisionID: r.ArticleRevisionID, OriginKind: authoringdomain.GeneratedOriginSynthesisNote, OriginID: c.NoteID, OriginRevisionID: r.ID, ProjectionHash: r.Hash, ExpectedDocumentVersion: owner.Document.Version, RevisionNo: int(r.ArticleRevisionNo), Title: r.Title, TargetPath: owner.Document.CanonicalPath, Content: attempt.Candidate, ParentRevisionID: revision.ArticleRevisionID}
		hash, err := authoringdomain.ComputeGeneratedRevisionRequestHash(request)
		if err != nil {
			return err
		}
		appended, err := dependencies.Authoring.AppendGeneratedRevisionScoped(ctx, scope, authoringapp.GeneratedRevisionRecord{Request: request, RequestHash: hash, IdempotencyKey: "synthesis-revision:" + string(r.ID), CreatedAt: at})
		if err != nil {
			return err
		}
		if appended.Revision.ID != r.ArticleRevisionID || appended.Revision.ContentHash != r.ContentHash {
			return synthesisConsistency("historical authoring result differs")
		}
		row, err := synthesisRevisionRecord(r)
		if err != nil {
			return err
		}
		var originalRow synthesisRevisionModel
		if err = tx.Select("manuscript_receipt_id").Where("workspace_id=? AND id=?", string(c.WorkspaceID), string(attempt.Selected.ID)).Take(&originalRow).Error; err != nil {
			return err
		}
		row.ManuscriptReceiptID = originalRow.ManuscriptReceiptID
		row.HistoricalRepublishID = synthesisIDPointer(ids[3])
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		if err = insertHistoricalRepublishSources(tx, r, attempt.Selected.ID); err != nil {
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
	resumed, err := h.Resume(ctx, app.ResumeSynthesisHistoricalRepublish{WorkspaceID: c.WorkspaceID, NoteID: c.NoteID, AttemptID: c.AttemptID})
	if err != nil && resumed.State == "" {
		return out, err
	}
	resumed.Replayed = out.Replayed
	return resumed, err
}

// 原样复制选定元组，包括历史上未记录的 NULL Profile。SQL 触发器检查每个元组均继承自选定版本。
func insertHistoricalRepublishSources(tx *gorm.DB, r domain.SynthesisRevision, selected foundation.ID) error {
	return tx.Exec(`INSERT INTO organizing.synthesis_revision_source(workspace_id,revision_id,note_id,source_id,source_version_id,content_artifact_id,parse_projection_id,source_span_id,content_hash,excerpt_hash,title,profile_revision_id)
 SELECT workspace_id,?,note_id,source_id,source_version_id,content_artifact_id,parse_projection_id,source_span_id,content_hash,excerpt_hash,title,profile_revision_id FROM organizing.synthesis_revision_source WHERE workspace_id=? AND note_id=? AND revision_id=?`, string(r.ID), string(r.WorkspaceID), string(r.NoteID), string(selected)).Error
}
