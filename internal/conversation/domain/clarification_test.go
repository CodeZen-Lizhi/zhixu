package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

func TestClarificationResultIsStrictBoundedAndDistinct(t *testing.T) {
	result := ClarificationResult{
		ResultType: agentdomain.ResultTypeClarification, SchemaID: ClarificationSchemaID,
		SchemaVersion: ClarificationSchemaVersionV1, ModelRunRef: "10000000-0000-4000-8000-000000000006",
		Payload: ClarificationPayload{
			Reason: "the environment is ambiguous", Question: "Which environment should be used?",
			SuggestedScopes: []string{"production", "development"},
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeClarification(raw, foundationstrictjson.DefaultLimits())
	if err != nil || decoded.Payload.Question != result.Payload.Question {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}

	invalidPayloads := []ClarificationPayload{
		{Reason: "", Question: "Which environment?", SuggestedScopes: []string{}},
		{Reason: "ambiguous", Question: "unsafe\x00question", SuggestedScopes: []string{}},
		{Reason: "ambiguous", Question: strings.Repeat("x", MaxClarificationQuestionBytes+1), SuggestedScopes: []string{}},
		{Reason: "ambiguous", Question: "Which environment?", SuggestedScopes: []string{"prod", "prod"}},
		{Reason: "ambiguous", Question: "Which environment?", SuggestedScopes: nil},
	}
	for _, payload := range invalidPayloads {
		if err := payload.Validate(); errorCode(err) != ErrorCodeClarificationInvalid {
			t.Fatalf("payload=%#v err=%v", payload, err)
		}
	}

	unknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := DecodeClarification(unknown, foundationstrictjson.DefaultLimits()); errorCode(err) != ErrorCodeClarificationInvalid {
		t.Fatalf("unknown field err=%v", err)
	}
	duplicate := []byte(`{"result_type":"clarification","result_type":"clarification","schema_id":"conversation.clarification","schema_version":"v1","model_run_ref":"10000000-0000-4000-8000-000000000006","payload":{"reason":"ambiguous","question":"Which environment?","suggested_scopes":[]}}`)
	if _, err := DecodeClarification(duplicate, foundationstrictjson.DefaultLimits()); errorCode(err) != ErrorCodeClarificationInvalid {
		t.Fatalf("duplicate field err=%v", err)
	}
}

func errorCode(err error) string {
	var typed *foundation.Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}
