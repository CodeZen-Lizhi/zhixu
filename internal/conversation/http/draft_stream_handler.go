package conversationhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

const (
	// ErrorCodeDraftStreamWorkspaceInvalid means the stream query does not
	// carry exactly one canonical Workspace identity.
	ErrorCodeDraftStreamWorkspaceInvalid = "ANSWER_DRAFT_STREAM_WORKSPACE_INVALID"
	// ErrorCodeDraftStreamAnswerInvalid means the path Answer identity is not
	// a canonical UUID.
	ErrorCodeDraftStreamAnswerInvalid = "ANSWER_DRAFT_STREAM_ANSWER_INVALID"
	// ErrorCodeDraftStreamCursorInvalid means Last-Event-ID is not the exact
	// generation:sequence grammar.
	ErrorCodeDraftStreamCursorInvalid = "ANSWER_DRAFT_STREAM_CURSOR_INVALID"
	// ErrorCodeDraftStreamCursorFuture means a browser claims a chunk which
	// the current generation has not persisted.
	ErrorCodeDraftStreamCursorFuture = "ANSWER_DRAFT_STREAM_CURSOR_FUTURE"
	// ErrorCodeDraftStreamUnavailable means the transient projection reader is
	// absent from API composition.
	ErrorCodeDraftStreamUnavailable = "ANSWER_DRAFT_STREAM_UNAVAILABLE"
	// ErrorCodeDraftStreamStreamingUnsupported means the response cannot flush
	// an SSE frame promptly.
	ErrorCodeDraftStreamStreamingUnsupported = "ANSWER_DRAFT_STREAM_STREAMING_UNSUPPORTED"

	draftStreamChunkEvent = "chunk"
	draftStreamResetEvent = "reset"
	draftStreamEndEvent   = "end"

	draftStreamHeartbeat = ": heartbeat\n\n"
)

// DraftStreamConfig configures bounded database polling and idle heartbeats.
// Non-positive values use production defaults.
type DraftStreamConfig struct {
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
}

// DraftStreamHandler exposes the transient, final-answer-only draft stream.
// It intentionally has no dependency on the durable Server Event store.
type DraftStreamHandler struct {
	reader            agentapplication.DraftStreamReader
	pollInterval      time.Duration
	heartbeatInterval time.Duration
}

// NewDraftStreamHandler creates the isolated Answer draft SSE boundary.
func NewDraftStreamHandler(reader agentapplication.DraftStreamReader, configs ...DraftStreamConfig) *DraftStreamHandler {
	config := DraftStreamConfig{}
	if len(configs) > 0 {
		config = configs[0]
	}
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 15 * time.Second
	}
	return &DraftStreamHandler{reader: reader, pollInterval: config.PollInterval, heartbeatInterval: config.HeartbeatInterval}
}

// Routes registers the endpoint under an existing /api/v1 router. API
// composition places this handler behind the same authentication middleware
// as Conversation read routes.
func (handler *DraftStreamHandler) Routes(router chi.Router) {
	router.Get("/answers/{answer_id}/stream", handler.handle)
}

func (handler *DraftStreamHandler) handle(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || isNilDraftReader(handler.reader) {
		httpapi.WriteProblem(writer, http.StatusServiceUnavailable, ErrorCodeDraftStreamUnavailable, "回答草稿流暂不可用", true, nil)
		return
	}
	query, cursor, err := parseDraftStreamRequest(request)
	if err != nil {
		writeDraftStreamError(writer, err)
		return
	}
	prepared, err := handler.readAndEncode(request.Context(), query, cursor)
	if err != nil {
		writeDraftStreamError(writer, err)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		httpapi.WriteProblem(writer, http.StatusInternalServerError, ErrorCodeDraftStreamStreamingUnsupported, "当前连接不支持回答草稿流", false, nil)
		return
	}

	header := writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	if len(prepared.page) > 0 {
		if _, err := writer.Write(prepared.page); err != nil {
			return
		}
	}
	flusher.Flush()
	if prepared.end {
		return
	}
	handler.stream(request.Context(), writer, flusher, query, prepared.cursor)
}

type preparedDraftStream struct {
	page   []byte
	cursor *agentapplication.DraftStreamReadCursor
	end    bool
}

