package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestExecutionServiceWorkspaceAnalysisReplaysSanitizedCanonicalReceipt(t *testing.T) {
	for _, version := range []int64{1, 2} {
		for _, test := range []struct {
			name         string
			snippet      string
			wantSnippet  string
			wantRedacted int
		}{
			{
				name:        "sensitive assignment from compose",
				snippet:     "Approved recovery requires durable replay without duplicate provider work. Evidence token: durable-rag-a7c0c6a6e6b9",
				wantSnippet: "[REDACTED]", wantRedacted: 1,
			},
			{
				name: "ordinary historical receipt", snippet: "Approved recovery requires durable replay.",
				wantSnippet: "Approved recovery requires durable replay.",
			},
			{
				name: "literal historical placeholder", snippet: "[REDACTED]", wantSnippet: "[REDACTED]",
			},
		} {
			t.Run(fmt.Sprintf("v%d/%s", version, test.name), func(t *testing.T) {
				service, repository, executor, command := canonicalSearchRedactionFixture(t, version, test.snippet)
				first, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), command)
				if err != nil {
					t.Errorf("first canonical result: %v", err)
				}
				if len(repository.receiptFinalized) != 1 {
					t.Fatalf("canonical finalizations = %d, want 1", len(repository.receiptFinalized))
				}
				persistedCall := repository.receiptFinalized[0].Call
				var summary struct {
					RedactedFieldCount *int `json:"redacted_field_count"`
				}
				if err := json.Unmarshal(persistedCall.ResponseSummary, &summary); err != nil ||
					summary.RedactedFieldCount == nil || *summary.RedactedFieldCount != test.wantRedacted {
					t.Fatalf("persisted redaction count differs: %s, %v", persistedCall.ResponseSummary, err)
				}
				var output struct {
					Items []struct {
						Snippet string `json:"snippet"`
					} `json:"items"`
				}
				if err := json.Unmarshal(repository.receipt.Output, &output); err != nil ||
					len(output.Items) != 1 || output.Items[0].Snippet != test.wantSnippet {
					t.Fatal("persisted receipt did not retain the sanitized snippet")
				}
				if err == nil && (first.Replayed || !first.UntrustedData || first.Call.Status != domain.CallSucceeded ||
					first.ResultReceiptID != repository.receipt.ID || !bytes.Equal(first.Output, repository.receipt.Output)) {
					t.Fatal("first result does not match the canonical receipt")
				}

				authorized := repository.workspaceAuthorized[0]
				repository.workspaceAuthorize = &WorkspaceAnalysisToolAuthorizationResult{
					Call: persistedCall, OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
					Disposition: WorkspaceAnalysisToolAuthorizationReuseResult,
				}
				second, replayErr := service.ExecuteWorkspaceAnalysisTool(context.Background(), command)
				if replayErr != nil {
					t.Errorf("replay canonical result: %v", replayErr)
				} else if !second.Replayed || !second.UntrustedData || second.ResultReceiptID != repository.receipt.ID ||
					second.OperationID != authorized.OperationID || !bytes.Equal(second.Output, repository.receipt.Output) ||
					!reflect.DeepEqual(second.Call, persistedCall) {
					t.Fatal("replay changed the canonical output, operation, or original Call summary")
				}
				if executor.calls != 1 || len(repository.receiptFinalized) != 1 || repository.receiptLoads != 1 ||
					len(repository.started) != 0 || len(repository.finalized) != 0 || len(repository.workspaceFinalized) != 0 ||
					len(repository.receiptFailures) != 0 || len(repository.unknown) != 0 {
					t.Fatal("canonical replay repeated executor work or changed its durable finalization")
				}
			})
		}
	}
}

