package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestEvidenceReferenceServiceReturnsImmutableSpanExcerpt(t *testing.T) {
	content := []byte("prefix-" + strings.Repeat("界", 1500) + "-suffix")
	spanBytes := content[7 : len(content)-7]
	reference := applicationSourceSpanReference(content, spanBytes)
	store := &fakeEvidenceReferenceStore{sourceVersion: reference.SourceVersion, sourceSpan: reference}
	reader := &fakeEvidenceArtifactReader{artifact: EvidenceArtifact{
		WorkspaceID: reference.SourceVersion.WorkspaceID, SourceVersionID: reference.SourceVersion.SourceVersionID,
		ContentArtifactID: reference.SourceVersion.ContentArtifactID, ContentHash: sha256Hex(content),
		ByteSize: int64(len(content)), Bytes: append([]byte(nil), content...),
	}}
	service, err := NewEvidenceReferenceService(store, reader)
	if err != nil {
		t.Fatalf("new evidence reference service: %v", err)
	}

	result, err := service.GetSourceSpan(context.Background(), reference.SourceVersion.WorkspaceID, reference.SourceVersion.SourceVersionID, reference.Span.ID)
	if err != nil {
		t.Fatalf("get source span: %v", err)
	}
	if result.Reference.Span.ID != reference.Span.ID || !result.ExcerptTruncated || len(result.Excerpt) > domain.MaxEvidenceSnippetBytes || !strings.HasPrefix(result.Excerpt, "界") {
		t.Fatalf("unexpected source span view: %+v", result)
	}
	if !strings.HasPrefix(string(reader.artifact.Bytes), "prefix-") {
		t.Fatal("service mutated artifact bytes")
	}
}

func TestEvidenceReferenceServiceReturnsSourceVersionWithoutReadingArtifact(t *testing.T) {
	reference := applicationSourceSpanReference([]byte("body"), []byte("body"))
	store := &fakeEvidenceReferenceStore{sourceVersion: reference.SourceVersion, sourceSpan: reference}
	reader := &fakeEvidenceArtifactReader{err: errors.New("must not read")}
	service, err := NewEvidenceReferenceService(store, reader)
	if err != nil {
		t.Fatalf("new evidence reference service: %v", err)
	}

	result, err := service.GetSourceVersion(context.Background(), reference.SourceVersion.WorkspaceID, reference.SourceVersion.SourceVersionID)
	if err != nil || result != reference.SourceVersion || reader.calls != 0 {
		t.Fatalf("unexpected source version result=%+v calls=%d err=%v", result, reader.calls, err)
	}
}

func TestEvidenceReferenceServiceOpensCompleteCitationTuple(t *testing.T) {
	content := []byte("trusted citation excerpt")
	reference := applicationSourceSpanReference(content, content)
	store := &fakeEvidenceReferenceStore{sourceSpan: reference}
	reader := &fakeEvidenceArtifactReader{artifact: EvidenceArtifact{
		WorkspaceID: reference.SourceVersion.WorkspaceID, SourceVersionID: reference.SourceVersion.SourceVersionID,
		ContentArtifactID: reference.SourceVersion.ContentArtifactID, ContentHash: sha256Hex(content), ByteSize: int64(len(content)), Bytes: content,
	}}
	service, err := NewEvidenceReferenceService(store, reader)
	if err != nil {
		t.Fatal(err)
	}
	query := domain.CitationReferenceQuery{
		WorkspaceID: reference.SourceVersion.WorkspaceID, IndexVersionID: "91000000-0000-4000-8000-000000000017",
		ChunkID: "91000000-0000-4000-8000-000000000018", SourceVersionID: reference.SourceVersion.SourceVersionID, SourceSpanID: reference.Span.ID,
	}
	opened, err := service.OpenCitationEvidence(context.Background(), query)
	if err != nil || opened.Excerpt != string(content) || store.citationQuery != query {
		t.Fatalf("opened=%+v query=%+v err=%v", opened, store.citationQuery, err)
	}
}

