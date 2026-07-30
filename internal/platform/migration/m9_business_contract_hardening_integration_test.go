//go:build integration

package migration

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM9BusinessContractHardeningMigrationSchemaAndEmptyDownUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 33); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 33); err != nil {
		t.Fatal(err)
	}
	assertM9MigrationVersion(t, ctx, pool, 33)
	assertM9BusinessContractHardeningShape(t, ctx, pool)

	if _, err := provider.DownTo(ctx, 29); err != nil {
		t.Fatalf("00030-00033 empty Down failed: %v", err)
	}
	assertM9MigrationVersion(t, ctx, pool, 29)
	assertM9BusinessContractHardeningAbsent(t, ctx, pool)
	if _, err := provider.UpTo(ctx, 33); err != nil {
		t.Fatalf("00030-00033 Up after Down failed: %v", err)
	}
	assertM9MigrationVersion(t, ctx, pool, 33)
	assertM9BusinessContractHardeningShape(t, ctx, pool)

	const (
		workspaceID = "b8000000-0000-4000-8000-000000000001"
		sourceID    = "b8010000-0000-4000-8000-000000000001"
		artifactID  = "b8020000-0000-4000-8000-000000000001"
		versionID   = "b8030000-0000-4000-8000-000000000001"
	)
	now := time.Date(2026, 7, 22, 6, 0, 0, 0, time.UTC)
	insertM9BusinessWorkspace(t, ctx, pool, workspaceID, "m9-source-down-guard", now)
	insertM9SourceFixture(t, ctx, pool, workspaceID, sourceID, artifactID, "source-down-guard.txt", strings.Repeat("1", 64), now)
	insertM9SourceVersionWithoutWorkspace(t, ctx, pool, sourceID, artifactID, versionID, "source-down-guard.txt", strings.Repeat("1", 64), now)
	assertM9SourceVersionWorkspace(t, ctx, pool, versionID, workspaceID)

	_, err := provider.DownTo(ctx, 29)
	assertPostgresCode(t, err, "55000")
	if !strings.Contains(err.Error(), "cannot downgrade M9 business contract hardening while Proposal or Source Version data exists") {
		t.Fatalf("00033 Source Version guarded Down returned unexpected error: %v", err)
	}
	assertM9MigrationVersion(t, ctx, pool, 33)
}

func TestM9BusinessContractIntermediateVersionsGuardedDown(t *testing.T) {
	for _, targetVersion := range []int64{30, 31, 32} {
		t.Run("version-"+strconv.FormatInt(targetVersion, 10), func(t *testing.T) {
			ctx := context.Background()
			pool, cleanup := newMigrationTestDatabase(t, ctx)
			defer cleanup()
			provider := migrationProvider(t, pool)
			if _, err := provider.UpTo(ctx, 29); err != nil {
				t.Fatal(err)
			}

			const (
				workspaceID = "b7000000-0000-4000-8000-000000000001"
				sourceID    = "b7010000-0000-4000-8000-000000000001"
				artifactID  = "b7020000-0000-4000-8000-000000000001"
				versionID   = "b7030000-0000-4000-8000-000000000001"
			)
			now := time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)
			insertM9BusinessWorkspace(t, ctx, pool, workspaceID, "m9-intermediate-down-guard", now)
			insertM9SourceFixture(t, ctx, pool, workspaceID, sourceID, artifactID, "intermediate-down-guard.txt", strings.Repeat("0", 64), now)
			insertM9SourceVersionWithoutWorkspace(t, ctx, pool, sourceID, artifactID, versionID, "intermediate-down-guard.txt", strings.Repeat("0", 64), now)

			if _, err := provider.UpTo(ctx, targetVersion); err != nil {
				t.Fatal(err)
			}
			_, err := provider.DownTo(ctx, 29)
			assertPostgresCode(t, err, "55000")
			if !strings.Contains(err.Error(), "cannot downgrade M9 business contract hardening while Proposal or Source Version data exists") {
				t.Fatalf("version %d guarded Down returned unexpected error: %v", targetVersion, err)
			}
			assertM9MigrationVersion(t, ctx, pool, targetVersion)
		})
	}
}

