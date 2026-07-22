package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5/pgxpool"
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
		RiskLevel:      domain.ProposalRiskLevelHigh,
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

	seedHistoricalKnowledgeProposalV1(t, ctx, pool, proposal)
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

	v2RequestHash, err := domain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
		workspaceID,
		change,
		domain.ProposalRiskLevelHigh,
		"medium",
		"create a corrective relation proposal",
	)
	if err != nil {
		t.Fatal(err)
	}
	v2Replay := replay
	v2Replay.ID = "71000000-0000-4000-8000-000000000006"
	v2Replay.Revision.ID = "71000000-0000-4000-8000-000000000007"
	v2Replay.Revision.ProposalID = v2Replay.ID
	v2Replay.RiskLevel = domain.ProposalRiskLevelHigh
	v2Replay.RequestHash = v2RequestHash
	replayedV2, err := repository.CreateKnowledgeChangeProposal(ctx, v2Replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayedV2.ID != proposal.ID {
		t.Fatalf("v2 replayed proposal id = %s, want %s", replayedV2.ID, proposal.ID)
	}

	newV1 := proposal
	newV1.ID = "71000000-0000-4000-8000-000000000011"
	newV1.Revision.ID = "71000000-0000-4000-8000-000000000012"
	newV1.Revision.ProposalID = newV1.ID
	newV1.IdempotencyKey = "typed-proposal-v1-new"
	if _, err := repository.CreateKnowledgeChangeProposal(ctx, newV1); !hasCode(err, "PROPOSAL_INVALID") {
		t.Fatalf("new v1 knowledge proposal error = %v", err)
	}

	freshV2 := proposal
	freshV2.ID = "71000000-0000-4000-8000-000000000013"
	freshV2.Revision.ID = "71000000-0000-4000-8000-000000000014"
	freshV2.Revision.ProposalID = freshV2.ID
	freshV2.IdempotencyKey = "typed-proposal-v2-new"
	freshV2.RequestHash = v2RequestHash
	createdFreshV2, err := repository.CreateKnowledgeChangeProposal(ctx, freshV2)
	if err != nil {
		t.Fatalf("new v2 knowledge proposal error = %v", err)
	}
	if createdFreshV2.ID != freshV2.ID || createdFreshV2.RequestHash != v2RequestHash || createdFreshV2.RiskLevel != domain.ProposalRiskLevelHigh {
		t.Fatalf("new v2 knowledge proposal = %#v", createdFreshV2)
	}
	legacyReverse := freshV2
	legacyReverse.ID = "71000000-0000-4000-8000-000000000015"
	legacyReverse.Revision.ID = "71000000-0000-4000-8000-000000000016"
	legacyReverse.Revision.ProposalID = legacyReverse.ID
	legacyReverse.RequestHash = requestHash
	if _, err := repository.CreateKnowledgeChangeProposal(ctx, legacyReverse); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("v1 reverse replay against v2 error = %v", err)
	}

	conflict := replay
	conflict.ID = "71000000-0000-4000-8000-000000000008"
	conflict.Revision.ID = "71000000-0000-4000-8000-000000000009"
	conflict.Revision.ProposalID = conflict.ID
	conflict.RequestHash = strings.Repeat("f", 64)
	if _, err := repository.CreateKnowledgeChangeProposal(ctx, conflict); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("idempotency conflict error = %v", err)
	}

	approval := domain.Approval{
		ID:         "71000000-0000-4000-8000-000000000010",
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

func TestRepositoryFilePatchProposalRequestHashCompatibility(t *testing.T) {
	pool, ctx := newChangeControlMigrationTestPool(t)
	workspaceID := foundation.ID("75000000-0000-4000-8000-000000000001")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,'File Proposal Hash Compatibility',$2,$2,$3,'test',1,$3,$3)`,
		string(workspaceID), "/tmp/file-proposal-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	baseHash := strings.Repeat("a", 64)
	content := "updated content"
	legacyHash := domain.ComputeRequestHash(workspaceID, "notes/a.md", baseHash, content, "verified evidence", "low", "restore file")
	proposal := domain.Proposal{
		ID:             "75000000-0000-4000-8000-000000000002",
		WorkspaceID:    workspaceID,
		Type:           domain.ProposalTypeFilePatch,
		RiskLevel:      domain.ProposalRiskLevelLow,
		TargetPath:     "notes/a.md",
		IdempotencyKey: "file-proposal-v1",
		RequestHash:    legacyHash,
		Status:         domain.StatusReady,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
		Revision: domain.Revision{
			ID:              "75000000-0000-4000-8000-000000000003",
			ProposalID:      "75000000-0000-4000-8000-000000000002",
			RevisionNo:      1,
			TargetPath:      "notes/a.md",
			BaseHash:        baseHash,
			Content:         content,
			EvidenceSummary: "verified evidence",
			Risk:            "low",
			RollbackPlan:    "restore file",
			ChangeHash:      domain.ComputeChangeHash("notes/a.md", baseHash, content),
			CreatedAt:       now,
		},
	}
	seedHistoricalFileProposalV1(t, ctx, pool, proposal)
	created, err := repository.CreateProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != proposal.ID || created.RequestHash != legacyHash || created.RiskLevel != domain.ProposalRiskLevelLow {
		t.Fatalf("historical v1 replay = %#v", created)
	}

	v2Hash, err := domain.ComputeRequestHashWithRiskLevel(
		workspaceID,
		"notes/a.md",
		baseHash,
		content,
		"verified evidence",
		domain.ProposalRiskLevelLow,
		"low",
		"restore file",
	)
	if err != nil {
		t.Fatal(err)
	}
	v2Replay := proposal
	v2Replay.ID = "75000000-0000-4000-8000-000000000004"
	v2Replay.Revision.ID = "75000000-0000-4000-8000-000000000005"
	v2Replay.Revision.ProposalID = v2Replay.ID
	v2Replay.RiskLevel = domain.ProposalRiskLevelLow
	v2Replay.RequestHash = v2Hash
	replayed, err := repository.CreateProposal(ctx, v2Replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != created.ID || replayed.RequestHash != legacyHash {
		t.Fatalf("v2 replayed proposal = %#v, want historical id=%s hash=%s", replayed, created.ID, legacyHash)
	}

	newV1 := proposal
	newV1.ID = "75000000-0000-4000-8000-000000000012"
	newV1.Revision.ID = "75000000-0000-4000-8000-000000000013"
	newV1.Revision.ProposalID = newV1.ID
	newV1.IdempotencyKey = "file-proposal-v1-new"
	if _, err := repository.CreateProposal(ctx, newV1); !hasCode(err, "PROPOSAL_INVALID") {
		t.Fatalf("new v1 file proposal error = %v", err)
	}

	v2Proposal := proposal
	v2Proposal.ID = "75000000-0000-4000-8000-000000000006"
	v2Proposal.Revision.ID = "75000000-0000-4000-8000-000000000007"
	v2Proposal.Revision.ProposalID = v2Proposal.ID
	v2Proposal.IdempotencyKey = "file-proposal-v2"
	v2Proposal.RiskLevel = domain.ProposalRiskLevelLow
	v2Proposal.RequestHash = v2Hash
	createdV2, err := repository.CreateProposal(ctx, v2Proposal)
	if err != nil {
		t.Fatal(err)
	}
	if createdV2.ID != v2Proposal.ID || createdV2.RequestHash != v2Hash || createdV2.RiskLevel != domain.ProposalRiskLevelLow {
		t.Fatalf("new v2 proposal = %#v", createdV2)
	}
	legacyReplay := v2Proposal
	legacyReplay.ID = "75000000-0000-4000-8000-000000000008"
	legacyReplay.Revision.ID = "75000000-0000-4000-8000-000000000009"
	legacyReplay.Revision.ProposalID = legacyReplay.ID
	legacyReplay.RequestHash = legacyHash
	if _, err := repository.CreateProposal(ctx, legacyReplay); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("v1 reverse replay against v2 error = %v", err)
	}

	highRiskHash, err := domain.ComputeRequestHashWithRiskLevel(
		workspaceID,
		"notes/a.md",
		baseHash,
		content,
		"verified evidence",
		domain.ProposalRiskLevelHigh,
		"low",
		"restore file",
	)
	if err != nil {
		t.Fatal(err)
	}
	riskConflict := legacyReplay
	riskConflict.ID = "75000000-0000-4000-8000-000000000010"
	riskConflict.Revision.ID = "75000000-0000-4000-8000-000000000011"
	riskConflict.Revision.ProposalID = riskConflict.ID
	riskConflict.RiskLevel = domain.ProposalRiskLevelHigh
	riskConflict.RequestHash = highRiskHash
	if _, err := repository.CreateProposal(ctx, riskConflict); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("risk-level idempotency conflict error = %v", err)
	}
}

func TestRepositoryProposalRiskLevelIsIndependentFromRevisionNarrative(t *testing.T) {
	pool, ctx := newChangeControlMigrationTestPool(t)
	workspaceID := foundation.ID("76000000-0000-4000-8000-000000000001")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,'Proposal Risk Source',$2,$2,$3,'test',1,$3,$3)`,
		string(workspaceID), "/tmp/proposal-risk-source-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	const narrative = "may change editorial structure"
	baseHash := strings.Repeat("a", 64)
	content := "updated editorial content"
	requestHash, err := domain.ComputeRequestHashWithRiskLevel(
		workspaceID,
		"notes/risk-source.md",
		baseHash,
		content,
		"verified evidence",
		domain.ProposalRiskLevelLow,
		narrative,
		"restore file",
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID:             "76000000-0000-4000-8000-000000000002",
		WorkspaceID:    workspaceID,
		Type:           domain.ProposalTypeFilePatch,
		RiskLevel:      domain.ProposalRiskLevelLow,
		TargetPath:     "notes/risk-source.md",
		IdempotencyKey: "proposal-risk-source",
		RequestHash:    requestHash,
		Status:         domain.StatusReady,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
		Revision: domain.Revision{
			ID:              "76000000-0000-4000-8000-000000000003",
			ProposalID:      "76000000-0000-4000-8000-000000000002",
			RevisionNo:      1,
			TargetPath:      "notes/risk-source.md",
			BaseHash:        baseHash,
			Content:         content,
			EvidenceSummary: "verified evidence",
			Risk:            narrative,
			RollbackPlan:    "restore file",
			ChangeHash:      domain.ComputeChangeHash("notes/risk-source.md", baseHash, content),
			CreatedAt:       now,
		},
	}
	created, err := repository.CreateProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if created.RiskLevel != domain.ProposalRiskLevelLow || created.Revision.Risk != narrative {
		t.Fatalf("created proposal risk binding = %#v", created)
	}
	loaded, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RiskLevel != domain.ProposalRiskLevelLow || loaded.Revision.Risk != narrative {
		t.Fatalf("loaded proposal risk binding = %#v", loaded)
	}
	items, hasMore, err := repository.ListProposals(ctx, domain.ProposalListQuery{
		WorkspaceID: workspaceID,
		RiskLevel:   domain.ProposalRiskLevelLow,
		Limit:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(items) != 1 || items[0].ProposalID != proposal.ID || items[0].RiskLevel != domain.ProposalRiskLevelLow || items[0].Risk != narrative {
		t.Fatalf("LOW proposal list = %#v hasMore=%v", items, hasMore)
	}
	replay := proposal
	replay.ID = "76000000-0000-4000-8000-000000000004"
	replay.Revision.ID = "76000000-0000-4000-8000-000000000005"
	replay.Revision.ProposalID = replay.ID
	replayed, err := repository.CreateProposal(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != proposal.ID || replayed.RiskLevel != domain.ProposalRiskLevelLow {
		t.Fatalf("replayed proposal risk binding = %#v", replayed)
	}
}

func seedHistoricalKnowledgeProposalV1(t *testing.T, ctx context.Context, pool *pgxpool.Pool, proposal domain.Proposal) {
	t.Helper()
	change := proposal.Revision.KnowledgeChange
	if change == nil {
		t.Fatal("historical knowledge proposal is missing change payload")
	}
	targetRefs, err := json.Marshal(change.TargetRefs)
	if err != nil {
		t.Fatal(err)
	}
	baseVersions, err := json.Marshal(change.BaseVersions)
	if err != nil {
		t.Fatal(err)
	}
	changeSet, err := json.Marshal(change.ChangeSet)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRefs, err := json.Marshal(change.EvidenceRefs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO change_control.proposal(
			id,workspace_id,proposal_type,idempotency_key,request_hash,risk_level,status,version,created_at,updated_at
		) VALUES($1,$2,'knowledge_change',$3,$4,$5,$6,$7,$8,$9)`,
		string(proposal.ID), string(proposal.WorkspaceID), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.RiskLevel), string(proposal.Status), proposal.Version, proposal.CreatedAt, proposal.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			target_refs,base_versions,change_set,evidence_refs,schema_version,created_at
		) VALUES($1,$2,$3,NULL,NULL,NULL,NULL,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		string(proposal.Revision.ID), string(proposal.ID), proposal.Revision.RevisionNo, proposal.Revision.Risk,
		proposal.Revision.RollbackPlan, proposal.Revision.ChangeHash, targetRefs, baseVersions, changeSet,
		evidenceRefs, change.SchemaVersion, proposal.Revision.CreatedAt); err != nil {
		t.Fatal(err)
	}
}

func seedHistoricalFileProposalV1(t *testing.T, ctx context.Context, pool *pgxpool.Pool, proposal domain.Proposal) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO change_control.proposal(
			id,workspace_id,proposal_type,idempotency_key,request_hash,risk_level,status,version,created_at,updated_at
		) VALUES($1,$2,'file_patch',$3,$4,$5,$6,$7,$8,$9)`,
		string(proposal.ID), string(proposal.WorkspaceID), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.RiskLevel), string(proposal.Status), proposal.Version, proposal.CreatedAt, proposal.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		string(proposal.Revision.ID), string(proposal.ID), proposal.Revision.RevisionNo, proposal.Revision.TargetPath,
		proposal.Revision.BaseHash, proposal.Revision.Content, proposal.Revision.EvidenceSummary, proposal.Revision.Risk,
		proposal.Revision.RollbackPlan, proposal.Revision.ChangeHash, proposal.Revision.CreatedAt); err != nil {
		t.Fatal(err)
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
	wantRiskLevel, err := domain.ParseProposalRiskLevel(want.RiskLevel)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.WorkspaceID != want.WorkspaceID || got.Type != domain.ProposalTypeKnowledgeChange || got.RiskLevel != wantRiskLevel || got.RequestHash != want.RequestHash ||
		got.TargetPath != "" || got.Revision.TargetPath != "" || got.Revision.BaseHash != "" ||
		got.Revision.Content != "" || got.Revision.EvidenceSummary != "" || got.Revision.KnowledgeChange == nil ||
		got.Revision.ChangeHash != want.Revision.ChangeHash {
		t.Fatalf("typed proposal = %#v, want binding %#v", got, want)
	}
	if hash, hashErr := domain.ComputeKnowledgeChangeHash(*got.Revision.KnowledgeChange, got.Revision.Risk, got.Revision.RollbackPlan); hashErr != nil || hash != got.Revision.ChangeHash {
		t.Fatalf("typed proposal hash = %s, err = %v", hash, hashErr)
	}
}
