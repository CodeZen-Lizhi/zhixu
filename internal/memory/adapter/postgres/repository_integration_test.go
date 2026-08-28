//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryPostgreSQLLifecycleReceiptScopeAndAudit(t *testing.T) {
	runMemoryIntegrationVariants(t, testRepositoryPostgreSQLLifecycleReceiptScopeAndAudit)
}

func testRepositoryPostgreSQLLifecycleReceiptScopeAndAudit(t *testing.T, ctx context.Context, repository memoryapp.Repository, testCase memoryIntegrationCase) {
	resources := testCase.repositories
	id := testCase.id
	pool := resources.pool
	workspaceA := id(1)
	workspaceB := id(2)
	seedMemoryWorkspace(t, ctx, pool, workspaceA, "memory-integration-a")
	seedMemoryWorkspace(t, ctx, pool, workspaceB, "memory-integration-b")
	now := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	service, err := memoryapp.NewService(memoryapp.Dependencies{
		Repository: repository, IDs: &memoryIntegrationIDs{base: testCase.offset, next: 10}, Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := domain.Principal{Kind: domain.PrincipalUser, ID: id(3)}
	other := domain.Principal{Kind: domain.PrincipalUser, ID: id(4)}
	task := id(5)
	created, err := service.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
		WorkspaceID: workspaceA, Owner: owner, Type: domain.TypePreference,
		Content: json.RawMessage(`{"language":"zh"}`), Source: domain.Source{Type: domain.SourceAgent, Ref: "agent:proposal-1"},
		TaskScopeID: &task, IdempotencyKey: "candidate-1",
	})
	if err != nil || created.Memory.Status != domain.StatusCandidate || created.Replayed {
		t.Fatalf("create candidate=%#v err=%v", created, err)
	}
	if effective, err := service.LoadEffective(ctx, memoryapp.EffectiveQuery{Scope: memoryapp.Scope{WorkspaceID: workspaceA, Owner: owner}, TaskScopeID: &task, Limit: 10}); err != nil || len(effective) != 0 {
		t.Fatalf("candidate effective=%#v err=%v", effective, err)
	}
	confirmed, err := service.Confirm(ctx, memoryapp.TransitionCommand{
		Scope: memoryapp.Scope{WorkspaceID: workspaceA, Owner: owner}, MemoryID: created.Memory.ID, ExpectedVersion: 1, IdempotencyKey: "confirm-1",
	})
	if err != nil || confirmed.Memory.Status != domain.StatusActive || confirmed.Memory.Version != 2 {
		t.Fatalf("confirm=%#v err=%v", confirmed, err)
	}
	replayed, err := service.Confirm(ctx, memoryapp.TransitionCommand{
		Scope: memoryapp.Scope{WorkspaceID: workspaceA, Owner: owner}, MemoryID: created.Memory.ID, ExpectedVersion: 1, IdempotencyKey: "confirm-1",
	})
	if err != nil || !replayed.Replayed || replayed.Memory.ID != confirmed.Memory.ID {
		t.Fatalf("confirm replay=%#v err=%v", replayed, err)
	}
	if _, err := service.Confirm(ctx, memoryapp.TransitionCommand{
		Scope: memoryapp.Scope{WorkspaceID: workspaceA, Owner: owner}, MemoryID: created.Memory.ID, ExpectedVersion: 2, IdempotencyKey: "confirm-1",
	}); !memoryIntegrationErrorCode(err, domain.ErrorCodeIdempotencyConflict) {
		t.Fatalf("confirm conflict error=%v", err)
	}
	if _, err := service.Get(ctx, memoryapp.Scope{WorkspaceID: workspaceA, Owner: other}, created.Memory.ID); !memoryIntegrationErrorCode(err, domain.ErrorCodeNotFound) {
		t.Fatalf("cross-owner get error=%v", err)
	}
	if _, err := service.Get(ctx, memoryapp.Scope{WorkspaceID: workspaceB, Owner: owner}, created.Memory.ID); !memoryIntegrationErrorCode(err, domain.ErrorCodeNotFound) {
		t.Fatalf("cross-workspace get error=%v", err)
	}
	effective, err := service.LoadEffective(ctx, memoryapp.EffectiveQuery{Scope: memoryapp.Scope{WorkspaceID: workspaceA, Owner: owner}, TaskScopeID: &task, Limit: 10})
	if err != nil || len(effective) != 1 || effective[0].ID != created.Memory.ID {
		t.Fatalf("confirmed effective=%#v err=%v", effective, err)
	}
	if effective, err := service.LoadEffective(ctx, memoryapp.EffectiveQuery{Scope: memoryapp.Scope{WorkspaceID: workspaceA, Owner: owner}, Limit: 10}); err != nil || len(effective) != 0 {
		t.Fatalf("task mismatch effective=%#v err=%v", effective, err)
	}
	ownerBoundCandidate, err := service.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
		WorkspaceID: workspaceA, Owner: owner, Type: domain.TypeGoal,
		Content: json.RawMessage(`{"goal":"practice"}`), Source: domain.Source{Type: domain.SourceUser, Ref: "user:goal"},
		IdempotencyKey: "candidate-owner-check",
	})
	if err != nil {
		t.Fatalf("create owner-bound candidate err=%v", err)
	}
	firstPage, err := service.List(ctx, memoryapp.ListQuery{
		Scope:    memoryapp.Scope{WorkspaceID: workspaceA, Owner: owner},
		Types:    []domain.Type{domain.TypeGoal, domain.TypePreference},
		Statuses: []domain.Status{domain.StatusCandidate, domain.StatusActive},
		Limit:    1,
	})
	if err != nil || len(firstPage.Items) != 1 || firstPage.Next == nil {
		t.Fatalf("first keyset page=%#v err=%v", firstPage, err)
	}
	secondPage, err := service.List(ctx, memoryapp.ListQuery{
		Scope:    memoryapp.Scope{WorkspaceID: workspaceA, Owner: owner},
		Types:    []domain.Type{domain.TypeGoal, domain.TypePreference},
		Statuses: []domain.Status{domain.StatusCandidate, domain.StatusActive},
		Limit:    1,
		After:    firstPage.Next,
	})
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("second keyset page=%#v err=%v", secondPage, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.memory
		SET status='ACTIVE',confirmed_at=$3,confirmed_by_principal_kind=$4,confirmed_by_principal_id=$5,version=version+1,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, string(workspaceA), string(ownerBoundCandidate.Memory.ID), now.Add(time.Minute), string(other.Kind), string(other.ID)); err == nil {
		t.Fatal("database allowed a different principal to confirm memory")
	} else {
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.ConstraintName != "learning_memory_confirmation_owner_check" {
			t.Fatalf("owner confirmation constraint error=%v", err)
		}
	}
	var commands, audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_command WHERE workspace_id=$1 AND memory_id=$2`, string(workspaceA), string(created.Memory.ID)).Scan(&commands); err != nil || commands != 2 {
		t.Fatalf("receipt count=%d err=%v", commands, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_audit WHERE workspace_id=$1 AND memory_id=$2`, string(workspaceA), string(created.Memory.ID)).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.memory_audit SET action='DELETED' WHERE workspace_id=$1 AND memory_id=$2`, string(workspaceA), string(created.Memory.ID)); err == nil {
		t.Fatal("memory audit mutation unexpectedly succeeded")
	} else {
		memoryIntegrationPostgresCode(t, err, "55000")
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.memory_command SET request_hash=request_hash WHERE workspace_id=$1 AND memory_id=$2`, string(workspaceA), string(created.Memory.ID)); err == nil {
		t.Fatal("memory command mutation unexpectedly succeeded")
	} else {
		memoryIntegrationPostgresCode(t, err, "55000")
	}
	memoryIntegrationAssertContextClassification(t, ctx, repository, memoryapp.Scope{WorkspaceID: workspaceA, Owner: owner}, created.Memory.ID, id(999))
	memoryIntegrationAssertHistoryWriteRollback(t, ctx, service, pool, workspaceA, owner)
	memoryIntegrationAssertSharedPoolVisibility(t, ctx, resources, id(900))
}

