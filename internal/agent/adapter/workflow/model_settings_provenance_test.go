package workflow

import (
	"context"
	"testing"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkflowExecutorsCopyAttemptModelSettingsRevision(t *testing.T) {
	t.Run("relation assessment", func(t *testing.T) {
		model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
			Model: testModelRef(), Content: relationDocument(t, testModelRunID, "NEW"),
			Usage: agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		}})
		repository := &workflowRepository{}
		executor := newWorkflowExecutor(t, model, repository)
		revision := int64(0)
		execution := testExecution(t, validInput())
		execution.ModelSettingsRevision = &revision
		if _, err := executor.Execute(context.Background(), execution); err != nil {
			t.Fatal(err)
		}
		revision = 9
		assertWorkflowRunRevision(t, repository.run.ModelSettingsRevision, 0)
	})

	t.Run("rag", func(t *testing.T) {
		executor := newReplayRAGWorkflowExecutor(t, &ragContextLoaderFake{}, &ragFinalizerFake{})
		revision := int64(6)
		run, err := executor.prepareRAGModelRun(workflowapplication.ExecutionContext{
			WorkspaceID: ragInputID(1), RunID: ragInputID(2), NodeRunID: ragInputID(3),
			NodeAttemptID: ragInputID(4), ModelSettingsRevision: &revision,
		}, agentdomain.RAGMemoryContextRef{})
		if err != nil {
			t.Fatal(err)
		}
		revision = 8
		assertWorkflowRunRevision(t, run.ModelSettingsRevision, 6)
	})
}

func assertWorkflowRunRevision(t *testing.T, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("model run revision=%v want=%d", got, want)
	}
}
