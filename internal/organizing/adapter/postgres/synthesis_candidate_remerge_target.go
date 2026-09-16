package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
)

// Target 读取可用的所属模块绑定，不创建合并尝试或捕获快照。
func (h *synthesisCandidateRemerge) Target(ctx context.Context, workspace, noteID foundation.ID) (app.SynthesisCandidateRemergeTarget, error) {
	var out app.SynthesisCandidateRemergeTarget
	if !validID(noteID) {
		return out, manuscriptStoreInvalid("invalid note")
	}
	err := h.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		root, err := h.authorize(ctx, scope, workspace)
		if err != nil {
			return err
		}
		note, err := loadSynthesisNote(tx, workspace, noteID, true)
		if err != nil {
			return err
		}
		state, err := h.runtime.dependencies.Candidates.dependencies.Authoring.ReadGeneratedDocumentScoped(ctx, scope, workspace, note.DocumentID, note.ID)
		if err != nil {
			return err
		}
		if state.Publication == nil {
			return manuscriptStoreConflict("candidate publication binding missing")
		}
		// 按与退役操作相同的 document → proposal 加锁顺序冻结提案版本；随后共用的当前状态检查验证其精确绑定。
		var proposal struct{ Version int64 }
		result := tx.Raw(`SELECT p.version FROM change_control.proposal p WHERE p.workspace_id=? AND p.id=? FOR UPDATE`, string(workspace), string(state.Publication.ProposalID)).Scan(&proposal)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return manuscriptStoreConflict("candidate proposal missing")
		}
		c := app.BeginSynthesisCandidateRemerge{WorkspaceID: workspace, NoteID: note.ID, ExpectedRevisionID: note.CurrentRevisionID, ExpectedNoteVersion: note.Version, ExpectedDocumentID: note.DocumentID, ExpectedDocumentVersion: state.Document.Version, ExpectedPublicationID: state.Publication.ID, ExpectedProposalID: state.Publication.ProposalID, ExpectedProposalRevisionID: state.Publication.ProposalRevisionID, ExpectedProposalVersion: proposal.Version}
		revision, owner, err := h.current(ctx, scope, tx, c)
		if err != nil {
			return err
		}
		_, base, err := candidateRemergeBase(tx, workspace, note.ID, revision)
		if err != nil {
			return err
		}
		if !base.Exists || owner.Document.CurrentPublishedRevisionID == "" {
			return manuscriptStoreConflict("remerge requires an existing published file")
		}
		var published bool
		if err = tx.Raw(`SELECT EXISTS(SELECT 1 FROM core.article_revision WHERE workspace_id=? AND document_id=? AND id=? AND status='PUBLISHED')`, string(workspace), string(note.DocumentID), string(owner.Document.CurrentPublishedRevisionID)).Scan(&published).Error; err != nil {
			return err
		}
		if !published {
			return manuscriptStoreConflict("published baseline missing")
		}
		// 核验实际根目录文件是否可用；字节和哈希均不离开此所属模块。
		capture := app.SynthesisManuscriptCapture{ID: note.ID, WorkspaceID: workspace, TargetPath: owner.Document.CanonicalPath, RootGrantID: root.GrantID, RootFingerprint: root.Fingerprint, WorkspaceBindingVersion: root.BindingVersion, Exists: true, CreatedAt: canonicalTime(h.runtime.dependencies.Storage.Clock.Now())}
		capture.Bytes, capture.ContentHash, err = h.runtime.dependencies.Storage.Files.CurrentContent(ctx, workspace, capture.TargetPath, domain.MaxSynthesisManuscriptBytes)
		if err != nil {
			return err
		}
		if err = validateManuscriptCapture(capture); err != nil {
			return err
		}
		out = app.SynthesisCandidateRemergeTarget{WorkspaceID: c.WorkspaceID, NoteID: c.NoteID, ExpectedRevisionID: c.ExpectedRevisionID, ExpectedNoteVersion: c.ExpectedNoteVersion, ExpectedDocumentID: c.ExpectedDocumentID, ExpectedDocumentVersion: c.ExpectedDocumentVersion, ExpectedPublicationID: c.ExpectedPublicationID, ExpectedProposalID: c.ExpectedProposalID, ExpectedProposalRevisionID: c.ExpectedProposalRevisionID, ExpectedProposalVersion: c.ExpectedProposalVersion}
		return nil
	})
	if err != nil {
		return app.SynthesisCandidateRemergeTarget{}, err
	}
	return out, nil
}
