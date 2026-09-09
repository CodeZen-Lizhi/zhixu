package application

import (
	"context"
	"errors"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestServiceGetWorkspaceAnalysisTimelineValidatesScopeAndProjection(t *testing.T) {
	t.Parallel()
	workspaceID := conversationApplicationID(80)
	answerID := conversationApplicationID(81)
	runID := conversationApplicationID(82)
	reader := &recordingWorkspaceAnalysisTimelineReader{timeline: validWorkspaceAnalysisTimelineForApplication(workspaceID, answerID, runID)}
	service, err := NewService(Dependencies{
		Repository: &recordingRepository{}, WorkspaceAnalysisTimelineReader: reader,
		IDs: fixedIDGenerator{id: conversationApplicationID(83)}, Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}

	timeline, err := service.GetWorkspaceAnalysisTimeline(context.Background(), WorkspaceAnalysisTimelineQuery{WorkspaceID: workspaceID, AnswerID: answerID})
	if err != nil {
		t.Fatal(err)
	}
	if timeline.AnalysisRunID != runID || reader.query.WorkspaceID != workspaceID || reader.query.AnswerID != answerID {
		t.Fatalf("timeline=%#v query=%#v", timeline, reader.query)
	}

	reader.timeline.AnswerID = conversationApplicationID(84)
	_, err = service.GetWorkspaceAnalysisTimeline(context.Background(), WorkspaceAnalysisTimelineQuery{WorkspaceID: workspaceID, AnswerID: answerID})
	requireConversationApplicationError(t, err, foundation.ErrorConsistencyViolation, errorCodeResultInconsistent)

	reader.timeline = validWorkspaceAnalysisTimelineForApplication(workspaceID, answerID, runID)
	reader.timeline.Items = nil
	_, err = service.GetWorkspaceAnalysisTimeline(context.Background(), WorkspaceAnalysisTimelineQuery{WorkspaceID: workspaceID, AnswerID: answerID})
	requireConversationApplicationError(t, err, foundation.ErrorConsistencyViolation, errorCodeResultInconsistent)

	_, err = service.GetWorkspaceAnalysisTimeline(context.Background(), WorkspaceAnalysisTimelineQuery{WorkspaceID: workspaceID, AnswerID: workspaceID})
	requireConversationApplicationError(t, err, foundation.ErrorInvalidInput, errorCodeRequestInvalid)
}

func TestServiceGetWorkspaceAnalysisTimelineFailsClosedWhenReaderIsUnavailable(t *testing.T) {
	t.Parallel()
	service, err := NewService(Dependencies{
		Repository: &recordingRepository{}, IDs: fixedIDGenerator{id: conversationApplicationID(90)}, Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GetWorkspaceAnalysisTimeline(context.Background(), WorkspaceAnalysisTimelineQuery{WorkspaceID: conversationApplicationID(91), AnswerID: conversationApplicationID(92)})
	requireConversationApplicationError(t, err, foundation.ErrorDependencyUnavailable, errorCodeWorkspaceAnalysisTimelineUnavailable)

	reader := &recordingWorkspaceAnalysisTimelineReader{err: foundation.NewError(foundation.ErrorDependencyUnavailable, "TIMELINE_STORE_UNAVAILABLE", true, errors.New("database unavailable"))}
	service.timelines = reader
	_, err = service.GetWorkspaceAnalysisTimeline(context.Background(), WorkspaceAnalysisTimelineQuery{WorkspaceID: conversationApplicationID(91), AnswerID: conversationApplicationID(92)})
	if !errors.Is(err, reader.err) {
		t.Fatalf("GetWorkspaceAnalysisTimeline() error=%#v, want preserved reader error", err)
	}
}

func TestServiceGetWorkspaceAnalysisTimelineValidatesV2ByItsPersistedVersion(t *testing.T) {
	t.Parallel()
	workspaceID, answerID, runID := conversationApplicationID(180), conversationApplicationID(181), conversationApplicationID(182)
	timeline := validWorkspaceAnalysisTimelineForApplication(workspaceID, answerID, runID)
	timeline.SchemaVersion = conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV2
	timeline.Budget = conversationdomain.WorkspaceAnalysisTimelineBudget{
		ModelCalls:   conversationdomain.WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV2MaxModelCalls},
		ToolCalls:    conversationdomain.WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV2MaxToolCalls},
		SourceReads:  conversationdomain.WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV2MaxSourceReads},
		InputTokens:  conversationdomain.WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV2MaxRunInputTokens},
		OutputTokens: conversationdomain.WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV2MaxRunOutputTokens},
	}
	timeline.Items = []conversationdomain.WorkspaceAnalysisTimelineItem{{Sequence: 1, Kind: conversationdomain.WorkspaceAnalysisTimelineItemModel,
		Phase: conversationdomain.WorkspaceAnalysisPhaseDecideNext, Status: conversationdomain.WorkspaceAnalysisTimelineItemPending}}
	reader := &recordingWorkspaceAnalysisTimelineReader{timeline: timeline}
	service, err := NewService(Dependencies{Repository: &recordingRepository{}, WorkspaceAnalysisTimelineReader: reader,
		IDs: fixedIDGenerator{id: conversationApplicationID(183)}, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	query := WorkspaceAnalysisTimelineQuery{WorkspaceID: workspaceID, AnswerID: answerID}
	got, err := service.GetWorkspaceAnalysisTimeline(context.Background(), query)
	if err != nil || got.SchemaVersion != conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV2 || got.Items[0].Phase != conversationdomain.WorkspaceAnalysisPhaseDecideNext {
		t.Fatalf("v2 authoritative projection was not preserved: %v", err)
	}
	reader.timeline.SchemaVersion = conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV1
	_, err = service.GetWorkspaceAnalysisTimeline(context.Background(), query)
	requireConversationApplicationError(t, err, foundation.ErrorConsistencyViolation, errorCodeResultInconsistent)
	reader.timeline = timeline
	reader.timeline.Budget = validWorkspaceAnalysisTimelineForApplication(workspaceID, answerID, runID).Budget
	_, err = service.GetWorkspaceAnalysisTimeline(context.Background(), query)
	requireConversationApplicationError(t, err, foundation.ErrorConsistencyViolation, errorCodeResultInconsistent)
}

type recordingWorkspaceAnalysisTimelineReader struct {
	query    WorkspaceAnalysisTimelineQuery
	timeline conversationdomain.WorkspaceAnalysisTimeline
	err      error
}

func (reader *recordingWorkspaceAnalysisTimelineReader) GetWorkspaceAnalysisTimeline(_ context.Context, query WorkspaceAnalysisTimelineQuery) (conversationdomain.WorkspaceAnalysisTimeline, error) {
	reader.query = query
	return reader.timeline, reader.err
}

func validWorkspaceAnalysisTimelineForApplication(workspaceID, answerID, runID foundation.ID) conversationdomain.WorkspaceAnalysisTimeline {
	return conversationdomain.WorkspaceAnalysisTimeline{
		SchemaID: conversationdomain.WorkspaceAnalysisTimelineSchemaID, SchemaVersion: conversationdomain.WorkspaceAnalysisTimelineSchemaVersionV1,
		WorkspaceID: workspaceID, AnswerID: answerID, AnalysisRunID: runID,
		RunStatus: conversationdomain.WorkspaceAnalysisTimelineRunQueued, Items: []conversationdomain.WorkspaceAnalysisTimelineItem{},
		Budget: conversationdomain.WorkspaceAnalysisTimelineBudget{
			ModelCalls:   conversationdomain.WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV1MaxModelCalls},
			ToolCalls:    conversationdomain.WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV1MaxToolCalls},
			SourceReads:  conversationdomain.WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV1MaxSourceReads},
			InputTokens:  conversationdomain.WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV1MaxRunInputTokens},
			OutputTokens: conversationdomain.WorkspaceAnalysisTimelineCounter{Max: agentdomain.WorkspaceAnalysisV1MaxRunOutputTokens},
		},
	}
}