func TestM9BusinessContractHardeningMigrationBackfillConstraintsAndGuardedDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 29); err != nil {
		t.Fatal(err)
	}

	const (
		workspaceID        = "b9000000-0000-4000-8000-000000000001"
		otherWorkspaceID   = "b9000000-0000-4000-8000-000000000002"
		mediumProposalID   = "b9100000-0000-4000-8000-000000000001"
		unknownProposalID  = "b9100000-0000-4000-8000-000000000002"
		criticalID         = "b9100000-0000-4000-8000-000000000003"
		lowProposalID      = "b9100000-0000-4000-8000-000000000004"
		noRevisionID       = "b9100000-0000-4000-8000-000000000005"
		candidateID        = "b9500000-0000-4000-8000-000000000001"
		candidateProposal  = "b9100000-0000-4000-8000-000000000006"
		lowercaseID        = "b9100000-0000-4000-8000-000000000007"
		paddedID           = "b9100000-0000-4000-8000-000000000008"
		orphanKnowledgeID  = "b9100000-0000-4000-8000-000000000011"
		historicalSource   = "b9800000-0000-4000-8000-000000000001"
		historicalArtifact = "b9810000-0000-4000-8000-000000000001"
		historicalVersion  = "b9820000-0000-4000-8000-000000000001"
		otherSource        = "b9800000-0000-4000-8000-000000000002"
		otherArtifact      = "b9810000-0000-4000-8000-000000000002"
		otherVersion       = "b9820000-0000-4000-8000-000000000002"
	)
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	insertM9BusinessWorkspace(t, ctx, pool, workspaceID, "m9-hardening", now)
	insertM9BusinessWorkspace(t, ctx, pool, otherWorkspaceID, "m9-hardening-other", now)
	insertM9SourceFixture(t, ctx, pool, workspaceID, historicalSource, historicalArtifact, "historical-a.txt", strings.Repeat("6", 64), now)
	insertM9SourceVersionWithoutWorkspace(t, ctx, pool, historicalSource, historicalArtifact, historicalVersion, "historical-a.txt", strings.Repeat("6", 64), now)
	insertM9SourceFixture(t, ctx, pool, otherWorkspaceID, otherSource, otherArtifact, "historical-b.txt", strings.Repeat("7", 64), now)
	insertM9SourceVersionWithoutWorkspace(t, ctx, pool, otherSource, otherArtifact, otherVersion, "historical-b.txt", strings.Repeat("7", 64), now)
	insertM9LegacyFileProposal(t, ctx, pool, workspaceID, mediumProposalID, now,
		m9LegacyRevision{ID: "b9200000-0000-4000-8000-000000000001", No: 1, Risk: "low"},
		m9LegacyRevision{ID: "b9200000-0000-4000-8000-000000000002", No: 2, Risk: "MEDIUM"},
	)
	insertM9LegacyFileProposal(t, ctx, pool, workspaceID, unknownProposalID, now,
		m9LegacyRevision{ID: "b9200000-0000-4000-8000-000000000003", No: 1, Risk: "review production impact"},
	)
	insertM9LegacyFileProposal(t, ctx, pool, workspaceID, criticalID, now,
		m9LegacyRevision{ID: "b9200000-0000-4000-8000-000000000004", No: 1, Risk: "CRITICAL"},
	)
	insertM9LegacyFileProposal(t, ctx, pool, workspaceID, lowProposalID, now,
		m9LegacyRevision{ID: "b9200000-0000-4000-8000-000000000005", No: 1, Risk: "LOW"},
	)
	insertM9LegacyFileProposal(t, ctx, pool, workspaceID, noRevisionID, now)
	insertM9LegacyFileProposal(t, ctx, pool, workspaceID, lowercaseID, now,
		m9LegacyRevision{ID: "b9200000-0000-4000-8000-000000000007", No: 1, Risk: "high"},
	)
	insertM9LegacyFileProposal(t, ctx, pool, workspaceID, paddedID, now,
		m9LegacyRevision{ID: "b9200000-0000-4000-8000-000000000008", No: 1, Risk: " HIGH "},
	)
	insertM9LegacyCandidateProposal(t, ctx, pool, workspaceID, candidateID, candidateProposal, now, "MEDIUM")
	insertM9LegacyOrphanKnowledgeProposal(t, ctx, pool, workspaceID, orphanKnowledgeID, now, "MEDIUM")

	const (
		definitionID      = "b9300000-0000-4000-8000-000000000001"
		otherDefinitionID = "b9300000-0000-4000-8000-000000000002"
	)
	insertM9WorkflowDefinition(t, ctx, pool, definitionID, workspaceID, "m9-definition", now)
	insertM9WorkflowDefinition(t, ctx, pool, otherDefinitionID, otherWorkspaceID, "m9-other-definition", now)

	if _, err := provider.UpTo(ctx, 33); err != nil {
		t.Fatal(err)
	}
	assertM9MigrationVersion(t, ctx, pool, 33)
	assertM9BusinessContractHardeningShape(t, ctx, pool)
	assertM9SourceVersionWorkspace(t, ctx, pool, historicalVersion, workspaceID)
	assertM9SourceVersionWorkspace(t, ctx, pool, otherVersion, otherWorkspaceID)

	for proposalID, want := range map[string]string{
		mediumProposalID:  "MEDIUM",
		unknownProposalID: "HIGH",
		criticalID:        "CRITICAL",
		lowProposalID:     "LOW",
		noRevisionID:      "HIGH",
		candidateProposal: "HIGH",
		lowercaseID:       "HIGH",
		paddedID:          "HIGH",
		orphanKnowledgeID: "HIGH",
	} {
		var got string
		if err := pool.QueryRow(ctx, `SELECT risk_level FROM change_control.proposal WHERE id=$1`, proposalID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("proposal %s risk_level=%s want=%s", proposalID, got, want)
		}
	}

	_, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,idempotency_key,request_hash,status,version,created_at,updated_at
	) VALUES('b9100000-0000-4000-8000-000000000009',$1,'file_patch','m9-missing-risk',repeat('d',64),'ready_for_review',1,$2,$2)`,
		workspaceID, now)
	assertPostgresCode(t, err, "23502")

	_, err = pool.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,idempotency_key,request_hash,risk_level,status,version,created_at,updated_at
	) VALUES('b9100000-0000-4000-8000-000000000010',$1,'file_patch','m9-invalid-risk',repeat('e',64),'UNKNOWN','ready_for_review',1,$2,$2)`,
		workspaceID, now)
	assertPostgresCode(t, err, "23514")

	_, err = pool.Exec(ctx, `UPDATE change_control.proposal
		SET risk_level='LOW',status='rejected',version=version+1,updated_at=updated_at+interval '1 second'
		WHERE id=$1`, unknownProposalID)
	assertPostgresCode(t, err, "23514")

	const (
		oldWriterSourceID   = "b9800000-0000-4000-8000-000000000003"
		oldWriterArtifactID = "b9810000-0000-4000-8000-000000000003"
		oldWriterVersionID  = "b9820000-0000-4000-8000-000000000003"
		wrongSourceID       = "b9800000-0000-4000-8000-000000000004"
		wrongArtifactID     = "b9810000-0000-4000-8000-000000000004"
		foreignSourceID     = "b9800000-0000-4000-8000-000000000005"
		foreignArtifactID   = "b9810000-0000-4000-8000-000000000005"
	)
	insertM9SourceFixture(t, ctx, pool, workspaceID, oldWriterSourceID, oldWriterArtifactID, "old-writer.txt", strings.Repeat("8", 64), now)
	insertM9SourceVersionWithoutWorkspace(t, ctx, pool, oldWriterSourceID, oldWriterArtifactID, oldWriterVersionID, "old-writer.txt", strings.Repeat("8", 64), now)
	assertM9SourceVersionWorkspace(t, ctx, pool, oldWriterVersionID, workspaceID)

	insertM9SourceFixture(t, ctx, pool, workspaceID, wrongSourceID, wrongArtifactID, "wrong-workspace.txt", strings.Repeat("9", 64), now)
	_, err = pool.Exec(ctx, `INSERT INTO core.source_version(
		id,workspace_id,source_id,content_artifact_id,content_hash,byte_size,mime_type,
		original_content_location,security_status,captured_at
	) VALUES('b9820000-0000-4000-8000-000000000004',$1,$2,$3,$4,4,'text/plain','wrong-workspace.txt','pending',$5)`,
		otherWorkspaceID, wrongSourceID, wrongArtifactID, strings.Repeat("9", 64), now)
	assertPostgresCode(t, err, "23514")

	insertM9SourceFixture(t, ctx, pool, otherWorkspaceID, foreignSourceID, foreignArtifactID, "foreign-artifact.txt", strings.Repeat("a", 64), now)
	_, err = pool.Exec(ctx, `INSERT INTO core.source_version(
		id,workspace_id,source_id,content_artifact_id,content_hash,byte_size,mime_type,
		original_content_location,security_status,captured_at
	) VALUES('b9820000-0000-4000-8000-000000000005',$1,$2,$3,$4,4,'text/plain','foreign-artifact.txt','pending',$5)`,
		workspaceID, wrongSourceID, foreignArtifactID, strings.Repeat("a", 64), now)
	assertPostgresCode(t, err, "23514")

	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at
	) VALUES('b9400000-0000-4000-8000-000000000001',$1,$2,'pending','{}',1,$3,$3)`,
		workspaceID, definitionID, now); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at
	) VALUES('b9400000-0000-4000-8000-000000000002',$1,$2,'pending','{}',1,$3,$3)`,
		workspaceID, otherDefinitionID, now)
	assertPostgresCode(t, err, "23503")

	_, err = provider.DownTo(ctx, 29)
	assertPostgresCode(t, err, "55000")
	if !strings.Contains(err.Error(), "cannot downgrade M9 business contract hardening while Proposal or Source Version data exists") {
		t.Fatalf("00033 guarded Down returned unexpected error: %v", err)
	}
	assertM9MigrationVersion(t, ctx, pool, 33)

	// The current repository reads fields added after the M9-only migration assertions above.
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	repository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := repository.GetProposal(ctx, foundation.ID(orphanKnowledgeID))
	if err != nil {
		t.Fatalf("read upgraded orphan knowledge proposal: %v", err)
	}
	if orphan.Type != changecontroldomain.ProposalTypeKnowledgeChange || orphan.RiskLevel != changecontroldomain.ProposalRiskLevelHigh {
		t.Fatalf("upgraded orphan knowledge proposal = %#v", orphan)
	}
	items, hasMore, err := repository.ListProposals(ctx, changecontroldomain.ProposalListQuery{
		WorkspaceID: foundation.ID(workspaceID),
		Type:        changecontroldomain.ProposalTypeKnowledgeChange,
		RiskLevel:   changecontroldomain.ProposalRiskLevelHigh,
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("list upgraded orphan knowledge proposal: %v", err)
	}
	foundOrphan := false
	for _, item := range items {
		if item.ProposalID == foundation.ID(orphanKnowledgeID) {
			foundOrphan = item.RiskLevel == changecontroldomain.ProposalRiskLevelHigh
		}
	}
	if hasMore || !foundOrphan {
		t.Fatalf("upgraded knowledge proposal list hasMore=%v items=%#v", hasMore, items)
	}
}

