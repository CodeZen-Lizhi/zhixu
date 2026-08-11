package eventshttp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/gin-gonic/gin"
)

const testWorkspaceID = foundation.ID("7e000000-0000-4000-8000-000000000001")

func TestHandlerRejectsCursorErrorsBeforeStartingSSE(t *testing.T) {
	earliest := int64(4)
	tests := []struct {
		name         string
		path         string
		cursor       *string
		cursorValues []string
		store        *fakeEventStore
		status       int
		code         string
		wantAction   string
	}{
		{name: "workspace missing", path: "/events", store: &fakeEventStore{}, status: http.StatusBadRequest, code: ErrorCodeWorkspaceInvalid},
		{name: "workspace duplicate", path: "/events?workspace_id=" + string(testWorkspaceID) + "&workspace_id=" + string(testWorkspaceID), store: &fakeEventStore{}, status: http.StatusBadRequest, code: ErrorCodeWorkspaceInvalid},
		{name: "query unknown", path: eventPath() + "&unknown=value", store: &fakeEventStore{}, status: http.StatusBadRequest, code: ErrorCodeWorkspaceInvalid},
		{name: "event format empty", path: eventPath() + "&event_format=", store: &fakeEventStore{}, status: http.StatusBadRequest, code: ErrorCodeWorkspaceInvalid},
		{name: "event format invalid", path: eventPath() + "&event_format=legacy", store: &fakeEventStore{}, status: http.StatusBadRequest, code: ErrorCodeWorkspaceInvalid},
		{name: "event format duplicate", path: eventPath() + "&event_format=message&event_format=message", store: &fakeEventStore{}, status: http.StatusBadRequest, code: ErrorCodeWorkspaceInvalid},
		{name: "query cursor empty", path: eventPath() + "&last_event_id=", store: &fakeEventStore{}, status: http.StatusBadRequest, code: domain.ErrorCodeCursorInvalid},
		{name: "query cursor duplicate", path: eventPath() + "&last_event_id=4&last_event_id=5", store: &fakeEventStore{}, status: http.StatusBadRequest, code: domain.ErrorCodeCursorInvalid},
		{name: "query cursor invalid", path: eventPath() + "&last_event_id=01", store: &fakeEventStore{}, status: http.StatusBadRequest, code: domain.ErrorCodeCursorInvalid},
		{name: "header cursor empty", path: eventPath(), cursor: stringPointer(""), store: &fakeEventStore{}, status: http.StatusBadRequest, code: domain.ErrorCodeCursorInvalid},
		{name: "header cursor duplicate", path: eventPath(), cursorValues: []string{"4", "5"}, store: &fakeEventStore{}, status: http.StatusBadRequest, code: domain.ErrorCodeCursorInvalid},
		{name: "cursor invalid", path: eventPath(), cursor: stringPointer("01"), store: &fakeEventStore{}, status: http.StatusBadRequest, code: domain.ErrorCodeCursorInvalid},
		{name: "cursor future", path: eventPath(), cursor: stringPointer("6"), store: &fakeEventStore{watermark: 5}, status: http.StatusBadRequest, code: domain.ErrorCodeCursorFuture},
		{name: "cursor expired", path: eventPath(), cursor: stringPointer("3"), store: &fakeEventStore{watermark: 5, earliest: &earliest}, status: http.StatusConflict, code: domain.ErrorCodeCursorExpired, wantAction: "refetch"},
		{name: "retention empty", path: eventPath(), cursor: stringPointer("5"), store: &fakeEventStore{watermark: 5}, status: http.StatusConflict, code: domain.ErrorCodeCursorExpired, wantAction: "refetch"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(test.store)
			router := gin.New()
			handler.Routes(router)
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.cursorValues != nil {
				request.Header["Last-Event-Id"] = test.cursorValues
			} else if test.cursor != nil {
				request.Header["Last-Event-Id"] = []string{*test.cursor}
			}
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != test.status || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d content_type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
			}
			var problem httpapi.Problem
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.ErrorCode != test.code {
				t.Fatalf("problem=%#v err=%v", problem, err)
			}
			if test.wantAction != "" && problem.Details["action"] != test.wantAction {
				t.Fatalf("problem action=%v want=%q", problem.Details["action"], test.wantAction)
			}
		})
	}
}

