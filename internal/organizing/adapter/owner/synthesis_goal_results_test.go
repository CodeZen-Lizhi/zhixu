package owner

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestGoalMaterialDeduplicatesEvidenceAndRetainsPointBindings(t *testing.T) {
	workspaceID, requestID := knowledgeDirectoryID(1), knowledgeDirectoryID(2)
	spanOne, spanTwo := knowledgeDirectoryID(3), knowledgeDirectoryID(4)
	source := knowledgeDirectorySynthesisRef(workspaceID, knowledgeDirectoryID(5), knowledgeDirectoryID(6), spanOne).Source
	first := app.KnowledgePointLocator{ProfileRevisionID: knowledgeDirectoryID(7), Kind: app.KnowledgePointKindKnowledgePoint, Index: 0}
	second := app.KnowledgePointLocator{ProfileRevisionID: first.ProfileRevisionID, Kind: app.KnowledgePointKindExample, Index: 0}
	results := &goalMaterialResultsFake{page: goalMaterialPage(workspaceID, requestID, []app.GoalSelectionResult{{
		SelectionID: knowledgeDirectoryID(8), ModelRunID: knowledgeDirectoryID(9),
		Points: []app.SynthesisGoalSelectedPoint{
			{Source: source, Locator: first, SourceSpanIDs: []foundation.ID{spanOne, spanTwo}, Reason: "first reason"},
			{Source: source, Locator: second, SourceSpanIDs: []foundation.ID{spanOne}, Reason: "second reason"},
		},
	}}, "", true)}
	sources := &goalMaterialSourcesFake{resolve: goalMaterialReferences}

	page, err := mustGoalMaterialReader(t, results, sources).ReadGoalSourceMaterials(t.Context(), app.GoalSelectionResultQuery{
		WorkspaceID: workspaceID, RequestID: requestID, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources.batches) != 1 || !reflect.DeepEqual(sources.batches[0], []foundation.ID{spanOne, spanTwo}) || len(page.Items) != 2 {
		t.Fatalf("batches=%#v materials=%#v", sources.batches, page.Items)
	}
	if page.Items[0].Reference.SourceSpanID != spanOne || len(page.Items[0].Points) != 2 ||
		page.Items[0].Points[0] != (app.GoalSourcePointBinding{SelectionID: knowledgeDirectoryID(8), ModelRunID: knowledgeDirectoryID(9), Locator: first, Reason: "first reason"}) ||
		page.Items[0].Points[1] != (app.GoalSourcePointBinding{SelectionID: knowledgeDirectoryID(8), ModelRunID: knowledgeDirectoryID(9), Locator: second, Reason: "second reason"}) ||
		page.Items[1].Reference.SourceSpanID != spanTwo || len(page.Items[1].Points) != 1 || page.Items[1].Points[0].Locator != first {
		t.Fatalf("materials=%#v", page.Items)
	}
}

func TestGoalMaterialSplitsMoreThan256EvidenceSpans(t *testing.T) {
	workspaceID, requestID := knowledgeDirectoryID(20), knowledgeDirectoryID(21)
	spans := make([]foundation.ID, domain.MaxSynthesisSources+1)
	for index := range spans {
		spans[index] = knowledgeDirectoryID(100 + index)
	}
	source := knowledgeDirectorySynthesisRef(workspaceID, knowledgeDirectoryID(22), knowledgeDirectoryID(23), spans[0]).Source
	results := &goalMaterialResultsFake{page: goalMaterialPage(workspaceID, requestID, []app.GoalSelectionResult{{
		SelectionID: knowledgeDirectoryID(24), ModelRunID: knowledgeDirectoryID(25), Points: []app.SynthesisGoalSelectedPoint{{
			Source: source, Locator: app.KnowledgePointLocator{ProfileRevisionID: knowledgeDirectoryID(26), Kind: app.KnowledgePointKindKnowledgePoint, Index: 0}, SourceSpanIDs: spans, Reason: "all evidence",
		}},
	}}, "", true)}
	sources := &goalMaterialSourcesFake{resolve: goalMaterialReferences}

	page, err := mustGoalMaterialReader(t, results, sources).ReadGoalSourceMaterials(t.Context(), app.GoalSelectionResultQuery{
		WorkspaceID: workspaceID, RequestID: requestID, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources.batches) != 2 || len(sources.batches[0]) != domain.MaxSynthesisSources || len(sources.batches[1]) != 1 ||
		!reflect.DeepEqual(append(append([]foundation.ID(nil), sources.batches[0]...), sources.batches[1]...), spans) || len(page.Items) != len(spans) {
		t.Fatalf("batches=%#v materials=%d", sources.batches, len(page.Items))
	}
}

func TestGoalMaterialPreservesCursorForExplicitEmptySelection(t *testing.T) {
	workspaceID, requestID, selectionID := knowledgeDirectoryID(30), knowledgeDirectoryID(31), knowledgeDirectoryID(32)
	results := &goalMaterialResultsFake{page: goalMaterialPage(workspaceID, requestID, []app.GoalSelectionResult{{
		SelectionID: selectionID, ModelRunID: knowledgeDirectoryID(33), Points: []app.SynthesisGoalSelectedPoint{},
	}}, selectionID, true)}
	sources := &goalMaterialSourcesFake{}

	page, err := mustGoalMaterialReader(t, results, sources).ReadGoalSourceMaterials(t.Context(), app.GoalSelectionResultQuery{
		WorkspaceID: workspaceID, RequestID: requestID, AfterSelectionID: knowledgeDirectoryID(29), Limit: 1,
	})
	if err != nil || page.NextAfterSelectionID != selectionID || len(page.Items) != 0 || len(sources.batches) != 0 {
		t.Fatalf("page=%#v batches=%#v err=%v", page, sources.batches, err)
	}
}

func TestGoalMaterialDoesNotResolveUnreadySelection(t *testing.T) {
	workspaceID, requestID := knowledgeDirectoryID(40), knowledgeDirectoryID(41)
	results := &goalMaterialResultsFake{page: goalMaterialPage(workspaceID, requestID, []app.GoalSelectionResult{{
		SelectionID: knowledgeDirectoryID(42), ModelRunID: knowledgeDirectoryID(43), Points: []app.SynthesisGoalSelectedPoint{},
	}}, "", false)}
	sources := &goalMaterialSourcesFake{err: errors.New("must not resolve unready selection")}

	page, err := mustGoalMaterialReader(t, results, sources).ReadGoalSourceMaterials(t.Context(), app.GoalSelectionResultQuery{
		WorkspaceID: workspaceID, RequestID: requestID, Limit: 1,
	})
	assertOwnerError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeOwnerResultInvalid)
	if !reflect.DeepEqual(page, app.GoalSourceMaterialPage{}) || len(sources.batches) != 0 {
		t.Fatalf("page=%#v batches=%#v", page, sources.batches)
	}
}

func TestGoalMaterialPropagatesSourceResolutionError(t *testing.T) {
	workspaceID, requestID, spanID := knowledgeDirectoryID(50), knowledgeDirectoryID(51), knowledgeDirectoryID(52)
	source := knowledgeDirectorySynthesisRef(workspaceID, knowledgeDirectoryID(53), knowledgeDirectoryID(54), spanID).Source
	stale := foundation.NewError(foundation.ErrorVersionConflict, "SYNTHESIS_SOURCE_STALE", false, errors.New("source changed"))
	results := &goalMaterialResultsFake{page: goalMaterialSelectionPage(workspaceID, requestID, source, spanID)}
	sources := &goalMaterialSourcesFake{err: stale}

	page, err := mustGoalMaterialReader(t, results, sources).ReadGoalSourceMaterials(t.Context(), app.GoalSelectionResultQuery{
		WorkspaceID: workspaceID, RequestID: requestID, Limit: 1,
	})
	if err != stale || !reflect.DeepEqual(page, app.GoalSourceMaterialPage{}) {
		t.Fatalf("page=%#v err=%#v", page, err)
	}
}

func TestGoalMaterialRejectsIncompleteOrReplacedEvidenceWithoutPartialResult(t *testing.T) {
	workspaceID, requestID := knowledgeDirectoryID(60), knowledgeDirectoryID(61)
	spanOne, spanTwo, replacement := knowledgeDirectoryID(62), knowledgeDirectoryID(63), knowledgeDirectoryID(64)
	source := knowledgeDirectorySynthesisRef(workspaceID, knowledgeDirectoryID(65), knowledgeDirectoryID(66), spanOne).Source
	for _, test := range []struct {
		name string
		refs []domain.SynthesisSourceRef
	}{
		{"replacement", []domain.SynthesisSourceRef{goalMaterialReference(source, spanOne), goalMaterialReference(source, replacement)}},
		{"duplicate", []domain.SynthesisSourceRef{goalMaterialReference(source, spanOne), goalMaterialReference(source, spanOne)}},
		{"missing", []domain.SynthesisSourceRef{goalMaterialReference(source, spanOne)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			results := &goalMaterialResultsFake{page: goalMaterialSelectionPage(workspaceID, requestID, source, spanOne, spanTwo)}
			sources := &goalMaterialSourcesFake{refs: test.refs}
			page, err := mustGoalMaterialReader(t, results, sources).ReadGoalSourceMaterials(t.Context(), app.GoalSelectionResultQuery{
				WorkspaceID: workspaceID, RequestID: requestID, Limit: 1,
			})
			assertOwnerError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeOwnerResultInvalid)
			if !reflect.DeepEqual(page, app.GoalSourceMaterialPage{}) {
				t.Fatalf("partial material returned: %#v", page)
			}
		})
	}
}

