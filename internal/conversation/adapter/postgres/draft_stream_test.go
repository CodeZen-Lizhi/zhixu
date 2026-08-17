package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateDraftBeginAllowsWorkspaceAnalysisRunWindow(t *testing.T) {
	command := agentapplication.BeginDraftStreamCommand{
		DraftStreamBinding: agentapplication.DraftStreamBinding{
			WorkspaceID:   foundation.ID("8a000000-0000-4000-8000-000000000001"),
			AnswerID:      foundation.ID("8a000000-0000-4000-8000-000000000002"),
			WorkflowRunID: foundation.ID("8a000000-0000-4000-8000-000000000003"),
			NodeRunID:     foundation.ID("8a000000-0000-4000-8000-000000000004"),
			NodeAttemptID: foundation.ID("8a000000-0000-4000-8000-000000000005"),
			AttemptNo:     1,
			LeaseOwner:    "worker-1",
		},
		TTL: agentapplication.WorkspaceAnalysisV1MaxRunDuration,
	}
	if err := validateDraftBegin(context.Background(), command); err != nil {
		t.Fatalf("one-hour Workspace Analysis draft TTL rejected: %v", err)
	}

	command.TTL += time.Nanosecond
	err := validateDraftBegin(context.Background(), command)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != ErrorCodeDraftStreamInvalid {
		t.Fatalf("oversized draft TTL error = %#v", err)
	}
}