func TestRepositoryPostgreSQLConcurrentConfirmReplaysExactlyOnce(t *testing.T) {
	runMemoryIntegrationVariants(t, testRepositoryPostgreSQLConcurrentConfirmReplaysExactlyOnce)
}

func testRepositoryPostgreSQLConcurrentConfirmReplaysExactlyOnce(t *testing.T, ctx context.Context, repository memoryapp.Repository, testCase memoryIntegrationCase) {
	resources := testCase.repositories
	id := testCase.id
	pool := resources.pool
	workspaceID := id(30)
	seedMemoryWorkspace(t, ctx, pool, workspaceID, "memory-concurrent-confirm")
	now := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	service, err := memoryapp.NewService(memoryapp.Dependencies{
		Repository: repository, IDs: &memoryIntegrationIDs{base: testCase.offset, next: 40}, Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := domain.Principal{Kind: domain.PrincipalUser, ID: id(31)}
	created, err := service.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
		WorkspaceID: workspaceID, Owner: owner, Type: domain.TypePreference,
		Content: json.RawMessage(`{"mode":"focused"}`), Source: domain.Source{Type: domain.SourceAgent, Ref: "agent:concurrent"},
		IdempotencyKey: "candidate-concurrent",
	})
	if err != nil {
		t.Fatalf("create candidate err=%v", err)
	}

	const attempts = 8
	type result struct {
		value memoryapp.CommandResult
		err   error
	}
	results := make(chan result, attempts)
	var group sync.WaitGroup
	group.Add(attempts)
	for range attempts {
		go func() {
			defer group.Done()
			value, err := service.Confirm(ctx, memoryapp.TransitionCommand{
				Scope: memoryapp.Scope{WorkspaceID: workspaceID, Owner: owner}, MemoryID: created.Memory.ID,
				ExpectedVersion: 1, IdempotencyKey: "confirm-concurrent",
			})
			results <- result{value: value, err: err}
		}()
	}
	group.Wait()
	close(results)

	createdCount := 0
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("concurrent confirm err=%v", outcome.err)
		}
		if outcome.value.Memory.Status != domain.StatusActive || outcome.value.Memory.Version != 2 {
			t.Fatalf("concurrent confirm result=%#v", outcome.value)
		}
		if !outcome.value.Replayed {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("non-replayed concurrent confirms=%d, want 1", createdCount)
	}
	var commands, audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_command WHERE workspace_id=$1 AND memory_id=$2`, string(workspaceID), string(created.Memory.ID)).Scan(&commands); err != nil || commands != 2 {
		t.Fatalf("receipt count=%d err=%v", commands, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_audit WHERE workspace_id=$1 AND memory_id=$2`, string(workspaceID), string(created.Memory.ID)).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
	memoryIntegrationAssertStaleCAS(t, ctx, repository, service, workspaceID, owner, now)
	memoryIntegrationAssertConcurrentExpiry(t, ctx, service, pool, workspaceID, owner, now)
}

