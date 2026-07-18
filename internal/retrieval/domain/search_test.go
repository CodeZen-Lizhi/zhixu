package domain

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCanonicalizeSearchRequestNormalizesSharedFiltersWithoutMutation(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 7, 19, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	before := from.Add(time.Hour)
	request := SearchRequest{
		WorkspaceID: "10000000-0000-4000-8000-000000000001",
		Query:       "  hybrid search  ", Mode: SearchModeHybrid, Limit: 20,
		Filter: SearchFilter{
			SourceIDs: []foundation.ID{
				"10000000-0000-4000-8000-000000000003",
				"10000000-0000-4000-8000-000000000002",
				"10000000-0000-4000-8000-000000000003",
			},
			SourceVersionIDs: []foundation.ID{"10000000-0000-4000-8000-000000000004"},
			PathPrefixes:     []string{"docs/api/", " docs ", "docs/api"},
			CapturedAtFrom:   &from, CapturedAtBefore: &before,
		},
	}
	canonical, err := CanonicalizeSearchRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Query != "hybrid search" || len(canonical.Filter.SourceIDs) != 2 ||
		canonical.Filter.SourceIDs[0] != "10000000-0000-4000-8000-000000000002" ||
		len(canonical.Filter.PathPrefixes) != 2 || canonical.Filter.PathPrefixes[0] != "docs" ||
		canonical.Filter.PathPrefixes[1] != "docs/api" || canonical.Filter.CapturedAtFrom.Location() != time.UTC {
		t.Fatalf("canonical=%#v", canonical)
	}
	if request.Query != "  hybrid search  " || request.Filter.PathPrefixes[0] != "docs/api/" || len(request.Filter.SourceIDs) != 3 {
		t.Fatal("CanonicalizeSearchRequest mutated caller input")
	}
}

func TestCanonicalizeSearchRequestRejectsInvalidBoundaryInputs(t *testing.T) {
	t.Parallel()
	base := validSearchRequest()
	from := time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*SearchRequest)
	}{
		{name: "missing workspace", mutate: func(value *SearchRequest) { value.WorkspaceID = "" }},
		{name: "empty query", mutate: func(value *SearchRequest) { value.Query = "  " }},
		{name: "oversized query", mutate: func(value *SearchRequest) { value.Query = strings.Repeat("x", MaxSearchQueryBytes+1) }},
		{name: "unknown mode", mutate: func(value *SearchRequest) { value.Mode = "auto" }},
		{name: "zero limit", mutate: func(value *SearchRequest) { value.Limit = 0 }},
		{name: "large limit", mutate: func(value *SearchRequest) { value.Limit = MaxSearchLimit + 1 }},
		{name: "absolute path", mutate: func(value *SearchRequest) { value.Filter.PathPrefixes = []string{"/etc"} }},
		{name: "escape path", mutate: func(value *SearchRequest) { value.Filter.PathPrefixes = []string{"../etc"} }},
		{name: "backslash path", mutate: func(value *SearchRequest) { value.Filter.PathPrefixes = []string{`docs\\api`} }},
		{name: "empty time range", mutate: func(value *SearchRequest) { value.Filter.CapturedAtFrom = &from; value.Filter.CapturedAtBefore = &from }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			err := func() error { _, err := CanonicalizeSearchRequest(request); return err }()
			if err == nil {
				t.Fatal("invalid search request accepted")
			}
			assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeSearchRequestInvalid)
		})
	}
}

func TestCanonicalizeEvidenceProvenanceSortsAndRejectsDuplicates(t *testing.T) {
	t.Parallel()
	values := []EvidenceProvenance{
		validProvenance("10000000-0000-4000-8000-000000000020", "10000000-0000-4000-8000-000000000021", "b.md"),
		validProvenance("10000000-0000-4000-8000-000000000010", "10000000-0000-4000-8000-000000000011", "a.md"),
	}
	canonical, err := CanonicalizeEvidenceProvenance(values)
	if err != nil {
		t.Fatal(err)
	}
	if canonical[0].RelativePath != "a.md" || values[0].RelativePath != "b.md" {
		t.Fatalf("canonical=%#v original=%#v", canonical, values)
	}
	_, err = CanonicalizeEvidenceProvenance([]EvidenceProvenance{values[0], values[0]})
	if err == nil {
		t.Fatal("duplicate provenance accepted")
	}
	assertDomainError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeSearchCandidateInvalid)
}

