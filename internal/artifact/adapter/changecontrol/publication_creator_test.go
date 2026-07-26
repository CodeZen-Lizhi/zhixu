package changecontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestPublicationCreatorMapsFrozenCoverageToTypedProposal(t *testing.T) {
	request := publicationRequestFixture()
	service := &proposalCreatorFake{}
	creator, err := NewPublicationCreator(service)
	if err != nil {
		t.Fatal(err)
	}
	proposalID, err := creator.CreateArtifactPublication(context.Background(), request, "artifact-publish-1")
	if err != nil {
		t.Fatal(err)
	}
	if proposalID != artifactAdapterID(90) || service.calls != 1 {
		t.Fatalf("proposal id=%q calls=%d", proposalID, service.calls)
	}
	command := service.command
	wantKey, err := proposalIdempotencyKey(command.Publication)
	if err != nil {
		t.Fatal(err)
	}
	if command.WorkspaceID != request.WorkspaceID || command.IdempotencyKey != wantKey || command.IdempotencyKey == "artifact-publish-1" || command.RiskLevel != changecontroldomain.ProposalRiskLevelHigh || command.Publication.SchemaVersion != changecontroldomain.PublishArtifactSchemaVersion {
		t.Fatalf("command=%#v", command)
	}
	if len(command.Publication.SourceCoverage) != 2 || command.Publication.SourceCoverage[0].SectionKey != "limits" || command.Publication.SourceCoverage[0].Status != changecontroldomain.ArtifactCoveragePartial || len(command.Publication.SourceCoverage[0].Gaps) != 1 || command.Publication.SourceCoverage[1].SectionKey != "summary" || command.Publication.SourceCoverage[1].Status != changecontroldomain.ArtifactCoverageCovered {
		t.Fatalf("coverage=%#v", command.Publication.SourceCoverage)
	}
	if !strings.Contains(command.Risk, "Change Control") || !strings.Contains(command.RollbackPlan, "formal Document") {
		t.Fatalf("risk=%q rollback=%q", command.Risk, command.RollbackPlan)
	}
}