func TestRepositoryPostgreSQLInterviewCandidateDualIdempotency(t *testing.T) {
	runMemoryIntegrationVariants(t, testRepositoryPostgreSQLInterviewCandidateDualIdempotency)
}

func testRepositoryPostgreSQLInterviewCandidateDualIdempotency(t *testing.T, ctx context.Context, repository memoryapp.Repository, testCase memoryIntegrationCase) {
	resources := testCase.repositories
	id := testCase.id
	pool := resources.pool
	workspaceID := id(50)
	seedMemoryWorkspace(t, ctx, pool, workspaceID, "memory-interview-candidate-idempotency")
	fixture := seedMemoryInterviewPath(t, ctx, pool, workspaceID, id)
	owner := domain.SingleUserOwner()
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	service, err := memoryapp.NewService(memoryapp.Dependencies{
		Repository: repository, IDs: &concurrentMemoryIntegrationIDs{base: testCase.offset, next: 60}, Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	interview := func(key, ref string, content json.RawMessage) (memoryapp.CommandResult, error) {
		return service.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
			WorkspaceID: workspaceID, Owner: owner, Type: domain.TypeGoal, Content: content,
			Source: domain.Source{Type: domain.SourceInterview, Ref: ref}, TaskScopeID: &fixture.sessionID, IdempotencyKey: key,
		})
	}
	sourceRef := memoryInterviewSourceRef(fixture.sessionID, fixture.pathID, fixture.primaryStepID)
	otherRef := memoryInterviewSourceRef(fixture.sessionID, fixture.pathID, fixture.otherStepID)
	concurrentRef := memoryInterviewSourceRef(fixture.sessionID, fixture.pathID, fixture.concurrentStepID)
	content := json.RawMessage(`{"goal":"Practice cancellation","rationale":"Close the persisted gap"}`)
	created, err := interview("interview-primary", sourceRef, content)
	if err != nil || created.Replayed {
		t.Fatalf("create interview candidate=%#v err=%v", created, err)
	}
	exactReplay, err := interview("interview-primary", sourceRef, content)
	if err != nil || !exactReplay.Replayed || exactReplay.Memory.ID != created.Memory.ID {
		t.Fatalf("exact key replay=%#v err=%v", exactReplay, err)
	}
	if _, err := interview("interview-primary", sourceRef, json.RawMessage(`{"goal":"Different goal","rationale":"Different request"}`)); !memoryIntegrationErrorCode(err, domain.ErrorCodeIdempotencyConflict) {
		t.Fatalf("same client key with different content error=%v", err)
	}
	if _, err := interview("interview-primary", otherRef, content); !memoryIntegrationErrorCode(err, domain.ErrorCodeIdempotencyConflict) {
		t.Fatalf("same client key with different step error=%v", err)
	}
	var otherStepCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory WHERE workspace_id=$1 AND source_type='INTERVIEW' AND source_ref=$2`, string(workspaceID), otherRef).Scan(&otherStepCount); err != nil || otherStepCount != 0 {
		t.Fatalf("same key created a second step candidate count=%d err=%v", otherStepCount, err)
	}
	replayed, err := interview("interview-primary-retry", sourceRef, content)
	if err != nil || !replayed.Replayed || replayed.Memory.ID != created.Memory.ID {
		t.Fatalf("new key same step replay=%#v err=%v", replayed, err)
	}
	if _, err := interview("interview-primary-conflict", sourceRef, json.RawMessage(`{"goal":"Different goal","rationale":"Different request"}`)); !memoryIntegrationErrorCode(err, domain.ErrorCodeIdempotencyConflict) {
		t.Fatalf("same provenance with different content error=%v", err)
	}
	var candidateCount, auditCount, receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory WHERE workspace_id=$1 AND source_type='INTERVIEW' AND source_ref=$2`, string(workspaceID), sourceRef).Scan(&candidateCount); err != nil || candidateCount != 1 {
		t.Fatalf("interview candidate count=%d err=%v", candidateCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_audit WHERE workspace_id=$1 AND memory_id=$2 AND action='CANDIDATE_CREATED'`, string(workspaceID), string(created.Memory.ID)).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("interview candidate audit count=%d err=%v", auditCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_command WHERE workspace_id=$1 AND memory_id=$2 AND command_type='CREATE_CANDIDATE'`, string(workspaceID), string(created.Memory.ID)).Scan(&receiptCount); err != nil || receiptCount != 2 {
		t.Fatalf("interview candidate receipt count=%d err=%v", receiptCount, err)
	}
	var persistedSessionID, persistedPathID, persistedStepID string
	if err := pool.QueryRow(ctx, `SELECT interview_session_id::text,interview_path_id::text,interview_step_id::text FROM learning.memory WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(created.Memory.ID)).Scan(&persistedSessionID, &persistedPathID, &persistedStepID); err != nil || persistedSessionID != string(fixture.sessionID) || persistedPathID != string(fixture.pathID) || persistedStepID != string(fixture.primaryStepID) {
		t.Fatalf("interview provenance projection session=%s path=%s step=%s err=%v", persistedSessionID, persistedPathID, persistedStepID, err)
	}

	const attempts = 8
	type outcome struct {
		result memoryapp.CommandResult
		err    error
	}
	results := make(chan outcome, attempts)
	var group sync.WaitGroup
	group.Add(attempts)
	for attempt := range attempts {
		go func(attempt int) {
			defer group.Done()
			result, err := interview(fmt.Sprintf("interview-concurrent-%d", attempt), concurrentRef, content)
			results <- outcome{result: result, err: err}
		}(attempt)
	}
	group.Wait()
	close(results)
	nonReplayed := 0
	var concurrentID foundation.ID
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("concurrent interview candidate error=%v", outcome.err)
		}
		if outcome.result.Memory.ID == "" || outcome.result.Memory.ID != concurrentID && concurrentID != "" {
			t.Fatalf("concurrent interview candidate result=%#v", outcome.result)
		}
		concurrentID = outcome.result.Memory.ID
		if !outcome.result.Replayed {
			nonReplayed++
		}
	}
	if nonReplayed != 1 {
		t.Fatalf("concurrent interview candidate non-replayed=%d", nonReplayed)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory WHERE workspace_id=$1 AND source_type='INTERVIEW' AND source_ref=$2`, string(workspaceID), concurrentRef).Scan(&candidateCount); err != nil || candidateCount != 1 {
		t.Fatalf("concurrent interview candidate count=%d err=%v", candidateCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_audit WHERE workspace_id=$1 AND memory_id=$2 AND action='CANDIDATE_CREATED'`, string(workspaceID), string(concurrentID)).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("concurrent interview candidate audit count=%d err=%v", auditCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_command WHERE workspace_id=$1 AND memory_id=$2 AND command_type='CREATE_CANDIDATE'`, string(workspaceID), string(concurrentID)).Scan(&receiptCount); err != nil || receiptCount != attempts {
		t.Fatalf("concurrent interview candidate receipt count=%d err=%v", receiptCount, err)
	}

	userOne, err := service.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
		WorkspaceID: workspaceID, Owner: owner, Type: domain.TypePreference, Content: json.RawMessage(`{"mode":"concise"}`),
		Source: domain.Source{Type: domain.SourceUser, Ref: "user:manual"}, IdempotencyKey: "user-manual-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	userTwo, err := service.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
		WorkspaceID: workspaceID, Owner: owner, Type: domain.TypePreference, Content: json.RawMessage(`{"mode":"concise"}`),
		Source: domain.Source{Type: domain.SourceUser, Ref: "user:manual"}, IdempotencyKey: "user-manual-two",
	})
	if err != nil || userOne.Replayed || userTwo.Replayed || userOne.Memory.ID == userTwo.Memory.ID {
		t.Fatalf("user provenance candidates first=%#v second=%#v err=%v", userOne, userTwo, err)
	}
}

