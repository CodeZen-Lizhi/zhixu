package conversationhttp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/gin-gonic/gin"
)

func TestDraftStreamRejectsMalformedWorkspaceAndCursorBeforeSSE(t *testing.T) {
	t.Parallel()
	session := draftSession(2, agentapplication.DraftStreamActive, 2)
	reader := &fakeDraftStreamReader{result: agentapplication.DraftStreamReadResult{Session: &session}}
	handler := NewDraftStreamHandler(reader)
	tests := []struct {
		name   string
		path   string
		cursor []string
		status int
		code   string
	}{
		{name: "workspace missing", path: draftStreamPath(""), status: http.StatusBadRequest, code: ErrorCodeDraftStreamWorkspaceInvalid},
		{name: "workspace duplicate", path: draftStreamPath(string(testWorkspaceID)) + "&workspace_id=" + string(testWorkspaceID), status: http.StatusBadRequest, code: ErrorCodeDraftStreamWorkspaceInvalid},
		{name: "cursor leading zero", path: draftStreamPath(string(testWorkspaceID)), cursor: []string{"02:1"}, status: http.StatusBadRequest, code: ErrorCodeDraftStreamCursorInvalid},
		{name: "cursor duplicate", path: draftStreamPath(string(testWorkspaceID)), cursor: []string{"2:1", "2:2"}, status: http.StatusBadRequest, code: ErrorCodeDraftStreamCursorInvalid},
		{name: "cursor future generation", path: draftStreamPath(string(testWorkspaceID)), cursor: []string{"3:1"}, status: http.StatusBadRequest, code: ErrorCodeDraftStreamCursorFuture},
		{name: "cursor future sequence", path: draftStreamPath(string(testWorkspaceID)), cursor: []string{"2:2"}, status: http.StatusBadRequest, code: ErrorCodeDraftStreamCursorFuture},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			for _, cursor := range test.cursor {
				request.Header.Add("Last-Event-ID", cursor)
			}
			response := httptest.NewRecorder()
			draftStreamRouter(handler).ServeHTTP(response, request)
			if response.Code != test.status || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
			var problem httpapi.Problem
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.ErrorCode != test.code {
				t.Fatalf("problem=%#v err=%v", problem, err)
			}
		})
	}
}

func TestDraftStreamFramesOnlyCurrentGenerationChunks(t *testing.T) {
	t.Parallel()
	session := draftSession(2, agentapplication.DraftStreamActive, 3)
	reader := &fakeDraftStreamReader{result: agentapplication.DraftStreamReadResult{
		Session: &session,
		Chunks: []agentapplication.DraftStreamChunk{
			{SessionID: session.ID, Generation: 2, Sequence: 1, Content: "第一段"},
			{SessionID: session.ID, Generation: 2, Sequence: 2, Content: "\n第二段"},
		},
	}}
	handler := NewDraftStreamHandler(reader, DraftStreamConfig{PollInterval: time.Hour, HeartbeatInterval: time.Hour})
	server := httptest.NewServer(draftStreamRouter(handler))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+draftStreamPath(string(testWorkspaceID)), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	assertDraftStreamHeaders(t, response)
	buffered := bufio.NewReader(response.Body)
	first := readDraftSSEBlock(t, buffered)
	second := readDraftSSEBlock(t, buffered)
	assertDraftChunk(t, first, "2:1", 2, 1, "第一段")
	assertDraftChunk(t, second, "2:2", 2, 2, "\n第二段")
	if strings.Contains(first+second, "lease_owner") || strings.Contains(first+second, "workflow_run_id") {
		t.Fatalf("draft SSE leaked runtime binding: %s%s", first, second)
	}
	queries := reader.queriesSnapshot()
	if len(queries) != 1 || queries[0].After != nil || queries[0].Limit != agentapplication.MaxDraftStreamReplayChunks {
		t.Fatalf("reader queries=%#v", queries)
	}
}

func TestDraftStreamResetsOldGenerationAndEndsTerminalStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		session    agentapplication.DraftStreamSession
		cursor     string
		wantEvents []string
	}{
		{name: "old generation", session: draftSession(3, agentapplication.DraftStreamActive, 1), cursor: "2:1", wantEvents: []string{draftStreamResetEvent, draftStreamEndEvent}},
		{name: "published", session: draftSession(3, agentapplication.DraftStreamPublished, 4), cursor: "3:2", wantEvents: []string{draftStreamEndEvent}},
		{name: "aborted", session: draftSession(3, agentapplication.DraftStreamAborted, 4), cursor: "3:2", wantEvents: []string{draftStreamResetEvent, draftStreamEndEvent}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeDraftStreamReader{result: agentapplication.DraftStreamReadResult{Session: &test.session}}
			request := httptest.NewRequest(http.MethodGet, draftStreamPath(string(testWorkspaceID)), nil)
			request.Header.Set("Last-Event-ID", test.cursor)
			response := httptest.NewRecorder()
			draftStreamRouter(NewDraftStreamHandler(reader)).ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
				t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
			frames := splitDraftFrames(response.Body.String())
			if len(frames) != len(test.wantEvents) {
				t.Fatalf("frames=%q want=%v", frames, test.wantEvents)
			}
			for index, event := range test.wantEvents {
				if !strings.Contains(frames[index], "event: "+event+"\n") || strings.Contains(frames[index], "content") {
					t.Fatalf("frame=%q want event=%q", frames[index], event)
				}
			}
		})
	}
}