func TestM9BusinessContractHardeningBackfillRejectsDirtySourceOwnershipAtomically(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 29); err != nil {
		t.Fatal(err)
	}

	const (
		workspaceID      = "bb000000-0000-4000-8000-000000000001"
		otherWorkspaceID = "bb000000-0000-4000-8000-000000000002"
		cleanSourceID    = "bb100000-0000-4000-8000-000000000001"
		cleanArtifactID  = "bb200000-0000-4000-8000-000000000001"
		cleanVersionID   = "bb300000-0000-4000-8000-000000000001"
		dirtySourceID    = "bb100000-0000-4000-8000-000000000002"
		dirtyArtifactID  = "bb200000-0000-4000-8000-000000000002"
		dirtyVersionID   = "bb300000-0000-4000-8000-000000000002"
		writerSourceID   = "bb100000-0000-4000-8000-000000000003"
		writerArtifactID = "bb200000-0000-4000-8000-000000000003"
		writerVersionID  = "bb300000-0000-4000-8000-000000000003"
	)
	now := time.Date(2026, 7, 22, 7, 30, 0, 0, time.UTC)
	insertM9BusinessWorkspace(t, ctx, pool, workspaceID, "m9-source-backfill", now)
	insertM9BusinessWorkspace(t, ctx, pool, otherWorkspaceID, "m9-source-backfill-other", now)
	insertM9SourceFixture(t, ctx, pool, workspaceID, cleanSourceID, cleanArtifactID, "clean-source.txt", strings.Repeat("b", 64), now)
	insertM9SourceVersionWithoutWorkspace(t, ctx, pool, cleanSourceID, cleanArtifactID, cleanVersionID, "clean-source.txt", strings.Repeat("b", 64), now)
	insertM9SourceFixture(t, ctx, pool, workspaceID, dirtySourceID, dirtyArtifactID, "dirty-source.txt", strings.Repeat("c", 64), now)
	insertM9SourceVersionWithoutWorkspace(t, ctx, pool, dirtySourceID, dirtyArtifactID, dirtyVersionID, "dirty-source.txt", strings.Repeat("c", 64), now)

	if _, err := provider.UpTo(ctx, 31); err != nil {
		t.Fatal(err)
	}
	insertM9SourceFixture(t, ctx, pool, workspaceID, writerSourceID, writerArtifactID, "expand-writer.txt", strings.Repeat("d", 64), now)
	insertM9SourceVersionWithoutWorkspace(t, ctx, pool, writerSourceID, writerArtifactID, writerVersionID, "expand-writer.txt", strings.Repeat("d", 64), now)
	assertM9SourceVersionWorkspace(t, ctx, pool, writerVersionID, workspaceID)
	setM9SourceVersionMutationTrigger(t, ctx, pool, false)
	if _, err := pool.Exec(ctx, `UPDATE core.source_version SET workspace_id=$1 WHERE id=$2`, otherWorkspaceID, dirtyVersionID); err != nil {
		t.Fatal(err)
	}
	setM9SourceVersionMutationTrigger(t, ctx, pool, true)

	_, err := provider.UpTo(ctx, 32)
	assertPostgresCode(t, err, "23514")
	if !strings.Contains(err.Error(), "source version workspace backfill found inconsistent ownership") {
		t.Fatalf("00032 dirty Source Version error=%v", err)
	}
	assertM9MigrationVersion(t, ctx, pool, 31)
	var cleanWorkspace, dirtyWorkspace sql.NullString
	if err := pool.QueryRow(ctx, `SELECT workspace_id::text FROM core.source_version WHERE id=$1`, cleanVersionID).Scan(&cleanWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT workspace_id::text FROM core.source_version WHERE id=$1`, dirtyVersionID).Scan(&dirtyWorkspace); err != nil {
		t.Fatal(err)
	}
	if cleanWorkspace.Valid || !dirtyWorkspace.Valid || dirtyWorkspace.String != otherWorkspaceID {
		t.Fatalf("failed 00032 committed partial backfill: clean=%v dirty=%v", cleanWorkspace, dirtyWorkspace)
	}

	setM9SourceVersionMutationTrigger(t, ctx, pool, false)
	if _, err := pool.Exec(ctx, `UPDATE core.source_version SET workspace_id=$1 WHERE id=$2`, workspaceID, dirtyVersionID); err != nil {
		t.Fatal(err)
	}
	setM9SourceVersionMutationTrigger(t, ctx, pool, true)
	if _, err := provider.UpTo(ctx, 33); err != nil {
		t.Fatalf("00032-00033 Up after repairing dirty Source Version failed: %v", err)
	}
	assertM9MigrationVersion(t, ctx, pool, 33)
	assertM9SourceVersionWorkspace(t, ctx, pool, cleanVersionID, workspaceID)
	assertM9SourceVersionWorkspace(t, ctx, pool, dirtyVersionID, workspaceID)
	assertM9BusinessContractHardeningShape(t, ctx, pool)
}

func TestM9BusinessContractHardeningContractRejectsDirtyWorkflowAtomically(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 32); err != nil {
		t.Fatal(err)
	}

	const (
		workspaceID       = "ba000000-0000-4000-8000-000000000001"
		otherWorkspaceID  = "ba000000-0000-4000-8000-000000000002"
		definitionID      = "ba100000-0000-4000-8000-000000000001"
		otherDefinitionID = "ba100000-0000-4000-8000-000000000002"
		dirtyRunID        = "ba200000-0000-4000-8000-000000000001"
	)
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	insertM9BusinessWorkspace(t, ctx, pool, workspaceID, "m9-dirty", now)
	insertM9BusinessWorkspace(t, ctx, pool, otherWorkspaceID, "m9-dirty-other", now)
	insertM9WorkflowDefinition(t, ctx, pool, definitionID, workspaceID, "m9-dirty-definition", now)
	insertM9WorkflowDefinition(t, ctx, pool, otherDefinitionID, otherWorkspaceID, "m9-dirty-other-definition", now)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at
	) VALUES($1,$2,$3,'pending','{}',1,$4,$4)`, dirtyRunID, workspaceID, otherDefinitionID, now); err != nil {
		t.Fatal(err)
	}

	_, err := provider.Up(ctx)
	assertPostgresCode(t, err, "23503")
	assertM9MigrationVersion(t, ctx, pool, 32)
	assertM9BusinessContractStillExpanded(t, ctx, pool)

	if _, err := pool.Exec(ctx, `DELETE FROM workflow.run WHERE id=$1`, dirtyRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 33); err != nil {
		t.Fatalf("00033 Up after removing dirty Workflow Run failed: %v", err)
	}
	assertM9MigrationVersion(t, ctx, pool, 33)
	assertM9BusinessContractHardeningShape(t, ctx, pool)
}

