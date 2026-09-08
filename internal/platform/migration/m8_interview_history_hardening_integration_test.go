//go:build integration

package migration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memorypostgres "github.com/CodeZen-Lizhi/zhixu/internal/memory/adapter/postgres"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	interviewpostgres "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/postgres"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM8InterviewHistoryHardeningMigrationSupportsRepeatedUp(t *testing.T) {
	ctx := t.Context()
	pool := newM8HistoryPlatform(t).DB()
	if err := platformmigration.MigrateAtlasToVersion(ctx, pool, 50); err != nil {
		t.Fatalf("00050 up: %v", err)
	}
	if err := platformmigration.MigrateAtlasToVersion(ctx, pool, 50); err != nil {
		t.Fatalf("00050 repeated up: %v", err)
	}
	var version int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(max(version),'0')::bigint
		FROM atlas_schema_revisions.atlas_schema_revisions`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 50 {
		t.Fatalf("migration version=%d want=50", version)
	}
	assertM8HistoryHardeningShape(t, ctx, pool)
	if err := platformmigration.MigrateAtlasToVersion(ctx, pool, 50); err != nil {
		t.Fatalf("00050 re-up: %v", err)
	}
	assertM8HistoryHardeningShape(t, ctx, pool)
}

func TestM8InterviewHistoryHardeningRejectsMutationAndPreservesExactReplay(t *testing.T) {
	ctx := t.Context()
	platform := newM8HistoryPlatform(t)
	pool := platform.DB()
	if err := platformmigration.MigrateAtlasToVersion(ctx, pool, 50); err != nil {
		t.Fatalf("00050 up: %v", err)
	}
	fixture := seedM8RetainedHistory(t, ctx, pool)

	for _, mutation := range []struct {
		name string
		sql  string
		args []any
	}{
		{name: "update memory command", sql: `UPDATE learning.memory_command SET response='{}'::jsonb WHERE workspace_id=$1 AND idempotency_key=$2`, args: []any{fixture.workspaceID, fixture.memoryKey}},
		{name: "delete memory command", sql: `DELETE FROM learning.memory_command WHERE workspace_id=$1 AND idempotency_key=$2`, args: []any{fixture.workspaceID, fixture.memoryKey}},
		{name: "update interview command", sql: `UPDATE learning.interview_command SET response='{}'::jsonb WHERE workspace_id=$1 AND idempotency_key=$2`, args: []any{fixture.workspaceID, fixture.interviewKey}},
		{name: "delete interview command", sql: `DELETE FROM learning.interview_command WHERE workspace_id=$1 AND idempotency_key=$2`, args: []any{fixture.workspaceID, fixture.interviewKey}},
		{name: "update interview turn", sql: `UPDATE learning.interview_turn SET user_answer='rewritten' WHERE workspace_id=$1 AND id=$2`, args: []any{fixture.workspaceID, fixture.turnID}},
		{name: "delete interview turn", sql: `DELETE FROM learning.interview_turn WHERE workspace_id=$1 AND id=$2`, args: []any{fixture.workspaceID, fixture.turnID}},
		{name: "rewrite pending question prompt", sql: `UPDATE learning.interview_question SET prompt='rewritten' WHERE workspace_id=$1 AND id=$2`, args: []any{fixture.workspaceID, fixture.answeredQuestionID}},
		{name: "answer without timestamp", sql: `UPDATE learning.interview_question SET status='ANSWERED' WHERE workspace_id=$1 AND id=$2`, args: []any{fixture.workspaceID, fixture.answeredQuestionID}},
		{name: "delete pending question", sql: `DELETE FROM learning.interview_question WHERE workspace_id=$1 AND id=$2`, args: []any{fixture.workspaceID, fixture.skippedQuestionID}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, mutation.sql, mutation.args...)
			assertPostgresCode(t, err, "55000")
		})
	}

	answeredAt := fixture.startedAt.Add(time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE learning.interview_question
		SET status='ANSWERED',answered_at=$3
		WHERE workspace_id=$1 AND id=$2`, fixture.workspaceID, fixture.answeredQuestionID, answeredAt); err != nil {
		t.Fatalf("legal PENDING to ANSWERED: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.interview_question
		SET answered_at=$3
		WHERE workspace_id=$1 AND id=$2`, fixture.workspaceID, fixture.answeredQuestionID, answeredAt.Add(time.Second)); err == nil {
		t.Fatal("terminal ANSWERED question accepted a second update")
	} else {
		assertPostgresCode(t, err, "55000")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM learning.interview_question WHERE workspace_id=$1 AND id=$2`, fixture.workspaceID, fixture.answeredQuestionID); err == nil {
		t.Fatal("terminal ANSWERED question was deleted")
	} else {
		assertPostgresCode(t, err, "55000")
	}

	if _, err := pool.Exec(ctx, `UPDATE learning.interview_question
		SET status='SKIPPED'
		WHERE workspace_id=$1 AND id=$2`, fixture.workspaceID, fixture.skippedQuestionID); err != nil {
		t.Fatalf("legal PENDING to SKIPPED: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.interview_question
		SET status='ANSWERED',answered_at=$3
		WHERE workspace_id=$1 AND id=$2`, fixture.workspaceID, fixture.skippedQuestionID, answeredAt); err == nil {
		t.Fatal("terminal SKIPPED question accepted a second transition")
	} else {
		assertPostgresCode(t, err, "55000")
	}

	database, err := platform.GORM()
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork, err := platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	memoryRepository, err := memorypostgres.NewGORMRepository(database, unitOfWork)
	if err != nil {
		t.Fatal(err)
	}
	memoryReplay, found, err := memoryRepository.FindCommand(ctx, fixture.memoryBinding)
	if err != nil || !found || !memoryReplay.Replayed || memoryReplay.Memory.ID != fixture.memoryID || string(memoryReplay.Memory.Content) != `{"style":"concise"}` {
		t.Fatalf("memory exact replay=%+v found=%t err=%v", memoryReplay, found, err)
	}
	interviewRepository, err := interviewpostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	interviewReplay, found, err := interviewRepository.FindStartReplay(ctx, fixture.workspaceID, fixture.interviewKey, fixture.interviewHash)
	if err != nil || !found || !interviewReplay.Replayed || interviewReplay.Session.ID != fixture.sessionID || len(interviewReplay.Questions) != 2 || interviewReplay.Questions[0].Status != interviewdomain.QuestionStatusPending {
		t.Fatalf("interview exact replay=%+v found=%t err=%v", interviewReplay, found, err)
	}
}

func newM8HistoryPlatform(t *testing.T) *platformpostgres.Pool {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{
		MaxConns:     16,
		Availability: testdb.FailWhenUnavailable,
		Migrate: func(ctx context.Context, pool *pgxpool.Pool) error {
			return platformmigration.MigrateAtlasToVersion(ctx, pool, 49)
		},
	})
	return fixture.Pool()
}

type m8RetainedHistoryFixture struct {
	workspaceID        foundation.ID
	memoryID           foundation.ID
	memoryKey          string
	memoryBinding      memoryapp.CommandBinding
	sessionID          foundation.ID
	interviewKey       string
	interviewHash      string
	answeredQuestionID foundation.ID
	skippedQuestionID  foundation.ID
	turnID             foundation.ID
	startedAt          time.Time
}

func seedM8RetainedHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool) m8RetainedHistoryFixture {
	t.Helper()
	workspaceID := m8HistoryID(1)
	memoryID := m8HistoryID(2)
	claimID := m8HistoryID(3)
	sessionID := m8HistoryID(4)
	answeredQuestionID := m8HistoryID(5)
	skippedQuestionID := m8HistoryID(6)
	turnID := m8HistoryID(7)
	startedAt := time.Date(2026, 7, 27, 17, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'m8-history','/tmp/m8-history','/tmp/m8-history',$2,'test',1,$2,$2)`, string(workspaceID), startedAt); err != nil {
		t.Fatal(err)
	}

	owner := memorydomain.SingleUserOwner()
	memory, err := memorydomain.NewCandidate(memorydomain.CandidateInput{
		ID: memoryID, WorkspaceID: workspaceID, Owner: owner, Type: memorydomain.TypePreference,
		Content: json.RawMessage(`{"style":"concise"}`), Source: memorydomain.Source{Type: memorydomain.SourceUser, Ref: "user:settings"}, CreatedAt: startedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryResponse, err := json.Marshal(memory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.memory(
		id,workspace_id,owner_principal_kind,owner_principal_id,memory_type,content,source_type,source_ref,
		status,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,$11)`,
		string(memory.ID), string(memory.WorkspaceID), string(memory.Owner.Kind), string(memory.Owner.ID), string(memory.Type), memory.Content,
		string(memory.Source.Type), memory.Source.Ref, string(memory.Status), memory.Version, memory.CreatedAt); err != nil {
		t.Fatal(err)
	}
	const memoryKey = "m8-history-memory"
	memoryBinding := memoryapp.CommandBinding{
		WorkspaceID: workspaceID, Owner: owner, MemoryID: memoryID, IdempotencyKey: memoryKey,
		RequestHash: strings.Repeat("1", 64), CommandType: memoryapp.CommandCreateCandidate,
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.memory_command(
		workspace_id,idempotency_key,owner_principal_kind,owner_principal_id,request_hash,command_type,
		memory_id,expected_version,memory_version,response,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,0,1,$8::jsonb,$9)`, string(workspaceID), memoryKey, string(owner.Kind), string(owner.ID),
		memoryBinding.RequestHash, string(memoryBinding.CommandType), string(memoryID), memoryResponse, startedAt); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO core.claim(
		id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,
		status,confidence_factors,fingerprint,version,created_at,updated_at
	) VALUES($1,$2,'Retained interview facts are immutable.','retained interview facts are immutable.','{}',
		'knowledge-applicability/v1',repeat('2',64),'SUGGESTED','{}',repeat('3',64),1,$3,$3)`, string(claimID), string(workspaceID), startedAt); err != nil {
		t.Fatal(err)
	}
	config := interviewdomain.Config{
		SchemaVersion: interviewdomain.SchemaVersion, Role: "backend engineer", Scope: interviewdomain.Scope{ClaimIDs: []foundation.ID{claimID}},
		Difficulty: interviewdomain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 2,
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	const interviewKey = "m8-history-interview"
	const interviewHash = "4444444444444444444444444444444444444444444444444444444444444444"
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_session(
		id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at
	) VALUES($1,$2,NULL,'INTERVIEW','ACTIVE',$3::jsonb,$4,$5,$6)`, string(sessionID), string(workspaceID), configJSON, interviewKey, interviewHash, startedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_session(
		session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at
	) VALUES($1,$2,$3,1,0,$4,$4)`, string(sessionID), string(workspaceID), interviewdomain.SchemaVersion, startedAt); err != nil {
		t.Fatal(err)
	}
	evidence := []interviewdomain.EvidenceRef{{
		SchemaVersion: interviewdomain.EvidenceSchemaVersion, ClaimID: claimID, IndexVersionID: m8HistoryID(10),
		ChunkID: m8HistoryID(11), SourceVersionID: m8HistoryID(8), SourceSpanID: m8HistoryID(9),
		EvidenceHash: strings.Repeat("5", 64), SupportType: "SUPPORTS",
	}}
	material := interviewdomain.Material{
		ClaimID: claimID, ClaimStatus: "CONFIRMED", Statement: "Retained interview facts are immutable.",
		AnswerPoints: []string{"Receipts and turns are append-only."}, Evidence: evidence,
	}
	questions := make([]interviewdomain.Question, 0, 2)
	for index, questionID := range []foundation.ID{answeredQuestionID, skippedQuestionID} {
		fingerprint, err := interviewdomain.ComputeQuestionFingerprint(config, material, index+1, 0)
		if err != nil {
			t.Fatal(err)
		}
		question := interviewdomain.Question{
			ID: questionID, WorkspaceID: workspaceID, SessionID: sessionID, QuestionNo: index + 1, ClaimID: claimID,
			Prompt: "Explain append-only retained history.", AnswerPoints: append([]string(nil), material.AnswerPoints...),
			Evidence: append([]interviewdomain.EvidenceRef(nil), evidence...), Status: interviewdomain.QuestionStatusPending,
			Fingerprint: fingerprint, CreatedAt: startedAt,
		}
		pointsJSON, _ := json.Marshal(question.AnswerPoints)
		evidenceJSON, _ := json.Marshal(question.Evidence)
		if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_question(
			id,workspace_id,session_id,question_no,follow_up_no,parent_question_id,claim_id,topic_id,prompt,
			answer_points,evidence,status,fingerprint,created_at,answered_at
		) VALUES($1,$2,$3,$4,0,NULL,$5,NULL,$6,$7::jsonb,$8::jsonb,'PENDING',$9,$10,NULL)`,
			string(question.ID), string(workspaceID), string(sessionID), question.QuestionNo, string(claimID), question.Prompt,
			pointsJSON, evidenceJSON, question.Fingerprint, startedAt); err != nil {
			t.Fatal(err)
		}
		questions = append(questions, question)
	}
	session := interviewdomain.Session{
		ID: sessionID, WorkspaceID: workspaceID, Config: config, Status: interviewdomain.SessionStatusActive, Version: 1, StartedAt: startedAt,
	}
	startResponse, err := json.Marshal(interviewapp.StartResult{Session: session, Questions: questions})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_command(
		workspace_id,idempotency_key,request_hash,command_type,session_id,response,created_at
	) VALUES($1,$2,$3,'START',$4,$5::jsonb,$6)`, string(workspaceID), interviewKey, interviewHash, string(sessionID), startResponse, startedAt); err != nil {
		t.Fatal(err)
	}
	score := interviewdomain.Score{
		SchemaVersion: interviewdomain.ScoreSchemaVersion,
		Correctness:   interviewdomain.ScoreDimension{Value: 1, Rationale: "complete"}, Coverage: interviewdomain.ScoreDimension{Value: 1, Rationale: "complete"},
		Boundaries: interviewdomain.ScoreDimension{Value: 1, Rationale: "complete"}, Clarity: interviewdomain.ScoreDimension{Value: 1, Rationale: "complete"},
		Evidence: evidence,
	}
	scoreJSON, _ := json.Marshal(score)
	decisionJSON, _ := json.Marshal(interviewdomain.TurnDecision{NextQuestionID: &skippedQuestionID})
	if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_turn(
		id,workspace_id,session_id,question_id,idempotency_key,request_hash,user_answer,score,decision,scorer_version,created_at
	) VALUES($1,$2,$3,$4,'m8-history-turn',repeat('6',64),'original answer',$5::jsonb,$6::jsonb,'deterministic/v1',$7)`,
		string(turnID), string(workspaceID), string(sessionID), string(answeredQuestionID), scoreJSON, decisionJSON, startedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return m8RetainedHistoryFixture{
		workspaceID: workspaceID, memoryID: memoryID, memoryKey: memoryKey, memoryBinding: memoryBinding,
		sessionID: sessionID, interviewKey: interviewKey, interviewHash: interviewHash,
		answeredQuestionID: answeredQuestionID, skippedQuestionID: skippedQuestionID, turnID: turnID, startedAt: startedAt,
	}
}

func assertM8HistoryHardeningShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var triggers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE NOT tgisinternal AND tgname = ANY($1::text[])`, []string{
		"trg_learning_memory_command_append_only",
		"trg_learning_interview_command_append_only",
		"trg_learning_interview_turn_append_only",
		"trg_learning_interview_question_history",
	}).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 4 {
		t.Fatalf("M8 history trigger count=%d", triggers)
	}
	var indexExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('learning.idx_learning_review_session_interview_started') IS NOT NULL`).Scan(&indexExists); err != nil {
		t.Fatal(err)
	}
	if !indexExists {
		t.Fatal("Interview Session list index is missing")
	}
}

func m8HistoryID(value int) foundation.ID {
	parsed, err := foundation.ParseID(fmt.Sprintf("8a000000-0000-4000-8000-%012x", value))
	if err != nil {
		panic(err)
	}
	return parsed
}
