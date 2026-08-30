//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConversationSSEMigrationUpRepeat(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	assertConversationSSEMigrationShape(t, ctx, pool)

	provider := migrationProvider(t, pool)
	if err := provider.Up(ctx); err != nil {
		t.Fatalf("conversation SSE repeated up: %v", err)
	}
	assertConversationSSEMigrationShape(t, ctx, pool)
}

func TestConversationSSEMigrationContractsProjection(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateConversationSSETestDatabase(t, ctx, pool)
	fixture := insertConversationSSEFixture(t, ctx, pool)

	duplicateConversation := validConversationRow(fixture.workspaceID, fixture.conversationID, "conversation-key")
	duplicateConversation.id = "ce100000-0000-4000-8000-000000000002"
	assertPostgresCode(t, insertConversation(ctx, pool, duplicateConversation), "23505")

	if err := insertConversation(ctx, pool, validConversationRow(fixture.otherWorkspaceID, "ce100000-0000-4000-8000-000000000004", "conversation-key")); err != nil {
		t.Fatalf("conversation idempotency key should be workspace scoped: %v", err)
	}

	question1 := validQuestionRow(fixture, fixture.question1ID, 1, "question-key-1")
	if err := insertQuestion(ctx, pool, question1); err != nil {
		t.Fatal(err)
	}
	duplicateQuestion := validQuestionRow(fixture, "ce100000-0000-4000-8000-000000000012", 2, "question-key-1")
	assertPostgresCode(t, insertQuestion(ctx, pool, duplicateQuestion), "23505")
	crossWorkspaceQuestion := validQuestionRow(fixture, "ce100000-0000-4000-8000-000000000013", 2, "question-key-2")
	crossWorkspaceQuestion.workspaceID = fixture.otherWorkspaceID
	assertPostgresCode(t, insertQuestion(ctx, pool, crossWorkspaceQuestion), "23503")
	_, err := pool.Exec(ctx, `UPDATE agent.question SET question_text='mutated' WHERE id=$1`, question1.id)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM agent.question WHERE id=$1`, question1.id)
	assertPostgresCode(t, err, "55000")

	answer1 := validPendingAnswerRow(fixture, fixture.answer1ID, question1.id, fixture.run1ID)
	if err := insertAnswer(ctx, pool, answer1); err != nil {
		t.Fatal(err)
	}
	questionWorkspaceGuard := validQuestionRow(fixture, "ce100000-0000-4000-8000-000000000015", 2, "question-key-workspace-guard")
	if err := insertQuestion(ctx, pool, questionWorkspaceGuard); err != nil {
		t.Fatal(err)
	}
	invalidInitialAnswer := validPendingAnswerRow(fixture, fixture.answer2ID, question1.id, fixture.run2ID)
	invalidInitialAnswer.publicationStatus = "completed"
	invalidInitialAnswer.resultType = conversationStringPointer("rag_answer")
	invalidInitialAnswer.modelRunID = &fixture.modelRun1ID
	invalidInitialAnswer.result = []byte(ragAnswerResultV2JSON("citation-1"))
	invalidInitialAnswer.resultHash = stringPointer(strings.Repeat("a", 64))
	invalidInitialAnswer.retrievalSummary = []byte(`{"requested_mode":"hybrid","effective_mode":"hybrid"}`)
	now := time.Now().UTC()
	invalidInitialAnswer.updatedAt = now
	invalidInitialAnswer.publishedAt = &now
	assertPostgresCode(t, insertAnswer(ctx, pool, invalidInitialAnswer), "23514")

	answerCrossWorkspace := validPendingAnswerRow(fixture, "ce100000-0000-4000-8000-000000000014", questionWorkspaceGuard.id, fixture.run2ID)
	answerCrossWorkspace.workspaceID = fixture.otherWorkspaceID
	answerCrossWorkspace.conversationID = "ce100000-0000-4000-8000-000000000004"
	assertPostgresCode(t, insertAnswer(ctx, pool, answerCrossWorkspace), "23503")

	completeRAGModelRun(t, ctx, pool, fixture.modelRun1ID, "ce100000-0000-4000-8000-000000000033", fixture.planStartedAt)
	completedAt := time.Now().UTC().Add(time.Millisecond)
	invalidResultTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, invalidResultErr := invalidResultTx.Exec(ctx, `UPDATE agent.answer SET
		publication_status='completed',result_type='rag_answer',model_run_id=$2,
		result=$3::jsonb,result_hash=repeat('a',64),retrieval_summary='{}'::jsonb,
		version=2,updated_at=$4,published_at=$4 WHERE id=$1`,
		answer1.id, fixture.modelRun1ID,
		`{"result_type":"rag_answer","model_run_ref":"`+fixture.modelRun1ID+`","payload":{}}`, completedAt)
	if rollbackErr := invalidResultTx.Rollback(ctx); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	assertPostgresCode(t, invalidResultErr, "55000")
	if _, err := pool.Exec(ctx, `UPDATE agent.answer SET
		publication_status='completed',
		result_type='rag_answer',
		model_run_id=$2,
		result=$3::jsonb,
		result_hash=repeat('b',64),
		retrieval_summary=$4::jsonb,
		version=2,
		updated_at=$5,
		published_at=$5
		WHERE id=$1`,
		answer1.id, fixture.modelRun1ID, ragAnswerResultV2JSON("citation-1"),
		`{"requested_mode":"hybrid","effective_mode":"hybrid","rewrite_count":1}`, completedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.run SET
		status='succeeded',version=version+1,updated_at=$2,completed_at=$2 WHERE id=$1`,
		fixture.run1ID, completedAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE agent.answer SET result_hash=repeat('c',64),version=4 WHERE id=$1`, answer1.id)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM agent.answer WHERE id=$1`, answer1.id)
	assertPostgresCode(t, err, "55000")

	invalidHelpfulFeedback := validFeedbackRow(fixture, "ce100000-0000-4000-8000-000000000021", answer1.id, "helpful", "feedback-key-1")
	invalidHelpfulFeedback.citationID = conversationStringPointer("citation-1")
	assertPostgresCode(t, insertFeedback(ctx, pool, invalidHelpfulFeedback), "23514")
	missingCitationFeedback := validFeedbackRow(fixture, "ce100000-0000-4000-8000-000000000022", answer1.id, "broken_citation", "feedback-key-2")
	assertPostgresCode(t, insertFeedback(ctx, pool, missingCitationFeedback), "23514")
	feedback := validFeedbackRow(fixture, fixture.feedback1ID, answer1.id, "broken_citation", "feedback-key-3")
	feedback.citationID = conversationStringPointer("citation-1")
	if err := insertFeedback(ctx, pool, feedback); err != nil {
		t.Fatal(err)
	}
	duplicateFeedback := validFeedbackRow(fixture, "ce100000-0000-4000-8000-000000000023", answer1.id, "broken_citation", "feedback-key-3")
	duplicateFeedback.citationID = conversationStringPointer("citation-1")
	assertPostgresCode(t, insertFeedback(ctx, pool, duplicateFeedback), "23505")
	crossWorkspaceFeedback := validFeedbackRow(fixture, "ce100000-0000-4000-8000-000000000024", answer1.id, "incorrect", "feedback-key-4")
	crossWorkspaceFeedback.workspaceID = fixture.otherWorkspaceID
	assertPostgresCode(t, insertFeedback(ctx, pool, crossWorkspaceFeedback), "23503")
	_, err = pool.Exec(ctx, `UPDATE agent.answer_feedback SET comment='mutated' WHERE id=$1`, feedback.id)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM agent.answer_feedback WHERE id=$1`, feedback.id)
	assertPostgresCode(t, err, "55000")

	question2 := validQuestionRow(fixture, fixture.question2ID, 3, "question-key-2")
	if err := insertQuestion(ctx, pool, question2); err != nil {
		t.Fatal(err)
	}
	answer2 := validPendingAnswerRow(fixture, fixture.answer2ID, question2.id, fixture.run2ID)
	if err := insertAnswer(ctx, pool, answer2); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,request_bytes,status,version,started_at
	) VALUES($1,$2,1,'PLAN','openai-compatible','v1','model-test','2026-07-01','default','v1',
		'rag-query-plan','v1','agent.rag-query-plan','v1',128,repeat('d',64),32,'STARTED',1,$3)`,
		fixture.planCallID, fixture.modelRun2ID, fixture.planStartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.model_call SET
			status='SUCCEEDED',response_hash=repeat('1',64),response_bytes=32,input_tokens=4,output_tokens=2,
			latency_ms=5,version=2,completed_at=$2 WHERE id=$1`, fixture.planCallID, fixture.planStartedAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	invalidPhaseTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, invalidPhaseErr := invalidPhaseTx.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,request_bytes,status,version,started_at
	) VALUES($1,$2,2,'REPAIR','openai-compatible','v1','model-test','2026-07-01','default','v1',
		'rag-answer-repair','v1','agent.rag-answer','v2',256,repeat('8',64),64,'STARTED',1,$3)`,
		"ce100000-0000-4000-8000-000000000034", fixture.modelRun2ID, fixture.planStartedAt.Add(2*time.Millisecond))
	if rollbackErr := invalidPhaseTx.Rollback(ctx); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	assertPostgresCode(t, invalidPhaseErr, "55000")
	_, err = pool.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,request_bytes,status,version,started_at
	) VALUES($1,$2,2,'PLAN','openai-compatible','v1','model-test','2026-07-01','default','v1',
		'rag-query-plan','v1','agent.rag-query-plan','v1',128,repeat('e',64),32,'STARTED',1,$3)`,
		"ce100000-0000-4000-8000-000000000031", fixture.modelRun2ID, fixture.planStartedAt.Add(2*time.Millisecond))
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,request_bytes,status,version,started_at
	) VALUES($1,$2,2,'INITIAL','openai-compatible','v1','model-test','2026-07-01','default','v1',
		'rag-answer','v2','agent.rag-answer','v2',256,repeat('f',64),64,'STARTED',1,$3)`,
		fixture.initialCallID, fixture.modelRun2ID, fixture.planStartedAt.Add(3*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.model_call SET
		status='SUCCEEDED',response_hash=repeat('2',64),response_bytes=64,input_tokens=8,output_tokens=6,
		latency_ms=7,version=2,completed_at=$2 WHERE id=$1`, fixture.initialCallID, fixture.planStartedAt.Add(4*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.model_run SET
		status='SUCCEEDED',final_result_type='clarification',version=2,updated_at=$2,completed_at=$2
		WHERE id=$1`, fixture.modelRun2ID, fixture.planStartedAt.Add(5*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.answer SET
		publication_status='clarification_required',
		result_type='clarification',
		model_run_id=$2,
		result=$3::jsonb,
		result_hash=repeat('3',64),
		retrieval_summary=$4::jsonb,
		version=2,
		updated_at=$5,
		published_at=$5
		WHERE id=$1`,
		answer2.id, fixture.modelRun2ID, clarificationResultJSON(),
		`{"requested_mode":"hybrid","effective_mode":"hybrid","rewrite_count":0}`, fixture.planStartedAt.Add(6*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE agent.answer SET publication_status='refused',version=3 WHERE id=$1`, answer2.id)
	assertPostgresCode(t, err, "55000")
	clarificationFeedback := validFeedbackRow(
		fixture, "ce100000-0000-4000-8000-000000000025", answer2.id, "missing_source", "feedback-clarification",
	)
	assertPostgresCode(t, insertFeedback(ctx, pool, clarificationFeedback), "23514")

	if _, err := pool.Exec(ctx, `INSERT INTO workflow.outbox_event(
		id,workspace_id,run_id,event_type,idempotency_key,event_key,schema_version,event_version,payload,occurred_at
	) VALUES(
		$1,$2,$3,'workflow.run.progress','outbox-conversation-1','workflow.run.progress:1',1,1,
		$4::jsonb,$5
	)`, fixture.outboxEventID, fixture.workspaceID, fixture.run1ID,
		`{"question_text":"secret question","answer_text":"secret answer","count":7}`, fixture.outboxOccurredAt); err != nil {
		t.Fatal(err)
	}
	var (
		seq             int64
		workflowRunID   *string
		resourceRef     string
		resourceVersion int64
		summary         string
		sourceRef       string
		expiresAt       time.Time
	)
	if err := pool.QueryRow(ctx, `SELECT seq,workflow_run_id::text,resource_ref,resource_version,payload_summary::text,source_event_ref,expires_at
		FROM ops.server_event WHERE workspace_id=$1 AND source_event_ref=$2`,
		fixture.workspaceID, "workflow.outbox_event:"+fixture.outboxEventID).Scan(
		&seq, &workflowRunID, &resourceRef, &resourceVersion, &summary, &sourceRef, &expiresAt,
	); err != nil {
		t.Fatal(err)
	}
	if seq <= 0 || workflowRunID == nil || *workflowRunID != fixture.run1ID {
		t.Fatalf("projected server event seq=%d workflow_run_id=%v", seq, workflowRunID)
	}
	if resourceRef != "workflow.run:"+fixture.run1ID || resourceVersion != 1 || sourceRef != "workflow.outbox_event:"+fixture.outboxEventID {
		t.Fatalf("projected server event ref=%q version=%d source=%q", resourceRef, resourceVersion, sourceRef)
	}
	if strings.Contains(summary, "secret question") || strings.Contains(summary, "secret answer") || strings.Contains(summary, "question_text") {
		t.Fatalf("projected summary leaked raw payload: %s", summary)
	}
	if !expiresAt.Equal(fixture.outboxOccurredAt.Add(24 * time.Hour)) {
		t.Fatalf("expires_at=%s want=%s", expiresAt, fixture.outboxOccurredAt.Add(24*time.Hour))
	}

	_, err = pool.Exec(ctx, `INSERT INTO ops.server_event(
		workspace_id,conversation_id,event_type,resource_ref,resource_version,payload_summary,schema_version,
		source_event_ref,occurred_at,expires_at
	) VALUES($1,$2,'conversation.updated','conversation:$2',2,'{}'::jsonb,1,$3,$4,$5)`,
		fixture.otherWorkspaceID, fixture.conversationID, "manual:cross-workspace", fixture.outboxOccurredAt, fixture.outboxOccurredAt.Add(24*time.Hour))
	assertPostgresCode(t, err, "23503")
}

