package workflow

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisValidationOutputCanonicalRoundTrip(t *testing.T) {
	output := workspaceAnalysisValidationOutputFixture()
	document, err := EncodeWorkspaceAnalysisValidationOutput(output)
	if err != nil {
		t.Fatalf("EncodeWorkspaceAnalysisValidationOutput: %v", err)
	}
	want := `{"schema_version":1,"candidate_id":"95000000-0000-4000-8000-000000000001","candidate_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","validation_tool_call_id":"95000000-0000-4000-8000-000000000002","validation_tool_receipt_id":"95000000-0000-4000-8000-000000000003","validation_tool_receipt_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","results":[{"evidence_ref":"E2","valid":true,"reason_code":"OK"},{"evidence_ref":"E1","valid":false,"reason_code":"EVIDENCE_INELIGIBLE"}]}`
	if string(document) != want {
		t.Fatalf("document = %s\nwant     = %s", document, want)
	}
	decoded, err := DecodeWorkspaceAnalysisValidationOutput(document)
	if err != nil {
		t.Fatalf("DecodeWorkspaceAnalysisValidationOutput: %v", err)
	}
	if !reflect.DeepEqual(decoded, output) {
		t.Fatalf("decoded = %#v, want %#v", decoded, output)
	}
	reencoded, err := EncodeWorkspaceAnalysisValidationOutput(decoded)
	if err != nil || string(reencoded) != string(document) {
		t.Fatalf("reencode = %s, %v", reencoded, err)
	}
}

func TestWorkspaceAnalysisValidationOutputRejectsStrictJSONAndUnsafeFields(t *testing.T) {
	canonical, err := EncodeWorkspaceAnalysisValidationOutput(workspaceAnalysisValidationOutputFixture())
	if err != nil {
		t.Fatal(err)
	}
	valid := string(canonical)
	tests := map[string]json.RawMessage{
		"unknown outer":    json.RawMessage(strings.TrimSuffix(valid, "}") + `,"extra":true}`),
		"unknown nested":   json.RawMessage(strings.Replace(valid, `"evidence_ref":"E2"`, `"evidence_ref":"E2","citation_id":"secret"`, 1)),
		"duplicate outer":  json.RawMessage(strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)),
		"duplicate nested": json.RawMessage(strings.Replace(valid, `"valid":true`, `"valid":true,"valid":true`, 1)),
		"null document":    json.RawMessage(`null`),
		"missing results":  json.RawMessage(strings.Replace(valid, `,"results":[{"evidence_ref":"E2","valid":true,"reason_code":"OK"},{"evidence_ref":"E1","valid":false,"reason_code":"EVIDENCE_INELIGIBLE"}]`, "", 1)),
		"null results": json.RawMessage(strings.Replace(valid,
			`"results":[{"evidence_ref":"E2","valid":true,"reason_code":"OK"},{"evidence_ref":"E1","valid":false,"reason_code":"EVIDENCE_INELIGIBLE"}]`,
			`"results":null`, 1)),
		"null result":     json.RawMessage(strings.Replace(valid, `[{"evidence_ref":"E2","valid":true,"reason_code":"OK"}`, `[null`, 1)),
		"null field":      json.RawMessage(strings.Replace(valid, `"candidate_hash":"`+strings.Repeat("a", 64)+`"`, `"candidate_hash":null`, 1)),
		"trailing":        json.RawMessage(valid + `{}`),
		"top-level array": json.RawMessage(`[]`),
		"oversize":        json.RawMessage(valid + strings.Repeat(" ", workspaceAnalysisMaxValidationOutputBytes)),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWorkspaceAnalysisValidationOutput(raw); err == nil {
				t.Fatalf("DecodeWorkspaceAnalysisValidationOutput accepted %s", raw)
			}
		})
	}

	for _, field := range []string{
		"excerpt", "content", "content_hash", "citation_id", "index_version_id", "chunk_id",
		"source_version_id", "source_span_id", "workspace_id", "workflow_run_id", "analysis_run_id",
		"operation_id", "node_attempt_id", "private_binding", "prompt", "path",
	} {
		t.Run("forbidden "+field, func(t *testing.T) {
			raw := json.RawMessage(strings.Replace(valid, `"evidence_ref":"E2"`,
				`"evidence_ref":"E2","`+field+`":"IDENTITY_BODY_CANARY"`, 1))
			if _, err := DecodeWorkspaceAnalysisValidationOutput(raw); err == nil {
				t.Fatalf("accepted forbidden field %q", field)
			}
		})
	}
}

