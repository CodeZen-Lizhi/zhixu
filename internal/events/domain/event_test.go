package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestDecodePayloadSummaryAcceptsStableFactsAndRejectsBodyFields(t *testing.T) {
	workflowID := "7a000000-0000-4000-8000-000000000002"
	raw := []byte(`{"workflow_run_id":"` + workflowID + `","status":"running","candidate_count":0}`)

	summary, err := DecodePayloadSummary(raw)
	if err != nil {
		t.Fatalf("decode stable summary: %v", err)
	}
	if summary.WorkflowRunID == nil || string(*summary.WorkflowRunID) != workflowID || summary.Status != "running" || summary.CandidateCount == nil || *summary.CandidateCount != 0 {
		t.Fatalf("unexpected summary: %#v", summary)
	}

	for _, unsafe := range []string{
		`{"question_text":"private question"}`,
		`{"answer_text":"private answer"}`,
		`{"evidence":"private evidence"}`,
		`{"absolute_path":"/private/workspace/document.md"}`,
		`{"status":"running","status":"completed"}`,
	} {
		if _, err := DecodePayloadSummary([]byte(unsafe)); errorCode(err) != ErrorCodePayloadSummaryInvalid {
			t.Fatalf("unsafe summary accepted: input=%q code=%q err=%v", unsafe, errorCode(err), err)
		}
	}
}

func TestServerEventEnvelopeContainsOnlyReplayContract(t *testing.T) {
	workflowID := foundation.ID("7a000000-0000-4000-8000-000000000002")
	count := int64(3)
	occurredAt := time.Date(2026, 7, 19, 8, 9, 10, 123000000, time.UTC)
	event := ServerEvent{
		Seq:             42,
		WorkspaceID:     foundation.ID("7a000000-0000-4000-8000-000000000001"),
		WorkflowRunID:   &workflowID,
		Type:            "workflow.run.progress",
		ResourceRef:     "workflow.run:" + string(workflowID),
		ResourceVersion: 4,
		PayloadSummary: PayloadSummary{
			WorkflowRunID:  &workflowID,
			Status:         "running",
			CandidateCount: &count,
		},
		SchemaVersion:  1,
		SourceEventRef: "workflow.outbox_event:7a000000-0000-4000-8000-000000000003",
		OccurredAt:     occurredAt,
		ExpiresAt:      occurredAt.Add(RetentionWindow),
	}

	envelope, err := event.Envelope()
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(encoded)
	for _, expected := range []string{`"id":"42"`, `"type":"workflow.run.progress"`, `"occurred_at":"2026-07-19T08:09:10.123Z"`, `"resource_version":4`, `"candidate_count":3`} {
		if !strings.Contains(wire, expected) {
			t.Fatalf("envelope missing %q: %s", expected, wire)
		}
	}
	for _, forbidden := range []string{"source_event_ref", "expires_at", "question_text", "answer_text"} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("envelope leaked %q: %s", forbidden, wire)
		}
	}
}

func TestServerEventRejectsPathLikeResourceReferences(t *testing.T) {
	occurredAt := time.Date(2026, 7, 19, 8, 9, 10, 0, time.UTC)
	event := ServerEvent{
		Seq: 1, WorkspaceID: foundation.ID("7a000000-0000-4000-8000-000000000001"),
		Type: "answer.completed", ResourceRef: "answer:7a000000-0000-4000-8000-000000000002", ResourceVersion: 1,
		PayloadSummary: PayloadSummary{}, SchemaVersion: 1,
		SourceEventRef: "manual:7a000000-0000-4000-8000-000000000003",
		OccurredAt:     occurredAt, ExpiresAt: occurredAt.Add(RetentionWindow),
	}
	for _, unsafe := range []string{
		"/private/workspace/answer.json",
		"file:/private/workspace/answer.json",
		"https://example.test/answer",
		"answer:../../private/answer",
	} {
		candidate := event
		candidate.ResourceRef = unsafe
		if err := candidate.Validate(); errorCode(err) != ErrorCodeEventInvalid {
			t.Fatalf("path-like resource reference accepted: value=%q code=%q err=%v", unsafe, errorCode(err), err)
		}
		candidate = event
		candidate.SourceEventRef = unsafe
		if err := candidate.Validate(); errorCode(err) != ErrorCodeEventInvalid {
			t.Fatalf("path-like source reference accepted: value=%q code=%q err=%v", unsafe, errorCode(err), err)
		}
	}
}

func TestReplayCursorParsingAndWindowValidation(t *testing.T) {
	cursor, err := ParseReplayCursor("9")
	if err != nil || cursor.AfterSeq != 9 {
		t.Fatalf("parse replay cursor: cursor=%#v err=%v", cursor, err)
	}
	earliest := int64(4)
	if err := ValidateReplayCursor(cursor, 12, &earliest); err != nil {
		t.Fatalf("validate retained cursor: %v", err)
	}

	for _, invalid := range []string{"", "0", "-1", "+1", "01", " 1", "1 ", "9223372036854775808"} {
		if _, err := ParseReplayCursor(invalid); errorCode(err) != ErrorCodeCursorInvalid {
			t.Fatalf("invalid cursor accepted: value=%q code=%q err=%v", invalid, errorCode(err), err)
		}
	}
	if err := ValidateReplayCursor(ReplayCursor{AfterSeq: 13}, 12, &earliest); errorCode(err) != ErrorCodeCursorFuture {
		t.Fatalf("future cursor code=%q err=%v", errorCode(err), err)
	}
	if err := ValidateReplayCursor(ReplayCursor{AfterSeq: 3}, 12, &earliest); errorCode(err) != ErrorCodeCursorExpired {
		t.Fatalf("expired cursor code=%q err=%v", errorCode(err), err)
	}
	if err := ValidateReplayCursor(ReplayCursor{AfterSeq: 12}, 12, nil); errorCode(err) != ErrorCodeCursorExpired {
		t.Fatalf("cursor against empty retained window code=%q err=%v", errorCode(err), err)
	}
	advancedEarliest := int64(13)
	if err := ValidateReplayCursor(ReplayCursor{AfterSeq: 12}, 12, &advancedEarliest); errorCode(err) != ErrorCodeCursorExpired {
		t.Fatalf("cursor across advancing retention boundary code=%q err=%v", errorCode(err), err)
	}
}

func errorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
