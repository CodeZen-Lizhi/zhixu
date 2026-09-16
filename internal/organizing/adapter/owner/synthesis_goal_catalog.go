package owner

import (
	"context"
	"errors"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type SynthesisGoalCatalogReader struct {
	profiles  captureapp.ProfileDirectoryReader
	sources   app.AnchorDiscoverySourceResolver
	readiness app.SynthesisGoalCatalogReadinessReader
}

func NewSynthesisGoalCatalogReader(profiles captureapp.ProfileDirectoryReader, sources app.AnchorDiscoverySourceResolver) (*SynthesisGoalCatalogReader, error) {
	if nilKnowledgeDirectoryDependency(profiles) || nilKnowledgeDirectoryDependency(sources) {
		return nil, dependencyUnavailable("goal catalog dependencies are unavailable")
	}
	readiness, ok := sources.(app.SynthesisGoalCatalogReadinessReader)
	if !ok || nilKnowledgeDirectoryDependency(readiness) {
		return nil, dependencyUnavailable("goal catalog readiness is unavailable")
	}
	return &SynthesisGoalCatalogReader{profiles: profiles, sources: sources, readiness: readiness}, nil
}

func (reader *SynthesisGoalCatalogReader) ReadSynthesisGoalCatalog(ctx context.Context, query app.SynthesisGoalCatalogQuery) (app.SynthesisGoalCatalogPage, error) {
	result := app.SynthesisGoalCatalogPage{Items: []app.SynthesisGoalCatalogItem{}}
	if reader == nil || nilKnowledgeDirectoryDependency(reader.profiles) || nilKnowledgeDirectoryDependency(reader.sources) || nilKnowledgeDirectoryDependency(reader.readiness) {
		return result, dependencyUnavailable("goal catalog is unavailable")
	}
	if ctx == nil || !validID(query.WorkspaceID) || query.AfterSourceID != "" && !validID(query.AfterSourceID) || query.SourceVersionID != "" && !validID(query.SourceVersionID) || query.Limit < 1 || query.Limit > captureapp.MaxProfileDirectoryPage {
		return result, invalid("goal catalog query is invalid")
	}
	// Profile 目录会省略仍在解析或没有可用 Profile 的来源。先检查就绪状态，再读取目录，避免两次读取之间完成的 Profile 将此前遗漏的行变为空的已完成目录。检查后到达的来源不属于本次就绪观察范围。
	readiness, err := reader.readiness.ReadSynthesisGoalCatalogReadiness(ctx, query)
	if err != nil {
		return result, err
	}
	if readiness.DeferredCode != "" {
		result.DeferredCode = readiness.DeferredCode
		return result, nil
	}
	page, err := reader.profiles.ListProfileDirectory(ctx, captureapp.ProfileDirectoryQuery{WorkspaceID: query.WorkspaceID, AfterSourceID: query.AfterSourceID, SourceVersionID: query.SourceVersionID, Limit: query.Limit})
	if err != nil {
		return result, err
	}
	if page.Items == nil || len(page.Items) > query.Limit {
		return result, inconsistent("goal catalog page is invalid")
	}
	last := query.AfterSourceID
	for _, entry := range page.Items {
		view := entry.View
		if !validID(entry.SourceID) || entry.SourceID <= last || view.Profile.Validate() != nil || view.Profile.WorkspaceID != query.WorkspaceID || view.Revision == nil || view.Profile.CurrentRevisionID != view.Revision.ID || view.Profile.ID != view.Revision.ProfileID {
			return result, inconsistent("goal catalog profile binding is invalid")
		}
		last = entry.SourceID
		source := domain.SynthesisSourceVersion{WorkspaceID: query.WorkspaceID, SourceID: entry.SourceID, SourceVersionID: view.Profile.SourceVersionID, ContentArtifactID: entry.ContentArtifactID, ContentHash: entry.ContentHash, ParseProjectionID: view.Revision.ParseProjectionID}
		if source.Validate() != nil || query.SourceVersionID != "" && source.SourceVersionID != query.SourceVersionID {
			return result, inconsistent("goal catalog source binding is invalid")
		}
		directory, err := directoryFromRevision(app.SourceKnowledgeDirectoryQuery{WorkspaceID: query.WorkspaceID, SourceVersionID: source.SourceVersionID}, view.Revision, view.Evidence, view.Profile.Status)
		if err != nil {
			return result, err
		}
		if len(directory.Points) == 0 {
			continue
		}
		// 现有仅使用元数据的解析器会排除派生笔记并重新检查来源是否最新；生成前会再次解析所有已选证据。
		refs, err := reader.sources.ResolveAnchorDiscoverySources(ctx, source, []foundation.ID{directory.Points[0].SourceSpanIDs[0]})
		if err != nil {
			var classified *foundation.Error
			if errors.As(err, &classified) && (classified.Code == "SYNTHESIS_SOURCE_NOT_FOUND" || classified.Code == "SYNTHESIS_SOURCE_STALE") {
				continue
			}
			return result, err
		}
		if len(refs) != 1 || refs[0].Source != source || refs[0].SourceSpanID != directory.Points[0].SourceSpanIDs[0] || refs[0].Title != entry.Title || refs[0].Validate() != nil {
			return result, inconsistent("goal catalog evidence binding is invalid")
		}
		result.Items = append(result.Items, app.SynthesisGoalCatalogItem{Source: source, Title: entry.Title, Directory: directory})
	}
	if page.NextAfterSourceID != nil && (len(page.Items) != query.Limit || *page.NextAfterSourceID != last) {
		return result, inconsistent("goal catalog cursor is invalid")
	}
	result.NextAfterSourceID = page.NextAfterSourceID
	return result, nil
}

var _ app.SynthesisGoalCatalogReader = (*SynthesisGoalCatalogReader)(nil)

type SynthesisGoalCatalogSnapshotReader struct {
	profiles captureapp.ProfileRevisionReader
}

func NewSynthesisGoalCatalogSnapshotReader(profiles captureapp.ProfileRevisionReader) (*SynthesisGoalCatalogSnapshotReader, error) {
	if nilKnowledgeDirectoryDependency(profiles) {
		return nil, dependencyUnavailable("frozen goal profile reader is unavailable")
	}
	return &SynthesisGoalCatalogSnapshotReader{profiles: profiles}, nil
}

func (reader *SynthesisGoalCatalogReader) ReadSynthesisGoalCatalogSnapshot(ctx context.Context, batch app.SynthesisGoalCatalogBatch) ([]app.SynthesisGoalCatalogItem, error) {
	if reader == nil {
		return nil, dependencyUnavailable("frozen goal profile reader is unavailable")
	}
	profiles, ok := reader.profiles.(captureapp.ProfileRevisionReader)
	if !ok {
		return nil, dependencyUnavailable("frozen goal profile reader is unavailable")
	}
	snapshots, err := NewSynthesisGoalCatalogSnapshotReader(profiles)
	if err != nil {
		return nil, err
	}
	return snapshots.ReadSynthesisGoalCatalogSnapshot(ctx, batch)
}

func (reader *SynthesisGoalCatalogSnapshotReader) ReadSynthesisGoalCatalogSnapshot(ctx context.Context, batch app.SynthesisGoalCatalogBatch) ([]app.SynthesisGoalCatalogItem, error) {
	if reader == nil || ctx == nil || !validID(batch.ID) || !validID(batch.WorkspaceID) || !validID(batch.RequestID) || batch.BatchNo < 1 || batch.Items == nil || len(batch.Items) > captureapp.MaxProfileDirectoryPage {
		return nil, invalid("frozen goal catalog is invalid")
	}
	profiles := reader.profiles

	result := make([]app.SynthesisGoalCatalogItem, 0, len(batch.Items))
	last := batch.AfterSourceID
	for _, binding := range batch.Items {
		if binding.Source.Validate() != nil || binding.Source.WorkspaceID != batch.WorkspaceID || binding.Source.SourceID <= last || !validID(binding.ProfileRevisionID) {
			return nil, inconsistent("frozen goal catalog binding is invalid")
		}
		last = binding.Source.SourceID
		snapshot, err := profiles.GetProfileRevision(ctx, captureapp.ProfileRevisionQuery{WorkspaceID: batch.WorkspaceID, SourceVersionID: binding.Source.SourceVersionID, RevisionID: binding.ProfileRevisionID})
		if err != nil {
			return nil, err
		}
		if snapshot.Revision.ID != binding.ProfileRevisionID || snapshot.Revision.ParseProjectionID != binding.Source.ParseProjectionID {
			return nil, inconsistent("frozen goal profile was replaced")
		}
		directory, err := directoryFromRevision(app.SourceKnowledgeDirectoryQuery{WorkspaceID: batch.WorkspaceID, SourceVersionID: binding.Source.SourceVersionID}, &snapshot.Revision, snapshot.Evidence, "")
		if err != nil {
			return nil, err
		}
		result = append(result, app.SynthesisGoalCatalogItem{Source: binding.Source, Title: binding.Title, Directory: directory})
	}
	return result, nil
}

var _ app.SynthesisGoalCatalogSnapshotReader = (*SynthesisGoalCatalogReader)(nil)