func TestEvidenceReferenceServiceBatchesFiveHundredCitationsAndReadsArtifactOnce(t *testing.T) {
	content := []byte("shared immutable evidence")
	reference := applicationSourceSpanReference(content, content)
	store := &fakeEvidenceReferenceStore{sourceSpan: reference}
	reader := &fakeEvidenceArtifactReader{artifact: EvidenceArtifact{
		WorkspaceID: reference.SourceVersion.WorkspaceID, SourceVersionID: reference.SourceVersion.SourceVersionID,
		ContentArtifactID: reference.SourceVersion.ContentArtifactID, ContentHash: sha256Hex(content), ByteSize: int64(len(content)), Bytes: content,
	}}
	service, err := NewEvidenceReferenceService(store, reader)
	if err != nil {
		t.Fatal(err)
	}
	queries := make([]domain.CitationReferenceQuery, 500)
	for index := range queries {
		queries[index] = domain.CitationReferenceQuery{
			WorkspaceID: reference.SourceVersion.WorkspaceID, IndexVersionID: "91000000-0000-4000-8000-000000000017",
			ChunkID:         foundation.ID(fmt.Sprintf("92%06d-0000-4000-8000-%012d", index, index+1)),
			SourceVersionID: reference.SourceVersion.SourceVersionID,
			SourceSpanID:    foundation.ID(fmt.Sprintf("93%06d-0000-4000-8000-%012d", index, index+1)),
		}
	}
	opened, err := service.OpenCitationEvidenceBatch(context.Background(), queries)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 500 || store.citationCalls != 1 || reader.calls != 1 {
		t.Fatalf("opened=%d store_calls=%d reader_calls=%d", len(opened), store.citationCalls, reader.calls)
	}
}

