package owner

import (
	"context"
	"errors"
	"testing"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestGoalCatalogSkipsChangedSourceWithoutLosingCursor(t *testing.T) {
	workspace, version, projection, span := knowledgeDirectoryID(1), knowledgeDirectoryID(2), knowledgeDirectoryID(3), knowledgeDirectoryID(4)
	view := knowledgeDirectoryProfile(t, workspace, version, projection, 80, []capturedomain.ProfilePoint{{Text: "Redis TTL", SourceSpanIDs: []foundation.ID{span}}}, nil, []foundation.ID{span})
	ref := knowledgeDirectorySynthesisRef(workspace, version, projection, span)
	next := ref.Source.SourceID
	profiles := goalCatalogProfilesFake{captureapp.ProfileDirectoryPage{Items: []captureapp.ProfileDirectoryEntry{{SourceID: ref.Source.SourceID, Title: ref.Title, ContentArtifactID: ref.Source.ContentArtifactID, ContentHash: ref.Source.ContentHash, View: view}}, NextAfterSourceID: &next}}
	sources := &goalCatalogSourcesFake{err: foundation.NewError(foundation.ErrorVersionConflict, "SYNTHESIS_SOURCE_STALE", false, errors.New("source changed"))}
	reader, err := NewSynthesisGoalCatalogReader(profiles, sources)
	if err != nil {
		t.Fatal(err)
	}
	query := app.SynthesisGoalCatalogQuery{WorkspaceID: workspace, Limit: 1}
	page, err := reader.ReadSynthesisGoalCatalog(t.Context(), query)
	if err != nil || len(page.Items) != 0 || page.NextAfterSourceID == nil || *page.NextAfterSourceID != next {
		t.Fatalf("stale source lost pagination %+v %v", page, err)
	}
	sources.err = errors.New("database unavailable")
	if _, err := reader.ReadSynthesisGoalCatalog(t.Context(), query); err == nil {
		t.Fatal("infrastructure failure treated as empty catalog")
	}
	sources.err = nil
	sources.refs = []domain.SynthesisSourceRef{ref}
	valid, err := reader.ReadSynthesisGoalCatalog(t.Context(), query)
	if err != nil || len(valid.Items) != 1 {
		t.Fatalf("valid catalog %+v %v", valid, err)
	}
	// 为指定知识点返回的替代片段不能被重新标记成原片段。
	sources.refs[0].SourceSpanID = knowledgeDirectoryID(90)
	if _, err := reader.ReadSynthesisGoalCatalog(t.Context(), query); err == nil {
		t.Fatal("replacement evidence accepted")
	}
}

func TestGoalCatalogDefersWhenCurrentSourceProfileIsPending(t *testing.T) {
	workspace := knowledgeDirectoryID(11)
	profiles := goalCatalogProfilesFake{captureapp.ProfileDirectoryPage{Items: []captureapp.ProfileDirectoryEntry{}}}
	sources := &goalCatalogSourcesFake{readiness: app.SynthesisGoalCatalogReadiness{DeferredCode: synthesisGoalSourcePending}}
	reader, err := NewSynthesisGoalCatalogReader(profiles, sources)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.ReadSynthesisGoalCatalog(t.Context(), app.SynthesisGoalCatalogQuery{WorkspaceID: workspace, Limit: 1})
	if err != nil || page.DeferredCode != synthesisGoalSourcePending || len(page.Items) != 0 || page.NextAfterSourceID != nil || sources.resolveCalls != 0 {
		t.Fatalf("pending source froze catalog %+v %v", page, err)
	}
	sources.readiness.DeferredCode = synthesisGoalProfileFailed
	page, err = reader.ReadSynthesisGoalCatalog(t.Context(), app.SynthesisGoalCatalogQuery{WorkspaceID: workspace, Limit: 1})
	if err != nil || page.DeferredCode != synthesisGoalProfileFailed {
		t.Fatalf("profile failure was not surfaced %+v %v", page, err)
	}
}

type goalCatalogProfilesFake struct {
	page captureapp.ProfileDirectoryPage
}

func (f goalCatalogProfilesFake) ListProfileDirectory(context.Context, captureapp.ProfileDirectoryQuery) (captureapp.ProfileDirectoryPage, error) {
	return f.page, nil
}

type goalCatalogSourcesFake struct {
	refs         []domain.SynthesisSourceRef
	err          error
	readiness    app.SynthesisGoalCatalogReadiness
	readinessErr error
	resolveCalls int
}

func (f *goalCatalogSourcesFake) ResolveAnchorDiscoverySources(context.Context, domain.SynthesisSourceVersion, []foundation.ID) ([]domain.SynthesisSourceRef, error) {
	f.resolveCalls++
	return f.refs, f.err
}

func (f *goalCatalogSourcesFake) ReadSynthesisGoalCatalogReadiness(context.Context, app.SynthesisGoalCatalogQuery) (app.SynthesisGoalCatalogReadiness, error) {
	return f.readiness, f.readinessErr
}
