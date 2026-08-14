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

func TestRAGQueryPlanProviderV2ComposesIdentitylessWire(t *testing.T) {
	provider := RAGQueryPlanProviderResultV2{
		Intent:          "find policy",
		Rewrites:        []string{"policy location"},
		SuggestedScopes: []string{},
	}
	raw := []byte(`{"i":"find policy","r":["policy location"],"d":"","q":"","s":[]}`)
	decoded, err := DecodeRAGQueryPlanProviderV2(raw, DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("DecodeRAGQueryPlanProviderV2() error = %v", err)
	}
	if decoded.Intent != provider.Intent || len(decoded.Rewrites) != 1 || decoded.ClarificationReason != "" {
		t.Fatalf("decoded = %+v", decoded)
	}
	composed, err := decoded.Compose(testModelRunID, "where is the policy?")
	if err != nil {
		t.Fatalf("Compose() error = %v", err)
	}
	if composed.ModelRunRef != testModelRunID || composed.SchemaVersion != OutputSchemaVersionV1 || composed.Payload.RequiresClarification ||
		len(composed.Payload.Rewrites) != 2 || composed.Payload.Rewrites[0] != "where is the policy?" || composed.Payload.Rewrites[1] != "policy location" {
		t.Fatalf("composed = %+v", composed)
	}
}

func TestRAGQueryPlanProviderV2DerivesClarificationAndNormalizesRetrievalBranch(t *testing.T) {
	clarification := RAGQueryPlanProviderResultV2{
		Intent:                "identify policy",
		Rewrites:              []string{},
		ClarificationReason:   "scope is missing",
		ClarificationQuestion: "Which workspace?",
		SuggestedScopes:       []string{"current workspace"},
	}
	if err := clarification.Validate(); err != nil {
		t.Fatalf("clarification Validate() error = %v", err)
	}
	retrieval := clarification
	retrieval.Rewrites = []string{"1", "policy location"}
	composed, err := retrieval.Compose(testModelRunID, "where is the policy?")
	if err != nil {
		t.Fatalf("retrieval Compose() error = %v", err)
	}
	if composed.Payload.RequiresClarification || composed.Payload.ClarificationReason != "" || composed.Payload.ClarificationQuestion != "" ||
		len(composed.Payload.SuggestedScopes) != 0 || len(composed.Payload.Rewrites) != 2 ||
		composed.Payload.Rewrites[0] != "where is the policy?" || composed.Payload.Rewrites[1] != "policy location" {
		t.Fatalf("normalized retrieval = %+v", composed.Payload)
	}
	fallback, err := DecodeRAGQueryPlanProviderV2([]byte(`{"i":"clarify","r":[],"d":"","q":"","s":["current workspace"]}`), DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("fallback DecodeRAGQueryPlanProviderV2() error = %v", err)
	}
	fallbackPlan, err := fallback.Compose(testModelRunID, "where is the policy?")
	if err != nil || fallbackPlan.Payload.RequiresClarification || len(fallbackPlan.Payload.Rewrites) != 1 ||
		fallbackPlan.Payload.Rewrites[0] != "where is the policy?" || len(fallbackPlan.Payload.SuggestedScopes) != 0 {
		t.Fatalf("fallback plan=%+v error=%v", fallbackPlan.Payload, err)
	}
	punctuation := RAGQueryPlanProviderResultV2{
		Intent: "clarify", Rewrites: []string{}, ClarificationReason: "?", ClarificationQuestion: "?", SuggestedScopes: []string{},
	}
	punctuationPlan, err := punctuation.Compose(testModelRunID, "where is the policy?")
	if err != nil || punctuationPlan.Payload.RequiresClarification || len(punctuationPlan.Payload.Rewrites) != 1 ||
		punctuationPlan.Payload.Rewrites[0] != "where is the policy?" {
		t.Fatalf("punctuation fallback=%+v error=%v", punctuationPlan.Payload, err)
	}
	partialActionable := punctuation
	partialActionable.ClarificationReason = "scope is missing"
	partialActionablePlan, err := partialActionable.Compose(testModelRunID, "where is the policy?")
	if err != nil || partialActionablePlan.Payload.RequiresClarification || len(partialActionablePlan.Payload.Rewrites) != 1 {
		t.Fatalf("partial actionable fallback=%+v error=%v", partialActionablePlan.Payload, err)
	}
	if _, err := DecodeRAGQueryPlanProviderV2([]byte(`{"i":"x","r":[],"d":"scope missing","q":"","s":[]}`), DefaultDecodeLimits()); errorCode(err) != ErrorCodeQueryPlanInvalid {
		t.Fatalf("partial clarification error = %v code=%q", err, errorCode(err))
	}
}

func TestRAGQueryPlanProviderV2RejectsInvalidSourceQuery(t *testing.T) {
	provider := RAGQueryPlanProviderResultV2{Intent: "find policy", Rewrites: []string{"policy"}, SuggestedScopes: []string{}}
	for _, query := range []string{"", " padded ", "unsafe\x00query"} {
		if _, err := provider.Compose(testModelRunID, query); errorCode(err) != ErrorCodeQueryPlanInvalid {
			t.Fatalf("Compose(%q) error=%v code=%q", query, err, errorCode(err))
		}
	}
}

func TestRAGQueryPlanProviderV2RejectsIdentityAndUnknownFields(t *testing.T) {
	for _, raw := range []string{
		`{"i":"x","r":["x"],"d":"","q":"","s":[],"model_run_ref":"61000000-0000-4000-8000-000000000001"}`,
		`{"i":"x","r":["x"],"d":"","q":"","s":[],"extra":true}`,
		`{"i":"x","r":["x"],"q":"","s":[]}`,
	} {
		if _, err := DecodeRAGQueryPlanProviderV2([]byte(raw), DefaultDecodeLimits()); err == nil {
			t.Fatalf("DecodeRAGQueryPlanProviderV2(%s) unexpectedly succeeded", raw)
		}
	}
}

func TestRAGQueryPlanProviderV2RejectsDuplicateProviderLists(t *testing.T) {
	for _, raw := range []string{
		`{"i":"find policy","r":["policy","policy"],"d":"","q":"","s":[]}`,
		`{"i":"clarify scope","r":[],"d":"scope is missing","q":"Which workspace?","s":["current","current"]}`,
	} {
		if _, err := DecodeRAGQueryPlanProviderV2([]byte(raw), DefaultDecodeLimits()); errorCode(err) != ErrorCodeQueryPlanInvalid {
			t.Fatalf("duplicate provider list error=%v code=%q", err, errorCode(err))
		}
	}
}