func TestHandlerReturnsProblemWhenInitialReplayReadFails(t *testing.T) {
	store := &fakeEventStore{}
	store.list = func(context.Context, int64, int) ([]domain.ServerEvent, error) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, errors.New("database unavailable"))
	}
	handler := NewHandler(store)
	response := httptest.NewRecorder()
	eventRouter(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, eventPath(), nil))

	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("initial replay failure status=%d content_type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var problem httpapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.ErrorCode != domain.ErrorCodeStoreUnavailable || !problem.Retryable {
		t.Fatalf("initial replay problem=%#v err=%v", problem, err)
	}
}

func TestHandlerRechecksRetentionAfterInitialReplayRead(t *testing.T) {
	store := &fakeEventStore{watermark: 6}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	store.earliestRun = func(call int) *int64 {
		if call == 1 {
			return int64Pointer(4)
		}
		return int64Pointer(6)
	}
	store.list = func(context.Context, int64, int) ([]domain.ServerEvent, error) {
		cancelRequest()
		return []domain.ServerEvent{}, nil
	}
	handler := NewHandler(store)
	request := httptest.NewRequest(http.MethodGet, eventPath(), nil).WithContext(requestContext)
	request.Header.Set("Last-Event-ID", "5")
	response := httptest.NewRecorder()

	eventRouter(handler).ServeHTTP(response, request)

	if response.Code != http.StatusConflict || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("retention race status=%d content_type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var problem httpapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.ErrorCode != domain.ErrorCodeCursorExpired || problem.Details["action"] != "refetch" {
		t.Fatalf("retention race problem=%#v err=%v", problem, err)
	}
}

func TestHandlerStartsFromScopedWatermarkOrRetainedCursorAndFramesMonotonically(t *testing.T) {
	tests := []struct {
		name          string
		headerCursor  string
		queryCursor   string
		watermark     int64
		earliest      *int64
		expectedAfter int64
	}{
		{name: "fresh connection", watermark: 10, expectedAfter: 10},
		{name: "retained header replay", headerCursor: "5", watermark: 10, earliest: int64Pointer(4), expectedAfter: 5},
		{name: "retained query replay", queryCursor: "5", watermark: 10, earliest: int64Pointer(4), expectedAfter: 5},
		{name: "header overrides stale query seed", headerCursor: "7", queryCursor: "5", watermark: 10, earliest: int64Pointer(4), expectedAfter: 7},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeEventStore{watermark: test.watermark, earliest: test.earliest}
			store.list = func(ctx context.Context, after int64, limit int) ([]domain.ServerEvent, error) {
				if after != test.expectedAfter || limit != domain.MaxReplayPageSize {
					return nil, foundation.NewError(foundation.ErrorConsistencyViolation, "TEST_BOUNDARY_INVALID", false, errors.New("unexpected replay boundary"))
				}
				return []domain.ServerEvent{
					testServerEvent(after+1, "answer.completed", 2),
					testServerEvent(after+3, "workflow.run.progress", 3),
				}, nil
			}
			handler := NewHandler(store, StreamConfig{PollInterval: time.Hour, HeartbeatInterval: time.Hour})
			server := httptest.NewServer(eventRouter(handler))
			t.Cleanup(server.Close)
			ctx, cancel := context.WithCancel(context.Background())
			path := server.URL + eventPath()
			if test.queryCursor != "" {
				path += "&last_event_id=" + test.queryCursor
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.headerCursor != "" {
				request.Header.Set("Last-Event-ID", test.headerCursor)
			}
			response, err := server.Client().Do(request)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			defer response.Body.Close()

			assertStreamHeaders(t, response)
			reader := bufio.NewReader(response.Body)
			first := readSSEBlock(t, reader)
			second := readSSEBlock(t, reader)
			cancel()

			assertSSEBlock(t, first, test.expectedAfter+1, "answer.completed")
			assertSSEBlock(t, second, test.expectedAfter+3, "workflow.run.progress")
			if strings.Contains(first+second, "source_event_ref") || strings.Contains(first+second, "expires_at") {
				t.Fatalf("SSE frame leaked persistence fields: %s%s", first, second)
			}
			if store.earliestCalls != 2*boolToInt(test.headerCursor != "" || test.queryCursor != "") {
				t.Fatalf("earliest retained calls=%d header_cursor=%q query_cursor=%q", store.earliestCalls, test.headerCursor, test.queryCursor)
			}
		})
	}
}