func (handler *DraftStreamHandler) readAndEncode(ctx context.Context, query agentapplication.DraftStreamReadQuery, cursor *agentapplication.DraftStreamReadCursor) (preparedDraftStream, error) {
	result, err := handler.reader.ReadDraftStream(ctx, query)
	if err != nil {
		return preparedDraftStream{}, err
	}
	page, next, end, err := encodeDraftStreamRead(result, query.WorkspaceID, query.AnswerID, cursor)
	if err != nil {
		return preparedDraftStream{}, err
	}
	return preparedDraftStream{page: page, cursor: next, end: end}, nil
}

func (handler *DraftStreamHandler) stream(ctx context.Context, writer http.ResponseWriter, flusher http.Flusher, query agentapplication.DraftStreamReadQuery, cursor *agentapplication.DraftStreamReadCursor) {
	poll := time.NewTicker(handler.pollInterval)
	heartbeat := time.NewTicker(handler.heartbeatInterval)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			query.After = cursor
			prepared, err := handler.readAndEncode(ctx, query, cursor)
			if err != nil {
				return
			}
			if len(prepared.page) > 0 {
				if _, err := writer.Write(prepared.page); err != nil {
					return
				}
				flusher.Flush()
			}
			cursor = prepared.cursor
			if prepared.end {
				return
			}
		case <-heartbeat.C:
			if _, err := writer.Write([]byte(draftStreamHeartbeat)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func parseDraftStreamRequest(request *http.Request) (agentapplication.DraftStreamReadQuery, *agentapplication.DraftStreamReadCursor, error) {
	if request == nil || request.URL == nil {
		return agentapplication.DraftStreamReadQuery{}, nil, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamWorkspaceInvalid, false, errors.New("draft stream request is invalid"))
	}
	queryValues, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(queryValues) != 1 {
		return agentapplication.DraftStreamReadQuery{}, nil, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamWorkspaceInvalid, false, errors.New("draft stream workspace query is invalid"))
	}
	workspaces, ok := queryValues["workspace_id"]
	if !ok || len(workspaces) != 1 {
		return agentapplication.DraftStreamReadQuery{}, nil, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamWorkspaceInvalid, false, errors.New("draft stream workspace query is invalid"))
	}
	workspaceID, err := parseCanonicalDraftID(workspaces[0])
	if err != nil {
		return agentapplication.DraftStreamReadQuery{}, nil, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamWorkspaceInvalid, false, err)
	}
	answerID, err := parseCanonicalDraftID(chi.URLParam(request, "answer_id"))
	if err != nil || answerID == workspaceID {
		return agentapplication.DraftStreamReadQuery{}, nil, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamAnswerInvalid, false, errors.New("draft stream answer identity is invalid"))
	}

	values, present := request.Header[http.CanonicalHeaderKey("Last-Event-ID")]
	if !present {
		return agentapplication.DraftStreamReadQuery{WorkspaceID: workspaceID, AnswerID: answerID, Limit: agentapplication.MaxDraftStreamReplayChunks}, nil, nil
	}
	if len(values) != 1 || values[0] == "" {
		return agentapplication.DraftStreamReadQuery{}, nil, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamCursorInvalid, false, errors.New("Last-Event-ID must be unique and non-empty"))
	}
	cursor, err := parseDraftStreamCursor(values[0])
	if err != nil {
		return agentapplication.DraftStreamReadQuery{}, nil, err
	}
	return agentapplication.DraftStreamReadQuery{WorkspaceID: workspaceID, AnswerID: answerID, After: &cursor, Limit: agentapplication.MaxDraftStreamReplayChunks}, &cursor, nil
}

func parseCanonicalDraftID(value string) (foundation.ID, error) {
	if strings.TrimSpace(value) != value || value == "" {
		return "", errors.New("draft stream identity is empty or not canonical")
	}
	id, err := foundation.ParseID(value)
	if err != nil || string(id) != value {
		return "", errors.New("draft stream identity is invalid")
	}
	return id, nil
}