type m9LegacyRevision struct {
	ID   string
	No   int
	Risk string
}

func insertM9BusinessWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, name string, now time.Time) {
	t.Helper()
	root := "/tmp/zhixu-" + name
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, id, name, root, now); err != nil {
		t.Fatal(err)
	}
}

func insertM9SourceFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, sourceID, artifactID, location, contentHash string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.source(
		id,workspace_id,type,logical_name,original_location,created_at
	) VALUES($1,$2,'file',$3,$3,$4)`, sourceID, workspaceID, location, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.content_artifact(
		id,workspace_id,content_hash,byte_size,managed_location,created_at
	) VALUES($1,$2,$3,4,'.knowledge/sources/' || $3,$4)`, artifactID, workspaceID, contentHash, now); err != nil {
		t.Fatal(err)
	}
}

func insertM9SourceVersionWithoutWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sourceID, artifactID, versionID, location, contentHash string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.source_version(
		id,source_id,content_artifact_id,content_hash,byte_size,mime_type,
		original_content_location,security_status,captured_at
	) VALUES($1,$2,$3,$4,4,'text/plain',$5,'pending',$6)`, versionID, sourceID, artifactID, contentHash, location, now); err != nil {
		t.Fatal(err)
	}
}

func assertM9SourceVersionWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, versionID, wantWorkspaceID string) {
	t.Helper()
	var workspaceID string
	if err := pool.QueryRow(ctx, `SELECT workspace_id::text FROM core.source_version WHERE id=$1`, versionID).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	if workspaceID != wantWorkspaceID {
		t.Fatalf("source_version %s workspace_id=%s want=%s", versionID, workspaceID, wantWorkspaceID)
	}
}

func setM9SourceVersionMutationTrigger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, enabled bool) {
	t.Helper()
	statement := `ALTER TABLE core.source_version DISABLE TRIGGER source_version_reject_update_delete`
	if enabled {
		statement = `ALTER TABLE core.source_version ENABLE TRIGGER source_version_reject_update_delete`
	}
	if _, err := pool.Exec(ctx, statement); err != nil {
		t.Fatal(err)
	}
}

func insertM9LegacyFileProposal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, proposalID string, now time.Time, revisions ...m9LegacyRevision) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,idempotency_key,request_hash,status,version,created_at,updated_at
	) VALUES($1,$2,'file_patch',$3,repeat('a',64),'ready_for_review',1,$4,$4)`,
		proposalID, workspaceID, "m9-legacy-"+proposalID, now); err != nil {
		t.Fatal(err)
	}
	for _, revision := range revisions {
		if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
		) VALUES($1,$2,$3,$4,repeat('b',64),'content','evidence',$5,'rollback',repeat('c',64),$6)`,
			revision.ID, proposalID, revision.No, "docs/"+revision.ID+".md", revision.Risk, now.Add(time.Duration(revision.No)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
}

func insertM9LegacyCandidateProposal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, candidateID, proposalID string, now time.Time, risk string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,idempotency_key,request_hash,status,version,created_at,updated_at
	) VALUES($1,$2,'knowledge_change',$3,repeat('1',64),'ready_for_review',1,$4,$4)`,
		proposalID, workspaceID, "m9-candidate-"+proposalID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_refs,base_versions,change_set,evidence_refs,
		risk,rollback_plan,change_hash,schema_version,created_at
	) VALUES(
		'b9200000-0000-4000-8000-000000000006',$1,1,
		jsonb_build_array(jsonb_build_object('type','RELATION_CANDIDATE','id',$2::text,'fingerprint',repeat('2',64))),
		jsonb_build_array(jsonb_build_object('node_type','CLAIM','node_id','b9600000-0000-4000-8000-000000000001','version',1)),
		jsonb_build_object('operation','CREATE_RELATION','relation_type','SUPPORTS'),
		jsonb_build_array(jsonb_build_object('candidate_evidence_id','b9700000-0000-4000-8000-000000000001','semantic_hash',repeat('3',64))),
		$3,'remove relation',repeat('4',64),'knowledge-relation-change/v1',$4
	)`, proposalID, candidateID, risk, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO graph.semantic_link_candidate(
		id,workspace_id,source_node_type,source_node_id,source_node_version,
		target_node_type,target_node_id,target_node_version,relation_type,
		fingerprint_schema_version,fingerprint,status,confidence_score,reason,
		discovery_methods,evidence_semantic_hashes,generation,version,created_at,updated_at
	) VALUES(
		$1,$2,'CLAIM','b9600000-0000-4000-8000-000000000001',1,
		'CLAIM','b9600000-0000-4000-8000-000000000002',1,'SUPPORTS',
		'semantic-link-fingerprint/v1',repeat('5',64),'ACTIVE',0.9,'candidate fixture',
		'["SEMANTIC"]','["3333333333333333333333333333333333333333333333333333333333333333"]','{}',1,$3,$3
	)`, candidateID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE graph.semantic_link_candidate
		SET status='PROPOSAL_CREATED',current_proposal_id=$1,version=2,updated_at=$2
		WHERE id=$3`, proposalID, now.Add(2*time.Minute), candidateID); err != nil {
		t.Fatal(err)
	}
}

func insertM9LegacyOrphanKnowledgeProposal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, proposalID string, now time.Time, risk string) {
	t.Helper()
	change := changecontroldomain.KnowledgeChange{
		TargetRefs: []changecontroldomain.KnowledgeTargetRef{{
			Type:        changecontroldomain.KnowledgeTargetRefRelationCandidate,
			ID:          "b9500000-0000-4000-8000-000000000011",
			Fingerprint: strings.Repeat("7", 64),
		}},
		BaseVersions: []changecontroldomain.KnowledgeBaseVersion{
			{NodeType: knowledgedomain.NodeTypeClaim, NodeID: "b9600000-0000-4000-8000-000000000011", Version: 2},
			{NodeType: knowledgedomain.NodeTypeTopic, NodeID: "b9600000-0000-4000-8000-000000000012", Version: 3},
		},
		ChangeSet: changecontroldomain.KnowledgeChangeSet{
			Operation:    changecontroldomain.KnowledgeChangeOperationCreateRelation,
			Source:       knowledgedomain.NodeRef{Type: knowledgedomain.NodeTypeClaim, ID: "b9600000-0000-4000-8000-000000000011"},
			Target:       knowledgedomain.NodeRef{Type: knowledgedomain.NodeTypeTopic, ID: "b9600000-0000-4000-8000-000000000012"},
			RelationType: knowledgedomain.RelationBelongsTo,
		},
		EvidenceRefs: []changecontroldomain.KnowledgeEvidenceRef{{
			CandidateEvidenceID: "b9700000-0000-4000-8000-000000000011",
			SemanticHash:        strings.Repeat("8", 64),
		}},
		SchemaVersion: changecontroldomain.KnowledgeChangeSchemaVersion,
	}
	changeHash, err := changecontroldomain.ComputeKnowledgeChangeHash(change, risk, "remove relation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,idempotency_key,request_hash,status,version,created_at,updated_at
	) VALUES($1,$2,'knowledge_change',$3,repeat('6',64),'ready_for_review',1,$4,$4)`,
		proposalID, workspaceID, "m9-orphan-knowledge-"+proposalID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_refs,base_versions,change_set,evidence_refs,
		risk,rollback_plan,change_hash,schema_version,created_at
	) VALUES(
		'b9200000-0000-4000-8000-000000000011',$1,1,
		jsonb_build_array(jsonb_build_object(
			'type','RELATION_CANDIDATE','id','b9500000-0000-4000-8000-000000000011','fingerprint',repeat('7',64)
		)),
		jsonb_build_array(
			jsonb_build_object('node_type','CLAIM','node_id','b9600000-0000-4000-8000-000000000011','version',2),
			jsonb_build_object('node_type','TOPIC','node_id','b9600000-0000-4000-8000-000000000012','version',3)
		),
		jsonb_build_object(
			'operation','CREATE_RELATION',
			'source',jsonb_build_object('type','CLAIM','id','b9600000-0000-4000-8000-000000000011'),
			'target',jsonb_build_object('type','TOPIC','id','b9600000-0000-4000-8000-000000000012'),
			'relation_type','BELONGS_TO'
		),
		jsonb_build_array(jsonb_build_object(
			'candidate_evidence_id','b9700000-0000-4000-8000-000000000011','semantic_hash',repeat('8',64)
		)),
		$2,'remove relation',$3,'knowledge-relation-change/v1',$4
	)`, proposalID, risk, changeHash, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func insertM9WorkflowDefinition(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, workspaceID, key string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,$3,1,'{}',$4)`, id, workspaceID, key, now); err != nil {
		t.Fatal(err)
	}
}

