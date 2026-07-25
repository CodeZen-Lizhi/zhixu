package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type recordingRepository struct {
	result   domain.Event
	replayed bool
	err      error
	calls    int
	received domain.Event
}

func (repository *recordingRepository) Append(_ context.Context, event domain.Event) (domain.Event, bool, error) {
	repository.calls++
	repository.received = event
	if repository.err != nil {
		return domain.Event{}, false, repository.err
	}
	if repository.result.ID == "" {
		repository.result = event
	}
	return repository.result, repository.replayed, nil
}

func TestRecorderRedactsBeforeRepositoryAndPreservesExactReplay(t *testing.T) {
	event := testEvent()
	event.Metadata = json.RawMessage(`{"nested":{"token":"do-not-store"},"content_hash":"sha256:ok"}`)
	redacted, err := event.Redacted()
	if err != nil {
		t.Fatalf("Redacted: %v", err)
	}
	redacted.ID = foundation.ID("10000000-0000-4000-8000-000000000012")
	repository := &recordingRepository{result: redacted, replayed: true}
	recorder, err := NewRecorder(repository)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	got, replayed, err := recorder.Record(context.Background(), event)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !replayed || repository.calls != 1 || !domain.EqualBinding(got, redacted) {
		t.Fatalf("got=%#v replayed=%t calls=%d", got, replayed, repository.calls)
	}
	if strings.Contains(string(repository.received.Metadata), "do-not-store") {
		t.Fatalf("repository received plaintext metadata: %s", repository.received.Metadata)
	}
	if !strings.Contains(string(repository.received.Metadata), `\u003credacted\u003e`) && !strings.Contains(string(repository.received.Metadata), domain.RedactedValue) {
		t.Fatalf("repository did not receive the redacted marker: %s", repository.received.Metadata)
	}
}

func TestRecorderRejectsRepositoryBindingDrift(t *testing.T) {
	event := testEvent()
	redacted, err := event.Redacted()
	if err != nil {
		t.Fatalf("Redacted: %v", err)
	}
	drifted := redacted
	drifted.Action = "security.block"
	repository := &recordingRepository{result: drifted}
	recorder, err := NewRecorder(repository)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	_, _, err = recorder.Record(context.Background(), event)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation {
		t.Fatalf("error=%v, want consistency violation", err)
	}
}

func TestRecorderRejectsChangedIDForNewEvent(t *testing.T) {
	event := testEvent()
	redacted, err := event.Redacted()
	if err != nil {
		t.Fatalf("Redacted: %v", err)
	}
	redacted.ID = foundation.ID("10000000-0000-4000-8000-000000000011")
	repository := &recordingRepository{result: redacted, replayed: false}
	recorder, err := NewRecorder(repository)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	_, _, err = recorder.Record(context.Background(), event)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation {
		t.Fatalf("error=%v, want consistency violation", err)
	}
}

func TestRecorderRejectsNilContext(t *testing.T) {
	recorder, err := NewRecorder(&recordingRepository{})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	_, _, err = recorder.Record(nil, testEvent())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeInvalid {
		t.Fatalf("error=%v, want invalid audit context", err)
	}
}

func testEvent() domain.Event {
	return domain.Event{
		ID:             foundation.ID("10000000-0000-4000-8000-000000000010"),
		ActorType:      domain.ActorSystem,
		Action:         "security.check",
		Outcome:        domain.OutcomeSucceeded,
		IdempotencyKey: "record-key-1",
		Correlation:    json.RawMessage(`{"request_id":"request-1"}`),
		Metadata:       json.RawMessage(`{"summary_hash":"sha256:ok"}`),
		SchemaVersion:  domain.SchemaVersion,
		OccurredAt:     time.Date(2026, 7, 23, 8, 9, 10, 123456000, time.UTC),
	}
}
