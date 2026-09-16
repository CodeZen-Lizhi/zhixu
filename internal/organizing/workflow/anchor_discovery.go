package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type AnchorDiscoveryDispatcher struct {
	Store     app.AnchorDiscoveryStore
	Directory app.KnowledgeDirectoryReader
	Sources   app.AnchorDiscoverySourceResolver
	Requests  app.AnchorRecommendationSourceRequester
}

// 发现过程不调用模型，也不准入来源。它通过来源的不可变知识目录
// 缩小原始材料范围，并将建议请求加入队列。
func (d *AnchorDiscoveryDispatcher) DispatchBatch(ctx context.Context, limit int) (int, error) {
	if d == nil || ctx == nil || limit < 1 || limit > 100 || nilScopedDependency(d.Store) || nilScopedDependency(d.Directory) || nilScopedDependency(d.Sources) || nilScopedDependency(d.Requests) {
		return 0, app.AnchorInvalid()
	}
	if _, err := d.Store.SeedAnchorSourceDiscoveries(ctx, limit); err != nil {
		return 0, err
	}
	items, err := d.Store.ListDueAnchorSourceDiscoveries(ctx, limit)
	if err != nil {
		return 0, err
	}
	if len(items) > limit {
		return 0, app.AnchorConflict()
	}
	completed := 0
	var failures []error
	for _, item := range items {
		done, err := d.discover(ctx, &item)
		if err != nil {
			code, stale := "ANCHOR_DISCOVERY_UNAVAILABLE", false
			var classified *foundation.Error
			if errors.As(err, &classified) {
				code = classified.Code
				stale = classified.Kind == foundation.ErrorNotFound || classified.Kind == foundation.ErrorVersionConflict
			}
			failures = append(failures, errors.Join(err, d.Store.DeferAnchorSourceDiscovery(ctx, item, code, stale)))
		}
		if done {
			completed++
		}
	}
	return completed, errors.Join(failures...)
}

func (d *AnchorDiscoveryDispatcher) discover(ctx context.Context, item *app.AnchorSourceDiscovery) (bool, error) {
	if !validID(item.ID) || !validID(item.WorkspaceID) || !validID(item.AnchorID) || item.ScopeVersion < 1 || item.Version < 1 || item.Source.Validate() != nil || item.Source.WorkspaceID != item.WorkspaceID {
		return false, app.AnchorInvalid()
	}
	if item.Status == app.AnchorDiscoveryWaiting {
		directory, err := d.Directory.GetSourceKnowledgeDirectory(ctx, app.SourceKnowledgeDirectoryQuery{WorkspaceID: item.WorkspaceID, SourceVersionID: item.Source.SourceVersionID})
		if err != nil {
			return false, err
		}
		if directory.WorkspaceID != item.WorkspaceID || directory.SourceVersionID != item.Source.SourceVersionID {
			return false, app.AnchorConflict()
		}
		if directory.Status != app.KnowledgeDirectoryAnalyzed {
			return false, d.Store.DeferAnchorSourceDiscovery(ctx, *item, "ANCHOR_PROFILE_NOT_READY", false)
		}
		if directory.ParseProjectionID != item.Source.ParseProjectionID {
			return false, d.Store.DeferAnchorSourceDiscovery(ctx, *item, "ANCHOR_PROFILE_PROJECTION_STALE", true)
		}
		if !validID(directory.ProfileRevisionID) || len(directory.Points) == 0 {
			return false, app.AnchorInvalid()
		}
		selected := make(map[foundation.ID]bool)
		for _, point := range directory.Points {
			if point.Locator.ProfileRevisionID != directory.ProfileRevisionID || len(point.SourceSpanIDs) == 0 {
				return false, app.AnchorConflict()
			}
			for _, id := range point.SourceSpanIDs {
				if !validID(id) {
					return false, app.AnchorConflict()
				}
				selected[id] = true
			}
		}
		if len(selected) > domain.MaxSynthesisSources {
			return false, app.AnchorInvalid()
		}
		ids := make([]foundation.ID, 0, len(selected))
		for id := range selected {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		refs, err := d.Sources.ResolveAnchorDiscoverySources(ctx, item.Source, ids)
		if err != nil {
			return false, err
		}
		if len(refs) != len(ids) {
			return false, app.AnchorConflict()
		}
		for _, ref := range refs {
			if ref.Validate() != nil || ref.Source != item.Source || !selected[ref.SourceSpanID] {
				return false, app.AnchorConflict()
			}
			delete(selected, ref.SourceSpanID)
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].SourceSpanID < refs[j].SourceSpanID })
		frozen, err := d.Store.FreezeAnchorSourceDiscovery(ctx, *item, directory.ProfileRevisionID, refs)
		if err != nil {
			return false, err
		}
		*item = frozen
	}
	if item.Status == app.AnchorDiscoveryRequested || item.Status == app.AnchorDiscoveryStale {
		return false, nil
	}
	if item.Status != app.AnchorDiscoveryPrepared || len(item.Evidence) == 0 || len(item.Evidence) > domain.MaxSynthesisSources {
		return false, app.AnchorConflict()
	}
	requests := make([]foundation.ID, 0, (len(item.Evidence)+31)/32)
	for start := 0; start < len(item.Evidence); start += 32 {
		end := min(start+32, len(item.Evidence))
		request, err := d.Requests.RequestAnchorSourceRecommendation(ctx, app.RequestAnchorSourceRecommendationCommand{
			WorkspaceID: item.WorkspaceID, AnchorID: item.AnchorID, ExpectedScopeVersion: item.ScopeVersion, Source: item.Source,
			Evidence: item.Evidence[start:end], IdempotencyKey: fmt.Sprintf("anchor-discovery:%s:%d", item.ID, start/32),
		})
		if err != nil {
			return false, err
		}
		if !validID(request.ID) || request.WorkspaceID != item.WorkspaceID || request.AnchorID != item.AnchorID || request.ExpectedScopeVersion != item.ScopeVersion || request.Source == nil || *request.Source != item.Source {
			return false, app.AnchorConflict()
		}
		requests = append(requests, request.ID)
	}
	if err := d.Store.CompleteAnchorSourceDiscovery(ctx, *item, requests); err != nil {
		return false, err
	}
	return true, nil
}