type memoryInterviewPathFixture struct {
	sessionID        foundation.ID
	pathID           foundation.ID
	primaryStepID    foundation.ID
	otherStepID      foundation.ID
	concurrentStepID foundation.ID
}

func seedMemoryInterviewPath(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, id func(int) foundation.ID) memoryInterviewPathFixture {
	t.Helper()
	fixture := memoryInterviewPathFixture{
		sessionID:        id(51),
		pathID:           id(52),
		primaryStepID:    id(53),
		otherStepID:      id(54),
		concurrentStepID: id(55),
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	contentArtifactID := id(80)
	sourceID := id(81)
	sourceVersionID := id(82)
	parseProjectionID := id(83)
	sourceSpanID := id(84)
	claimID := id(85)
	claimSourceID := id(86)
	reportArtifactID := id(87)
	reportRevisionID := id(88)
	pathArtifactID := id(89)
	pathRevisionID := id(90)
	reportID := id(91)
	contentHash := strings.Repeat("c", 64)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,4,$4,$5)`, []any{string(contentArtifactID), string(workspaceID), contentHash, ".knowledge/sources/" + contentHash, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'text','memory-interview.txt','memory-interview.txt',$3)`, []any{string(sourceID), string(workspaceID), now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,$5,4,'text/plain','memory-interview.txt','pending',$6)`, []any{string(sourceVersionID), string(sourceID), string(workspaceID), string(contentArtifactID), contentHash, now}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES($1,$2,$3,'text','v1',repeat('d',64),'v1',repeat('e',64),'[]',$4)`, []any{string(parseProjectionID), string(workspaceID), string(contentArtifactID), now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,4,'{}',repeat('f',64),'v1','v1',$5)`, []any{string(sourceSpanID), string(workspaceID), string(contentArtifactID), string(parseProjectionID), now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, []any{string(sourceVersionID), string(parseProjectionID), string(workspaceID), now}},
		{`INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_factors,fingerprint,version,created_at,updated_at) VALUES($1,$2,'Practice cancellation','practice cancellation','{}','knowledge-applicability/v1',repeat('1',64),'SUGGESTED','{}',repeat('2',64),1,$3,$3)`, []any{string(claimID), string(workspaceID), now}},
		{`INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at) VALUES($1,$2,$3,$4,$5,'SUPPORTS','Memory Interview fixture',repeat('a',64),$6)`, []any{string(claimSourceID), string(workspaceID), string(claimID), string(sourceVersionID), string(sourceSpanID), now}},
		{`UPDATE core.claim SET status='CONFIRMED',version=2,updated_at=$3 WHERE id=$1 AND workspace_id=$2`, []any{string(claimID), string(workspaceID), now.Add(time.Second)}},
		{`INSERT INTO learning.review_session(id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at,ended_at) VALUES($1,$2,NULL,'INTERVIEW','ACTIVE','{"schema_version":"interview/v1"}'::jsonb,'memory-interview-session',repeat('3',64),$3,NULL)`, []any{string(fixture.sessionID), string(workspaceID), now}},
		{`INSERT INTO learning.interview_session(session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at) VALUES($1,$2,'interview/v1',1,0,$3,$3)`, []any{string(fixture.sessionID), string(workspaceID), now}},
		{`INSERT INTO learning.artifact(id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at,domain_schema_version,scope_definition,source_coverage,current_revision_id) VALUES($1,$2,'INTERVIEW_DOC','Memory Interview report','{}','DRAFT',1,$3,$3,'artifact/v1','{}','[]',$4)`, []any{string(reportArtifactID), string(workspaceID), now, string(reportRevisionID)}},
		{`INSERT INTO learning.artifact_revision(id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,created_at,domain_schema_version,content_hash,created_by_type) VALUES($1,$2,$3,1,'SNAPSHOT','[]','[]','[]','[]','[]','','{}',$4,'artifact-revision/v1',repeat('4',64),'HUMAN')`, []any{string(reportRevisionID), string(reportArtifactID), string(workspaceID), now}},
		{`INSERT INTO learning.artifact(id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at,domain_schema_version,scope_definition,source_coverage,current_revision_id) VALUES($1,$2,'LEARNING_PATH','Memory Interview path','{}','DRAFT',1,$3,$3,'artifact/v1','{}','[]',$4)`, []any{string(pathArtifactID), string(workspaceID), now, string(pathRevisionID)}},
		{`INSERT INTO learning.artifact_revision(id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,created_at,domain_schema_version,content_hash,created_by_type) VALUES($1,$2,$3,1,'SNAPSHOT','[]','[]','[]','[]','[]','','{}',$4,'artifact-revision/v1',repeat('5',64),'HUMAN')`, []any{string(pathRevisionID), string(pathArtifactID), string(workspaceID), now}},
		{`INSERT INTO learning.interview_report(id,workspace_id,session_id,domain_schema_version,report,report_hash,artifact_id,artifact_revision_id,artifact_version,created_at) VALUES($1,$2,$3,'interview-report/v1','{}',repeat('6',64),$4,$5,1,$6)`, []any{string(reportID), string(workspaceID), string(fixture.sessionID), string(reportArtifactID), string(reportRevisionID), now}},
		{`INSERT INTO learning.interview_learning_path(id,workspace_id,session_id,report_id,artifact_id,artifact_revision_id,artifact_version,status,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,1,'ACTIVE',1,$7,$7)`, []any{string(fixture.pathID), string(workspaceID), string(fixture.sessionID), string(reportID), string(pathArtifactID), string(pathRevisionID), now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	for index, stepID := range []foundation.ID{fixture.primaryStepID, fixture.otherStepID, fixture.concurrentStepID} {
		if _, err := tx.Exec(ctx, `INSERT INTO learning.interview_learning_path_step(id,workspace_id,path_id,step_no,claim_id,source_version_id,source_span_id,evidence_hash,title,rationale,status,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,repeat('a',64),'Practice cancellation','Close the persisted gap','PENDING',1,$8,$8)`, string(stepID), string(workspaceID), string(fixture.pathID), index+1, string(claimID), string(sourceVersionID), string(sourceSpanID), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func memoryInterviewSourceRef(sessionID, pathID, stepID foundation.ID) string {
	return fmt.Sprintf("interview:%s:learning-path:%s:step:%s", sessionID, pathID, stepID)
}

type memoryIntegrationRepositories struct {
	pool       *pgxpool.Pool
	legacy     *Repository
	gorm       *GORMRepository
	unitOfWork foundation.UnitOfWork
}

type memoryIntegrationCase struct {
	repositories memoryIntegrationRepositories
	offset       int
}

func (testCase memoryIntegrationCase) id(value int) foundation.ID {
	return memoryIntegrationID(testCase.offset + value)
}

type memoryIntegrationVariant struct {
	name       string
	repository func(memoryIntegrationRepositories) memoryapp.Repository
}

func runMemoryIntegrationVariants(t *testing.T, scenario func(*testing.T, context.Context, memoryapp.Repository, memoryIntegrationCase)) {
	t.Helper()
	fixtureCtx, cancelFixture := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelFixture()
	resources := newMemoryIntegrationRepositories(t)
	variants := []memoryIntegrationVariant{
		{name: "legacy-pgx", repository: func(resources memoryIntegrationRepositories) memoryapp.Repository { return resources.legacy }},
		{name: "gorm", repository: func(resources memoryIntegrationRepositories) memoryapp.Repository { return resources.gorm }},
	}
	for index, variant := range variants {
		index := index
		variant := variant
		t.Run(variant.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(fixtureCtx, 45*time.Second)
			defer cancel()
			scenario(t, ctx, variant.repository(resources), memoryIntegrationCase{repositories: resources, offset: (index + 1) * 1000})
		})
	}
}

func newMemoryIntegrationRepositories(t *testing.T) memoryIntegrationRepositories {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{MaxConns: 8, Availability: testdb.FailWhenUnavailable})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("test database fixture did not expose a shared platform pool")
	}
	legacy, err := NewRepository(platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	root, err := platform.GORM()
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork, err := platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	gorm, err := NewGORMRepository(root, unitOfWork)
	if err != nil {
		t.Fatal(err)
	}
	return memoryIntegrationRepositories{pool: platform.DB(), legacy: legacy, gorm: gorm, unitOfWork: unitOfWork}
}

func memoryIntegrationAssertContextClassification(t *testing.T, ctx context.Context, repository memoryapp.Repository, scope memoryapp.Scope, memoryID, missingID foundation.ID) {
	t.Helper()
	if _, err := repository.Get(ctx, scope, missingID); !memoryIntegrationErrorCode(err, domain.ErrorCodeNotFound) {
		t.Fatalf("no-row get error=%v", err)
	}
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repository.Get(cancelledCtx, scope, memoryID); !errors.Is(err, context.Canceled) || !memoryIntegrationErrorCode(err, domain.ErrorCodeUnavailable) {
		t.Fatalf("cancelled get error=%v", err)
	}
	deadlineCtx, cancelDeadline := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancelDeadline()
	if _, err := repository.Get(deadlineCtx, scope, memoryID); !errors.Is(err, context.DeadlineExceeded) || !memoryIntegrationErrorCode(err, domain.ErrorCodeUnavailable) {
		t.Fatalf("deadline get error=%v", err)
	}
}

func memoryIntegrationAssertHistoryWriteRollback(t *testing.T, ctx context.Context, service *memoryapp.Service, pool *pgxpool.Pool, workspaceID foundation.ID, owner domain.Principal) {
	t.Helper()
	for _, history := range []struct {
		name  string
		table string
	}{
		{name: "audit", table: "learning.memory_audit"},
		{name: "receipt", table: "learning.memory_command"},
	} {
		t.Run(history.name, func(t *testing.T) {
			created, err := service.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
				WorkspaceID: workspaceID, Owner: owner, Type: domain.TypePreference,
				Content: json.RawMessage(`{"mode":"rollback"}`), Source: domain.Source{Type: domain.SourceAgent, Ref: "agent:rollback-" + history.name},
				IdempotencyKey: "rollback-" + history.name + "-candidate",
			})
			if err != nil {
				t.Fatal(err)
			}
			lock, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lock.Exec(ctx, "LOCK TABLE "+history.table+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
				_ = lock.Rollback(context.Background())
				t.Fatal(err)
			}
			blockedCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
			_, err = service.Confirm(blockedCtx, memoryapp.TransitionCommand{
				Scope: memoryapp.Scope{WorkspaceID: workspaceID, Owner: owner}, MemoryID: created.Memory.ID, ExpectedVersion: 1,
				IdempotencyKey: "rollback-" + history.name + "-confirm",
			})
			cancel()
			if rollbackErr := lock.Rollback(context.Background()); rollbackErr != nil {
				t.Fatalf("release %s lock: %v", history.name, rollbackErr)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("blocked %s write error=%v", history.name, err)
			}
			var status string
			var version int64
			if err := pool.QueryRow(ctx, `SELECT status,version FROM learning.memory WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(created.Memory.ID)).Scan(&status, &version); err != nil || status != string(domain.StatusCandidate) || version != 1 {
				t.Fatalf("blocked %s aggregate status=%s version=%d err=%v", history.name, status, version, err)
			}
			var commands, audits int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_command WHERE workspace_id=$1 AND memory_id=$2`, string(workspaceID), string(created.Memory.ID)).Scan(&commands); err != nil || commands != 1 {
				t.Fatalf("blocked %s receipt count=%d err=%v", history.name, commands, err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_audit WHERE workspace_id=$1 AND memory_id=$2`, string(workspaceID), string(created.Memory.ID)).Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("blocked %s audit count=%d err=%v", history.name, audits, err)
			}
		})
	}
}

