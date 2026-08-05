package domain

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestArtifactLifecycleKeepsRevisionsImmutableAndIsolated(t *testing.T) {
	now := artifactTime()
	artifact, revision, err := PlanArtifact(PlanInput{ArtifactID: artifactID(1), InitialRevisionID: artifactID(2), WorkspaceID: artifactID(3), Type: "study-guide", Title: "Go concurrency", ScopeDefinition: "approved Go concurrency material", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	plannedArtifact, plannedRevision := CloneArtifact(artifact), CloneRevision(revision)
	if err := ValidateRevision(revision); err != nil {
		t.Fatalf("planned revision invalid: %v", err)
	}

	outline := []OutlineSection{{Key: "channels", Title: "Channels"}, {Key: "context", Title: "Context"}}
	artifact, revision, err = SubmitOutline(artifact, revision, artifactID(4), outline, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusOutlineReview || revision.RevisionNo != 2 || revision.ID != artifactID(4) {
		t.Fatalf("unexpected outline result: artifact=%+v revision=%+v", artifact, revision)
	}
	if !reflect.DeepEqual(plannedArtifact, CloneArtifact(mustPlan(t, now))) {
		t.Fatalf("planning artifact was mutated: %+v", plannedArtifact)
	}
	if plannedRevision.RevisionNo != 1 || len(plannedRevision.Outline) != 0 {
		t.Fatalf("planning revision was mutated: %+v", plannedRevision)
	}

	artifact, revision, err = ApproveOutline(artifact, revision, artifactID(5), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusGenerating || revision.RevisionNo != 3 {
		t.Fatalf("outline approval did not enter generating: %+v", artifact)
	}

	first := verifiedSection("channels", "Channels", "Use channels to synchronize work.", 20, 21)
	artifact, revision, err = RecordSection(artifact, revision, artifactID(6), first, CreatorAgent, agentMetadata(), now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusGenerating || len(revision.Sections) != 1 || len(artifact.SourceCoverage) != 1 {
		t.Fatalf("first section should retain generating state: artifact=%+v revision=%+v", artifact, revision)
	}
	firstSnapshot := CloneRevision(revision)

	second := verifiedSection("context", "Context", "Context carries cancellation.", 22, 23)
	artifact, revision, err = RecordSection(artifact, revision, artifactID(7), second, CreatorAgent, agentMetadata(), now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusDraft || revision.RevisionNo != 5 || len(revision.Sections) != 2 || len(artifact.SourceCoverage) != 2 {
		t.Fatalf("complete revision should become draft: artifact=%+v revision=%+v", artifact, revision)
	}
	mutatedLatest := CloneRevision(revision)
	mutatedLatest.Sections[0].Content = "caller-side mutation"
	if revision.Sections[0].Content != "Use channels to synchronize work." || firstSnapshot.Sections[0].Content != "Use channels to synchronize work." || len(firstSnapshot.Sections) != 1 {
		t.Fatalf("previous revision changed after a new section: %+v", firstSnapshot)
	}

	artifact, err = ApproveDraft(artifact, revision, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusApproved {
		t.Fatalf("artifact status = %s", artifact.Status)
	}
	artifact, err = MarkExported(artifact, revision, now.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusExported {
		t.Fatalf("artifact status = %s", artifact.Status)
	}
	artifact, request, err := CreatePublicationRequest(artifact, revision, now.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusPublishProposed || request.ProposalType != PublishArtifactProposalType || request.RevisionID != revision.ID || request.ContentHash != revision.ContentHash || request.ArtifactVersion != artifact.Version || !reflect.DeepEqual(request.SourceCoverage, artifact.SourceCoverage) {
		t.Fatalf("publication request is not bound to the approved revision: artifact=%+v request=%+v", artifact, request)
	}
	request.SourceCoverage[0].SectionKey = "caller-mutation"
	if artifact.SourceCoverage[0].SectionKey == "caller-mutation" {
		t.Fatal("publication request shares source coverage with artifact")
	}
	artifact, err = ConfirmPublished(artifact, revision, PublicationConfirmation{ProposalID: artifactID(80), FormalDocumentID: artifactID(81), ConfirmedAt: now.Add(8 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusPublished {
		t.Fatalf("artifact status = %s", artifact.Status)
	}
}

func TestRecordSectionRejectsUnverifiedContentAndSilentGap(t *testing.T) {
	artifact, revision := generatingArtifact(t)
	section := verifiedSection("channels", "Channels", "generated without proof", 20, 21)
	section.Citations[0].Verified = false
	_, _, err := RecordSection(artifact, revision, artifactID(10), section, CreatorAgent, agentMetadata(), artifactTime().Add(3*time.Minute))
	assertErrorCode(t, err, ErrorCodeArtifactCoverageInvalid)

	section = Section{Key: "channels", Title: "Channels", Content: "invented text", Citations: []Citation{}, Coverage: Coverage{SectionKey: "channels", Status: CoverageGap, Gaps: []Gap{{Code: "NO_SOURCE", Description: "No verified source"}}}}
	_, _, err = RecordSection(artifact, revision, artifactID(11), section, CreatorHuman, nil, artifactTime().Add(3*time.Minute))
	assertErrorCode(t, err, ErrorCodeArtifactCoverageInvalid)
}

func TestRecordSectionPreservesExplicitKnowledgeGap(t *testing.T) {
	artifact, revision := generatingArtifact(t)
	gap := Section{Key: "channels", Title: "Channels", Content: "", Citations: []Citation{}, Coverage: Coverage{SectionKey: "channels", Status: CoverageGap, Gaps: []Gap{{Code: "NO_SOURCE", Description: "No verified source"}}}}
	artifact, revision, err := RecordSection(artifact, revision, artifactID(12), gap, CreatorHuman, nil, artifactTime().Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusDraft || revision.Sections[0].Coverage.Status != CoverageGap || revision.Sections[0].Content != "" || len(revision.Sections[0].Citations) != 0 {
		t.Fatalf("knowledge gap was not retained explicitly: artifact=%+v revision=%+v", artifact, revision)
	}
}

func TestStateTransitionsFailClosed(t *testing.T) {
	artifact, revision := generatingArtifact(t)
	_, _, err := CreatePublicationRequest(artifact, revision, artifactTime().Add(time.Minute))
	assertErrorCode(t, err, ErrorCodeArtifactPublicationInvalid)
	_, err = MarkExported(artifact, revision, artifactTime().Add(time.Minute))
	assertErrorCode(t, err, ErrorCodeArtifactTransitionInvalid)
	_, err = ConfirmPublished(artifact, revision, PublicationConfirmation{ProposalID: artifactID(70), FormalDocumentID: artifactID(71), ConfirmedAt: artifactTime().Add(time.Minute)})
	assertErrorCode(t, err, ErrorCodeArtifactPublicationInvalid)
}

func TestStartRevisionCreatesAnotherSnapshotAndArchivePreservesIt(t *testing.T) {
	artifact, revision := generatingArtifact(t)
	section := verifiedSection("channels", "Channels", "Initial verified content.", 20, 21)
	artifact, revision, err := RecordSection(artifact, revision, artifactID(40), section, CreatorHuman, nil, artifactTime().Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	firstDraft := CloneRevision(revision)
	artifact, err = StartRevision(artifact, revision, artifactTime().Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusGenerating {
		t.Fatalf("revision status = %s", artifact.Status)
	}
	section.Content = "Revised verified content."
	artifact, revision, err = RecordSection(artifact, revision, artifactID(41), section, CreatorHuman, nil, artifactTime().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if revision.RevisionNo != firstDraft.RevisionNo+1 || revision.ContentHash == firstDraft.ContentHash || firstDraft.Sections[0].Content != "Initial verified content." {
		t.Fatalf("revision snapshot was not preserved: old=%+v new=%+v", firstDraft, revision)
	}
	artifact, err = Archive(artifact, revision, artifactTime().Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != StatusArchived || artifact.CurrentRevisionID != revision.ID {
		t.Fatalf("archive lost current revision binding: %+v", artifact)
	}
}

func TestRevisionHashIsCanonicalAndTamperEvident(t *testing.T) {
	artifact, revision := generatingArtifact(t)
	section := verifiedSection("channels", "Channels", "Use channels to synchronize work.", 20, 21)
	section.Citations = append(section.Citations, Citation{SourceVersionID: artifactID(25), SourceSpanID: artifactID(26), VerifiedContentHash: strings.Repeat("b", 64), Excerpt: "A verified span", Verified: true})
	artifact, revision, err := RecordSection(artifact, revision, artifactID(30), section, CreatorAgent, agentMetadata(), artifactTime().Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	copyValue := CloneRevision(revision)
	copyValue.Sections[0].Citations[0], copyValue.Sections[0].Citations[1] = copyValue.Sections[0].Citations[1], copyValue.Sections[0].Citations[0]
	hash, err := ComputeRevisionContentHash(copyValue)
	if err != nil {
		t.Fatal(err)
	}
	if hash != revision.ContentHash {
		t.Fatalf("canonical hash changed with citation order: %s != %s", hash, revision.ContentHash)
	}
	copyValue.Sections[0].Content = "tampered"
	assertErrorCode(t, ValidateRevision(copyValue), ErrorCodeArtifactInvalid)
	if artifact.CurrentRevisionID != revision.ID {
		t.Fatalf("artifact current revision = %s", artifact.CurrentRevisionID)
	}
}

func TestDocumentSourcesUseRevisionV2WithoutChangingLegacyHashes(t *testing.T) {
	artifact, revision := generatingArtifact(t)
	legacy := verifiedSection("channels", "Channels", "Legacy verified content.", 20, 21)
	_, legacyRevision, err := RecordSection(artifact, revision, artifactID(30), legacy, CreatorAgent, agentMetadata(), artifactTime().Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	legacyWithExplicitEmptySources := CloneRevision(legacyRevision)
	legacyWithExplicitEmptySources.Sections[0].DocumentSources = []DocumentSource{}
	legacyHash, err := ComputeRevisionContentHash(legacyWithExplicitEmptySources)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema, err := RevisionSchemaVersion(legacyWithExplicitEmptySources)
	if err != nil {
		t.Fatal(err)
	}
	if legacyHash != legacyRevision.ContentHash || legacySchema != RevisionSchemaV1 {
		t.Fatalf("legacy hash/schema drifted: got %s/%s want %s/%s", legacyHash, legacySchema, legacyRevision.ContentHash, RevisionSchemaV1)
	}

	documentBacked := Section{
		Key: "channels", Title: "Channels", Content: "Content from an immutable document revision.", Citations: []Citation{},
		DocumentSources: []DocumentSource{
			{DocumentID: artifactID(40), ArticleRevisionID: artifactID(41), RevisionNo: 2, VerifiedContentHash: strings.Repeat("c", 64), Verified: true},
			{DocumentID: artifactID(42), ArticleRevisionID: artifactID(43), RevisionNo: 1, VerifiedContentHash: strings.Repeat("d", 64), Verified: true},
		},
		Coverage: Coverage{SectionKey: "channels", Status: CoverageCovered, Gaps: []Gap{}},
	}
	_, v2Revision, err := RecordSection(artifact, revision, artifactID(31), documentBacked, CreatorAgent, agentMetadata(), artifactTime().Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	v2Schema, err := RevisionSchemaVersion(v2Revision)
	if err != nil {
		t.Fatal(err)
	}
	if v2Schema != RevisionSchemaV2 || v2Revision.ContentHash == legacyRevision.ContentHash {
		t.Fatalf("document-backed revision did not use v2: schema=%s hash=%s", v2Schema, v2Revision.ContentHash)
	}
	reordered := CloneRevision(v2Revision)
	reordered.Sections[0].DocumentSources[0], reordered.Sections[0].DocumentSources[1] = reordered.Sections[0].DocumentSources[1], reordered.Sections[0].DocumentSources[0]
	reorderedHash, err := ComputeRevisionContentHash(reordered)
	if err != nil || reorderedHash != v2Revision.ContentHash {
		t.Fatalf("document source order changed canonical hash: hash=%s err=%v", reorderedHash, err)
	}
	tampered := CloneRevision(v2Revision)
	tampered.Sections[0].DocumentSources[0].RevisionNo++
	assertErrorCode(t, ValidateRevision(tampered), ErrorCodeArtifactInvalid)
}

func TestDocumentSourceCoverageSemanticsFailClosed(t *testing.T) {
	artifact, revision := generatingArtifact(t)
	source := DocumentSource{DocumentID: artifactID(50), ArticleRevisionID: artifactID(51), RevisionNo: 1, VerifiedContentHash: strings.Repeat("e", 64), Verified: true}
	covered := Section{Key: "channels", Title: "Channels", Content: "Document-backed content.", Citations: []Citation{}, DocumentSources: []DocumentSource{source}, Coverage: Coverage{SectionKey: "channels", Status: CoverageCovered, Gaps: []Gap{}}}
	if _, _, err := RecordSection(artifact, revision, artifactID(52), covered, CreatorAgent, agentMetadata(), artifactTime().Add(3*time.Minute)); err != nil {
		t.Fatalf("document-only covered section was rejected: %v", err)
	}
	gap := covered
	gap.Content = ""
	gap.Coverage = Coverage{SectionKey: "channels", Status: CoverageGap, Gaps: []Gap{{Code: "NO_SOURCE", Description: "No source"}}}
	_, _, err := RecordSection(artifact, revision, artifactID(53), gap, CreatorAgent, agentMetadata(), artifactTime().Add(3*time.Minute))
	assertErrorCode(t, err, ErrorCodeArtifactCoverageInvalid)
}

func generatingArtifact(t *testing.T) (Artifact, Revision) {
	t.Helper()
	now := artifactTime()
	artifact, revision, err := PlanArtifact(PlanInput{ArtifactID: artifactID(1), InitialRevisionID: artifactID(2), WorkspaceID: artifactID(3), Type: "study-guide", Title: "Go concurrency", ScopeDefinition: "approved Go concurrency material", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	artifact, revision, err = SubmitOutline(artifact, revision, artifactID(4), []OutlineSection{{Key: "channels", Title: "Channels"}}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	artifact, revision, err = ApproveOutline(artifact, revision, artifactID(5), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return artifact, revision
}

func mustPlan(t *testing.T, now time.Time) Artifact {
	t.Helper()
	artifact, _, err := PlanArtifact(PlanInput{ArtifactID: artifactID(1), InitialRevisionID: artifactID(2), WorkspaceID: artifactID(3), Type: "study-guide", Title: "Go concurrency", ScopeDefinition: "approved Go concurrency material", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func verifiedSection(key, title, content string, sourceVersion, sourceSpan int) Section {
	return Section{Key: key, Title: title, Content: content, Citations: []Citation{{SourceVersionID: artifactID(sourceVersion), SourceSpanID: artifactID(sourceSpan), VerifiedContentHash: strings.Repeat("a", 64), Excerpt: "A verified span", Verified: true}}, Coverage: Coverage{SectionKey: key, Status: CoverageCovered, Gaps: []Gap{}}}
}

func agentMetadata() *GenerationMetadata {
	return &GenerationMetadata{PromptVersion: "artifact-prompt/v1", ModelVersion: "model/v1", WorkflowDefinitionVersion: "workflow/v1", SchemaVersion: "artifact-output/v1"}
}

func artifactID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", value))
}

func artifactTime() time.Time {
	return time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
}

func assertErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *foundation.Error
	if err == nil || !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}
