package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestQueryServiceListsRevisionableGenerationsInOutlineOrder(t *testing.T) {
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	state := generationQueryState(t, now)
	first := generationQueryPending(now.Add(5*time.Minute), state, "first", 800)
	second := generationQueryPending(now.Add(5*time.Minute+time.Second), state, "second", 805)
	third := generationQueryPending(now.Add(6*time.Minute), state, "third", 810)
	repository := &commandRepositoryFake{
		generationSnapshot: SectionGenerationSnapshot{State: state, Items: []SectionGeneration{first, second, third}},
	}
	service, err := NewQueryService(repository)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ListSectionGenerations(context.Background(), state.Artifact.WorkspaceID, state.Artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceID != state.Artifact.WorkspaceID || result.ArtifactID != state.Artifact.ID ||
		len(result.Items) != 3 || result.Items[0].SectionKey != "first" || result.Items[1].SectionKey != "second" || result.Items[2].SectionKey != "third" {
		t.Fatalf("generation list = %#v", result)
	}

	repository.generationSnapshot.Items = nil
	empty, err := service.ListSectionGenerations(context.Background(), state.Artifact.WorkspaceID, state.Artifact.ID)
	if err == nil || empty.Items != nil {
		t.Fatalf("nil repository items must fail closed: result=%#v err=%v", empty, err)
	}

	repository.generationSnapshot.Items = []SectionGeneration{}
	empty, err = service.ListSectionGenerations(context.Background(), state.Artifact.WorkspaceID, state.Artifact.ID)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("explicit empty generation list = %#v err=%v", empty, err)
	}
}

func TestQueryServiceRejectsInvalidGenerationProjection(t *testing.T) {
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	state := generationQueryState(t, now)
	first := generationQueryPending(now.Add(5*time.Minute), state, "first", 820)
	third := generationQueryPending(now.Add(6*time.Minute), state, "third", 830)
	repository := &commandRepositoryFake{}
	service, err := NewQueryService(repository)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		items []SectionGeneration
	}{
		{name: "outline order", items: []SectionGeneration{third, first}},
		{name: "duplicate section", items: []SectionGeneration{first, first}},
		{name: "section outside outline", items: []SectionGeneration{generationQueryPending(now.Add(7*time.Minute), state, "missing", 840)}},
		{name: "cross workspace", items: []SectionGeneration{func() SectionGeneration {
			value := first
			value.WorkspaceID = appID(899)
			return value
		}()}},
		{name: "future source", items: []SectionGeneration{func() SectionGeneration {
			value := first
			value.SourceArtifactVersion = state.Artifact.Version + 1
			return value
		}()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository.generationSnapshot = SectionGenerationSnapshot{State: state, Items: test.items}
			_, queryErr := service.ListSectionGenerations(context.Background(), state.Artifact.WorkspaceID, state.Artifact.ID)
			var classified *foundation.Error
			if !errors.As(queryErr, &classified) || classified.Code != ErrorCodeResultInconsistent {
				t.Fatalf("error=%v", queryErr)
			}
		})
	}
}