func TestWorkspaceAnalysisValidationOutputRejectsBindingAndResultBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisValidationOutput)
	}{
		{name: "schema", mutate: func(value *WorkspaceAnalysisValidationOutput) { value.SchemaVersion++ }},
		{name: "nil results", mutate: func(value *WorkspaceAnalysisValidationOutput) { value.Results = nil }},
		{name: "empty results", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.Results = []WorkspaceAnalysisCitationValidationResult{}
		}},
		{name: "too many results", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.Results = append(value.Results,
				WorkspaceAnalysisCitationValidationResult{EvidenceRef: "E3", Valid: false, ReasonCode: WorkspaceAnalysisCitationValidationReasonBindingMismatch},
				WorkspaceAnalysisCitationValidationResult{EvidenceRef: "E2", Valid: true, ReasonCode: WorkspaceAnalysisCitationValidationReasonOK},
			)
		}},
		{name: "invalid id", mutate: func(value *WorkspaceAnalysisValidationOutput) { value.CandidateID = "invalid" }},
		{name: "non canonical id", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.ValidationToolCallID = "95000000-0000-4000-8000-00000000000A"
		}},
		{name: "candidate call collision", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.ValidationToolCallID = value.CandidateID
		}},
		{name: "call receipt collision", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.ValidationToolReceiptID = value.ValidationToolCallID
		}},
		{name: "uppercase candidate hash", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.CandidateHash = strings.Repeat("A", 64)
		}},
		{name: "short receipt hash", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.ValidationToolReceiptHash = strings.Repeat("b", 63)
		}},
		{name: "non hex receipt hash", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.ValidationToolReceiptHash = strings.Repeat("g", 64)
		}},
		{name: "reference out of range", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.Results[0].EvidenceRef = "E4"
		}},
		{name: "duplicate reference", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.Results[1].EvidenceRef = value.Results[0].EvidenceRef
		}},
		{name: "unknown reason", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.Results[0].ReasonCode = "OTHER"
		}},
		{name: "valid non ok", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.Results[0].ReasonCode = WorkspaceAnalysisCitationValidationReasonBindingMismatch
		}},
		{name: "invalid ok", mutate: func(value *WorkspaceAnalysisValidationOutput) {
			value.Results[1].ReasonCode = WorkspaceAnalysisCitationValidationReasonOK
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := workspaceAnalysisValidationOutputFixture()
			test.mutate(&candidate)
			if _, err := EncodeWorkspaceAnalysisValidationOutput(candidate); err == nil {
				t.Fatalf("EncodeWorkspaceAnalysisValidationOutput accepted %#v", candidate)
			}
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeWorkspaceAnalysisValidationOutput(raw); err == nil {
				t.Fatalf("DecodeWorkspaceAnalysisValidationOutput accepted %s", raw)
			}
		})
	}
}