type goalMaterialResultsFake struct {
	page    app.GoalSelectionResultPage
	err     error
	queries []app.GoalSelectionResultQuery
}

func (fake *goalMaterialResultsFake) ReadGoalSelectionProgress(context.Context, foundation.ID, foundation.ID) (app.SynthesisGoalSelectionProgress, error) {
	return fake.page.Progress, fake.err
}

func (fake *goalMaterialResultsFake) ReadGoalSelectionResults(_ context.Context, query app.GoalSelectionResultQuery) (app.GoalSelectionResultPage, error) {
	fake.queries = append(fake.queries, query)
	return fake.page, fake.err
}

type goalMaterialSourcesFake struct {
	refs    []domain.SynthesisSourceRef
	err     error
	batches [][]foundation.ID
	resolve func(domain.SynthesisSourceVersion, []foundation.ID) []domain.SynthesisSourceRef
}

func (fake *goalMaterialSourcesFake) ResolveAnchorDiscoverySources(_ context.Context, source domain.SynthesisSourceVersion, ids []foundation.ID) ([]domain.SynthesisSourceRef, error) {
	fake.batches = append(fake.batches, append([]foundation.ID(nil), ids...))
	if fake.err != nil {
		return nil, fake.err
	}
	if fake.resolve != nil {
		return fake.resolve(source, ids), nil
	}
	return append([]domain.SynthesisSourceRef(nil), fake.refs...), nil
}

