//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkspaceAnalysisFinalizerClarificationAcceptsPlannerResultWithoutSubjectCandidateAndReplays(t *testing.T) {
	shared, pool, ctx, dispatched := newWorkspaceAnalysisCancellationHookFixture(t, "clarification-finalizer")
	ids := foundation.NewUUIDGenerator(nil)
	newID := func() foundation.ID {
		t.Helper()
		id, err := ids.New()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	var analysisRunID, definitionID foundation.ID
	var definitionVersion int64
	var definitionHash string
	if err := pool.QueryRow(ctx, `SELECT analysis.id::text,definition.id::text,definition.version,analysis.definition_hash
		FROM agent.workspace_analysis_run AS analysis
		JOIN workflow.run AS run ON run.id=analysis.workflow_run_id
		JOIN workflow.definition AS definition ON definition.id=run.definition_id
		WHERE analysis.answer_id=$1`, string(dispatched.AnswerID)).Scan(
		&analysisRunID, &definitionID, &definitionVersion, &definitionHash,
	); err != nil {
		t.Fatal(err)
	}

	retrieveNodeID, retrieveAttemptID, operationID, reservationID, modelRunID, modelCallID, resultID, indexVersionID :=
		newID(), newID(), newID(), newID(), newID(), newID(), newID(), newID()
	const leaseOwner = "workspace-analysis-clarification-finalizer"
	request := []byte(`{"prompt":"clarify workspace scope"}`)
	requestDigest := sha256.Sum256(request)
	requestHash := hex.EncodeToString(requestDigest[:])
	batch := &pgx.Batch{}
	batch.Queue(`UPDATE workflow.run
		SET status='running',version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2`, string(dispatched.WorkflowRunID), string(dispatched.WorkspaceID))
	batch.Queue(`UPDATE agent.workspace_analysis_run
		SET status='running',version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND status='queued'`, string(analysisRunID))
	batch.Queue(`INSERT INTO workflow.node_run(
			id,run_id,node_key,node_type,status,attempt,input,lease_owner,lease_until,
			idempotency_key,input_schema_version,output_schema_version,dispatch_no,version,created_at,updated_at
		) VALUES(
			$1,$2,'retrieve_evidence','agent.workspace-analysis.retrieve_evidence','running',1,'{}',$3,
			clock_timestamp()+interval '5 minutes','workspace-analysis-clarification-retrieve',1,1,2,1,
			clock_timestamp(),clock_timestamp()
		)`, string(retrieveNodeID), string(dispatched.WorkflowRunID), leaseOwner)
	batch.Queue(`INSERT INTO workflow.node_attempt(
			id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
		) VALUES(
			$1,$2,1,2,0,'workspace-analysis-clarification-retrieve-1',$3,
			(SELECT lease_until FROM workflow.node_run WHERE id=$2),'running',clock_timestamp()
		)`, string(retrieveAttemptID), string(retrieveNodeID), leaseOwner)
	batch.Queue(`INSERT INTO retrieval.index_version(
			id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
			source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
			version,created_at,updated_at
		) VALUES(
			$1,$2,'simple','v1',repeat('8',64),'{}','workspace-analysis-clarification:index',repeat('9',64),0,
			'workspace-analysis-clarification-index','building','["vector"]',1,clock_timestamp(),clock_timestamp()
		)`, string(indexVersionID), string(dispatched.WorkspaceID))
	batch.Queue(`INSERT INTO agent.workspace_analysis_operation(
			id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
			ordinal,call_kind,request_hash,status,version,created_at,updated_at
		) VALUES(
			$1,$2,$3,$4,$5,'retrieve_evidence','RETRIEVAL_PLAN',1,'MODEL',$6,'PENDING',1,
			clock_timestamp(),clock_timestamp()
		)`, string(operationID), string(dispatched.WorkspaceID), string(analysisRunID), string(dispatched.WorkflowRunID), string(retrieveNodeID), requestHash)
	batchResults := pool.SendBatch(ctx, batch)
	defer func() { _ = batchResults.Close() }()
	for range 6 {
		if _, err := batchResults.Exec(); err != nil {
			t.Fatal(err)
		}
	}
	var workflowStatus, nodeStatus, attemptStatus, analysisStatus, nodeOwner, attemptOwner string
	var nodeAttempt, attemptNo int
	if err := pool.QueryRow(ctx, `SELECT workflow.status,node.status,attempt.status,analysis.status,
		node.lease_owner,attempt.lease_owner,node.attempt,attempt.attempt_no
		FROM workflow.run AS workflow
		JOIN workflow.node_run AS node ON node.id=$1
		JOIN workflow.node_attempt AS attempt ON attempt.id=$2
		JOIN agent.workspace_analysis_run AS analysis ON analysis.id=$3
		WHERE workflow.id=$4`, string(retrieveNodeID), string(retrieveAttemptID), string(analysisRunID), string(dispatched.WorkflowRunID)).Scan(
		&workflowStatus, &nodeStatus, &attemptStatus, &analysisStatus, &nodeOwner, &attemptOwner, &nodeAttempt, &attemptNo,
	); err != nil {
		t.Fatal(err)
	}
	if workflowStatus != "running" || nodeStatus != "running" || attemptStatus != "running" || analysisStatus != "running" ||
		nodeOwner != leaseOwner || attemptOwner != leaseOwner || nodeAttempt != 1 || attemptNo != 1 {
		t.Fatalf("planner fixture fence workflow=%s node=%s/%d/%s attempt=%s/%d/%s analysis=%s", workflowStatus, nodeStatus, nodeAttempt, nodeOwner, attemptStatus, attemptNo, attemptOwner, analysisStatus)
	}
	seedWorkspaceAnalysisFinalizerGitPrefix(t, ctx, pool, ids, dispatched.WorkspaceID, dispatched.WorkflowRunID, analysisRunID, dispatched.NodeRunID)

	fence, err := workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(shared)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := agentpostgres.NewGORMWorkspaceAnalysisRepository(shared, fence)
	if err != nil {
		t.Fatal(err)
	}
	identity := agentapplication.WorkspaceAnalysisModelExecutionIdentity{
		WorkspaceID: dispatched.WorkspaceID, DefinitionID: definitionID, DefinitionVersion: definitionVersion,
		DefinitionHash: definitionHash, WorkflowRunID: dispatched.WorkflowRunID,
		NodeKey:   agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence,
		NodeRunID: retrieveNodeID, NodeAttemptID: retrieveAttemptID, LeaseOwner: leaseOwner, LeaseFence: 1,
	}
	operationKey := agentdomain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: analysisRunID, NodeKey: agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence,
		Kind: agentdomain.WorkspaceAnalysisOperationRetrievalPlan, Ordinal: 1,
	}
	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	modelRun := agentdomain.ModelRun{
		ID: modelRunID, WorkspaceID: dispatched.WorkspaceID, WorkflowRunID: dispatched.WorkflowRunID,
		NodeRunID: retrieveNodeID, NodeAttemptID: retrieveAttemptID,
		Model:         agentdomain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "model-test", ModelVersion: "2026-08-17"},
		Profile:       agentdomain.ModelProfileRef{ID: "workspace-analysis", Version: "v1"},
		Prompt:        agentdomain.PromptRef{ID: "workspace-analysis-plan", Version: "v1"},
		Schema:        agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		ReducedSchema: agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		Retrieval:     agentdomain.RetrievalRef{IndexVersionID: indexVersionID}, Status: agentdomain.ModelRunRunning, Version: 1,
		CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	modelCall := agentdomain.ModelCall{
		ID: modelCallID, ModelRunID: modelRunID, CallNo: 1, Phase: agentdomain.ModelCallPlan,
		Model: modelRun.Model, Profile: modelRun.Profile, Prompt: modelRun.Prompt, Schema: modelRun.Schema,
		MaxOutputTokens: int(agentdomain.WorkspaceAnalysisV1PlanMaxOutputTokens), Status: agentdomain.ModelCallStarted,
		RequestHash: requestHash, RequestBytes: int64(len(request)), Version: 1, StartedAt: startedAt,
	}
	authorized, err := repository.AuthorizeWorkspaceAnalysisModelCall(ctx, agentapplication.AuthorizeWorkspaceAnalysisModelCallCommand{
		Identity: identity, OperationKey: operationKey, OperationID: operationID, ReservationID: reservationID,
		Run: modelRun, Call: modelCall, RequestDocument: request,
	})
	if err != nil || authorized.Disposition != agentapplication.WorkspaceAnalysisModelAuthorizationCreated {
		t.Fatalf("AuthorizeWorkspaceAnalysisModelCall()=%#v err=%s", authorized, conversationErrorChain(err))
	}

	completedAt := time.Now().UTC().Truncate(time.Microsecond)
	if completedAt.Before(authorized.Call.StartedAt) {
		completedAt = authorized.Call.StartedAt
	}
	planDocument, err := json.Marshal(agentdomain.WorkspaceAnalysisPlanResult{
		ResultType: agentdomain.ResultTypeWorkspaceAnalysisPlan, SchemaID: agentdomain.WorkspaceAnalysisPlanSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: authorized.Run.ID,
		Payload: agentdomain.RAGQueryPlanPayload{
			Intent: "clarify workspace scope", RequiresClarification: true, Rewrites: []string{},
			ClarificationReason: "the workspace area is missing", ClarificationQuestion: "Which workspace area should be analyzed?",
			SuggestedScopes: []string{"internal/agent"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	responseDigest := sha256.Sum256(planDocument)
	responseHash := hex.EncodeToString(responseDigest[:])
	finalCall := authorized.Call
	finalCall.Status, finalCall.ResponseHash, finalCall.ResponseBytes = agentdomain.ModelCallSucceeded, responseHash, int64(len(planDocument))
	finalCall.Usage = agentdomain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	finalCall.LatencyMillis, finalCall.Version, finalCall.CompletedAt = 1, 2, &completedAt
	finalRun := authorized.Run
	finalRun.Status, finalRun.FinalResultType, finalRun.Version = agentdomain.ModelRunSucceeded, agentdomain.ResultTypeWorkspaceAnalysisPlan, 2
	finalRun.UpdatedAt, finalRun.CompletedAt = completedAt, &completedAt
	modelResult := agentdomain.WorkspaceAnalysisModelResult{
		ID: resultID, WorkspaceID: dispatched.WorkspaceID, AnalysisRunID: analysisRunID, OperationID: authorized.OperationID,
		NodeAttemptID: retrieveAttemptID, ModelRunID: authorized.Run.ID, ModelCallID: authorized.Call.ID,
		OperationKind: agentdomain.WorkspaceAnalysisOperationRetrievalPlan, Schema: finalRun.Schema,
		Document: planDocument, DocumentHash: responseHash, DocumentBytes: int64(len(planDocument)), CreatedAt: completedAt,
	}
	if _, err := repository.FinalizeWorkspaceAnalysisModelResult(ctx, agentapplication.FinalizeWorkspaceAnalysisModelResultCommand{
		Identity: identity, OperationKey: operationKey, OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: authorized.Call.Version, ExpectedRunVersion: authorized.Run.Version,
		Call: finalCall, Run: finalRun, Result: modelResult,
	}); err != nil {
		t.Fatalf("FinalizeWorkspaceAnalysisModelResult() err=%s", conversationErrorChain(err))
	}

	var subjectCandidateID, subjectCandidateHash *string
	if err := pool.QueryRow(ctx, `SELECT subject_candidate_id::text,subject_candidate_hash
		FROM agent.workspace_analysis_model_result WHERE id=$1`, string(resultID)).Scan(&subjectCandidateID, &subjectCandidateHash); err != nil {
		t.Fatal(err)
	}
	if subjectCandidateID != nil || subjectCandidateHash != nil {
		t.Fatalf("planner subject candidate id=%v hash=%v; want both NULL", subjectCandidateID, subjectCandidateHash)
	}

	events, err := eventspostgres.NewGORMStore(shared)
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := NewGORMWorkspaceAnalysisFinalizer(shared, events, ids)
	if err != nil {
		t.Fatal(err)
	}
	lookup := conversationapplication.WorkspaceAnalysisPublicationLookup{
		AnswerPublicationLookup: conversationapplication.AnswerPublicationLookup{
			WorkspaceID: dispatched.WorkspaceID, WorkflowRunID: dispatched.WorkflowRunID,
			NodeRunID: retrieveNodeID, NodeAttemptID: retrieveAttemptID,
			ConversationID: dispatched.ConversationID, QuestionID: dispatched.QuestionID, AnswerID: dispatched.AnswerID,
		},
		AnalysisRunID: analysisRunID, ExpectedLeaseOwner: leaseOwner, ExpectedLeaseFence: 1,
	}
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: lookup, ExpectedAnswerVersion: 1,
		Reason: agentdomain.WorkspaceAnalysisRunNeedsClarification, OperationID: &authorized.OperationID,
		Artifact: &conversationapplication.WorkspaceAnalysisTerminationArtifact{
			Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult, ID: resultID, Hash: responseHash,
		},
	}
	output, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || replayed || output.ModelRunID == nil || *output.ModelRunID != authorized.Run.ID ||
		output.PublicationStatus != conversationdomain.AnswerPublicationClarificationRequired || output.ResultType != conversationdomain.AnswerResultClarification {
		t.Fatalf("FinalizeTermination()=%#v replayed=%t err=%s", output, replayed, conversationErrorChain(err))
	}

	var answerModelRunID, proofModelRunID, proofArtifactID, proofOperationID *string
	var runStatus, runReason string
	if err := pool.QueryRow(ctx, `SELECT answer.model_run_id::text,analysis.status,analysis.termination_reason,
		proof.published_model_run_id::text,proof.artifact_id::text,proof.operation_id::text
		FROM agent.answer AS answer
		JOIN agent.workspace_analysis_run AS analysis ON analysis.answer_id=answer.id
		JOIN agent.workspace_analysis_termination_proof AS proof ON proof.analysis_run_id=analysis.id
		WHERE answer.id=$1`, string(dispatched.AnswerID)).Scan(
		&answerModelRunID, &runStatus, &runReason, &proofModelRunID, &proofArtifactID, &proofOperationID,
	); err != nil {
		t.Fatal(err)
	}
	wantModelRunID := string(authorized.Run.ID)
	if answerModelRunID == nil || proofModelRunID == nil || *answerModelRunID != wantModelRunID || *proofModelRunID != wantModelRunID ||
		proofArtifactID == nil || *proofArtifactID != string(resultID) || proofOperationID == nil || *proofOperationID != string(authorized.OperationID) ||
		runStatus != string(agentdomain.WorkspaceAnalysisRunClarificationRequired) || runReason != string(agentdomain.WorkspaceAnalysisRunNeedsClarification) {
		t.Fatalf("clarification bindings answer=%v proof=%v artifact=%v operation=%v run=%s/%s", answerModelRunID, proofModelRunID, proofArtifactID, proofOperationID, runStatus, runReason)
	}

	finalizer.ids = workspaceAnalysisReplayFailingIDs{}
	replayedOutput, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || !replayed || replayedOutput.SchemaVersion != output.SchemaVersion ||
		replayedOutput.AnswerID != output.AnswerID || replayedOutput.PublicationStatus != output.PublicationStatus ||
		replayedOutput.ResultType != output.ResultType || replayedOutput.ModelRunID == nil || output.ModelRunID == nil ||
		*replayedOutput.ModelRunID != *output.ModelRunID || replayedOutput.ResultHash != output.ResultHash || replayedOutput.ProofID != output.ProofID {
		t.Fatalf("FinalizeTermination(replay)=%#v replayed=%t err=%s", replayedOutput, replayed, conversationErrorChain(err))
	}
}

func seedWorkspaceAnalysisFinalizerGitPrefix(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	ids foundation.IDGenerator,
	workspaceID, workflowRunID, analysisRunID, nodeRunID foundation.ID,
) {
	t.Helper()
	newID := func() foundation.ID {
		id, err := ids.New()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	attemptID, operationID, reservationID, toolCallID, receiptID := newID(), newID(), newID(), newID(), newID()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE workflow.node_run SET
		status='running',attempt=1,lease_owner='workspace-analysis-clarification-git',
		lease_until=clock_timestamp()+interval '5 minutes',version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND run_id=$2`, string(nodeRunID), string(workflowRunID))
	exec(`INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
	) VALUES(
		$1,$2,1,1,0,'workspace-analysis-clarification-git-1','workspace-analysis-clarification-git',
		(SELECT lease_until FROM workflow.node_run WHERE id=$2),'running',clock_timestamp()
	)`, string(attemptID), string(nodeRunID))
	exec(`INSERT INTO agent.workspace_analysis_operation(
		id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,
		ordinal,call_kind,request_hash,status,version,created_at,updated_at
	) VALUES(
		$1,$2,$3,$4,$5,'inspect_workspace','GIT_STATUS',1,'TOOL',repeat('5',64),'PENDING',1,
		clock_timestamp(),clock_timestamp()
	)`, string(operationID), string(workspaceID), string(analysisRunID), string(workflowRunID), string(nodeRunID))
	exec(`INSERT INTO workflow.tool_call(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
		requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
		output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
		request_hash,request_bytes,request_summary,status,retryable,started_at,duration_ms,version
	) VALUES(
		$1,$2,$3,$4,$5,1,'ReadGitStatus',2,
		'b5dd1fcca72d5bb41fd9ad3f74006d3706e4184ad39e2a3409b565d1ff896bbd',
		'tool.read_git_status.input',1,'tool.read_git_status.output',1,'READ_LOCAL','NONE','TRUSTED_WORKFLOW_ONLY',
		repeat('5',64),2,'{}','STARTED',false,clock_timestamp(),0,1
	)`, string(toolCallID), string(workspaceID), string(workflowRunID), string(nodeRunID), string(attemptID))
	exec(`UPDATE agent.workspace_analysis_operation SET
		status='STARTED',first_node_attempt_id=$1,latest_node_attempt_id=$1,tool_call_id=$2,
		version=2,started_at=clock_timestamp(),updated_at=clock_timestamp()
		WHERE id=$3`, string(attemptID), string(toolCallID), string(operationID))
	exec(`INSERT INTO agent.workspace_analysis_budget_reservation(
		id,workspace_id,analysis_run_id,operation_id,call_kind,tool_call_id,status,
		reserved_model_calls,reserved_tool_calls,reserved_source_reads,reserved_input_tokens,reserved_output_tokens,created_at
	) VALUES($1,$2,$3,$4,'TOOL',$5,'RESERVED',0,1,0,0,0,clock_timestamp())`,
		string(reservationID), string(workspaceID), string(analysisRunID), string(operationID), string(toolCallID))
	exec(`UPDATE agent.workspace_analysis_run SET
		reserved_tool_calls=reserved_tool_calls+1,version=version+1,updated_at=clock_timestamp()
		WHERE id=$1`, string(analysisRunID))
	const output = `{"branch":"main","clean":true,"conflict_count":0,"head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}`
	exec(`UPDATE workflow.tool_call SET
		status='SUCCEEDED',response_hash=encode(sha256(convert_to($1,'UTF8')),'hex'),
		response_bytes=octet_length(convert_to($1,'UTF8')),response_summary='{"clean":true}',
		completed_at=clock_timestamp(),duration_ms=1,version=2
		WHERE id=$2`, output, string(toolCallID))
	exec(`INSERT INTO workflow.tool_result_receipt(
		id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,
		output_schema_id,output_schema_version,definition_hash,persistence_policy,max_output_bytes,max_private_binding_bytes,
		output_document,output_hash,output_bytes,created_at
	) SELECT $1,id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,requested_tool_name,tool_version,
		output_schema_id,output_schema_version,definition_hash,'PERSIST_CANONICAL',4096,1024,
		convert_to($2,'UTF8'),response_hash,response_bytes,completed_at+interval '1 millisecond'
		FROM workflow.tool_call WHERE id=$3`, string(receiptID), output, string(toolCallID))
	exec(`UPDATE agent.workspace_analysis_budget_reservation SET
		status='SETTLED',settled_tool_calls=1,settled_at=clock_timestamp() WHERE id=$1`, string(reservationID))
	exec(`UPDATE agent.workspace_analysis_run SET
		reserved_tool_calls=reserved_tool_calls-1,settled_tool_calls=settled_tool_calls+1,
		version=version+1,updated_at=clock_timestamp() WHERE id=$1`, string(analysisRunID))
	exec(`UPDATE agent.workspace_analysis_operation SET
		status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',result_id=$1,
		result_hash=(SELECT output_hash FROM workflow.tool_result_receipt WHERE id=$1),version=3,
		completed_at=clock_timestamp(),updated_at=clock_timestamp()
		WHERE id=$2`, string(receiptID), string(operationID))
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceAnalysisFinalizerCancellationCommitsBundleAndRecoversExactResponseLoss(t *testing.T) {
	repository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(900)
	conversationID := conversationPostgresID(901)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := repository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Workspace analysis finalizer", "workspace-analysis-finalizer", createdAt,
	)); err != nil {
		t.Fatal(err)
	}
	dispatched, err := newWorkspaceAnalysisQuestionDispatcherIntegration(t, shared).SubmitQuestion(
		ctx,
		workspaceAnalysisQuestionDispatchRecord(
			t, workspaceID, conversationID, "Inspect the current workspace.", "workspace-analysis-finalizer-question",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	var analysisRunID foundation.ID
	if err := pool.QueryRow(ctx, `SELECT id::text FROM agent.workspace_analysis_run
		WHERE workspace_id=$1 AND workflow_run_id=$2 AND answer_id=$3`,
		string(workspaceID), string(dispatched.Workflow.RunID), string(dispatched.Answer.ID),
	).Scan(&analysisRunID); err != nil {
		t.Fatal(err)
	}
	attemptID := conversationPostgresID(902)
	if _, err := pool.Exec(ctx, `UPDATE workflow.run
		SET status='running',version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2`, string(dispatched.Workflow.RunID), string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run
		SET status='running',attempt=1,lease_owner='workspace-analysis-finalizer',
			lease_until=clock_timestamp()+interval '5 minutes',version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND run_id=$2`, string(dispatched.NodeRunID), string(dispatched.Workflow.RunID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
	) SELECT $1,$2,1,1,0,'workspace-analysis-finalizer-delivery','workspace-analysis-finalizer',
		lease_until,'running',clock_timestamp() FROM workflow.node_run WHERE id=$2`,
		string(attemptID), string(dispatched.NodeRunID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.workspace_analysis_run
		SET status='running',version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND status='queued'`, string(analysisRunID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.run
		SET cancel_requested_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND status='running'`, string(dispatched.Workflow.RunID)); err != nil {
		t.Fatal(err)
	}
	draftID := conversationPostgresID(903)
	if _, err := pool.Exec(ctx, `INSERT INTO agent.answer_draft_session(
		id,workspace_id,answer_id,workflow_run_id,node_run_id,node_attempt_id,attempt_no,lease_owner,generation,status,
		next_sequence,total_bytes,expires_at,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,1,'workspace-analysis-finalizer',1,'ACTIVE',1,0,
		clock_timestamp()+interval '30 minutes',clock_timestamp(),clock_timestamp())`,
		string(draftID), string(workspaceID), string(dispatched.Answer.ID), string(dispatched.Workflow.RunID),
		string(dispatched.NodeRunID), string(attemptID)); err != nil {
		t.Fatal(err)
	}

	events, err := eventspostgres.NewGORMStore(shared)
	if err != nil {
		t.Fatal(err)
	}
	lookup := conversationapplication.WorkspaceAnalysisPublicationLookup{
		AnswerPublicationLookup: conversationapplication.AnswerPublicationLookup{
			WorkspaceID: workspaceID, WorkflowRunID: dispatched.Workflow.RunID,
			NodeRunID: dispatched.NodeRunID, NodeAttemptID: attemptID,
			ConversationID: conversationID, QuestionID: dispatched.Question.ID, AnswerID: dispatched.Answer.ID,
		},
		AnalysisRunID:      analysisRunID,
		ExpectedLeaseOwner: "workspace-analysis-finalizer",
		ExpectedLeaseFence: 1,
	}
	command := conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
		WorkspaceAnalysisPublicationLookup: lookup,
		ExpectedAnswerVersion:              1,
		Reason:                             agentdomain.WorkspaceAnalysisRunCancellation,
	}
	auditRecorder, auditStore := newWorkspaceAnalysisAuditIntegration(t, shared)
	auditFailure := errors.New("injected workspace analysis terminal audit failure")
	failingFinalizer, err := NewGORMWorkspaceAnalysisFinalizerWithAudit(
		shared, events, foundation.NewUUIDGenerator(nil), &workspaceAnalysisAuditCapture{err: auditFailure},
		"workspace-analysis-finalizer-integration",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := failingFinalizer.FinalizeTermination(ctx, command); !errors.Is(err, auditFailure) {
		t.Fatalf("FinalizeTermination(audit failure) err=%s", conversationErrorChain(err))
	}
	var rollbackAnswerStatus, rollbackRunStatus, rollbackDraftStatus string
	var rollbackProofs, rollbackAnswerEvents, rollbackAnalysisEvents, rollbackAudits int64
	if err := pool.QueryRow(ctx, `SELECT a.publication_status,r.status,d.status,
		(SELECT count(*) FROM agent.workspace_analysis_termination_proof p WHERE p.analysis_run_id=r.id),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id
		 AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.cancelled'),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id
		 AND e.resource_ref='workspace_analysis:'||r.id::text AND e.event_type='workspace_analysis.terminated'),
		(SELECT count(*) FROM ops.audit_event e WHERE e.workspace_id=a.workspace_id)
		FROM agent.answer a
		JOIN agent.workspace_analysis_run r ON r.answer_id=a.id AND r.workspace_id=a.workspace_id
		JOIN agent.answer_draft_session d ON d.answer_id=a.id AND d.workspace_id=a.workspace_id
		WHERE a.id=$1`, string(dispatched.Answer.ID)).Scan(
		&rollbackAnswerStatus, &rollbackRunStatus, &rollbackDraftStatus,
		&rollbackProofs, &rollbackAnswerEvents, &rollbackAnalysisEvents, &rollbackAudits,
	); err != nil {
		t.Fatal(err)
	}
	if rollbackAnswerStatus != "pending" || rollbackRunStatus != "running" || rollbackDraftStatus != "ACTIVE" ||
		rollbackProofs != 0 || rollbackAnswerEvents != 0 || rollbackAnalysisEvents != 0 || rollbackAudits != 0 {
		t.Fatalf(
			"audit rollback answer=%s run=%s draft=%s proofs=%d answer_events=%d analysis_events=%d audits=%d",
			rollbackAnswerStatus, rollbackRunStatus, rollbackDraftStatus,
			rollbackProofs, rollbackAnswerEvents, rollbackAnalysisEvents, rollbackAudits,
		)
	}

	finalizer, err := NewGORMWorkspaceAnalysisFinalizerWithAudit(
		shared, events, foundation.NewUUIDGenerator(nil), auditRecorder, "workspace-analysis-finalizer-integration",
	)
	if err != nil {
		t.Fatal(err)
	}
	finalizer.uow = &conversationCommitResponseLossDB{UnitOfWork: finalizer.uow, loseNext: true}
	output, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || replayed || output.AnswerID != dispatched.Answer.ID || output.ProofID == "" ||
		output.PublicationStatus != conversationdomain.WorkspaceAnalysisPublicationCancelled ||
		output.ResultType != conversationdomain.AnswerResultWorkspaceAnalysisTermination || output.ModelRunID != nil {
		t.Fatalf("FinalizeTermination(response loss)=%#v replayed=%t err=%s", output, replayed, conversationErrorChain(err))
	}
	lookedUp, found, err := finalizer.LookupPublication(ctx, lookup)
	if err != nil || !found || lookedUp != output {
		t.Fatalf("LookupPublication()=%#v found=%t err=%s", lookedUp, found, conversationErrorChain(err))
	}
	wrongOwner := lookup
	wrongOwner.ExpectedLeaseOwner = "other-workspace-analysis-finalizer"
	if _, found, err := finalizer.LookupPublication(ctx, wrongOwner); err == nil || found {
		t.Fatalf("LookupPublication(wrong owner) found=%t err=%s", found, conversationErrorChain(err))
	}
	wrongFence := lookup
	wrongFence.ExpectedLeaseFence++
	if _, found, err := finalizer.LookupPublication(ctx, wrongFence); err == nil || found {
		t.Fatalf("LookupPublication(wrong fence) found=%t err=%s", found, conversationErrorChain(err))
	}
	wrongCommand := command
	wrongCommand.WorkspaceAnalysisPublicationLookup = wrongOwner
	if _, replayed, err := finalizer.FinalizeTermination(ctx, wrongCommand); err == nil || replayed {
		t.Fatalf("FinalizeTermination(wrong owner) replayed=%t err=%s", replayed, conversationErrorChain(err))
	}
	finalizer.ids = workspaceAnalysisReplayFailingIDs{}
	replayedOutput, replayed, err := finalizer.FinalizeTermination(ctx, command)
	if err != nil || !replayed || replayedOutput != output {
		t.Fatalf("FinalizeTermination(replay without new proof ID)=%#v replayed=%t err=%s", replayedOutput, replayed, conversationErrorChain(err))
	}

	var answerStatus, answerResultType, runStatus, runReason, draftStatus, eventType, sourceRef string
	var analysisEventType, analysisSourceRef, analysisSummary string
	var modelRunID *string
	var answerVersion, runVersion, conversationVersion, proofCount, eventCount, analysisEventCount int64
	if err := pool.QueryRow(ctx, `SELECT
		a.publication_status,a.result_type,a.model_run_id::text,a.version,
		r.status,r.termination_reason,r.version,c.version,d.status,
		(SELECT count(*) FROM agent.workspace_analysis_termination_proof p WHERE p.analysis_run_id=r.id),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.cancelled'),
		(SELECT event_type FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.cancelled'),
		(SELECT source_event_ref FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.cancelled'),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='workspace_analysis:'||r.id::text AND e.event_type='workspace_analysis.terminated'),
		(SELECT event_type FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='workspace_analysis:'||r.id::text AND e.event_type='workspace_analysis.terminated'),
		(SELECT source_event_ref FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='workspace_analysis:'||r.id::text AND e.event_type='workspace_analysis.terminated'),
		(SELECT payload_summary::text FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='workspace_analysis:'||r.id::text AND e.event_type='workspace_analysis.terminated')
		FROM agent.answer a
		JOIN agent.workspace_analysis_run r ON r.answer_id=a.id AND r.workspace_id=a.workspace_id
		JOIN agent.conversation c ON c.id=a.conversation_id AND c.workspace_id=a.workspace_id
		JOIN agent.answer_draft_session d ON d.answer_id=a.id AND d.workspace_id=a.workspace_id
		WHERE a.id=$1`, string(dispatched.Answer.ID)).Scan(
		&answerStatus, &answerResultType, &modelRunID, &answerVersion,
		&runStatus, &runReason, &runVersion, &conversationVersion, &draftStatus,
		&proofCount, &eventCount, &eventType, &sourceRef, &analysisEventCount, &analysisEventType, &analysisSourceRef, &analysisSummary,
	); err != nil {
		t.Fatal(err)
	}
	wantSource := "answer.cancelled:" + string(dispatched.Answer.ID) + ":v2"
	analysisPayload, payloadErr := eventsdomain.DecodePayloadSummary([]byte(analysisSummary))
	if answerStatus != "cancelled" || answerResultType != "workspace_analysis_termination" || modelRunID != nil || answerVersion != 2 ||
		runStatus != "cancelled" || runReason != string(agentdomain.WorkspaceAnalysisRunCancellation) || runVersion != 3 ||
		conversationVersion < 2 || draftStatus != "ABORTED" || proofCount != 1 || eventCount != 1 ||
		eventType != "answer.cancelled" || sourceRef != wantSource || analysisEventCount != 1 ||
		analysisEventType != "workspace_analysis.terminated" ||
		analysisSourceRef != "workspace_analysis.terminated:"+string(analysisRunID)+":v1" ||
		payloadErr != nil || analysisPayload.Status != "cancelled" ||
		analysisPayload.PublicationStatus != "cancelled" ||
		analysisPayload.ResultType != "workspace_analysis_termination" ||
		strings.Contains(analysisSummary, "published_document") || strings.Contains(analysisSummary, "result_hash") ||
		strings.Contains(analysisSummary, "private_binding") {
		t.Fatalf("answer=%s/%s model=%v v%d run=%s/%s v%d conversation=v%d draft=%s proof=%d event=%d/%s/%s analysis=%d/%s/%s/%s",
			answerStatus, answerResultType, modelRunID, answerVersion, runStatus, runReason, runVersion,
			conversationVersion, draftStatus, proofCount, eventCount, eventType, sourceRef,
			analysisEventCount, analysisEventType, analysisSourceRef, analysisSummary)
	}
	auditEvent := requireWorkspaceAnalysisAuditEventIntegration(
		t, ctx, auditStore, workspaceID, "workspace-analysis:terminated:"+string(analysisRunID)+":v1",
	)
	if auditEvent.WorkspaceID == nil || *auditEvent.WorkspaceID != workspaceID ||
		string(auditEvent.ActorType) != "WORKER" || auditEvent.ActorRef != "workspace-analysis-finalizer-integration" ||
		auditEvent.Action != workspaceAnalysisTerminatedAuditAction || string(auditEvent.Outcome) != "REJECTED" ||
		auditEvent.ErrorCode != string(agentdomain.WorkspaceAnalysisRunCancellation) ||
		auditEvent.ResourceType != workspaceAnalysisAuditResourceType ||
		auditEvent.ResourceRef != "workspace_analysis:"+string(analysisRunID) {
		t.Fatalf("workspace analysis terminal audit=%#v", auditEvent)
	}
	requireWorkspaceAnalysisAuditJSONObjectIntegration(t, auditEvent.Correlation, map[string]any{
		"analysis_run_id": string(analysisRunID), "workflow_run_id": string(dispatched.Workflow.RunID),
		"conversation_id": string(conversationID), "question_id": string(dispatched.Question.ID),
		"answer_id": string(dispatched.Answer.ID),
	})
	requireWorkspaceAnalysisAuditJSONObjectIntegration(t, auditEvent.Metadata, map[string]any{
		"status": "cancelled", "termination_reason": string(agentdomain.WorkspaceAnalysisRunCancellation),
		"publication_status": "cancelled", "result_type": "workspace_analysis_termination",
		"citation_count": float64(0), "answer_version": float64(2),
	})
	requireWorkspaceAnalysisAuditSafeIntegration(t, auditEvent, "Inspect the current workspace.")
}

type workspaceAnalysisReplayFailingIDs struct{}

func (workspaceAnalysisReplayFailingIDs) New() (foundation.ID, error) {
	return "", errors.New("proof id generation must not run during replay")
}
