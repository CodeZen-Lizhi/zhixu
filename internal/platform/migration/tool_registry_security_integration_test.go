//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestToolRegistrySecurityMigrationUpRepeat(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	assertToolRegistryMigrationShape(t, ctx, pool)

	provider := migrationProvider(t, pool)
	if err := provider.Up(ctx); err != nil {
		t.Fatalf("00019 repeat Up failed: %v", err)
	}
	assertToolRegistryMigrationShape(t, ctx, pool)
}

func TestToolRegistrySecurityMigrationBindingsLifecycle(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateToolRegistryTestDatabase(t, ctx, pool)
	fixture := insertToolCallWorkflowFixture(t, ctx, pool)

	started := validResolvedToolCall(fixture.primary, 1, "SearchKnowledge", "READ_LOCAL")
	if err := insertToolCall(ctx, pool, started); err != nil {
		t.Fatal(err)
	}

	activeConflict := validResolvedToolCall(fixture.primary, 2, "ReadSource", "READ_LOCAL")
	assertPostgresCode(t, insertToolCall(ctx, pool, activeConflict), "23505")

	_, err := pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='SUCCEEDED',request_hash=repeat('f',64),response_hash=repeat('b',64),response_bytes=12,
		response_summary=$2::jsonb,completed_at=$3,duration_ms=10,version=2 WHERE id=$1`,
		started.id, `{"count":1}`, started.startedAt.Add(10*time.Millisecond))
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='SUCCEEDED',response_hash=repeat('b',64),response_bytes=12,response_summary=$2::jsonb,
		completed_at=$3,duration_ms=10,version=3 WHERE id=$1`,
		started.id, `{"count":1}`, started.startedAt.Add(10*time.Millisecond))
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='REFUSED',error_code='POLICY_DENIED',completed_at=$2,duration_ms=10,version=2 WHERE id=$1`,
		started.id, started.startedAt.Add(10*time.Millisecond))
	assertPostgresCode(t, err, "55000")
	if _, err := pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='SUCCEEDED',response_hash=repeat('b',64),response_bytes=12,response_summary=$2::jsonb,
		completed_at=$3,duration_ms=10,version=2 WHERE id=$1`,
		started.id, `{"count":1}`, started.startedAt.Add(10*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE workflow.tool_call SET duration_ms=11,version=3 WHERE id=$1`, started.id)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM workflow.tool_call WHERE id=$1`, started.id)
	assertPostgresCode(t, err, "55000")
	duplicateCallNo := validResolvedRefusedToolCall(fixture.primary, 1, "ReadSource", "READ_LOCAL")
	duplicateCallNo.id = "dd000000-0000-4000-8000-000000000001"
	assertPostgresCode(t, insertToolCall(ctx, pool, duplicateCallNo), "23505")

	unknown := validResolvedToolCall(fixture.primary, 2, "FetchWebPage", "READ_EXTERNAL")
	if err := insertToolCall(ctx, pool, unknown); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='UNKNOWN',error_code='TOOL_OUTCOME_UNKNOWN',result_ref='receipt:unverified',
		completed_at=$2,duration_ms=20,version=2 WHERE id=$1`, unknown.id, unknown.startedAt.Add(20*time.Millisecond))
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='UNKNOWN',error_code='TOOL_OUTCOME_UNKNOWN',side_effect_type='web_fetch',side_effect_id=repeat('f',64),
		completed_at=$2,duration_ms=20,version=2 WHERE id=$1`, unknown.id, unknown.startedAt.Add(20*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	failed := validResolvedToolCall(fixture.primary, 3, "ReadDocument", "READ_LOCAL")
	if err := insertToolCall(ctx, pool, failed); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='FAILED',completed_at=$2,duration_ms=30,version=2 WHERE id=$1`, failed.id, failed.startedAt.Add(30*time.Millisecond))
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='FAILED',error_code='TOOL_DEPENDENCY_UNAVAILABLE',retryable=true,
		completed_at=$2,duration_ms=30,version=2 WHERE id=$1`, failed.id, failed.startedAt.Add(30*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	resultRef := validResolvedToolCall(fixture.primary, 4, "CalculateDiff", "")
	if err := insertToolCall(ctx, pool, resultRef); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='SUCCEEDED',result_ref='diff:'||repeat('a',64)||':'||repeat('b',64),completed_at=$2,duration_ms=5,version=2 WHERE id=$1`,
		resultRef.id, resultRef.startedAt.Add(5*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	refused := validUnresolvedRefusedToolCall(fixture.primary, 5)
	if err := insertToolCall(ctx, pool, refused); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE workflow.tool_call SET error_code='OTHER',version=2 WHERE id=$1`, refused.id)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `DELETE FROM workflow.tool_call WHERE id=$1`, refused.id)
	assertPostgresCode(t, err, "55000")

	responseBoundary := validResolvedToolCall(fixture.primary, 6, "ReadSource", "READ_LOCAL")
	if err := insertToolCall(ctx, pool, responseBoundary); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='SUCCEEDED',response_hash=repeat('c',64),response_bytes=2,response_summary=$2::jsonb,
		completed_at=$3,duration_ms=1,version=2 WHERE id=$1`,
		responseBoundary.id, `[1]`, responseBoundary.startedAt.Add(time.Millisecond))
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='SUCCEEDED',response_hash=repeat('c',64),response_bytes=16385,response_summary=$2::jsonb,
		completed_at=$3,duration_ms=1,version=2 WHERE id=$1`,
		responseBoundary.id, `{"value":"`+strings.Repeat("x", 16384)+`"}`, responseBoundary.startedAt.Add(time.Millisecond))
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='FAILED',error_code='TOOL_FAILED',completed_at=$2,duration_ms=1,version=2 WHERE id=$1`,
		responseBoundary.id, responseBoundary.startedAt.Add(-time.Millisecond))
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE workflow.tool_call SET
		status='FAILED',error_code='TOOL_FAILED',completed_at=$2,duration_ms=1,version=2 WHERE id=$1`,
		responseBoundary.id, responseBoundary.startedAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	invalidInitial := validResolvedToolCall(fixture.secondary, 1, "SearchKnowledge", "READ_LOCAL")
	invalidInitial.status = "SUCCEEDED"
	invalidInitial.completedAt = timePointer(invalidInitial.startedAt.Add(time.Millisecond))
	invalidInitial.responseHash = stringPointer(strings.Repeat("b", 64))
	invalidInitial.responseBytes = 1
	invalidInitial.responseSummary = []byte(`{"count":1}`)
	assertPostgresCode(t, insertToolCall(ctx, pool, invalidInitial), "23514")

	invalidVersion := validResolvedToolCall(fixture.secondary, 1, "SearchKnowledge", "READ_LOCAL")
	invalidVersion.version = 2
	assertPostgresCode(t, insertToolCall(ctx, pool, invalidVersion), "23514")
}

