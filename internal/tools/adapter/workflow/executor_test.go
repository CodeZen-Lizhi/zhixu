package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestExecutorUsesPersistedIdentityAndReturnsOnlyUntrustedSummary(t *testing.T) {
	service := &workflowExecutionFake{}
	rawOutput := json.RawMessage(`{"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","clean":true,"detail":"secret raw output"}`)
	call := successfulCall(rawOutput)
	service.result = toolsapplication.ToolExecutionResult{
		Call: call, Output: rawOutput, UntrustedData: true,
	}
	executor, err := NewExecutor(service)
	if err != nil {
		t.Fatal(err)
	}
	execution := workflowExecutionContext()
	result, err := executor.Execute(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if service.calls != 1 || service.command.Identity.WorkspaceID != execution.WorkspaceID ||
		service.command.Identity.DefinitionID != execution.DefinitionID || service.command.Identity.WorkflowRunID != execution.RunID ||
		service.command.Identity.NodeAttemptID != execution.NodeAttemptID || service.command.Identity.LeaseFence != int64(execution.AttemptNo) ||
		service.command.Invocation != toolsdomain.InvocationSourceModelRequest || service.command.CallNo != 1 ||
		service.command.Request.ToolName != "ReadGitStatus" || service.command.Request.Reason != persistedReason {
		t.Fatalf("command=%+v", service.command)
	}
	decoded, err := DecodeToolResultV1(result.Output)
	if err != nil || decoded.ToolCallID != call.ID || decoded.ResultRef != call.ResultRef || !decoded.UntrustedData {
		t.Fatalf("decoded=%+v output=%s err=%v", decoded, result.Output, err)
	}
	if strings.Contains(string(result.Output), "secret raw output") || strings.Contains(string(result.Output), persistedReason) {
		t.Fatalf("workflow output persisted raw tool data: %s", result.Output)
	}
}

func TestExecutorRejectsUntrustedContextAndStrictInputBeforeService(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*workflowapplication.ExecutionContext)
	}{
		{name: "workspace field", change: func(value *workflowapplication.ExecutionContext) {
			value.Input = json.RawMessage(`{"schema_version":1,"tool_name":"ReadGitStatus","arguments":{},"workspace_id":"a0000000-0000-4000-8000-000000000099"}`)
		}},
		{name: "capability field", change: func(value *workflowapplication.ExecutionContext) {
			value.Input = json.RawMessage(`{"schema_version":1,"tool_name":"ReadGitStatus","arguments":{},"capability":"READ_LOCAL"}`)
		}},
		{name: "model reason field", change: func(value *workflowapplication.ExecutionContext) {
			value.Input = json.RawMessage(`{"schema_version":1,"tool_name":"ReadGitStatus","arguments":{},"reason":"raw model text"}`)
		}},
		{name: "wrong node kind", change: func(value *workflowapplication.ExecutionContext) { value.NodeKind = "attacker.tool" }},
		{name: "missing attempt", change: func(value *workflowapplication.ExecutionContext) { value.AttemptNo = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &workflowExecutionFake{}
			executor, err := NewExecutor(service)
			if err != nil {
				t.Fatal(err)
			}
			execution := workflowExecutionContext()
			test.change(&execution)
			if _, err := executor.Execute(context.Background(), execution); err == nil {
				t.Fatal("invalid workflow request was accepted")
			}
			if service.calls != 0 {
				t.Fatalf("execution service calls=%d", service.calls)
			}
		})
	}
}

func TestExecutorRejectsIncompleteServiceSuccess(t *testing.T) {
	service := &workflowExecutionFake{result: toolsapplication.ToolExecutionResult{Call: successfulCall(json.RawMessage(`{"ok":true}`)), UntrustedData: true}}
	executor, err := NewExecutor(service)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), workflowExecutionContext()); err == nil {
		t.Fatal("empty raw result was accepted as a service success")
	}
}

type workflowExecutionFake struct {
	result  toolsapplication.ToolExecutionResult
	err     error
	calls   int
	command toolsapplication.ExecuteToolCommand
}

func (fake *workflowExecutionFake) Execute(_ context.Context, command toolsapplication.ExecuteToolCommand) (toolsapplication.ToolExecutionResult, error) {
	fake.calls++
	fake.command = command
	return fake.result, fake.err
}

func workflowExecutionContext() workflowapplication.ExecutionContext {
	return workflowapplication.ExecutionContext{
		WorkspaceID: testID(1), DefinitionID: testID(2), DefinitionVersion: 1, DefinitionHash: hash("d"),
		RunID: testID(3), NodeKey: NodeKey, NodeRunID: testID(4), NodeAttemptID: testID(5), NodeKind: NodeKind,
		NodeVersion: 2, InputSchemaVersion: InputSchemaVersion, AttemptNo: 1, DispatchNo: 1, RetryNo: 0,
		LeaseOwner: "worker:job-1-attempt-1",
		Input:      json.RawMessage(`{"schema_version":1,"tool_name":"ReadGitStatus","arguments":{}}`),
	}
}

func successfulCall(output json.RawMessage) toolsdomain.ToolCall {
	completed := time.Now().UTC()
	tool := toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 1}
	inputSchema := toolsdomain.SchemaRef{ID: "tool.read_git_status.input", Version: 1}
	outputSchema := toolsdomain.SchemaRef{ID: "tool.read_git_status.output", Version: 1}
	digest := sha256.Sum256(output)
	responseHash := hex.EncodeToString(digest[:])
	return toolsdomain.ToolCall{
		ID: testID(6), WorkspaceID: testID(1), WorkflowRunID: testID(3), NodeRunID: testID(4), NodeAttemptID: testID(5),
		CallNo: 1, RequestedToolName: tool.Name, Tool: &tool, DefinitionHash: hash("d"), InputSchema: &inputSchema, OutputSchema: &outputSchema,
		SideEffectLevel: toolsdomain.SideEffectNone, InvocationPolicy: toolsdomain.InvocationModelRequestable,
		RequestHash: hash("e"), RequestBytes: 10, RequestSummary: json.RawMessage(`{"argument_bytes":10}`),
		Status: toolsdomain.CallSucceeded, Version: 2, ResultRef: "git-head:" + strings.Repeat("a", 40),
		ResponseHash: responseHash, ResponseBytes: int64(len(output)),
		ResponseSummary: json.RawMessage(`{"output_bytes":` + jsonNumber(len(output)) + `,"output_hash":"` + responseHash + `","redacted_field_count":0,"schema_id":"tool.read_git_status.output","schema_version":1}`),
		StartedAt:       completed.Add(-time.Millisecond), CompletedAt: &completed, DurationMillis: 1,
	}
}

func jsonNumber(value int) string {
	return fmt.Sprintf("%d", value)
}
