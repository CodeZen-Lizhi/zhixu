package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const timelineCursorSchema = "knowledge-timeline-cursor/v1"

const (
	// DefaultTimelineLimit 是 Timeline 默认页大小。
	DefaultTimelineLimit = 25
	// DefaultTimelineCursorTTL 限制单个 Timeline cursor 的有效期。
	DefaultTimelineCursorTTL = 15 * time.Minute
)

// TimelineReader 是 Timeline 只读适配器边界。
type TimelineReader interface {
	ListEvents(context.Context, domain.TimelineQuery) (domain.TimelinePage, error)
	GetEvent(context.Context, foundation.ID, foundation.ID) (domain.KnowledgeEvent, error)
}

// EventProjector 是受信任领域事务向 Timeline 投影事件的窄接口。
// HTTP 层不暴露该接口，因此客户端不能直接写 Knowledge Event。
type EventProjector interface {
	AppendEvent(context.Context, domain.KnowledgeEvent) (domain.KnowledgeEvent, bool, error)
}

// TimelineListRequest 是 HTTP/Application 共用的外部查询请求。
type TimelineListRequest struct {
	WorkspaceID foundation.ID
	Filter      domain.TimelineFilter
	Limit       int
	Cursor      string
}

// TimelinePage 是带 opaque cursor 的应用层结果。
type TimelinePage struct {
	WorkspaceID foundation.ID
	Items       []domain.KnowledgeEvent
	NextCursor  string
	HasMore     bool
}

// TimelineService 负责 Timeline 查询、cursor 绑定和投影边界校验。
type TimelineService struct {
	reader TimelineReader
	codec  *TimelineCursorCodec
}

// NewTimelineService 构造 Timeline 查询服务。
func NewTimelineService(reader TimelineReader, codec *TimelineCursorCodec) (*TimelineService, error) {
	if isNil(reader) || codec == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTimelineUnavailable, true, errors.New("timeline dependencies are unavailable"))
	}
	return &TimelineService{reader: reader, codec: codec}, nil
}

// List 返回 Workspace 隔离、稳定排序且有界的 Timeline 页面。
func (service *TimelineService) List(ctx context.Context, request TimelineListRequest) (TimelinePage, error) {
	if service == nil || isNil(service.reader) || service.codec == nil {
		return TimelinePage{}, timelineUnavailable("timeline service is unavailable")
	}
	if ctx == nil || !validID(request.WorkspaceID) {
		return TimelinePage{}, timelineInvalid("timeline workspace is invalid")
	}
	limit := request.Limit
	if limit == 0 {
		limit = DefaultTimelineLimit
	}
	if limit < 1 || limit > domain.MaxTimelineLimit {
		return TimelinePage{}, timelineInvalid("timeline limit is outside the bounded range")
	}
	filter := cloneFilter(request.Filter)
	if err := validateFilter(filter); err != nil {
		return TimelinePage{}, err
	}
	query := domain.TimelineQuery{WorkspaceID: request.WorkspaceID, Filter: filter, Limit: limit}
	binding := cursorBinding{Schema: timelineCursorSchema, WorkspaceID: request.WorkspaceID, Filter: canonicalFilter(filter), Limit: limit}
	if strings.TrimSpace(request.Cursor) != "" {
		position, err := service.codec.Decode(request.Cursor, binding)
		if err != nil {
			return TimelinePage{}, err
		}
		query.After = &position
	}
	result, err := service.reader.ListEvents(ctx, query)
	if err != nil {
		return TimelinePage{}, err
	}
	if len(result.Items) > limit {
		return TimelinePage{}, timelineInconsistent("timeline repository returned an unbounded page")
	}
	for index, event := range result.Items {
		if event.WorkspaceID != request.WorkspaceID {
			return TimelinePage{}, timelineInconsistent("timeline repository crossed workspace boundary")
		}
		if err := event.Validate(); err != nil {
			return TimelinePage{}, timelineInconsistent("timeline repository returned an invalid event")
		}
		if index > 0 && !timelinePositionBefore(result.Items[index-1], event) {
			return TimelinePage{}, timelineInconsistent("timeline repository returned unstable ordering")
		}
	}
	page := TimelinePage{WorkspaceID: request.WorkspaceID, Items: append([]domain.KnowledgeEvent(nil), result.Items...), HasMore: result.HasMore}
	if result.HasMore {
		if result.Next == nil || len(result.Items) == 0 {
			return TimelinePage{}, timelineInconsistent("timeline repository omitted next position")
		}
		last := result.Items[len(result.Items)-1]
		if !result.Next.OccurredAt.Equal(last.OccurredAt) || result.Next.ID != last.ID {
			return TimelinePage{}, timelineInconsistent("timeline repository returned a mismatched next position")
		}
		page.NextCursor, err = service.codec.Encode(cursorDocument{Binding: binding, Position: *result.Next})
		if err != nil {
			return TimelinePage{}, err
		}
	} else if result.Next != nil {
		return TimelinePage{}, timelineInconsistent("timeline repository returned an unexpected next position")
	}
	return page, nil
}

