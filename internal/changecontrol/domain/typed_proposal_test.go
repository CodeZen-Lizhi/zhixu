package domain

import (
	"errors"
	"strings"
	"testing"

	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestKnowledgeChangeHashIsStableAcrossOrderingAndCanonicalization(t *testing.T) {
	first := KnowledgeChange{
		TargetRefs: []KnowledgeTargetRef{{
			Type: KnowledgeTargetRefRelationCandidate, ID: "70000000-0000-4000-8000-000000000002", Fingerprint: strings.ToUpper(strings.Repeat("a", 64)),
		}, {
			Type: KnowledgeTargetRefRelationCandidate, ID: "70000000-0000-4000-8000-000000000001", Fingerprint: strings.Repeat("b", 64),
		}},
		BaseVersions: []KnowledgeBaseVersion{
			{NodeType: knowledge.NodeTypeClaim, NodeID: "90000000-0000-4000-8000-000000000003", Version: 8},
			{NodeType: knowledge.NodeTypeClaim, NodeID: "90000000-0000-4000-8000-000000000001", Version: 3},
		},
		ChangeSet: KnowledgeChangeSet{
			Operation:    KnowledgeChangeOperationCreateRelation,
			Source:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: "90000000-0000-4000-8000-000000000003"},
			Target:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: "90000000-0000-4000-8000-000000000001"},
			RelationType: knowledge.RelationConflictsWith,
		},
		EvidenceRefs: []KnowledgeEvidenceRef{{
			CandidateEvidenceID: "80000000-0000-4000-8000-000000000002", SemanticHash: strings.ToUpper(strings.Repeat("c", 64)),
		}, {
			CandidateEvidenceID: "80000000-0000-4000-8000-000000000001", SemanticHash: strings.Repeat("d", 64),
		}},
		SchemaVersion: KnowledgeChangeSchemaVersion,
	}
	second := KnowledgeChange{
		TargetRefs: []KnowledgeTargetRef{first.TargetRefs[1], first.TargetRefs[0]},
		BaseVersions: []KnowledgeBaseVersion{
			first.BaseVersions[1],
			first.BaseVersions[0],
		},
		ChangeSet: KnowledgeChangeSet{
			Operation:    KnowledgeChangeOperationCreateRelation,
			Source:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: "90000000-0000-4000-8000-000000000001"},
			Target:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: "90000000-0000-4000-8000-000000000003"},
			RelationType: knowledge.RelationConflictsWith,
		},
		EvidenceRefs: []KnowledgeEvidenceRef{
			first.EvidenceRefs[1],
			first.EvidenceRefs[0],
		},
		SchemaVersion: KnowledgeChangeSchemaVersion,
	}
	firstHash, err := ComputeKnowledgeChangeHash(first, "medium", "restore relation")
	if err != nil {
		t.Fatalf("first hash error = %v", err)
	}
	secondHash, err := ComputeKnowledgeChangeHash(second, "medium", "restore relation")
	if err != nil {
		t.Fatalf("second hash error = %v", err)
	}
	if firstHash != secondHash {
		t.Fatalf("hash mismatch: %s != %s", firstHash, secondHash)
	}
}

func TestValidateKnowledgeChangeRejectsMissingEvidenceAndIncompatibleEndpoints(t *testing.T) {
	invalid := KnowledgeChange{
		TargetRefs: []KnowledgeTargetRef{{
			Type: KnowledgeTargetRefRelationCandidate, ID: "70000000-0000-4000-8000-000000000001", Fingerprint: strings.Repeat("a", 64),
		}},
		BaseVersions: []KnowledgeBaseVersion{
			{NodeType: knowledge.NodeTypeClaim, NodeID: "90000000-0000-4000-8000-000000000003", Version: 8},
			{NodeType: knowledge.NodeTypeTopic, NodeID: "90000000-0000-4000-8000-000000000002", Version: 4},
		},
		ChangeSet: KnowledgeChangeSet{
			Operation:    KnowledgeChangeOperationCreateRelation,
			Source:       knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: "90000000-0000-4000-8000-000000000002"},
			Target:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: "90000000-0000-4000-8000-000000000003"},
			RelationType: knowledge.RelationBelongsTo,
		},
		SchemaVersion: KnowledgeChangeSchemaVersion,
	}
	if _, err := ValidateKnowledgeChange(invalid); err == nil {
		t.Fatal("ValidateKnowledgeChange() accepted invalid endpoints and empty evidence")
	}
}

