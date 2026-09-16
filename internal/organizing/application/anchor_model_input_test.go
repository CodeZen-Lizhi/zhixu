package application

import (
	"bytes"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestAnchorModelInputUsesFrozenTitleBodyAndExactExcerpts(t *testing.T) {
	input, generated := synthesisContractFixture()
	ref := input.Sources[0].Reference
	items, err := domain.ApplySynthesisDelta(ref.Source.WorkspaceID, nil, generated.Notes[0].Delta, []domain.SynthesisSourceRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	noteID, revisionID := synthesisContractID(30), synthesisContractID(32)
	body, err := domain.RenderSynthesisMarkdown(ref.Source.WorkspaceID, noteID, generated.Notes[0].Title, items.Items)
	if err != nil {
		t.Fatal(err)
	}
	revision := domain.SynthesisRevision{ID: revisionID, WorkspaceID: ref.Source.WorkspaceID, NoteID: noteID, DocumentID: synthesisContractID(31), ArticleRevisionID: synthesisContractID(33), RevisionNo: 1, ArticleRevisionNo: 1, Title: generated.Notes[0].Title, RendererVersion: domain.SynthesisRendererVersion, ContentHash: synthesisContractHash(body), Items: items.Items, Delta: generated.Notes[0].Delta, SourceEventID: input.SourceEvent.ID, WorkflowRunID: input.WorkflowRunID, ModelRunID: generated.ModelRunID, CreatedAt: input.SourceEvent.CreatedAt}
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	if err != nil {
		t.Fatal(err)
	}
	request := AnchorRecommendationRequest{ID: synthesisContractID(90), WorkspaceID: revision.WorkspaceID, NoteID: noteID, BasisRevisionID: revisionID, Kind: AnchorInitialScopeRecommendation, Evidence: []domain.SynthesisSourceRef{ref}}
	opened := []SynthesisSourceView{{Reference: ref, Availability: domain.MaterialAvailable, Text: input.Sources[0].Text}}
	payload, err := BuildAnchorRecommendationInput(request, revision, nil, opened)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{revision.Title, items.Items[0].Fact.Text, `"label":"S001"`, `"confirmed_scope":null`} {
		if !bytes.Contains(payload, []byte(expected)) {
			t.Fatalf("missing content %s", expected)
		}
	}
	for _, private := range []string{string(noteID), string(ref.Source.SourceVersionID), ref.ExcerptHash, `"workspace_id"`, `"source_span_id"`} {
		if bytes.Contains(payload, []byte(private)) {
			t.Fatalf("provider input leaked control field %s", private)
		}
	}
	opened[0].Text = "changed excerpt"
	if _, err = BuildAnchorRecommendationInput(request, revision, nil, opened); err == nil {
		t.Fatal("changed excerpt accepted")
	}
	opened[0].Text = ""
	opened[0].Availability = domain.MaterialUnavailable
	if _, err = BuildAnchorRecommendationInput(request, revision, nil, opened); err == nil {
		t.Fatal("unavailable evidence silently used")
	}
	opened[0].Text = input.Sources[0].Text
	opened[0].Availability = domain.MaterialAvailable
	request.AnchorID = synthesisContractID(95)
	request.ExpectedScopeVersion = 2
	request.Kind = domain.AnchorSourceAssociation
	request.Source = &ref.Source
	anchor := domain.Anchor{ID: request.AnchorID, WorkspaceID: request.WorkspaceID, NoteID: noteID, BasisRevisionID: revisionID, Title: revision.Title, Scope: domain.AnchorScope{Topics: []string{"缓存"}, Audiences: []string{"复习"}, Description: "缓存复习"}, ScopeVersion: 1, Version: 1, CreatedAt: revision.CreatedAt, UpdatedAt: revision.CreatedAt}
	if _, err = BuildAnchorRecommendationInput(request, revision, &anchor, opened); err == nil {
		t.Fatal("stale confirmed scope accepted")
	}
	anchor.ScopeVersion = 2
	if _, err = BuildAnchorRecommendationInput(request, revision, &anchor, opened); err != nil {
		t.Fatal(err)
	}
}
