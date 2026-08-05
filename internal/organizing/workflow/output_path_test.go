package workflow

import (
	"strings"
	"testing"
	"time"

	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestDefaultOutputPathExpandsFrozenIntentAndDate(t *testing.T) {
	snapshot, _ := semanticSnapshot([]semanticEvidence{{source: 1, span: 11, excerpt: "verified"}})
	revision := semanticBuiltInRevision(t, organizingdomain.TemplateMergeDocuments)
	revision.Declaration.Output.Directory = "reports"
	revision.Declaration.Output.FilenamePattern = "{date}-{slug}.md"
	canonical, digest, err := organizingdomain.CanonicalTemplateDeclaration(revision.Declaration)
	if err != nil {
		t.Fatal(err)
	}
	revision.Declaration = canonical
	revision.DeclarationHash = digest
	snapshot.Intent = "Java AI / RAG 与 Agent 设计"
	snapshot.CreatedAt = time.Date(2026, 8, 4, 23, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60)).UTC()
	snapshot.TemplateID, snapshot.TemplateRevisionID, snapshot.TemplateHash = revision.TemplateID, revision.ID, revision.DeclarationHash
	snapshot.Hash, err = organizingdomain.ComputeSnapshotHash(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("snapshot.Validate()=%v snapshot=%+v", err, snapshot)
	}

	target, err := defaultOutputPath(snapshot, revision)
	if err != nil {
		t.Fatal(err)
	}
	if target != "reports/2026-08-04-java-ai-rag-与-agent-设计.md" {
		t.Fatalf("defaultOutputPath()=%q", target)
	}
}

func TestOutputSlugIsBoundedAndHasFallback(t *testing.T) {
	if got := outputSlug(" / "); got != "organized-note" {
		t.Fatalf("fallback slug=%q", got)
	}
	if got := outputSlug(strings.Repeat("知识", 100)); len(got) > maxOutputSlugBytes {
		t.Fatalf("slug bytes=%d", len(got))
	}
}
