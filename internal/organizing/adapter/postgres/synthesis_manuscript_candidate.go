package postgres

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
)

func synthesisApplyBinding(input app.SynthesisGenerationInput, generation app.SynthesisGenerationResult, manuscripts []app.SynthesisManuscriptCandidate) ([]byte, error) {
	if len(manuscripts) == 0 {
		return json.Marshal(struct {
			Input      app.SynthesisGenerationInput
			Generation app.SynthesisGenerationResult
		}{input, generation})
	}
	return json.Marshal(struct {
		Version     string
		Input       app.SynthesisGenerationInput
		Generation  app.SynthesisGenerationResult
		Manuscripts []app.SynthesisManuscriptCandidate
	}{"synthesis-apply/v2", input, generation, manuscripts})
}

func (s *GORMSynthesisStore) manuscriptCandidate(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, record app.SynthesisApplyRecord, index int, base *app.SynthesisGenerationNote, items []domain.SynthesisItem) (*app.SynthesisManuscriptReceipt, error) {
	var selected *app.SynthesisManuscriptCandidate
	for i := range record.Manuscripts {
		if record.Manuscripts[i].NoteID == record.Generation.Notes[index].NoteID {
			if selected != nil {
				return nil, manuscriptStoreInvalid("duplicate manuscript candidate")
			}
			selected = &record.Manuscripts[i]
		}
	}
	if selected == nil {
		if base != nil && (base.Revision.Manuscript != nil || s.dependencies.Manuscripts != nil) {
			return nil, manuscriptStoreInvalid("existing manuscript candidate requires its sealed receipt")
		}
		return nil, nil
	}
	if base == nil || s.dependencies.Manuscripts == nil {
		return nil, manuscriptStoreInvalid("manuscript candidate owner is unavailable")
	}
	var row manuscriptReceiptRow
	if err := tx.Where("workspace_id=? AND id=?", string(base.Note.WorkspaceID), string(selected.ReceiptID)).Take(&row).Error; err != nil {
		return nil, manuscriptReadError(err)
	}
	attempt, _, err := s.dependencies.Manuscripts.readAttempt(ctx, tx, base.Note.WorkspaceID, foundation.ID(row.AttemptID))
	if err != nil {
		return nil, err
	}
	receipt, err := s.dependencies.Manuscripts.decodeReceipt(row, attempt)
	if err != nil {
		return nil, err
	}
	if receipt.Hash != selected.ReceiptHash || attempt.Command.ProcessingID != record.Input.ProcessingID || !reflect.DeepEqual(receipt.Manuscript.Machine.MachineItems, items) {
		return nil, manuscriptStoreConflict("candidate differs from sealed machine result")
	}
	trusted, err := receipt.Manuscript.TrustedItems(receipt.Manuscript.Machine, s.dependencies.Manuscripts.dependencies.Mapper)
	if err != nil {
		return nil, err
	}
	ids := record.Candidates[index]
	revision := domain.SynthesisRevision{ID: ids.RevisionID, WorkspaceID: base.Note.WorkspaceID, NoteID: base.Note.ID, DocumentID: base.Note.DocumentID, ArticleRevisionID: ids.ArticleRevisionID, ParentRevisionID: base.Revision.ID, RevisionNo: base.Revision.RevisionNo + 1, ArticleRevisionNo: base.Revision.ArticleRevisionNo + 1, Title: base.Note.Title, RendererVersion: domain.SynthesisRendererVersionV2, Items: trusted, Manuscript: &receipt.Manuscript, Delta: record.Generation.Notes[index].Delta, SourceEventID: record.Input.SourceEvent.ID, WorkflowRunID: record.Input.WorkflowRunID, ModelRunID: record.Generation.ModelRunID, ContentHash: receipt.Manuscript.ContentHash, CreatedAt: canonicalTime(record.AppliedAt)}
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	if err != nil {
		return nil, err
	}
	if err = s.dependencies.Manuscripts.VerifyReceiptScoped(ctx, scope, base.Note.WorkspaceID, receipt.ID, revision); err != nil {
		return nil, err
	}
	return &receipt, nil
}
