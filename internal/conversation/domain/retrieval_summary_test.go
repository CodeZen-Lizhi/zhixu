package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestDecodeRetrievalSummaryStrictlyRestoresCanonicalDocument(t *testing.T) {
	raw := json.RawMessage(`{
		"selected_count":1,
		"candidate_count":2,
		"conflict_count":0,
		"degradations":[
			{"capability":"rerank","code":"RERANK_UNAVAILABLE","retryable":true},
			{"capability":"vector","code":"EMBEDDING_UNAVAILABLE","retryable":true}
		],
		"scope":{
			"allow_web":false,
			"captured_at_before":null,
			"source_version_ids":[],
			"path_prefixes":[" docs/runtime/ ","docs/runtime"],
			"allow_original_sources":false,
			"source_ids":[
				"10000000-0000-4000-8000-000000000004",
				"10000000-0000-4000-8000-000000000003",
				"10000000-0000-4000-8000-000000000004"
			],
			"captured_at_from":"2026-07-19T08:00:00+08:00"
		},
		"requested_mode":"hybrid",
		"effective_mode":"keyword",
		"rewrites":["runtime recovery"],
		"index_version_id":"10000000-0000-4000-8000-000000000020",
		"embedding_version_id":null
	}`)

	decoded, err := DecodeRetrievalSummary(testWorkspaceID, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Scope.SourceIDs) != 2 || decoded.Scope.SourceIDs[0] != "10000000-0000-4000-8000-000000000003" ||
		len(decoded.Scope.PathPrefixes) != 1 || decoded.Scope.PathPrefixes[0] != "docs/runtime" ||
		decoded.Scope.CapturedAtFrom == nil || !decoded.Scope.CapturedAtFrom.Equal(time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)) ||
		decoded.Scope.CapturedAtFrom.Location() != time.UTC || len(decoded.Degradations) != 2 ||
		decoded.Degradations[0].Capability != retrievaldomain.SearchDegradationVector {
		t.Fatalf("decoded=%#v", decoded)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := DecodeRetrievalSummary(testWorkspaceID, encoded)
	if err != nil || !reflect.DeepEqual(roundTrip, decoded) {
		t.Fatalf("round trip=%#v err=%v encoded=%s", roundTrip, err, encoded)
	}
}

func TestDecodeRetrievalSummaryRejectsMalformedPersistenceDocument(t *testing.T) {
	valid := `{"rewrites":[],"requested_mode":"hybrid","effective_mode":"hybrid","scope":{"source_ids":[],"source_version_ids":[],"path_prefixes":[],"captured_at_from":null,"captured_at_before":null,"allow_original_sources":false,"allow_web":false},"index_version_id":null,"embedding_version_id":null,"candidate_count":0,"selected_count":0,"conflict_count":0,"degradations":[]}`
	tests := []struct {
		name        string
		workspaceID foundation.ID
		raw         string
	}{
		{name: "invalid workspace", workspaceID: "bad", raw: valid},
		{name: "unknown field", workspaceID: testWorkspaceID, raw: valid[:len(valid)-1] + `,"unknown":true}`},
		{name: "unknown nested field", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"allow_web":false}`, `"allow_web":false,"unknown":true}`, 1)},
		{name: "duplicate field", workspaceID: testWorkspaceID, raw: valid[:len(valid)-1] + `,"candidate_count":0}`},
		{name: "trailing value", workspaceID: testWorkspaceID, raw: valid + `{}`},
		{name: "invalid type", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"candidate_count":0`, `"candidate_count":"0"`, 1)},
		{name: "null count", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"candidate_count":0`, `"candidate_count":null`, 1)},
		{name: "missing count", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `,"candidate_count":0`, "", 1)},
		{name: "null scope boolean", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"allow_web":false`, `"allow_web":null`, 1)},
		{name: "missing nullable id", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"index_version_id":null,`, "", 1)},
		{name: "null degradation boolean", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"degradations":[]`, `"degradations":[{"capability":"vector","code":"EMBEDDING_UNAVAILABLE","retryable":null}]`, 1)},
		{name: "null required list", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"rewrites":[]`, `"rewrites":null`, 1)},
		{name: "invalid mode", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"requested_mode":"hybrid"`, `"requested_mode":"magic"`, 1)},
		{name: "invalid time", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"captured_at_from":null`, `"captured_at_from":"tomorrow"`, 1)},
		{name: "duplicate rewrite", workspaceID: testWorkspaceID, raw: strings.Replace(valid, `"rewrites":[]`, `"rewrites":["same","same"]`, 1)},
		{name: "oversized document", workspaceID: testWorkspaceID, raw: strings.Repeat(" ", MaxRetrievalSummaryBytes) + valid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeRetrievalSummary(test.workspaceID, json.RawMessage(test.raw))
			var typed *foundation.Error
			if !errors.As(err, &typed) || typed.Code != ErrorCodeRetrievalSummaryInvalid || typed.Kind != foundation.ErrorInvalidInput {
				t.Fatalf("err=%v", err)
			}
		})
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
