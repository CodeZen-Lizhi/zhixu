package changecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestCalculateDiffExecutorProducesDeterministicCatalogOutput(t *testing.T) {
	executor := NewCalculateDiffExecutor()
	arguments := []byte(`{"before":"old\nline","after":"new\nline"}`)
	original := append([]byte(nil), arguments...)
	first, err := executor.Execute(context.Background(), diffRequest(arguments))
	if err != nil {
		t.Fatal(err)
	}
	second, err := executor.Execute(context.Background(), diffRequest(arguments))
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments) != string(original) || string(first.Output) != string(second.Output) || first.ResultRef != second.ResultRef {
		t.Fatal("diff output is not deterministic or mutated input")
	}
	decodeDiffCatalogOutput(t, first.Output)
	var output calculateDiffOutput
	if err := json.Unmarshal(first.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Changed == nil || !*output.Changed || output.Patch == nil ||
		!strings.Contains(*output.Patch, "--- before\n+++ after\n") ||
		!strings.Contains(*output.Patch, "-old\n-line\n") || !strings.Contains(*output.Patch, "+new\n+line\n") ||
		output.BeforeHash != hashString("old\nline") || output.AfterHash != hashString("new\nline") {
		t.Fatalf("output=%s", first.Output)
	}
	request := diffRequest(arguments)
	tool := request.Tool
	receipt, err := executor.LoadResultReceipt(context.Background(), request, toolsdomain.ToolCall{Status: toolsdomain.CallSucceeded, Tool: &tool, RequestedToolName: calculateDiffName, ResultRef: first.ResultRef})
	if err != nil || string(receipt.Output) != string(first.Output) || receipt.ResultRef != first.ResultRef {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

func TestCalculateDiffExecutorReturnsEmptyPatchForEqualInput(t *testing.T) {
	result, err := NewCalculateDiffExecutor().Execute(context.Background(), diffRequest([]byte(`{"before":"same","after":"same"}`)))
	if err != nil {
		t.Fatal(err)
	}
	decodeDiffCatalogOutput(t, result.Output)
	var output calculateDiffOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Changed == nil || *output.Changed || output.Patch == nil || *output.Patch != "" || output.BeforeHash != output.AfterHash {
		t.Fatalf("output=%s", result.Output)
	}
}

func TestCalculateDiffExecutorRejectsInvalidInputAndWrongTool(t *testing.T) {
	executor := NewCalculateDiffExecutor()
	invalid := [][]byte{
		[]byte(`{"before":null,"after":"x"}`),
		[]byte(`{"before":"x","after":"y","path":"forged"}`),
		[]byte(`{"before":"x\u0000","after":"y"}`),
	}
	for _, arguments := range invalid {
		_, err := executor.Execute(context.Background(), diffRequest(arguments))
		requireDiffCode(t, err, errorCodeDiffInput)
	}
	request := diffRequest([]byte(`{"before":"x","after":"y"}`))
	request.Tool.Name = "ReadGitStatus"
	_, err := executor.Execute(context.Background(), request)
	requireDiffCode(t, err, errorCodeDiffInput)
}

func TestCalculateDiffExecutorHonorsCancellationAndOutputLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewCalculateDiffExecutor().Execute(ctx, diffRequest([]byte(`{"before":"x","after":"y"}`)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}

	input := calculateDiffInput{
		Before: stringPointer(strings.Repeat("aaa\n", 100000)),
		After:  stringPointer(strings.Repeat("bbb\n", 100000)),
	}
	arguments, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments) >= maxDiffInputBytes {
		t.Fatalf("test input is unexpectedly oversized: %d", len(arguments))
	}
	_, err = NewCalculateDiffExecutor().Execute(context.Background(), diffRequest(arguments))
	requireDiffCode(t, err, errorCodeDiffOutputSize)
}

func TestCalculateDiffExecutorNilReceiverFailsClosed(t *testing.T) {
	var executor *CalculateDiffExecutor
	_, err := executor.Execute(context.Background(), diffRequest([]byte(`{"before":"x","after":"y"}`)))
	requireDiffCode(t, err, "TOOL_CALCULATE_DIFF_UNAVAILABLE")
}

func diffRequest(arguments []byte) toolsapplication.ExecutorRequest {
	return toolsapplication.ExecutorRequest{
		Identity: toolsdomain.TrustedExecutionIdentity{
			WorkspaceID: "74000000-0000-4000-8000-000000000001", DefinitionID: "74000000-0000-4000-8000-000000000002",
			DefinitionVersion: 1, DefinitionHash: strings.Repeat("a", 64), WorkflowRunID: "74000000-0000-4000-8000-000000000003",
			NodeKey: "diff-node", NodeRunID: "74000000-0000-4000-8000-000000000004", NodeAttemptID: "74000000-0000-4000-8000-000000000005",
			LeaseOwner: "worker-1", LeaseFence: 1,
		},
		Tool: toolsdomain.ToolRef{Name: calculateDiffName, Version: 1}, Arguments: arguments, Reason: "compare approved text",
	}
}

func decodeDiffCatalogOutput(t *testing.T, raw []byte) {
	t.Helper()
	contracts, err := catalog.Contracts()
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range contracts {
		if contract.Definition.Ref.Name == calculateDiffName {
			if _, err := contract.DecodeOutput(raw); err != nil {
				t.Fatalf("catalog rejected output %s: %v", raw, err)
			}
			return
		}
	}
	t.Fatal("CalculateDiff contract not found")
}

func requireDiffCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v code=%q", err, code)
	}
}

func hashString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
