package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisInspectOutputRoundTrip(t *testing.T) {
	output := workspaceAnalysisInspectOutputFixture()
	encoded, err := EncodeWorkspaceAnalysisInspectOutput(output)
	if err != nil {
		t.Fatalf("EncodeWorkspaceAnalysisInspectOutput: %v", err)
	}
	decoded, err := DecodeWorkspaceAnalysisInspectOutput(encoded)
	if err != nil {
		t.Fatalf("DecodeWorkspaceAnalysisInspectOutput: %v", err)
	}
	if decoded != output {
		t.Fatalf("decoded = %+v, want %+v", decoded, output)
	}
	if strings.Contains(string(encoded), "path") || strings.Contains(string(encoded), "diff") {
		t.Fatalf("inspect output leaked forbidden fields: %s", encoded)
	}
}

func TestWorkspaceAnalysisInspectOutputRejectsStrictAndBindingDrift(t *testing.T) {
	valid := workspaceAnalysisInspectOutputFixture()
	tests := []struct {
		name   string
		raw    json.RawMessage
		mutate func(*WorkspaceAnalysisInspectOutput)
	}{
		{name: "unknown", raw: json.RawMessage(`{"schema_version":1,"tool_call_id":"91000000-0000-4000-8000-000000000001","tool_receipt_id":"91000000-0000-4000-8000-000000000002","tool_receipt_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","git_status":{"branch":"main","clean":true,"conflict_count":0,"head":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0},"extra":true}`)},
		{name: "duplicate", raw: json.RawMessage(`{"schema_version":1,"schema_version":1}`)},
		{name: "null", raw: json.RawMessage(`{"schema_version":null}`)},
		{name: "trailing", raw: json.RawMessage(`{} {}`)},
		{name: "reused identity", mutate: func(value *WorkspaceAnalysisInspectOutput) { value.ToolReceiptID = value.ToolCallID }},
		{name: "hash", mutate: func(value *WorkspaceAnalysisInspectOutput) { value.ToolReceiptHash = strings.Repeat("A", 64) }},
		{name: "clean drift", mutate: func(value *WorkspaceAnalysisInspectOutput) { value.GitStatus.Clean = false }},
		{name: "count", mutate: func(value *WorkspaceAnalysisInspectOutput) {
			value.GitStatus.UntrackedCount = workspaceAnalysisMaxGitChanges + 1
		}},
		{name: "head format", mutate: func(value *WorkspaceAnalysisInspectOutput) { value.GitStatus.ObjectFormat = "sha256" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := test.raw
			if test.mutate != nil {
				candidate := valid
				test.mutate(&candidate)
				encoded, _ := json.Marshal(candidate)
				raw = encoded
			}
			if _, err := DecodeWorkspaceAnalysisInspectOutput(raw); err == nil {
				t.Fatalf("DecodeWorkspaceAnalysisInspectOutput accepted %s", raw)
			}
		})
	}
}

func TestDecodeWorkspaceAnalysisGitStatusSummaryRejectsPrivateOrMalformedData(t *testing.T) {
	valid := json.RawMessage(`{"branch":"main","clean":false,"conflict_count":0,"head":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","object_format":"sha1","staged_count":1,"unstaged_count":0,"untracked_count":0}`)
	summary, err := DecodeWorkspaceAnalysisGitStatusSummary(valid)
	if err != nil || summary.Branch != "main" || summary.StagedCount != 1 {
		t.Fatalf("summary=%+v error=%v", summary, err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"branch":"main","clean":true,"conflict_count":0,"head":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0,"path":"secret"}`),
		json.RawMessage(`{"branch":"main","clean":true,"conflict_count":0,"head":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","object_format":"sha1","staged_count":0,"unstaged_count":0}`),
	} {
		if _, err := DecodeWorkspaceAnalysisGitStatusSummary(raw); err == nil {
			t.Fatalf("DecodeWorkspaceAnalysisGitStatusSummary accepted %s", raw)
		}
	}
}

func workspaceAnalysisInspectOutputFixture() WorkspaceAnalysisInspectOutput {
	return WorkspaceAnalysisInspectOutput{
		SchemaVersion:   WorkspaceAnalysisOutputSchemaVersion,
		ToolCallID:      foundation.ID("91000000-0000-4000-8000-000000000001"),
		ToolReceiptID:   foundation.ID("91000000-0000-4000-8000-000000000002"),
		ToolReceiptHash: strings.Repeat("a", 64),
		GitStatus: WorkspaceAnalysisGitStatusSummary{
			Branch: "main", Clean: true, Head: strings.Repeat("b", 40), ObjectFormat: "sha1",
		},
	}
}