func TestPublicationCreatorUsesOneStableProposalKeyForFrozenBinding(t *testing.T) {
	request := publicationRequestFixture()
	first := &proposalCreatorFake{}
	creator, err := NewPublicationCreator(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.CreateArtifactPublication(context.Background(), request, "client-command-one"); err != nil {
		t.Fatal(err)
	}
	second := &proposalCreatorFake{}
	creator, err = NewPublicationCreator(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.CreateArtifactPublication(context.Background(), request, "client-command-two"); err != nil {
		t.Fatal(err)
	}
	if first.command.IdempotencyKey != second.command.IdempotencyKey || first.command.IdempotencyKey == "client-command-one" || len(first.command.IdempotencyKey) > 128 {
		t.Fatalf("first=%q second=%q", first.command.IdempotencyKey, second.command.IdempotencyKey)
	}
	changed := request
	changed.ContentHash = strings.Repeat("b", 64)
	changedKey, err := proposalIdempotencyKey(mustPublication(t, changed))
	if err != nil {
		t.Fatal(err)
	}
	if changedKey == first.command.IdempotencyKey {
		t.Fatal("proposal key did not bind the frozen content hash")
	}
	changedCoverage := publicationRequestFixture()
	changedCoverage.SourceCoverage[1].Gaps[0].Description = "a different verified gap"
	changedCoverageKey, err := proposalIdempotencyKey(mustPublication(t, changedCoverage))
	if err != nil {
		t.Fatal(err)
	}
	if changedCoverageKey == first.command.IdempotencyKey {
		t.Fatal("proposal key did not bind the frozen source coverage")
	}
}

func mustPublication(t *testing.T, request artifactdomain.PublicationRequest) changecontroldomain.PublishArtifact {
	t.Helper()
	publication, err := publicationFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func TestPublicationCreatorFailsClosedForInvalidCoverageOrResult(t *testing.T) {
	request := publicationRequestFixture()
	request.SourceCoverage[0].Status = artifactdomain.CoverageStatus("UNKNOWN")
	creator, err := NewPublicationCreator(&proposalCreatorFake{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.CreateArtifactPublication(context.Background(), request, "artifact-publish-invalid"); err == nil {
		t.Fatal("expected invalid coverage rejection")
	}

	service := &proposalCreatorFake{invalidResult: true}
	creator, err = NewPublicationCreator(service)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.CreateArtifactPublication(context.Background(), publicationRequestFixture(), "artifact-publish-conflict"); err == nil {
		t.Fatal("expected invalid proposal result rejection")
	}
}

func TestPublicationCreatorRejectsEveryFrozenPublicationBindingDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*changecontroldomain.PublishArtifact)
	}{
		{name: "workspace", mutate: func(value *changecontroldomain.PublishArtifact) { value.WorkspaceID = artifactAdapterID(91) }},
		{name: "schema version", mutate: func(value *changecontroldomain.PublishArtifact) { value.SchemaVersion = "artifact-publication/v2" }},
		{name: "source coverage", mutate: func(value *changecontroldomain.PublishArtifact) {
			value.SourceCoverage[0].Gaps[0].Description = "drifted gap"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &proposalCreatorFake{mutateResult: test.mutate}
			creator, err := NewPublicationCreator(service)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := creator.CreateArtifactPublication(context.Background(), publicationRequestFixture(), "artifact-publish-drift"); err == nil {
				t.Fatal("expected frozen publication binding drift rejection")
			}
		})
	}
}

func TestPublicationCreatorPropagatesProposalErrors(t *testing.T) {
	service := &proposalCreatorFake{err: errors.New("change control unavailable")}
	creator, err := NewPublicationCreator(service)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.CreateArtifactPublication(context.Background(), publicationRequestFixture(), "artifact-publish-error"); !errors.Is(err, service.err) {
		t.Fatalf("error=%v", err)
	}
}

type proposalCreatorFake struct {
	calls         int
	command       changecontrolapp.CreatePublishArtifactCommand
	err           error
	invalidResult bool
	mutateResult  func(*changecontroldomain.PublishArtifact)
}

func (fake *proposalCreatorFake) CreatePublishArtifactProposal(_ context.Context, command changecontrolapp.CreatePublishArtifactCommand) (changecontrolapp.CreateResult, error) {
	fake.calls++
	fake.command = command
	if fake.err != nil {
		return changecontrolapp.CreateResult{}, fake.err
	}
	proposal := changecontroldomain.Proposal{ID: artifactAdapterID(90), WorkspaceID: command.WorkspaceID, Type: changecontroldomain.ProposalTypePublishArtifact}
	if !fake.invalidResult {
		publication := command.Publication
		if fake.mutateResult != nil {
			fake.mutateResult(&publication)
		}
		proposal.Revision.PublishArtifact = &publication
	}
	return changecontrolapp.CreateResult{Proposal: proposal}, nil
}

func publicationRequestFixture() artifactdomain.PublicationRequest {
	return artifactdomain.PublicationRequest{
		ProposalType: artifactdomain.PublishArtifactProposalType, WorkspaceID: artifactAdapterID(1), ArtifactID: artifactAdapterID(2), RevisionID: artifactAdapterID(3),
		RevisionNo: 4, ArtifactVersion: 5, ContentHash: strings.Repeat("a", 64), RequestedAt: time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC),
		SourceCoverage: []artifactdomain.Coverage{
			{SectionKey: "summary", Status: artifactdomain.CoverageCovered, Gaps: []artifactdomain.Gap{}},
			{SectionKey: "limits", Status: artifactdomain.CoveragePartial, Gaps: []artifactdomain.Gap{{Code: "NO_SOURCE", Description: "needs an approved source"}}},
		},
	}
}

func artifactAdapterID(value int) foundation.ID {
	return foundation.ID("52000000-0000-4000-8000-" + fmt.Sprintf("%012d", value))
}
