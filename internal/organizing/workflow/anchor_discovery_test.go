package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestAnchorDiscoveryWaitsForMetadataAndResumesFrozenBatches(t *testing.T) {
	f := newDiscoveryFixture(33)
	d := AnchorDiscoveryDispatcher{Store: f, Directory: f, Sources: f, Requests: f}
	if n, err := d.DispatchBatch(t.Context(), 10); err != nil || n != 0 || f.resolved != 0 || len(f.requests) != 0 {
		t.Fatalf("unanalyzed file was read: %d %v %+v", n, err, f)
	}
	f.directory.Status = app.KnowledgeDirectoryAnalyzed
	f.failSecond = true
	if n, err := d.DispatchBatch(t.Context(), 10); err == nil || n != 0 || len(f.requests) != 1 || f.item.Status != app.AnchorDiscoveryPrepared {
		t.Fatalf("partial request creation: %d %v %+v", n, err, f)
	}
	// 即使已有更新的画像，下一轮仍必须使用之前冻结的目录。
	// 第一个批次的回执必须原样复用。
	f.directory.Points = nil
	f.failSecond = false
	if n, err := d.DispatchBatch(t.Context(), 10); err != nil || n != 1 || len(f.requests) != 2 || f.resolved != 1 || f.item.Status != app.AnchorDiscoveryRequested {
		t.Fatalf("resume: %d %v %+v", n, err, f)
	}
	if len(f.commands[0].Evidence) != 32 || len(f.commands[1].Evidence) != 1 {
		t.Fatal("lost source modules", f.commands)
	}
	if n, err := d.DispatchBatch(t.Context(), 10); err != nil || n != 0 || len(f.requests) != 2 {
		t.Fatalf("duplicate scan=%d %v", n, err)
	}
}

func TestAnchorDiscoveryRejectsDirectoryAndResolverDrift(t *testing.T) {
	for _, part := range []string{"workspace", "profile", "span"} {
		t.Run(part, func(t *testing.T) {
			f := newDiscoveryFixture(1)
			f.directory.Status = app.KnowledgeDirectoryAnalyzed
			switch part {
			case "workspace":
				f.directory.WorkspaceID = discoveryID(99)
			case "profile":
				f.directory.Points[0].Locator.ProfileRevisionID = discoveryID(99)
			case "span":
				f.refs[0].SourceSpanID = discoveryID(99)
			}
			d := AnchorDiscoveryDispatcher{Store: f, Directory: f, Sources: f, Requests: f}
			if _, err := d.DispatchBatch(t.Context(), 10); err == nil || len(f.requests) != 0 {
				t.Fatal("drift produced request", err)
			}
		})
	}
}

func TestAnchorDiscoveryStopsWaitingForReplacedProjection(t *testing.T) {
	f := newDiscoveryFixture(1)
	f.directory.Status = app.KnowledgeDirectoryAnalyzed
	f.directory.ParseProjectionID = discoveryID(99)
	d := AnchorDiscoveryDispatcher{Store: f, Directory: f, Sources: f, Requests: f}
	if n, err := d.DispatchBatch(t.Context(), 10); err != nil || n != 0 || f.item.Status != app.AnchorDiscoveryStale || f.item.ErrorCode != "ANCHOR_PROFILE_PROJECTION_STALE" {
		t.Fatalf("replaced projection keeps waiting: count=%d error=%v item=%+v", n, err, f.item)
	}
	if n, err := d.DispatchBatch(t.Context(), 10); err != nil || n != 0 || f.resolved != 0 || len(f.requests) != 0 {
		t.Fatalf("stale discovery read sources or created requests: count=%d error=%v", n, err)
	}
}

type discoveryFixture struct {
	item          app.AnchorSourceDiscovery
	directory     app.SourceKnowledgeDirectory
	refs          []domain.SynthesisSourceRef
	resolved      int
	requests      map[string]app.AnchorRecommendationRequest
	commands      []app.RequestAnchorSourceRecommendationCommand
	failSecond    bool
	completeError bool
}