// Get 返回 Workspace 隔离的单个 Timeline 事件。
func (service *TimelineService) Get(ctx context.Context, workspaceID, eventID foundation.ID) (domain.KnowledgeEvent, error) {
	if service == nil || isNil(service.reader) {
		return domain.KnowledgeEvent{}, timelineUnavailable("timeline service is unavailable")
	}
	if ctx == nil || !validID(workspaceID) || !validID(eventID) {
		return domain.KnowledgeEvent{}, timelineInvalid("timeline event identity is invalid")
	}
	event, err := service.reader.GetEvent(ctx, workspaceID, eventID)
	if err != nil {
		return domain.KnowledgeEvent{}, err
	}
	if event.WorkspaceID != workspaceID || event.ID != eventID {
		return domain.KnowledgeEvent{}, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeTimelineNotFound, false, errors.New("timeline event is not visible in workspace"))
	}
	if err := event.Validate(); err != nil {
		return domain.KnowledgeEvent{}, timelineInconsistent("timeline event projection is invalid")
	}
	return event, nil
}

// Project 将正式状态变化投影为不可变 Timeline 事件；不会触碰 Knowledge 主事实。
func (service *TimelineService) Project(ctx context.Context, projector EventProjector, event domain.KnowledgeEvent) (domain.KnowledgeEvent, bool, error) {
	if service == nil || ctx == nil || isNil(projector) {
		return domain.KnowledgeEvent{}, false, timelineUnavailable("timeline projector is unavailable")
	}
	event.OccurredAt = domain.CanonicalTimelineTime(event.OccurredAt)
	event.CreatedAt = domain.CanonicalTimelineTime(event.CreatedAt)
	if err := event.Validate(); err != nil {
		return domain.KnowledgeEvent{}, false, err
	}
	return projector.AppendEvent(ctx, event)
}

func timelinePositionBefore(previous, current domain.KnowledgeEvent) bool {
	if previous.OccurredAt.After(current.OccurredAt) {
		return true
	}
	return previous.OccurredAt.Equal(current.OccurredAt) && previous.ID > current.ID
}

// TimelineCursorCodec 对 cursor 做 HMAC 与签发时间绑定，防止跨 Workspace/过滤器或过期重放。
type TimelineCursorCodec struct {
	key   []byte
	clock foundation.Clock
	ttl   time.Duration
}

// NewTimelineCursorCodec 创建 cursor codec；密钥至少 32 字节。
func NewTimelineCursorCodec(key []byte) (*TimelineCursorCodec, error) {
	return newTimelineCursorCodec(key, foundation.SystemClock{}, DefaultTimelineCursorTTL)
}

func newTimelineCursorCodec(key []byte, clock foundation.Clock, ttl time.Duration) (*TimelineCursorCodec, error) {
	if len(key) < 32 {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTimelineUnavailable, true, errors.New("timeline cursor key is too short"))
	}
	if isNil(clock) || ttl <= 0 {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTimelineUnavailable, true, errors.New("timeline cursor clock or ttl is unavailable"))
	}
	return &TimelineCursorCodec{key: append([]byte(nil), key...), clock: clock, ttl: ttl}, nil
}

type cursorBinding struct {
	Schema      string                  `json:"schema"`
	WorkspaceID foundation.ID           `json:"workspace_id"`
	Filter      canonicalTimelineFilter `json:"filter"`
	Limit       int                     `json:"limit"`
}

type canonicalTimelineFilter struct {
	EventTypes     []domain.EventType           `json:"event_types,omitempty"`
	AggregateType  domain.TimelineAggregateType `json:"aggregate_type,omitempty"`
	AggregateID    *foundation.ID               `json:"aggregate_id,omitempty"`
	SourceEventRef string                       `json:"source_event_ref,omitempty"`
	OccurredAfter  string                       `json:"occurred_after,omitempty"`
	OccurredBefore string                       `json:"occurred_before,omitempty"`
}

type cursorDocument struct {
	Binding  cursorBinding           `json:"binding"`
	Position domain.TimelinePosition `json:"position"`
	IssuedAt time.Time               `json:"issued_at"`
	MAC      string                  `json:"mac"`
}

