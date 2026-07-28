package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
)

func TestWriterCreatesTaskScopedInterviewGoalCandidate(t *testing.T) {
	request := writerRequest()
	creator := &candidateCreatorFake{}
	writer, err := NewWriter(creator)
	if err != nil {
		t.Fatal(err)
	}
	creator.result = candidateResult(t, request, true)

	result, err := writer.CreateCandidate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	command := creator.command
	expectedRef := candidateSourceRef(request.SessionID, request.PathID, request.StepID)
	if creator.calls != 1 || command.WorkspaceID != request.WorkspaceID || command.Owner != memorydomain.SingleUserOwner() ||
		command.Type != memorydomain.TypeGoal || string(command.Content) != string(request.Content) ||
		command.Source != (memorydomain.Source{Type: memorydomain.SourceInterview, Ref: expectedRef}) ||
		command.TaskScopeID == nil || *command.TaskScopeID != request.SessionID || command.ExpiresAt != nil ||
		command.IdempotencyKey != request.IdempotencyKey {
		t.Fatalf("candidate command=%+v calls=%d", command, creator.calls)
	}
	if result.MemoryID != creator.result.Memory.ID || !result.Replayed {
		t.Fatalf("candidate result=%+v", result)
	}
}

func TestWriterRejectsInvalidRequestAndInconsistentMemoryResult(t *testing.T) {
	request := writerRequest()
	creator := &candidateCreatorFake{}
	writer, err := NewWriter(creator)
	if err != nil {
		t.Fatal(err)
	}

	invalid := request
	invalid.Content = json.RawMessage(`{"rationale":"reason","goal":"goal"}`)
	if _, err := writer.CreateCandidate(context.Background(), invalid); err == nil || creator.calls != 0 {
		t.Fatalf("non-canonical request err=%v calls=%d", err, creator.calls)
	}

	creator.result = candidateResult(t, request, false)
	creator.result.Memory.Source = memorydomain.Source{Type: memorydomain.SourceUser, Ref: "user:forged"}
	if _, err := writer.CreateCandidate(context.Background(), request); err == nil || creator.calls != 1 {
		t.Fatalf("inconsistent result err=%v calls=%d", err, creator.calls)
	}
}

func TestNewWriterRejectsNilCreator(t *testing.T) {
	if writer, err := NewWriter(nil); err == nil || writer != nil {
		t.Fatalf("writer=%+v err=%v", writer, err)
	}
	var typedNil *candidateCreatorFake
	if writer, err := NewWriter(typedNil); err == nil || writer != nil {
		t.Fatalf("typed nil writer=%+v err=%v", writer, err)
	}
}

type candidateCreatorFake struct {
	command memoryapp.CreateCandidateCommand
	result  memoryapp.CommandResult
	err     error
	calls   int
}

func (creator *candidateCreatorFake) CreateCandidate(_ context.Context, command memoryapp.CreateCandidateCommand) (memoryapp.CommandResult, error) {
	creator.calls++
	creator.command = command
	return creator.result, creator.err
}

func writerRequest() interviewapp.MemoryCandidateRequest {
	return interviewapp.MemoryCandidateRequest{
		WorkspaceID:    writerID(1),
		SessionID:      writerID(2),
		PathID:         writerID(3),
		StepID:         writerID(4),
		Content:        json.RawMessage(`{"goal":"Close the gap","rationale":"Practice the persisted step"}`),
		IdempotencyKey: "interview-goal:" + fmt.Sprintf("%064x", 1),
	}
}

func candidateResult(t *testing.T, request interviewapp.MemoryCandidateRequest, replayed bool) memoryapp.CommandResult {
	t.Helper()
	taskScopeID := request.SessionID
	memory, err := memorydomain.NewCandidate(memorydomain.CandidateInput{
		ID: writerID(10), WorkspaceID: request.WorkspaceID, Owner: memorydomain.SingleUserOwner(), Type: memorydomain.TypeGoal,
		Content: request.Content,
		Source: memorydomain.Source{
			Type: memorydomain.SourceInterview,
			Ref:  candidateSourceRef(request.SessionID, request.PathID, request.StepID),
		},
		TaskScopeID: &taskScopeID,
		CreatedAt:   time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return memoryapp.CommandResult{Memory: memory, Replayed: replayed}
}

func writerID(value int) foundation.ID {
	parsed, err := foundation.ParseID(fmt.Sprintf("31000000-0000-4000-8000-%012x", value))
	if err != nil {
		panic(err)
	}
	return parsed
}
