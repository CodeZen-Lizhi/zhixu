//go:build integration

package eventshttp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHandlerRealPostgreSQLCursorRecoveryAndFreshWatermark(t *testing.T) {
	store, pool, ctx := newEventHTTPIntegration(t)
	workspaceA := foundation.ID("7f000000-0000-4000-8000-000000000001")
	workspaceB := foundation.ID("7f000000-0000-4000-8000-000000000002")
	seedEventHTTPWorkspace(t, ctx, pool, workspaceA, 1)
	seedEventHTTPWorkspace(t, ctx, pool, workspaceB, 2)
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiredSeq := insertEventHTTPRow(t, ctx, pool, workspaceA, "workflow.run.started", now.Add(-25*time.Hour), `{}`)
	firstRetainedSeq := insertEventHTTPRow(t, ctx, pool, workspaceA, "workflow.run.progress", now.Add(-time.Hour), `{"status":"running"}`)
	_ = insertEventHTTPRow(t, ctx, pool, workspaceB, "answer.completed", now.Add(-45*time.Minute), `{"status":"completed"}`)
	secondRetainedSeq := insertEventHTTPRow(t, ctx, pool, workspaceA, "answer.completed", now.Add(-30*time.Minute), `{"status":"completed","candidate_count":2}`)

	handler := NewHandler(store, StreamConfig{PollInterval: 5 * time.Millisecond, HeartbeatInterval: time.Hour})
	router := eventRouter(handler)
	path := "/events?workspace_id=" + string(workspaceA)
	assertIntegrationProblem(t, router, path, "invalid", http.StatusBadRequest, domain.ErrorCodeCursorInvalid, "")
	assertIntegrationProblem(t, router, path, strconv.FormatInt(secondRetainedSeq+100, 10), http.StatusBadRequest, domain.ErrorCodeCursorFuture, "")
	assertIntegrationProblem(t, router, path, strconv.FormatInt(expiredSeq, 10), http.StatusConflict, domain.ErrorCodeCursorExpired, "refetch")

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	replayContext, cancelReplay := context.WithTimeout(context.Background(), 2*time.Second)
	replayRequest, err := http.NewRequestWithContext(replayContext, http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayRequest.Header.Set("Last-Event-ID", strconv.FormatInt(firstRetainedSeq, 10))
	replayResponse, err := server.Client().Do(replayRequest)
	if err != nil {
		cancelReplay()
		t.Fatal(err)
	}
	replayBlock := readSSEBlock(t, bufio.NewReader(replayResponse.Body))
	_ = replayResponse.Body.Close()
	cancelReplay()
	assertSSEBlockForWorkspace(t, replayBlock, secondRetainedSeq, "answer.completed", workspaceA)
	if strings.Contains(replayBlock, string(workspaceB)) {
		t.Fatalf("cross-workspace event leaked into replay: %s", replayBlock)
	}

	freshContext, cancelFresh := context.WithTimeout(context.Background(), 2*time.Second)
	freshRequest, err := http.NewRequestWithContext(freshContext, http.MethodGet, server.URL+path, nil)
	if err != nil {
		cancelFresh()
		t.Fatal(err)
	}
	freshResponse, err := server.Client().Do(freshRequest)
	if err != nil {
		cancelFresh()
		t.Fatal(err)
	}
	newSequence := insertEventHTTPRow(t, ctx, pool, workspaceA, "answer.feedback.recorded", time.Now().UTC().Truncate(time.Microsecond), `{"status":"recorded"}`)
	freshBlock := readSSEBlock(t, bufio.NewReader(freshResponse.Body))
	_ = freshResponse.Body.Close()
	cancelFresh()
	assertSSEBlockForWorkspace(t, freshBlock, newSequence, "answer.feedback.recorded", workspaceA)
	if newSequence <= secondRetainedSeq {
		t.Fatalf("fresh event sequence=%d current watermark=%d", newSequence, secondRetainedSeq)
	}
}

func assertIntegrationProblem(t *testing.T, handler http.Handler, path, cursor string, status int, code, action string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Last-Event-ID", cursor)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var problem httpapi.Problem
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &problem) != nil || problem.ErrorCode != code {
		t.Fatalf("cursor=%q status=%d problem=%#v body=%s", cursor, response.Code, problem, response.Body.String())
	}
	if action != "" && problem.Details["action"] != action {
		t.Fatalf("cursor=%q action=%v want=%q", cursor, problem.Details["action"], action)
	}
}

func insertEventHTTPRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, eventType string, occurredAt time.Time, summary string) int64 {
	t.Helper()
	sourceID := fmt.Sprintf("%08x-0000-4000-8000-%012x", occurredAt.UnixNano()&0xffffffff, occurredAt.UnixNano()&0xffffffffffff)
	var sequence int64
	if err := pool.QueryRow(ctx, `INSERT INTO ops.server_event(
		workspace_id,event_type,resource_ref,resource_version,payload_summary,schema_version,
		source_event_ref,occurred_at,expires_at
	) VALUES($1,$2,$3,1,$4::jsonb,1,$5,$6::timestamptz,$6::timestamptz + INTERVAL '24 hours')
	RETURNING seq`, string(workspaceID), eventType, "event:"+sourceID, summary, "manual:"+sourceID, occurredAt).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	return sequence
}

func seedEventHTTPWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, suffix int) {
	t.Helper()
	root := fmt.Sprintf("/tmp/events-http-%d", suffix)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,now(),'test',1,now(),now())`, string(workspaceID), fmt.Sprintf("events-http-%d", suffix), root); err != nil {
		t.Fatal(err)
	}
}

func newEventHTTPIntegration(t *testing.T) (*eventspostgres.Store, *pgxpool.Pool, context.Context) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for Server Event HTTP integration tests")
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
	name := fmt.Sprintf("zhixu_events_http_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 2, 0)
	if err == nil {
		var runner *platformmigration.Runner
		runner, err = platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
		if err == nil {
			err = runner.Up(ctx)
		}
		migrationPool.Close()
	}
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	store, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	return store, pool, ctx
}
