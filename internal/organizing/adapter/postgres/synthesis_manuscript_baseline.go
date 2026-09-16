package postgres

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

type SynthesisManuscriptModelProof interface {
	VerifySynthesisManuscriptHistoryScoped(context.Context, foundation.TransactionScope, app.SynthesisGenerationInput, app.SynthesisGenerationResult) error
	VerifySynthesisGenerationForExecutionScoped(context.Context, foundation.TransactionScope, workflowapp.ExecutionContext, app.SynthesisGenerationInput, app.SynthesisGenerationResult) error
}

// 每次运行时调用构造独立值；执行权限不存入上下文，也不保留在共享候选仓储上。
type synthesisManuscriptBaseline struct {
	store      *GORMSynthesisStore
	models     SynthesisManuscriptModelProof
	roots      app.SynthesisManuscriptRootReader
	execution  workflowapp.ExecutionContext
	input      app.SynthesisGenerationInput
	generation app.SynthesisGenerationResult
	semantic   app.SynthesisSemanticReceipt
}

func (b *synthesisManuscriptBaseline) LoadSynthesisManuscriptBaseline(ctx context.Context, c app.PrepareSynthesisManuscript) (app.SynthesisManuscriptPrepared, error) {
	var result app.SynthesisManuscriptPrepared
	if c.WorkspaceID != b.execution.WorkspaceID || c.ProcessingID != b.input.ProcessingID {
		return result, manuscriptStoreInvalid("manuscript command differs from execution")
	}
	err := b.store.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		var err error
		result, err = b.read(ctx, scope, tx, c.NoteID)
		return err
	})
	return result, err
}

func (b *synthesisManuscriptBaseline) VerifySynthesisManuscriptPreparedScoped(ctx context.Context, scope foundation.TransactionScope, p app.SynthesisManuscriptPrepared) (app.SynthesisManuscriptAuthority, error) {
	tx, err := b.storeTransaction(ctx, scope)
	if err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	current, err := b.read(ctx, scope, tx, p.MergeInput.Latest.NoteID)
	if err != nil {
		return app.SynthesisManuscriptAuthority{}, err
	}
	// F 由手稿存储独立捕获并检查。
	p.MergeInput.FileContent = ""
	p.MergeInput.FileExists = false
	if !reflect.DeepEqual(current, p) {
		return app.SynthesisManuscriptAuthority{}, manuscriptStoreConflict("manuscript owner or model baseline changed")
	}
	return current.Authority, nil
}

func (b *synthesisManuscriptBaseline) storeTransaction(ctx context.Context, scope foundation.TransactionScope) (*gorm.DB, error) {
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, err
	}
	return tx.WithContext(ctx), nil
}

func (b *synthesisManuscriptBaseline) read(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, noteID foundation.ID) (app.SynthesisManuscriptPrepared, error) {
	var out app.SynthesisManuscriptPrepared
	if err := b.models.VerifySynthesisGenerationForExecutionScoped(ctx, scope, b.execution, b.input, b.generation); err != nil {
		return out, err
	}
	return b.readOwners(ctx, scope, tx, noteID)
}