func TestExecutionServiceWorkspaceAnalysisRejectsSanitizedReceiptDrift(t *testing.T) {
	for _, version := range []int64{1, 2} {
		for _, test := range []struct {
			name         string
			summaryField string
			summaryValue string
			mutate       func(*testing.T, *domain.ToolCall, *domain.ResultReceipt, ExecutorResult)
		}{
			{name: "missing redaction count", summaryField: "redacted_field_count"},
			{name: "negative redaction count", summaryField: "redacted_field_count", summaryValue: "-1"},
			{name: "null redaction count", summaryField: "redacted_field_count", summaryValue: "null"},
			{name: "string redaction count", summaryField: "redacted_field_count", summaryValue: `"1"`},
			{name: "fractional redaction count", summaryField: "redacted_field_count", summaryValue: "1.5"},
			{name: "unbounded redaction count", summaryField: "redacted_field_count", summaryValue: "1000000"},
			{name: "overflowing redaction count", summaryField: "redacted_field_count", summaryValue: "9223372036854775808"},
			{name: "summary output hash", summaryField: "output_hash", summaryValue: `"` + strings.Repeat("f", 64) + `"`},
			{name: "summary output bytes", summaryField: "output_bytes", summaryValue: "1"},
			{name: "summary schema id", summaryField: "schema_id", summaryValue: `"tool.wrong.output"`},
			{name: "summary schema version", summaryField: "schema_version", summaryValue: "99"},
			{name: "summary unknown field", summaryField: "extra", summaryValue: "true"},
			{name: "output changed", mutate: func(_ *testing.T, _ *domain.ToolCall, receipt *domain.ResultReceipt, _ ExecutorResult) {
				receipt.Output = bytes.Replace(receipt.Output, []byte("[REDACTED]"), []byte("changed"), 1)
			}},
			{name: "unsanitized output with matching stored hashes", mutate: func(t *testing.T, call *domain.ToolCall, receipt *domain.ResultReceipt, original ExecutorResult) {
				receipt.Output = append(json.RawMessage(nil), original.Output...)
				receipt.OutputHash = hashBytes(receipt.Output)
				receipt.OutputBytes = int64(len(receipt.Output))
				call.ResponseHash, call.ResponseBytes = receipt.OutputHash, receipt.OutputBytes
				setCanonicalSummaryField(t, call, "output_hash", `"`+receipt.OutputHash+`"`)
				setCanonicalSummaryField(t, call, "output_bytes", fmt.Sprint(receipt.OutputBytes))
			}},
			{name: "private binding changed with matching stored hash", mutate: func(_ *testing.T, _ *domain.ToolCall, receipt *domain.ResultReceipt, _ ExecutorResult) {
				receipt.PrivateBinding.Document = bytes.Replace(receipt.PrivateBinding.Document, []byte(`"evidence_ref":"E1"`), []byte(`"evidence_ref":"E2"`), 1)
				receipt.PrivateBinding.Hash = hashBytes(receipt.PrivateBinding.Document)
				receipt.PrivateBinding.Bytes = int64(len(receipt.PrivateBinding.Document))
			}},
			{name: "receipt schema", mutate: func(_ *testing.T, _ *domain.ToolCall, receipt *domain.ResultReceipt, _ ExecutorResult) {
				receipt.OutputSchema.Version++
			}},
			{name: "receipt workspace", mutate: func(_ *testing.T, _ *domain.ToolCall, receipt *domain.ResultReceipt, _ ExecutorResult) {
				receipt.WorkspaceID = "00000000-0000-4000-8000-000000000091"
			}},
			{name: "receipt call", mutate: func(_ *testing.T, _ *domain.ToolCall, receipt *domain.ResultReceipt, _ ExecutorResult) {
				receipt.ToolCallID = "00000000-0000-4000-8000-000000000092"
			}},
		} {
			t.Run(fmt.Sprintf("v%d/%s", version, test.name), func(t *testing.T) {
				service, repository, executor, command := canonicalSearchRedactionFixture(t, version, "Evidence token: durable-rag-a7c0c6a6e6b9")
				first, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), command)
				if err != nil {
					t.Fatalf("first canonical result: %v", err)
				}
				call := first.Call
				if test.summaryField != "" {
					setCanonicalSummaryField(t, &call, test.summaryField, test.summaryValue)
				}
				if test.mutate != nil {
					test.mutate(t, &call, &repository.receipt, executor.result)
				}
				authorized := repository.workspaceAuthorized[0]
				repository.workspaceAuthorize = &WorkspaceAnalysisToolAuthorizationResult{
					Call: call, OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
					Disposition: WorkspaceAnalysisToolAuthorizationReuseResult,
				}
				result, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), command)
				if errorCode(err) != errorCodeResultReplayUnavailable || len(result.Output) != 0 || executor.calls != 1 ||
					len(repository.receiptFinalized) != 1 || repository.receiptLoads != 1 {
					t.Fatalf("drift did not fail closed without repeating executor work: %v", err)
				}
			})
		}
	}
}

func TestExecutionServiceRejectsOriginalRedactionCountChangedDuringFinalization(t *testing.T) {
	service, repository, executor, command := canonicalSearchRedactionFixture(t, 2, "Evidence token: durable-rag-a7c0c6a6e6b9")
	repository.receiptCallMutator = func(call *domain.ToolCall) {
		setCanonicalSummaryField(t, call, "redacted_field_count", "0")
	}
	result, err := service.ExecuteWorkspaceAnalysisTool(context.Background(), command)
	if errorCode(err) != errorCodeResultReplayUnavailable || len(result.Output) != 0 ||
		executor.calls != 1 || len(repository.receiptFinalized) != 1 {
		t.Fatalf("changed original count was accepted during finalization: %v", err)
	}
}