func TestDraftStreamLeaseInvalidationResetsAndEndsWithoutCursor(t *testing.T) {
	t.Parallel()
	reader := &fakeDraftStreamReader{result: agentapplication.DraftStreamReadResult{Invalidated: true}}
	response := httptest.NewRecorder()
	draftStreamRouter(NewDraftStreamHandler(reader)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, draftStreamPath(string(testWorkspaceID)), nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	frames := splitDraftFrames(response.Body.String())
	if len(frames) != 2 || !strings.Contains(frames[0], "event: "+draftStreamResetEvent+"\n") ||
		!strings.Contains(frames[1], "event: "+draftStreamEndEvent+"\n") || strings.Contains(response.Body.String(), "heartbeat") {
		t.Fatalf("invalidated frames=%q", frames)
	}
	var reset draftResetPayload
	resetLines := strings.Split(strings.TrimSuffix(frames[0], "\n\n"), "\n")
	if len(resetLines) != 2 || !strings.HasPrefix(resetLines[1], "data: ") ||
		json.Unmarshal([]byte(strings.TrimPrefix(resetLines[1], "data: ")), &reset) != nil || reset.Reason != "draft_stale" {
		t.Fatalf("invalidated reset payload=%q parsed=%#v", frames[0], reset)
	}
}

func TestDraftStreamReaderFailureDoesNotExposeCause(t *testing.T) {
	t.Parallel()
	reader := &fakeDraftStreamReader{err: foundation.NewError(foundation.ErrorDependencyUnavailable, "CONVERSATION_DRAFT_STREAM_UNAVAILABLE", true, errors.New("postgres password=secret"))}
	response := httptest.NewRecorder()
	draftStreamRouter(NewDraftStreamHandler(reader)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, draftStreamPath(string(testWorkspaceID)), nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "password=secret") || !strings.Contains(response.Body.String(), "CONVERSATION_DRAFT_STREAM_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDraftStreamRejectsCorruptSessionOrChunkBeforeWritingSSE(t *testing.T) {
	t.Parallel()
	session := draftSession(2, agentapplication.DraftStreamActive, 3)
	corruptSession := session
	corruptSession.Binding.AnswerID = foundation.ID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	tests := []struct {
		name   string
		result agentapplication.DraftStreamReadResult
	}{
		{name: "cross answer session", result: agentapplication.DraftStreamReadResult{Session: &corruptSession}},
		{name: "chunk gap", result: agentapplication.DraftStreamReadResult{Session: &session, Chunks: []agentapplication.DraftStreamChunk{{SessionID: session.ID, Generation: 2, Sequence: 2, Content: "skipped"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			draftStreamRouter(NewDraftStreamHandler(&fakeDraftStreamReader{result: test.result})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, draftStreamPath(string(testWorkspaceID)), nil))
			if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Type") != "application/json" || strings.Contains(response.Body.String(), "event: ") {
				t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
		})
	}
}

func draftStreamRouter(handler *DraftStreamHandler) http.Handler {
	router := gin.New()
	handler.Routes(router)
	return router
}

func draftStreamPath(workspaceID string) string {
	return "/answers/" + string(testAnswerID) + "/stream?workspace_id=" + workspaceID
}

func draftSession(generation int64, status agentapplication.DraftStreamStatus, nextSequence int64) agentapplication.DraftStreamSession {
	now := time.Date(2026, 8, 9, 2, 30, 0, 0, time.UTC)
	return agentapplication.DraftStreamSession{
		ID: foundation.ID("77777777-7777-4777-8777-777777777777"),
		Binding: agentapplication.DraftStreamBinding{
			WorkspaceID: testWorkspaceID, AnswerID: testAnswerID,
			WorkflowRunID: foundation.ID("88888888-8888-4888-8888-888888888888"),
			NodeRunID:     foundation.ID("99999999-9999-4999-8999-999999999999"),
			NodeAttemptID: foundation.ID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), AttemptNo: 1, LeaseOwner: "worker-1",
		},
		Generation: generation, Status: status, NextSeq: nextSequence, ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	}
}

type fakeDraftStreamReader struct {
	mu      sync.Mutex
	result  agentapplication.DraftStreamReadResult
	err     error
	queries []agentapplication.DraftStreamReadQuery
}

func (reader *fakeDraftStreamReader) ReadDraftStream(_ context.Context, query agentapplication.DraftStreamReadQuery) (agentapplication.DraftStreamReadResult, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.queries = append(reader.queries, query)
	return reader.result, reader.err
}

func (reader *fakeDraftStreamReader) queriesSnapshot() []agentapplication.DraftStreamReadQuery {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return append([]agentapplication.DraftStreamReadQuery(nil), reader.queries...)
}

func assertDraftStreamHeaders(t *testing.T, response *http.Response) {
	t.Helper()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("Cache-Control") != "no-store" ||
		response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("unexpected stream response: status=%d headers=%v", response.StatusCode, response.Header)
	}
}

func readDraftSSEBlock(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var block strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.Fatalf("draft stream ended before complete frame: %q", block.String())
			}
			t.Fatalf("read draft SSE frame: %v", err)
		}
		block.WriteString(line)
		if line == "\n" {
			return block.String()
		}
	}
}

func assertDraftChunk(t *testing.T, frame, id string, generation, sequence int64, content string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(frame, "\n\n"), "\n")
	if len(lines) != 3 || lines[0] != "id: "+id || lines[1] != "event: "+draftStreamChunkEvent || !strings.HasPrefix(lines[2], "data: ") {
		t.Fatalf("invalid chunk frame=%q", frame)
	}
	var payload draftChunkPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[2], "data: ")), &payload); err != nil || payload.Generation != generation || payload.Sequence != sequence || payload.Content != content {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
}

func splitDraftFrames(body string) []string {
	parts := strings.Split(body, "\n\n")
	frames := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			frames = append(frames, part+"\n\n")
		}
	}
	return frames
}