func TestEvidenceReferenceServiceFailsClosedOnArtifactOrExcerptMismatch(t *testing.T) {
	content := []byte("immutable evidence")
	reference := applicationSourceSpanReference(content, content)
	tests := []struct {
		name   string
		mutate func(*EvidenceArtifact, *domain.SourceSpanReference)
	}{
		{name: "workspace", mutate: func(artifact *EvidenceArtifact, _ *domain.SourceSpanReference) {
			artifact.WorkspaceID = "91000000-0000-4000-8000-000000000099"
		}},
		{name: "artifact", mutate: func(artifact *EvidenceArtifact, _ *domain.SourceSpanReference) {
			artifact.ContentArtifactID = "91000000-0000-4000-8000-000000000099"
		}},
		{name: "byte size", mutate: func(artifact *EvidenceArtifact, _ *domain.SourceSpanReference) { artifact.ByteSize++ }},
		{name: "content hash", mutate: func(artifact *EvidenceArtifact, _ *domain.SourceSpanReference) {
			artifact.ContentHash = strings.Repeat("f", 64)
		}},
		{name: "bytes", mutate: func(artifact *EvidenceArtifact, _ *domain.SourceSpanReference) { artifact.Bytes[0] = 'I' }},
		{name: "excerpt hash", mutate: func(_ *EvidenceArtifact, span *domain.SourceSpanReference) {
			span.ExcerptHash = strings.Repeat("e", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateReference := reference
			artifact := EvidenceArtifact{
				WorkspaceID: reference.SourceVersion.WorkspaceID, SourceVersionID: reference.SourceVersion.SourceVersionID,
				ContentArtifactID: reference.SourceVersion.ContentArtifactID, ContentHash: sha256Hex(content),
				ByteSize: int64(len(content)), Bytes: append([]byte(nil), content...),
			}
			test.mutate(&artifact, &candidateReference)
			service, err := NewEvidenceReferenceService(&fakeEvidenceReferenceStore{sourceSpan: candidateReference}, &fakeEvidenceArtifactReader{artifact: artifact})
			if err != nil {
				t.Fatalf("new evidence reference service: %v", err)
			}
			_, err = service.GetSourceSpan(context.Background(), reference.SourceVersion.WorkspaceID, reference.SourceVersion.SourceVersionID, reference.Span.ID)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != evidenceArtifactInvalidCode {
				t.Fatalf("expected evidence artifact consistency error, got %v", err)
			}
			_, err = service.OpenCitationEvidence(context.Background(), domain.CitationReferenceQuery{
				WorkspaceID: reference.SourceVersion.WorkspaceID, IndexVersionID: "91000000-0000-4000-8000-000000000017",
				ChunkID: "91000000-0000-4000-8000-000000000018", SourceVersionID: reference.SourceVersion.SourceVersionID, SourceSpanID: reference.Span.ID,
			})
			classified = nil
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != evidenceArtifactInvalidCode {
				t.Fatalf("expected citation artifact consistency error, got %v", err)
			}
		})
	}
}

func applicationSourceSpanReference(content, excerpt []byte) domain.SourceSpanReference {
	start := bytes.Index(content, excerpt)
	if start < 0 {
		panic("test excerpt is not part of content")
	}
	version := domain.SourceVersionReference{
		WorkspaceID: "91000000-0000-4000-8000-000000000011", SourceID: "91000000-0000-4000-8000-000000000012",
		SourceVersionID: "91000000-0000-4000-8000-000000000013", ContentArtifactID: "91000000-0000-4000-8000-000000000014",
		SourceType: "local_file", LogicalName: "evidence.md", RelativePath: "docs/evidence.md",
		ContentHash: sha256Hex(content), ByteSize: int64(len(content)), MediaType: "text/markdown", SecurityStatus: "passed",
		CapturedAt: time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC),
	}
	return domain.SourceSpanReference{
		SourceVersion: version, ParseProjectionID: "91000000-0000-4000-8000-000000000015",
		Span:     domain.EvidenceSpan{ID: "91000000-0000-4000-8000-000000000016", StartLine: 1, EndLine: 2, StartByte: int64(start), EndByte: int64(start + len(excerpt))},
		SpanType: "section", Selector: []byte(`{"heading":["Evidence"]}`), ExcerptHash: sha256Hex(excerpt),
		ParserVersion: "goldmark-1", SchemaVersion: "parse-v1",
	}
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

type fakeEvidenceReferenceStore struct {
	sourceVersion domain.SourceVersionReference
	sourceSpan    domain.SourceSpanReference
	citationQuery domain.CitationReferenceQuery
	citationCalls int
	err           error
}

func (f *fakeEvidenceReferenceStore) LoadSourceVersionReference(context.Context, foundation.ID, foundation.ID) (domain.SourceVersionReference, error) {
	return f.sourceVersion, f.err
}

func (f *fakeEvidenceReferenceStore) LoadSourceSpanReference(context.Context, foundation.ID, foundation.ID, foundation.ID) (domain.SourceSpanReference, error) {
	return f.sourceSpan, f.err
}

func (f *fakeEvidenceReferenceStore) LoadCitationSourceSpanReferences(_ context.Context, queries []domain.CitationReferenceQuery) ([]CitationSourceSpanBinding, error) {
	f.citationCalls++
	if f.err != nil {
		return nil, f.err
	}
	f.citationQuery = queries[0]
	result := make([]CitationSourceSpanBinding, len(queries))
	for index, query := range queries {
		reference := f.sourceSpan
		reference.Span.ID = query.SourceSpanID
		result[index] = CitationSourceSpanBinding{Query: query, Reference: reference}
	}
	return result, nil
}

type fakeEvidenceArtifactReader struct {
	artifact EvidenceArtifact
	err      error
	calls    int
}

func (f *fakeEvidenceArtifactReader) ReadEvidenceArtifact(context.Context, foundation.ID, foundation.ID) (EvidenceArtifact, error) {
	f.calls++
	return f.artifact, f.err
}
