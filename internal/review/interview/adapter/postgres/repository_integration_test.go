//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQuestionSourceSelectsTopicScopedConfirmedEvidenceOnlyFromActiveIndex(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newInterviewTestDatabase(t, ctx)
	defer cleanup()
	fixture := seedInterviewQuestionSourceFixture(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}

	selection := interviewapp.Selection{
		WorkspaceID: fixture.workspaceID,
		Scope:       domain.Scope{TopicIDs: []foundation.ID{fixture.topicID}},
		Limit:       2,
	}
	materials, err := repository.Select(ctx, selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(materials) != 0 {
		t.Fatalf("materials without an active index = %+v, want none", materials)
	}

	activateInterviewQuestionSourceIndex(t, ctx, pool, fixture)
	materials, err = repository.Select(ctx, selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(materials) != 1 {
		t.Fatalf("topic-scoped materials = %+v, want exactly one", materials)
	}
	material := materials[0]
	if material.ClaimID != fixture.claimID || material.TopicID == nil || *material.TopicID != fixture.topicID || len(material.Evidence) != 1 {
		t.Fatalf("topic-scoped material = %+v", material)
	}
	evidence := material.Evidence[0]
	if evidence.ClaimID != fixture.claimID || evidence.IndexVersionID != fixture.indexID || evidence.ChunkID != fixture.chunkID || evidence.SourceVersionID != fixture.sourceVersionID || evidence.SourceSpanID != fixture.sourceSpanID || evidence.SupportType != "SUPPORTS" {
		t.Fatalf("frozen active-index evidence = %+v", evidence)
	}

	nonMatching, err := repository.Select(ctx, interviewapp.Selection{
		WorkspaceID: fixture.workspaceID,
		Scope:       domain.Scope{TopicIDs: []foundation.ID{fixture.otherTopicID}},
		Limit:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nonMatching) != 0 {
		t.Fatalf("non-matching topic selected material = %+v", nonMatching)
	}
	crossWorkspace, err := repository.Select(ctx, interviewapp.Selection{
		WorkspaceID: interviewIntegrationID(199),
		Scope:       domain.Scope{TopicIDs: []foundation.ID{fixture.topicID}},
		Limit:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(crossWorkspace) != 0 {
		t.Fatalf("cross-workspace selection = %+v", crossWorkspace)
	}

	_, err = pool.Exec(ctx, `INSERT INTO core.relation(
		id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,
		relation_type,status,fingerprint,version,created_at,updated_at
	) VALUES($1,$2,'TOPIC',$3,'CLAIM',$4,'BELONGS_TO','SUGGESTED',repeat('e',64),1,$5,$5)`,
		string(interviewIntegrationID(140)), string(fixture.workspaceID), string(fixture.topicID), string(fixture.claimID), fixture.now)
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("reverse TOPIC -> CLAIM BELONGS_TO error = %v, want constraint violation", err)
	}
}

func TestInterviewRepositoryPersistsTurnsReportsPathsAndNeverWritesFSRS(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newInterviewTestDatabase(t, ctx)
	defer cleanup()
	seedInterviewPersistence(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	workspaceID := interviewIntegrationID(1)
	claimID := interviewIntegrationID(2)
	config := domain.Config{SchemaVersion: domain.SchemaVersion, Role: "backend engineer", Scope: domain.Scope{ClaimIDs: []foundation.ID{claimID}}, Difficulty: domain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 1, MaxFollowUps: 0}
	material := domain.Material{ClaimID: claimID, ClaimStatus: "CONFIRMED", Statement: "An interview turn must not update FSRS.", AnswerPoints: []string{"An interview turn must not update FSRS."}, Evidence: []domain.EvidenceRef{{SchemaVersion: domain.EvidenceSchemaVersion, ClaimID: claimID, IndexVersionID: interviewIntegrationID(32), ChunkID: interviewIntegrationID(33), SourceVersionID: interviewIntegrationID(30), SourceSpanID: interviewIntegrationID(31), EvidenceHash: strings.Repeat("a", 64), SupportType: "SUPPORTS"}}}
	fingerprint, err := domain.ComputeQuestionFingerprint(config, material, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	session := domain.Session{ID: interviewIntegrationID(10), WorkspaceID: workspaceID, Config: config, Status: domain.SessionStatusActive, Version: 1, StartedAt: now}
	question := domain.Question{ID: interviewIntegrationID(11), WorkspaceID: workspaceID, SessionID: session.ID, QuestionNo: 1, ClaimID: claimID, Prompt: "Explain why an interview turn must not update FSRS.", AnswerPoints: material.AnswerPoints, Evidence: material.Evidence, Status: domain.QuestionStatusPending, Fingerprint: fingerprint, CreatedAt: now}
	started, err := repository.Start(ctx, interviewapp.StartRecord{Session: session, Questions: []domain.Question{question}, IdempotencyKey: "interview-start-integration", RequestHash: strings.Repeat("b", 64)})
	if err != nil || started.Replayed || started.Session.ID != session.ID {
		t.Fatalf("start=%+v err=%v", started, err)
	}
	forgedEvidence := append([]domain.EvidenceRef(nil), question.Evidence...)
	forgedEvidence[0].ChunkID = interviewIntegrationID(199)
	forgedEvidenceJSON, err := json.Marshal(forgedEvidence)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO learning.interview_question(
		id,workspace_id,session_id,question_no,follow_up_no,parent_question_id,claim_id,topic_id,
		prompt,answer_points,evidence,status,fingerprint,created_at,answered_at
	) VALUES($1,$2,$3,2,0,NULL,$4,NULL,'Forged provenance','["invalid"]',$5::jsonb,
		'PENDING',repeat('f',64),$6,NULL)`, string(interviewIntegrationID(19)), string(workspaceID), string(session.ID), string(claimID), forgedEvidenceJSON, now)
	assertInterviewPostgresCode(t, err, "23514")

	_, err = pool.Exec(ctx, `UPDATE learning.review_session
		SET session_type='REVIEW',deck_id=NULL,config='{}'::jsonb
		WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(session.ID))
	assertInterviewPostgresCode(t, err, "23514")

	_, err = pool.Exec(ctx, `INSERT INTO learning.review_answer(
		id,workspace_id,session_id,card_id,question_ref,idempotency_key,user_answer,scorer_version,
		score,feedback,rating,schedule_snapshot,request_hash,created_at
	) VALUES($1,$2,$3,NULL,'interview-forged-review-answer','interview-forged-review-answer','answer',
		'review-scorer/v1','{}','{}',3,'{}',repeat('a',64),$4)`,
		string(interviewIntegrationID(18)), string(workspaceID), string(session.ID), now)
	assertInterviewPostgresCode(t, err, "23514")

	startReplay, err := repository.Start(ctx, interviewapp.StartRecord{Session: session, Questions: []domain.Question{question}, IdempotencyKey: "interview-start-integration", RequestHash: strings.Repeat("b", 64)})
	if err != nil || !startReplay.Replayed || startReplay.Session.ID != session.ID {
		t.Fatalf("start replay=%+v err=%v", startReplay, err)
	}
	score := integrationScore(question.Evidence)
	turn := domain.Turn{ID: interviewIntegrationID(12), WorkspaceID: workspaceID, SessionID: session.ID, QuestionID: question.ID, IdempotencyKey: "interview-submit-integration", RequestHash: strings.Repeat("c", 64), UserAnswer: "Interview turns remain independent from spaced-repetition scheduling.", Score: score, Decision: domain.TurnDecision{}, ScorerVersion: "interview-deterministic/v1", CreatedAt: now.Add(time.Minute)}
	submitted, err := repository.Submit(ctx, interviewapp.SubmitRecord{WorkspaceID: workspaceID, SessionID: session.ID, ExpectedVersion: 1, Question: question, Turn: turn, IdempotencyKey: turn.IdempotencyKey, RequestHash: turn.RequestHash})
	if err != nil || submitted.Replayed || submitted.Turn.ID != turn.ID {
		t.Fatalf("submit=%+v err=%v", submitted, err)
	}
	submitReplay, err := repository.Submit(ctx, interviewapp.SubmitRecord{WorkspaceID: workspaceID, SessionID: session.ID, ExpectedVersion: 1, Question: question, Turn: turn, IdempotencyKey: turn.IdempotencyKey, RequestHash: turn.RequestHash})
	if err != nil || !submitReplay.Replayed || submitReplay.Turn.ID != turn.ID {
		t.Fatalf("submit replay=%+v err=%v", submitReplay, err)
	}
	reportBinding := domain.ArtifactBinding{Kind: interviewapp.ArtifactKindInterviewDocument, ArtifactID: interviewIntegrationID(40), RevisionID: interviewIntegrationID(41), ArtifactVersion: 1}
	pathBinding := domain.ArtifactBinding{Kind: interviewapp.ArtifactKindLearningPath, ArtifactID: interviewIntegrationID(42), RevisionID: interviewIntegrationID(43), ArtifactVersion: 1}
	report := domain.Report{ID: interviewIntegrationID(13), WorkspaceID: workspaceID, SessionID: session.ID, SchemaVersion: domain.ReportSchemaVersion, Summary: domain.ReportSummary{QuestionsTotal: 1, AnsweredTotal: 1, Correctness: 1, Coverage: 1, Boundaries: 1, Clarity: 1}, Evidence: question.Evidence, Artifact: reportBinding, CreatedAt: now.Add(2 * time.Minute)}
	path := domain.LearningPath{ID: interviewIntegrationID(14), WorkspaceID: workspaceID, SessionID: session.ID, ReportID: report.ID, Artifact: pathBinding, Status: domain.PathStatusActive, Version: 1, CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(2 * time.Minute)}
	attemptDigest := strings.Repeat("e", 64)
	completeRecord := interviewapp.CompleteRecord{WorkspaceID: workspaceID, SessionID: session.ID, ExpectedVersion: 2, ArtifactDigest: attemptDigest, Report: report, Path: path, IdempotencyKey: "interview-complete-integration", RequestHash: strings.Repeat("d", 64), At: now.Add(2 * time.Minute)}
	begin, err := repository.BeginComplete(ctx, interviewapp.BeginCompleteRecord{
		WorkspaceID: workspaceID, SessionID: session.ID, IdempotencyKey: completeRecord.IdempotencyKey,
		RequestHash: completeRecord.RequestHash,
	})
	if err != nil || begin.Terminal != nil || begin.Reservation.SnapshotVersion != completeRecord.ExpectedVersion {
		t.Fatalf("begin complete=%+v err=%v", begin, err)
	}
	prepared, err := repository.PrepareComplete(ctx, interviewapp.PrepareCompleteRecord{
		WorkspaceID: workspaceID, SessionID: session.ID, IdempotencyKey: completeRecord.IdempotencyKey,
		RequestHash: completeRecord.RequestHash, SnapshotVersion: completeRecord.ExpectedVersion, ArtifactDigest: attemptDigest,
	})
	if err != nil || prepared.Terminal != nil || prepared.Reservation.ArtifactDigest != attemptDigest {
		t.Fatalf("prepare complete=%+v err=%v", prepared, err)
	}
	seedInterviewCompletionVisibilityHold(t, ctx, pool, workspaceID, session.ID, reportBinding.ArtifactID, "REPORT", attemptDigest, now.Add(2*time.Minute))
	if _, err := repository.Complete(ctx, completeRecord); err == nil {
		t.Fatal("completion with a missing PATH visibility hold must fail")
	}
	if count := interviewRowCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_visibility_hold WHERE workspace_id=$1 AND owner_id=$2`, string(workspaceID), string(session.ID)); count != 1 {
		t.Fatalf("failed completion visibility hold count=%d want=1", count)
	}
	if count := interviewRowCount(t, ctx, pool, `SELECT count(*) FROM learning.interview_report WHERE workspace_id=$1 AND session_id=$2`, string(workspaceID), string(session.ID)); count != 0 {
		t.Fatalf("missing-hold completion report count=%d want=0", count)
	}
	if count := interviewRowCount(t, ctx, pool, `SELECT count(*) FROM learning.interview_learning_path WHERE workspace_id=$1 AND session_id=$2`, string(workspaceID), string(session.ID)); count != 0 {
		t.Fatalf("missing-hold completion path count=%d want=0", count)
	}
	failedSnapshot, err := repository.Get(ctx, workspaceID, session.ID)
	if err != nil || failedSnapshot.Session.Status != domain.SessionStatusActive || failedSnapshot.Session.Version != 2 || failedSnapshot.Report != nil || failedSnapshot.Path != nil {
		t.Fatalf("missing-hold completion committed partial state: snapshot=%+v err=%v", failedSnapshot, err)
	}
	seedInterviewCompletionVisibilityHold(t, ctx, pool, workspaceID, session.ID, pathBinding.ArtifactID, "PATH", attemptDigest, now.Add(2*time.Minute))
	completed, err := repository.Complete(ctx, completeRecord)
	if err != nil || completed.Replayed || completed.Report.Artifact != reportBinding || completed.Path.Artifact != pathBinding {
		t.Fatalf("complete=%+v err=%v", completed, err)
	}
	if count := interviewRowCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_visibility_hold WHERE workspace_id=$1 AND owner_id=$2`, string(workspaceID), string(session.ID)); count != 0 {
		t.Fatalf("completion visibility hold count=%d want=0", count)
	}
	completeReplay, err := repository.Complete(ctx, completeRecord)
	if err != nil || !completeReplay.Replayed || completeReplay.Report.ID != report.ID || completeReplay.Path.Artifact != pathBinding {
		t.Fatalf("complete replay=%+v err=%v", completeReplay, err)
	}
	persisted, err := repository.Get(ctx, workspaceID, session.ID)
	if err != nil || persisted.Report == nil || persisted.Path == nil || persisted.Session.Status != domain.SessionStatusCompleted || persisted.Report.Artifact != reportBinding || persisted.Path.Artifact != pathBinding {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	var reviewAnswerCount, scheduleCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.review_answer WHERE workspace_id=$1`, string(workspaceID)).Scan(&reviewAnswerCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.review_schedule WHERE workspace_id=$1`, string(workspaceID)).Scan(&scheduleCount); err != nil {
		t.Fatal(err)
	}
	if reviewAnswerCount != 0 || scheduleCount != 0 {
		t.Fatalf("interview wrote FSRS facts: review_answers=%d schedules=%d", reviewAnswerCount, scheduleCount)
	}
	assertInterviewBindingConstraints(t, ctx, pool, now)
	assertInterviewPathCompletionInvariant(t, ctx, repository, now)
}

func assertInterviewPostgresCode(t *testing.T, err error, want string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != want {
		t.Fatalf("PostgreSQL error=%v code=%q want=%q", err, pgErrCode(pgErr), want)
	}
}

func pgErrCode(err *pgconn.PgError) string {
	if err == nil {
		return ""
	}
	return err.Code
}

func TestInterviewRepositoryListsWorkspaceSessionsWithStableKeyset(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newInterviewTestDatabase(t, ctx)
	defer cleanup()
	seedInterviewPersistence(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := interviewIntegrationID(1)
	startedAt := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	for _, fixture := range []struct {
		sessionID  foundation.ID
		questionID foundation.ID
		startedAt  time.Time
		key        string
	}{
		{sessionID: interviewIntegrationID(60), questionID: interviewIntegrationID(70), startedAt: startedAt.Add(-time.Minute), key: "interview-list-old"},
		{sessionID: interviewIntegrationID(61), questionID: interviewIntegrationID(71), startedAt: startedAt, key: "interview-list-tie-low"},
		{sessionID: interviewIntegrationID(62), questionID: interviewIntegrationID(72), startedAt: startedAt, key: "interview-list-tie-high"},
	} {
		record := interviewListStartRecord(t, workspaceID, fixture.sessionID, fixture.questionID, fixture.startedAt, fixture.key)
		if _, err := repository.Start(ctx, record); err != nil {
			t.Fatalf("start %s: %v", fixture.sessionID, err)
		}
	}

	first, err := repository.List(ctx, interviewapp.SessionListQuery{WorkspaceID: workspaceID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != interviewIntegrationID(62) || first.Items[1].ID != interviewIntegrationID(61) || first.Next == nil || first.Next.ID != interviewIntegrationID(61) {
		t.Fatalf("first page=%+v", first)
	}
	second, err := repository.List(ctx, interviewapp.SessionListQuery{WorkspaceID: workspaceID, Limit: 2, After: first.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != interviewIntegrationID(60) || second.Next != nil {
		t.Fatalf("second page=%+v", second)
	}
	empty, err := repository.List(ctx, interviewapp.SessionListQuery{WorkspaceID: interviewIntegrationID(99), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Items) != 0 || empty.Next != nil {
		t.Fatalf("cross-workspace page=%+v", empty)
	}
}

func interviewListStartRecord(t *testing.T, workspaceID, sessionID, questionID foundation.ID, startedAt time.Time, key string) interviewapp.StartRecord {
	t.Helper()
	claimID := interviewIntegrationID(2)
	config := domain.Config{
		SchemaVersion: domain.SchemaVersion, Role: "backend engineer", Scope: domain.Scope{ClaimIDs: []foundation.ID{claimID}},
		Difficulty: domain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 1,
	}
	material := domain.Material{
		ClaimID: claimID, ClaimStatus: "CONFIRMED", Statement: "Interview session lists use stable keysets.",
		AnswerPoints: []string{"Interview session lists use stable keysets."},
		Evidence: []domain.EvidenceRef{{
			SchemaVersion: domain.EvidenceSchemaVersion, ClaimID: claimID, IndexVersionID: interviewIntegrationID(32),
			ChunkID: interviewIntegrationID(33), SourceVersionID: interviewIntegrationID(30), SourceSpanID: interviewIntegrationID(31),
			EvidenceHash: strings.Repeat("a", 64), SupportType: "SUPPORTS",
		}},
	}
	fingerprint, err := domain.ComputeQuestionFingerprint(config, material, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	session := domain.Session{ID: sessionID, WorkspaceID: workspaceID, Config: config, Status: domain.SessionStatusActive, Version: 1, StartedAt: startedAt}
	question := domain.Question{
		ID: questionID, WorkspaceID: workspaceID, SessionID: sessionID, QuestionNo: 1, ClaimID: claimID,
		Prompt: "Explain stable keyset pagination.", AnswerPoints: material.AnswerPoints, Evidence: material.Evidence,
		Status: domain.QuestionStatusPending, Fingerprint: fingerprint, CreatedAt: startedAt,
	}
	return interviewapp.StartRecord{Session: session, Questions: []domain.Question{question}, IdempotencyKey: key, RequestHash: strings.Repeat("9", 64)}
}

func newInterviewTestDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for Interview PostgreSQL integration test")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := "zhixu_interview_" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	runner, err := platformmigration.NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	}
}

type interviewQuestionSourceFixture struct {
	workspaceID     foundation.ID
	claimID         foundation.ID
	topicID         foundation.ID
	otherTopicID    foundation.ID
	sourceVersionID foundation.ID
	sourceSpanID    foundation.ID
	chunkID         foundation.ID
	indexID         foundation.ID
	now             time.Time
}

func seedInterviewQuestionSourceFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) interviewQuestionSourceFixture {
	t.Helper()
	fixture := interviewQuestionSourceFixture{
		workspaceID:     interviewIntegrationID(100),
		claimID:         interviewIntegrationID(108),
		topicID:         interviewIntegrationID(109),
		otherTopicID:    interviewIntegrationID(110),
		sourceVersionID: interviewIntegrationID(103),
		sourceSpanID:    interviewIntegrationID(105),
		chunkID:         interviewIntegrationID(106),
		indexID:         interviewIntegrationID(107),
		now:             time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC),
	}
	contentHash := strings.Repeat("a", 64)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
			VALUES($1,'interview-question-source','/tmp/interview-question-source','/tmp/interview-question-source',$2,'test',1,$2,$2)`, []any{string(fixture.workspaceID), fixture.now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
			VALUES($1,$2,$3,29,$4,$5)`, []any{string(interviewIntegrationID(101)), string(fixture.workspaceID), contentHash, ".knowledge/sources/" + contentHash, fixture.now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
			VALUES($1,$2,'text','interview-question-source.txt','interview-question-source.txt',$3)`, []any{string(interviewIntegrationID(102)), string(fixture.workspaceID), fixture.now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at)
			VALUES($1,$2,$3,$4,$5,29,'text/plain','interview-question-source.txt','passed',$6)`, []any{string(fixture.sourceVersionID), string(interviewIntegrationID(102)), string(fixture.workspaceID), string(interviewIntegrationID(101)), contentHash, fixture.now}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at)
			VALUES($1,$2,$3,'text','v1',repeat('b',64),'v1',repeat('c',64),'[]',$4)`, []any{string(interviewIntegrationID(104)), string(fixture.workspaceID), string(interviewIntegrationID(101)), fixture.now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
			VALUES($1,$2,$3,$4)`, []any{string(fixture.sourceVersionID), string(interviewIntegrationID(104)), string(fixture.workspaceID), fixture.now}},
		{`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at)
			VALUES($1,$2,$3,$4,'chunked','passed','text','v1',repeat('b',64),'structure-v1','v1','interview-question-source-chunked',1,$5,$5)`, []any{string(interviewIntegrationID(116)), string(fixture.workspaceID), string(fixture.sourceVersionID), string(interviewIntegrationID(104)), fixture.now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at)
			VALUES($1,$2,$3,$4,'paragraph',1,1,0,29,'{}',repeat('d',64),'v1','v1',$5)`, []any{string(fixture.sourceSpanID), string(fixture.workspaceID), string(interviewIntegrationID(101)), string(interviewIntegrationID(104)), fixture.now}},
		{`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
			VALUES($1,$2,$3,0,'["Interview"]','A confirmed source stays active.',$4,$5,29,29,'v1','structure-v1','v1',false,'active',$6)`, []any{string(fixture.chunkID), string(fixture.workspaceID), string(interviewIntegrationID(104)), contentHash, string(fixture.sourceSpanID), fixture.now}},
		{`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
			VALUES($1,$2,'Question source topic','question source topic','','ACTIVE',1,$3,$3)`, []any{string(fixture.topicID), string(fixture.workspaceID), fixture.now}},
		{`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
			VALUES($1,$2,'Other topic','other topic','','ACTIVE',1,$3,$3)`, []any{string(fixture.otherTopicID), string(fixture.workspaceID), fixture.now}},
		{`INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_factors,fingerprint,version,created_at,updated_at)
			VALUES($1,$2,'A confirmed source stays active.','a confirmed source stays active.','{}','knowledge-applicability/v1',repeat('e',64),'SUGGESTED','{}',repeat('f',64),1,$3,$3)`, []any{string(fixture.claimID), string(fixture.workspaceID), fixture.now}},
		{`INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at)
			VALUES($1,$2,$3,$4,$5,'SUPPORTS','Question source supporting evidence.',repeat('1',64),$6)`, []any{string(interviewIntegrationID(114)), string(fixture.workspaceID), string(fixture.claimID), string(fixture.sourceVersionID), string(fixture.sourceSpanID), fixture.now}},
		{`INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at)
			VALUES($1,$2,$3,$4,$5,'REFUTES','Refuting evidence must never become an answer point.',repeat('2',64),$6)`, []any{string(interviewIntegrationID(115)), string(fixture.workspaceID), string(fixture.claimID), string(fixture.sourceVersionID), string(fixture.sourceSpanID), fixture.now}},
		{`INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,fingerprint,version,created_at,updated_at)
			VALUES($1,$2,'CLAIM',$3,'TOPIC',$4,'BELONGS_TO','SUGGESTED',repeat('3',64),1,$5,$5)`, []any{string(interviewIntegrationID(111)), string(fixture.workspaceID), string(fixture.claimID), string(fixture.topicID), fixture.now}},
		{`INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at)
			VALUES($1,$2,$3,$4,$5,'Question source topic evidence.',repeat('4',64),'{}','knowledge-applicability/v1',repeat('5',64),'USER_APPROVAL','interview-question-source-fixture',$6)`, []any{string(interviewIntegrationID(112)), string(fixture.workspaceID), string(interviewIntegrationID(111)), string(fixture.sourceVersionID), string(fixture.sourceSpanID), fixture.now}},
		{`UPDATE core.claim SET status='CONFIRMED',version=2,updated_at=$3 WHERE workspace_id=$1 AND id=$2`, []any{string(fixture.workspaceID), string(fixture.claimID), fixture.now.Add(time.Second)}},
		{`UPDATE core.relation SET status='CONFIRMED',evidence_fingerprint=repeat('6',64),confirmation_method='USER_APPROVAL',confirmation_ref='interview-question-source-fixture',version=2,updated_at=$3 WHERE workspace_id=$1 AND id=$2`, []any{string(fixture.workspaceID), string(interviewIntegrationID(111)), fixture.now.Add(time.Second)}},
		{`INSERT INTO retrieval.index_version(id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version)
			VALUES($1,$2,'simple','v1',repeat('7',64),'{}','interview-question-source',repeat('8',64),1,'interview-question-source','building','["vector"]',1,$3,$3,repeat('9',64),1,'text','v1',repeat('b',64),'structure-v1','v1')`, []any{string(fixture.indexID), string(fixture.workspaceID), fixture.now}},
		{`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at)
			VALUES($1,$2,$3,$4,0,'v1','structure-v1','v1',$5)`, []any{string(fixture.indexID), string(fixture.chunkID), string(fixture.workspaceID), contentHash, fixture.now}},
		{`INSERT INTO retrieval.index_manifest_source(index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,exclusion_code,created_at)
			VALUES($1,$2,$3,$4,$5,'included',NULL,$6)`, []any{string(fixture.indexID), string(fixture.workspaceID), string(interviewIntegrationID(102)), string(fixture.sourceVersionID), string(interviewIntegrationID(104)), fixture.now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func activateInterviewQuestionSourceIndex(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture interviewQuestionSourceFixture) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.chunk_projection(index_version_id,chunk_id,workspace_id,search_vector,token_count,lexical_status,vector_status,created_at,updated_at)
		VALUES($1,$2,$3,to_tsvector('simple','A confirmed source stays active.'),5,'ready','disabled',$4,$4)`, string(fixture.indexID), string(fixture.chunkID), string(fixture.workspaceID), fixture.now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	builtAt := fixture.now.Add(3 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE retrieval.index_version SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE workspace_id=$1 AND id=$3`, string(fixture.workspaceID), builtAt, string(fixture.indexID)); err != nil {
		t.Fatal(err)
	}
	activatedAt := fixture.now.Add(4 * time.Minute)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(id,kind,workspace_id,target_index_version_id,target_version,idempotency_key,reason_code,created_at)
			VALUES($1,'activate',$2,$3,3,'interview-question-source-activate','interview-question-source-fixture',$4)`, string(interviewIntegrationID(113)), string(fixture.workspaceID), string(fixture.indexID), activatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET status='active',version=3,activated_at=$2,updated_at=$2 WHERE workspace_id=$1 AND id=$3`, string(fixture.workspaceID), activatedAt, string(fixture.indexID)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func seedInterviewPersistence(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	workspaceID := string(interviewIntegrationID(1))
	claimID := string(interviewIntegrationID(2))
	contentHash := strings.Repeat("c", 64)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'interview','/tmp/interview','/tmp/interview',$2,'test',1,$2,$2)`, []any{workspaceID, now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,4,$4,$5)`, []any{string(interviewIntegrationID(20)), workspaceID, contentHash, ".knowledge/sources/" + contentHash, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'text','interview.txt','interview.txt',$3)`, []any{string(interviewIntegrationID(21)), workspaceID, now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,$5,4,'text/plain','interview.txt','pending',$6)`, []any{string(interviewIntegrationID(30)), string(interviewIntegrationID(21)), workspaceID, string(interviewIntegrationID(20)), contentHash, now}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`, []any{string(interviewIntegrationID(22)), workspaceID, string(interviewIntegrationID(20)), strings.Repeat("d", 64), strings.Repeat("e", 64), now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,4,'{}',$5,'v1','v1',$6)`, []any{string(interviewIntegrationID(31)), workspaceID, string(interviewIntegrationID(20)), string(interviewIntegrationID(22)), strings.Repeat("f", 64), now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, []any{string(interviewIntegrationID(30)), string(interviewIntegrationID(22)), workspaceID, now}},
		{`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at)
			VALUES($1,$2,$3,$4,'chunked','passed','text','v1',repeat('d',64),'structure-v1','v1','interview-persistence-chunked',1,$5,$5)`, []any{string(interviewIntegrationID(34)), workspaceID, string(interviewIntegrationID(30)), string(interviewIntegrationID(22)), now}},
		{`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
			VALUES($1,$2,$3,0,'["Interview"]','An interview turn must not update FSRS.',$4,$5,39,39,'v1','structure-v1','v1',false,'active',$6)`, []any{string(interviewIntegrationID(33)), workspaceID, string(interviewIntegrationID(22)), contentHash, string(interviewIntegrationID(31)), now}},
		{`INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_factors,fingerprint,version,created_at,updated_at) VALUES($1,$2,'An interview turn must not update FSRS.','an interview turn must not update fsrs.','{}','knowledge-applicability/v1',repeat('1',64),'SUGGESTED','{}',repeat('2',64),1,$3,$3)`, []any{claimID, workspaceID, now}},
		{`INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at) VALUES($1,$2,$3,$4,$5,'SUPPORTS','Interview support evidence.',repeat('a',64),$6)`, []any{string(interviewIntegrationID(23)), workspaceID, claimID, string(interviewIntegrationID(30)), string(interviewIntegrationID(31)), now}},
		{`INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at) VALUES($1,$2,$3,$4,$5,'REFUTES','Interview refuting evidence.',repeat('b',64),$6)`, []any{string(interviewIntegrationID(24)), workspaceID, claimID, string(interviewIntegrationID(30)), string(interviewIntegrationID(31)), now}},
		{`INSERT INTO retrieval.index_version(id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version)
			VALUES($1,$2,'simple','v1',repeat('7',64),'{}','interview-persistence',repeat('8',64),1,'interview-persistence','building','["vector"]',1,$3,$3,repeat('9',64),1,'text','v1',repeat('d',64),'structure-v1','v1')`, []any{string(interviewIntegrationID(32)), workspaceID, now}},
		{`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at)
			VALUES($1,$2,$3,$4,0,'v1','structure-v1','v1',$5)`, []any{string(interviewIntegrationID(32)), string(interviewIntegrationID(33)), workspaceID, contentHash, now}},
		{`INSERT INTO retrieval.index_manifest_source(index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,exclusion_code,created_at)
			VALUES($1,$2,$3,$4,$5,'included',NULL,$6)`, []any{string(interviewIntegrationID(32)), workspaceID, string(interviewIntegrationID(21)), string(interviewIntegrationID(30)), string(interviewIntegrationID(22)), now}},
		{`INSERT INTO learning.artifact(id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at,domain_schema_version,scope_definition,source_coverage,current_revision_id) VALUES($1,$2,'INTERVIEW_DOC','Interview document','{}','DRAFT',1,$3,$3,'artifact/v1','{}','[]',$4)`, []any{string(interviewIntegrationID(40)), workspaceID, now, string(interviewIntegrationID(41))}},
		{`INSERT INTO learning.artifact_revision(id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,created_at,domain_schema_version,content_hash,created_by_type) VALUES($1,$2,$3,1,'SNAPSHOT','[]','[]','[]','[]','[]','','{}',$4,'artifact-revision/v1',repeat('3',64),'HUMAN')`, []any{string(interviewIntegrationID(41)), string(interviewIntegrationID(40)), workspaceID, now}},
		{`INSERT INTO learning.artifact(id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at,domain_schema_version,scope_definition,source_coverage,current_revision_id) VALUES($1,$2,'LEARNING_PATH','Learning path','{}','DRAFT',1,$3,$3,'artifact/v1','{}','[]',$4)`, []any{string(interviewIntegrationID(42)), workspaceID, now, string(interviewIntegrationID(43))}},
		{`INSERT INTO learning.artifact_revision(id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,created_at,domain_schema_version,content_hash,created_by_type) VALUES($1,$2,$3,1,'SNAPSHOT','[]','[]','[]','[]','[]','','{}',$4,'artifact-revision/v1',repeat('4',64),'HUMAN')`, []any{string(interviewIntegrationID(43)), string(interviewIntegrationID(42)), workspaceID, now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE core.claim SET status='CONFIRMED',version=version+1,updated_at=$3 WHERE id=$1 AND workspace_id=$2`, claimID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	activateInterviewQuestionSourceIndex(t, ctx, pool, interviewQuestionSourceFixture{
		workspaceID: interviewIntegrationID(1), sourceVersionID: interviewIntegrationID(30),
		sourceSpanID: interviewIntegrationID(31), chunkID: interviewIntegrationID(33),
		indexID: interviewIntegrationID(32), now: now,
	})
}

