//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestM8MemoryStableOwnerMigrationBackfillsReceiptsWithoutSyntheticEvents(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 47); err != nil {
		t.Fatalf("migrate through 00047: %v", err)
	}

	workspaceID := "78000000-0000-4000-8000-000000000001"
	memoryID := "78000000-0000-4000-8000-000000000002"
	credentialID := "78000000-0000-4000-8000-000000000003"
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'m8-memory-owner','/tmp/m8-memory-owner','/tmp/m8-memory-owner',$2,'test',1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.memory(
		id,workspace_id,owner_principal_kind,owner_principal_id,memory_type,content,source_type,source_ref,
		status,version,created_at,updated_at
	) VALUES($1,$2,'SESSION',$3,'PREFERENCE','{"mode":"concise"}','USER','user:settings','CANDIDATE',1,$4,$4)`,
		memoryID, workspaceID, credentialID, now); err != nil {
		t.Fatal(err)
	}
	response := `{
		"id":"` + memoryID + `",
		"workspace_id":"` + workspaceID + `",
		"owner":{"kind":"SESSION","id":"` + credentialID + `"},
		"type":"PREFERENCE",
		"content":{"mode":"concise"},
		"source":{"type":"USER","ref":"user:settings"},
		"status":"CANDIDATE",
		"version":1,
		"created_at":"` + now.Format(time.RFC3339Nano) + `",
		"updated_at":"` + now.Format(time.RFC3339Nano) + `"
	}`
	if _, err := pool.Exec(ctx, `INSERT INTO learning.memory_command(
		workspace_id,idempotency_key,owner_principal_kind,owner_principal_id,request_hash,command_type,
		memory_id,expected_version,memory_version,response,created_at
	) VALUES($1,'stable-owner-backfill','SESSION',$2,repeat('a',64),'CREATE_CANDIDATE',$3,0,1,$4::jsonb,$5)`,
		workspaceID, credentialID, memoryID, response, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.memory_audit(
		workspace_id,memory_id,owner_principal_kind,owner_principal_id,actor_principal_kind,actor_principal_id,
		action,from_status,to_status,memory_version,idempotency_key,request_hash,occurred_at
	) VALUES($1,$2,'SESSION',$3,'SESSION',$3,'CANDIDATE_CREATED',NULL,'CANDIDATE',1,
		'stable-owner-backfill',repeat('a',64),$4)`, workspaceID, memoryID, credentialID, now); err != nil {
		t.Fatal(err)
	}
	beforeEvents := m8LearningEventCount(t, ctx, pool, workspaceID)

	if _, err := provider.UpTo(ctx, 48); err != nil {
		t.Fatalf("00048 up: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 48)
	const stableOwnerID = "00000000-0000-5000-8000-000000000001"
	var memoryKind, memoryOwner, commandKind, commandOwner, responseKind, responseOwner, auditKind, auditOwner string
	if err := pool.QueryRow(ctx, `SELECT owner_principal_kind,owner_principal_id::text
		FROM learning.memory WHERE workspace_id=$1 AND id=$2`, workspaceID, memoryID).Scan(&memoryKind, &memoryOwner); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT owner_principal_kind,owner_principal_id::text,
		response #>> '{owner,kind}',response #>> '{owner,id}'
		FROM learning.memory_command WHERE workspace_id=$1 AND idempotency_key='stable-owner-backfill'`, workspaceID).
		Scan(&commandKind, &commandOwner, &responseKind, &responseOwner); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT owner_principal_kind,owner_principal_id::text
		FROM learning.memory_audit WHERE workspace_id=$1 AND memory_id=$2`, workspaceID, memoryID).Scan(&auditKind, &auditOwner); err != nil {
		t.Fatal(err)
	}
	for label, value := range map[string]string{
		"memory kind": memoryKind, "command kind": commandKind, "response kind": responseKind, "audit kind": auditKind,
	} {
		if value != "USER" {
			t.Fatalf("%s=%q", label, value)
		}
	}
	for label, value := range map[string]string{
		"memory owner": memoryOwner, "command owner": commandOwner, "response owner": responseOwner, "audit owner": auditOwner,
	} {
		if value != stableOwnerID {
			t.Fatalf("%s=%q", label, value)
		}
	}
	_, err := pool.Exec(ctx, `UPDATE learning.memory
		SET source_type='AGENT',source_ref='agent:tampered',version=version+1,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, workspaceID, memoryID, now.Add(time.Minute))
	assertPostgresCode(t, err, "23514")
	if afterEvents := m8LearningEventCount(t, ctx, pool, workspaceID); afterEvents != beforeEvents {
		t.Fatalf("owner backfill emitted synthetic SSE events: before=%d after=%d", beforeEvents, afterEvents)
	}
	if _, err := provider.DownTo(ctx, 47); err == nil || !strings.Contains(err.Error(), "stable memory ownership exists") {
		t.Fatalf("00048 down did not guard stable ownership: %v", err)
	}
}

func TestM8MemoryStableOwnerMigrationSupportsEmptyDownUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 48); err != nil {
		t.Fatalf("00048 up: %v", err)
	}
	if _, err := provider.DownTo(ctx, 47); err != nil {
		t.Fatalf("00048 empty down: %v", err)
	}
	if _, err := provider.UpTo(ctx, 48); err != nil {
		t.Fatalf("00048 re-up: %v", err)
	}
}
