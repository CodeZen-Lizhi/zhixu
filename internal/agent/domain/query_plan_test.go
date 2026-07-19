package domain

import (
	"encoding/json"
	"testing"
)

func TestRAGQueryPlanSeparatesRetrievalFromClarification(t *testing.T) {
	plan := RAGQueryPlanResult{
		ResultType: ResultTypeRAGQueryPlan, SchemaID: RAGQueryPlanSchemaID,
		SchemaVersion: OutputSchemaVersionV1, ModelRunRef: testModelRunID,
		Payload: RAGQueryPlanPayload{
			Intent: "compare deployment behavior", RequiresClarification: false,
			Rewrites: []string{"deployment behavior", "runtime deployment behavior"}, SuggestedScopes: []string{},
		},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRAGQueryPlan(raw, DefaultDecodeLimits()); err != nil {
		t.Fatalf("decode retrieval plan: %v", err)
	}

	clarification := plan
	clarification.Payload = RAGQueryPlanPayload{
		Intent: "resolve deployment target", RequiresClarification: true, Rewrites: []string{},
		ClarificationReason:   "the target environment is missing",
		ClarificationQuestion: "Which deployment environment should be used?",
		SuggestedScopes:       []string{"production", "development"},
	}
	if err := clarification.Validate(); err != nil {
		t.Fatalf("validate clarification plan: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RAGQueryPlanPayload)
	}{
		{name: "missing rewrites", mutate: func(value *RAGQueryPlanPayload) { value.Rewrites = nil }},
		{name: "duplicate rewrites", mutate: func(value *RAGQueryPlanPayload) { value.Rewrites = []string{"same", "same"} }},
		{name: "too many rewrites", mutate: func(value *RAGQueryPlanPayload) { value.Rewrites = []string{"one", "two", "three", "four"} }},
		{name: "nul rewrite", mutate: func(value *RAGQueryPlanPayload) { value.Rewrites = []string{"unsafe\x00query"} }},
		{name: "clarification mixed with retrieval", mutate: func(value *RAGQueryPlanPayload) {
			value.RequiresClarification = true
			value.ClarificationReason = "missing scope"
			value.ClarificationQuestion = "Which scope?"
		}},
		{name: "clarification details without flag", mutate: func(value *RAGQueryPlanPayload) { value.ClarificationReason = "invented" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := plan.Payload
			value.Rewrites = append([]string(nil), plan.Payload.Rewrites...)
			value.SuggestedScopes = append([]string(nil), plan.Payload.SuggestedScopes...)
			test.mutate(&value)
			if err := value.Validate(); errorCode(err) != ErrorCodeQueryPlanInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