func TestValidateSearchCandidateRequiresCanonicalEvidenceAndFiniteScores(t *testing.T) {
	t.Parallel()
	candidate := validSearchCandidate()
	if err := ValidateSearchCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	invalids := []func(*SearchCandidate){
		func(value *SearchCandidate) {
			value.Provenances[0], value.Provenances[1] = value.Provenances[1], value.Provenances[0]
		},
		func(value *SearchCandidate) { value.Vector = &CandidateStageScore{Rank: 1, Score: math.NaN()} },
		func(value *SearchCandidate) { value.Lexical = nil; value.Vector = nil },
		func(value *SearchCandidate) { value.LexicalFTSScore = nil },
		func(value *SearchCandidate) { value.Span.StartLine = 0 },
		func(value *SearchCandidate) { value.Snippet = "" },
		func(value *SearchCandidate) { value.ContentHash = "bad" },
	}
	for _, mutate := range invalids {
		invalid := validSearchCandidate()
		mutate(&invalid)
		err := ValidateSearchCandidate(invalid)
		if err == nil {
			t.Fatalf("invalid candidate accepted: %#v", invalid)
		}
		assertDomainError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeSearchCandidateInvalid)
	}
}

func TestNormalizeSearchDegradationsSeparatesQueryFromIndexState(t *testing.T) {
	t.Parallel()
	values := []SearchDegradation{
		{Capability: SearchDegradationRerank, Code: "RERANK_UNAVAILABLE", Retryable: true},
		{Capability: SearchDegradationVector, Code: "EMBEDDING_UNAVAILABLE", Retryable: true},
		{Capability: SearchDegradationRerank, Code: "RERANK_UNAVAILABLE", Retryable: true},
	}
	canonical, err := NormalizeSearchDegradations(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 2 || canonical[0].Capability != SearchDegradationVector || canonical[1].Capability != SearchDegradationRerank {
		t.Fatalf("canonical=%#v", canonical)
	}
	_, err = NormalizeSearchDegradations([]SearchDegradation{
		{Capability: SearchDegradationVector, Code: "A"},
		{Capability: SearchDegradationVector, Code: "B"},
	})
	if err == nil {
		t.Fatal("conflicting degradation reasons accepted")
	}
	assertDomainError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeSearchResultInvalid)
}

func TestValidateSearchResultEnforcesDegradationMatrixAndChunkUniqueness(t *testing.T) {
	t.Parallel()
	request := validSearchRequest()
	request.Mode = SearchModeHybrid
	result := validSearchResult(request)
	if err := ValidateSearchResult(request, result); err != nil {
		t.Fatal(err)
	}

	ftsOnly := result
	ftsOnly.EmbeddingVersionID = nil
	ftsOnly.EffectiveMode = SearchModeKeyword
	ftsOnly.IndexDegradedCapabilities = []DegradedCapability{DegradedVector}
	ftsOnly.Degradations = []SearchDegradation{{Capability: SearchDegradationVector, Code: "EMBEDDING_UNAVAILABLE", Retryable: true}}
	ftsOnly.Items = nil
	if err := ValidateSearchResult(request, ftsOnly); err != nil {
		t.Fatalf("hybrid fallback error = %v", err)
	}

	missingDegradation := ftsOnly
	missingDegradation.Degradations = nil
	if err := ValidateSearchResult(request, missingDegradation); err == nil {
		t.Fatal("hybrid keyword fallback without degradation accepted")
	}

	duplicate := result
	duplicate.Items = append(append([]EvidenceV1(nil), result.Items...), result.Items[0])
	if err := ValidateSearchResult(request, duplicate); err == nil {
		t.Fatal("duplicate chunk ranks accepted")
	}

	semantic := request
	semantic.Mode = SearchModeSemantic
	semanticResult := result
	semanticResult.RequestedMode = SearchModeSemantic
	semanticResult.EffectiveMode = SearchModeSemantic
	semanticResult.IndexDegradedCapabilities = []DegradedCapability{DegradedVector}
	if err := ValidateSearchResult(semantic, semanticResult); err == nil {
		t.Fatal("semantic result accepted degraded vector index")
	}

	keyword := request
	keyword.Mode = SearchModeKeyword
	keywordResult := result
	keywordResult.RequestedMode = SearchModeKeyword
	keywordResult.EffectiveMode = SearchModeKeyword
	keywordResult.Degradations = []SearchDegradation{{Capability: SearchDegradationRerank, Code: "RERANK_UNAVAILABLE"}}
	if err := ValidateSearchResult(keyword, keywordResult); err == nil {
		t.Fatal("keyword result accepted query degradation")
	}
}