func TestToolRegistrySecurityMigrationConstraints(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateToolRegistryTestDatabase(t, ctx, pool)
	fixture := insertToolCallWorkflowFixture(t, ctx, pool)

	for index, value := range []string{
		"READ_LOCAL", "READ_EXTERNAL", "WRITE_PROPOSAL", "WRITE_KNOWLEDGE",
		"GIT_WRITE", "INDEX_MAINTENANCE", "EVALUATION_RUN",
	} {
		row := validResolvedRefusedToolCall(fixture.primary, int16(index+1), "SearchKnowledge", value)
		if err := insertToolCall(ctx, pool, row); err != nil {
			t.Fatalf("canonical capability %s rejected: %v", value, err)
		}
	}
	calculateDiff := validResolvedRefusedToolCall(fixture.primary, 8, "CalculateDiff", "")
	if err := insertToolCall(ctx, pool, calculateDiff); err != nil {
		t.Fatalf("CalculateDiff without capability rejected: %v", err)
	}
	admin := validResolvedRefusedToolCall(fixture.primary, 9, "RebuildIndex", "ADMIN_MAINTENANCE")
	assertPostgresCode(t, insertToolCall(ctx, pool, admin), "23514")
	missingCapability := validResolvedRefusedToolCall(fixture.primary, 9, "SearchKnowledge", "")
	assertPostgresCode(t, insertToolCall(ctx, pool, missingCapability), "23514")

	crossWorkspace := validResolvedRefusedToolCall(fixture.primary, 9, "SearchKnowledge", "READ_LOCAL")
	crossWorkspace.workspaceID = fixture.otherWorkspaceID
	assertPostgresCode(t, insertToolCall(ctx, pool, crossWorkspace), "23503")
	crossRun := validResolvedRefusedToolCall(fixture.primary, 9, "SearchKnowledge", "READ_LOCAL")
	crossRun.workflowRunID = fixture.other.workflowRunID
	assertPostgresCode(t, insertToolCall(ctx, pool, crossRun), "23503")
	crossAttempt := validResolvedRefusedToolCall(fixture.other, 11, "SearchKnowledge", "READ_LOCAL")
	crossAttempt.nodeAttemptID = fixture.primary.nodeAttemptID
	assertPostgresCode(t, insertToolCall(ctx, pool, crossAttempt), "23503")

	invalidSummary := validUnresolvedRefusedToolCall(fixture.primary, 9)
	invalidSummary.requestSummary = []byte(`[1]`)
	assertPostgresCode(t, insertToolCall(ctx, pool, invalidSummary), "23514")
	oversizedSummary := validUnresolvedRefusedToolCall(fixture.primary, 9)
	oversizedSummary.requestSummary = []byte(`{"value":"` + strings.Repeat("x", 16384) + `"}`)
	assertPostgresCode(t, insertToolCall(ctx, pool, oversizedSummary), "23514")
	invalidBytes := validUnresolvedRefusedToolCall(fixture.primary, 9)
	invalidBytes.requestBytes = 0
	assertPostgresCode(t, insertToolCall(ctx, pool, invalidBytes), "23514")
	oversizedBytes := validUnresolvedRefusedToolCall(fixture.primary, 9)
	oversizedBytes.requestBytes = 16777217
	assertPostgresCode(t, insertToolCall(ctx, pool, oversizedBytes), "23514")
	mismatchedSideEffect := validResolvedRefusedToolCall(fixture.primary, 9, "SearchKnowledge", "READ_LOCAL")
	mismatchedSideEffect.sideEffectType = stringPointer("writeback")
	assertPostgresCode(t, insertToolCall(ctx, pool, mismatchedSideEffect), "23514")
	unsafeResultRef := validResolvedToolCall(fixture.primary, 9, "ReadSource", "READ_LOCAL")
	if err := insertToolCall(ctx, pool, unsafeResultRef); err != nil {
		t.Fatal(err)
	}
	_, unsafeErr := pool.Exec(ctx, `UPDATE workflow.tool_call SET status='SUCCEEDED',result_ref='/Users/private/source.md',completed_at=$2,duration_ms=1,version=2 WHERE id=$1`, unsafeResultRef.id, unsafeResultRef.startedAt.Add(time.Millisecond))
	assertPostgresCode(t, unsafeErr, "23514")
	if _, err := pool.Exec(ctx, `UPDATE workflow.tool_call SET status='FAILED',error_code='TOOL_OUTPUT_INVALID',completed_at=$2,duration_ms=1,version=2 WHERE id=$1`, unsafeResultRef.id, unsafeResultRef.startedAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	unsafeSideEffect := validResolvedToolCall(fixture.secondary, 9, "ApplyApprovedPatch", "WRITE_KNOWLEDGE")
	unsafeSideEffect.sideEffectLevel = stringPointer("DOMAIN_WRITE")
	unsafeSideEffect.invocationPolicy = stringPointer("TRUSTED_WORKFLOW_ONLY")
	unsafeSideEffect.idempotencyKey = stringPointer("write:unsafe-ref")
	if err := insertToolCall(ctx, pool, unsafeSideEffect); err != nil {
		t.Fatal(err)
	}
	_, unsafeErr = pool.Exec(ctx, `UPDATE workflow.tool_call SET status='SUCCEEDED',response_hash=repeat('a',64),response_bytes=2,response_summary='{"count":1}',side_effect_type='writeback_execution',side_effect_id='Authorization: Bearer secret',completed_at=$2,duration_ms=1,version=2 WHERE id=$1`, unsafeSideEffect.id, unsafeSideEffect.startedAt.Add(time.Millisecond))
	assertPostgresCode(t, unsafeErr, "23514")

	writeCall := validResolvedRefusedToolCall(fixture.primary, 10, "ApplyApprovedPatch", "WRITE_KNOWLEDGE")
	writeCall.sideEffectLevel = stringPointer("DOMAIN_WRITE")
	writeCall.invocationPolicy = stringPointer("TRUSTED_WORKFLOW_ONLY")
	if err := insertToolCall(ctx, pool, writeCall); err != nil {
		t.Fatalf("refused write without idempotency key must remain auditable: %v", err)
	}
	writeCallWithKey := validResolvedRefusedToolCall(fixture.primary, 11, "ApplyApprovedPatch", "WRITE_KNOWLEDGE")
	writeCallWithKey.sideEffectLevel = stringPointer("DOMAIN_WRITE")
	writeCallWithKey.invocationPolicy = stringPointer("TRUSTED_WORKFLOW_ONLY")
	writeCallWithKey.idempotencyKey = stringPointer("write:shared-key")
	if err := insertToolCall(ctx, pool, writeCallWithKey); err != nil {
		t.Fatal(err)
	}
	replayedOtherBinding := validResolvedRefusedToolCall(fixture.secondary, 1, "ApplyApprovedPatch", "WRITE_KNOWLEDGE")
	replayedOtherBinding.sideEffectLevel = stringPointer("DOMAIN_WRITE")
	replayedOtherBinding.invocationPolicy = stringPointer("TRUSTED_WORKFLOW_ONLY")
	replayedOtherBinding.idempotencyKey = stringPointer("write:shared-key")
	if err := insertToolCall(ctx, pool, replayedOtherBinding); err != nil {
		t.Fatalf("refused call must not reserve execution idempotency key: %v", err)
	}
	unsafeModelWrite := validResolvedRefusedToolCall(fixture.primary, 12, "ApplyApprovedPatch", "WRITE_KNOWLEDGE")
	unsafeModelWrite.sideEffectLevel = stringPointer("DOMAIN_WRITE")
	unsafeModelWrite.invocationPolicy = stringPointer("MODEL_REQUESTABLE")
	assertPostgresCode(t, insertToolCall(ctx, pool, unsafeModelWrite), "23514")

	readOne := validResolvedRefusedToolCall(fixture.primary, 13, "ReadSource", "READ_LOCAL")
	readOne.idempotencyKey = stringPointer("read:reusable-key")
	if err := insertToolCall(ctx, pool, readOne); err != nil {
		t.Fatal(err)
	}
	readTwo := validResolvedRefusedToolCall(fixture.secondary, 2, "ReadSource", "READ_LOCAL")
	readTwo.idempotencyKey = stringPointer("read:reusable-key")
	if err := insertToolCall(ctx, pool, readTwo); err != nil {
		t.Fatalf("read-only idempotency key was globally reserved: %v", err)
	}
	activeWrite := validResolvedToolCall(fixture.primary, 14, "ApplyApprovedPatch", "WRITE_KNOWLEDGE")
	activeWrite.sideEffectLevel = stringPointer("DOMAIN_WRITE")
	activeWrite.invocationPolicy = stringPointer("TRUSTED_WORKFLOW_ONLY")
	activeWrite.idempotencyKey = stringPointer("write:active-key")
	if err := insertToolCall(ctx, pool, activeWrite); err != nil {
		t.Fatal(err)
	}
	activeWriteConflict := validResolvedToolCall(fixture.secondary, 3, "ApplyApprovedPatch", "WRITE_KNOWLEDGE")
	activeWriteConflict.sideEffectLevel = stringPointer("DOMAIN_WRITE")
	activeWriteConflict.invocationPolicy = stringPointer("TRUSTED_WORKFLOW_ONLY")
	activeWriteConflict.idempotencyKey = stringPointer("write:active-key")
	assertPostgresCode(t, insertToolCall(ctx, pool, activeWriteConflict), "23505")
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second'
		WHERE id=$1`, fixture.secondary.nodeAttemptID); err != nil {
		t.Fatal(err)
	}
	expiredLease := validUnresolvedRefusedToolCall(fixture.secondary, 4)
	assertPostgresCode(t, insertToolCall(ctx, pool, expiredLease), "55000")
}

type toolCallIdentity struct {
	workspaceID   string
	workflowRunID string
	nodeRunID     string
	nodeAttemptID string
}

type toolCallFixture struct {
	primary          toolCallIdentity
	secondary        toolCallIdentity
	other            toolCallIdentity
	otherWorkspaceID string
}

type toolCallInsert struct {
	id, workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
	callNo                                                   int16
	requestedToolName                                        string
	toolVersion                                              *int64
	definitionHash, inputSchemaID                            *string
	inputSchemaVersion                                       *int64
	outputSchemaID                                           *string
	outputSchemaVersion                                      *int64
	capability, sideEffectLevel, invocationPolicy            *string
	idempotencyKey                                           *string
	requestHash                                              string
	requestBytes                                             int64
	requestSummary                                           []byte
	responseHash                                             *string
	responseBytes                                            int64
	responseSummary                                          []byte
	resultRef, sideEffectType, sideEffectID                  *string
	status, errorCode                                        string
	retryable                                                bool
	startedAt                                                time.Time
	completedAt                                              *time.Time
	durationMS, version                                      int64
}

func insertToolCall(ctx context.Context, pool *pgxpool.Pool, row toolCallInsert) error {
	_, err := pool.Exec(ctx, `INSERT INTO workflow.tool_call(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
		requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
		output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
		idempotency_key,request_hash,request_bytes,request_summary,response_hash,response_bytes,
		response_summary,result_ref,side_effect_type,side_effect_id,status,error_code,retryable,
		started_at,completed_at,duration_ms,version
	) VALUES(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20::jsonb,
		$21,$22,$23::jsonb,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33
	)`, row.id, row.workspaceID, row.workflowRunID, row.nodeRunID, row.nodeAttemptID, row.callNo,
		row.requestedToolName, row.toolVersion, row.definitionHash, row.inputSchemaID, row.inputSchemaVersion,
		row.outputSchemaID, row.outputSchemaVersion, row.capability, row.sideEffectLevel, row.invocationPolicy,
		row.idempotencyKey, row.requestHash, row.requestBytes, row.requestSummary, row.responseHash, row.responseBytes,
		nullableJSON(row.responseSummary), row.resultRef, row.sideEffectType, row.sideEffectID, row.status, nullableText(row.errorCode), row.retryable,
		row.startedAt, row.completedAt, row.durationMS, row.version)
	return err
}

func validResolvedToolCall(identity toolCallIdentity, callNo int16, name, capability string) toolCallInsert {
	startedAt := time.Now().UTC().Add(-time.Second)
	version := int64(1)
	definitionHash := strings.Repeat("a", 64)
	inputSchemaID := "tool.input"
	outputSchemaID := "tool.output"
	sideEffect := "NONE"
	invocation := "MODEL_REQUESTABLE"
	row := toolCallInsert{
		id: requestedToolCallID(identity.nodeAttemptID, callNo), workspaceID: identity.workspaceID,
		workflowRunID: identity.workflowRunID, nodeRunID: identity.nodeRunID, nodeAttemptID: identity.nodeAttemptID,
		callNo: callNo, requestedToolName: name, toolVersion: &version, definitionHash: &definitionHash,
		inputSchemaID: &inputSchemaID, inputSchemaVersion: &version, outputSchemaID: &outputSchemaID,
		outputSchemaVersion: &version, sideEffectLevel: &sideEffect, invocationPolicy: &invocation,
		requestHash: strings.Repeat("1", 64), requestBytes: 32, requestSummary: []byte(`{"argument_bytes":2}`),
		status: "STARTED", startedAt: startedAt, version: 1,
	}
	if capability != "" {
		row.capability = stringPointer(capability)
	}
	return row
}

func validResolvedRefusedToolCall(identity toolCallIdentity, callNo int16, name, capability string) toolCallInsert {
	row := validResolvedToolCall(identity, callNo, name, capability)
	row.status = "REFUSED"
	row.errorCode = "TOOL_POLICY_REFUSED"
	row.completedAt = timePointer(row.startedAt.Add(time.Millisecond))
	row.durationMS = 1
	return row
}

func validUnresolvedRefusedToolCall(identity toolCallIdentity, callNo int16) toolCallInsert {
	startedAt := time.Now().UTC().Add(-time.Second)
	return toolCallInsert{
		id: requestedToolCallID(identity.nodeAttemptID, callNo), workspaceID: identity.workspaceID,
		workflowRunID: identity.workflowRunID, nodeRunID: identity.nodeRunID, nodeAttemptID: identity.nodeAttemptID,
		callNo: callNo, requestedToolName: "UnknownTool", requestHash: strings.Repeat("1", 64), requestBytes: 32,
		requestSummary: []byte(`{"argument_bytes":2}`), status: "REFUSED", errorCode: "TOOL_NOT_FOUND",
		startedAt: startedAt, completedAt: timePointer(startedAt.Add(time.Millisecond)), durationMS: 1, version: 1,
	}
}

func insertToolCallWorkflowFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) toolCallFixture {
	t.Helper()
	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	fixture := toolCallFixture{
		primary:          toolCallIdentity{"c1000000-0000-4000-8000-000000000001", "c3000000-0000-4000-8000-000000000001", "c4000000-0000-4000-8000-000000000001", "c5000000-0000-4000-8000-000000000001"},
		secondary:        toolCallIdentity{"c1000000-0000-4000-8000-000000000001", "c3000000-0000-4000-8000-000000000002", "c4000000-0000-4000-8000-000000000002", "c5000000-0000-4000-8000-000000000002"},
		other:            toolCallIdentity{"c1000000-0000-4000-8000-000000000002", "c3000000-0000-4000-8000-000000000003", "c4000000-0000-4000-8000-000000000003", "c5000000-0000-4000-8000-000000000003"},
		otherWorkspaceID: "c1000000-0000-4000-8000-000000000002",
	}
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at) VALUES
			($1,'tool-a','/tmp/tool-a','/tmp/tool-a',CURRENT_TIMESTAMP,'active',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
			($2,'tool-b','/tmp/tool-b','/tmp/tool-b',CURRENT_TIMESTAMP,'inactive',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{fixture.primary.workspaceID, fixture.other.workspaceID}},
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES
			($1,$2,'tool-a-1',1,'{"nodes":[]}',CURRENT_TIMESTAMP),
			($3,$2,'tool-a-2',1,'{"nodes":[]}',CURRENT_TIMESTAMP),
			($4,$5,'tool-b-1',1,'{"nodes":[]}',CURRENT_TIMESTAMP)`, []any{"c2000000-0000-4000-8000-000000000001", fixture.primary.workspaceID, "c2000000-0000-4000-8000-000000000002", "c2000000-0000-4000-8000-000000000003", fixture.other.workspaceID}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES
			($1,$2,$3,'running','{}',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
			($4,$2,$5,'running','{}',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
			($6,$7,$8,'running','{}',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{fixture.primary.workflowRunID, fixture.primary.workspaceID, "c2000000-0000-4000-8000-000000000001", fixture.secondary.workflowRunID, "c2000000-0000-4000-8000-000000000002", fixture.other.workflowRunID, fixture.other.workspaceID, "c2000000-0000-4000-8000-000000000003"}},
		{`INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,input,attempt,lease_owner,lease_until,version,created_at,updated_at) VALUES
				($1,$2,'tool-1','agent.tool','running','{}',1,'worker-a',$7,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
				($3,$4,'tool-2','agent.tool','running','{}',1,'worker-a',$7,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
				($5,$6,'tool-3','agent.tool','running','{}',1,'worker-b',$7,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{fixture.primary.nodeRunID, fixture.primary.workflowRunID, fixture.secondary.nodeRunID, fixture.secondary.workflowRunID, fixture.other.nodeRunID, fixture.other.workflowRunID, leaseUntil}},
		{`INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at) VALUES
				($1,$2,1,1,0,'tool-delivery-1','worker-a',$7,'running',CURRENT_TIMESTAMP),
				($3,$4,1,1,0,'tool-delivery-2','worker-a',$7,'running',CURRENT_TIMESTAMP),
				($5,$6,1,1,0,'tool-delivery-3','worker-b',$7,'running',CURRENT_TIMESTAMP)`, []any{fixture.primary.nodeAttemptID, fixture.primary.nodeRunID, fixture.secondary.nodeAttemptID, fixture.secondary.nodeRunID, fixture.other.nodeAttemptID, fixture.other.nodeRunID, leaseUntil}},
	}
	for _, query := range queries {
		if _, err := pool.Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func assertToolRegistryMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var tables, columns, triggers, indexes, meta, prohibited int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='workflow' AND table_name='tool_call'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='workflow' AND table_name='tool_call'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE tgrelid='workflow.tool_call'::regclass AND tgname='workflow_tool_call_guard_mutation'`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname='workflow' AND tablename='tool_call'
		AND indexname = ANY($1)`, []string{
		"uq_workflow_tool_call_attempt_active", "uq_workflow_tool_call_side_effect_idempotency",
		"idx_workflow_tool_call_run_node_timeline", "idx_workflow_tool_call_run_timeline", "idx_workflow_tool_call_workspace_status",
		"idx_workflow_tool_call_side_effect_ref", "idx_workflow_tool_call_started_recovery",
	}).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='tool_registry_security' AND value='m6-03'`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='workflow' AND table_name='tool_call'
		AND column_name = ANY($1)`, []string{"arguments", "raw_arguments", "output", "raw_output", "credential", "authorization", "prompt", "stderr"}).Scan(&prohibited); err != nil {
		t.Fatal(err)
	}
	if tables != 1 || columns != 33 || triggers != 1 || indexes != 7 || meta != 1 || prohibited != 0 {
		t.Fatalf("tables=%d columns=%d triggers=%d indexes=%d meta=%d prohibited=%d", tables, columns, triggers, indexes, meta, prohibited)
	}
}

func migrateToolRegistryTestDatabase(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
}

func requestedToolCallID(attemptID string, callNo int16) string {
	value := []byte(attemptID)
	value[0] = 'd'
	value[1] = attemptID[len(attemptID)-1]
	value[len(value)-2] = "0123456789abcdef"[(callNo/16)%16]
	value[len(value)-1] = "0123456789abcdef"[callNo%16]
	return string(value)
}

func nullableJSON(value []byte) any {
	if value == nil {
		return nil
	}
	return string(value)
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func stringPointer(value string) *string     { return &value }
func timePointer(value time.Time) *time.Time { return &value }
