package domain

import (
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
