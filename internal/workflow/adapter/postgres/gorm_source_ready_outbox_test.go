package workflowpostgres

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestSourceReadyOutboxDecodesOnlyExactPersistedBinding(t *testing.T) {
	ready := ingestiondomain.SourceReady{
		WorkspaceID: "12000000-0000-4000-8000-000000000001", SourceID: "12000000-0000-4000-8000-000000000002",
		SourceVersionID: "12000000-0000-4000-8000-000000000003", ContentArtifactID: "12000000-0000-4000-8000-000000000004",
		ParseProjectionID: "12000000-0000-4000-8000-000000000005", IngestionAttemptID: "12000000-0000-4000-8000-000000000006",
		ContentHash: strings.Repeat("a", 64), OccurredAt: time.Date(2026, 9, 9, 2, 0, 0, 123000, time.UTC),
	}
	payload, err := json.Marshal(ready)
	if err != nil {
		t.Fatal(err)
	}
	key := sourceReadyOutboxKey(ready)
	record := sourceReadyOutboxGORMRecord{
		ID: "12000000-0000-4000-8000-000000000007", WorkspaceID: string(ready.WorkspaceID),
		EventType: application.SourceReadyEventType, IdempotencyKey: key, EventKey: key,
		SchemaVersion: 1, EventVersion: 1, Payload: payload, OccurredAt: ready.OccurredAt,
	}
	fact, err := record.fact()
	if err != nil || fact.EventID != foundation.ID(record.ID) || fact.Ready != ready {
		t.Fatalf("decode ready fact = %#v, %v", fact, err)
	}
	cases := map[string]func(*sourceReadyOutboxGORMRecord){
		"workspace": func(value *sourceReadyOutboxGORMRecord) { value.WorkspaceID = "12000000-0000-4000-8000-000000000099" },
		"event id":  func(value *sourceReadyOutboxGORMRecord) { value.ID = "" },
		"event key": func(value *sourceReadyOutboxGORMRecord) { value.EventKey += ":different" },
		"replay key": func(value *sourceReadyOutboxGORMRecord) {
			value.IdempotencyKey += ":different"
		},
		"event type":     func(value *sourceReadyOutboxGORMRecord) { value.EventType = "workflow.other" },
		"event version":  func(value *sourceReadyOutboxGORMRecord) { value.EventVersion++ },
		"schema version": func(value *sourceReadyOutboxGORMRecord) { value.SchemaVersion++ },
		"timestamp":      func(value *sourceReadyOutboxGORMRecord) { value.OccurredAt = value.OccurredAt.Add(time.Second) },
		"workflow run": func(value *sourceReadyOutboxGORMRecord) {
			runID := "12000000-0000-4000-8000-000000000008"
			value.RunID = &runID
		},
		"unknown field": func(value *sourceReadyOutboxGORMRecord) {
			value.Payload = workflowJSONB(strings.TrimSuffix(string(payload), "}") + `,"source_text":"private"}`)
		},
		"missing field": func(value *sourceReadyOutboxGORMRecord) {
			value.Payload = workflowJSONB(strings.Replace(string(payload), `"content_hash":"`+ready.ContentHash+`",`, "", 1))
		},
		"null payload":     func(value *sourceReadyOutboxGORMRecord) { value.Payload = workflowJSONB("null") },
		"trailing payload": func(value *sourceReadyOutboxGORMRecord) { value.Payload = workflowJSONB(string(payload) + `{}`) },
		"oversized":        func(value *sourceReadyOutboxGORMRecord) { value.Payload = workflowJSONB(strings.Repeat(" ", 4097)) },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			value := record
			corrupt(&value)
			_, err := value.fact()
			var failure *foundation.Error
			if !errors.As(err, &failure) || failure.Code != "SOURCE_READY_OUTBOX_RECORD_INVALID" || failure.Retryable {
				t.Fatalf("corrupt fact error = %v", err)
			}
		})
	}
}