func parseDraftStreamCursor(value string) (agentapplication.DraftStreamReadCursor, error) {
	if len(value) > 39 {
		return agentapplication.DraftStreamReadCursor{}, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamCursorInvalid, false, errors.New("Last-Event-ID is too long"))
	}
	generationText, sequenceText, found := strings.Cut(value, ":")
	if !found || generationText == "" || sequenceText == "" || strings.Contains(sequenceText, ":") {
		return agentapplication.DraftStreamReadCursor{}, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamCursorInvalid, false, errors.New("Last-Event-ID must be generation:sequence"))
	}
	generation, generationErr := strconv.ParseInt(generationText, 10, 64)
	sequence, sequenceErr := strconv.ParseInt(sequenceText, 10, 64)
	if generationErr != nil || sequenceErr != nil || generation <= 0 || sequence <= 0 || strconv.FormatInt(generation, 10) != generationText || strconv.FormatInt(sequence, 10) != sequenceText {
		return agentapplication.DraftStreamReadCursor{}, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamCursorInvalid, false, errors.New("Last-Event-ID is not canonical"))
	}
	return agentapplication.DraftStreamReadCursor{Generation: generation, Sequence: sequence}, nil
}

func encodeDraftStreamRead(result agentapplication.DraftStreamReadResult, workspaceID, answerID foundation.ID, cursor *agentapplication.DraftStreamReadCursor) ([]byte, *agentapplication.DraftStreamReadCursor, bool, error) {
	if result.Session == nil {
		if len(result.Chunks) != 0 {
			return nil, cursor, false, draftStreamError(foundation.ErrorNonRetryableFailure, ErrorCodeDraftStreamUnavailable, false, errors.New("draft stream chunks have no session"))
		}
		if result.Invalidated {
			page, err := encodeDraftResetAndEnd(0, "draft_stale")
			return page, cursor, true, err
		}
		if cursor == nil {
			return nil, nil, false, nil
		}
		page, err := encodeDraftResetAndEnd(0, "draft_unavailable")
		return page, cursor, true, err
	}
	session := *result.Session
	if err := validateDraftSession(session, workspaceID, answerID); err != nil {
		return nil, cursor, false, err
	}
	if cursor != nil && cursor.Generation > session.Generation {
		return nil, cursor, false, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamCursorFuture, false, errors.New("draft stream generation is ahead of the current generation"))
	}
	if cursor != nil && cursor.Generation < session.Generation {
		page, err := encodeDraftResetAndEnd(session.Generation, "generation_replaced")
		return page, cursor, true, err
	}
	if cursor != nil && cursor.Sequence >= session.NextSeq {
		return nil, cursor, false, draftStreamError(foundation.ErrorInvalidInput, ErrorCodeDraftStreamCursorFuture, false, errors.New("draft stream sequence is ahead of the current generation"))
	}
	if session.Status == agentapplication.DraftStreamPublished {
		page, err := encodeDraftEnd(session.Generation, string(session.Status), "refetch")
		return page, cursor, true, err
	}
	if session.Status == agentapplication.DraftStreamAborted || session.Status == agentapplication.DraftStreamSuperseded {
		page, err := encodeDraftResetAndEnd(session.Generation, strings.ToLower(string(session.Status)))
		return page, cursor, true, err
	}

	page, next, err := encodeDraftChunks(result.Chunks, session, cursor)
	if err != nil {
		return nil, cursor, false, err
	}
	return page, next, false, nil
}

func validateDraftSession(session agentapplication.DraftStreamSession, workspaceID, answerID foundation.ID) error {
	if !canonicalDraftSessionID(session.ID) || !canonicalDraftSessionID(session.Binding.WorkflowRunID) ||
		!canonicalDraftSessionID(session.Binding.NodeRunID) || !canonicalDraftSessionID(session.Binding.NodeAttemptID) ||
		session.Binding.WorkspaceID != workspaceID || session.Binding.AnswerID != answerID || session.Binding.AttemptNo < 1 ||
		strings.TrimSpace(session.Binding.LeaseOwner) != session.Binding.LeaseOwner || session.Binding.LeaseOwner == "" || len(session.Binding.LeaseOwner) > 256 ||
		session.Generation <= 0 || session.NextSeq <= 0 || session.TotalBytes < 0 || session.TotalBytes > agentapplication.MaxDraftStreamSessionBytes ||
		session.CreatedAt.IsZero() || session.ExpiresAt.IsZero() || !session.ExpiresAt.After(session.CreatedAt) {
		return draftStreamError(foundation.ErrorNonRetryableFailure, ErrorCodeDraftStreamUnavailable, false, errors.New("draft stream session binding is inconsistent"))
	}
	switch session.Status {
	case agentapplication.DraftStreamActive, agentapplication.DraftStreamCompleted, agentapplication.DraftStreamDegraded,
		agentapplication.DraftStreamPublished, agentapplication.DraftStreamAborted, agentapplication.DraftStreamSuperseded:
		return nil
	default:
		return draftStreamError(foundation.ErrorNonRetryableFailure, ErrorCodeDraftStreamUnavailable, false, errors.New("draft stream session status is invalid"))
	}
}

