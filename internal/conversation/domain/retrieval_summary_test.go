package domain

import (
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestCanonicalizeRetrievalSummaryPreservesExplainableBoundedFacts(t *testing.T) {
	requested := RetrievalSummary{
		Rewrites:      []string{"runtime recovery"},
		RequestedMode: retrievaldomain.SearchModeHybrid,
		EffectiveMode: retrievaldomain.SearchModeKeyword,
		Scope: RetrievalScopeSummary{
			SourceIDs: []foundation.ID{
				"10000000-0000-4000-8000-000000000004",
				"10000000-0000-4000-8000-000000000003",
				"10000000-0000-4000-8000-000000000004",
			},
			SourceVersionIDs: []foundation.ID{}, PathPrefixes: []string{" docs/runtime/ ", "docs/runtime"},
		},
		IndexVersionID: stringIDPointer("10000000-0000-4000-8000-000000000020"),
		CandidateCount: 2, SelectedCount: 1, ConflictCount: 0,
		Degradations: []RetrievalDegradation{
			{Capability: retrievaldomain.SearchDegradationRerank, Code: "RERANK_UNAVAILABLE", Retryable: true},
			{Capability: retrievaldomain.SearchDegradationVector, Code: "EMBEDDING_UNAVAILABLE", Retryable: true},
		},
	}
	canonical, err := CanonicalizeRetrievalSummary(testWorkspaceID, requested)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical.Scope.SourceIDs) != 2 || canonical.Scope.SourceIDs[0] != "10000000-0000-4000-8000-000000000003" ||
		len(canonical.Scope.PathPrefixes) != 1 || canonical.Scope.PathPrefixes[0] != "docs/runtime" ||
		len(canonical.Degradations) != 2 || canonical.Degradations[0].Capability != retrievaldomain.SearchDegradationVector ||
		len(requested.Scope.SourceIDs) != 3 || requested.Scope.PathPrefixes[0] != " docs/runtime/ " {
		t.Fatalf("canonical=%#v requested=%#v", canonical, requested)
	}
	if err := ValidateRetrievalSummary(testWorkspaceID, AnswerPublicationCompleted, canonical); err != nil {
		t.Fatal(err)
	}

	invalids := []func(*RetrievalSummary){
		func(value *RetrievalSummary) { value.Rewrites = nil },
		func(value *RetrievalSummary) { value.Rewrites = []string{"same", "same"} },
		func(value *RetrievalSummary) { value.CandidateCount = -1 },
		func(value *RetrievalSummary) { value.SelectedCount = value.CandidateCount + 1 },
		func(value *RetrievalSummary) { value.IndexVersionID = nil },
		func(value *RetrievalSummary) {
			value.EffectiveMode = retrievaldomain.SearchModeSemantic
			value.EmbeddingVersionID = nil
		},
		func(value *RetrievalSummary) { value.RequestedMode = retrievaldomain.SearchModeSemantic },
		func(value *RetrievalSummary) {
			value.RequestedMode = retrievaldomain.SearchModeKeyword
			value.EffectiveMode = retrievaldomain.SearchModeKeyword
		},
		func(value *RetrievalSummary) {
			value.Rewrites = []string{strings.Repeat("a", MaxQuestionBytes), strings.Repeat("b", MaxQuestionBytes)}
		},
		func(value *RetrievalSummary) { value.Scope.PathPrefixes = []string{"../secret"} },
	}
	for _, mutate := range invalids {
		value := canonical
		value.Rewrites = append([]string{}, canonical.Rewrites...)
		value.Scope = copyRetrievalScopeSummary(canonical.Scope)
		value.Degradations = append([]RetrievalDegradation{}, canonical.Degradations...)
		mutate(&value)
		if err := ValidateRetrievalSummary(testWorkspaceID, AnswerPublicationCompleted, value); errorCode(err) != ErrorCodeRetrievalSummaryInvalid {
			t.Fatalf("summary=%#v err=%v", value, err)
		}
	}

	clarification := RetrievalSummary{
		Rewrites: []string{}, RequestedMode: retrievaldomain.SearchModeHybrid, EffectiveMode: retrievaldomain.SearchModeHybrid,
		Scope:        RetrievalScopeSummary{SourceIDs: []foundation.ID{}, SourceVersionIDs: []foundation.ID{}, PathPrefixes: []string{}},
		Degradations: []RetrievalDegradation{},
	}
	if err := ValidateRetrievalSummary(testWorkspaceID, AnswerPublicationClarificationRequired, clarification); err != nil {
		t.Fatalf("clarification summary: %v", err)
	}
	clarification.CandidateCount = 1
	if err := ValidateRetrievalSummary(testWorkspaceID, AnswerPublicationClarificationRequired, clarification); errorCode(err) != ErrorCodeRetrievalSummaryInvalid {
		t.Fatalf("clarification with retrieval err=%v", err)
	}
}

func stringIDPointer(value string) *foundation.ID {
	id := foundation.ID(value)
	return &id
}

func copyRetrievalScopeSummary(value RetrievalScopeSummary) RetrievalScopeSummary {
	value.SourceIDs = append([]foundation.ID{}, value.SourceIDs...)
	value.SourceVersionIDs = append([]foundation.ID{}, value.SourceVersionIDs...)
	value.PathPrefixes = append([]string{}, value.PathPrefixes...)
	return value
}
