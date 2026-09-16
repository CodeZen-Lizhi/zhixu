package postgres

import (
	"context"
	"encoding/json"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ app.AcceptedAnchorSourceReader = (*GORMAnchorStore)(nil)
var _ app.SynthesisAnchorAdmissionReader = (*GORMAnchorStore)(nil)
var _ app.SynthesisAnchorAdmissionFence = (*GORMAnchorStore)(nil)

// ReadAcceptedAnchorSources 只描述准入情况；应用时仍必须核验来源和范围，此读取不能授权后续写入。
func (s *GORMAnchorStore) ReadAcceptedAnchorSources(ctx context.Context, workspace, noteID foundation.ID, source domain.SynthesisSourceVersion) ([]domain.SynthesisSourceRef, error) {
	admission, err := s.ReadSynthesisAnchorAdmission(ctx, workspace, noteID, source)
	if err != nil {
		return nil, err
	}
	return admission.AllowedSources, nil
}

// ReadSynthesisAnchorAdmission 对未设锚点的笔记返回空 AnchorID。已有锚点但没有已接受关联的笔记仍无资格接收新来源，因此返回空 AllowedSources。
func (s *GORMAnchorStore) ReadSynthesisAnchorAdmission(ctx context.Context, workspace, noteID foundation.ID, source domain.SynthesisSourceVersion) (app.SynthesisAnchorAdmission, error) {
	out := app.SynthesisAnchorAdmission{}
	if !validID(workspace) || !validID(noteID) || source.Validate() != nil || source.WorkspaceID != workspace {
		return out, app.AnchorInvalid()
	}
	err := s.within(ctx, false, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var row anchorModel
		if err := tx.Where("workspace_id=? AND note_id=?", string(workspace), string(noteID)).Take(&row).Error; err != nil {
			if gormNoRows(err) {
				return nil
			}
			return err
		}
		var scopeRow anchorScopeModel
		if err := tx.Where("workspace_id=? AND anchor_id=? AND version=?", row.WorkspaceID, row.ID, row.ScopeVersion).Take(&scopeRow).Error; err != nil {
			return err
		}
		var scope domain.AnchorScope
		if err := json.Unmarshal(scopeRow.Scope, &scope); err != nil {
			return err
		}
		refs, err := readAcceptedAnchorRefs(tx, workspace, noteID, source)
		if err != nil {
			return err
		}
		out = app.SynthesisAnchorAdmission{AnchorID: foundation.ID(row.ID), ScopeVersion: row.ScopeVersion, Scope: scope, AllowedSources: refs}
		return nil
	})
	return out, err
}

func readAcceptedAnchorRefs(tx *gorm.DB, workspace, noteID foundation.ID, source domain.SynthesisSourceVersion) ([]domain.SynthesisSourceRef, error) {
	var rows []anchorEvidenceModel
	query := tx.Table("organizing.anchor_proposal_evidence AS e").Select("DISTINCT ON (e.source_span_id) e.workspace_id,e.source_id,e.source_version_id,e.content_artifact_id,e.parse_projection_id,e.source_span_id,e.content_hash,e.excerpt_hash,e.title").Joins("JOIN organizing.anchor_proposal p ON p.id=e.proposal_id AND p.workspace_id=e.workspace_id AND p.anchor_id=e.anchor_id").Joins("JOIN organizing.anchor_decision d ON d.proposal_id=p.id AND d.workspace_id=p.workspace_id AND d.anchor_id=p.anchor_id").Joins("JOIN organizing.knowledge_anchor a ON a.id=p.anchor_id AND a.workspace_id=p.workspace_id AND a.scope_version=p.scope_version").Where("a.workspace_id=? AND a.note_id=? AND p.kind=? AND d.decision=? AND e.source_id=? AND e.source_version_id=? AND e.content_artifact_id=? AND e.parse_projection_id=? AND e.content_hash=?", string(workspace), string(noteID), domain.AnchorSourceAssociation, string(domain.AnchorAccepted), string(source.SourceID), string(source.SourceVersionID), string(source.ContentArtifactID), string(source.ParseProjectionID), source.ContentHash).Order("e.source_span_id,p.created_at,p.id").Limit(domain.MaxSynthesisSources + 1)
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > domain.MaxSynthesisSources {
		return nil, app.AnchorInvalid()
	}
	refs := make([]domain.SynthesisSourceRef, 0, len(rows))
	for _, row := range rows {
		ref := row.ref()
		if ref.Validate() != nil || ref.Source != source {
			return nil, app.AnchorConflict()
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// VerifySynthesisAnchorAdmissionScoped 执行应用时的校验。先锁定锚点行，再检查当前范围版本及已接受的精确证据，避免并发范围变更绕过冻结请求。
func (s *GORMAnchorStore) VerifySynthesisAnchorAdmissionScoped(ctx context.Context, scope foundation.TransactionScope, workspace, noteID foundation.ID, source domain.SynthesisSourceVersion, expected *app.SynthesisAnchorBinding) error {
	if !validID(workspace) || !validID(noteID) || source.Validate() != nil || source.WorkspaceID != workspace {
		return app.AnchorInvalid()
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	var row anchorModel
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("workspace_id=? AND note_id=?", string(workspace), string(noteID))
	err = query.Take(&row).Error
	if expected == nil {
		if gormNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return app.AnchorConflict()
	}
	if err != nil {
		if gormNoRows(err) {
			return app.AnchorConflict()
		}
		return err
	}
	if foundation.ID(row.ID) != expected.AnchorID || row.ScopeVersion != expected.ScopeVersion {
		return app.AnchorConflict()
	}
	accepted, err := readAcceptedAnchorRefs(tx, workspace, noteID, source)
	if err != nil {
		return err
	}
	if len(expected.AllowedSources) < 1 || len(expected.AllowedSources) > domain.MaxSynthesisSources {
		return app.AnchorInvalid()
	}
	wanted := make(map[string]bool, len(expected.AllowedSources))
	for _, ref := range expected.AllowedSources {
		if ref.Validate() != nil || ref.Source != source {
			return app.AnchorConflict()
		}
		key, _ := ref.IdentityKey()
		if wanted[key] {
			return app.AnchorConflict()
		}
		wanted[key] = true
	}
	for _, ref := range accepted {
		key, _ := ref.IdentityKey()
		if wanted[key] {
			delete(wanted, key)
		}
	}
	if len(wanted) != 0 {
		return app.AnchorConflict()
	}
	return nil
}
