package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
)

func TestWorkspaceAnalysisV2DecisionReceiptAcceptsSameInstantAtToolAndFinish(t *testing.T) {
	for _, location := range []*time.Location{time.FixedZone("database UTC", 0), time.FixedZone("database offset", 8*60*60)} {
		for _, boundary := range []string{"tool", "finish"} {
			t.Run(location.String()+"/"+boundary, func(t *testing.T) {
				f := newWorkspaceAnalysisV2ExecutorFixture(t, "E17")
				execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeDecideNext)
				executor := f.executor(t, execution.NodeKey).(*workspaceAnalysisV2NodeExecutor)
				if boundary == "tool" {
					entry := f.journal.snapshot.Entries[0]
					accepted := *entry.Decision
					accepted.CreatedAt = accepted.CreatedAt.In(location)
					reachedTool := errors.New("tool execution reached")
					f.tools.execute = func(toolsapplication.ExecuteWorkspaceAnalysisToolCommand) (toolsapplication.ToolExecutionResult, error) {
						return toolsapplication.ToolExecutionResult{}, reachedTool
					}
					invoker := workspaceAnalysisV2LoopToolInvoker{executor: executor, state: f.state(execution)}
					_, err := invoker.InvokeWorkspaceAnalysisDecisionTool(context.Background(), agentapplication.WorkspaceAnalysisDecisionMutationResult{
						Run: *entry.ModelRun, Call: *entry.ModelCall, Operation: entry.Operation, Decision: &accepted,
					})
					if !errors.Is(err, reachedTool) || len(f.tools.commands) != 1 {
						t.Fatalf("same-instant receipt did not reach authorized tool execution: calls=%d err=%v", len(f.tools.commands), err)
					}
					return
				}
				accepted := *f.loop.result.Finish.Decision
				accepted.CreatedAt = accepted.CreatedAt.In(location)
				f.loop.result.Finish.Decision = &accepted
				result, err := executor.decideNext(context.Background(), f.state(execution))
				if err != nil {
					t.Fatalf("same-instant finish receipt was rejected: %v", err)
				}
				output, err := decodeWorkspaceAnalysisLoopOutputV2(result.Output)
				if err != nil || output.FinishDecisionID != accepted.ID || output.DecisionCount != 3 || output.ToolCount != 2 {
					t.Fatalf("finish output lost its durable binding: %v", err)
				}
			})
		}
	}
}

func TestWorkspaceAnalysisV2DecisionReceiptRejectsDriftAtToolAndFinish(t *testing.T) {
	cases := map[string]func(*agentdomain.WorkspaceAnalysisDecisionReceipt){
		"receipt identity": func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) { receipt.ID = v2ExecutorID(9901) },
		"workspace":        func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) { receipt.WorkspaceID = v2ExecutorID(9901) },
		"analysis run": func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) {
			receipt.AnalysisRunID = v2ExecutorID(9901)
		},
		"operation": func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) { receipt.OperationID = v2ExecutorID(9901) },
		"attempt": func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) {
			receipt.NodeAttemptID = v2ExecutorID(9901)
		},
		"model run":  func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) { receipt.ModelRunID = v2ExecutorID(9901) },
		"model call": func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) { receipt.ModelCallID = v2ExecutorID(9901) },
		"ordinal":    func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) { receipt.Ordinal++ },
		"document hash": func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) {
			receipt.DocumentHash = strings.Repeat("f", 64)
		},
		"document size": func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) { receipt.DocumentBytes++ },
		"instant": func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) {
			receipt.CreatedAt = receipt.CreatedAt.Add(time.Microsecond)
		},
		"valid different decision": func(receipt *agentdomain.WorkspaceAnalysisDecisionReceipt) {
			query := "different query"
			receipt.Decision = agentdomain.WorkspaceAnalysisDecision{Action: agentdomain.WorkspaceAnalysisDecisionKnowledgeSearch, Query: &query}
			raw, err := receipt.Decision.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			receipt.DocumentHash, receipt.DocumentBytes = workspaceAnalysisV2Hash(raw), int64(len(raw))
		},
	}
	for name, mutate := range cases {
		for _, boundary := range []string{"tool", "finish"} {
			t.Run(name+"/"+boundary, func(t *testing.T) {
				f := newWorkspaceAnalysisV2ExecutorFixture(t, "E17")
				execution := f.execution(conversationworkflow.WorkspaceAnalysisNodeDecideNext)
				executor := f.executor(t, execution.NodeKey).(*workspaceAnalysisV2NodeExecutor)
				if boundary == "tool" {
					entry := f.journal.snapshot.Entries[0]
					accepted := *entry.Decision
					mutate(&accepted)
					invoker := workspaceAnalysisV2LoopToolInvoker{executor: executor, state: f.state(execution)}
					_, err := invoker.InvokeWorkspaceAnalysisDecisionTool(context.Background(), agentapplication.WorkspaceAnalysisDecisionMutationResult{
						Run: *entry.ModelRun, Call: *entry.ModelCall, Operation: entry.Operation, Decision: &accepted,
					})
					if err == nil || len(f.tools.commands) != 0 || f.outputs.outputCalls != 0 {
						t.Fatal("drifted receipt reached tool execution")
					}
					return
				}
				accepted := *f.loop.result.Finish.Decision
				mutate(&accepted)
				f.loop.result.Finish.Decision = &accepted
				result, err := executor.decideNext(context.Background(), f.state(execution))
				if err == nil || len(result.Output) != 0 {
					t.Fatal("drifted receipt advanced past the decision loop")
				}
			})
		}
	}
}