// Encode 签发绑定当前查询的 opaque cursor。
func (codec *TimelineCursorCodec) Encode(document cursorDocument) (string, error) {
	if codec == nil || len(codec.key) < 32 || isNil(codec.clock) || codec.ttl <= 0 || document.Binding.Schema != timelineCursorSchema || !validID(document.Binding.WorkspaceID) || document.Binding.Limit < 1 || document.Binding.Limit > domain.MaxTimelineLimit || !validID(document.Position.ID) {
		return "", timelineInvalid("timeline cursor document is invalid")
	}
	document.Position.OccurredAt = domain.CanonicalTimelineTime(document.Position.OccurredAt)
	document.IssuedAt = domain.CanonicalTimelineTime(codec.clock.Now())
	if document.Position.OccurredAt.IsZero() || document.IssuedAt.IsZero() {
		return "", timelineUnavailable("timeline cursor clock is unavailable")
	}
	document.MAC = ""
	payload, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, codec.key)
	_, _ = h.Write(payload)
	document.MAC = base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	raw, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode 验证签名、请求绑定和 cursor 结构。
func (codec *TimelineCursorCodec) Decode(raw string, binding cursorBinding) (domain.TimelinePosition, error) {
	if codec == nil || len(codec.key) < 32 || isNil(codec.clock) || codec.ttl <= 0 || raw == "" || len(raw) > 4096 || binding.Schema != timelineCursorSchema {
		return domain.TimelinePosition{}, timelineCursorInvalid("timeline cursor is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return domain.TimelinePosition{}, timelineCursorInvalid("timeline cursor is invalid")
	}
	var document cursorDocument
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || decoder.Decode(&struct{}{}) != io.EOF || document.MAC == "" || !validID(document.Position.ID) || document.Position.OccurredAt.IsZero() || document.IssuedAt.IsZero() {
		return domain.TimelinePosition{}, timelineCursorInvalid("timeline cursor is invalid")
	}
	macValue, err := base64.RawURLEncoding.DecodeString(document.MAC)
	if err != nil {
		return domain.TimelinePosition{}, timelineCursorInvalid("timeline cursor is invalid")
	}
	expected := document
	expected.MAC = ""
	payload, err := json.Marshal(expected)
	if err != nil {
		return domain.TimelinePosition{}, timelineCursorInvalid("timeline cursor is invalid")
	}
	h := hmac.New(sha256.New, codec.key)
	_, _ = h.Write(payload)
	if !hmac.Equal(macValue, h.Sum(nil)) {
		return domain.TimelinePosition{}, timelineCursorInvalid("timeline cursor is invalid")
	}
	now := domain.CanonicalTimelineTime(codec.clock.Now())
	if now.IsZero() {
		return domain.TimelinePosition{}, timelineUnavailable("timeline cursor clock is unavailable")
	}
	if !reflect.DeepEqual(document.Binding, binding) ||
		!document.Position.OccurredAt.Equal(domain.CanonicalTimelineTime(document.Position.OccurredAt)) ||
		!document.IssuedAt.Equal(domain.CanonicalTimelineTime(document.IssuedAt)) ||
		document.IssuedAt.After(now) || !now.Before(document.IssuedAt.Add(codec.ttl)) {
		return domain.TimelinePosition{}, timelineCursorInvalid("timeline cursor is invalid")
	}
	return document.Position, nil
}

func canonicalFilter(filter domain.TimelineFilter) canonicalTimelineFilter {
	eventTypes := append([]domain.EventType(nil), filter.EventTypes...)
	sort.Slice(eventTypes, func(left, right int) bool { return eventTypes[left] < eventTypes[right] })
	result := canonicalTimelineFilter{EventTypes: eventTypes, AggregateType: filter.AggregateType, AggregateID: filter.AggregateID, SourceEventRef: filter.SourceEventRef}
	if filter.OccurredAfter != nil {
		result.OccurredAfter = domain.CanonicalTimelineTime(*filter.OccurredAfter).Format(time.RFC3339Nano)
	}
	if filter.OccurredBefore != nil {
		result.OccurredBefore = domain.CanonicalTimelineTime(*filter.OccurredBefore).Format(time.RFC3339Nano)
	}
	return result
}

func cloneFilter(filter domain.TimelineFilter) domain.TimelineFilter {
	result := filter
	result.EventTypes = append([]domain.EventType(nil), filter.EventTypes...)
	if filter.AggregateID != nil {
		id := *filter.AggregateID
		result.AggregateID = &id
	}
	if filter.OccurredAfter != nil {
		value := domain.CanonicalTimelineTime(*filter.OccurredAfter)
		result.OccurredAfter = &value
	}
	if filter.OccurredBefore != nil {
		value := domain.CanonicalTimelineTime(*filter.OccurredBefore)
		result.OccurredBefore = &value
	}
	return result
}

func validateFilter(filter domain.TimelineFilter) error {
	return domain.ValidateTimelineQuery(domain.TimelineQuery{WorkspaceID: foundation.ID("00000000-0000-4000-8000-000000000001"), Filter: filter, Limit: 1})
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func timelineInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeTimelineInvalid, false, errors.New(message))
}

func timelineCursorInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeTimelineCursorInvalid, false, errors.New(message))
}

func timelineUnavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTimelineUnavailable, true, errors.New(message))
}

func timelineInconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeTimelineInconsistent, false, errors.New(message))
}
