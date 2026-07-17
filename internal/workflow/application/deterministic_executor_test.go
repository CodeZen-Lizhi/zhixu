package application

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCanonicalJSONHashExecutorReturnsStableVersionedOutput(t *testing.T) {
	executor := NewCanonicalJSONHashExecutor()
	left, err := executor.Execute(context.Background(), ExecutionContext{
		NodeKind:           CanonicalJSONHashNodeKind,
		InputSchemaVersion: CanonicalJSONHashInputSchemaVersion,
		Input:              json.RawMessage(`{"b":2, "a":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	right, err := executor.Execute(context.Background(), ExecutionContext{
		NodeKind:           CanonicalJSONHashNodeKind,
		InputSchemaVersion: CanonicalJSONHashInputSchemaVersion,
		Input:              json.RawMessage("{\n\t\"a\": 1,\n\t\"b\": 2\n}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	const expected = `{"schema_version":1,"sha256":"43258cff783fe7036d8a43033f830adfc60ec037382473548ac742b888292777"}`
	if string(left.Output) != expected || string(right.Output) != expected {
		t.Fatalf("outputs = %s / %s", left.Output, right.Output)
	}
}

func TestCanonicalJSONHashExecutorRejectsUnknownContractAndInvalidJSON(t *testing.T) {
	executor := NewCanonicalJSONHashExecutor()
	tests := []struct {
		name    string
		context ExecutionContext
		code    string
	}{
		{name: "kind", context: ExecutionContext{NodeKind: "deterministic.other", InputSchemaVersion: 1, Input: json.RawMessage(`{}`)}, code: "WORKFLOW_DETERMINISTIC_CONTRACT_UNSUPPORTED"},
		{name: "schema", context: ExecutionContext{NodeKind: CanonicalJSONHashNodeKind, InputSchemaVersion: 2, Input: json.RawMessage(`{}`)}, code: "WORKFLOW_DETERMINISTIC_CONTRACT_UNSUPPORTED"},
		{name: "json", context: ExecutionContext{NodeKind: CanonicalJSONHashNodeKind, InputSchemaVersion: 1, Input: json.RawMessage(`{"broken":`)}, code: "WORKFLOW_DETERMINISTIC_INPUT_INVALID"},
		{name: "trailing json", context: ExecutionContext{NodeKind: CanonicalJSONHashNodeKind, InputSchemaVersion: 1, Input: json.RawMessage(`{} {}`)}, code: "WORKFLOW_DETERMINISTIC_INPUT_INVALID"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := executor.Execute(context.Background(), tc.context)
			assertWorkflowErrorCode(t, err, tc.code)
		})
	}
}