type conversationSSEFixture struct {
	workspaceID      string
	otherWorkspaceID string
	conversationID   string
	run1ID           string
	run2ID           string
	modelRun1ID      string
	modelRun2ID      string
	question1ID      string
	question2ID      string
	answer1ID        string
	answer2ID        string
	feedback1ID      string
	planCallID       string
	initialCallID    string
	outboxEventID    string
	planStartedAt    time.Time
	outboxOccurredAt time.Time
}

type conversationInsert struct {
	id, workspaceID, status, idempotencyKey, requestHash string
	title                                                *string
	version                                              int64
	lastActivityAt, createdAt, updatedAt                 time.Time
	archivedAt                                           *time.Time
}

type questionInsert struct {
	id, workspaceID, conversationID, questionText, answerDepth, outputFormat string
	ordinal                                                                  int64
	scope                                                                    []byte
	contextThroughOrdinal                                                    int64
	contextHash, idempotencyKey, requestHash                                 string
	createdAt                                                                time.Time
}

type answerInsert struct {
	id, workspaceID, conversationID, questionID, workflowRunID, publicationStatus string
	modelRunID                                                                    *string
	resultType                                                                    *string
	result                                                                        []byte
	resultHash                                                                    *string
	retrievalSummary                                                              []byte
	version                                                                       int64
	createdAt, updatedAt                                                          time.Time
	publishedAt                                                                   *time.Time
}

