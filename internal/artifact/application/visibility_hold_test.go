package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCommandServicePlanBindsVisibilityHoldIntoCreateAndRequestHash(t *testing.T) {
	now := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	repository := &commandRepositoryFake{receipts: map[string]CommandResult{}}
	service, err := NewCommandService(Dependencies{
		Repository: repository,
		IDs:        &sequenceIDs{next: 900},
		Clock:      foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	hold := &VisibilityHold{
		OwnerType:     VisibilityHoldOwnerInterviewComplete,
		OwnerID:       appID(899),
		OwnerRole:     VisibilityHoldRoleReport,
		AttemptDigest: strings.Repeat("a", 64),
	}
	planKey := "iv1:" + string(hold.OwnerID) + ":INTERVIEW_DOC:" + hold.AttemptDigest + ":p"
	command := PlanCommand{
		WorkspaceID:     appID(898),
		Type:            "INTERVIEW_DOC",
		Title:           "Interview report",
		ScopeDefinition: `{"schema_version":"interview-report/v1"}`,
		IdempotencyKey:  planKey,
		VisibilityHold:  hold,
	}
	result, err := service.Plan(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if repository.create == nil || repository.create.VisibilityHold == nil {
		t.Fatal("visibility hold was not passed to the atomic create record")
	}
	if *repository.create.VisibilityHold != *hold {
		t.Fatalf("create hold=%+v want=%+v", repository.create.VisibilityHold, hold)
	}
	wantHash, err := requestHash(CommandPlan, command.WorkspaceID, "", 0, planRequest{
		Type: command.Type, Title: command.Title, ScopeDefinition: command.ScopeDefinition,
		VisibilityHold: hold,
	})
	if err != nil {
		t.Fatal(err)
	}
	ordinaryHash, err := requestHash(CommandPlan, command.WorkspaceID, "", 0, planRequest{
		Type: command.Type, Title: command.Title, ScopeDefinition: command.ScopeDefinition,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestHash != wantHash || wantHash == ordinaryHash || repository.create.Binding.RequestHash != wantHash {
		t.Fatalf("result hash=%s create hash=%s ordinary hash=%s want=%s", result.RequestHash, repository.create.Binding.RequestHash, ordinaryHash, wantHash)
	}
	hold.OwnerRole = VisibilityHoldRolePath
	if repository.create.VisibilityHold.OwnerRole != VisibilityHoldRoleReport {
		t.Fatal("create record retained an alias to the caller's visibility hold")
	}
}

func TestCommandServiceRejectsInvalidVisibilityHoldBeforeCreate(t *testing.T) {
	now := time.Date(2026, 7, 28, 8, 30, 0, 0, time.UTC)
	validDigest := strings.Repeat("a", 64)
	tests := []struct {
		name string
		hold *VisibilityHold
	}{
		{name: "owner type", hold: &VisibilityHold{OwnerType: "OTHER", OwnerID: appID(910), OwnerRole: VisibilityHoldRoleReport, AttemptDigest: validDigest}},
		{name: "owner id", hold: &VisibilityHold{OwnerType: VisibilityHoldOwnerInterviewComplete, OwnerID: "invalid", OwnerRole: VisibilityHoldRoleReport, AttemptDigest: validDigest}},
		{name: "owner role", hold: &VisibilityHold{OwnerType: VisibilityHoldOwnerInterviewComplete, OwnerID: appID(911), OwnerRole: "OTHER", AttemptDigest: validDigest}},
		{name: "missing digest", hold: &VisibilityHold{OwnerType: VisibilityHoldOwnerInterviewComplete, OwnerID: appID(911), OwnerRole: VisibilityHoldRoleReport}},
		{name: "non canonical digest", hold: &VisibilityHold{OwnerType: VisibilityHoldOwnerInterviewComplete, OwnerID: appID(911), OwnerRole: VisibilityHoldRoleReport, AttemptDigest: strings.Repeat("A", 64)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &commandRepositoryFake{receipts: map[string]CommandResult{}}
			service, err := NewCommandService(Dependencies{
				Repository: repository,
				IDs:        &sequenceIDs{next: 920},
				Clock:      foundation.FixedClock{Value: now},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Plan(context.Background(), PlanCommand{
				WorkspaceID: appID(912), Type: "INTERVIEW_DOC", Title: "Invalid hold",
				ScopeDefinition: "approved material", IdempotencyKey: "artifact-invalid-hold", VisibilityHold: test.hold,
			})
			var typed *foundation.Error
			if !errors.As(err, &typed) || typed.Code != ErrorCodeRequestInvalid || repository.create != nil {
				t.Fatalf("error=%v create=%+v", err, repository.create)
			}
		})
	}
}

func TestCommandServiceTransitionsThroughInternalCommandState(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	artifact, revision, err := domain.PlanArtifact(domain.PlanInput{
		ArtifactID: appID(930), InitialRevisionID: appID(931), WorkspaceID: appID(932),
		Type: "INTERVIEW_DOC", Title: "Held report", ScopeDefinition: "approved material", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := &commandRepositoryFake{state: State{Artifact: artifact, Revision: revision}, receipts: map[string]CommandResult{}}
	service, err := NewCommandService(Dependencies{
		Repository: repository,
		IDs:        &sequenceIDs{next: 933},
		Clock:      foundation.FixedClock{Value: now.Add(time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitOutline(context.Background(), SubmitOutlinePersistentCommand{
		WorkspaceID: artifact.WorkspaceID, ArtifactID: artifact.ID, ExpectedVersion: 1,
		Outline: []domain.OutlineSection{{Key: "report", Title: "Report"}}, IdempotencyKey: "held-outline",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Artifact.Version != 2 || repository.getCalls != 1 || repository.publicGetCalls != 0 {
		t.Fatalf("result=%+v internal gets=%d public gets=%d", result, repository.getCalls, repository.publicGetCalls)
	}
}