func validSearchRequest() SearchRequest {
	return SearchRequest{
		WorkspaceID: "10000000-0000-4000-8000-000000000001",
		Query:       "search", Mode: SearchModeKeyword, Limit: 10,
	}
}

func validProvenance(sourceID, versionID foundation.ID, relativePath string) EvidenceProvenance {
	return EvidenceProvenance{
		SourceID: sourceID, SourceVersionID: versionID, RelativePath: relativePath,
		CapturedAt: time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC),
	}
}

func validSearchCandidate() SearchCandidate {
	embeddingID := foundation.ID("10000000-0000-4000-8000-000000000005")
	ftsScore, trigramScore := 0.7, 0.3
	return SearchCandidate{
		WorkspaceID:        "10000000-0000-4000-8000-000000000001",
		IndexVersionID:     "10000000-0000-4000-8000-000000000002",
		EmbeddingVersionID: &embeddingID,
		ChunkID:            "10000000-0000-4000-8000-000000000003",
		ParseProjectionID:  "10000000-0000-4000-8000-000000000004",
		Sequence:           2, ContentHash: strings.Repeat("a", 64), HeadingPath: []string{"Retrieval"},
		Span:    EvidenceSpan{ID: "10000000-0000-4000-8000-000000000006", StartLine: 1, EndLine: 2, StartByte: 0, EndByte: 12},
		Snippet: "evidence", RerankText: "bounded candidate text",
		Provenances: []EvidenceProvenance{
			validProvenance("10000000-0000-4000-8000-000000000010", "10000000-0000-4000-8000-000000000011", "a.md"),
			validProvenance("10000000-0000-4000-8000-000000000020", "10000000-0000-4000-8000-000000000021", "b.md"),
		},
		Lexical: &CandidateStageScore{Rank: 1, Score: 0.8}, LexicalFTSScore: &ftsScore, LexicalTrigramScore: &trigramScore,
		Vector: &CandidateStageScore{Rank: 2, Score: 0.2},
		Fusion: &CandidateStageScore{Rank: 1, Score: 0.03},
	}
}

func validSearchResult(request SearchRequest) SearchResult {
	candidate := validSearchCandidate()
	return SearchResult{
		WorkspaceID: candidate.WorkspaceID, IndexVersionID: candidate.IndexVersionID,
		EmbeddingVersionID: candidate.EmbeddingVersionID,
		RequestedMode:      request.Mode, EffectiveMode: request.Mode,
		Items: []EvidenceV1{{
			WorkspaceID: candidate.WorkspaceID, IndexVersionID: candidate.IndexVersionID,
			EmbeddingVersionID: candidate.EmbeddingVersionID, ChunkID: candidate.ChunkID,
			ParseProjectionID: candidate.ParseProjectionID, Sequence: candidate.Sequence,
			ContentHash: candidate.ContentHash, HeadingPath: candidate.HeadingPath, Span: candidate.Span,
			Snippet: candidate.Snippet, Provenances: candidate.Provenances,
			Lexical: candidate.Lexical, LexicalFTSScore: candidate.LexicalFTSScore,
			LexicalTrigramScore: candidate.LexicalTrigramScore, Vector: candidate.Vector, Fusion: *candidate.Fusion,
		}},
	}
}