func TestExecutionServiceLegacyReceiptLoaderRetainsRedactionValidation(t *testing.T) {
	contract := testContract("SearchKnowledge", 1)
	contract.Definition.SensitiveFields = []string{"/items"}
	original := ExecutorResult{Output: json.RawMessage(`{"items":[{"text":"Evidence token: durable-rag-a7c0c6a6e6b9"}]}`)}
	executor := &executionTestReceiptExecutor{
		executionTestExecutor: executionTestExecutor{result: original}, receipt: original,
	}
	service, repository, _ := newExecutionTestService(t, contract, executor)
	command := executionTestCommand("SearchKnowledge", json.RawMessage(`{}`))
	first, err := service.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	repository.startResult = &StartCallResult{Call: first.Call, Disposition: StartCallReplayed}
	second, err := service.Execute(context.Background(), command)
	if err != nil || !second.Replayed || !reflect.DeepEqual(second.Call, first.Call) ||
		!bytes.Equal(second.Output, json.RawMessage(`{"items":[{"text":"[REDACTED]"}]}`)) ||
		executor.calls != 1 || executor.replayCalls != 1 || len(repository.finalized) != 1 {
		t.Fatalf("legacy raw receipt redaction replay changed: %v", err)
	}
	// This loader has no canonical persistence promise. It must still reproduce
	// the original response summary instead of borrowing the stored count.
	executor.receipt.Output = first.Output
	result, err := service.Execute(context.Background(), command)
	if errorCode(err) != errorCodeResultReplayUnavailable || len(result.Output) != 0 ||
		executor.calls != 1 || executor.replayCalls != 2 || len(repository.finalized) != 1 {
		t.Fatalf("legacy loader's response-summary verification was weakened: %v", err)
	}
}

func setCanonicalSummaryField(t *testing.T, call *domain.ToolCall, key, raw string) {
	t.Helper()
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(call.ResponseSummary, &summary); err != nil {
		t.Fatal(err)
	}
	if raw == "" {
		delete(summary, key)
	} else {
		summary[key] = json.RawMessage(raw)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	call.ResponseSummary = encoded
}

func canonicalSearchRedactionFixture(t *testing.T, version int64, snippet string) (
	*ExecutionService, *executionTestRepository, *executionTestExecutor, ExecuteWorkspaceAnalysisToolCommand,
) {
	t.Helper()
	contract := workspaceAnalysisSearchExecutionContract()
	contract.Definition.SensitiveFields = []string{"/items", "/query"}
	nodeKey := agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence
	ordinal := 1
	if version == 2 {
		contract.Definition.Ref.Version = 3
		contract.Definition.OutputSchema.Version = 3
		contract.Definition.AllowedWorkflows[0].Version = 2
		nodeKey = agentdomain.WorkspaceAnalysisOperationNodeDecideNext
		ordinal = 2
	}
	output, err := json.Marshal(map[string]any{
		"degradations": []string{}, "effective_mode": "hybrid",
		"items": []map[string]any{{"evidence_ref": "E1", "rank": 1, "snippet": snippet}},
	})
	if err != nil {
		t.Fatal(err)
	}
	privateBinding, err := json.Marshal(map[string]any{
		"items": []map[string]any{{
			"evidence_ref": "E1", "citation_id": "cite-" + hashBytes([]byte("citation")),
			"index_version_id":  "00000000-0000-4000-8000-000000000011",
			"chunk_id":          "00000000-0000-4000-8000-000000000012",
			"source_version_id": "00000000-0000-4000-8000-000000000013",
			"source_span_id":    "00000000-0000-4000-8000-000000000014",
			"content_hash":      hashBytes([]byte(snippet)),
		}},
		"selected_refs": []string{"E1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := &executionTestExecutor{result: ExecutorResult{
		Output: output,
		PrivateBinding: &ExecutorPrivateBinding{
			Schema: domain.SchemaRef{ID: "tool.search_knowledge.private_binding", Version: version}, Document: privateBinding,
		},
	}}
	service, repository, policy := newExecutionTestService(t, contract, executor)
	command := ExecuteWorkspaceAnalysisToolCommand{
		OperationKey: agentdomain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: "00000000-0000-4000-8000-000000000006",
			NodeKey:       nodeKey, Kind: agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, Ordinal: ordinal,
		},
		Tool: executionTestCommand("SearchKnowledge", json.RawMessage(`{"query":"approved recovery"}`)),
	}
	command.Tool.Identity.DefinitionVersion = version
	command.Tool.Identity.NodeKey = string(nodeKey)
	command.Tool.Invocation = domain.InvocationSourceTrustedWorkflow
	command.Tool.CallNo = ordinal
	policy.policy.Identity = command.Tool.Identity
	policy.policy.NodeKind = "agent.workspace-analysis." + string(nodeKey)
	return service, repository, executor, command
}
