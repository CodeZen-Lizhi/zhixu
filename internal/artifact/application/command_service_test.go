package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCommandServiceReplaysReceiptBeforeCurrentVersionCheck(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	artifact, first, err := domain.PlanArtifact(domain.PlanInput{
		ArtifactID: appID(300), InitialRevisionID: appID(301), WorkspaceID: appID(302),
		Type: "study-guide", Title: "Receipt replay", ScopeDefinition: "approved material", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	outline := []domain.OutlineSection{{Key: "summary", Title: "Summary"}}
	artifact, second, err := domain.SubmitOutline(artifact, first, appID(303), outline, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	command := SubmitOutlinePersistentCommand{WorkspaceID: artifact.WorkspaceID, ArtifactID: artifact.ID, ExpectedVersion: 1, Outline: outline, IdempotencyKey: "artifact-outline-replay"}
	hash, err := requestHash(CommandSubmitOutline, command.WorkspaceID, command.ArtifactID, command.ExpectedVersion, submitOutlineRequest{Outline: command.Outline})
	if err != nil {
		t.Fatal(err)
	}
	repository := &commandRepositoryFake{
		state: State{Artifact: artifact, Revision: second},
		receipts: map[string]CommandResult{
			command.IdempotencyKey: {
				State: State{Artifact: artifact, Revision: second}, CommandVersion: 2, RequestHash: hash,
				CommandType: CommandSubmitOutline, Replayed: true,
			},
		},
	}
	service, err := NewCommandService(Dependencies{Repository: repository, IDs: &sequenceIDs{next: 400}, Clock: foundation.FixedClock{Value: now}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitOutline(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || result.CommandVersion != 2 || repository.getCalls != 0 {
		t.Fatalf("result=%+v get calls=%d", result, repository.getCalls)
	}
}

func TestCommandServicePlanAcceptsCreatedVersionOne(t *testing.T) {
	now := time.Date(2026, 7, 26, 8, 30, 0, 0, time.UTC)
	repository := &commandRepositoryFake{receipts: map[string]CommandResult{}}
	service, err := NewCommandService(Dependencies{
		Repository: repository,
		IDs:        &sequenceIDs{next: 280},
		Clock:      foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Plan(context.Background(), PlanCommand{
		WorkspaceID:     appID(279),
		Type:            "study-guide",
		Title:           "First plan response",
		ScopeDefinition: "approved material",
		IdempotencyKey:  "artifact-plan-first-response",
	})
	if err != nil {
		t.Fatalf("plan returned an error after the repository created v1: %v", err)
	}
	if result.Replayed || result.CommandVersion != 1 || result.State.Artifact.Version != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestCommandServicePublishFreezesSourceCoverage(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	state := approvedCommandState(t, now)
	if err := ValidateState(state); err != nil {
		t.Fatalf("approved state is invalid before publish: %+v: %v", state, err)
	}
	publisher := &publicationCreatorFake{id: appID(390)}
	repository := &commandRepositoryFake{state: state, receipts: map[string]CommandResult{}}
	service, err := NewCommandService(Dependencies{
		Repository: repository, Publisher: publisher, IDs: &sequenceIDs{next: 400}, Clock: foundation.FixedClock{Value: now.Add(6 * time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Publish(context.Background(), RevisionPersistentCommand{
		WorkspaceID: state.Artifact.WorkspaceID, ArtifactID: state.Artifact.ID, ExpectedVersion: state.Artifact.Version, IdempotencyKey: "artifact-publish-coverage",
	})
	if err != nil {
		t.Fatalf("publish error=%v cause=%v", err, errors.Unwrap(err))
	}
	if publisher.calls != 1 || !reflect.DeepEqual(publisher.request.SourceCoverage, state.Artifact.SourceCoverage) {
		t.Fatalf("publisher request=%+v artifact coverage=%+v", publisher.request, state.Artifact.SourceCoverage)
	}
	publisher.request.SourceCoverage[0].SectionKey = "caller-mutation"
	if state.Artifact.SourceCoverage[0].SectionKey == "caller-mutation" {
		t.Fatal("publisher received an aliased coverage slice")
	}
	if result.State.Artifact.Status != domain.StatusPublishProposed || result.Publication == nil || result.Publication.ProposalID != publisher.id || repository.transition == nil {
		t.Fatalf("result=%+v transition=%+v", result, repository.transition)
	}
}

func TestCommandServiceExternalPreflightDoesNotReserveWhenClockPredatesState(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	state := approvedCommandState(t, now)
	repository := &commandRepositoryFake{state: state, receipts: map[string]CommandResult{}}
	publisher := &publicationCreatorFake{id: appID(399)}
	service, err := NewCommandService(Dependencies{
		Repository: repository, Publisher: publisher, IDs: &sequenceIDs{next: 410},
		Clock: foundation.FixedClock{Value: state.Artifact.UpdatedAt.Add(-time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Publish(context.Background(), RevisionPersistentCommand{
		WorkspaceID: state.Artifact.WorkspaceID, ArtifactID: state.Artifact.ID,
		ExpectedVersion: state.Artifact.Version, IdempotencyKey: "artifact-publish-old-clock",
	})
	if err == nil {
		t.Fatal("publish with a clock before the current state was accepted")
	}
	if repository.probeCalls != 1 || repository.reserveCalls != 0 || publisher.calls != 0 {
		t.Fatalf("probe=%d reserve=%d publisher=%d", repository.probeCalls, repository.reserveCalls, publisher.calls)
	}
}

func TestCommandServiceExternalProbeConflictPrecedesDependencyAvailability(t *testing.T) {
	now := time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC)
	state := approvedCommandState(t, now)
	tests := []struct {
		name string
		run  func(context.Context, *CommandService, RevisionPersistentCommand) (CommandResult, error)
	}{
		{name: "export", run: func(ctx context.Context, service *CommandService, command RevisionPersistentCommand) (CommandResult, error) {
			return service.ExportMarkdown(ctx, command)
		}},
		{name: "publish", run: func(ctx context.Context, service *CommandService, command RevisionPersistentCommand) (CommandResult, error) {
			return service.Publish(ctx, command)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &commandRepositoryFake{
				state: state, receipts: map[string]CommandResult{},
				probeErr: versionConflict("artifact external transition is reserved by another command"),
			}
			service, err := NewCommandService(Dependencies{
				Repository: repository, IDs: &sequenceIDs{next: 420}, Clock: foundation.FixedClock{Value: now.Add(time.Hour)},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = test.run(context.Background(), service, RevisionPersistentCommand{
				WorkspaceID: state.Artifact.WorkspaceID, ArtifactID: state.Artifact.ID,
				ExpectedVersion: state.Artifact.Version, IdempotencyKey: "artifact-owner-conflict-" + test.name,
			})
			var typed *foundation.Error
			if !errors.As(err, &typed) || typed.Code != ErrorCodeVersionConflict || repository.probeCalls != 1 || repository.reserveCalls != 0 {
				t.Fatalf("error=%v probe=%d reserve=%d", err, repository.probeCalls, repository.reserveCalls)
			}
		})
	}
}

func TestExportIDForRevisionIsStableAndScoped(t *testing.T) {
	first, err := exportIDForRevision(appID(500), appID(501))
	if err != nil {
		t.Fatal(err)
	}
	again, err := exportIDForRevision(appID(500), appID(501))
	if err != nil {
		t.Fatal(err)
	}
	other, err := exportIDForRevision(appID(500), appID(502))
	if err != nil {
		t.Fatal(err)
	}
	if first != again || first == other {
		t.Fatalf("first=%q again=%q other=%q", first, again, other)
	}
}

func approvedCommandState(t *testing.T, now time.Time) State {
	t.Helper()
	artifact, revision, err := domain.PlanArtifact(domain.PlanInput{
		ArtifactID: appID(350), InitialRevisionID: appID(351), WorkspaceID: appID(352),
		Type: "study-guide", Title: "Publish coverage", ScopeDefinition: "approved material", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	outline := []domain.OutlineSection{{Key: "summary", Title: "Summary"}, {Key: "limits", Title: "Limits"}}
	artifact, revision, err = domain.SubmitOutline(artifact, revision, appID(353), outline, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	artifact, revision, err = domain.ApproveOutline(artifact, revision, appID(354), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	covered := domain.Section{
		Key: "summary", Title: "Summary", Content: "Verified summary.",
		Citations: []domain.Citation{{SourceVersionID: appID(355), SourceSpanID: appID(356), VerifiedContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Excerpt: "verified span", Verified: true}},
		Coverage:  domain.Coverage{SectionKey: "summary", Status: domain.CoverageCovered, Gaps: []domain.Gap{}},
	}
	artifact, revision, err = domain.RecordSection(artifact, revision, appID(357), covered, domain.CreatorHuman, nil, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	partial := domain.Section{
		Key: "limits", Title: "Limits", Content: "Verified caveat.",
		Citations: []domain.Citation{{SourceVersionID: appID(358), SourceSpanID: appID(359), VerifiedContentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Excerpt: "verified limitation", Verified: true}},
		Coverage:  domain.Coverage{SectionKey: "limits", Status: domain.CoveragePartial, Gaps: []domain.Gap{{Code: "NO_SOURCE", Description: "needs an approved source"}}},
	}
	artifact, revision, err = domain.RecordSection(artifact, revision, appID(360), partial, domain.CreatorHuman, nil, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err = domain.ApproveDraft(artifact, revision, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return State{Artifact: artifact, Revision: revision}
}

type commandRepositoryFake struct {
	state              State
	generationSnapshot SectionGenerationSnapshot
	generationErr      error
	receipts           map[string]CommandResult
	getCalls           int
	probeCalls         int
	probeErr           error
	reserveCalls       int
	transition         *TransitionRecord
}

func (fake *commandRepositoryFake) FindCommand(_ context.Context, binding CommandBinding) (CommandResult, bool, error) {
	result, ok := fake.receipts[binding.IdempotencyKey]
	return result, ok, nil
}

func (fake *commandRepositoryFake) ReserveExternalTransition(_ context.Context, _ CommandBinding) (State, error) {
	fake.reserveCalls++
	return fake.state, nil
}

func (fake *commandRepositoryFake) ProbeExternalTransition(_ context.Context, _ CommandBinding) (State, error) {
	fake.probeCalls++
	return fake.state, fake.probeErr
}

func (fake *commandRepositoryFake) Create(_ context.Context, record CreateRecord) (CommandResult, error) {
	fake.state = record.State
	return CommandResult{State: record.State, CommandVersion: record.State.Artifact.Version, RequestHash: record.Binding.RequestHash, CommandType: record.Binding.CommandType}, nil
}

func (fake *commandRepositoryFake) Transition(_ context.Context, record TransitionRecord) (CommandResult, error) {
	copyRecord := record
	fake.transition = &copyRecord
	fake.state = record.State
	return CommandResult{
		State: record.State, CommandVersion: record.State.Artifact.Version, RequestHash: record.Binding.RequestHash, CommandType: record.Binding.CommandType,
		Export: record.Export, Publication: record.Publication,
	}, nil
}

func (fake *commandRepositoryFake) Get(_ context.Context, _, _ foundation.ID) (State, error) {
	fake.getCalls++
	return fake.state, nil
}

func (fake *commandRepositoryFake) List(context.Context, ListQuery) (ArtifactPage, error) {
	return ArtifactPage{}, errors.New("not implemented")
}

func (fake *commandRepositoryFake) ListSectionGenerations(context.Context, foundation.ID, foundation.ID) (SectionGenerationSnapshot, error) {
	return fake.generationSnapshot, fake.generationErr
}

func (fake *commandRepositoryFake) GetExport(context.Context, foundation.ID, foundation.ID, foundation.ID) (ExportRecord, error) {
	return ExportRecord{}, errors.New("not implemented")
}

type publicationCreatorFake struct {
	calls   int
	request domain.PublicationRequest
	id      foundation.ID
}

func (fake *publicationCreatorFake) CreateArtifactPublication(_ context.Context, request domain.PublicationRequest, _ string) (foundation.ID, error) {
	fake.calls++
	fake.request = request
	return fake.id, nil
}
