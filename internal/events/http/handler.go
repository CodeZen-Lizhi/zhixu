package eventshttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/gin-gonic/gin"
)

const (
	// ErrorCodeWorkspaceInvalid 表示 SSE 请求未携带唯一、规范的 Workspace ID。
	ErrorCodeWorkspaceInvalid = "SSE_WORKSPACE_INVALID"
	// ErrorCodeServiceUnavailable 表示 SSE 重放服务尚未构造。
	ErrorCodeServiceUnavailable = "SSE_SERVICE_UNAVAILABLE"
	// ErrorCodeStreamingUnsupported 表示当前 HTTP Writer 不支持及时 Flush。
	ErrorCodeStreamingUnsupported = "SSE_STREAMING_UNSUPPORTED"

	defaultPollInterval      = time.Second
	defaultHeartbeatInterval = 15 * time.Second
	heartbeatFrame           = ": heartbeat\n\n"
)

type eventStreamFormat uint8

const (
	eventStreamFormatLegacy eventStreamFormat = iota
	eventStreamFormatMessage
)

// StreamConfig 配置短轮询与空闲心跳间隔；零值使用生产默认值。
type StreamConfig struct {
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
}

// Handler 把持久事件 Store 映射为 Last-Event-ID SSE 协议。
type Handler struct {
	store             application.Store
	pollInterval      time.Duration
	heartbeatInterval time.Duration
}

// NewHandler 创建 Events HTTP Handler；缺失 Store 时路由保留并显式返回 503。
func NewHandler(store application.Store, configs ...StreamConfig) *Handler {
	config := StreamConfig{}
	if len(configs) > 0 {
		config = configs[0]
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = defaultHeartbeatInterval
	}
	return &Handler{store: store, pollInterval: config.PollInterval, heartbeatInterval: config.HeartbeatInterval}
}

// Routes 在既有 `/api/v1` Router 下注册 Events SSE 路由。
func (handler *Handler) Routes(router gin.IRouter) {
	router.GET("/events", httpapi.GinHandler(handler.handleEvents))
}

func (handler *Handler) handleEvents(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if handler == nil || isNilStore(handler.store) {
		httpapi.WriteProblem(writer, http.StatusServiceUnavailable, ErrorCodeServiceUnavailable, "事件流服务暂不可用", true, nil)
		return
	}
	replayRequest, err := parseReplayRequest(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	prepared, err := handler.prepare(
		request.Context(),
		replayRequest.workspaceID,
		replayRequest.cursorText,
		replayRequest.hasCursor,
		replayRequest.format,
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		httpapi.WriteProblem(writer, http.StatusInternalServerError, ErrorCodeStreamingUnsupported, "当前连接不支持事件流", false, nil)
		return
	}

	header := writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	if len(prepared.page) > 0 {
		if _, err := writer.Write(prepared.page); err != nil {
			return
		}
	}
	flusher.Flush()
	handler.stream(
		request.Context(),
		writer,
		flusher,
		replayRequest.workspaceID,
		prepared.afterSeq,
		prepared.continueDrain,
		replayRequest.format,
	)
}

type preparedStream struct {
	page          []byte
	afterSeq      int64
	continueDrain bool
}

func (handler *Handler) prepare(
	ctx context.Context,
	workspaceID foundation.ID,
	cursorText string,
	hasCursor bool,
	format eventStreamFormat,
) (preparedStream, error) {
	afterSeq, cursor, watermark, err := handler.resolveReplayStart(ctx, workspaceID, cursorText, hasCursor)
	if err != nil {
		return preparedStream{}, err
	}
	events, err := handler.store.ListAfter(ctx, workspaceID, afterSeq, domain.MaxReplayPageSize)
	if err != nil {
		return preparedStream{}, err
	}
	if len(events) > domain.MaxReplayPageSize {
		return preparedStream{}, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEventCorrupt, false, errors.New("SSE replay page exceeds its bound"))
	}
	if cursor != nil {
		earliest, err := handler.store.EarliestRetained(ctx, workspaceID)
		if err != nil {
			return preparedStream{}, err
		}
		if err := domain.ValidateReplayCursor(*cursor, watermark, earliest); err != nil {
			return preparedStream{}, err
		}
	}
	page, lastSequence, err := encodePage(events, workspaceID, afterSeq, format)
	if err != nil {
		return preparedStream{}, err
	}
	return preparedStream{page: page, afterSeq: lastSequence, continueDrain: len(events) == domain.MaxReplayPageSize}, nil
}

