package application

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestServicePlansGeneratesAndOnlyRequestsPublication(t *testing.T) {
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	service, err := NewService(&sequenceIDs{next: 1}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.PlanArtifact(PlanArtifactCommand{WorkspaceID: appID(90), Type: "study-guide", Title: "Go concurrency", ScopeDefinition: "approved Go concurrency material"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Artifact.Status != domain.StatusPlanning || state.Revision.RevisionNo != 1 {
		t.Fatalf("unexpected plan result: %+v", state)
	}
	state, err = service.SubmitOutline(SubmitOutlineCommand{RevisionCommand: RevisionCommand{Artifact: state.Artifact, Revision: state.Revision}, Outline: []domain.OutlineSection{{Key: "channels", Title: "Channels"}}})
	if err != nil {
		t.Fatal(err)
	}
	state, err = service.ApproveOutline(RevisionCommand{Artifact: state.Artifact, Revision: state.Revision})
	if err != nil {
		t.Fatal(err)
	}
	section := domain.Section{Key: "channels", Title: "Channels", Content: "Channels synchronize work.", Citations: []domain.Citation{{SourceVersionID: appID(30), SourceSpanID: appID(31), VerifiedContentHash: strings.Repeat("a", 64), Excerpt: "Verified source span", Verified: true}}, Coverage: domain.Coverage{SectionKey: "channels", Status: domain.CoverageCovered, Gaps: []domain.Gap{}}}
	state, err = service.GenerateSection(RecordSectionCommand{RevisionCommand: RevisionCommand{Artifact: state.Artifact, Revision: state.Revision}, Section: section, Metadata: &domain.GenerationMetadata{PromptVersion: "artifact-prompt/v1", ModelVersion: "model/v1", WorkflowDefinitionVersion: "workflow/v1", SchemaVersion: "artifact-output/v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if state.Artifact.Status != domain.StatusDraft || state.Revision.CreatedBy != domain.CreatorAgent {
		t.Fatalf("generation did not create agent draft revision: %+v", state)
	}
	state, err = service.ApproveDraft(RevisionCommand{Artifact: state.Artifact, Revision: state.Revision})
	if err != nil {
		t.Fatal(err)
	}
	state, request, err := service.PublishArtifactProposal(PublicationCommand{RevisionCommand: RevisionCommand{Artifact: state.Artifact, Revision: state.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	if state.Artifact.Status != domain.StatusPublishProposed || request.ProposalType != domain.PublishArtifactProposalType || request.RevisionID != state.Revision.ID || request.ContentHash != state.Revision.ContentHash {
		t.Fatalf("publication did not remain a domain request: state=%+v request=%+v", state, request)
	}
}

func TestServiceRejectsIncompleteDependencies(t *testing.T) {
	if _, err := NewService(nil, foundation.SystemClock{}); err == nil {
		t.Fatal("expected missing id generator to fail")
	}
}

type sequenceIDs struct{ next int }

func (generator *sequenceIDs) New() (foundation.ID, error) {
	id := appID(generator.next)
	generator.next++
	return id, nil
}

func appID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", value))
}
