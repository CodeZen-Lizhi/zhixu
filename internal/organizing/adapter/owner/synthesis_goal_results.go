package owner

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// 每页解析一个不可变的选择切片：最多 32 个知识点，每点最多 500 个证据片段。生成前另行打开原始字节；元数据可用并不证明来源内容可用。
type SynthesisGoalMaterialReader struct {
	results app.GoalSelectionResultReader
	sources app.AnchorDiscoverySourceResolver
}

func NewSynthesisGoalMaterialReader(results app.GoalSelectionResultReader, sources app.AnchorDiscoverySourceResolver) (*SynthesisGoalMaterialReader, error) {
	if nilKnowledgeDirectoryDependency(results) || nilKnowledgeDirectoryDependency(sources) {
		return nil, dependencyUnavailable("goal material dependencies are unavailable")
	}
	return &SynthesisGoalMaterialReader{results: results, sources: sources}, nil
}

func (r *SynthesisGoalMaterialReader) ReadGoalSourceMaterials(ctx context.Context, q app.GoalSelectionResultQuery) (app.GoalSourceMaterialPage, error) {
	var result app.GoalSourceMaterialPage
	if r == nil || ctx == nil || !validID(q.WorkspaceID) || !validID(q.RequestID) || q.Limit != 1 || q.AfterSelectionID != "" && !validID(q.AfterSelectionID) {
		return result, invalid("goal material page requires one selection")
	}
	page, err := r.results.ReadGoalSelectionResults(ctx, q)
	if err != nil {
		return result, err
	}
	if page.Progress.Request.ID != q.RequestID || page.Progress.Request.WorkspaceID != q.WorkspaceID || page.Items == nil || len(page.Items) > 1 || !page.Progress.Ready() && (len(page.Items) != 0 || page.NextAfterSelectionID != "") {
		return result, inconsistent("goal result page binding is invalid")
	}
	result = app.GoalSourceMaterialPage{Progress: page.Progress, Items: []app.GoalSourceMaterial{}, NextAfterSelectionID: page.NextAfterSelectionID}
	if len(page.Items) == 0 {
		if page.NextAfterSelectionID != "" {
			return app.GoalSourceMaterialPage{}, inconsistent("empty goal result page has a cursor")
		}
		return result, nil
	}
	selection := page.Items[0]
	if !validID(selection.SelectionID) || selection.SelectionID <= q.AfterSelectionID || !validID(selection.ModelRunID) || selection.Points == nil || len(selection.Points) > app.MaxGoalSelectionPoints || page.NextAfterSelectionID != "" && page.NextAfterSelectionID != selection.SelectionID {
		return app.GoalSourceMaterialPage{}, inconsistent("goal selection result is invalid")
	}
	if len(selection.Points) == 0 {
		return result, nil
	}
	source := selection.Points[0].Source
	revision := selection.Points[0].Locator.ProfileRevisionID
	if source.Validate() != nil || source.WorkspaceID != q.WorkspaceID || !validID(revision) {
		return app.GoalSourceMaterialPage{}, inconsistent("goal point source is invalid")
	}
	indices := map[foundation.ID]int{}
	ids := []foundation.ID{}
	seenPoints := map[app.KnowledgePointLocator]bool{}
	for _, point := range selection.Points {
		locator := point.Locator
		if point.Source != source || locator.ProfileRevisionID != revision || locator.Index < 0 || locator.Index >= 256 || locator.Kind != app.KnowledgePointKindKnowledgePoint && locator.Kind != app.KnowledgePointKindExample || seenPoints[locator] || len(point.SourceSpanIDs) < 1 || len(point.SourceSpanIDs) > 500 || point.Reason == "" {
			return app.GoalSourceMaterialPage{}, inconsistent("goal point binding is invalid")
		}
		seenPoints[locator] = true
		seenSpans := map[foundation.ID]bool{}
		for _, id := range point.SourceSpanIDs {
			if !validID(id) || seenSpans[id] {
				return app.GoalSourceMaterialPage{}, inconsistent("goal point evidence is invalid")
			}
			seenSpans[id] = true
			index, found := indices[id]
			if !found {
				index = len(result.Items)
				indices[id] = index
				ids = append(ids, id)
				result.Items = append(result.Items, app.GoalSourceMaterial{Points: []app.GoalSourcePointBinding{}})
			}
			result.Items[index].Points = append(result.Items[index].Points, app.GoalSourcePointBinding{SelectionID: selection.SelectionID, ModelRunID: selection.ModelRunID, Locator: locator, Reason: point.Reason})
		}
	}
	// 解析每个唯一片段。所属模块单次查询最多 256 项；知识点超出该限制时继续查询，不丢失证据。
	var title string
	for offset := 0; offset < len(ids); offset += domain.MaxSynthesisSources {
		end := min(offset+domain.MaxSynthesisSources, len(ids))
		batch := ids[offset:end]
		refs, err := r.sources.ResolveAnchorDiscoverySources(ctx, source, batch)
		if err != nil {
			return app.GoalSourceMaterialPage{}, err
		}
		if len(refs) != len(batch) {
			return app.GoalSourceMaterialPage{}, inconsistent("goal evidence resolution is incomplete")
		}
		expected := make(map[foundation.ID]bool, len(batch))
		for _, id := range batch {
			expected[id] = true
		}
		for _, ref := range refs {
			if ref.Validate() != nil || ref.Source != source || !expected[ref.SourceSpanID] || title != "" && ref.Title != title {
				return app.GoalSourceMaterialPage{}, inconsistent("goal evidence was replaced")
			}
			delete(expected, ref.SourceSpanID)
			title = ref.Title
			result.Items[indices[ref.SourceSpanID]].Reference = ref
		}
		if len(expected) != 0 {
			return app.GoalSourceMaterialPage{}, inconsistent("goal evidence resolution is incomplete")
		}
	}
	return result, nil
}

var _ app.GoalSourceMaterialReader = (*SynthesisGoalMaterialReader)(nil)