func (handler *Handler) resolveReplayStart(ctx context.Context, workspaceID foundation.ID, cursorText string, hasCursor bool) (int64, *domain.ReplayCursor, int64, error) {
	if !hasCursor {
		watermark, err := handler.store.CurrentWatermark(ctx, workspaceID)
		return watermark, nil, watermark, err
	}
	cursor, err := domain.ParseReplayCursor(cursorText)
	if err != nil {
		return 0, nil, 0, err
	}
	watermark, err := handler.store.CurrentWatermark(ctx, workspaceID)
	if err != nil {
		return 0, nil, 0, err
	}
	if cursor.AfterSeq > watermark {
		return 0, nil, 0, domain.ValidateReplayCursor(cursor, watermark, nil)
	}
	earliest, err := handler.store.EarliestRetained(ctx, workspaceID)
	if err != nil {
		return 0, nil, 0, err
	}
	if err := domain.ValidateReplayCursor(cursor, watermark, earliest); err != nil {
		return 0, nil, 0, err
	}
	return cursor.AfterSeq, &cursor, watermark, nil
}

func (handler *Handler) stream(
	ctx context.Context,
	writer http.ResponseWriter,
	flusher http.Flusher,
	workspaceID foundation.ID,
	afterSeq int64,
	continueDrain bool,
	format eventStreamFormat,
) {
	var err error
	if continueDrain {
		afterSeq, err = handler.drain(ctx, writer, flusher, workspaceID, afterSeq, format)
		if err != nil {
			return
		}
	}
	poll := time.NewTicker(handler.pollInterval)
	heartbeat := time.NewTicker(handler.heartbeatInterval)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			afterSeq, err = handler.drain(ctx, writer, flusher, workspaceID, afterSeq, format)
			if err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := writer.Write([]byte(heartbeatFrame)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (handler *Handler) drain(
	ctx context.Context,
	writer http.ResponseWriter,
	flusher http.Flusher,
	workspaceID foundation.ID,
	afterSeq int64,
	format eventStreamFormat,
) (int64, error) {
	for {
		if err := ctx.Err(); err != nil {
			return afterSeq, err
		}
		events, err := handler.store.ListAfter(ctx, workspaceID, afterSeq, domain.MaxReplayPageSize)
		if err != nil {
			return afterSeq, err
		}
		if len(events) == 0 {
			return afterSeq, nil
		}
		if len(events) > domain.MaxReplayPageSize {
			return afterSeq, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEventCorrupt, false, errors.New("SSE replay page exceeds its bound"))
		}
		page, lastSequence, err := encodePage(events, workspaceID, afterSeq, format)
		if err != nil {
			return afterSeq, err
		}
		if _, err := writer.Write(page); err != nil {
			return afterSeq, err
		}
		flusher.Flush()
		afterSeq = lastSequence
		if len(events) < domain.MaxReplayPageSize {
			return afterSeq, nil
		}
	}
}

func encodePage(
	events []domain.ServerEvent,
	workspaceID foundation.ID,
	afterSeq int64,
	format eventStreamFormat,
) ([]byte, int64, error) {
	var page bytes.Buffer
	lastSequence := afterSeq
	for _, event := range events {
		if event.WorkspaceID != workspaceID || event.Seq <= lastSequence {
			return nil, afterSeq, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEventCorrupt, false, errors.New("SSE replay page scope or order is inconsistent"))
		}
		envelope, err := event.Envelope()
		if err != nil {
			return nil, afterSeq, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEventCorrupt, false, err)
		}
		data, err := json.Marshal(envelope)
		if err != nil || bytes.ContainsAny(data, "\r\n") {
			return nil, afterSeq, foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEventCorrupt, false, errors.New("SSE envelope cannot be encoded on one line"))
		}
		if format == eventStreamFormatMessage {
			_, _ = fmt.Fprintf(&page, "id: %d\ndata: %s\n\n", event.Seq, data)
		} else {
			_, _ = fmt.Fprintf(&page, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, data)
		}
		lastSequence = event.Seq
	}
	return page.Bytes(), lastSequence, nil
}