func assertM9BusinessContractHardeningShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var nullable, sourceVersionNullable string
	var defaultExpression, sourceVersionDefault sql.NullString
	if err := pool.QueryRow(ctx, `SELECT is_nullable,column_default
		FROM information_schema.columns
		WHERE table_schema='change_control' AND table_name='proposal' AND column_name='risk_level'`).Scan(&nullable, &defaultExpression); err != nil {
		t.Fatal(err)
	}
	if nullable != "NO" || defaultExpression.Valid {
		t.Fatalf("risk_level nullable=%s default=%v", nullable, defaultExpression)
	}
	if err := pool.QueryRow(ctx, `SELECT is_nullable,column_default
		FROM information_schema.columns
		WHERE table_schema='core' AND table_name='source_version' AND column_name='workspace_id'`).Scan(&sourceVersionNullable, &sourceVersionDefault); err != nil {
		t.Fatal(err)
	}
	if sourceVersionNullable != "NO" || sourceVersionDefault.Valid {
		t.Fatalf("source_version.workspace_id nullable=%s default=%v", sourceVersionNullable, sourceVersionDefault)
	}

	var checkValidated, sourceVersionCheckValidated bool
	if err := pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint
		WHERE conrelid='change_control.proposal'::regclass AND conname='ck_proposal_risk_level'`).Scan(&checkValidated); err != nil {
		t.Fatal(err)
	}
	if !checkValidated {
		t.Fatal("ck_proposal_risk_level is not validated")
	}
	if err := pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint
		WHERE conrelid='core.source_version'::regclass AND conname='ck_source_version_workspace_required'`).Scan(&sourceVersionCheckValidated); err != nil {
		t.Fatal(err)
	}
	if !sourceVersionCheckValidated {
		t.Fatal("ck_source_version_workspace_required is not validated")
	}

	for _, expectation := range []struct {
		table      string
		constraint string
		definition string
	}{
		{table: "workflow.run", constraint: "fk_workflow_run_definition_workspace", definition: "FOREIGN KEY (definition_id, workspace_id) REFERENCES workflow.definition(id, workspace_id) ON DELETE RESTRICT"},
		{table: "core.source_version", constraint: "fk_source_version_source_workspace", definition: "FOREIGN KEY (source_id, workspace_id) REFERENCES core.source(id, workspace_id) ON DELETE RESTRICT"},
		{table: "core.source_version", constraint: "fk_source_version_artifact_workspace", definition: "FOREIGN KEY (workspace_id, content_artifact_id) REFERENCES core.content_artifact(workspace_id, id) ON DELETE RESTRICT"},
	} {
		var fkValidated bool
		var fkDefinition string
		if err := pool.QueryRow(ctx, `SELECT convalidated,pg_get_constraintdef(oid,false)
			FROM pg_constraint
			WHERE conrelid=$1::regclass AND conname=$2`, expectation.table, expectation.constraint).Scan(&fkValidated, &fkDefinition); err != nil {
			t.Fatal(err)
		}
		if !fkValidated || strings.Join(strings.Fields(fkDefinition), " ") != expectation.definition {
			t.Fatalf("composite FK %s validated=%t definition=%q want=%q", expectation.constraint, fkValidated, fkDefinition, expectation.definition)
		}
	}

	indexExpectations := []struct {
		schema string
		name   string
		unique bool
		shape  string
	}{
		{schema: "change_control", name: "idx_proposal_workspace_updated_id", shape: "(workspace_id, updated_at DESC, id DESC)"},
		{schema: "workflow", name: "idx_workflow_run_workspace_updated_id", shape: "(workspace_id, updated_at DESC, id DESC)"},
		{schema: "workflow", name: "idx_workflow_run_workspace_status_updated_id", shape: "(workspace_id, status, updated_at DESC, id DESC)"},
		{schema: "core", name: "idx_knowledge_topic_workspace_status_id", shape: "(workspace_id, status, id)"},
		{schema: "core", name: "idx_knowledge_claim_workspace_status_id", shape: "(workspace_id, status, id)"},
		{schema: "core", name: "idx_source_version_workspace_captured_id", shape: "(workspace_id, captured_at DESC, id DESC)"},
		{schema: "ingestion", name: "idx_ingestion_attempt_source_started_id", shape: "(source_version_id, started_at DESC, id DESC) INCLUDE (status, security_status, workflow_run_id)"},
		{schema: "workflow", name: "uq_workflow_definition_id_workspace", unique: true, shape: "(id, workspace_id)"},
	}
	for _, expectation := range indexExpectations {
		var valid, ready, unique, ownedByUniqueConstraint bool
		var definition string
		if err := pool.QueryRow(ctx, `SELECT index_state.indisvalid,index_state.indisready,index_state.indisunique,
			EXISTS (
				SELECT 1 FROM pg_constraint AS owner
				WHERE owner.conindid=index_relation.oid AND owner.contype IN ('p','u')
			),pg_get_indexdef(index_relation.oid)
			FROM pg_class AS index_relation
			JOIN pg_namespace AS index_namespace ON index_namespace.oid=index_relation.relnamespace
			JOIN pg_index AS index_state ON index_state.indexrelid=index_relation.oid
			WHERE index_namespace.nspname=$1 AND index_relation.relname=$2`, expectation.schema, expectation.name).
			Scan(&valid, &ready, &unique, &ownedByUniqueConstraint, &definition); err != nil {
			t.Fatal(err)
		}
		if !valid || !ready || unique != expectation.unique || ownedByUniqueConstraint || !strings.Contains(definition, expectation.shape) {
			t.Fatalf("index %s.%s valid=%t ready=%t unique=%t owned_by_unique_constraint=%t definition=%q",
				expectation.schema, expectation.name, valid, ready, unique, ownedByUniqueConstraint, definition)
		}
	}

	var triggers, immutableFunction, sourceBindTrigger, sourceBindFunction, schemaMeta, uniqueConstraint int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT trigger_name) FROM information_schema.triggers
		WHERE event_object_schema='change_control' AND event_object_table='proposal'
		  AND trigger_name='proposal_validate_transition'`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc
		WHERE pronamespace='change_control'::regnamespace
		  AND proname='validate_proposal_transition'
		  AND pg_get_functiondef(oid) LIKE '%NEW.risk_level <> OLD.risk_level%'`).Scan(&immutableFunction); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE tgrelid='core.source_version'::regclass
		  AND tgname='source_version_bind_workspace'
		  AND NOT tgisinternal`).Scan(&sourceBindTrigger); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc
		WHERE pronamespace='core'::regnamespace
		  AND proname='bind_source_version_workspace'
		  AND pg_get_functiondef(oid) LIKE '%NEW.workspace_id := source_workspace_id%'`).Scan(&sourceBindFunction); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='m9_business_contract_hardening' AND value='m9'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid='workflow.definition'::regclass
		  AND conname='uq_workflow_definition_id_workspace'`).Scan(&uniqueConstraint); err != nil {
		t.Fatal(err)
	}
	if triggers != 1 || immutableFunction != 1 || sourceBindTrigger != 1 || sourceBindFunction != 1 || schemaMeta != 1 || uniqueConstraint != 0 {
		t.Fatalf("contract triggers=%d immutable_function=%d source_bind_trigger=%d source_bind_function=%d schema_meta=%d unique_constraint=%d",
			triggers, immutableFunction, sourceBindTrigger, sourceBindFunction, schemaMeta, uniqueConstraint)
	}
}

func assertM9BusinessContractStillExpanded(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var nullable string
	var defaultExpression sql.NullString
	if err := pool.QueryRow(ctx, `SELECT is_nullable,column_default
		FROM information_schema.columns
		WHERE table_schema='change_control' AND table_name='proposal' AND column_name='risk_level'`).Scan(&nullable, &defaultExpression); err != nil {
		t.Fatal(err)
	}
	var fk, schemaMeta, immutableFunction int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid='workflow.run'::regclass AND conname='fk_workflow_run_definition_workspace'`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='m9_business_contract_hardening'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc
		WHERE pronamespace='change_control'::regnamespace
		  AND proname='validate_proposal_transition'
		  AND pg_get_functiondef(oid) LIKE '%NEW.risk_level <> OLD.risk_level%'`).Scan(&immutableFunction); err != nil {
		t.Fatal(err)
	}
	if nullable != "YES" || !defaultExpression.Valid || !strings.Contains(defaultExpression.String, "HIGH") || fk != 0 || schemaMeta != 0 || immutableFunction != 0 {
		t.Fatalf("failed contract rollback nullable=%s default=%v fk=%d schema_meta=%d immutable_function=%d",
			nullable, defaultExpression, fk, schemaMeta, immutableFunction)
	}
}

func assertM9BusinessContractHardeningAbsent(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var columns, constraints, indexes, riskFunctionReferences, sourceBindFunctions, schemaMeta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE (table_schema='change_control' AND table_name='proposal' AND column_name='risk_level')
		   OR (table_schema='core' AND table_name='source_version' AND column_name='workspace_id')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conname IN (
			'ck_proposal_risk_level','ck_source_version_workspace_required',
			'fk_workflow_run_definition_workspace','fk_source_version_source_workspace',
			'fk_source_version_artifact_workspace','uq_workflow_definition_id_workspace'
		)`).Scan(&constraints); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE indexname IN (
			'idx_proposal_workspace_updated_id','idx_workflow_run_workspace_updated_id',
			'idx_workflow_run_workspace_status_updated_id','idx_knowledge_topic_workspace_status_id',
			'idx_knowledge_claim_workspace_status_id','idx_source_version_workspace_captured_id',
			'idx_ingestion_attempt_source_started_id','uq_workflow_definition_id_workspace'
		)`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc
		WHERE pronamespace='change_control'::regnamespace
		  AND proname='validate_proposal_transition'
		  AND pg_get_functiondef(oid) LIKE '%risk_level%'`).Scan(&riskFunctionReferences); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc
		WHERE pronamespace='core'::regnamespace
		  AND proname='bind_source_version_workspace'`).Scan(&sourceBindFunctions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='m9_business_contract_hardening'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if columns != 0 || constraints != 0 || indexes != 0 || riskFunctionReferences != 0 || sourceBindFunctions != 0 || schemaMeta != 0 {
		t.Fatalf("00030-00033 Down columns=%d constraints=%d indexes=%d risk_function_references=%d source_bind_functions=%d schema_meta=%d",
			columns, constraints, indexes, riskFunctionReferences, sourceBindFunctions, schemaMeta)
	}
}

func assertM9MigrationVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int64) {
	t.Helper()
	provider := migrationProvider(t, pool)
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != want {
		t.Fatalf("migration version=%d want=%d", version, want)
	}
}
