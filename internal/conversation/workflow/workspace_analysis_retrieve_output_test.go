package workflow

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestWorkspaceAnalysisRetrieveEvidenceOutputCanonicalRoundTrip(t *testing.T) {
	output := workspaceAnalysisRetrieveEvidenceOutputFixture()
	encoded, err := EncodeWorkspaceAnalysisRetrieveEvidenceOutput(output)
	if err != nil {
		t.Fatalf("EncodeWorkspaceAnalysisRetrieveEvidenceOutput: %v", err)
	}
	want := `{"schema_version":1,"git_tool_call_id":"93000000-0000-4000-8000-000000000001","git_tool_receipt_id":"93000000-0000-4000-8000-000000000002","git_tool_receipt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","planner_model_run_id":"93000000-0000-4000-8000-000000000003","planner_model_call_id":"93000000-0000-4000-8000-000000000004","planner_model_result_id":"93000000-0000-4000-8000-000000000005","planner_model_result_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","search_tool_call_id":"93000000-0000-4000-8000-000000000006","search_tool_receipt_id":"93000000-0000-4000-8000-000000000007","search_tool_receipt_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","search_summary":{"effective_mode":"hybrid","evidence_refs":["E1","E2","E3"],"hit_count":3,"degradation_codes":["EMBEDDING_UNAVAILABLE","RERANK_UNAVAILABLE"]}}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s\nwant    = %s", encoded, want)
	}
	decoded, err := DecodeWorkspaceAnalysisRetrieveEvidenceOutput(encoded)
	if err != nil {
		t.Fatalf("DecodeWorkspaceAnalysisRetrieveEvidenceOutput: %v", err)
	}
	if !reflect.DeepEqual(decoded, output) {
		t.Fatalf("decoded = %#v, want %#v", decoded, output)
	}

	reordered := json.RawMessage(`{"search_summary":{"hit_count":3,"degradation_codes":["EMBEDDING_UNAVAILABLE","RERANK_UNAVAILABLE"],"evidence_refs":["E1","E2","E3"],"effective_mode":"hybrid"},"search_tool_receipt_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","search_tool_receipt_id":"93000000-0000-4000-8000-000000000007","search_tool_call_id":"93000000-0000-4000-8000-000000000006","planner_model_result_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","planner_model_result_id":"93000000-0000-4000-8000-000000000005","planner_model_call_id":"93000000-0000-4000-8000-000000000004","planner_model_run_id":"93000000-0000-4000-8000-000000000003","git_tool_receipt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","git_tool_receipt_id":"93000000-0000-4000-8000-000000000002","git_tool_call_id":"93000000-0000-4000-8000-000000000001","schema_version":1}`)
	replayed, err := DecodeWorkspaceAnalysisRetrieveEvidenceOutput(reordered)
	if err != nil {
		t.Fatalf("decode reordered output: %v", err)
	}
	reencoded, err := EncodeWorkspaceAnalysisRetrieveEvidenceOutput(replayed)
	if err != nil {
		t.Fatalf("encode reordered output: %v", err)
	}
	if string(reencoded) != want {
		t.Fatalf("replayed output is not canonical: %s", reencoded)
	}
}

func TestWorkspaceAnalysisRetrieveEvidenceOutputRejectsStrictJSON(t *testing.T) {
	canonical, err := EncodeWorkspaceAnalysisRetrieveEvidenceOutput(workspaceAnalysisRetrieveEvidenceOutputFixture())
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]json.RawMessage{
		"unknown outer":     json.RawMessage(strings.TrimSuffix(string(canonical), "}") + `,"prompt":"secret"}`),
		"unknown nested":    json.RawMessage(strings.Replace(string(canonical), `"degradation_codes":["EMBEDDING_UNAVAILABLE","RERANK_UNAVAILABLE"]`, `"degradation_codes":["EMBEDDING_UNAVAILABLE","RERANK_UNAVAILABLE"],"snippet":"secret"`, 1)),
		"duplicate outer":   json.RawMessage(strings.Replace(string(canonical), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)),
		"duplicate nested":  json.RawMessage(strings.Replace(string(canonical), `"hit_count":3`, `"hit_count":3,"hit_count":3`, 1)),
		"null document":     json.RawMessage(`null`),
		"null binding":      json.RawMessage(strings.Replace(string(canonical), `"git_tool_call_id":"93000000-0000-4000-8000-000000000001"`, `"git_tool_call_id":null`, 1)),
		"null summary":      json.RawMessage(strings.Replace(string(canonical), `"search_summary":{"effective_mode":"hybrid","evidence_refs":["E1","E2","E3"],"hit_count":3,"degradation_codes":["EMBEDDING_UNAVAILABLE","RERANK_UNAVAILABLE"]}`, `"search_summary":null`, 1)),
		"null references":   json.RawMessage(strings.Replace(string(canonical), `"evidence_refs":["E1","E2","E3"]`, `"evidence_refs":null`, 1)),
		"null degradations": json.RawMessage(strings.Replace(string(canonical), `"degradation_codes":["EMBEDDING_UNAVAILABLE","RERANK_UNAVAILABLE"]`, `"degradation_codes":null`, 1)),
		"trailing":          append(append(json.RawMessage(nil), canonical...), []byte(` {}`)...),
		"oversize":          append(append(json.RawMessage(nil), canonical...), []byte(strings.Repeat(" ", workspaceAnalysisMaxRetrieveOutputBytes))...),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWorkspaceAnalysisRetrieveEvidenceOutput(raw); err == nil {
				t.Fatal("DecodeWorkspaceAnalysisRetrieveEvidenceOutput accepted invalid input")
			}
		})
	}
}

func TestWorkspaceAnalysisRetrieveEvidenceOutputRejectsBindingAndBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisRetrieveEvidenceOutput)
	}{
		{name: "schema", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) { value.SchemaVersion++ }},
		{name: "invalid id", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) { value.GitToolCallID = "invalid" }},
		{name: "non canonical id", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.GitToolCallID = "93000000-0000-4000-8000-00000000000A"
		}},
		{name: "reused identity", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.SearchToolReceiptID = value.GitToolReceiptID
		}},
		{name: "git hash", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.GitToolReceiptHash = strings.Repeat("A", 64)
		}},
		{name: "planner hash", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.PlannerModelResultHash = strings.Repeat("b", 63)
		}},
		{name: "search hash", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.SearchToolReceiptHash = strings.Repeat("g", 64)
		}},
		{name: "negative hit count", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) { value.SearchSummary.HitCount = -1 }},
		{name: "hit count over limit", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.SearchSummary.HitCount = workspaceAnalysisMaxSearchHits + 1
		}},
		{name: "hit count drift", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) { value.SearchSummary.HitCount = 2 }},
		{name: "nil references", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) { value.SearchSummary.EvidenceRefs = nil }},
		{name: "reference gap", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) { value.SearchSummary.EvidenceRefs[1] = "E3" }},
		{name: "reference out of bounds", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.SearchSummary.EvidenceRefs = []string{"E1", "E2", "E3", "E4", "E5", "E6"}
			value.SearchSummary.HitCount = 6
		}},
		{name: "mode", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) { value.SearchSummary.EffectiveMode = "auto" }},
		{name: "nil degradations", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) { value.SearchSummary.DegradationCodes = nil }},
		{name: "unsorted degradations", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.SearchSummary.DegradationCodes = []string{"RERANK_UNAVAILABLE", "EMBEDDING_UNAVAILABLE"}
		}},
		{name: "duplicate degradation", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.SearchSummary.DegradationCodes = []string{"EMBEDDING_UNAVAILABLE", "EMBEDDING_UNAVAILABLE"}
		}},
		{name: "invalid degradation", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.SearchSummary.DegradationCodes = []string{"provider-body"}
		}},
		{name: "degradation code over limit", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.SearchSummary.DegradationCodes = []string{"A" + strings.Repeat("B", workspaceAnalysisMaxDegradationCodeBytes)}
		}},
		{name: "degradation count over limit", mutate: func(value *WorkspaceAnalysisRetrieveEvidenceOutput) {
			value.SearchSummary.DegradationCodes = workspaceAnalysisDegradationCodes(workspaceAnalysisMaxSearchDegradations + 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := workspaceAnalysisRetrieveEvidenceOutputFixture()
			test.mutate(&candidate)
			if _, err := EncodeWorkspaceAnalysisRetrieveEvidenceOutput(candidate); err == nil {
				t.Fatalf("EncodeWorkspaceAnalysisRetrieveEvidenceOutput accepted %#v", candidate)
			}
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeWorkspaceAnalysisRetrieveEvidenceOutput(raw); err == nil {
				t.Fatalf("DecodeWorkspaceAnalysisRetrieveEvidenceOutput accepted %s", raw)
			}
		})
	}
}

func TestWorkspaceAnalysisRetrieveEvidenceOutputAcceptsEmptyAndMaximumSearchSummary(t *testing.T) {
	tests := []WorkspaceAnalysisRetrieveSearchSummary{
		{EffectiveMode: "semantic", EvidenceRefs: []string{}, HitCount: 0, DegradationCodes: []string{}},
		{
			EffectiveMode: "hybrid", EvidenceRefs: []string{"E1", "E2", "E3", "E4", "E5"}, HitCount: 5,
			DegradationCodes: workspaceAnalysisDegradationCodes(workspaceAnalysisMaxSearchDegradations),
		},
	}
	for _, summary := range tests {
		output := workspaceAnalysisRetrieveEvidenceOutputFixture()
		output.SearchSummary = summary
		encoded, err := EncodeWorkspaceAnalysisRetrieveEvidenceOutput(output)
		if err != nil {
			t.Fatalf("encode summary %#v: %v", summary, err)
		}
		decoded, err := DecodeWorkspaceAnalysisRetrieveEvidenceOutput(encoded)
		if err != nil {
			t.Fatalf("decode summary %#v: %v", summary, err)
		}
		if !reflect.DeepEqual(decoded.SearchSummary, summary) {
			t.Fatalf("decoded summary = %#v, want %#v", decoded.SearchSummary, summary)
		}
	}

	empty, err := DecodeWorkspaceAnalysisRetrieveSearchSummary(json.RawMessage(`{"effective_mode":"keyword","items":[],"degradations":[]}`))
	if err != nil {
		t.Fatalf("decode zero-hit SearchKnowledge@2 output: %v", err)
	}
	if empty.HitCount != 0 || empty.EvidenceRefs == nil || empty.DegradationCodes == nil {
		t.Fatalf("zero-hit summary lost required empty collections: %#v", empty)
	}
}

func TestDecodeWorkspaceAnalysisRetrieveSearchSummaryProjectsOnlySafeFacts(t *testing.T) {
	raw := json.RawMessage(`{"effective_mode":"keyword","items":[{"evidence_ref":"E1","rank":1,"snippet":"SNIPPET_CANARY /private/workspace/secret.md"},{"evidence_ref":"E2","rank":2,"snippet":"PROVIDER_BODY_CANARY"}],"degradations":["EMBEDDING_UNAVAILABLE"]}`)
	summary, err := DecodeWorkspaceAnalysisRetrieveSearchSummary(raw)
	if err != nil {
		t.Fatalf("DecodeWorkspaceAnalysisRetrieveSearchSummary: %v", err)
	}
	if !reflect.DeepEqual(summary, WorkspaceAnalysisRetrieveSearchSummary{
		EffectiveMode: "keyword", EvidenceRefs: []string{"E1", "E2"}, HitCount: 2,
		DegradationCodes: []string{"EMBEDDING_UNAVAILABLE"},
	}) {
		t.Fatalf("summary = %#v", summary)
	}
	output := workspaceAnalysisRetrieveEvidenceOutputFixture()
	output.SearchSummary = summary
	encoded, err := EncodeWorkspaceAnalysisRetrieveEvidenceOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	for index, forbidden := range []string{
		"SNIPPET_CANARY", "PROVIDER_BODY_CANARY", "snippet", "excerpt", "citation_id", "index_version_id",
		"chunk_id", "source_version_id", "source_span_id", "workspace_id", "analysis_run_id", "operation_id",
		"node_attempt_id", "private_binding", "prompt", "path", "/private/workspace/secret.md",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("retrieve output contains forbidden field or value at index %d", index)
		}
	}
}

func TestDecodeWorkspaceAnalysisRetrieveSearchSummaryRejectsUnsafeOrMalformedReceipt(t *testing.T) {
	valid := `{"effective_mode":"keyword","items":[{"evidence_ref":"E1","rank":1,"snippet":"bounded evidence"}],"degradations":[]}`
	tests := map[string]json.RawMessage{
		"unknown":              json.RawMessage(strings.Replace(valid, `"degradations":[]`, `"degradations":[],"private_binding":{"path":"secret"}`, 1)),
		"private item":         json.RawMessage(strings.Replace(valid, `"snippet":"bounded evidence"`, `"snippet":"bounded evidence","source_version_id":"93000000-0000-4000-8000-000000000009"`, 1)),
		"duplicate":            json.RawMessage(strings.Replace(valid, `"effective_mode":"keyword"`, `"effective_mode":"keyword","effective_mode":"keyword"`, 1)),
		"null items":           json.RawMessage(strings.Replace(valid, `"items":[{"evidence_ref":"E1","rank":1,"snippet":"bounded evidence"}]`, `"items":null`, 1)),
		"null snippet":         json.RawMessage(strings.Replace(valid, `"snippet":"bounded evidence"`, `"snippet":null`, 1)),
		"rank drift":           json.RawMessage(strings.Replace(valid, `"rank":1`, `"rank":2`, 1)),
		"reference drift":      json.RawMessage(strings.Replace(valid, `"E1"`, `"E2"`, 1)),
		"invalid mode":         json.RawMessage(strings.Replace(valid, `"keyword"`, `"auto"`, 1)),
		"unsorted degradation": json.RawMessage(strings.Replace(valid, `"degradations":[]`, `"degradations":["RERANK_UNAVAILABLE","EMBEDDING_UNAVAILABLE"]`, 1)),
		"trailing":             json.RawMessage(valid + `{}`),
		"oversize snippet":     json.RawMessage(strings.Replace(valid, `bounded evidence`, strings.Repeat("x", workspaceAnalysisMaxSearchSnippetBytes+1), 1)),
		"oversize receipt":     json.RawMessage(valid + strings.Repeat(" ", int(toolsdomain.SearchKnowledgeV2ReceiptMaxOutputBytes))),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWorkspaceAnalysisRetrieveSearchSummary(raw); err == nil {
				t.Fatal("DecodeWorkspaceAnalysisRetrieveSearchSummary accepted invalid input")
			}
		})
	}
}

func workspaceAnalysisRetrieveEvidenceOutputFixture() WorkspaceAnalysisRetrieveEvidenceOutput {
	return WorkspaceAnalysisRetrieveEvidenceOutput{
		SchemaVersion:          WorkspaceAnalysisOutputSchemaVersion,
		GitToolCallID:          workspaceAnalysisRetrieveOutputID(1),
		GitToolReceiptID:       workspaceAnalysisRetrieveOutputID(2),
		GitToolReceiptHash:     strings.Repeat("a", 64),
		PlannerModelRunID:      workspaceAnalysisRetrieveOutputID(3),
		PlannerModelCallID:     workspaceAnalysisRetrieveOutputID(4),
		PlannerModelResultID:   workspaceAnalysisRetrieveOutputID(5),
		PlannerModelResultHash: strings.Repeat("b", 64),
		SearchToolCallID:       workspaceAnalysisRetrieveOutputID(6),
		SearchToolReceiptID:    workspaceAnalysisRetrieveOutputID(7),
		SearchToolReceiptHash:  strings.Repeat("c", 64),
		SearchSummary: WorkspaceAnalysisRetrieveSearchSummary{
			EffectiveMode: "hybrid", EvidenceRefs: []string{"E1", "E2", "E3"}, HitCount: 3,
			DegradationCodes: []string{"EMBEDDING_UNAVAILABLE", "RERANK_UNAVAILABLE"},
		},
	}
}

func workspaceAnalysisRetrieveOutputID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("93000000-0000-4000-8000-%012d", value))
}

func workspaceAnalysisDegradationCodes(count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = "CODE_" + string([]byte{byte('A' + index/26), byte('A' + index%26)})
	}
	return values
}