func seedInterviewCompletionVisibilityHold(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, sessionID, artifactID foundation.ID,
	role string,
	attemptDigest string,
	createdAt time.Time,
) {
	t.Helper()
	artifactType := "INTERVIEW_DOC"
	if role == "PATH" {
		artifactType = "LEARNING_PATH"
	}
	planKey := "iv1:" + string(sessionID) + ":" + artifactType + ":" + attemptDigest + ":p"
	if _, err := pool.Exec(ctx, `
			INSERT INTO learning.artifact_command(
				workspace_id,idempotency_key,request_hash,command_type,artifact_id,artifact_version,response,created_at
			) VALUES($1,$2,repeat('f',64),'PLAN',$3,1,'{}'::jsonb,$4)`,
		string(workspaceID), planKey, string(artifactID), createdAt.UTC()); err != nil {
		t.Fatalf("seed Interview completion %s PLAN receipt: %v", role, err)
	}
	if _, err := pool.Exec(ctx, `
			INSERT INTO learning.artifact_visibility_hold(
				workspace_id,artifact_id,owner_type,owner_id,owner_role,attempt_digest,created_at
			) VALUES($1,$2,'INTERVIEW_COMPLETE',$3,$4,$5,$6)`,
		string(workspaceID), string(artifactID), string(sessionID), role, attemptDigest, createdAt.UTC()); err != nil {
		t.Fatalf("seed Interview completion %s visibility hold: %v", role, err)
	}
}

func interviewRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertInterviewBindingConstraints(t *testing.T, ctx context.Context, pool *pgxpool.Pool, now time.Time) {
	t.Helper()
	workspaceID := string(interviewIntegrationID(1))
	pathID := string(interviewIntegrationID(14))
	pathArtifactID := string(interviewIntegrationID(42))
	staleRevisionID := string(interviewIntegrationID(44))
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision(id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,created_at,domain_schema_version,content_hash,created_by_type) VALUES($1,$2,$3,2,'SNAPSHOT','[]','[]','[]','[]','[]','','{}',$4,'artifact-revision/v1',repeat('5',64),'HUMAN')`, staleRevisionID, pathArtifactID, workspaceID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.interview_learning_path SET artifact_revision_id=$3 WHERE workspace_id=$1 AND id=$2`, workspaceID, pathID, staleRevisionID); err == nil {
		t.Fatal("learning path accepted a non-current artifact revision")
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.interview_learning_path SET artifact_version=2 WHERE workspace_id=$1 AND id=$2`, workspaceID, pathID); err == nil {
		t.Fatal("learning path accepted a mismatched artifact version")
	}

	legacyArtifactID := string(interviewIntegrationID(45))
	legacyRevisionID := string(interviewIntegrationID(46))
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact(id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at) VALUES($1,$2,'LEARNING_PATH','Legacy learning path','{}','DRAFT',1,$3,$3)`, legacyArtifactID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision(id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,created_at) VALUES($1,$2,$3,1,'DRAFT','[]','[]','{}','[]','[]','','{}',$4)`, legacyRevisionID, legacyArtifactID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.interview_learning_path SET artifact_id=$3,artifact_revision_id=$4 WHERE workspace_id=$1 AND id=$2`, workspaceID, pathID, legacyArtifactID, legacyRevisionID); err == nil {
		t.Fatal("learning path accepted a legacy artifact binding")
	}

	invalidStepID := string(interviewIntegrationID(50))
	if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_learning_path_step(id,workspace_id,path_id,step_no,claim_id,source_version_id,source_span_id,evidence_hash,title,rationale,status,version,created_at,updated_at) VALUES($1,$2,$3,1,$4,$5,$6,repeat('b',64),'Invalid evidence','Must not use refuting evidence.','PENDING',1,$7,$7)`, invalidStepID, workspaceID, pathID, string(interviewIntegrationID(2)), string(interviewIntegrationID(30)), string(interviewIntegrationID(31)), now.Add(3*time.Minute)); err == nil {
		t.Fatal("learning path step accepted non-SUPPORTS claim evidence")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_learning_path_step(id,workspace_id,path_id,step_no,claim_id,source_version_id,source_span_id,evidence_hash,title,rationale,status,version,created_at,updated_at) VALUES($1,$2,$3,1,$4,$5,$6,repeat('a',64),'Valid evidence','Use the confirmed supporting evidence.','PENDING',1,$7,$7)`, string(interviewIntegrationID(51)), workspaceID, pathID, string(interviewIntegrationID(2)), string(interviewIntegrationID(30)), string(interviewIntegrationID(31)), now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.interview_learning_path_step WHERE workspace_id=$1 AND path_id=$2`, workspaceID, pathID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("learning path steps count=%d err=%v", count, err)
	}
}

func assertInterviewPathCompletionInvariant(t *testing.T, ctx context.Context, repository *Repository, now time.Time) {
	t.Helper()
	workspaceID := interviewIntegrationID(1)
	pathID := interviewIntegrationID(14)
	stepID := interviewIntegrationID(51)
	if _, err := repository.UpdatePathStatus(ctx, interviewapp.UpdatePathStatusRecord{
		WorkspaceID: workspaceID, PathID: pathID, ExpectedVersion: 1, Status: domain.PathStatusCompleted,
		IdempotencyKey: "learning-path-complete-pending", RequestHash: strings.Repeat("e", 64), At: now.Add(4 * time.Minute),
	}); err == nil {
		t.Fatal("repository accepted completion while a path step was pending")
	}
	stepResult, err := repository.UpdatePathStep(ctx, interviewapp.UpdatePathStepRecord{
		WorkspaceID: workspaceID, PathID: pathID, StepID: stepID, ExpectedVersion: 1, Status: domain.StepStatusSkipped,
		IdempotencyKey: "learning-path-step-skip", RequestHash: strings.Repeat("f", 64), At: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if stepResult.Path.Status != domain.PathStatusCompleted || stepResult.Path.Version != 2 || stepResult.Step.Status != domain.StepStatusSkipped {
		t.Fatalf("last terminal step did not complete the path: %+v", stepResult)
	}
	replay, err := repository.UpdatePathStep(ctx, interviewapp.UpdatePathStepRecord{
		WorkspaceID: workspaceID, PathID: pathID, StepID: stepID, ExpectedVersion: 1, Status: domain.StepStatusSkipped,
		IdempotencyKey: "learning-path-step-skip", RequestHash: strings.Repeat("f", 64), At: now.Add(4 * time.Minute),
	})
	if err != nil || !replay.Replayed || replay.Path.Status != domain.PathStatusCompleted {
		t.Fatalf("terminal step replay=%+v err=%v", replay, err)
	}
	persisted, err := repository.Get(ctx, workspaceID, interviewIntegrationID(10))
	if err != nil || persisted.Path == nil || persisted.Path.Status != domain.PathStatusCompleted || len(persisted.Steps) != 1 || persisted.Steps[0].Status != domain.StepStatusSkipped {
		t.Fatalf("persisted terminal path=%+v err=%v", persisted, err)
	}
}

func integrationScore(evidence []domain.EvidenceRef) domain.Score {
	dimension := domain.ScoreDimension{Value: 1, Rationale: "fully covered frozen answer point"}
	return domain.Score{SchemaVersion: domain.ScoreSchemaVersion, Correctness: dimension, Coverage: dimension, Boundaries: dimension, Clarity: dimension, Evidence: evidence}
}

func interviewIntegrationID(value int) foundation.ID {
	parsed, err := foundation.ParseID(fmt.Sprintf("72000000-0000-4000-8000-%012x", value))
	if err != nil {
		panic(err)
	}
	return parsed
}