// readOwners 由实时执行和独立授权的 HumanTask 调用方共享，不伪造或复用运行时租约。
func (b *synthesisManuscriptBaseline) readOwners(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, noteID foundation.ID) (app.SynthesisManuscriptPrepared, error) {
	var out app.SynthesisManuscriptPrepared
	// 语义身份来自同一独立持久化的就绪步骤；上方模型校验器证明其精确输出和终态模型调用。
	var semantic struct {
		ModelRunID string
		OutputHash string
	}
	row := tx.Raw(`SELECT model_run_id,output_hash FROM organizing.synthesis_model_step WHERE workspace_id=? AND workflow_run_id=? AND stage='VALIDATE' AND status='READY'`, string(b.input.SourceEvent.Source.WorkspaceID), string(b.input.WorkflowRunID)).Scan(&semantic)
	if row.Error != nil {
		return out, row.Error
	}
	if row.RowsAffected != 1 || semantic.ModelRunID != string(b.semantic.ModelRunID) || semantic.OutputHash != b.semantic.OutputHash || !b.semantic.Accepted {
		return out, manuscriptStoreInvalid("independent semantic identity changed")
	}
	root, err := b.roots.ReadSynthesisManuscriptRootScoped(ctx, scope, b.input.SourceEvent.Source.WorkspaceID)
	if err != nil {
		return out, err
	}
	var frozen *app.SynthesisGenerationNote
	for i := range b.input.Notes {
		if b.input.Notes[i].Note.ID == noteID {
			frozen = &b.input.Notes[i]
		}
	}
	if frozen == nil || b.input.BodyRefresh != nil && frozen.Anchor == nil {
		return out, manuscriptStoreInvalid("manuscript has no admitted existing target")
	}
	refs := make([]domain.SynthesisSourceRef, len(b.input.Sources))
	for i, v := range b.input.Sources {
		refs[i] = v.Reference
	}
	if err := b.store.dependencies.Sources.VerifySynthesisSourcesScoped(ctx, scope, b.input.SourceEvent.Source.WorkspaceID, refs); err != nil {
		return out, err
	}
	if b.input.BodyRefresh == nil {
		if err := b.store.dependencies.Anchors.VerifySynthesisAnchorAdmissionScoped(ctx, scope, b.input.SourceEvent.Source.WorkspaceID, noteID, b.input.SourceEvent.Source, frozen.Anchor); err != nil {
			return out, err
		}
	} else {
		groups := map[domain.SynthesisSourceVersion][]domain.SynthesisSourceRef{}
		for _, ref := range refs {
			groups[ref.Source] = append(groups[ref.Source], ref)
		}
		for source, evidence := range groups {
			binding := *frozen.Anchor
			binding.AllowedSources = evidence
			if err := b.store.dependencies.Anchors.VerifySynthesisAnchorAdmissionScoped(ctx, scope, b.input.SourceEvent.Source.WorkspaceID, noteID, source, &binding); err != nil {
				return out, err
			}
		}
	}
	bindings := app.SynthesisBodyRefreshPublications(b.input.BodyRefresh)
	for _, n := range b.input.Notes {
		if n.PublicationID != "" {
			bindings = append(bindings, app.SynthesisPublicationBinding{WorkspaceID: n.Note.WorkspaceID, NoteID: n.Note.ID, RevisionID: n.Revision.ID, PublicationID: n.PublicationID, ProjectionHash: n.Revision.Hash})
		}
	}
	if len(bindings) > 0 {
		raw, err := json.Marshal(bindings)
		if err != nil {
			return out, err
		}
		if err = tx.Exec(`SELECT organizing.verify_synthesis_published_bindings(?::jsonb)`, organizingJSONB(raw)).Error; err != nil {
			return out, err
		}
	}
	note, err := loadSynthesisNote(tx, b.input.SourceEvent.Source.WorkspaceID, noteID, true)
	if err != nil {
		return out, err
	}
	if note.Version != frozen.Note.Version || note.CurrentRevisionID != frozen.Revision.ID || note.DocumentID != frozen.Note.DocumentID {
		return out, manuscriptStoreConflict("latest synthesis baseline changed")
	}
	if frozen.Anchor == nil {
		// CreateAnchor 先锁定笔记；获取该锁后重新检查锚点缺失，防止并发创建的锚点在两次读取之间漏过检查。
		if err := b.store.dependencies.Anchors.VerifySynthesisAnchorAdmissionScoped(ctx, scope, note.WorkspaceID, note.ID, b.input.SourceEvent.Source, nil); err != nil {
			return out, err
		}
	}
	latest, err := loadSynthesisRevision(tx, note.WorkspaceID, note.ID, note.CurrentRevisionID)
	if err != nil {
		return out, err
	}
	if latest.Hash != frozen.Revision.Hash {
		return out, manuscriptStoreConflict("latest synthesis projection changed")
	}
	owner, err := b.store.dependencies.Authoring.ReadGeneratedDocumentScoped(ctx, scope, note.WorkspaceID, note.DocumentID, note.ID)
	if err != nil {
		return out, err
	}
	if owner.Revision.ID != latest.ArticleRevisionID || owner.Revision.ContentHash != latest.ContentHash || owner.Revision.RevisionNo != int(latest.ArticleRevisionNo) || owner.Revision.CreatedByType != "AGENT" {
		return out, manuscriptStoreConflict("latest authoring owner changed")
	}
	if err := verifyManuscriptPending(tx, note.WorkspaceID, note.DocumentID, owner.Revision.ID); err != nil {
		return out, err
	}
	authority := app.SynthesisManuscriptAuthority{DocumentID: note.DocumentID, DocumentVersion: owner.Document.Version, NoteVersion: note.Version, LatestArticleID: latest.ArticleRevisionID, TargetPath: owner.Document.CanonicalPath, RootGrantID: root.GrantID, RootFingerprint: root.Fingerprint, WorkspaceBindingVersion: root.BindingVersion}
	if frozen.Anchor != nil {
		authority.Anchor = *frozen.Anchor
	}
	var publication struct {
		RevisionID       string
		PublicationID    string
		ProposalCommitID string
		GitCommit        string
	}
	query := tx.Raw(`SELECT r.id AS revision_id,b.id AS publication_id,pc.id AS proposal_commit_id,b.git_commit
 FROM core.document d JOIN organizing.synthesis_revision r ON r.workspace_id=d.workspace_id AND r.document_id=d.id AND r.article_revision_id=d.current_published_revision_id
 JOIN organizing.synthesis_proven_publication proven ON proven.workspace_id=r.workspace_id AND proven.revision_id=r.id
 JOIN authoring.document_publication_binding b ON b.id=proven.publication_id AND b.workspace_id=proven.workspace_id
 JOIN change_control.proposal_commit pc ON pc.workspace_id=b.workspace_id AND pc.proposal_id=b.proposal_id AND pc.revision_id=b.proposal_revision_id AND pc.result_hash=b.content_hash AND pc.git_commit=b.git_commit
 WHERE d.workspace_id=? AND d.id=? AND b.status='PUBLISHED' AND b.target_path=d.canonical_path`, string(note.WorkspaceID), string(note.DocumentID)).Scan(&publication)
	if query.Error != nil {
		return out, query.Error
	}
	var published *domain.SynthesisRevision
	if owner.Document.CurrentPublishedRevisionID != "" {
		if query.RowsAffected != 1 {
			return out, manuscriptStoreConflict("published document lacks exact synthesis proof")
		}
		p, err := loadSynthesisRevision(tx, note.WorkspaceID, note.ID, foundation.ID(publication.RevisionID))
		if err != nil {
			return out, err
		}
		// 递归祖先查询使用不可变父指针；其他所属模块或脱离关系的发布记录均不能作为工作区共同祖先。
		var ancestor bool
		err = tx.Raw(`WITH RECURSIVE ancestors AS (SELECT id,parent_revision_id FROM organizing.synthesis_revision WHERE workspace_id=? AND note_id=? AND id=? UNION ALL SELECT r.id,r.parent_revision_id FROM organizing.synthesis_revision r JOIN ancestors a ON r.id=a.parent_revision_id WHERE r.workspace_id=? AND r.note_id=?) SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=?)`, string(note.WorkspaceID), string(note.ID), string(latest.ID), string(note.WorkspaceID), string(note.ID), string(p.ID)).Scan(&ancestor).Error
		if err != nil {
			return out, err
		}
		if !ancestor {
			return out, manuscriptStoreConflict("published revision is not a latest ancestor")
		}
		published = &p
		authority.PublishedPublicationID = foundation.ID(publication.PublicationID)
		authority.PublishedProposalCommitID = foundation.ID(publication.ProposalCommitID)
		authority.PublishedGitCommit = publication.GitCommit
	} else if query.RowsAffected != 0 {
		return out, manuscriptStoreConflict("unexpected published baseline")
	}
	var generated *app.SynthesisGeneratedNote
	for i := range b.generation.Notes {
		if b.generation.Notes[i].NoteID == note.ID {
			generated = &b.generation.Notes[i]
		}
	}
	if generated == nil {
		return out, manuscriptStoreInvalid("generation omitted manuscript target")
	}
	delta, err := app.SynthesisMachineDelta(b.input, *generated, &latest)
	if err != nil {
		return out, err
	}
	if !delta.Changed {
		return out, manuscriptStoreInvalid("source-only change does not create a manuscript")
	}
	machine := domain.SynthesisManuscriptMachine{WorkspaceID: note.WorkspaceID, NoteID: note.ID, MachineTitle: latest.Title, MachineItems: delta.Items, IneligibleItemIDs: []foundation.ID{}}
	out = app.SynthesisManuscriptPrepared{Authority: authority, GenerationInput: b.input, Generation: b.generation, SemanticModelRunID: b.semantic.ModelRunID, SemanticOutputHash: b.semantic.OutputHash, MergeInput: app.SynthesisManuscriptMergeInput{Latest: latest, Published: published, NextMachine: machine}}
	return out, nil
}

