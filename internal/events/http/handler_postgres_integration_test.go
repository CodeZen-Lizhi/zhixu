//go:build integration && testcontainers

package eventshttp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
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
	assertIntegrationQueryProblem(t, router, path, "invalid", http.StatusBadRequest, domain.ErrorCodeCursorInvalid, "")
	assertIntegrationQueryProblem(t, router, path, strconv.FormatInt(secondRetainedSeq+100, 10), http.StatusBadRequest, domain.ErrorCodeCursorFuture, "")
	assertIntegrationQueryProblem(t, router, path, strconv.FormatInt(expiredSeq, 10), http.StatusConflict, domain.ErrorCodeCursorExpired, "refetch")

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	replayContext, cancelReplay := context.WithTimeout(context.Background(), 2*time.Second)
	replayRequest, err := http.NewRequestWithContext(
		replayContext,
		http.MethodGet,
		server.URL+path+"&last_event_id="+strconv.FormatInt(firstRetainedSeq, 10),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
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

	precedenceContext, cancelPrecedence := context.WithTimeout(context.Background(), 2*time.Second)
	precedenceRequest, err := http.NewRequestWithContext(
		precedenceContext,
		http.MethodGet,
		server.URL+path+"&last_event_id="+strconv.FormatInt(expiredSeq, 10)+"&event_format=message",
		nil,
	)
	if err != nil {
		cancelPrecedence()
		t.Fatal(err)
	}
	precedenceRequest.Header.Set("Last-Event-ID", strconv.FormatInt(firstRetainedSeq, 10))
	precedenceResponse, err := server.Client().Do(precedenceRequest)
	if err != nil {
		cancelPrecedence()
		t.Fatal(err)
	}
	precedenceBlock := readSSEBlock(t, bufio.NewReader(precedenceResponse.Body))
	_ = precedenceResponse.Body.Close()
	cancelPrecedence()
	assertSSEMessageBlockForWorkspace(t, precedenceBlock, secondRetainedSeq, "answer.completed", workspaceA)

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

func assertIntegrationQueryProblem(t *testing.T, handler http.Handler, path, cursor string, status int, code, action string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path+"&last_event_id="+url.QueryEscape(cursor), nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var problem httpapi.Problem
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &problem) != nil || problem.ErrorCode != code {
		t.Fatalf("query cursor=%q status=%d problem=%#v body=%s", cursor, response.Code, problem, response.Body.String())
	}
	if action != "" && problem.Details["action"] != action {
		t.Fatalf("query cursor=%q action=%v want=%q", cursor, problem.Details["action"], action)
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
	) VALUES($1,$2,$3,$3,now(),'inactive',1,now(),now())`, string(workspaceID), fmt.Sprintf("events-http-%d", suffix), root); err != nil {
		t.Fatal(err)
	}
}

func newEventHTTPIntegration(t *testing.T) (*eventspostgres.GORMStore, *pgxpool.Pool, context.Context) {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("test database fixture did not provide a shared platform pool")
	}
	store, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatalf("NewGORMStore from shared platform pool: %v", err)
	}
	return store, platform.DB(), context.Background()
}
