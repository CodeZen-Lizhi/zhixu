package retrieval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestAdapterSearchPreservesScopedRequestAndResultMetadata(t *testing.T) {
	result := validSearchResult()
	result.EmbeddingVersionID = nil
	result.EffectiveMode = retrievaldomain.SearchModeKeyword
	result.IndexDegradedCapabilities = []retrievaldomain.DegradedCapability{retrievaldomain.DegradedVector}
	result.Degradations = []retrievaldomain.SearchDegradation{{Capability: retrievaldomain.SearchDegradationVector, Code: "EMBEDDING_UNAVAILABLE", Retryable: true}}
	result.Items[0].EmbeddingVersionID = nil
	result.Items[0].Vector = nil
	search := &fakeSearcher{result: result}
	adapter, err := NewAdapter(search, &fakeReference{})
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 7, 1, 8, 0, 0, 123, time.FixedZone("UTC+8", 8*60*60))
	request := retrievaldomain.SearchRequest{
		WorkspaceID: testWorkspaceID, Query: "  approved knowledge  ", Mode: retrievaldomain.SearchModeHybrid, Limit: 10,
		Filter: retrievaldomain.SearchFilter{
			SourceIDs:        []foundation.ID{testSourceID2, testSourceID1, testSourceID1},
			SourceVersionIDs: []foundation.ID{testSourceVersion2}, PathPrefixes: []string{"docs/", "docs"}, CapturedAtFrom: &from,
		},
	}
	scoped, err := adapter.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if search.request.Query != "approved knowledge" || search.request.Mode != retrievaldomain.SearchModeHybrid ||
		len(search.request.Filter.SourceIDs) != 2 || len(search.request.Filter.PathPrefixes) != 1 ||
		search.request.Filter.CapturedAtFrom == nil || search.request.Filter.CapturedAtFrom.Location() != time.UTC {
		t.Fatalf("canonical request=%#v", search.request)
	}
	if scoped.SearchResult.RequestedMode != retrievaldomain.SearchModeHybrid ||
		scoped.SearchResult.EffectiveMode != retrievaldomain.SearchModeKeyword ||
		len(scoped.SearchResult.IndexDegradedCapabilities) != 1 || len(scoped.SearchResult.Degradations) != 1 ||
		len(scoped.RetrievalBatch.Items) != 2 {
		t.Fatalf("scoped=%#v", scoped)
	}
}

func TestAdapterSearchExpandsAllProvenanceAndMarksTruncation(t *testing.T) {
	result := validSearchResult()
	result.Items = make([]retrievaldomain.EvidenceV1, 63)
	for index := range result.Items {
		item := validSearchResult().Items[0]
		item.ChunkID = foundation.ID(fmt.Sprintf("51000000-0000-4000-8000-%012d", 100+index))
		item.ParseProjectionID = foundation.ID(fmt.Sprintf("51000000-0000-4000-8001-%012d", 100+index))
		item.Span.ID = foundation.ID(fmt.Sprintf("51000000-0000-4000-8002-%012d", 100+index))
		item.Provenances = make([]retrievaldomain.EvidenceProvenance, 8)
		for provenanceIndex := range item.Provenances {
			item.Provenances[provenanceIndex] = retrievaldomain.EvidenceProvenance{
				SourceID:        foundation.ID(fmt.Sprintf("51000000-0000-4000-8003-%012d", index*8+provenanceIndex)),
				SourceVersionID: foundation.ID(fmt.Sprintf("51000000-0000-4000-8004-%012d", index*8+provenanceIndex)),
				RelativePath:    fmt.Sprintf("docs/%03d-%d.md", index, provenanceIndex), CapturedAt: time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC),
			}
		}
		result.Items[index] = item
	}
	result.Items[0].ProvenanceTruncated = true
	adapter, err := NewAdapter(&fakeSearcher{result: result}, &fakeReference{})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := adapter.Search(context.Background(), retrievaldomain.SearchRequest{
		WorkspaceID: testWorkspaceID, Query: "q", Mode: retrievaldomain.SearchModeHybrid, Limit: 63,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.SearchResult.Items) != 63 || len(scoped.RetrievalBatch.Items) != agentapplication.MaxRetrievedEvidence || !scoped.RetrievalBatch.Truncated {
		t.Fatalf("raw=%d expanded=%d truncated=%v", len(scoped.SearchResult.Items), len(scoped.RetrievalBatch.Items), scoped.RetrievalBatch.Truncated)
	}
}