func generationQueryState(t *testing.T, now time.Time) State {
	t.Helper()
	artifact, revision, err := domain.PlanArtifact(domain.PlanInput{
		ArtifactID: appID(750), InitialRevisionID: appID(751), WorkspaceID: appID(752),
		Type: "study-guide", Title: "Refresh recovery", ScopeDefinition: "approved material", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, revision, err = domain.SubmitOutline(artifact, revision, appID(753), []domain.OutlineSection{
		{Key: "first", Title: "First"}, {Key: "second", Title: "Second"}, {Key: "third", Title: "Third"},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	artifact, revision, err = domain.ApproveOutline(artifact, revision, appID(754), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	artifact, revision, err = domain.RecordSection(artifact, revision, appID(755), domain.Section{
		Key: "second", Title: "Second", Content: "", Citations: []domain.Citation{},
		Coverage: domain.Coverage{SectionKey: "second", Status: domain.CoverageGap, Gaps: []domain.Gap{{Code: "NO_SOURCE", Description: "no approved source"}}},
	}, domain.CreatorHuman, nil, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return State{Artifact: artifact, Revision: revision}
}

func generationQueryPending(at time.Time, state State, key string, base int) SectionGeneration {
	return SectionGeneration{
		ID: appID(base), WorkspaceID: state.Artifact.WorkspaceID, ArtifactID: state.Artifact.ID,
		SourceRevisionID: state.Revision.ID, SourceRevisionNo: state.Revision.RevisionNo,
		SourceArtifactVersion: state.Artifact.Version, SectionKey: key,
		IdempotencyKey: "artifact-generation-" + key, RequestHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		WorkflowRunID: appID(base + 1), NodeRunID: appID(base + 2), Status: SectionGenerationPending,
		Version: 1, CreatedAt: at, UpdatedAt: at,
	}
}

func TestValidateSectionGenerationAllowsARebasedTerminalRevision(t *testing.T) {
	createdAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	completedAt := createdAt.Add(time.Minute)
	modelRunID := appID(708)
	baseRevisionID := appID(709)
	recordedRevisionID := appID(710)
	recordedVersion := int64(5)
	generation := validSectionGeneration(createdAt)
	generation.Status = SectionGenerationCompleted
	generation.ModelRunID = &modelRunID
	generation.RecordedBaseRevisionID = &baseRevisionID
	generation.RecordedRevisionID = &recordedRevisionID
	generation.RecordedArtifactVersion = &recordedVersion
	generation.ContentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	generation.Version = 2
	generation.UpdatedAt = completedAt
	generation.CompletedAt = &completedAt
	generation.TerminalAt = &completedAt

	if err := ValidateSectionGeneration(generation); err != nil {
		t.Fatalf("rebased terminal generation should be valid: %v", err)
	}
}

func TestValidateSectionGenerationAcceptsFrozenFailureTerminals(t *testing.T) {
	createdAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	terminalAt := createdAt.Add(time.Minute)
	modelRunID := appID(711)

	tests := []struct {
		name         string
		status       SectionGenerationStatus
		failureClass string
		modelRunID   *foundation.ID
	}{
		{name: "failed without provider call", status: SectionGenerationFailed, failureClass: "non_retryable"},
		{name: "cancelled with settled provider call", status: SectionGenerationCancelled, failureClass: "cancelled", modelRunID: &modelRunID},
		{name: "uncertain provider outcome", status: SectionGenerationRecoveryRequired, failureClass: "manual_recovery", modelRunID: &modelRunID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			generation := validSectionGeneration(createdAt)
			generation.Status = test.status
			generation.ModelRunID = test.modelRunID
			generation.FailureClass = test.failureClass
			generation.ErrorCode = "ARTIFACT_GENERATION_TERMINATED"
			generation.ErrorSummary = "artifact generation terminated"
			generation.Version = 2
			generation.UpdatedAt = terminalAt
			generation.TerminalAt = &terminalAt
			if err := ValidateSectionGeneration(generation); err != nil {
				t.Fatalf("terminal generation should be valid: %v", err)
			}
		})
	}
}

func TestValidateSectionGenerationRejectsMixedTerminalFacts(t *testing.T) {
	createdAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	terminalAt := createdAt.Add(time.Minute)
	revisionID := appID(712)

	pending := validSectionGeneration(createdAt)
	pending.FailureClass = "non_retryable"
	if err := ValidateSectionGeneration(pending); err == nil {
		t.Fatal("pending generation with failure facts should fail")
	}

	failed := validSectionGeneration(createdAt)
	failed.Status = SectionGenerationFailed
	failed.FailureClass = "non_retryable"
	failed.ErrorCode = "ARTIFACT_GENERATION_FAILED"
	failed.ErrorSummary = "artifact generation failed"
	failed.RecordedRevisionID = &revisionID
	failed.Version = 2
	failed.UpdatedAt = terminalAt
	failed.TerminalAt = &terminalAt
	if err := ValidateSectionGeneration(failed); err == nil {
		t.Fatal("failed generation with a fabricated revision receipt should fail")
	}

	recovery := validSectionGeneration(createdAt)
	recovery.Status = SectionGenerationRecoveryRequired
	recovery.FailureClass = "non_retryable"
	recovery.ErrorCode = "ARTIFACT_GENERATION_UNKNOWN"
	recovery.ErrorSummary = "artifact generation outcome is unknown"
	recovery.Version = 2
	recovery.UpdatedAt = terminalAt
	recovery.TerminalAt = &terminalAt
	if err := ValidateSectionGeneration(recovery); err == nil {
		t.Fatal("recovery-required generation without manual recovery class should fail")
	}
}

func TestSectionGenerationStatusOwnsSourceSlot(t *testing.T) {
	for _, test := range []struct {
		status SectionGenerationStatus
		want   bool
	}{
		{status: SectionGenerationPending, want: true},
		{status: SectionGenerationRecoveryRequired, want: true},
		{status: SectionGenerationCompleted, want: false},
		{status: SectionGenerationFailed, want: false},
		{status: SectionGenerationCancelled, want: false},
	} {
		if got := test.status.OwnsSourceSlot(); got != test.want {
			t.Fatalf("status %s OwnsSourceSlot() = %t, want %t", test.status, got, test.want)
		}
	}
}

func TestValidateSectionGenerationRequiresAnActualBaseRevision(t *testing.T) {
	createdAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	completedAt := createdAt.Add(time.Minute)
	modelRunID := appID(708)
	recordedRevisionID := appID(710)
	recordedVersion := int64(4)
	generation := validSectionGeneration(createdAt)
	generation.Status = SectionGenerationCompleted
	generation.ModelRunID = &modelRunID
	generation.RecordedRevisionID = &recordedRevisionID
	generation.RecordedArtifactVersion = &recordedVersion
	generation.ContentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	generation.Version = 2
	generation.UpdatedAt = completedAt
	generation.CompletedAt = &completedAt

	if err := ValidateSectionGeneration(generation); err == nil {
		t.Fatal("completed generation without its actual base revision should fail")
	}
}

func TestSectionGenerationUsesCanonicalKeysAndSingleTerminalTransition(t *testing.T) {
	command := StartSectionGenerationCommand{
		WorkspaceID: appID(701), ArtifactID: appID(702), ExpectedVersion: 3,
		SectionKey: "invalid_key", IdempotencyKey: "artifact-generate-section",
	}
	if err := ValidateStartSectionGenerationCommand(command); err == nil {
		t.Fatal("non-canonical section key should fail at the command boundary")
	}

	generation := validSectionGeneration(time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC))
	generation.Version = 2
	if err := ValidateSectionGeneration(generation); err == nil {
		t.Fatal("pending generation with a version above one should fail")
	}
}

func validSectionGeneration(at time.Time) SectionGeneration {
	return SectionGeneration{
		ID: appID(700), WorkspaceID: appID(701), ArtifactID: appID(702),
		SourceRevisionID: appID(703), SourceRevisionNo: 3, SourceArtifactVersion: 3,
		SectionKey: "summary", IdempotencyKey: "artifact-generate-summary",
		RequestHash:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		WorkflowRunID: appID(704), NodeRunID: appID(705), Status: SectionGenerationPending,
		Version: 1, CreatedAt: at, UpdatedAt: at,
	}
}