func memoryIntegrationAssertSharedPoolVisibility(t *testing.T, ctx context.Context, resources memoryIntegrationRepositories, visibilityID foundation.ID) {
	t.Helper()
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	if err := resources.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		name := "memory-shared-pool-" + string(visibilityID)
		path := "/tmp/" + name
		if result := transaction.WithContext(callbackCtx).Exec(`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
			VALUES(?,?,?,?,?,'inactive',1,?,?)`, string(visibilityID), name, path, path, now, now, now); result.Error != nil {
			return result.Error
		}
		var visible int
		if err := resources.pool.QueryRow(callbackCtx, `SELECT count(*) FROM core.workspace WHERE id=$1`, string(visibilityID)).Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			return fmt.Errorf("uncommitted GORM write was visible through pgx pool: %d", visible)
		}
		return nil
	}); err != nil {
		t.Fatalf("shared pool transaction visibility: %v", err)
	}
	var committed int
	if err := resources.pool.QueryRow(ctx, `SELECT count(*) FROM core.workspace WHERE id=$1`, string(visibilityID)).Scan(&committed); err != nil || committed != 1 {
		t.Fatalf("committed GORM write count=%d err=%v", committed, err)
	}
}

func memoryIntegrationAssertStaleCAS(t *testing.T, ctx context.Context, repository memoryapp.Repository, service *memoryapp.Service, workspaceID foundation.ID, owner domain.Principal, now time.Time) {
	t.Helper()
	created, err := service.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
		WorkspaceID: workspaceID, Owner: owner, Type: domain.TypePreference,
		Content: json.RawMessage(`{"mode":"stale-cas"}`), Source: domain.Source{Type: domain.SourceAgent, Ref: "agent:stale-cas"},
		IdempotencyKey: "stale-cas-candidate",
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := repository.Get(ctx, memoryapp.Scope{WorkspaceID: workspaceID, Owner: owner}, created.Memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := domain.Confirm(current, owner, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	actor := owner
	record := memoryapp.MutationRecord{
		Binding: memoryapp.CommandBinding{
			WorkspaceID: workspaceID, Owner: owner, MemoryID: current.ID, IdempotencyKey: "stale-cas-first",
			RequestHash: strings.Repeat("a", 64), CommandType: memoryapp.CommandConfirm, ExpectedVersion: current.Version,
		},
		Current: current, Next: next, Action: domain.AuditConfirmed, Actor: &actor,
	}
	if _, err := repository.Mutate(ctx, record); err != nil {
		t.Fatalf("initial CAS mutation: %v", err)
	}
	record.Binding.IdempotencyKey = "stale-cas-second"
	record.Binding.RequestHash = strings.Repeat("b", 64)
	if _, err := repository.Mutate(ctx, record); !memoryIntegrationErrorCode(err, domain.ErrorCodeVersionConflict) {
		t.Fatalf("stale CAS error=%v", err)
	}
}

func memoryIntegrationAssertConcurrentExpiry(t *testing.T, ctx context.Context, service *memoryapp.Service, pool *pgxpool.Pool, workspaceID foundation.ID, owner domain.Principal, now time.Time) {
	t.Helper()
	dueAt := now.Add(time.Minute)
	ids := make([]foundation.ID, 0, 4)
	for index := 0; index < cap(ids); index++ {
		created, err := service.CreateCandidate(ctx, memoryapp.CreateCandidateCommand{
			WorkspaceID: workspaceID, Owner: owner, Type: domain.TypeEpisodic,
			Content: json.RawMessage(fmt.Sprintf(`{"event":"due-%d"}`, index)), Source: domain.Source{Type: domain.SourceAgent, Ref: fmt.Sprintf("agent:due-%d", index)},
			ExpiresAt: &dueAt, IdempotencyKey: fmt.Sprintf("due-candidate-%d", index),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created.Memory.ID)
	}
	type expiryResult struct {
		count int
		err   error
	}
	results := make(chan expiryResult, 2)
	var group sync.WaitGroup
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			count, err := service.ExpireDue(ctx, 2)
			results <- expiryResult{count: count, err: err}
		}()
	}
	group.Wait()
	close(results)
	total := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent expiry: %v", result.err)
		}
		total += result.count
	}
	if total != len(ids) {
		t.Fatalf("concurrent expiry count=%d, want %d", total, len(ids))
	}
	for _, id := range ids {
		var status string
		var version int64
		if err := pool.QueryRow(ctx, `SELECT status,version FROM learning.memory WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(id)).Scan(&status, &version); err != nil || status != string(domain.StatusExpired) || version != 2 {
			t.Fatalf("expired memory id=%s status=%s version=%d err=%v", id, status, version, err)
		}
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.memory_audit WHERE workspace_id=$1 AND action='EXPIRED'`, string(workspaceID)).Scan(&audits); err != nil || audits != len(ids) {
		t.Fatalf("expiry audit count=%d err=%v", audits, err)
	}
	if count, err := service.ExpireDue(ctx, domain.MaxListLimit); err != nil || count != 0 {
		t.Fatalf("repeated expiry count=%d err=%v", count, err)
	}
}

func seedMemoryWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id foundation.ID, name string) {
	t.Helper()
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	name += "-" + string(id)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(id), name, "/tmp/"+name, now); err != nil {
		t.Fatal(err)
	}
}

type memoryIntegrationIDs struct {
	base int
	next int
}

func (ids *memoryIntegrationIDs) New() (foundation.ID, error) {
	ids.next++
	return memoryIntegrationID(ids.base + ids.next), nil
}

type concurrentMemoryIntegrationIDs struct {
	mu   sync.Mutex
	base int
	next int
}

func (ids *concurrentMemoryIntegrationIDs) New() (foundation.ID, error) {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.next++
	return memoryIntegrationID(ids.base + ids.next), nil
}

func memoryIntegrationID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("40000000-0000-4000-8000-%012d", value))
}

func memoryIntegrationErrorCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}

func memoryIntegrationPostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != code {
		t.Fatalf("postgres error=%v, want code=%s", err, code)
	}
}