func verifyManuscriptPending(tx *gorm.DB, workspace, document, latest foundation.ID) error {
	return verifyManuscriptPendingState(tx, workspace, document, latest, false)
}

func verifyManuscriptPendingState(tx *gorm.DB, workspace, document, latest foundation.ID, unreviewedNeedsRevision bool) error {
	var reserved bool
	if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM authoring.document_publication_reservation WHERE workspace_id=? AND document_id=? AND status='PENDING') OR EXISTS(SELECT 1 FROM authoring.document_publication_binding WHERE workspace_id=? AND document_id=? AND (status='RECOVERY_REQUIRED' OR (status='PENDING' AND article_revision_id<>?)))`, string(workspace), string(document), string(workspace), string(document), string(latest)).Scan(&reserved).Error; err != nil {
		return err
	}
	if reserved {
		return manuscriptStoreConflict("document has an unfinished publication reservation")
	}
	var invalid bool
	err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM authoring.document_publication_binding b LEFT JOIN change_control.proposal p ON p.id=b.proposal_id AND p.workspace_id=b.workspace_id WHERE b.workspace_id=? AND b.document_id=? AND b.article_revision_id=? AND ((b.status='RECOVERY_REQUIRED' OR (b.status='CLOSED' AND NOT (? AND b.error_code='AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION' AND p.status='needs_revision'))) OR p.current_revision_id IS DISTINCT FROM b.proposal_revision_id OR (b.status IN ('PENDING','CLOSED') AND (p.status NOT IN ('ready_for_review',CASE WHEN ? THEN 'needs_revision' ELSE 'ready_for_review' END) OR p.workflow_run_id IS NOT NULL OR EXISTS(SELECT 1 FROM change_control.approval a WHERE a.proposal_id=p.id) OR EXISTS(SELECT 1 FROM change_control.proposal_revision_dispatch a WHERE a.proposal_id=p.id) OR EXISTS(SELECT 1 FROM change_control.tool_authorization a WHERE a.proposal_id=p.id) OR EXISTS(SELECT 1 FROM change_control.writeback_execution a WHERE a.proposal_id=p.id) OR EXISTS(SELECT 1 FROM change_control.proposal_commit a WHERE a.proposal_id=p.id)))))`, string(workspace), string(document), string(latest), unreviewedNeedsRevision, unreviewedNeedsRevision).Scan(&invalid).Error
	if err != nil {
		return err
	}
	if invalid {
		return manuscriptStoreConflict("pending publication is not safely retireable")
	}
	return nil
}