func TestValidateKnowledgeChangeRejectsDuplicateBaseVersionEndpoint(t *testing.T) {
	invalid := KnowledgeChange{
		TargetRefs: []KnowledgeTargetRef{{
			Type: KnowledgeTargetRefRelationCandidate, ID: "70000000-0000-4000-8000-000000000001", Fingerprint: strings.Repeat("a", 64),
		}},
		BaseVersions: []KnowledgeBaseVersion{
			{NodeType: knowledge.NodeTypeClaim, NodeID: "90000000-0000-4000-8000-000000000003", Version: 8},
			{NodeType: knowledge.NodeTypeClaim, NodeID: "90000000-0000-4000-8000-000000000003", Version: 9},
		},
		ChangeSet: KnowledgeChangeSet{
			Operation:    KnowledgeChangeOperationCreateRelation,
			Source:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: "90000000-0000-4000-8000-000000000003"},
			Target:       knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: "90000000-0000-4000-8000-000000000002"},
			RelationType: knowledge.RelationBelongsTo,
		},
		EvidenceRefs: []KnowledgeEvidenceRef{{
			CandidateEvidenceID: "80000000-0000-4000-8000-000000000004", SemanticHash: strings.Repeat("b", 64),
		}},
		SchemaVersion: KnowledgeChangeSchemaVersion,
	}
	if _, err := ValidateKnowledgeChange(invalid); err == nil {
		t.Fatal("ValidateKnowledgeChange() accepted two versions for the source endpoint while omitting the target")
	}
}

func TestKnowledgeChangeRequestHashBindsWorkspaceAndRollbackPlan(t *testing.T) {
	change := KnowledgeChange{
		TargetRefs: []KnowledgeTargetRef{{
			Type: KnowledgeTargetRefRelationCandidate, ID: "70000000-0000-4000-8000-000000000001", Fingerprint: strings.Repeat("a", 64),
		}},
		BaseVersions: []KnowledgeBaseVersion{
			{NodeType: knowledge.NodeTypeClaim, NodeID: "90000000-0000-4000-8000-000000000003", Version: 8},
			{NodeType: knowledge.NodeTypeTopic, NodeID: "90000000-0000-4000-8000-000000000002", Version: 4},
		},
		ChangeSet: KnowledgeChangeSet{
			Operation:    KnowledgeChangeOperationCreateRelation,
			Source:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: "90000000-0000-4000-8000-000000000003"},
			Target:       knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: "90000000-0000-4000-8000-000000000002"},
			RelationType: knowledge.RelationBelongsTo,
		},
		EvidenceRefs: []KnowledgeEvidenceRef{{
			CandidateEvidenceID: "80000000-0000-4000-8000-000000000004", SemanticHash: strings.Repeat("b", 64),
		}},
		SchemaVersion: KnowledgeChangeSchemaVersion,
	}
	firstHash, err := ComputeKnowledgeChangeRequestHash("10000000-0000-4000-8000-000000000001", change, "medium", "restore relation")
	if err != nil {
		t.Fatalf("first hash error = %v", err)
	}
	const wantV1 = "2d23a69a5734cccd86e34cb168080aacea6cf5c1e2ed01d9f58f7fb286f22bc9"
	if firstHash != wantV1 {
		t.Fatalf("knowledge change v1 request hash = %s, want %s", firstHash, wantV1)
	}
	secondHash, err := ComputeKnowledgeChangeRequestHash("10000000-0000-4000-8000-000000000002", change, "medium", "restore relation")
	if err != nil {
		t.Fatalf("second hash error = %v", err)
	}
	thirdHash, err := ComputeKnowledgeChangeRequestHash("10000000-0000-4000-8000-000000000001", change, "medium", "different rollback")
	if err != nil {
		t.Fatalf("third hash error = %v", err)
	}
	if firstHash == secondHash || firstHash == thirdHash {
		t.Fatalf("request hash did not bind workspace and rollback: %s %s %s", firstHash, secondHash, thirdHash)
	}
	mediumV2, err := ComputeKnowledgeChangeRequestHashWithRiskLevel("10000000-0000-4000-8000-000000000001", change, ProposalRiskLevelMedium, "medium", "restore relation")
	if err != nil {
		t.Fatalf("medium v2 hash error = %v", err)
	}
	highV2, err := ComputeKnowledgeChangeRequestHashWithRiskLevel("10000000-0000-4000-8000-000000000001", change, ProposalRiskLevelHigh, "medium", "restore relation")
	if err != nil {
		t.Fatalf("high v2 hash error = %v", err)
	}
	if mediumV2 == highV2 || mediumV2 == firstHash || highV2 == firstHash {
		t.Fatalf("knowledge request hashes do not bind schema and risk level: v1=%s medium=%s high=%s", firstHash, mediumV2, highV2)
	}
}

