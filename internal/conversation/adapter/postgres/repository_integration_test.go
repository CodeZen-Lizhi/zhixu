//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryCreatesConversationWithEventAndExactReplay(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceA := conversationPostgresID(1)
	workspaceB := conversationPostgresID(2)
	seedConversationWorkspaces(t, ctx, pool, workspaceA, workspaceB)
	now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)

	firstRecord := conversationCreateRecord(t, workspaceA, conversationPostgresID(10), "RAG notes", "create-key", now)
	created, err := repository.CreateConversation(ctx, firstRecord)
	if err != nil || created.Replayed || created.Conversation.ID != firstRecord.Conversation.ID {
		t.Fatalf("CreateConversation(first) = %#v, error_chain=%s", created, conversationErrorChain(err))
	}

	replayRecord := conversationCreateRecord(t, workspaceA, conversationPostgresID(11), "RAG notes", "create-key", now.Add(time.Minute))
	replayed, err := repository.CreateConversation(ctx, replayRecord)
	if err != nil || !replayed.Replayed || replayed.Conversation.ID != firstRecord.Conversation.ID ||
		!replayed.Conversation.CreatedAt.Equal(now) {
		t.Fatalf("CreateConversation(replay) = %#v, %v", replayed, err)
	}

	conflictRecord := conversationCreateRecord(t, workspaceA, conversationPostgresID(12), "Different title", "create-key", now.Add(2*time.Minute))
	_, err = repository.CreateConversation(ctx, conflictRecord)
	requireConversationRepositoryError(t, err, foundation.ErrorVersionConflict, ErrorCodeIdempotencyConflict)

	otherRecord := conversationCreateRecord(t, workspaceB, conversationPostgresID(13), "RAG notes", "create-key", now.Add(3*time.Minute))
	other, err := repository.CreateConversation(ctx, otherRecord)
	if err != nil || other.Replayed || other.Conversation.WorkspaceID != workspaceB {
		t.Fatalf("CreateConversation(other workspace) = %#v, %v", other, err)
	}

	var conversationCount, eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.conversation WHERE id IN ($1,$2,$3,$4)`,
		string(firstRecord.Conversation.ID), string(replayRecord.Conversation.ID), string(conflictRecord.Conversation.ID), string(otherRecord.Conversation.ID)).Scan(&conversationCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event WHERE event_type='conversation.created'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if conversationCount != 2 || eventCount != 2 {
		t.Fatalf("conversation_count=%d event_count=%d", conversationCount, eventCount)
	}
	var resourceRef, sourceEventRef, status string
	if err := pool.QueryRow(ctx, `SELECT resource_ref,source_event_ref,payload_summary->>'status'
		FROM ops.server_event WHERE workspace_id=$1 AND conversation_id=$2`, string(workspaceA), string(firstRecord.Conversation.ID)).Scan(
		&resourceRef, &sourceEventRef, &status,
	); err != nil {
		t.Fatal(err)
	}
	if resourceRef != "conversation:"+string(firstRecord.Conversation.ID) ||
		sourceEventRef != "conversation.created:"+string(firstRecord.Conversation.ID)+":v1" || status != "open" {
		t.Fatalf("event binding = %q %q %q", resourceRef, sourceEventRef, status)
	}
}

func TestRepositoryListsAndGetsConversationsWithStableWorkspaceCursor(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceA := conversationPostgresID(20)
	workspaceB := conversationPostgresID(21)
	seedConversationWorkspaces(t, ctx, pool, workspaceA, workspaceB)
	base := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)

	records := []conversationapplication.CreateConversationRecord{
		conversationCreateRecord(t, workspaceA, conversationPostgresID(32), "Latest", "list-latest", base.Add(2*time.Minute)),
		conversationCreateRecord(t, workspaceA, conversationPostgresID(30), "Tie first", "list-tie-first", base.Add(time.Minute)),
		conversationCreateRecord(t, workspaceA, conversationPostgresID(31), "Tie second", "list-tie-second", base.Add(time.Minute)),
		conversationCreateRecord(t, workspaceB, conversationPostgresID(33), "Other workspace", "list-other", base.Add(3*time.Minute)),
	}
	for _, record := range records {
		if _, err := repository.CreateConversation(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	first, err := repository.ListConversations(ctx, conversationapplication.ListConversationsQuery{
		WorkspaceID: workspaceA,
		Limit:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != records[0].Conversation.ID || first.Items[1].ID != records[1].Conversation.ID ||
		first.NextCursor == nil || first.NextCursor.ID != records[1].Conversation.ID {
		t.Fatalf("first page = %#v", first)
	}
	second, err := repository.ListConversations(ctx, conversationapplication.ListConversationsQuery{
		WorkspaceID: workspaceA,
		Cursor:      first.NextCursor,
		Limit:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != records[2].Conversation.ID || second.NextCursor != nil {
		t.Fatalf("second page = %#v", second)
	}

	loaded, err := repository.GetConversation(ctx, workspaceA, records[1].Conversation.ID)
	if err != nil || loaded.ID != records[1].Conversation.ID || loaded.Title == nil || *loaded.Title != "Tie first" {
		t.Fatalf("GetConversation() = %#v, %v", loaded, err)
	}
	_, crossWorkspaceErr := repository.GetConversation(ctx, workspaceB, records[1].Conversation.ID)
	_, missingErr := repository.GetConversation(ctx, workspaceA, conversationPostgresID(99))
	requireConversationRepositoryError(t, crossWorkspaceErr, foundation.ErrorNotFound, ErrorCodeConversationNotFound)
	requireConversationRepositoryError(t, missingErr, foundation.ErrorNotFound, ErrorCodeConversationNotFound)
}

func TestRepositoryConcurrentConversationCreateHasOneFactAndOneReplay(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(40)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 19, 14, 30, 0, 0, time.UTC)
	records := []conversationapplication.CreateConversationRecord{
		conversationCreateRecord(t, workspaceID, conversationPostgresID(41), "Concurrent", "concurrent-create", now),
		conversationCreateRecord(t, workspaceID, conversationPostgresID(42), "Concurrent", "concurrent-create", now.Add(time.Second)),
	}
	type outcome struct {
		result conversationapplication.CreateConversationResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, len(records))
	for _, record := range records {
		record := record
		go func() {
			<-start
			result, err := repository.CreateConversation(ctx, record)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	first := <-outcomes
	second := <-outcomes
	if first.err != nil || second.err != nil || first.result.Conversation.ID != second.result.Conversation.ID || first.result.Replayed == second.result.Replayed {
		t.Fatalf("concurrent outcomes = %#v %#v", first, second)
	}
	var conversations, events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.conversation WHERE workspace_id=$1 AND idempotency_key='concurrent-create'`, string(workspaceID)).Scan(&conversations); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND event_type='conversation.created'`, string(workspaceID)).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if conversations != 1 || events != 1 {
		t.Fatalf("conversation_count=%d event_count=%d", conversations, events)
	}
}

func TestRepositoryExactReplaySurvivesServerEventRetentionCleanup(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(50)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 19, 15, 0, 0, 0, time.UTC)
	createdRecord := conversationCreateRecord(t, workspaceID, conversationPostgresID(51), "Retained conversation", "retention-replay", now)
	created, err := repository.CreateConversation(ctx, createdRecord)
	if err != nil || created.Replayed {
		t.Fatalf("CreateConversation(first) = %#v, %v", created, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ops.server_event WHERE workspace_id=$1 AND conversation_id=$2`,
		string(workspaceID), string(created.Conversation.ID)); err != nil {
		t.Fatal(err)
	}

	replayRecord := conversationCreateRecord(t, workspaceID, conversationPostgresID(52), "Retained conversation", "retention-replay", now.Add(25*time.Hour))
	replayed, err := repository.CreateConversation(ctx, replayRecord)
	if err != nil || !replayed.Replayed || replayed.Conversation.ID != created.Conversation.ID ||
		!replayed.Conversation.CreatedAt.Equal(now) {
		t.Fatalf("CreateConversation(after event cleanup) = %#v, %v", replayed, err)
	}
	var conversationCount, eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.conversation WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(workspaceID), createdRecord.IdempotencyKey).Scan(&conversationCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND conversation_id=$2`,
		string(workspaceID), string(created.Conversation.ID)).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if conversationCount != 1 || eventCount != 0 {
		t.Fatalf("conversation_count=%d event_count=%d", conversationCount, eventCount)
	}
}

func TestRepositoryRollsBackConversationWhenCreationEventAppendFails(t *testing.T) {
	_, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(60)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	repository, err := NewRepository(pool, failingConversationEventAppender{
		err: foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_EVENT_APPEND_FAILED", true, errors.New("injected event failure")),
	})
	if err != nil {
		t.Fatal(err)
	}
	record := conversationCreateRecord(t, workspaceID, conversationPostgresID(61), "Atomic create", "atomic-create", time.Now().UTC())
	if _, err := repository.CreateConversation(ctx, record); err == nil {
		t.Fatal("CreateConversation() succeeded after event append failure")
	}
	var conversationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.conversation WHERE id=$1`, string(record.Conversation.ID)).Scan(&conversationCount); err != nil {
		t.Fatal(err)
	}
	if conversationCount != 0 {
		t.Fatalf("event failure left %d conversation rows", conversationCount)
	}
}