type feedbackInsert struct {
	id, workspaceID, answerID, feedbackType, idempotencyKey, requestHash string
	citationID, comment                                                  *string
	createdAt                                                            time.Time
}

func migrateConversationSSETestDatabase(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertConversationSSEMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var tables, meta, activeMeta, activeTrigger, pendingIndex int
	var conversationIndex string
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE (table_schema='agent' AND table_name IN ('conversation','question','answer','answer_feedback'))
		   OR (table_schema='ops' AND table_name='server_event')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='rag_conversation_sse' AND value='m6-04'`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='conversation_active_workflow' AND value='m6-04-t06'`).Scan(&activeMeta); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.triggers
		WHERE event_object_schema='agent' AND event_object_table='answer'
		  AND trigger_name='agent_answer_validate_active_workflow'`).Scan(&activeTrigger); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='agent' AND indexname='uq_agent_answer_conversation_pending'`).Scan(&pendingIndex); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes
		WHERE schemaname='agent' AND indexname='idx_agent_conversation_workspace_activity'`).Scan(&conversationIndex); err != nil {
		t.Fatal(err)
	}
	if tables != 5 || meta != 1 || activeMeta != 1 || activeTrigger != 1 || pendingIndex != 0 ||
		!strings.Contains(conversationIndex, "(workspace_id, last_activity_at DESC, id)") {
		t.Fatalf("tables=%d meta=%d active_meta=%d active_trigger=%d pending_index=%d conversation_index=%q",
			tables, meta, activeMeta, activeTrigger, pendingIndex, conversationIndex)
	}
}

func insertConversationSSEFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) conversationSSEFixture {
	t.Helper()
	fixture := conversationSSEFixture{
		workspaceID:      "ce100000-0000-4000-8000-000000000001",
		otherWorkspaceID: "ce100000-0000-4000-8000-000000000002",
		conversationID:   "ce100000-0000-4000-8000-000000000003",
		run1ID:           "ce100000-0000-4000-8000-000000000004",
		run2ID:           "ce100000-0000-4000-8000-000000000005",
		modelRun1ID:      "ce100000-0000-4000-8000-000000000006",
		modelRun2ID:      "ce100000-0000-4000-8000-000000000007",
		question1ID:      "ce100000-0000-4000-8000-000000000008",
		question2ID:      "ce100000-0000-4000-8000-000000000009",
		answer1ID:        "ce100000-0000-4000-8000-000000000010",
		answer2ID:        "ce100000-0000-4000-8000-000000000011",
		feedback1ID:      "ce100000-0000-4000-8000-000000000020",
		planCallID:       "ce100000-0000-4000-8000-000000000030",
		initialCallID:    "ce100000-0000-4000-8000-000000000032",
		outboxEventID:    "ce100000-0000-4000-8000-000000000040",
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at) VALUES
		($1,'conversation-sse','/tmp/conversation-sse','/tmp/conversation-sse',now(),'active',now(),now()),
		($2,'conversation-sse-other','/tmp/conversation-sse-other','/tmp/conversation-sse-other',now(),'inactive',now(),now())`,
		fixture.workspaceID, fixture.otherWorkspaceID); err != nil {
		t.Fatal(err)
	}
	insertWorkflowFixture(t, ctx, pool, fixture.workspaceID, fixture.run1ID, fixture.modelRun1ID, '1')
	insertWorkflowFixture(t, ctx, pool, fixture.workspaceID, fixture.run2ID, fixture.modelRun2ID, '2')
	if err := insertConversation(ctx, pool, validConversationRow(fixture.workspaceID, fixture.conversationID, "conversation-key")); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&fixture.planStartedAt); err != nil {
		t.Fatal(err)
	}
	fixture.planStartedAt = fixture.planStartedAt.UTC().Add(10 * time.Millisecond)
	fixture.outboxOccurredAt = fixture.planStartedAt.Add(time.Hour)
	return fixture
}

func insertWorkflowFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, runID, modelRunID string, suffix byte) {
	t.Helper()
	definitionID := randomFixtureUUID(runID, '8')
	nodeRunID := randomFixtureUUID(runID, '9')
	nodeAttemptID := randomFixtureUUID(runID, '7')
	indexID := randomFixtureUUID(runID, '6')
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,$3,1,'{"nodes":[]}',now())`, definitionID, workspaceID, "conversation-sse-"+string(suffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		VALUES($1,$2,$3,'running','{}',1,now(),now())`, runID, workspaceID, definitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at
	) VALUES($1,$2,'rag','agent.rag-answer','running','{}','worker-rag',now()+interval '5 minutes',1,now(),now())`,
		nodeRunID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
	) VALUES($1,$2,1,1,0,$3,'worker-rag',now()+interval '5 minutes','running',now())`,
		nodeAttemptID, nodeRunID, "delivery-"+string(suffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
		version,created_at,updated_at
	) VALUES($1,$2,'simple','v1',repeat('1',64),'{}',$3,repeat('2',64),0,$4,'building','["vector"]',1,now(),now())`,
		indexID, workspaceID, "conversation-sse:index:"+string(suffix), "conversation-sse-index-"+string(suffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.model_run(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,
		retrieval_index_version_id,status,version,started_at,updated_at
	) VALUES($1,$2,$3,$4,$5,'openai-compatible','v1','model-test','2026-07-01',
		'default','v1','rag-answer','v2','agent.rag-answer','v2','agent.refusal','v1',$6,'RUNNING',1,now(),now())`,
		modelRunID, workspaceID, runID, nodeRunID, nodeAttemptID, indexID); err != nil {
		t.Fatal(err)
	}
}

func completeRAGModelRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, modelRunID, modelCallID string, startedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,request_bytes,status,version,started_at
	) VALUES($1,$2,1,'INITIAL','openai-compatible','v1','model-test','2026-07-01','default','v1',
		'rag-answer','v2','agent.rag-answer','v2',256,repeat('6',64),64,'STARTED',1,$3)`,
		modelCallID, modelRunID, startedAt); err != nil {
		t.Fatal(err)
	}
	completedAt := startedAt.Add(time.Millisecond)
	if _, err := pool.Exec(ctx, `UPDATE agent.model_call SET
		status='SUCCEEDED',response_hash=repeat('7',64),response_bytes=64,input_tokens=8,output_tokens=6,
		latency_ms=7,version=2,completed_at=$2 WHERE id=$1`, modelCallID, completedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.model_run SET
		status='SUCCEEDED',final_result_type='rag_answer',version=2,updated_at=$2,completed_at=$2
		WHERE id=$1`, modelRunID, completedAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
}

func validConversationRow(workspaceID, conversationID, idempotencyKey string) conversationInsert {
	now := time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)
	return conversationInsert{
		id:             conversationID,
		workspaceID:    workspaceID,
		status:         "open",
		title:          conversationStringPointer("RAG Conversation"),
		version:        1,
		lastActivityAt: now,
		createdAt:      now,
		updatedAt:      now,
		idempotencyKey: idempotencyKey,
		requestHash:    strings.Repeat("a", 64),
	}
}