func TestPublishArtifactHashCanonicalizesCoverageAndBindsFrozenFields(t *testing.T) {
	first := PublishArtifact{
		WorkspaceID: "62000000-0000-4000-8000-000000000001", ArtifactID: "62000000-0000-4000-8000-000000000002", RevisionID: "62000000-0000-4000-8000-000000000003",
		RevisionNo: 3, ArtifactVersion: 5, ContentHash: strings.ToUpper(strings.Repeat("a", 64)), SchemaVersion: PublishArtifactSchemaVersion,
		SourceCoverage: []ArtifactSourceCoverage{
			{SectionKey: "b", Status: ArtifactCoveragePartial, Gaps: []ArtifactCoverageGap{{Code: "G2", Description: "second"}, {Code: "G1", Description: "first"}}},
			{SectionKey: "a", Status: ArtifactCoverageCovered},
		},
	}
	second := first
	second.ContentHash = strings.Repeat("a", 64)
	second.SourceCoverage = []ArtifactSourceCoverage{first.SourceCoverage[1], first.SourceCoverage[0]}
	second.SourceCoverage[1].Gaps = []ArtifactCoverageGap{first.SourceCoverage[0].Gaps[1], first.SourceCoverage[0].Gaps[0]}
	firstHash, err := ComputePublishArtifactHash(first, "publication impact", "retain isolated artifact")
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := ComputePublishArtifactHash(second, "publication impact", "retain isolated artifact")
	if err != nil || firstHash != secondHash {
		t.Fatalf("canonical hash first=%s second=%s err=%v", firstHash, secondHash, err)
	}
	requestHash, err := ComputePublishArtifactRequestHash(first.WorkspaceID, first, ProposalRiskLevelHigh, "publication impact", "retain isolated artifact")
	if err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.ArtifactVersion++
	changedRequestHash, err := ComputePublishArtifactRequestHash(changed.WorkspaceID, changed, ProposalRiskLevelHigh, "publication impact", "retain isolated artifact")
	if err != nil || requestHash == changedRequestHash {
		t.Fatalf("request hash failed to bind artifact version: first=%s changed=%s err=%v", requestHash, changedRequestHash, err)
	}
}

