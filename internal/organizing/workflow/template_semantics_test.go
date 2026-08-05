package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestMergeRendererClassifiesFrozenEvidenceAndPreservesSources(t *testing.T) {
	snapshot, verifier := semanticSnapshot([]semanticEvidence{
		{source: 1, span: 11, excerpt: "Cache data expires after five minutes."},
		{source: 2, span: 12, excerpt: "Cache data expires after five minutes."},
		{source: 3, span: 13, excerpt: "Cache supports ttl."},
		{source: 4, span: 14, excerpt: "Cache supports eviction."},
		{source: 5, span: 15, excerpt: "Storage feature is enabled."},
		{source: 6, span: 16, excerpt: "Storage feature is not enabled."},
		{source: 7, span: 17, excerpt: "Index stores embeddings."},
	})
	revision := semanticBuiltInRevision(t, organizingdomain.TemplateMergeDocuments)
	renderer, err := NewEvidenceRenderer(verifier)
	if err != nil {
		t.Fatal(err)
	}
	sections, comparison, err := renderer.RenderMerge(context.Background(), snapshot, revision)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.EvidenceCount != 7 || mergeConflictCount(comparison) != 2 {
		t.Fatalf("comparison=%+v", comparison)
	}
	assertMergeCategoryCount(t, comparison, mergeCategoryDuplicate, 2)
	assertMergeCategoryCount(t, comparison, mergeCategoryComplementary, 2)
	assertMergeCategoryCount(t, comparison, mergeCategoryConflict, 2)
	assertMergeCategoryCount(t, comparison, mergeCategoryUnique, 1)
	if len(comparison.Preview) != 7 {
		t.Fatalf("preview=%+v", comparison.Preview)
	}

	byKey := sectionsByKey(sections)
	assertSectionEvidence(t, byKey["common"], 2)
	assertSectionEvidence(t, byKey["complementary"], 2)
	assertSectionEvidence(t, byKey["conflicts"], 2)
	assertSectionEvidence(t, byKey["unique"], 1)
	if got := byKey["conflicts"].Citations[0].SourceVersionID; got != semanticID(5) && got != semanticID(6) {
		t.Fatalf("conflict source=%s", got)
	}

	diff := RenderCreateOnlyDiff("# diff\n")
	receipt := mergeStageReceipt{
		SchemaVersion: receiptSchemaV1, SnapshotID: snapshot.ID, SnapshotHash: snapshot.Hash,
		ArtifactID: semanticID(90), RevisionHash: hashText("revision"), DefaultTargetPath: "organized/cache.md",
		DiffHash: hashText(diff), DiffPreview: diff,
		ConflictCount: mergeConflictCount(comparison), EvidenceCount: comparison.EvidenceCount,
		Categories: comparison.Categories, Comparison: comparison.Preview,
	}
	if !validMergeComparisonReceipt(receipt) {
		t.Fatalf("receipt is invalid: %+v", receipt)
	}
	receipt.Comparison = receipt.Comparison[:len(receipt.Comparison)-1]
	if validMergeComparisonReceipt(receipt) {
		t.Fatal("incomplete comparison preview was accepted")
	}
	receipt.Comparison = comparison.Preview
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := decodeMergeReceipt(encoded); err != nil || decoded.ConflictCount != 2 || len(decoded.Comparison) != 7 {
		t.Fatalf("decoded=%+v error=%v", decoded, err)
	}
}

func TestBoundedDiffPreviewKeepsValidUTF8AtTheByteLimit(t *testing.T) {
	diff := "--- /dev/null\n+++ b/organized-result.md\n@@ -0,0 +1,1 @@\n+" +
		strings.Repeat("a", maxDiffPreviewBytes) + "界\n"
	preview, truncated := boundedDiffPreview(diff)
	if !truncated || !utf8.ValidString(preview) || len(preview) < maxDiffPreviewBytes-(utf8.UTFMax-1) {
		t.Fatalf("truncated=%v bytes=%d utf8=%v", truncated, len(preview), utf8.ValidString(preview))
	}
	receipt := mergeStageReceipt{DiffPreview: preview, DiffTruncated: true}
	if !validDiffPreview(receipt) {
		t.Fatalf("valid UTF-8 boundary preview was rejected: bytes=%d", len(preview))
	}
}

func TestBuiltInTemplateSectionsUseDistinctEvidenceOnlySemantics(t *testing.T) {
	snapshot, verifier := semanticSnapshot([]semanticEvidence{
		{source: 1, span: 11, excerpt: "Cache is a shared storage concept."},
		{source: 2, span: 12, excerpt: "What is cache and how does ttl work?"},
		{source: 3, span: 13, excerpt: "func cache() { return }"},
		{source: 4, span: 14, excerpt: "The deployment risk remains unknown."},
		{source: 5, span: 15, excerpt: "Storage feature is enabled."},
		{source: 6, span: 16, excerpt: "Storage feature is not enabled."},
	})
	renderer, err := NewEvidenceRenderer(verifier)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		kind        organizingdomain.TemplateKind
		coveredKeys []string
		gapKeys     []string
	}{
		{organizingdomain.TemplateTopicArticle, []string{"overview", "core-concepts", "details", "conflicts", "gaps", "sources"}, []string{"examples"}},
		{organizingdomain.TemplateKnowledgeReport, []string{"summary", "coverage", "findings", "conflicts", "gaps", "sources"}, nil},
		{organizingdomain.TemplateInterviewReview, []string{"core-concepts", "questions", "follow-ups", "code-examples", "gaps", "sources"}, nil},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			sections, err := renderer.Render(context.Background(), snapshot, semanticBuiltInRevision(t, test.kind))
			if err != nil {
				t.Fatal(err)
			}
			byKey := sectionsByKey(sections)
			for _, key := range test.coveredKeys {
				assertSectionEvidence(t, byKey[key], 1)
			}
			for _, key := range test.gapKeys {
				section := byKey[key]
				if section.Coverage.Status != artifactdomain.CoverageGap || section.Content != "" || len(section.Citations) != 0 {
					t.Fatalf("%s section=%+v", key, section)
				}
			}
		})
	}
}