func validQuestionRow(fixture conversationSSEFixture, questionID string, ordinal int64, idempotencyKey string) questionInsert {
	return questionInsert{
		id:                    questionID,
		workspaceID:           fixture.workspaceID,
		conversationID:        fixture.conversationID,
		ordinal:               ordinal,
		questionText:          "What changed in the approved evidence set?",
		scope:                 []byte(`{"workspace_id":"` + fixture.workspaceID + `","mode":"hybrid"}`),
		answerDepth:           "standard",
		outputFormat:          "markdown",
		contextThroughOrdinal: ordinal - 1,
		contextHash:           strings.Repeat("b", 64),
		idempotencyKey:        idempotencyKey,
		requestHash:           strings.Repeat("c", 64),
		createdAt:             time.Date(2026, 7, 19, 9, int(ordinal), 0, 0, time.UTC),
	}
}

func validPendingAnswerRow(fixture conversationSSEFixture, answerID, questionID, workflowRunID string) answerInsert {
	now := time.Date(2026, 7, 19, 9, 30, 0, 0, time.UTC)
	return answerInsert{
		id:                answerID,
		workspaceID:       fixture.workspaceID,
		conversationID:    fixture.conversationID,
		questionID:        questionID,
		workflowRunID:     workflowRunID,
		publicationStatus: "pending",
		version:           1,
		createdAt:         now,
		updatedAt:         now,
	}
}

func validFeedbackRow(fixture conversationSSEFixture, feedbackID, answerID, feedbackType, idempotencyKey string) feedbackInsert {
	return feedbackInsert{
		id:             feedbackID,
		workspaceID:    fixture.workspaceID,
		answerID:       answerID,
		feedbackType:   feedbackType,
		idempotencyKey: idempotencyKey,
		requestHash:    strings.Repeat("d", 64),
		comment:        conversationStringPointer("feedback comment"),
		createdAt:      time.Date(2026, 7, 19, 9, 45, 0, 0, time.UTC),
	}
}