func TestPublishArtifactRejectsIncompleteCoverageAndMixedUnion(t *testing.T) {
	publication := PublishArtifact{
		WorkspaceID: "62000000-0000-4000-8000-000000000001", ArtifactID: "62000000-0000-4000-8000-000000000002", RevisionID: "62000000-0000-4000-8000-000000000003",
		RevisionNo: 1, ArtifactVersion: 1, ContentHash: strings.Repeat("a", 64), SchemaVersion: PublishArtifactSchemaVersion,
		SourceCoverage: []ArtifactSourceCoverage{{SectionKey: "gap", Status: ArtifactCoverageStatusGap}},
	}
	if _, err := ValidatePublishArtifact(publication); !errors.Is(err, ErrPublishArtifactInvalid) {
		t.Fatalf("invalid coverage error=%v", err)
	}
	publication.SourceCoverage[0].Gaps = []ArtifactCoverageGap{{Code: "NO_SOURCE", Description: "no verified source"}}
	hash, err := ComputePublishArtifactHash(publication, "impact", "retain artifact")
	if err != nil {
		t.Fatal(err)
	}
	revision := Revision{TargetPath: "notes/a.md", Risk: "impact", RollbackPlan: "retain artifact", ChangeHash: hash, PublishArtifact: &publication}
	if err := ValidateProposalRevisionForType(ProposalTypePublishArtifact, revision); !errors.Is(err, ErrProposalTypeInvalid) {
		t.Fatalf("mixed typed union error=%v", err)
	}
	fileRevision := Revision{TargetPath: "notes/a.md", BaseHash: strings.Repeat("b", 64), Content: "new content", ChangeHash: ComputeChangeHash("notes/a.md", strings.Repeat("b", 64), "new content"), PublishArtifact: &publication}
	if err := ValidateProposalRevisionForType(ProposalTypeFilePatch, fileRevision); !errors.Is(err, ErrProposalTypeInvalid) {
		t.Fatalf("file patch mixed typed union error=%v", err)
	}
}

func TestDownstreamUpdateHashCanonicalizesBindingAndBindsReport(t *testing.T) {
	first := validArtifactDownstreamUpdate()
	first.TargetType = knowledge.ImpactObjectType("artifact")
	first.Action = knowledge.ImpactAction("regenerate_artifact")
	first.ReportFingerprint = strings.ToUpper(first.ReportFingerprint)
	first.OwnerBinding.Artifact.ContentHash = strings.ToUpper(first.OwnerBinding.Artifact.ContentHash)
	first.Reason = "  cited source changed  "

	second := validArtifactDownstreamUpdate()
	firstHash, err := ComputeDownstreamUpdateHash(first, "downstream dependency changed", "no target write has executed")
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := ComputeDownstreamUpdateHash(second, "downstream dependency changed", "no target write has executed")
	if err != nil || firstHash != secondHash {
		t.Fatalf("canonical downstream hash first=%s second=%s err=%v", firstHash, secondHash, err)
	}

	requestHash, err := ComputeDownstreamUpdateRequestHash(second.WorkspaceID, second, ProposalRiskLevelHigh, "downstream dependency changed", "no target write has executed")
	if err != nil {
		t.Fatal(err)
	}
	changed := second
	changed.ReportID = "63000000-0000-4000-8000-000000000009"
	changedHash, err := ComputeDownstreamUpdateRequestHash(changed.WorkspaceID, changed, ProposalRiskLevelHigh, "downstream dependency changed", "no target write has executed")
	if err != nil || requestHash == changedHash {
		t.Fatalf("request hash failed to bind report: first=%s changed=%s err=%v", requestHash, changedHash, err)
	}
}

