package domain

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestGeneratedRevisionAppendsAgentHistoryAndPreservesUserFreeze(t *testing.T) {
	request := generatedDomainRequest(t)
	at := time.Date(2026, 9, 8, 14, 0, 0, 123456789, time.UTC)
	document, first, err := PrepareGeneratedRevision(request, nil, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	if first.CreatedByType != "AGENT" || first.SourceVersionID != "" || first.Status != RevisionDraft || first.GitCommit != "" ||
		document.Lifecycle != DocumentDraft || document.CurrentPublishedRevisionID != "" || first.Content != request.Content {
		t.Fatalf("generated provenance or initial state is wrong: document=%+v revision=%+v", document, first)
	}
	next := request
	next.ArticleRevisionID = "69000000-0000-4000-8000-000000000008"
	next.OriginRevisionID = "69000000-0000-4000-8000-000000000009"
	next.ExpectedDocumentVersion, next.RevisionNo, next.ParentRevisionID = 1, 2, first.ID
	next.Content += "\nA complementary fact.\n"
	next.ProjectionHash = ComputeContentHash("second semantic projection")
	secondDocument, second, err := PrepareGeneratedRevision(next, &document, &first, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if secondDocument.Version != 2 || second.RevisionNo != 2 || second.ParentRevisionID != first.ID || second.CreatedByType != "AGENT" ||
		first.Content != request.Content || document.Version != 1 || secondDocument.CurrentPublishedRevisionID != "" {
		t.Fatalf("generated history changed or gained publication: first=%+v second=%+v", first, second)
	}
	blank, err := NewBlankWorkingDraft(testDraftID, testWorkspaceID, at)
	if err != nil {
		t.Fatal(err)
	}
	working, err := ApplyUpdate(blank, 1, "User article", "user.md", "# User\n", at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, _, userRevision, err := PrepareFreeze(working, 2, testDocumentID, testRevisionID, nil, nil, at.Add(2*time.Second))
	if err != nil || userRevision.CreatedByType != "USER" {
		t.Fatalf("ordinary Working Draft lost USER provenance: type=%q error=%v", userRevision.CreatedByType, err)
	}
}

func TestGeneratedRevisionRejectsChangedOwnerBaseline(t *testing.T) {
	request := generatedDomainRequest(t)
	at := time.Date(2026, 9, 8, 14, 0, 0, 0, time.UTC)
	document, first, err := PrepareGeneratedRevision(request, nil, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	request.ArticleRevisionID = "69000000-0000-4000-8000-000000000008"
	request.OriginRevisionID = "69000000-0000-4000-8000-000000000009"
	request.ExpectedDocumentVersion, request.RevisionNo, request.ParentRevisionID = 1, 2, first.ID
	for _, test := range []struct {
		name   string
		mutate func(*Document, *ArticleRevision)
	}{
		{"user version", func(_ *Document, revision *ArticleRevision) { revision.CreatedByType = "USER" }},
		{"source provenance", func(_ *Document, revision *ArticleRevision) { revision.SourceVersionID = testDraftID }},
		{"document CAS", func(doc *Document, _ *ArticleRevision) { doc.Version++ }},
		{"document path", func(doc *Document, _ *ArticleRevision) { doc.CanonicalPath = "changed.md" }},
		{"workspace", func(doc *Document, _ *ArticleRevision) { doc.WorkspaceID = testDraftID }},
		{"parent", func(_ *Document, revision *ArticleRevision) { revision.ID = testDraftID }},
		{"archived parent", func(_ *Document, revision *ArticleRevision) { revision.Status = RevisionArchived }},
		{"archived document", func(doc *Document, _ *ArticleRevision) { doc.Lifecycle = DocumentArchived }},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc, revision := document, first
			test.mutate(&doc, &revision)
			if _, _, err := PrepareGeneratedRevision(request, &doc, &revision, at.Add(time.Second)); !isCode(err, ErrorCodeGeneratedOriginConflict) {
				t.Fatalf("changed baseline was accepted: %v", err)
			}
		})
	}
}

func TestGeneratedRequestHashBindsOriginProjectionAndExactRendering(t *testing.T) {
	request := generatedDomainRequest(t)
	original, err := ComputeGeneratedRevisionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*GeneratedRevisionRequest)
	}{
		{"workspace", func(value *GeneratedRevisionRequest) { value.WorkspaceID = testDraftID }},
		{"origin", func(value *GeneratedRevisionRequest) { value.OriginID = testDocumentID }},
		{"origin revision", func(value *GeneratedRevisionRequest) { value.OriginRevisionID = testDocumentID }},
		{"article identity", func(value *GeneratedRevisionRequest) { value.ArticleRevisionID = testDraftID }},
		{"projection", func(value *GeneratedRevisionRequest) { value.ProjectionHash = strings.Repeat("b", 64) }},
		{"rendered bytes", func(value *GeneratedRevisionRequest) { value.Content += "\n" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.mutate(&changed)
			hash, err := ComputeGeneratedRevisionRequestHash(changed)
			if err != nil || hash == original {
				t.Fatalf("request hash did not bind change: error=%v equal=%t", err, hash == original)
			}
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*GeneratedRevisionRequest)
	}{
		{"unknown owner", func(value *GeneratedRevisionRequest) { value.OriginKind = "USER" }},
		{"CRLF render", func(value *GeneratedRevisionRequest) { value.Content = "# Test\r\n" }},
		{"unsafe path", func(value *GeneratedRevisionRequest) { value.TargetPath = "../note.md" }},
		{"missing projection", func(value *GeneratedRevisionRequest) { value.ProjectionHash = "" }},
		{"initial version", func(value *GeneratedRevisionRequest) { value.ExpectedDocumentVersion = 1 }},
		{"overflow", func(value *GeneratedRevisionRequest) { value.ExpectedDocumentVersion = math.MaxInt64 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.mutate(&changed)
			if _, err := ComputeGeneratedRevisionRequestHash(changed); !isCode(err, ErrorCodeGeneratedInvalid) {
				t.Fatalf("invalid request was accepted: %v", err)
			}
		})
	}
}

func TestGeneratedPublicationRetirementHashBindsBothVersions(t *testing.T) {
	generated := generatedDomainRequest(t)
	request := GeneratedPublicationRetirementRequest{WorkspaceID: generated.WorkspaceID, DocumentID: generated.DocumentID,
		ArticleRevisionID: generated.ArticleRevisionID, PublicationID: testDraftID,
		OriginKind: generated.OriginKind, OriginID: generated.OriginID, OriginRevisionID: generated.OriginRevisionID,
		ProjectionHash: generated.ProjectionHash, ExpectedDocumentVersion: 1, ExpectedProposalVersion: 1}
	hash, err := ComputeGeneratedPublicationRetirementRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*GeneratedPublicationRetirementRequest){
		func(value *GeneratedPublicationRetirementRequest) { value.ExpectedDocumentVersion++ },
		func(value *GeneratedPublicationRetirementRequest) { value.ExpectedProposalVersion++ },
		func(value *GeneratedPublicationRetirementRequest) { value.PublicationID = testDocumentID },
		func(value *GeneratedPublicationRetirementRequest) { value.ProjectionHash = strings.Repeat("a", 64) },
	} {
		changed := request
		change(&changed)
		other, err := ComputeGeneratedPublicationRetirementRequestHash(changed)
		if err != nil || other == hash {
			t.Fatalf("retirement hash did not bind changed baseline: %v", err)
		}
	}
}

func generatedDomainRequest(t *testing.T) GeneratedRevisionRequest {
	t.Helper()
	path, err := DefaultGeneratedTargetPath(testDraftID)
	if err != nil || strings.Contains(path, "/") {
		t.Fatalf("default path needs a new directory: %q error=%v", path, err)
	}
	return GeneratedRevisionRequest{WorkspaceID: testWorkspaceID, DocumentID: testDocumentID, ArticleRevisionID: testRevisionID,
		OriginKind: GeneratedOriginSynthesisNote, OriginID: testDraftID,
		OriginRevisionID: foundation.ID("69000000-0000-4000-8000-000000000007"), ProjectionHash: ComputeContentHash("semantic projection"),
		RevisionNo: 1, Title: "Continuous note", TargetPath: path, Content: "# Continuous note\n\nAn original fact.\n"}
}