type replayRequest struct {
	workspaceID foundation.ID
	cursorText  string
	hasCursor   bool
	format      eventStreamFormat
}

func parseReplayRequest(request *http.Request) (replayRequest, error) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(query) < 1 || len(query) > 3 {
		return replayRequest{}, workspaceInvalid()
	}
	for key := range query {
		if key != "workspace_id" && key != "last_event_id" && key != "event_format" {
			return replayRequest{}, workspaceInvalid()
		}
	}
	workspaces, ok := query["workspace_id"]
	if !ok || len(workspaces) != 1 || workspaces[0] == "" {
		return replayRequest{}, workspaceInvalid()
	}
	workspaceText := workspaces[0]
	workspaceID, err := foundation.ParseID(workspaceText)
	if err != nil || string(workspaceID) != workspaceText {
		return replayRequest{}, workspaceInvalid()
	}

	format := eventStreamFormatLegacy
	if formats, present := query["event_format"]; present {
		if len(formats) != 1 || formats[0] != "message" {
			return replayRequest{}, workspaceInvalid()
		}
		format = eventStreamFormatMessage
	}

	queryCursor := ""
	hasQueryCursor := false
	if cursors, present := query["last_event_id"]; present {
		if len(cursors) != 1 || cursors[0] == "" {
			return replayRequest{}, cursorInvalid("last_event_id must be unique and non-empty")
		}
		if _, err := domain.ParseReplayCursor(cursors[0]); err != nil {
			return replayRequest{}, err
		}
		queryCursor = cursors[0]
		hasQueryCursor = true
	}

	canonicalHeader := http.CanonicalHeaderKey("Last-Event-ID")
	values, present := request.Header[canonicalHeader]
	if !present {
		return replayRequest{
			workspaceID: workspaceID,
			cursorText:  queryCursor,
			hasCursor:   hasQueryCursor,
			format:      format,
		}, nil
	}
	if len(values) != 1 || values[0] == "" {
		return replayRequest{}, cursorInvalid("Last-Event-ID must be unique and non-empty")
	}
	return replayRequest{workspaceID: workspaceID, cursorText: values[0], hasCursor: true, format: format}, nil
}

func cursorInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCursorInvalid, false, errors.New(message))
}

func workspaceInvalid() error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceInvalid, false, errors.New("SSE workspace identity is invalid"))
}

func writeError(writer http.ResponseWriter, err error) {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(writer, http.StatusInternalServerError, "SSE_INTERNAL_ERROR", "事件流请求未完成", false, nil)
		return
	}
	details := map[string]any(nil)
	if classified.Code == domain.ErrorCodeCursorExpired {
		details = map[string]any{"action": "refetch"}
	}
	httpapi.WriteProblem(writer, httpapi.StatusForErrorKind(classified.Kind), classified.Code, publicMessage(classified.Code), classified.Retryable, details)
}

func publicMessage(code string) string {
	if strings.TrimSpace(code) == "" {
		return "事件流请求未完成"
	}
	return "事件流请求未完成：" + code
}

func isNilStore(store application.Store) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