func TestHandlerKeepsLegacyFramesAndSupportsMessageFormat(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		lineCount  int
		namedEvent bool
	}{
		{name: "legacy default", lineCount: 3, namedEvent: true},
		{name: "native message", query: "&event_format=message", lineCount: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeEventStore{watermark: 4, earliest: int64Pointer(4)}
			store.list = func(context.Context, int64, int) ([]domain.ServerEvent, error) {
				return []domain.ServerEvent{testServerEvent(5, "answer.completed", 2)}, nil
			}
			handler := NewHandler(store, StreamConfig{PollInterval: time.Hour, HeartbeatInterval: time.Hour})
			server := httptest.NewServer(eventRouter(handler))
			t.Cleanup(server.Close)
			ctx, cancel := context.WithCancel(context.Background())
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+eventPath()+test.query, nil)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			request.Header.Set("Last-Event-ID", "4")
			response, err := server.Client().Do(request)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			block := readSSEBlock(t, bufio.NewReader(response.Body))
			_ = response.Body.Close()
			cancel()

			lines := strings.Split(strings.TrimSuffix(block, "\n\n"), "\n")
			if len(lines) != test.lineCount || strings.HasPrefix(lines[1], "event: ") != test.namedEvent {
				t.Fatalf("unexpected SSE format=%q", block)
			}
			dataLine := lines[len(lines)-1]
			var decoded domain.Envelope
			if !strings.HasPrefix(dataLine, "data: ") || json.Unmarshal([]byte(strings.TrimPrefix(dataLine, "data: ")), &decoded) != nil || decoded.Type != "answer.completed" {
				t.Fatalf("unexpected SSE envelope=%q", block)
			}
		})
	}
}

func TestHandlerWritesHeartbeatCommentWhileIdle(t *testing.T) {
	store := &fakeEventStore{}
	handler := NewHandler(store, StreamConfig{PollInterval: time.Hour, HeartbeatInterval: 5 * time.Millisecond})
	server := httptest.NewServer(eventRouter(handler))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+eventPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer response.Body.Close()

	assertStreamHeaders(t, response)
	heartbeat := readSSEBlock(t, bufio.NewReader(response.Body))
	cancel()
	if heartbeat != ": heartbeat\n\n" {
		t.Fatalf("heartbeat=%q", heartbeat)
	}
}

func TestHandlerCancellationReleasesBlockedReplayQuery(t *testing.T) {
	started := make(chan struct{})
	released := make(chan struct{})
	store := &fakeEventStore{}
	var calls int
	store.list = func(ctx context.Context, _ int64, _ int) ([]domain.ServerEvent, error) {
		calls++
		if calls == 1 {
			return []domain.ServerEvent{}, nil
		}
		close(started)
		<-ctx.Done()
		close(released)
		return nil, ctx.Err()
	}
	handler := NewHandler(store, StreamConfig{PollInterval: 5 * time.Millisecond, HeartbeatInterval: time.Hour})
	server := httptest.NewServer(eventRouter(handler))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+eventPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer response.Body.Close()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("replay query did not start")
	}
	cancel()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not release replay query")
	}
}

type fakeEventStore struct {
	mu             sync.Mutex
	watermark      int64
	earliest       *int64
	list           func(context.Context, int64, int) ([]domain.ServerEvent, error)
	earliestRun    func(int) *int64
	earliestCalls  int
	watermarkCalls int
}

func (store *fakeEventStore) CurrentWatermark(context.Context, foundation.ID) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.watermarkCalls++
	return store.watermark, nil
}