func mustGoalMaterialReader(t *testing.T, results app.GoalSelectionResultReader, sources app.AnchorDiscoverySourceResolver) *SynthesisGoalMaterialReader {
	t.Helper()
	reader, err := NewSynthesisGoalMaterialReader(results, sources)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func goalMaterialPage(workspaceID, requestID foundation.ID, selections []app.GoalSelectionResult, next foundation.ID, ready bool) app.GoalSelectionResultPage {
	progress := app.SynthesisGoalSelectionProgress{Request: app.SynthesisGoalRequest{
		ID: requestID, WorkspaceID: workspaceID, Status: app.SynthesisGoalCatalogReady, CatalogBatches: 1,
	}, CatalogBatches: 1, PreparedBatches: 1, Selections: 1, Succeeded: 1}
	if !ready {
		progress.Pending = 1
		progress.Succeeded = 0
	}
	return app.GoalSelectionResultPage{Progress: progress, Items: selections, NextAfterSelectionID: next}
}

func goalMaterialSelectionPage(workspaceID, requestID foundation.ID, source domain.SynthesisSourceVersion, spans ...foundation.ID) app.GoalSelectionResultPage {
	return goalMaterialPage(workspaceID, requestID, []app.GoalSelectionResult{{
		SelectionID: knowledgeDirectoryID(700), ModelRunID: knowledgeDirectoryID(701), Points: []app.SynthesisGoalSelectedPoint{{
			Source: source, Locator: app.KnowledgePointLocator{ProfileRevisionID: knowledgeDirectoryID(702), Kind: app.KnowledgePointKindKnowledgePoint, Index: 0}, SourceSpanIDs: spans, Reason: "evidence",
		}},
	}}, "", true)
}

func goalMaterialReferences(source domain.SynthesisSourceVersion, ids []foundation.ID) []domain.SynthesisSourceRef {
	refs := make([]domain.SynthesisSourceRef, len(ids))
	for index, id := range ids {
		refs[index] = goalMaterialReference(source, id)
	}
	return refs
}

func goalMaterialReference(source domain.SynthesisSourceVersion, spanID foundation.ID) domain.SynthesisSourceRef {
	return domain.SynthesisSourceRef{Source: source, SourceSpanID: spanID, ExcerptHash: knowledgeDirectoryHash(800), Title: "Redis source"}
}
