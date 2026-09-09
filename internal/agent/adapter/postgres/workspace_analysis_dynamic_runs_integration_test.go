//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
)

func TestWorkspaceAnalysisDynamicScopedRunStartIntegration(t *testing.T) {
	platform, ctx := newAgentPlatformIntegrationPool(t)
	config, command := seedWorkspaceAnalysisDynamicRunStart(t, ctx, platform)
	repository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	ids := &dynamicRunStartIDs{id: workspaceAnalysisRunStartIntegrationID(20)}
	service, err := application.NewScopedWorkspaceAnalysisRunServiceV2(repository, ids, config)
	if err != nil {
		t.Fatal(err)
	}
	start := func(rollback bool) (domain.WorkspaceAnalysisRun, error) {
		var run domain.WorkspaceAnalysisRun
		err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			tx, err := platformpostgres.GORMTransaction(scope)
			if err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Exec(`SET LOCAL TIME ZONE 'Asia/Shanghai'`).Error; err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Exec(workspaceAnalysisRunStartAnswerSQL(true), workspaceAnalysisRunStartAnswerArguments(command)...).Error; err != nil {
				return err
			}
			run, err = service.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
			if err != nil {
				return err
			}
			if rollback {
				return errWorkspaceAnalysisRunStartIntegrationRollback
			}
			return nil
		})
		return run, err
	}
	if _, err := start(true); !errors.Is(err, errWorkspaceAnalysisRunStartIntegrationRollback) {
		t.Fatalf("v2 scoped start must reach its caller-owned rollback: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	assertWorkspaceAnalysisDispatchCountsIntegration(t, ctx, platform.DB(), command.QuestionID, 0)
	run, err := start(false)
	if err != nil {
		t.Fatalf("v2 scoped insert and readback: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	if run.DefinitionVersion != 2 || run.PolicyVersion != 2 || run.Status != domain.WorkspaceAnalysisRunQueued || run.Version != 1 ||
		!run.CreatedAt.Equal(command.CreatedAt) || !run.UpdatedAt.Equal(command.CreatedAt) || ids.calls != 2 ||
		run.Limits.Amount.ModelCalls != 14 || run.Limits.Amount.ToolCalls != 13 {
		t.Fatal("v2 scoped start changed its frozen queued run")
	}
	assertWorkspaceAnalysisDispatchCountsIntegration(t, ctx, platform.DB(), command.QuestionID, 1)
	if _, err := platform.DB().Exec(ctx, `UPDATE agent.workspace_analysis_run SET status='running',version=version+1,updated_at=clock_timestamp() WHERE id=$1`, string(run.ID)); err != nil {
		t.Fatal(err)
	}
	config.ConfigRevision++
	replayIDs := &dynamicRunStartIDs{id: workspaceAnalysisRunStartIntegrationID(21)}
	replayService, err := application.NewScopedWorkspaceAnalysisRunServiceV2(repository, replayIDs, config)
	if err != nil {
		t.Fatal(err)
	}
	command.Replayed = true
	var replayed domain.WorkspaceAnalysisRun
	err = repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		replayed, err = replayService.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
		return err
	})
	if err != nil || replayed.ID != run.ID || replayed.ConfigRevision != run.ConfigRevision || replayed.Status != domain.WorkspaceAnalysisRunRunning || replayIDs.calls != 0 {
		t.Fatalf("v2 scoped replay did not preserve its persisted binding: %v", unwrapGORMWorkspaceAnalysisModelFenceCause(err))
	}
	command.AnswerID = workspaceAnalysisRunStartIntegrationID(22)
	err = repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		_, err := replayService.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
		return err
	})
	if agentErrorCode(err) != application.ErrorCodeWorkspaceAnalysisRunStartConflict || replayIDs.calls != 0 {
		t.Fatalf("v2 scoped replay accepted a different Answer binding: %v", err)
	}
	assertWorkspaceAnalysisDispatchCountsIntegration(t, ctx, platform.DB(), command.QuestionID, 1)
}

type dynamicRunStartIDs struct {
	id    foundation.ID
	calls int
}

func (ids *dynamicRunStartIDs) New() (foundation.ID, error) {
	ids.calls++
	return ids.id, nil
}

func seedWorkspaceAnalysisDynamicRunStart(t *testing.T, ctx context.Context, platform *platformpostgres.Pool) (application.WorkspaceAnalysisRunStartConfig, application.WorkspaceAnalysisRunStartCommand) {
	t.Helper()
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2()
	graph, err := json.Marshal(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshotV2()
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, conversationID := workspaceAnalysisRunStartIntegrationID(1), workspaceAnalysisRunStartIntegrationID(2)
	questionID, definitionID := workspaceAnalysisRunStartIntegrationID(3), workspaceAnalysisRunStartIntegrationID(4)
	workflowID, answerID := workspaceAnalysisRunStartIntegrationID(5), workspaceAnalysisRunStartIntegrationID(6)
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
			VALUES($1,'dynamic-run-start','/tmp/dynamic-run-start','/tmp/dynamic-run-start',clock_timestamp(),'active',clock_timestamp(),clock_timestamp())`, []any{string(workspaceID)}},
		{`INSERT INTO agent.conversation(id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,idempotency_key,request_hash)
			VALUES($1,$2,'open','Dynamic Run Start',1,clock_timestamp(),clock_timestamp(),clock_timestamp(),'dynamic-run-start',repeat('1',64))`, []any{string(conversationID), string(workspaceID)}},
		{`INSERT INTO agent.question(id,workspace_id,conversation_id,ordinal,mode,question_text,scope,answer_depth,output_format,context_through_ordinal,context_hash,idempotency_key,request_hash,created_at)
			VALUES($1,$2,$3,1,'workspace_analysis','analyze workspace','{}','standard','markdown',0,repeat('2',64),'dynamic-run-question',repeat('3',64),clock_timestamp())`, []any{string(questionID), string(workspaceID), string(conversationID)}},
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
			VALUES($1,$2,$3,$4,$5::jsonb,clock_timestamp())`, []any{string(definitionID), string(workspaceID), definition.Key, definition.Version, string(graph)}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
			VALUES($1,$2,$3,'running','{}',1,clock_timestamp(),clock_timestamp())`, []any{string(workflowID), string(workspaceID), string(definitionID)}},
	} {
		if _, err := platform.DB().Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	command := application.WorkspaceAnalysisRunStartCommand{WorkspaceID: workspaceID, ConversationID: conversationID,
		QuestionID: questionID, AnswerID: answerID, WorkflowRunID: workflowID}
	if err := platform.DB().QueryRow(ctx, `SELECT created_at FROM agent.question WHERE id=$1`, string(questionID)).Scan(&command.CreatedAt); err != nil {
		t.Fatal(err)
	}
	return application.WorkspaceAnalysisRunStartConfig{
		DefinitionHash: definition.GraphHash, ToolCatalogHash: catalog.Hash, ConfigRevision: 7, SynthesisProfileMaxOutputTokens: 4096,
		Timeouts: application.WorkspaceAnalysisV1Timeouts{
			PlanModelTimeout: 2 * time.Second, SynthesisModelTimeout: 3 * time.Second, ReviewModelTimeout: 2 * time.Second,
			GitToolTimeout: time.Second, SearchToolTimeout: time.Second, SourceReadToolTimeout: time.Second, ValidateCitationToolTimeout: time.Second,
		},
		RuntimeLimits: application.WorkspaceAnalysisRuntimeLimits{RiverJobTimeout: 10 * time.Minute, LeaseDuration: 30 * time.Second, HeartbeatInterval: 5 * time.Second},
	}, command
}
