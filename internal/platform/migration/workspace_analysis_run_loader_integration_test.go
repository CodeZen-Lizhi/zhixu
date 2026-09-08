//go:build integration

package migration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisRunLoaderRequiresExactWorkspaceExecutionBinding(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	prepareWorkspaceAnalysisToolAuthorizationRuntime(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at
	) VALUES (
		'83000000-0000-4000-8000-000000000100','wa-loader-other',
		'/tmp/wa-loader-other','/tmp/wa-loader-other',now(),'inactive',now(),now()
	)`); err != nil {
		t.Fatalf("insert second workspace: %v", err)
	}
	runtime := openMigrationRuntimePool(t, ctx, pool)
	defer runtime.Close()
	repository, err := agentpostgres.NewGORMRepository(runtime)
	if err != nil {
		t.Fatal(err)
	}

	query := agentapplication.WorkspaceAnalysisRunExecutionQuery{
		WorkspaceID:    "83000000-0000-4000-8000-000000000001",
		WorkflowRunID:  "83000000-0000-4000-8000-000000000012",
		ConversationID: "83000000-0000-4000-8000-000000000002",
		QuestionID:     "83000000-0000-4000-8000-000000000010",
		AnswerID:       "83000000-0000-4000-8000-000000000013",
	}
	loaded, err := repository.LoadWorkspaceAnalysisRunForExecution(ctx, query)
	if err != nil {
		t.Fatalf("load exact workspace analysis run: %v", err)
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	if loaded.ID != "83000000-0000-4000-8000-000000000014" ||
		loaded.WorkspaceID != query.WorkspaceID || loaded.WorkflowRunID != query.WorkflowRunID ||
		loaded.ConversationID != query.ConversationID || loaded.QuestionID != query.QuestionID || loaded.AnswerID != query.AnswerID ||
		loaded.DefinitionKey != definition.Key || loaded.DefinitionVersion != definition.Version || loaded.DefinitionHash != definition.GraphHash ||
		loaded.Status != agentdomain.WorkspaceAnalysisRunRunning || loaded.Version != 2 || agentdomain.ValidateWorkspaceAnalysisRun(loaded) != nil {
		t.Fatalf("loaded workspace analysis run differs from frozen fixture: %+v", loaded)
	}

	tests := []struct {
		name   string
		mutate func(*agentapplication.WorkspaceAnalysisRunExecutionQuery)
	}{
		{
			name: "missing workflow run",
			mutate: func(candidate *agentapplication.WorkspaceAnalysisRunExecutionQuery) {
				candidate.WorkflowRunID = "83000000-0000-4000-8000-000000000101"
			},
		},
		{
			name: "conversation drift in same workspace",
			mutate: func(candidate *agentapplication.WorkspaceAnalysisRunExecutionQuery) {
				candidate.ConversationID = "83000000-0000-4000-8000-000000000102"
			},
		},
		{
			name: "question drift in same workspace",
			mutate: func(candidate *agentapplication.WorkspaceAnalysisRunExecutionQuery) {
				candidate.QuestionID = "83000000-0000-4000-8000-000000000103"
			},
		},
		{
			name: "answer drift in same workspace",
			mutate: func(candidate *agentapplication.WorkspaceAnalysisRunExecutionQuery) {
				candidate.AnswerID = "83000000-0000-4000-8000-000000000104"
			},
		},
		{
			name: "workflow run from another workspace",
			mutate: func(candidate *agentapplication.WorkspaceAnalysisRunExecutionQuery) {
				candidate.WorkspaceID = "83000000-0000-4000-8000-000000000100"
			},
		},
	}

	wantPublicError := agentapplication.ErrorCodeWorkspaceAnalysisRunExecutionConflict + ": " + string(foundation.ErrorConsistencyViolation)
	sensitiveFragments := []string{
		string(loaded.ID), string(loaded.WorkspaceID), string(loaded.WorkflowRunID),
		string(loaded.ConversationID), string(loaded.QuestionID), string(loaded.AnswerID),
		"83000000-0000-4000-8000-000000000100", "wa-loader-other", "/tmp/wa-loader-other",
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := query
			test.mutate(&candidate)

			got, loadErr := repository.LoadWorkspaceAnalysisRunForExecution(ctx, candidate)
			if !reflect.DeepEqual(got, agentdomain.WorkspaceAnalysisRun{}) {
				t.Fatalf("failed scoped load returned persisted run data: %+v", got)
			}
			var classified *foundation.Error
			if !errors.As(loadErr, &classified) || classified.Kind != foundation.ErrorConsistencyViolation ||
				classified.Code != agentapplication.ErrorCodeWorkspaceAnalysisRunExecutionConflict || classified.Retryable {
				t.Fatalf("load error = %#v", loadErr)
			}
			if loadErr.Error() != wantPublicError {
				t.Fatalf("public error = %q, want %q", loadErr.Error(), wantPublicError)
			}
			for _, sensitiveFragment := range sensitiveFragments {
				if strings.Contains(loadErr.Error(), sensitiveFragment) {
					t.Fatalf("public error leaked persisted data %q: %q", sensitiveFragment, loadErr.Error())
				}
			}
		})
	}
}