func insertConversation(ctx context.Context, pool *pgxpool.Pool, row conversationInsert) error {
	_, err := pool.Exec(ctx, `INSERT INTO agent.conversation(
		id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,archived_at,idempotency_key,request_hash
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		row.id, row.workspaceID, row.status, row.title, row.version, row.lastActivityAt, row.createdAt, row.updatedAt,
		row.archivedAt, row.idempotencyKey, row.requestHash)
	return err
}

func insertQuestion(ctx context.Context, pool *pgxpool.Pool, row questionInsert) error {
	_, err := pool.Exec(ctx, `INSERT INTO agent.question(
		id,workspace_id,conversation_id,ordinal,question_text,scope,answer_depth,output_format,
		context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,$12,$13)`,
		row.id, row.workspaceID, row.conversationID, row.ordinal, row.questionText, row.scope, row.answerDepth,
		row.outputFormat, row.contextThroughOrdinal, row.contextHash, row.idempotencyKey, row.requestHash, row.createdAt)
	return err
}

func insertAnswer(ctx context.Context, pool *pgxpool.Pool, row answerInsert) error {
	_, err := pool.Exec(ctx, `INSERT INTO agent.answer(
		id,workspace_id,conversation_id,question_id,workflow_run_id,model_run_id,publication_status,result_type,
		result,result_hash,retrieval_summary,version,created_at,updated_at,published_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11::jsonb,$12,$13,$14,$15)`,
		row.id, row.workspaceID, row.conversationID, row.questionID, row.workflowRunID, row.modelRunID,
		row.publicationStatus, row.resultType, nullableJSON(row.result), row.resultHash, nullableJSON(row.retrievalSummary),
		row.version, row.createdAt, row.updatedAt, row.publishedAt)
	return err
}

func insertFeedback(ctx context.Context, pool *pgxpool.Pool, row feedbackInsert) error {
	_, err := pool.Exec(ctx, `INSERT INTO agent.answer_feedback(
		id,workspace_id,answer_id,feedback_type,citation_id,comment,idempotency_key,request_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		row.id, row.workspaceID, row.answerID, row.feedbackType, row.citationID, row.comment, row.idempotencyKey,
		row.requestHash, row.createdAt)
	return err
}

func ragAnswerResultV2JSON(citationID string) string {
	return `{
		"result_type":"rag_answer",
		"schema_id":"agent.rag-answer",
		"schema_version":"v2",
		"model_run_ref":"ce100000-0000-4000-8000-000000000006",
		"payload":{
			"conclusion":"Approved evidence changed.",
			"assertions":[{"id":"assertion-1","text":"Approved evidence changed.","kind":"FACTUAL","citation_ids":["` + citationID + `"]}],
			"citations":[{"id":"` + citationID + `","workspace_id":"ce100000-0000-4000-8000-000000000001","index_version_id":"6e100000-0000-4000-8000-000000000004","chunk_id":"ce100000-0000-4000-8000-000000000101","source_version_id":"ce100000-0000-4000-8000-000000000102","source_span_id":"ce100000-0000-4000-8000-000000000103"}],
			"conflict_positions":[],
			"conflict_summary":"",
			"related_topics":[{"topic_id":"ce100000-0000-4000-8000-000000000104","name":"Approvals","citation_ids":["` + citationID + `"]}],
			"follow_up_questions":["Which source version changed?"]
		}
	}`
}

func clarificationResultJSON() string {
	return `{
		"result_type":"clarification",
		"schema_id":"conversation.clarification",
		"schema_version":"v1",
		"model_run_ref":"ce100000-0000-4000-8000-000000000007",
		"payload":{
			"reason":"scope_ambiguous",
			"question":"Do you want the latest approved source version or all approved source versions?",
			"suggested_scopes":["latest_approved","all_approved"]
		}
	}`
}

func conversationStringPointer(value string) *string {
	return &value
}
