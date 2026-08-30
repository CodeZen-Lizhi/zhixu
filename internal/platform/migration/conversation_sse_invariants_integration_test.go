//go:build integration

package migration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestConversationSSEMigrationEnforcesActiveAnswerModelRunAndFeedbackBoundaries(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateConversationSSETestDatabase(t, ctx, pool)
	fixture := insertConversationSSEFixture(t, ctx, pool)

	_, err := pool.Exec(ctx, `UPDATE agent.conversation SET title='mutated' WHERE id=$1`, fixture.conversationID)
	assertPostgresCode(t, err, "55000")
	activityAt := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `UPDATE agent.conversation SET
		last_activity_at=$2,updated_at=$2,version=2 WHERE id=$1`, fixture.conversationID, activityAt); err != nil {
		t.Fatalf("valid conversation activity CAS rejected: %v", err)
	}

	question1 := validQuestionRow(fixture, fixture.question1ID, 1, "question-invariant-1")
	if err := insertQuestion(ctx, pool, question1); err != nil {
		t.Fatal(err)
	}
	answer1 := validPendingAnswerRow(fixture, fixture.answer1ID, question1.id, fixture.run1ID)
	if err := insertAnswer(ctx, pool, answer1); err != nil {
		t.Fatal(err)
	}
	pendingFeedback := validFeedbackRow(fixture, fixture.feedback1ID, answer1.id, "helpful", "feedback-pending")
	assertPostgresCode(t, insertFeedback(ctx, pool, pendingFeedback), "23514")

	question2 := validQuestionRow(fixture, fixture.question2ID, 2, "question-invariant-2")
	if err := insertQuestion(ctx, pool, question2); err != nil {
		t.Fatal(err)
	}
	secondPending := validPendingAnswerRow(fixture, fixture.answer2ID, question2.id, fixture.run2ID)
	activeWorkflowErr := insertAnswer(ctx, pool, secondPending)
	assertPostgresCode(t, activeWorkflowErr, "23505")
	var activeWorkflowPGError *pgconn.PgError
	if !errors.As(activeWorkflowErr, &activeWorkflowPGError) || activeWorkflowPGError.ConstraintName != "agent_answer_active_workflow" {
		t.Fatalf("active workflow trigger error = %#v", activeWorkflowErr)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.run SET
		status='failed',version=version+1,updated_at=CURRENT_TIMESTAMP,completed_at=CURRENT_TIMESTAMP
		WHERE id=$1`, fixture.run1ID); err != nil {
		t.Fatal(err)
	}
	if err := insertAnswer(ctx, pool, secondPending); err != nil {
		t.Fatalf("terminal failed workflow should allow the next answer slot: %v", err)
	}

	publishedAt := time.Date(2026, 7, 19, 10, 1, 0, 0, time.UTC)
	_, err = pool.Exec(ctx, `UPDATE agent.answer SET
		publication_status='completed',result_type='rag_answer',model_run_id=$2,
		result=$3::jsonb,result_hash=repeat('a',64),retrieval_summary='{}'::jsonb,
		version=2,updated_at=$4,published_at=$4 WHERE id=$1`,
		answer1.id, fixture.modelRun1ID, ragAnswerResultV2JSON("citation-1"), publishedAt)
	assertPostgresCode(t, err, "55000")

	_, err = pool.Exec(ctx, `INSERT INTO ops.server_event(
		workspace_id,event_type,resource_ref,resource_version,payload_summary,schema_version,occurred_at,expires_at
	) VALUES($1,'answer.pending',$2,1,'{}',1,$3,$4)`,
		fixture.workspaceID, "answer:"+answer1.id, publishedAt, publishedAt.Add(24*time.Hour))
	assertPostgresCode(t, err, "23502")
}

func TestConversationSSEMigrationAllowsNextAnswerAfterCancelledWorkflow(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateConversationSSETestDatabase(t, ctx, pool)
	fixture := insertConversationSSEFixture(t, ctx, pool)

	question1 := validQuestionRow(fixture, fixture.question1ID, 1, "question-cancelled-1")
	if err := insertQuestion(ctx, pool, question1); err != nil {
		t.Fatal(err)
	}
	if err := insertAnswer(ctx, pool, validPendingAnswerRow(fixture, fixture.answer1ID, question1.id, fixture.run1ID)); err != nil {
		t.Fatal(err)
	}
	question2 := validQuestionRow(fixture, fixture.question2ID, 2, "question-cancelled-2")
	if err := insertQuestion(ctx, pool, question2); err != nil {
		t.Fatal(err)
	}
	secondPending := validPendingAnswerRow(fixture, fixture.answer2ID, question2.id, fixture.run2ID)
	assertPostgresCode(t, insertAnswer(ctx, pool, secondPending), "23505")
	if _, err := pool.Exec(ctx, `UPDATE workflow.run SET
		status='cancelled',version=version+1,updated_at=CURRENT_TIMESTAMP,completed_at=CURRENT_TIMESTAMP
		WHERE id=$1`, fixture.run1ID); err != nil {
		t.Fatal(err)
	}
	if err := insertAnswer(ctx, pool, secondPending); err != nil {
		t.Fatalf("terminal cancelled workflow should allow the next answer slot: %v", err)
	}
	var pendingAnswers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.answer
		WHERE conversation_id=$1 AND publication_status='pending'`, fixture.conversationID).Scan(&pendingAnswers); err != nil {
		t.Fatal(err)
	}
	if pendingAnswers != 2 {
		t.Fatalf("pending answer count = %d", pendingAnswers)
	}
}

func TestConversationSSEMigrationRejectsAnswerForArchivedConversationWithStableConstraint(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateConversationSSETestDatabase(t, ctx, pool)
	fixture := insertConversationSSEFixture(t, ctx, pool)
	question := validQuestionRow(fixture, fixture.question1ID, 1, "question-archived-answer")
	if err := insertQuestion(ctx, pool, question); err != nil {
		t.Fatal(err)
	}
	archivedAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `UPDATE agent.conversation SET
		status='archived',archived_at=$2,last_activity_at=$2,updated_at=$2,version=version+1
		WHERE id=$1`, fixture.conversationID, archivedAt); err != nil {
		t.Fatal(err)
	}
	err := insertAnswer(ctx, pool, validPendingAnswerRow(fixture, fixture.answer1ID, question.id, fixture.run1ID))
	assertPostgresCode(t, err, "23514")
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.ConstraintName != "agent_answer_conversation_open" {
		t.Fatalf("archived conversation trigger error = %#v", err)
	}
}