func discoveryID(n int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", n))
}
func newDiscoveryFixture(n int) *discoveryFixture {
	source := domain.SynthesisSourceVersion{WorkspaceID: discoveryID(1), SourceID: discoveryID(2), SourceVersionID: discoveryID(3), ContentArtifactID: discoveryID(4), ParseProjectionID: discoveryID(5), ContentHash: fmt.Sprintf("%064d", 1)}
	f := &discoveryFixture{item: app.AnchorSourceDiscovery{ID: discoveryID(6), WorkspaceID: source.WorkspaceID, AnchorID: discoveryID(7), ScopeVersion: 1, Source: source, Status: app.AnchorDiscoveryWaiting, Version: 1}, requests: map[string]app.AnchorRecommendationRequest{}}
	f.directory = app.SourceKnowledgeDirectory{WorkspaceID: source.WorkspaceID, SourceVersionID: source.SourceVersionID, ParseProjectionID: source.ParseProjectionID, ProfileRevisionID: discoveryID(8), Status: app.KnowledgeDirectoryUnanalyzed}
	for i := 0; i < n; i++ {
		id := discoveryID(100 + i)
		f.directory.Points = append(f.directory.Points, app.KnowledgeDirectoryPoint{Locator: app.KnowledgePointLocator{ProfileRevisionID: f.directory.ProfileRevisionID, Kind: app.KnowledgePointKindKnowledgePoint, Index: i}, Text: "Module", SourceSpanIDs: []foundation.ID{id}})
		f.refs = append(f.refs, domain.SynthesisSourceRef{Source: source, SourceSpanID: id, ExcerptHash: source.ContentHash, Title: "mixed source"})
	}
	return f
}
func (f *discoveryFixture) SeedAnchorSourceDiscoveries(context.Context, int) (int, error) {
	return 0, nil
}
func (f *discoveryFixture) ListDueAnchorSourceDiscoveries(context.Context, int) ([]app.AnchorSourceDiscovery, error) {
	if f.item.Status == app.AnchorDiscoveryRequested || f.item.Status == app.AnchorDiscoveryStale {
		return nil, nil
	}
	return []app.AnchorSourceDiscovery{f.item}, nil
}
func (f *discoveryFixture) FreezeAnchorSourceDiscovery(_ context.Context, item app.AnchorSourceDiscovery, profile foundation.ID, refs []domain.SynthesisSourceRef) (app.AnchorSourceDiscovery, error) {
	f.item.Status = app.AnchorDiscoveryPrepared
	f.item.ProfileRevisionID = profile
	f.item.Evidence = refs
	f.item.Version++
	return f.item, nil
}
func (f *discoveryFixture) CompleteAnchorSourceDiscovery(_ context.Context, item app.AnchorSourceDiscovery, ids []foundation.ID) error {
	if item.Version != f.item.Version {
		return app.AnchorConflict()
	}
	if f.completeError {
		return errors.New("completion response unavailable")
	}
	f.item.Status = app.AnchorDiscoveryRequested
	f.item.RequestIDs = ids
	return nil
}
func (f *discoveryFixture) DeferAnchorSourceDiscovery(_ context.Context, item app.AnchorSourceDiscovery, code string, stale bool) error {
	if item.Version != f.item.Version {
		return app.AnchorConflict()
	}
	f.item.ErrorCode = code
	if stale {
		f.item.Status = app.AnchorDiscoveryStale
	}
	return nil
}
func (f *discoveryFixture) GetSourceKnowledgeDirectory(context.Context, app.SourceKnowledgeDirectoryQuery) (app.SourceKnowledgeDirectory, error) {
	return f.directory, nil
}
func (f *discoveryFixture) ProjectSynthesisKnowledgePoints(context.Context, domain.SynthesisSourceRef) (app.SynthesisKnowledgePointProjection, error) {
	return app.SynthesisKnowledgePointProjection{}, errors.New("unused")
}
func (f *discoveryFixture) ResolveAnchorDiscoverySources(context.Context, domain.SynthesisSourceVersion, []foundation.ID) ([]domain.SynthesisSourceRef, error) {
	f.resolved++
	return append([]domain.SynthesisSourceRef(nil), f.refs...), nil
}
func (f *discoveryFixture) RequestAnchorSourceRecommendation(_ context.Context, c app.RequestAnchorSourceRecommendationCommand) (app.AnchorRecommendationRequest, error) {
	if existing, ok := f.requests[c.IdempotencyKey]; ok {
		return existing, nil
	}
	if f.failSecond && len(f.requests) == 1 {
		return app.AnchorRecommendationRequest{}, errors.New("temporary request failure")
	}
	r := app.AnchorRecommendationRequest{ID: discoveryID(2000 + len(f.requests)), WorkspaceID: c.WorkspaceID, AnchorID: c.AnchorID, ExpectedScopeVersion: c.ExpectedScopeVersion, Source: &c.Source, Evidence: c.Evidence}
	f.requests[c.IdempotencyKey] = r
	f.commands = append(f.commands, c)
	return r, nil
}

func TestAnchorDiscoveryCompletionFailureRetainsReplayableRequests(t *testing.T) {
	f := newDiscoveryFixture(1)
	f.directory.Status = app.KnowledgeDirectoryAnalyzed
	f.completeError = true
	d := AnchorDiscoveryDispatcher{Store: f, Directory: f, Sources: f, Requests: f}
	if n, err := d.DispatchBatch(t.Context(), 10); err == nil || n != 0 || len(f.requests) != 1 || f.item.Status != app.AnchorDiscoveryPrepared {
		t.Fatalf("false completion=%d %v", n, err)
	}
	f.completeError = false
	if n, err := d.DispatchBatch(t.Context(), 10); err != nil || n != 1 || len(f.requests) != 1 {
		t.Fatalf("completion replay=%d %v", n, err)
	}
}
