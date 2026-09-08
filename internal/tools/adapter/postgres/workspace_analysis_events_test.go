package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestWorkspaceAnalysisEventsRejectsNilAppender(t *testing.T) {
	repository := &GORMWorkspaceAnalysisRepository{}
	if err := repository.appendRequested(context.Background(), &workspaceAnalysisToolEventScope{}, agentdomain.WorkspaceAnalysisOperation{}, domain.ToolCall{}, false); errorCodeForUnit(err) != ErrorCodeDatabaseUnavailable {
		t.Fatalf("nil event appender error=%v", err)
	}
	var appender *workspaceAnalysisToolEventCapture
	repository.events = appender
	if err := repository.appendRequested(context.Background(), &workspaceAnalysisToolEventScope{}, agentdomain.WorkspaceAnalysisOperation{}, domain.ToolCall{}, false); errorCodeForUnit(err) != ErrorCodeDatabaseUnavailable {
		t.Fatalf("typed nil event appender error=%v", err)
	}
}

func TestWorkspaceAnalysisToolEventsUseOnlyStableProjectionAndExactReplay(t *testing.T) {
	now := time.Date(2026, 8, 17, 9, 10, 11, 123000000, time.UTC)
	workflowRunID := foundation.ID("95000000-0000-4000-8000-000000000002")
	call := domain.ToolCall{
		WorkspaceID: "95000000-0000-4000-8000-000000000001", WorkflowRunID: workflowRunID,
		StartedAt: now,
	}
	operation := agentdomain.WorkspaceAnalysisOperation{
		ID: "95000000-0000-4000-8000-000000000003", AnalysisRunID: "95000000-0000-4000-8000-000000000004",
		NodeKey: "retrieve_evidence",
	}
	capture := &workspaceAnalysisToolEventCapture{}
	repository := &GORMWorkspaceAnalysisRepository{events: capture}
	if err := repository.appendRequested(context.Background(), &workspaceAnalysisToolEventScope{}, operation, call, false); err != nil {
		t.Fatalf("append requested: %v", err)
	}
	if len(capture.requests) != 1 {
		t.Fatalf("request count=%d", len(capture.requests))
	}
	request := capture.requests[0]
	if request.Type != gormWorkspaceAnalysisToolRequestedEvent ||
		request.ResourceRef != "workspace_analysis:"+string(operation.AnalysisRunID) || request.ResourceVersion != 1 ||
		request.SourceEventRef != "workspace_analysis.tool_requested:"+string(operation.ID)+":v1" ||
		request.OccurredAt != now || request.WorkflowRunID == nil || *request.WorkflowRunID != workflowRunID ||
		request.PayloadSummary.WorkflowRunID == nil || *request.PayloadSummary.WorkflowRunID != workflowRunID ||
		request.PayloadSummary.Stage != string(operation.NodeKey) || request.PayloadSummary.Status != "started" {
		t.Fatalf("requested event=%#v", request)
	}
	if request.PayloadSummary.ConversationID != nil || request.PayloadSummary.QuestionID != nil ||
		request.PayloadSummary.AnswerID != nil || request.PayloadSummary.ModelRunID != nil ||
		request.PayloadSummary.CandidateCount != nil || request.PayloadSummary.CitationCount != nil {
		t.Fatalf("requested event leaked non-timeline data: %#v", request.PayloadSummary)
	}

	completedAt := now.Add(3 * time.Second)
	call.CompletedAt = &completedAt
	capture.replayed = true
	if err := repository.appendCompleted(context.Background(), &workspaceAnalysisToolEventScope{}, operation, call, "succeeded", true); err != nil {
		t.Fatalf("append completion replay: %v", err)
	}
	completed := capture.requests[1]
	if completed.Type != gormWorkspaceAnalysisToolCompletedEvent ||
		completed.SourceEventRef != "workspace_analysis.tool_completed:"+string(operation.ID)+":v1" ||
		completed.OccurredAt != completedAt || completed.PayloadSummary.Status != "succeeded" {
		t.Fatalf("completed event=%#v", completed)
	}

	capture.replayed = false
	if err := repository.appendCompleted(context.Background(), &workspaceAnalysisToolEventScope{}, operation, call, "succeeded", true); errorCodeForUnit(err) != ErrorCodePersistenceConsistency {
		t.Fatalf("replay drift error=%v", err)
	}
	if err := repository.appendEvent(context.Background(), &workspaceAnalysisToolEventScope{}, operation, call, gormWorkspaceAnalysisToolCompletedEvent, "started", completedAt, false); errorCodeForUnit(err) != ErrorCodePersistenceConsistency {
		t.Fatalf("invalid terminal status error=%v", err)
	}
	capture.err = errors.New("event store unavailable")
	if err := repository.appendCompleted(context.Background(), &workspaceAnalysisToolEventScope{}, operation, call, "failed", false); !errors.Is(err, capture.err) {
		t.Fatalf("appender error=%v", err)
	}
}

func TestWorkspaceAnalysisToolEventsRequireAppender(t *testing.T) {
	repository := &GORMWorkspaceAnalysisRepository{}
	call := domain.ToolCall{WorkspaceID: "95000000-0000-4000-8000-000000000001", WorkflowRunID: "95000000-0000-4000-8000-000000000002", StartedAt: time.Now().UTC()}
	operation := agentdomain.WorkspaceAnalysisOperation{ID: "95000000-0000-4000-8000-000000000003", AnalysisRunID: "95000000-0000-4000-8000-000000000004", NodeKey: "inspect_workspace"}
	if err := repository.appendRequested(context.Background(), &workspaceAnalysisToolEventScope{}, operation, call, false); errorCodeForUnit(err) != ErrorCodeDatabaseUnavailable {
		t.Fatalf("missing event appender: %v", err)
	}
}

type workspaceAnalysisToolEventCapture struct {
	requests []eventsdomain.AppendRequest
	replayed bool
	err      error
}

func (capture *workspaceAnalysisToolEventCapture) AppendScoped(_ context.Context, _ foundation.TransactionScope, request eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error) {
	if capture == nil {
		return eventsdomain.ServerEvent{}, false, errors.New("capture is nil")
	}
	if capture.err != nil {
		return eventsdomain.ServerEvent{}, false, capture.err
	}
	capture.requests = append(capture.requests, request)
	return eventsdomain.ServerEvent{}, capture.replayed, nil
}

type workspaceAnalysisToolEventScope struct{}

func (*workspaceAnalysisToolEventScope) TransactionScope() {}