func TestAdapterSearchCanonicalizesEvidenceExcerptAndRejectsWhitespaceOnly(t *testing.T) {
	result := validSearchResult()
	result.Items[0].Snippet = "\n  bounded evidence  \t"
	adapter, err := NewAdapter(&fakeSearcher{result: result}, &fakeReference{})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := adapter.Search(context.Background(), retrievaldomain.SearchRequest{
		WorkspaceID: testWorkspaceID, Query: "q", Mode: retrievaldomain.SearchModeHybrid, Limit: 1,
	})
	if err != nil || len(scoped.RetrievalBatch.Items) == 0 || scoped.RetrievalBatch.Items[0].SearchExcerpt != "bounded evidence" {
		t.Fatalf("scoped=%#v err=%v", scoped, err)
	}
	result.Items[0].Snippet = " \n\t "
	adapter, err = NewAdapter(&fakeSearcher{result: result}, &fakeReference{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Search(context.Background(), retrievaldomain.SearchRequest{
		WorkspaceID: testWorkspaceID, Query: "q", Mode: retrievaldomain.SearchModeHybrid, Limit: 1,
	})
	requireCode(t, err, errorCodeResultInvalid)
}

func TestAdapterSearchPreservesRequestedMode(t *testing.T) {
	for _, mode := range []retrievaldomain.SearchMode{retrievaldomain.SearchModeKeyword, retrievaldomain.SearchModeSemantic} {
		t.Run(string(mode), func(t *testing.T) {
			result := validSearchResult()
			result.RequestedMode, result.EffectiveMode = mode, mode
			if mode == retrievaldomain.SearchModeKeyword {
				result.EmbeddingVersionID, result.Items[0].EmbeddingVersionID, result.Items[0].Vector = nil, nil, nil
			} else {
				result.Items[0].Lexical, result.Items[0].LexicalFTSScore, result.Items[0].LexicalTrigramScore = nil, nil, nil
			}
			search := &fakeSearcher{result: result}
			adapter, err := NewAdapter(search, &fakeReference{})
			if err != nil {
				t.Fatal(err)
			}
			scoped, err := adapter.Search(context.Background(), retrievaldomain.SearchRequest{
				WorkspaceID: testWorkspaceID, Query: "q", Mode: mode, Limit: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if search.request.Mode != mode || scoped.SearchResult.RequestedMode != mode || scoped.SearchResult.EffectiveMode != mode {
				t.Fatalf("request=%#v result=%#v", search.request, scoped.SearchResult)
			}
		})
	}
}

func TestAdapterSearchFailsClosedAndPreservesSearchError(t *testing.T) {
	invalid := validSearchResult()
	invalid.RequestedMode = retrievaldomain.SearchModeKeyword
	adapter, err := NewAdapter(&fakeSearcher{result: invalid}, &fakeReference{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Search(context.Background(), retrievaldomain.SearchRequest{
		WorkspaceID: testWorkspaceID, Query: "q", Mode: retrievaldomain.SearchModeHybrid, Limit: 1,
	})
	requireCode(t, err, errorCodeResultInvalid)

	dependencyErr := foundation.NewError(foundation.ErrorRetryableFailure, "SEARCH_DOWN", true, errors.New("down"))
	adapter, err = NewAdapter(&fakeSearcher{err: dependencyErr}, &fakeReference{})
	if err != nil {
		t.Fatal(err)
	}
	_, actual := adapter.Search(context.Background(), retrievaldomain.SearchRequest{
		WorkspaceID: testWorkspaceID, Query: "q", Mode: retrievaldomain.SearchModeHybrid, Limit: 1,
	})
	if !errors.Is(actual, dependencyErr) {
		t.Fatalf("error=%v", actual)
	}
}

const (
	testWorkspaceID    foundation.ID = "51000000-0000-4000-8000-000000000001"
	testIndexID        foundation.ID = "51000000-0000-4000-8000-000000000002"
	testEmbeddingID    foundation.ID = "51000000-0000-4000-8000-000000000003"
	testChunkID        foundation.ID = "51000000-0000-4000-8000-000000000004"
	testProjectionID   foundation.ID = "51000000-0000-4000-8000-000000000005"
	testSpanID         foundation.ID = "51000000-0000-4000-8000-000000000006"
	testSourceID1      foundation.ID = "51000000-0000-4000-8000-000000000007"
	testSourceVersion1 foundation.ID = "51000000-0000-4000-8000-000000000008"
	testSourceID2      foundation.ID = "51000000-0000-4000-8000-000000000009"
	testSourceVersion2 foundation.ID = "51000000-0000-4000-8000-000000000010"
)

func TestAdapterExpandsConcreteProvenanceAndUsesHybridSearch(t *testing.T) {
	search := &fakeSearcher{result: validSearchResult()}
	reference := &fakeReference{}
	adapter, err := NewAdapter(search, reference)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := adapter.Retrieve(context.Background(), agentapplication.RetrievalRequest{
		WorkspaceID: testWorkspaceID, Query: "approved knowledge", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if search.request.Mode != retrievaldomain.SearchModeHybrid || batch.WorkspaceID != testWorkspaceID ||
		batch.IndexVersionID != testIndexID || batch.EmbeddingVersionID == nil || *batch.EmbeddingVersionID != testEmbeddingID ||
		len(batch.Items) != 2 || batch.Truncated {
		t.Fatalf("request=%#v batch=%#v", search.request, batch)
	}
	first, second := batch.Items[0], batch.Items[1]
	if first.Citation.SourceVersionID != testSourceVersion1 || second.Citation.SourceVersionID != testSourceVersion2 ||
		first.Citation.SourceSpanID != testSpanID || second.Citation.SourceSpanID != testSpanID ||
		first.Citation.ID == second.Citation.ID || first.SearchExcerpt != "bounded evidence" {
		t.Fatalf("expanded items=%#v", batch.Items)
	}
	if first.CapturedAt.Location() != time.UTC || second.CapturedAt.Location() != time.UTC {
		t.Fatalf("captured times are not UTC: %v %v", first.CapturedAt, second.CapturedAt)
	}
}

func TestAdapterOpenUsesOnlyCitationIdentity(t *testing.T) {
	result := validSearchResult()
	adapter, err := NewAdapter(&fakeSearcher{result: result}, &fakeReference{excerpt: "immutable source span"})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := adapter.Retrieve(context.Background(), agentapplication.RetrievalRequest{WorkspaceID: testWorkspaceID, Query: "q", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	reference := adapter.reference.(*fakeReference)
	opened, err := adapter.Open(context.Background(), batch.Items[0].Citation)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Citation != batch.Items[0].Citation || opened.Excerpt != "immutable source span" ||
		reference.workspaceID != testWorkspaceID || reference.indexVersionID != testIndexID || reference.chunkID != testChunkID ||
		reference.sourceVersionID != testSourceVersion1 || reference.spanID != testSpanID {
		t.Fatalf("opened=%#v reference=%#v", opened, reference)
	}
}

func TestAdapterOpenBatchUsesOneReferenceCallAndPreservesInputOrder(t *testing.T) {
	reference := &fakeReference{excerpt: "\n  immutable source span  \t"}
	adapter, err := NewAdapter(&fakeSearcher{result: validSearchResult()}, reference)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := adapter.Retrieve(context.Background(), agentapplication.RetrievalRequest{WorkspaceID: testWorkspaceID, Query: "q", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := adapter.OpenBatch(context.Background(), []agentdomain.Citation{batch.Items[1].Citation, batch.Items[0].Citation})
	if err != nil {
		t.Fatal(err)
	}
	if reference.calls != 1 || len(opened) != 2 || opened[0].Citation != batch.Items[1].Citation || opened[1].Citation != batch.Items[0].Citation ||
		opened[0].Excerpt != "immutable source span" || opened[1].Excerpt != "immutable source span" {
		t.Fatalf("calls=%d opened=%+v", reference.calls, opened)
	}
}

func TestAdapterFailsClosedForInvalidSearchOrOpenedBinding(t *testing.T) {
	invalid := validSearchResult()
	invalid.WorkspaceID = "51000000-0000-4000-8000-000000000099"
	adapter, err := NewAdapter(&fakeSearcher{result: invalid}, &fakeReference{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Retrieve(context.Background(), agentapplication.RetrievalRequest{WorkspaceID: testWorkspaceID, Query: "q", Limit: 1})
	requireCode(t, err, errorCodeResultInvalid)

	result := validSearchResult()
	reference := &fakeReference{drift: true}
	adapter, err = NewAdapter(&fakeSearcher{result: result}, reference)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := adapter.Retrieve(context.Background(), agentapplication.RetrievalRequest{WorkspaceID: testWorkspaceID, Query: "q", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Open(context.Background(), batch.Items[0].Citation)
	requireCode(t, err, errorCodeResultInvalid)
}

func TestAdapterRejectsMissingDependenciesAndPreservesSeamErrors(t *testing.T) {
	if _, err := NewAdapter(nil, &fakeReference{}); err == nil {
		t.Fatal("nil search accepted")
	}
	dependencyErr := foundation.NewError(foundation.ErrorRetryableFailure, "SEARCH_DOWN", true, errors.New("down"))
	adapter, err := NewAdapter(&fakeSearcher{err: dependencyErr}, &fakeReference{})
	if err != nil {
		t.Fatal(err)
	}
	_, actual := adapter.Retrieve(context.Background(), agentapplication.RetrievalRequest{WorkspaceID: testWorkspaceID, Query: "q", Limit: 1})
	if !errors.Is(actual, dependencyErr) {
		t.Fatalf("error=%v", actual)
	}
}

func validSearchResult() retrievaldomain.SearchResult {
	fts, trigram := 0.8, 0.2
	return retrievaldomain.SearchResult{
		WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID, EmbeddingVersionID: idPointer(testEmbeddingID),
		RequestedMode: retrievaldomain.SearchModeHybrid, EffectiveMode: retrievaldomain.SearchModeHybrid,
		Items: []retrievaldomain.EvidenceV1{{
			WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID, EmbeddingVersionID: idPointer(testEmbeddingID),
			ChunkID: testChunkID, ParseProjectionID: testProjectionID, Sequence: 0, ContentHash: strings.Repeat("a", 64),
			HeadingPath: []string{"Agent"}, Span: retrievaldomain.EvidenceSpan{ID: testSpanID, StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 16},
			Snippet: "bounded evidence",
			Provenances: []retrievaldomain.EvidenceProvenance{
				{SourceID: testSourceID1, SourceVersionID: testSourceVersion1, RelativePath: "a.md", CapturedAt: time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)},
				{SourceID: testSourceID2, SourceVersionID: testSourceVersion2, RelativePath: "b.md", CapturedAt: time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)},
			},
			Lexical: &retrievaldomain.CandidateStageScore{Rank: 1, Score: 0.8}, LexicalFTSScore: &fts, LexicalTrigramScore: &trigram,
			Vector: &retrievaldomain.CandidateStageScore{Rank: 1, Score: 0.1}, Fusion: retrievaldomain.CandidateStageScore{Rank: 1, Score: 0.03},
		}},
	}
}

func idPointer(value foundation.ID) *foundation.ID {
	cloned := value
	return &cloned
}

type fakeSearcher struct {
	request retrievaldomain.SearchRequest
	result  retrievaldomain.SearchResult
	err     error
}

func (fake *fakeSearcher) Search(_ context.Context, request retrievaldomain.SearchRequest) (retrievaldomain.SearchResult, error) {
	fake.request = request
	return fake.result, fake.err
}

type fakeReference struct {
	workspaceID, indexVersionID, chunkID foundation.ID
	sourceVersionID, spanID              foundation.ID
	excerpt                              string
	drift                                bool
	calls                                int
}

func (fake *fakeReference) OpenCitationEvidenceBatch(_ context.Context, queries []retrievaldomain.CitationReferenceQuery) ([]retrievalapplication.OpenedCitationEvidence, error) {
	fake.calls++
	first := queries[0]
	fake.workspaceID, fake.indexVersionID, fake.chunkID = first.WorkspaceID, first.IndexVersionID, first.ChunkID
	fake.sourceVersionID, fake.spanID = first.SourceVersionID, first.SourceSpanID
	if fake.excerpt == "" {
		fake.excerpt = "immutable source span"
	}
	result := make([]retrievalapplication.OpenedCitationEvidence, len(queries))
	for index, query := range queries {
		viewQuery := query
		if fake.drift {
			viewQuery.WorkspaceID = "51000000-0000-4000-8000-000000000099"
		}
		result[index] = retrievalapplication.OpenedCitationEvidence{Query: query, View: retrievalapplication.SourceSpanView{
			Reference: retrievaldomain.SourceSpanReference{
				SourceVersion: retrievaldomain.SourceVersionReference{WorkspaceID: viewQuery.WorkspaceID, SourceVersionID: viewQuery.SourceVersionID},
				Span:          retrievaldomain.EvidenceSpan{ID: viewQuery.SourceSpanID},
			},
			Excerpt: fake.excerpt,
		}}
	}
	return result, nil
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v code=%q", err, code)
	}
}