func canonicalDraftSessionID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func encodeDraftChunks(chunks []agentapplication.DraftStreamChunk, session agentapplication.DraftStreamSession, cursor *agentapplication.DraftStreamReadCursor) ([]byte, *agentapplication.DraftStreamReadCursor, error) {
	if len(chunks) > agentapplication.MaxDraftStreamReplayChunks {
		return nil, cursor, draftStreamError(foundation.ErrorNonRetryableFailure, ErrorCodeDraftStreamUnavailable, false, errors.New("draft stream page exceeds its bound"))
	}
	after := int64(0)
	if cursor != nil {
		after = cursor.Sequence
	}
	next := cursor
	var page bytes.Buffer
	for _, chunk := range chunks {
		if chunk.SessionID != session.ID || chunk.Generation != session.Generation || chunk.Sequence != after+1 ||
			chunk.Sequence >= session.NextSeq || chunk.Content == "" || len(chunk.Content) > agentapplication.MaxAnswerStreamChunkBytes || !utf8.ValidString(chunk.Content) {
			return nil, cursor, draftStreamError(foundation.ErrorNonRetryableFailure, ErrorCodeDraftStreamUnavailable, false, errors.New("draft stream chunk page is inconsistent"))
		}
		if err := writeDraftSSEFrame(&page, fmt.Sprintf("%d:%d", chunk.Generation, chunk.Sequence), draftStreamChunkEvent, draftChunkPayload{Generation: chunk.Generation, Sequence: chunk.Sequence, Content: chunk.Content}); err != nil {
			return nil, cursor, err
		}
		after = chunk.Sequence
		next = &agentapplication.DraftStreamReadCursor{Generation: chunk.Generation, Sequence: chunk.Sequence}
	}
	return page.Bytes(), next, nil
}

type draftChunkPayload struct {
	Generation int64  `json:"generation"`
	Sequence   int64  `json:"sequence"`
	Content    string `json:"content"`
}

type draftResetPayload struct {
	Generation int64  `json:"generation"`
	Reason     string `json:"reason"`
	Action     string `json:"action"`
}

type draftEndPayload struct {
	Generation int64  `json:"generation"`
	Status     string `json:"status"`
	Action     string `json:"action"`
}

func encodeDraftResetAndEnd(generation int64, reason string) ([]byte, error) {
	var page bytes.Buffer
	if err := writeDraftSSEFrame(&page, "", draftStreamResetEvent, draftResetPayload{Generation: generation, Reason: reason, Action: "refetch"}); err != nil {
		return nil, err
	}
	if err := writeDraftSSEFrame(&page, "", draftStreamEndEvent, draftEndPayload{Generation: generation, Status: "RESET", Action: "refetch"}); err != nil {
		return nil, err
	}
	return page.Bytes(), nil
}

func encodeDraftEnd(generation int64, status, action string) ([]byte, error) {
	var page bytes.Buffer
	if err := writeDraftSSEFrame(&page, "", draftStreamEndEvent, draftEndPayload{Generation: generation, Status: status, Action: action}); err != nil {
		return nil, err
	}
	return page.Bytes(), nil
}

func writeDraftSSEFrame(page *bytes.Buffer, id, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil || bytes.ContainsAny(data, "\r\n") {
		return draftStreamError(foundation.ErrorNonRetryableFailure, ErrorCodeDraftStreamUnavailable, false, errors.New("draft stream event cannot be encoded on one line"))
	}
	if id != "" {
		_, _ = fmt.Fprintf(page, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(page, "event: %s\ndata: %s\n\n", event, data)
	return nil
}

func draftStreamError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}

func writeDraftStreamError(writer http.ResponseWriter, err error) {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(writer, http.StatusInternalServerError, "ANSWER_DRAFT_STREAM_INTERNAL_ERROR", "回答草稿流请求未完成", false, nil)
		return
	}
	httpapi.WriteProblem(writer, httpapi.StatusForErrorKind(classified.Kind), classified.Code, "回答草稿流请求未完成："+classified.Code, classified.Retryable, nil)
}

func isNilDraftReader(reader agentapplication.DraftStreamReader) bool {
	if reader == nil {
		return true
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
