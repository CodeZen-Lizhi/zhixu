package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestTimelineCursorRejectsTamperWorkspaceAndFilterReuse(t *testing.T) {
	workspaceID := testID(1)
	now := testTime()
	newer := timelineApplicationEvent(testID(11), workspaceID, now.Add(time.Second))
	older := timelineApplicationEvent(testID(10), workspaceID, now)
	reader := &timelineReaderStub{page: domain.TimelinePage{
		Items: []domain.KnowledgeEvent{newer, older}, HasMore: true,
		Next: &domain.TimelinePosition{OccurredAt: older.OccurredAt, ID: older.ID},
	}}
	codec, err := NewTimelineCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewTimelineService(reader, codec)
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.List(context.Background(), TimelineListRequest{WorkspaceID: workspaceID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == "" || !first.HasMore || len(first.Items) != 2 {
		t.Fatalf("unexpected first page: %#v", first)
	}

	tampered := first.NextCursor[:len(first.NextCursor)-1] + "A"
	_, err = service.List(context.Background(), TimelineListRequest{WorkspaceID: workspaceID, Limit: 2, Cursor: tampered})
	assertApplicationCode(t, err, domain.ErrorCodeTimelineCursorInvalid)
	_, err = service.List(context.Background(), TimelineListRequest{WorkspaceID: testID(2), Limit: 2, Cursor: first.NextCursor})
	assertApplicationCode(t, err, domain.ErrorCodeTimelineCursorInvalid)
	_, err = service.List(context.Background(), TimelineListRequest{WorkspaceID: workspaceID, Limit: 2, Cursor: first.NextCursor, Filter: domain.TimelineFilter{EventTypes: []domain.EventType{domain.EventGitCommitted}}})
	assertApplicationCode(t, err, domain.ErrorCodeTimelineCursorInvalid)
	if reader.listCalls != 1 {
		t.Fatalf("invalid cursors reached reader: calls=%d", reader.listCalls)
	}
}

func TestTimelineCursorRejectsExpiredCursorBeforeRepositoryRead(t *testing.T) {
	workspaceID := testID(1)
	now := testTime()
	older := timelineApplicationEvent(testID(10), workspaceID, now)
	reader := &timelineReaderStub{page: domain.TimelinePage{
		Items: []domain.KnowledgeEvent{older}, HasMore: true,
		Next: &domain.TimelinePosition{OccurredAt: older.OccurredAt, ID: older.ID},
	}}
	clock := &mutableTimelineClock{now: now}
	codec, err := newTimelineCursorCodec([]byte("0123456789abcdef0123456789abcdef"), clock, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewTimelineService(reader, codec)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.List(context.Background(), TimelineListRequest{WorkspaceID: workspaceID, Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	clock.now = now.Add(time.Minute)
	_, err = service.List(context.Background(), TimelineListRequest{WorkspaceID: workspaceID, Limit: 1, Cursor: first.NextCursor})
	assertApplicationCode(t, err, domain.ErrorCodeTimelineCursorInvalid)
	if reader.listCalls != 1 {
		t.Fatalf("expired cursor reached reader: calls=%d", reader.listCalls)
	}
}

func TestTimelineProjectCanonicalizesPostgreSQLTimestampPrecision(t *testing.T) {
	codec, err := NewTimelineCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewTimelineService(&timelineReaderStub{}, codec)
	if err != nil {
		t.Fatal(err)
	}
	event := timelineApplicationEvent(testID(11), testID(1), testTime().Add(789*time.Nanosecond))
	projector := &timelineProjectorStub{}
	persisted, _, err := service.Project(context.Background(), projector, event)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.OccurredAt.Nanosecond()%int(time.Microsecond) != 0 || persisted.OccurredAt.Location() != time.UTC || !persisted.OccurredAt.Equal(projector.event.OccurredAt) || !persisted.CreatedAt.Equal(projector.event.CreatedAt) {
		t.Fatalf("persisted=%#v projected=%#v", persisted, projector.event)
	}
}

func TestTimelineServiceRejectsUnstableOrderingAndMismatchedNextPosition(t *testing.T) {
	workspaceID := testID(1)
	older := timelineApplicationEvent(testID(10), workspaceID, testTime())
	newer := timelineApplicationEvent(testID(11), workspaceID, testTime().Add(time.Second))
	codec, err := NewTimelineCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		page domain.TimelinePage
	}{
		{name: "unstable order", page: domain.TimelinePage{Items: []domain.KnowledgeEvent{older, newer}}},
		{name: "mismatched next", page: domain.TimelinePage{Items: []domain.KnowledgeEvent{newer, older}, HasMore: true, Next: &domain.TimelinePosition{OccurredAt: newer.OccurredAt, ID: newer.ID}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, createErr := NewTimelineService(&timelineReaderStub{page: test.page}, codec)
			if createErr != nil {
				t.Fatal(createErr)
			}
			_, listErr := service.List(context.Background(), TimelineListRequest{WorkspaceID: workspaceID, Limit: 2})
			if errorCode(listErr) != domain.ErrorCodeTimelineInconsistent {
				t.Fatalf("error=%v", listErr)
			}
		})
	}
}

type timelineReaderStub struct {
	page      domain.TimelinePage
	event     domain.KnowledgeEvent
	listCalls int
}

type timelineProjectorStub struct{ event domain.KnowledgeEvent }

func (projector *timelineProjectorStub) AppendEvent(_ context.Context, event domain.KnowledgeEvent) (domain.KnowledgeEvent, bool, error) {
	projector.event = event
	return event, false, nil
}

type mutableTimelineClock struct{ now time.Time }

func (clock *mutableTimelineClock) Now() time.Time { return clock.now }

func (reader *timelineReaderStub) ListEvents(_ context.Context, _ domain.TimelineQuery) (domain.TimelinePage, error) {
	reader.listCalls++
	return reader.page, nil
}

func (reader *timelineReaderStub) GetEvent(_ context.Context, _, _ foundation.ID) (domain.KnowledgeEvent, error) {
	return reader.event, nil
}

func timelineApplicationEvent(id, workspaceID foundation.ID, occurredAt time.Time) domain.KnowledgeEvent {
	aggregateID := testID(20)
	return domain.KnowledgeEvent{
		ID: id, WorkspaceID: workspaceID, EventType: domain.EventConflictResolved, AggregateType: domain.TimelineAggregateConflict,
		AggregateID: &aggregateID, SourceEventRef: "conflict:resolved:" + string(id), SourceRef: "conflict:" + string(aggregateID),
		EventVersion: 1, SchemaVersion: domain.KnowledgeEventSchemaVersion, Summary: "conflict resolved", Payload: json.RawMessage(`{}`),
		OccurredAt: occurredAt.UTC(), CreatedAt: occurredAt.UTC(),
	}
}

func assertApplicationCode(t *testing.T, err error, want string) {
	t.Helper()
	if errorCode(err) != want {
		t.Fatalf("error=%v want=%s", err, want)
	}
}