func TestTopicOutlineReceiptCarriesFrozenEvidenceSupportAndExplicitGaps(t *testing.T) {
	snapshot, _ := semanticSnapshot([]semanticEvidence{{source: 1, span: 11, excerpt: "Cache is a shared storage concept."}})
	revision := semanticBuiltInRevision(t, organizingdomain.TemplateTopicArticle)
	loaded := runContext{snapshot: snapshot, template: revision}
	receipt := newOutlineReceipt(loaded)
	if len(receipt.Outline) != len(revision.Declaration.Sections) || receipt.Outline[0].GapCode != "" || len(receipt.Outline[0].Supports) != 1 {
		t.Fatalf("outline=%+v", receipt.Outline)
	}
	gapCount := 0
	for _, item := range receipt.Outline {
		if item.GapCode == evidenceUnavailableGap {
			gapCount++
		}
	}
	if gapCount == 0 {
		t.Fatalf("outline did not expose any evidence gap: %+v", receipt.Outline)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := decodeOutlineReceipt(encoded); err != nil || !sameOutlineReceipt(decoded, receipt) {
		t.Fatalf("decoded=%+v error=%v", decoded, err)
	}
}

type semanticEvidence struct {
	source  int
	span    int
	excerpt string
}

type semanticCitationVerifier struct {
	citations map[foundation.ID]artifactdomain.Citation
}

func (verifier semanticCitationVerifier) VerifyCitations(_ context.Context, _ foundation.ID, inputs []artifactapp.CitationInput) ([]artifactdomain.Citation, error) {
	result := make([]artifactdomain.Citation, len(inputs))
	for index, input := range inputs {
		citation, found := verifier.citations[input.SourceSpanID]
		if !found || citation.SourceVersionID != input.SourceVersionID {
			return nil, fmt.Errorf("unexpected citation %s/%s", input.SourceVersionID, input.SourceSpanID)
		}
		result[index] = citation
	}
	return result, nil
}

func semanticSnapshot(evidence []semanticEvidence) (organizingdomain.Snapshot, semanticCitationVerifier) {
	verifier := semanticCitationVerifier{citations: make(map[foundation.ID]artifactdomain.Citation, len(evidence))}
	materials := make([]organizingdomain.MaterialRef, len(evidence))
	for index, item := range evidence {
		sourceID, spanID := semanticID(item.source), semanticID(item.span)
		contentHash, excerptHash := hashText("source-"+string(sourceID)), hashText(item.excerpt)
		verifier.citations[spanID] = artifactdomain.Citation{
			SourceVersionID: sourceID, SourceSpanID: spanID, VerifiedContentHash: contentHash,
			Excerpt: item.excerpt, Verified: true,
		}
		materials[index] = organizingdomain.MaterialRef{
			Kind: organizingdomain.MaterialSourceVersion, SourceVersionID: sourceID, ContentHash: contentHash,
			Evidence: []organizingdomain.EvidenceRef{{
				IndexVersionID: semanticID(80), ChunkID: semanticID(81 + index), SourceVersionID: sourceID, SourceSpanID: spanID,
				ContentHash: contentHash, ExcerptHash: excerptHash,
			}},
		}
	}
	return organizingdomain.Snapshot{
		ID: semanticID(70), WorkspaceID: semanticID(71), DraftID: semanticID(72), DraftVersion: 1,
		TemplateID: semanticID(73), TemplateRevisionID: semanticID(74), TemplateHash: hashText("template"),
		Intent: "cache", Materials: materials, Hash: hashText("snapshot"), CreatedAt: time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
	}, verifier
}

func semanticBuiltInRevision(t *testing.T, kind organizingdomain.TemplateKind) organizingdomain.TemplateRevision {
	t.Helper()
	_, revisions, err := organizingdomain.BuiltInTemplates(time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, revision := range revisions {
		if revision.Declaration.Kind == kind {
			return revision
		}
	}
	t.Fatalf("missing template kind %s", kind)
	return organizingdomain.TemplateRevision{}
}

func semanticID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", value))
}

func sectionsByKey(sections []artifactapp.SectionInput) map[string]artifactapp.SectionInput {
	result := make(map[string]artifactapp.SectionInput, len(sections))
	for _, section := range sections {
		result[section.Key] = section
	}
	return result
}

func assertSectionEvidence(t *testing.T, section artifactapp.SectionInput, minimum int) {
	t.Helper()
	if section.Coverage.Status == artifactdomain.CoverageGap || len(section.Citations) < minimum || section.Content == "" {
		t.Fatalf("section=%+v", section)
	}
}

func assertMergeCategoryCount(t *testing.T, comparison mergeComparison, category string, expected int) {
	t.Helper()
	for _, current := range comparison.Categories {
		if current.Category == category {
			if current.Count != expected {
				t.Fatalf("category %s count=%d want=%d", category, current.Count, expected)
			}
			return
		}
	}
	t.Fatalf("missing category %s", category)
}
