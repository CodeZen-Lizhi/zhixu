//go:build integration

package migration

import (
	"context"
	"testing"
	"time"
)

func TestWorkspaceRootGrantMigrationPreservesLegacyAndGuardsIdentity(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 66); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	const (
		legacyActiveID = "67000000-0000-4000-8000-000000000001"
		legacyTestID   = "67000000-0000-4000-8000-000000000002"
		parentID       = "67000000-0000-4000-8000-000000000003"
		childID        = "67000000-0000-4000-8000-000000000004"
		duplicateID    = "67000000-0000-4000-8000-000000000005"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
VALUES
($1,'legacy-active','/workspace','/workspace',$3,'active',4,$3,$3),
($2,'legacy-test','/tmp/legacy-test','/tmp/legacy-test',$3,'test',7,$3,$3)`,
		legacyActiveID, legacyTestID, now); err != nil {
		t.Fatal(err)
	}
	if err := provider.UpTo(ctx, 67); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		id              string
		wantVersion     int64
		wantLegacyState string
	}{
		{id: legacyActiveID, wantVersion: 5, wantLegacyState: "active"},
		{id: legacyTestID, wantVersion: 8, wantLegacyState: "test"},
	} {
		var status, availability, reason, fingerprint string
		var bindingVersion, version int64
		if err := pool.QueryRow(ctx, `SELECT workspace.status,workspace.availability,
COALESCE(workspace.availability_reason,''),COALESCE(workspace.root_fingerprint,''),
workspace.binding_version,workspace.version
FROM core.workspace AS workspace WHERE workspace.id=$1`, fixture.id).Scan(
			&status, &availability, &reason, &fingerprint, &bindingVersion, &version,
		); err != nil {
			t.Fatal(err)
		}
		if status != "inactive" || availability != "migration_required" ||
			reason != "WORKSPACE_BINDING_LEGACY_UNVERIFIED" || fingerprint != "" ||
			bindingVersion != 0 || version != fixture.wantVersion {
			t.Fatalf("legacy row %s state=%s/%s/%s/%q/%d/%d", fixture.id,
				status, availability, reason, fingerprint, bindingVersion, version)
		}
		var snapshotStatus string
		if err := pool.QueryRow(ctx, `SELECT status FROM ops.workspace_registry_legacy_snapshot
WHERE workspace_id=$1`, fixture.id).Scan(&snapshotStatus); err != nil {
			t.Fatal(err)
		}
		if snapshotStatus != fixture.wantLegacyState {
			t.Fatalf("legacy snapshot %s status=%q want=%q", fixture.id, snapshotStatus, fixture.wantLegacyState)
		}
	}

	insertVerified := func(id, root, fingerprint string) error {
		_, err := pool.Exec(ctx, `INSERT INTO core.workspace(
id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
VALUES($1,'verified',$2,$3,1,$2,$4,'inactive','available',NULL,$4,1,$4,$4)`,
			id, root, fingerprint, now)
		return err
	}
	parentFingerprint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	childFingerprint := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := insertVerified(parentID, "/tmp/verified", parentFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := insertVerified(childID, "/tmp/verified/child", childFingerprint); err != nil {
		t.Fatalf("parent and child Registry identities should coexist: %v", err)
	}
	assertPostgresCode(t, insertVerified(duplicateID, "/tmp/verified-moved", parentFingerprint), "23505")
	_, err := pool.Exec(ctx, `UPDATE core.workspace
SET root_path='/tmp/rebound',git_repository_path='/tmp/rebound',version=version+1,updated_at=now()
WHERE id=$1`, parentID)
	assertPostgresCode(t, err, "55000")
}

func TestWorkspaceRootGrantMigrationSharesModelSettingsMutationGate(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 67); err != nil {
		t.Fatal(err)
	}
	const rolloutID = "67000000-0000-4000-8000-000000000010"
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET rollout_id=$1,target_revision=desired_revision,previous_active_revision=active_revision,
phase='validating',lease_expires_at=clock_timestamp()+interval '1 minute',
version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`, rolloutID); err != nil {
		t.Fatal(err)
	}
	var ownerKind, ownerID string
	if err := pool.QueryRow(ctx, `SELECT owner_kind,owner_id::text
FROM ops.runtime_mutation_gate WHERE singleton=true`).Scan(&ownerKind, &ownerID); err != nil {
		t.Fatal(err)
	}
	if ownerKind != "model_settings" || ownerID != rolloutID {
		t.Fatalf("model settings gate owner=%q/%q", ownerKind, ownerID)
	}
	_, err := pool.Exec(ctx, `UPDATE ops.runtime_mutation_gate
SET owner_kind='workspace_switch',owner_id='67000000-0000-4000-8000-000000000011',
lease_expires_at=clock_timestamp()+interval '1 minute',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`)
	assertPostgresCode(t, err, "55P03")
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET phase='failed',lease_expires_at=NULL,last_error_code='MODEL_SETTINGS_TEST_FAILURE',
version=version+1,updated_at=clock_timestamp()
WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	var released bool
	if err := pool.QueryRow(ctx, `SELECT owner_id IS NULL AND owner_kind IS NULL AND lease_expires_at IS NULL
FROM ops.runtime_mutation_gate WHERE singleton=true`).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("model settings terminal transition did not release runtime mutation gate")
	}
}
