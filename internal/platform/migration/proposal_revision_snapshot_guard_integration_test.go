//go:build integration

package migration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProposalRevisionSnapshotGuardMigrationRepairsRuntimeAmbiguity(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 89); err != nil {
		t.Fatal(err)
	}

	const (
		workspaceID = "99000000-0000-4000-8000-000000000001"
		proposalID  = "99000000-0000-4000-8000-000000000002"
		revisionID  = "99000000-0000-4000-8000-000000000003"
		baseContent = "# Snapshot base\n"
	)
	baseHash := fmt.Sprintf("%x", sha256.Sum256([]byte(baseContent)))
	now := time.Now().UTC().Truncate(time.Microsecond)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO core.workspace(
    id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
) VALUES($1,'Snapshot Guard','/tmp/snapshot-guard','/tmp/snapshot-guard',$2,'active',1,$2,$2)`,
		workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO change_control.proposal(
    id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,
    created_at,updated_at,current_revision_id
) VALUES($1,$2,'file_patch','LOW','snapshot-guard',repeat('a',64),'ready_for_review',1,$4,$4,$3)`,
		proposalID, workspaceID, revisionID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO change_control.proposal_revision(
    id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,
    risk,rollback_plan,change_hash,created_at
) VALUES($1,$2,1,'docs/snapshot.md','REPLACE',$3,'replacement','snapshot evidence',
    'low','restore the base',repeat('b',64),$4)`, revisionID, proposalID, baseHash, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	_, err = pool.Exec(ctx, `
INSERT INTO change_control.proposal_revision_base_snapshot(
    proposal_id,revision_id,base_hash,content,base_byte_size,schema_version,created_at
) VALUES($1,$2,$3,$4,octet_length($4),'proposal-base-snapshot/v1',$5)`,
		proposalID, revisionID, baseHash, baseContent, now)
	assertPostgresCode(t, err, "42702")

	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO change_control.proposal_revision_base_snapshot(
    proposal_id,revision_id,base_hash,content,base_byte_size,schema_version,created_at
) VALUES($1,$2,$3,$4,octet_length($4),'proposal-base-snapshot/v1',$5)`,
		proposalID, revisionID, baseHash, baseContent, now); err != nil {
		t.Fatal(err)
	}

	_, err = pool.Exec(ctx, `
INSERT INTO change_control.proposal_revision_base_snapshot(
    proposal_id,revision_id,base_hash,content,base_byte_size,schema_version,created_at
) VALUES($1,$2,$3,'wrong',5,'proposal-base-snapshot/v1',$4)`,
		proposalID, revisionID, baseHash, now)
	assertPostgresCode(t, err, "23514")

	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	var functionDefinition string
	if err := pool.QueryRow(ctx, `SELECT pg_get_functiondef('change_control.validate_revision_base_snapshot()'::regprocedure)`).Scan(&functionDefinition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(functionDefinition, "proposal_type_value") {
		t.Fatal("00090 Down restored the ambiguous proposal_type variable")
	}
}
