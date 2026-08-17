package workflow

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisReadEvidenceOutputCanonicalRoundTrip(t *testing.T) {
	output := workspaceAnalysisReadEvidenceOutputFixture(3)
	encoded, err := EncodeWorkspaceAnalysisReadEvidenceOutput(output)
	if err != nil {
		t.Fatalf("EncodeWorkspaceAnalysisReadEvidenceOutput: %v", err)
	}
	want := `{"schema_version":1,"reads":[{"evidence_ref":"E1","tool_call_id":"94000000-0000-4000-8000-000000000001","tool_receipt_id":"94000000-0000-4000-8000-000000000002","tool_receipt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"evidence_ref":"E2","tool_call_id":"94000000-0000-4000-8000-000000000003","tool_receipt_id":"94000000-0000-4000-8000-000000000004","tool_receipt_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},{"evidence_ref":"E3","tool_call_id":"94000000-0000-4000-8000-000000000005","tool_receipt_id":"94000000-0000-4000-8000-000000000006","tool_receipt_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}]}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s\nwant    = %s", encoded, want)
	}
	decoded, err := DecodeWorkspaceAnalysisReadEvidenceOutput(encoded)
	if err != nil {
		t.Fatalf("DecodeWorkspaceAnalysisReadEvidenceOutput: %v", err)
	}
	if !reflect.DeepEqual(decoded, output) {
		t.Fatalf("decoded = %#v, want %#v", decoded, output)
	}

	reordered := json.RawMessage(`{"reads":[{"tool_receipt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tool_receipt_id":"94000000-0000-4000-8000-000000000002","tool_call_id":"94000000-0000-4000-8000-000000000001","evidence_ref":"E1"}],"schema_version":1}`)
	replayed, err := DecodeWorkspaceAnalysisReadEvidenceOutput(reordered)
	if err != nil {
		t.Fatalf("decode reordered output: %v", err)
	}
	reencoded, err := EncodeWorkspaceAnalysisReadEvidenceOutput(replayed)
	if err != nil {
		t.Fatalf("encode reordered output: %v", err)
	}
	wantOne := `{"schema_version":1,"reads":[{"evidence_ref":"E1","tool_call_id":"94000000-0000-4000-8000-000000000001","tool_receipt_id":"94000000-0000-4000-8000-000000000002","tool_receipt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`
	if string(reencoded) != wantOne {
		t.Fatalf("replayed output is not canonical: %s", reencoded)
	}
}

func TestWorkspaceAnalysisReadEvidenceOutputRejectsStrictJSONAndUnsafeFields(t *testing.T) {
	canonical, err := EncodeWorkspaceAnalysisReadEvidenceOutput(workspaceAnalysisReadEvidenceOutputFixture(1))
	if err != nil {
		t.Fatal(err)
	}
	valid := string(canonical)
	tests := map[string]json.RawMessage{
		"unknown outer":    json.RawMessage(strings.TrimSuffix(valid, "}") + `,"prompt":"secret"}`),
		"unknown nested":   json.RawMessage(strings.Replace(valid, `"evidence_ref":"E1"`, `"evidence_ref":"E1","excerpt":"SOURCE_BODY_CANARY"`, 1)),
		"duplicate outer":  json.RawMessage(strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)),
		"duplicate nested": json.RawMessage(strings.Replace(valid, `"evidence_ref":"E1"`, `"evidence_ref":"E1","evidence_ref":"E1"`, 1)),
		"null document":    json.RawMessage(`null`),
		"missing reads":    json.RawMessage(`{"schema_version":1}`),
		"null reads":       json.RawMessage(`{"schema_version":1,"reads":null}`),
		"null read":        json.RawMessage(`{"schema_version":1,"reads":[null]}`),
		"null field":       json.RawMessage(strings.Replace(valid, `"tool_call_id":"94000000-0000-4000-8000-000000000001"`, `"tool_call_id":null`, 1)),
		"trailing":         json.RawMessage(valid + ` {}`),
		"top-level array":  json.RawMessage(`[]`),
		"oversize":         json.RawMessage(valid + strings.Repeat(" ", workspaceAnalysisMaxReadEvidenceOutputBytes)),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWorkspaceAnalysisReadEvidenceOutput(raw); err == nil {
				t.Fatal("DecodeWorkspaceAnalysisReadEvidenceOutput accepted invalid input")
			}
		})
	}

	for _, field := range []string{
		"snippet", "content", "content_hash", "path", "workspace_id", "workflow_run_id", "analysis_run_id",
		"operation_id", "node_attempt_id", "citation_id", "index_version_id", "chunk_id", "source_version_id",
		"source_span_id", "private_binding",
	} {
		t.Run("forbidden "+field, func(t *testing.T) {
			raw := json.RawMessage(strings.Replace(valid, `"evidence_ref":"E1"`,
				`"evidence_ref":"E1","`+field+`":"IDENTITY_BODY_CANARY"`, 1))
			if _, err := DecodeWorkspaceAnalysisReadEvidenceOutput(raw); err == nil {
				t.Fatalf("accepted forbidden field %q", field)
			}
		})
	}
}

