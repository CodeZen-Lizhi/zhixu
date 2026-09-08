package postgres

import (
	"context"
	"testing"
	"time"

	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	exportdomain "github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type lifecycleEventScope struct{}

func (*lifecycleEventScope) TransactionScope() {}

type lifecycleEventAppender struct {
	requests []eventsdomain.AppendRequest
}

func (appender *lifecycleEventAppender) AppendScoped(_ context.Context, _ foundation.TransactionScope, request eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error) {
	if err := request.Validate(); err != nil {
		return eventsdomain.ServerEvent{}, false, err
	}
	appender.requests = append(appender.requests, request)
	return eventsdomain.ServerEvent{}, false, nil
}

func TestAppendLifecycleEventCarriesLowercaseScopeKindForEachStage(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	stages := []string{"created", "claimed", "prepared", "completed", "failed", "expired", "cleanup_succeeded"}
	tests := []struct {
		name      string
		scopeKind exportdomain.ScopeKind
		want      string
	}{
		{name: "Collection", scopeKind: exportdomain.ScopeCollection, want: "collection"},
		{name: "Workspace attachments", scopeKind: exportdomain.ScopeWorkspaceAttachments, want: "workspace_attachments"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			appender := &lifecycleEventAppender{}
			repository := &exportRepository{events: appender}
			job := exportdomain.Job{
				ID: foundation.ID("7a000000-0000-4000-8000-000000000001"), WorkspaceID: foundation.ID("7a000000-0000-4000-8000-000000000002"),
				Scope: exportdomain.Scope{Kind: test.scopeKind}, Status: exportdomain.StatusSucceeded, Version: 4, UpdatedAt: now,
			}
			for _, stage := range stages {
				if err := repository.appendLifecycleEvent(context.Background(), &gormTx{scope: &lifecycleEventScope{}}, job, stage); err != nil {
					t.Fatalf("append lifecycle stage %q: %v", stage, err)
				}
			}
			if len(appender.requests) != len(stages) {
				t.Fatalf("lifecycle requests=%d, want %d", len(appender.requests), len(stages))
			}
			for index, request := range appender.requests {
				if request.PayloadSummary.ScopeKind != test.want || request.PayloadSummary.Stage != stages[index] {
					t.Fatalf("lifecycle request[%d] summary=%#v", index, request.PayloadSummary)
				}
			}
		})
	}
}