func TestDownstreamUpdateValidatesReviewBindingAndRejectsMixedUnion(t *testing.T) {
	update := DownstreamUpdate{
		WorkspaceID: "64000000-0000-4000-8000-000000000001", ReportID: "64000000-0000-4000-8000-000000000002",
		AnalysisVersion: knowledge.ImpactAnalysisVersionV2, ReportFingerprint: strings.Repeat("a", 64),
		SourceEventID: "64000000-0000-4000-8000-000000000003", SourceEventVersion: 4,
		TargetType: knowledge.ImpactObjectReviewCard, TargetID: "64000000-0000-4000-8000-000000000004", BaseVersion: 7,
		Action: knowledge.ImpactActionRevalidateReviewCard,
		OwnerBinding: knowledge.EventOwnerBinding{ReviewCard: &knowledge.ReviewCardImpactBinding{
			CardID: "64000000-0000-4000-8000-000000000004", CardVersion: 7, Status: "invalidated",
			Fingerprint: strings.ToUpper(strings.Repeat("b", 64)), ClaimID: "64000000-0000-4000-8000-000000000005",
			EvidenceBindingFingerprint: strings.ToUpper(strings.Repeat("c", 64)),
		}},
		Reason: "claim evidence changed", SchemaVersion: DownstreamUpdateSchemaVersion,
	}
	canonical, err := ValidateDownstreamUpdate(update)
	if err != nil || canonical.OwnerBinding.ReviewCard.Status != "INVALIDATED" || canonical.OwnerBinding.ReviewCard.Fingerprint != strings.Repeat("b", 64) {
		t.Fatalf("canonical review binding=%#v err=%v", canonical.OwnerBinding.ReviewCard, err)
	}
	hash, err := ComputeDownstreamUpdateHash(canonical, "review dependency changed", "no target write has executed")
	if err != nil {
		t.Fatal(err)
	}
	revision := Revision{
		Risk: "review dependency changed", RollbackPlan: "no target write has executed", ChangeHash: hash,
		DownstreamUpdate: &canonical,
	}
	if err := ValidateProposalRevisionForType(ProposalTypeDownstreamUpdate, revision); err != nil {
		t.Fatalf("valid downstream revision error=%v", err)
	}
	revision.PublishArtifact = &PublishArtifact{}
	if err := ValidateProposalRevisionForType(ProposalTypeDownstreamUpdate, revision); !errors.Is(err, ErrProposalTypeInvalid) {
		t.Fatalf("mixed union error=%v", err)
	}
	canonical.Action = knowledge.ImpactActionRegenerateArtifact
	if _, err := ValidateDownstreamUpdate(canonical); !errors.Is(err, ErrDownstreamUpdateInvalid) {
		t.Fatalf("mismatched target/action error=%v", err)
	}
}

func TestDownstreamUpdateRiskAndTransitionsAreApprovalOnly(t *testing.T) {
	if _, err := ValidateProposalRiskLevelForType(ProposalTypeDownstreamUpdate, ProposalRiskLevelMedium); !errors.Is(err, ErrProposalRiskLevelInvalid) {
		t.Fatalf("medium downstream risk error=%v", err)
	}
	if _, err := ValidateProposalRiskLevelForType(ProposalTypeDownstreamUpdate, ProposalRiskLevelHigh); err != nil {
		t.Fatalf("high downstream risk error=%v", err)
	}
	for _, to := range []ProposalStatus{StatusApproved, StatusRejected} {
		if err := ValidateProposalTransitionForType(ProposalTypeDownstreamUpdate, StatusReady, to); err != nil {
			t.Fatalf("ready -> %s error=%v", to, err)
		}
	}
	for _, transition := range [][2]ProposalStatus{{StatusReady, StatusNeedsRevision}, {StatusApproved, StatusApplying}, {StatusApproved, StatusNeedsRevision}} {
		if err := ValidateProposalTransitionForType(ProposalTypeDownstreamUpdate, transition[0], transition[1]); !errors.Is(err, ErrProposalInvalidTransition) {
			t.Fatalf("downstream transition %s -> %s error=%v", transition[0], transition[1], err)
		}
	}
	if err := ValidateProposalTransitionForType(ProposalTypeFilePatch, StatusApproved, StatusApplying); err != nil {
		t.Fatalf("file patch transition changed: %v", err)
	}
}

func validArtifactDownstreamUpdate() DownstreamUpdate {
	return DownstreamUpdate{
		WorkspaceID: "63000000-0000-4000-8000-000000000001", ReportID: "63000000-0000-4000-8000-000000000002",
		AnalysisVersion: knowledge.ImpactAnalysisVersionV2, ReportFingerprint: strings.Repeat("a", 64),
		SourceEventID: "63000000-0000-4000-8000-000000000003", SourceEventVersion: 3,
		TargetType: knowledge.ImpactObjectArtifact, TargetID: "63000000-0000-4000-8000-000000000004", BaseVersion: 6,
		Action: knowledge.ImpactActionRegenerateArtifact,
		OwnerBinding: knowledge.EventOwnerBinding{Artifact: &knowledge.ArtifactImpactBinding{
			ArtifactID: "63000000-0000-4000-8000-000000000004", ArtifactVersion: 6,
			RevisionID: "63000000-0000-4000-8000-000000000005", RevisionNo: 2, ContentHash: strings.Repeat("b", 64),
		}},
		Reason: "cited source changed", SchemaVersion: DownstreamUpdateSchemaVersion,
	}
}
