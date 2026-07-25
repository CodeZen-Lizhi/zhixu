package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
)

func TestDispatchTimelineProjectionUsesBoundedApplicationLimit(t *testing.T) {
	port := &workerTimelineProjectionPortStub{alwaysProjected: true}
	dispatcher, err := knowledgeapplication.NewTimelineProjectionDispatcher(port)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := dispatchTimelineProjection(context.Background(), slog.New(slog.NewJSONHandler(io.Discard, nil)), dispatcher, timelineProjectionPeriodicPhase)
	if err != nil {
		t.Fatal(err)
	}
	if port.calls != knowledgeapplication.MaxTimelineProjectionBatch || batch.Processed != knowledgeapplication.MaxTimelineProjectionBatch || batch.Projected != knowledgeapplication.MaxTimelineProjectionBatch {
		t.Fatalf("calls=%d batch=%+v", port.calls, batch)
	}
}

func TestDispatchTimelineProjectionLogsPoisonCountsWithoutCause(t *testing.T) {
	const privateCause = "timeline-projection-private-cause"
	poisoned := foundation.NewError(foundation.ErrorManualRecoveryRequired, "KNOWLEDGE_TIMELINE_PROJECTION_POISONED", false, errors.New(privateCause))
	port := &workerTimelineProjectionPortStub{
		results: []knowledgeapplication.TimelineProjectionResult{{Outcome: knowledgeapplication.TimelineProjectionPoisoned}},
		errors:  []error{poisoned},
	}
	dispatcher, err := knowledgeapplication.NewTimelineProjectionDispatcher(port)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	batch, err := dispatchTimelineProjection(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)), dispatcher, timelineProjectionStartupPhase)
	if !errors.Is(err, poisoned) || batch.Processed != 1 || batch.Poisoned != 1 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	if strings.Contains(output.String(), privateCause) {
		t.Fatalf("private cause leaked into log: %s", output.String())
	}
	var entry struct {
		Message        string `json:"msg"`
		ErrorCode      string `json:"error_code"`
		Retryable      bool   `json:"retryable"`
		Phase          string `json:"phase"`
		ProcessedCount int    `json:"processed_count"`
		ProjectedCount int    `json:"projected_count"`
		ReplayedCount  int    `json:"replayed_count"`
		PoisonedCount  int    `json:"poisoned_count"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode log: %v output=%s", err, output.String())
	}
	if entry.Message != "timeline projection dispatch failed" || entry.ErrorCode != "KNOWLEDGE_TIMELINE_PROJECTION_POISONED" || entry.Retryable || entry.Phase != timelineProjectionStartupPhase || entry.ProcessedCount != 1 || entry.ProjectedCount != 0 || entry.ReplayedCount != 0 || entry.PoisonedCount != 1 {
		t.Fatalf("entry=%+v", entry)
	}
}

type workerTimelineProjectionPortStub struct {
	results         []knowledgeapplication.TimelineProjectionResult
	errors          []error
	alwaysProjected bool
	calls           int
}

func (stub *workerTimelineProjectionPortStub) ProjectNext(context.Context) (knowledgeapplication.TimelineProjectionResult, bool, error) {
	index := stub.calls
	stub.calls++
	if stub.alwaysProjected {
		return knowledgeapplication.TimelineProjectionResult{Outcome: knowledgeapplication.TimelineProjectionProjected}, true, nil
	}
	if index >= len(stub.results) {
		return knowledgeapplication.TimelineProjectionResult{}, false, nil
	}
	var err error
	if index < len(stub.errors) {
		err = stub.errors[index]
	}
	return stub.results[index], true, err
}
