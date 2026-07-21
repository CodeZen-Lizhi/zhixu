package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestRepositoryKnowledgeChangeProposalRoundTripAndReplay(t *testing.T) {
	pool, ctx := newChangeControlMigrationTestPool(t)
	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,'Typed Proposal Test',$2,$2,$3,'test',1,$3,$3)`,
		string(workspaceID), "/tmp/typed-proposal-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	change := typedProposalKnowledgeChange()
	changeHash, err := domain.ComputeKnowledgeChangeHash(change, "medium", "create a corrective relation proposal")
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := domain.ComputeKnowledgeChangeRequestHash(workspaceID, change, "medium", "create a corrective relation proposal")
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID:             "71000000-0000-4000-8000-000000000002",
		WorkspaceID:    workspaceID,
		Type:           domain.ProposalTypeKnowledgeChange,
		IdempotencyKey: "typed-proposal-round-trip",
		RequestHash:    requestHash,
		Status:         domain.StatusReady,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
		Revision: domain.Revision{
			ID:              "71000000-0000-4000-8000-000000000003",
			ProposalID:      "71000000-0000-4000-8000-000000000002",
			RevisionNo:      1,
			Risk:            "medium",
			RollbackPlan:    "create a corrective relation proposal",
			ChangeHash:      changeHash,
			KnowledgeChange: &change,
			CreatedAt:       now,
		},
	}

	created, err := repository.CreateKnowledgeChangeProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	assertTypedProposalRoundTrip(t, created, proposal)

	replay := proposal
	replay.ID = "71000000-0000-4000-8000-000000000004"
	replay.Revision.ID = "71000000-0000-4000-8000-000000000005"
	replay.Revision.ProposalID = replay.ID
	replayed, err := repository.CreateKnowledgeChangeProposal(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != proposal.ID {
		t.Fatalf("replayed proposal id = %s, want %s", replayed.ID, proposal.ID)
	}
	assertTypedProposalRoundTrip(t, replayed, proposal)

	conflict := replay
	conflict.ID = "71000000-0000-4000-8000-000000000006"
	conflict.Revision.ID = "71000000-0000-4000-8000-000000000007"
	conflict.Revision.ProposalID = conflict.ID
	conflict.RequestHash = strings.Repeat("f", 64)
	if _, err := repository.CreateKnowledgeChangeProposal(ctx, conflict); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("idempotency conflict error = %v", err)
	}

	approval := domain.Approval{
		ID:         "71000000-0000-4000-8000-000000000008",
		ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash,
		Decision:   domain.DecisionApproved,
		DecidedAt:  now.Add(time.Minute),
	}
	if _, err := repository.Approve(ctx, approval); err != nil {
		t.Fatal(err)
	}
	approved, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTypedProposalRoundTrip(t, approved, proposal)
	if approved.Approval == nil || approved.Approval.ID != approval.ID || approved.Approval.ApprovedGitHead != nil {
		t.Fatalf("approved typed proposal = %#v", approved)
	}
}

func typedProposalKnowledgeChange() domain.KnowledgeChange {
	return domain.KnowledgeChange{
		TargetRefs: []domain.KnowledgeTargetRef{{
			Type:        domain.KnowledgeTargetRefRelationCandidate,
			ID:          "72000000-0000-4000-8000-000000000001",
			Fingerprint: strings.Repeat("a", 64),
		}},
		BaseVersions: []domain.KnowledgeBaseVersion{
			{NodeType: knowledge.NodeTypeClaim, NodeID: "73000000-0000-4000-8000-000000000001", Version: 3},
			{NodeType: knowledge.NodeTypeTopic, NodeID: "73000000-0000-4000-8000-000000000002", Version: 5},
		},
		ChangeSet: domain.KnowledgeChangeSet{
			Operation:    domain.KnowledgeChangeOperationCreateRelation,
			Source:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: "73000000-0000-4000-8000-000000000001"},
			Target:       knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: "73000000-0000-4000-8000-000000000002"},
			RelationType: knowledge.RelationBelongsTo,
		},
		EvidenceRefs: []domain.KnowledgeEvidenceRef{{
			CandidateEvidenceID: "74000000-0000-4000-8000-000000000001",
			SemanticHash:        strings.Repeat("b", 64),
		}},
		SchemaVersion: domain.KnowledgeChangeSchemaVersion,
	}
}

func assertTypedProposalRoundTrip(t *testing.T, got, want domain.Proposal) {
	t.Helper()
	if got.ID != want.ID || got.WorkspaceID != want.WorkspaceID || got.Type != domain.ProposalTypeKnowledgeChange ||
		got.TargetPath != "" || got.Revision.TargetPath != "" || got.Revision.BaseHash != "" ||
		got.Revision.Content != "" || got.Revision.EvidenceSummary != "" || got.Revision.KnowledgeChange == nil ||
		got.Revision.ChangeHash != want.Revision.ChangeHash {
		t.Fatalf("typed proposal = %#v, want binding %#v", got, want)
	}
	if hash, err := domain.ComputeKnowledgeChangeHash(*got.Revision.KnowledgeChange, got.Revision.Risk, got.Revision.RollbackPlan); err != nil || hash != got.Revision.ChangeHash {
		t.Fatalf("typed proposal hash = %s, err = %v", hash, err)
	}
}
