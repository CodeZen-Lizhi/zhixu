package postgres

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"gorm.io/gorm"
)

// SynthesisSourceReviewRead 组装安全视图，从不导出执行记录行。
type SynthesisSourceReviewRead struct {
	store   *GORMSynthesisManuscriptSourceReviewStore
	sources app.SynthesisSourceReader
}

func NewSynthesisSourceReviewRead(store *GORMSynthesisManuscriptSourceReviewStore, sources app.SynthesisSourceReader) (*SynthesisSourceReviewRead, error) {
	if store == nil || isNilInterface(sources) {
		return nil, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_UNAVAILABLE")
	}
	return &SynthesisSourceReviewRead{store: store, sources: sources}, nil
}
func (r *SynthesisSourceReviewRead) authorize(ctx context.Context, w foundation.ID) error {
	return r.store.within(ctx, func(ctx context.Context, scope foundation.TransactionScope, _ *gorm.DB) error {
		_, err := r.store.runtime.dependencies.Roots.ReadSynthesisManuscriptRootScoped(ctx, scope, w)
		return err
	})
}
func (r *SynthesisSourceReviewRead) GetSourceReviewView(ctx context.Context, w, id foundation.ID) (app.SynthesisSourceReviewView, error) {
	if !validID(w) || !validID(id) {
		return app.SynthesisSourceReviewView{}, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_ID_INVALID")
	}
	view, err := r.store.SourceReviewView(ctx, w, id)
	if err != nil {
		return app.SynthesisSourceReviewView{}, err
	}
	// 业务当前状态检查期间权限发生变化时，不得仅显示过期标记后继续泄露已保存的本地手稿。
	if err = r.authorize(ctx, w); err != nil {
		return app.SynthesisSourceReviewView{}, err
	}
	view.CreatedAt = view.CreatedAt.UTC()
	if view.CompletedAt != nil {
		at := view.CompletedAt.UTC()
		view.CompletedAt = &at
	}
	return view, nil
}
func (r *SynthesisSourceReviewRead) ListSourceReviewViews(ctx context.Context, q app.SynthesisSourceReviewQuery) (app.SynthesisSourceReviewPage, error) {
	out := app.SynthesisSourceReviewPage{WorkspaceID: q.WorkspaceID, ProcessingID: q.ProcessingID, NoteID: q.NoteID, RevisionID: q.RevisionID, Items: []app.SynthesisSourceReviewView{}}
	byProcessing := validID(q.ProcessingID) && q.NoteID == "" && q.RevisionID == ""
	byRevision := q.ProcessingID == "" && validID(q.NoteID) && validID(q.RevisionID)
	if !validID(q.WorkspaceID) || (!byProcessing && !byRevision) || q.Limit < 1 || q.Limit > 100 || (q.AfterID != "" && !validID(q.AfterID)) {
		return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_QUERY_INVALID")
	}
	if err := r.authorize(ctx, q.WorkspaceID); err != nil {
		return out, err
	}
	db := r.store.runtime.dependencies.Candidates.database.WithContext(ctx)
	parent := db.Table("organizing.synthesis_processing").Where("workspace_id=? AND id=?", string(q.WorkspaceID), string(q.ProcessingID))
	if byRevision {
		parent = db.Table("organizing.synthesis_revision").Where("workspace_id=? AND note_id=? AND id=?", string(q.WorkspaceID), string(q.NoteID), string(q.RevisionID))
	}
	var count int64
	if err := parent.Count(&count).Error; err != nil {
		return out, err
	}
	if count != 1 {
		return out, synthesisNotFound()
	}
	base := func() *gorm.DB {
		query := db.Table("organizing.synthesis_manuscript_source_review").Where("workspace_id=?", string(q.WorkspaceID))
		if byProcessing {
			return query.Where("origin_processing_id=?", string(q.ProcessingID))
		}
		return query.Where(`EXISTS (SELECT 1 FROM jsonb_array_elements(convert_from(snapshot,'UTF8')::jsonb->'targets') AS target WHERE target->>'note_id'=? AND target->>'base_revision_id'=?)`, string(q.NoteID), string(q.RevisionID))
	}
	if q.AfterID != "" {
		if err := base().Where("id=?", string(q.AfterID)).Count(&count).Error; err != nil {
			return out, err
		}
		if count != 1 {
			return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CURSOR_INVALID")
		}
	}
	query := base()
	if q.AfterID != "" {
		query = query.Where("id>?", string(q.AfterID))
	}
	var ids []string
	if err := query.Order("id ASC").Limit(q.Limit+1).Pluck("id", &ids).Error; err != nil {
		return out, err
	}
	if len(ids) > q.Limit {
		ids = ids[:q.Limit]
		out.NextAfterID = foundation.ID(ids[len(ids)-1])
	}
	for _, id := range ids {
		view, err := r.GetSourceReviewView(ctx, q.WorkspaceID, foundation.ID(id))
		if err != nil {
			return out, err
		}
		if byRevision {
			targets := []app.SynthesisSourceReviewTargetView{}
			for _, target := range view.Targets {
				if target.NoteID == q.NoteID && target.BaseRevisionID == q.RevisionID {
					targets = append(targets, target)
				}
			}
			if len(targets) == 0 {
				return out, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CORRUPT")
			}
			view.Targets = targets
		}
		out.Items = append(out.Items, view)
	}
	if err := r.authorize(ctx, q.WorkspaceID); err != nil {
		return app.SynthesisSourceReviewPage{}, err
	}
	return out, nil
}
func (r *SynthesisSourceReviewRead) OpenSourceReviewEvidence(ctx context.Context, w, review, id foundation.ID) (app.SynthesisSourceReviewEvidenceView, app.SynthesisSourceView, error) {
	var empty app.SynthesisSourceReviewEvidenceView
	var source app.SynthesisSourceView
	if !validID(id) {
		return empty, source, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_ID_INVALID")
	}
	view, err := r.GetSourceReviewView(ctx, w, review)
	if err != nil {
		return empty, source, err
	}
	for _, target := range view.Targets {
		for _, evidence := range target.Evidence {
			if evidence.ID != id {
				continue
			}
			if evidence.Source.Validate() != nil || evidence.Source.Source.WorkspaceID != w {
				return empty, source, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CORRUPT")
			}
			source, err = r.sources.OpenSynthesisSource(ctx, evidence.Source)
			if err != nil {
				return empty, app.SynthesisSourceView{}, err
			}
			if source.Reference != evidence.Source || source.Validate(w) != nil {
				return empty, app.SynthesisSourceView{}, app.SourceReviewError("SYNTHESIS_SOURCE_REVIEW_CORRUPT")
			}
			if err = r.authorize(ctx, w); err != nil {
				return empty, app.SynthesisSourceView{}, err
			}
			return evidence, source, nil
		}
	}
	return empty, source, synthesisNotFound()
}

var _ app.SynthesisSourceReviewReader = (*SynthesisSourceReviewRead)(nil)