func TestWorkspaceAnalysisValidationOutputAcceptsBoundsStableOrderAndDoesNotLeak(t *testing.T) {
	for _, results := range [][]WorkspaceAnalysisCitationValidationResult{
		{{EvidenceRef: "E3", Valid: true, ReasonCode: WorkspaceAnalysisCitationValidationReasonOK}},
		{{EvidenceRef: "E2", Valid: false, ReasonCode: WorkspaceAnalysisCitationValidationReasonUnresolvable}},
		{
			{EvidenceRef: "E3", Valid: false, ReasonCode: WorkspaceAnalysisCitationValidationReasonBindingMismatch},
			{EvidenceRef: "E1", Valid: true, ReasonCode: WorkspaceAnalysisCitationValidationReasonOK},
			{EvidenceRef: "E2", Valid: false, ReasonCode: WorkspaceAnalysisCitationValidationReasonEvidenceIneligible},
		},
	} {
		output := workspaceAnalysisValidationOutputFixture()
		output.Results = results
		encoded, err := EncodeWorkspaceAnalysisValidationOutput(output)
		if err != nil {
			t.Fatalf("encode results: %v", err)
		}
		decoded, err := DecodeWorkspaceAnalysisValidationOutput(encoded)
		if err != nil {
			t.Fatalf("decode results: %v", err)
		}
		if !reflect.DeepEqual(decoded.Results, results) {
			t.Fatalf("result order drifted: %#v, want %#v", decoded.Results, results)
		}
	}

	unsafe := workspaceAnalysisValidationOutputFixture()
	unsafe.CandidateID = "CANDIDATE_ID_CANARY"
	unsafe.CandidateHash = "CANDIDATE_HASH_CANARY"
	unsafe.ValidationToolCallID = "TOOL_CALL_CANARY"
	unsafe.ValidationToolReceiptID = "TOOL_RECEIPT_CANARY"
	unsafe.ValidationToolReceiptHash = "TOOL_RECEIPT_HASH_CANARY"
	unsafe.Results[0] = WorkspaceAnalysisCitationValidationResult{
		EvidenceRef: "EVIDENCE_REF_CANARY", Valid: false, ReasonCode: "REASON_CODE_CANARY",
	}
	for _, formatted := range []string{
		fmt.Sprint(unsafe), fmt.Sprintf("%+v", unsafe), fmt.Sprintf("%#v", unsafe), unsafe.LogValue().String(),
	} {
		for _, canary := range []string{
			"CANDIDATE_ID_CANARY", "CANDIDATE_HASH_CANARY", "TOOL_CALL_CANARY", "TOOL_RECEIPT_CANARY",
			"TOOL_RECEIPT_HASH_CANARY", "EVIDENCE_REF_CANARY", "REASON_CODE_CANARY",
		} {
			if strings.Contains(formatted, canary) {
				t.Fatalf("safe projection leaked %q: %s", canary, formatted)
			}
		}
	}
	for _, formatted := range []string{
		fmt.Sprint(unsafe.Results[0]), fmt.Sprintf("%+v", unsafe.Results[0]), fmt.Sprintf("%#v", unsafe.Results[0]),
	} {
		if formatted != "WorkspaceAnalysisCitationValidationResult{redacted}" {
			t.Fatalf("unsafe result projection = %q", formatted)
		}
	}
}

func workspaceAnalysisValidationOutputFixture() WorkspaceAnalysisValidationOutput {
	return WorkspaceAnalysisValidationOutput{
		SchemaVersion:             WorkspaceAnalysisOutputSchemaVersion,
		CandidateID:               workspaceAnalysisValidationOutputID(1),
		CandidateHash:             strings.Repeat("a", 64),
		ValidationToolCallID:      workspaceAnalysisValidationOutputID(2),
		ValidationToolReceiptID:   workspaceAnalysisValidationOutputID(3),
		ValidationToolReceiptHash: strings.Repeat("b", 64),
		Results: []WorkspaceAnalysisCitationValidationResult{
			{EvidenceRef: "E2", Valid: true, ReasonCode: WorkspaceAnalysisCitationValidationReasonOK},
			{EvidenceRef: "E1", Valid: false, ReasonCode: WorkspaceAnalysisCitationValidationReasonEvidenceIneligible},
		},
	}
}

func workspaceAnalysisValidationOutputID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("95000000-0000-4000-8000-%012d", value))
}