func TestRepositoryRecoversCommittedCreateAfterCommitResponseLoss(t *testing.T) {
	_, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationPostgresID(70)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	lossDB := &conversationCommitResponseLossDB{Pool: pool, loseNext: true}
	repository, err := NewRepository(lossDB, events)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)
	firstRecord := conversationCreateRecord(t, workspaceID, conversationPostgresID(71), "Response loss", "response-loss", now)
	if _, err := repository.CreateConversation(ctx, firstRecord); err == nil {
		t.Fatal("CreateConversation() did not expose the injected commit response loss")
	}
	replayRecord := conversationCreateRecord(t, workspaceID, conversationPostgresID(72), "Response loss", "response-loss", now.Add(time.Minute))
	replayed, err := repository.CreateConversation(ctx, replayRecord)
	if err != nil || !replayed.Replayed || replayed.Conversation.ID != firstRecord.Conversation.ID {
		t.Fatalf("CreateConversation(replay after response loss) = %#v, %v", replayed, err)
	}
	var conversationCount, eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.conversation WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(workspaceID), firstRecord.IdempotencyKey).Scan(&conversationCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND conversation_id=$2`,
		string(workspaceID), string(firstRecord.Conversation.ID)).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if conversationCount != 1 || eventCount != 1 {
		t.Fatalf("conversation_count=%d event_count=%d", conversationCount, eventCount)
	}
}

type failingConversationEventAppender struct{ err error }

func (appender failingConversationEventAppender) AppendTx(context.Context, any, eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error) {
	return eventsdomain.ServerEvent{}, false, appender.err
}

type conversationCommitResponseLossDB struct {
	*pgxpool.Pool
	loseNext bool
}

func (database *conversationCommitResponseLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	transaction, err := database.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if !database.loseNext {
		return transaction, nil
	}
	database.loseNext = false
	return conversationCommitResponseLossTx{Tx: transaction}, nil
}

type conversationCommitResponseLossTx struct{ pgx.Tx }

func (transaction conversationCommitResponseLossTx) Commit(ctx context.Context) error {
	if err := transaction.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("injected commit response loss")
}

func conversationCreateRecord(
	t *testing.T,
	workspaceID, conversationID foundation.ID,
	title, idempotencyKey string,
	at time.Time,
) conversationapplication.CreateConversationRecord {
	t.Helper()
	request, err := conversationdomain.CanonicalizeConversationCreateRequest(conversationdomain.ConversationCreateRequest{
		WorkspaceID: workspaceID,
		Title:       &title,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := conversationdomain.ComputeConversationCreateRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	return conversationapplication.CreateConversationRecord{
		Conversation: conversationdomain.Conversation{
			ID: conversationID, WorkspaceID: workspaceID, Status: conversationdomain.ConversationStatusOpen,
			Title: request.Title, Version: 1, LastActivityAt: at, CreatedAt: at, UpdatedAt: at,
		},
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
	}
}

func newConversationTestRepository(t *testing.T) (*Repository, *pgxpool.Pool, context.Context) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for Conversation integration tests")
	}
	ctx := context.Background()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_conversation_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 4, 0)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	runner, err := platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
	if err == nil {
		err = runner.Up(ctx)
	}
	migrationPool.Close()
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	database, err := platformpostgres.Open(ctx, databaseURL, 8, 0)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	if err := database.Ping(ctx); err != nil {
		database.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	events, err := eventspostgres.NewStore(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(database.DB(), events)
	if err != nil {
		t.Fatal(err)
	}
	return repository, database.DB(), ctx
}

func seedConversationWorkspaces(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceIDs ...foundation.ID) {
	t.Helper()
	for index, workspaceID := range workspaceIDs {
		status := "inactive"
		if index == 0 {
			status = "active"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,$2,$3,$3,CURRENT_TIMESTAMP,$4,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			string(workspaceID), fmt.Sprintf("conversation-%d", index), fmt.Sprintf("/tmp/zhixu-conversation-%d-%s", index, workspaceID), status); err != nil {
			t.Fatal(err)
		}
	}
}

func requireConversationRepositoryError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error = %#v, want kind=%s code=%s", err, kind, code)
	}
}

func conversationErrorChain(err error) string {
	parts := make([]string, 0, 4)
	for err != nil {
		parts = append(parts, fmt.Sprintf("%T:%v", err, err))
		err = errors.Unwrap(err)
	}
	return strings.Join(parts, " <- ")
}

func conversationPostgresID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("72000000-0000-4000-8000-%012d", value))
}