func (store *fakeEventStore) EarliestRetained(context.Context, foundation.ID) (*int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.earliestCalls++
	if store.earliestRun != nil {
		value := store.earliestRun(store.earliestCalls)
		if value == nil {
			return nil, nil
		}
		copy := *value
		return &copy, nil
	}
	if store.earliest == nil {
		return nil, nil
	}
	value := *store.earliest
	return &value, nil
}

func (store *fakeEventStore) ListAfter(ctx context.Context, _ foundation.ID, after int64, limit int) ([]domain.ServerEvent, error) {
	if store.list == nil {
		return []domain.ServerEvent{}, nil
	}
	return store.list(ctx, after, limit)
}

func eventRouter(handler *Handler) http.Handler {
	router := gin.New()
	handler.Routes(router)
	return router
}

func eventPath() string {
	return "/events?workspace_id=" + string(testWorkspaceID)
}

func testServerEvent(sequence int64, eventType string, version int64) domain.ServerEvent {
	count := sequence
	occurredAt := time.Date(2026, 7, 19, 8, 9, 10, 0, time.UTC).Add(time.Duration(sequence) * time.Second)
	return domain.ServerEvent{
		Seq: sequence, WorkspaceID: testWorkspaceID, Type: eventType,
		ResourceRef: "answer:7e000000-0000-4000-8000-000000000002", ResourceVersion: version,
		PayloadSummary: domain.PayloadSummary{Status: "completed", CandidateCount: &count},
		SchemaVersion:  1, SourceEventRef: "test:" + strconv.FormatInt(sequence, 10),
		OccurredAt: occurredAt, ExpiresAt: occurredAt.Add(domain.RetentionWindow),
	}
}

func readSSEBlock(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var block strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.Fatalf("SSE stream ended before a complete frame: %q", block.String())
			}
			t.Fatalf("read SSE frame: %v", err)
		}
		block.WriteString(line)
		if line == "\n" {
			return block.String()
		}
	}
}

func assertStreamHeaders(t *testing.T, response *http.Response) {
	t.Helper()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" ||
		response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" ||
		response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("unexpected stream response: status=%d headers=%v", response.StatusCode, response.Header)
	}
}

func assertSSEBlock(t *testing.T, block string, sequence int64, eventType string) {
	t.Helper()
	assertSSEBlockForWorkspace(t, block, sequence, eventType, testWorkspaceID)
}

func assertSSEBlockForWorkspace(t *testing.T, block string, sequence int64, eventType string, workspaceID foundation.ID) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(block, "\n\n"), "\n")
	if len(lines) != 3 || lines[0] != "id: "+strconv.FormatInt(sequence, 10) || lines[1] != "event: "+eventType || !strings.HasPrefix(lines[2], "data: ") {
		t.Fatalf("invalid SSE frame=%q", block)
	}
	var envelope domain.Envelope
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[2], "data: ")), &envelope); err != nil {
		t.Fatalf("decode SSE envelope: %v", err)
	}
	if envelope.ID != strconv.FormatInt(sequence, 10) || envelope.Type != eventType || envelope.WorkspaceID != workspaceID {
		t.Fatalf("unexpected envelope=%#v", envelope)
	}
}

func assertSSEMessageBlockForWorkspace(t *testing.T, block string, sequence int64, eventType string, workspaceID foundation.ID) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(block, "\n\n"), "\n")
	if len(lines) != 2 || lines[0] != "id: "+strconv.FormatInt(sequence, 10) || !strings.HasPrefix(lines[1], "data: ") {
		t.Fatalf("invalid SSE message frame=%q", block)
	}
	var envelope domain.Envelope
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &envelope); err != nil {
		t.Fatalf("decode SSE message envelope: %v", err)
	}
	if envelope.ID != strconv.FormatInt(sequence, 10) || envelope.Type != eventType || envelope.WorkspaceID != workspaceID {
		t.Fatalf("unexpected message envelope=%#v", envelope)
	}
}

func stringPointer(value string) *string { return &value }

func int64Pointer(value int64) *int64 { return &value }

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