func TestWorkspaceAnalysisReadEvidenceOutputRejectsBindingAndBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisReadEvidenceOutput)
	}{
		{name: "schema", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) { value.SchemaVersion++ }},
		{name: "nil reads", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) { value.Reads = nil }},
		{name: "empty reads", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) {
			value.Reads = []WorkspaceAnalysisReadEvidenceReceipt{}
		}},
		{name: "too many reads", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) {
			value.Reads = append(value.Reads, WorkspaceAnalysisReadEvidenceReceipt{
				EvidenceRef: "E4", ToolCallID: workspaceAnalysisReadEvidenceOutputID(7),
				ToolReceiptID: workspaceAnalysisReadEvidenceOutputID(8), ToolReceiptHash: strings.Repeat("d", 64),
			})
		}},
		{name: "reference gap", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) { value.Reads[1].EvidenceRef = "E3" }},
		{name: "reference reorder", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) {
			value.Reads[0].EvidenceRef, value.Reads[1].EvidenceRef = value.Reads[1].EvidenceRef, value.Reads[0].EvidenceRef
		}},
		{name: "invalid id", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) { value.Reads[0].ToolCallID = "invalid" }},
		{name: "non canonical id", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) {
			value.Reads[0].ToolCallID = "94000000-0000-4000-8000-00000000000A"
		}},
		{name: "call receipt collision", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) {
			value.Reads[0].ToolReceiptID = value.Reads[0].ToolCallID
		}},
		{name: "global identity reuse", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) {
			value.Reads[2].ToolCallID = value.Reads[0].ToolReceiptID
		}},
		{name: "uppercase hash", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) {
			value.Reads[0].ToolReceiptHash = strings.Repeat("A", 64)
		}},
		{name: "short hash", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) {
			value.Reads[0].ToolReceiptHash = strings.Repeat("a", 63)
		}},
		{name: "non hex hash", mutate: func(value *WorkspaceAnalysisReadEvidenceOutput) {
			value.Reads[0].ToolReceiptHash = strings.Repeat("g", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := workspaceAnalysisReadEvidenceOutputFixture(3)
			test.mutate(&candidate)
			if _, err := EncodeWorkspaceAnalysisReadEvidenceOutput(candidate); err == nil {
				t.Fatalf("EncodeWorkspaceAnalysisReadEvidenceOutput accepted %#v", candidate)
			}
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeWorkspaceAnalysisReadEvidenceOutput(raw); err == nil {
				t.Fatalf("DecodeWorkspaceAnalysisReadEvidenceOutput accepted %s", raw)
			}
		})
	}
}

func TestWorkspaceAnalysisReadEvidenceOutputAcceptsReadBoundsAndDoesNotLeak(t *testing.T) {
	for _, count := range []int{1, workspaceAnalysisMaxEvidenceReads} {
		output := workspaceAnalysisReadEvidenceOutputFixture(count)
		encoded, err := EncodeWorkspaceAnalysisReadEvidenceOutput(output)
		if err != nil {
			t.Fatalf("encode %d reads: %v", count, err)
		}
		decoded, err := DecodeWorkspaceAnalysisReadEvidenceOutput(encoded)
		if err != nil {
			t.Fatalf("decode %d reads: %v", count, err)
		}
		if !reflect.DeepEqual(decoded, output) {
			t.Fatalf("decoded = %#v, want %#v", decoded, output)
		}
		for _, forbidden := range []string{
			"snippet", "excerpt", "content_hash", "citation_id", "index_version_id", "chunk_id",
			"source_version_id", "source_span_id", "workspace_id", "analysis_run_id", "operation_id",
			"node_attempt_id", "private_binding", "prompt", "path",
		} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("read output contains forbidden field %q: %s", forbidden, encoded)
			}
		}
	}

	unsafe := WorkspaceAnalysisReadEvidenceOutput{
		SchemaVersion: 1,
		Reads: []WorkspaceAnalysisReadEvidenceReceipt{{
			EvidenceRef: "SOURCE_BODY_CANARY", ToolCallID: "TOOL_CALL_CANARY",
			ToolReceiptID: "TOOL_RECEIPT_CANARY", ToolReceiptHash: "PRIVATE_HASH_CANARY",
		}},
	}
	for _, formatted := range []string{fmt.Sprint(unsafe), fmt.Sprintf("%+v", unsafe), fmt.Sprintf("%#v", unsafe)} {
		if formatted != "WorkspaceAnalysisReadEvidenceOutput{reads:1}" {
			t.Fatalf("unsafe String projection = %q", formatted)
		}
	}
	for _, formatted := range []string{
		fmt.Sprint(unsafe.Reads[0]), fmt.Sprintf("%+v", unsafe.Reads[0]), fmt.Sprintf("%#v", unsafe.Reads[0]),
	} {
		if formatted != "WorkspaceAnalysisReadEvidenceReceipt{redacted}" {
			t.Fatalf("unsafe nested String projection = %q", formatted)
		}
	}
}

func workspaceAnalysisReadEvidenceOutputFixture(count int) WorkspaceAnalysisReadEvidenceOutput {
	reads := make([]WorkspaceAnalysisReadEvidenceReceipt, count)
	for index := range reads {
		reads[index] = WorkspaceAnalysisReadEvidenceReceipt{
			EvidenceRef:     workspaceAnalysisEvidenceRef(index + 1),
			ToolCallID:      workspaceAnalysisReadEvidenceOutputID(index*2 + 1),
			ToolReceiptID:   workspaceAnalysisReadEvidenceOutputID(index*2 + 2),
			ToolReceiptHash: strings.Repeat(string(rune('a'+index)), 64),
		}
	}
	return WorkspaceAnalysisReadEvidenceOutput{SchemaVersion: WorkspaceAnalysisOutputSchemaVersion, Reads: reads}
}

func workspaceAnalysisReadEvidenceOutputID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("94000000-0000-4000-8000-%012d", value))
}
