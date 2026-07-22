//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIssueRepositoryObservationReopenDecisionAndHistory(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	workspaceID := repositoryTestID(t, "71000000-0000-4000-8000-000000000001")
	cleanupHealthIntegrationWorkspace(t, pool, workspaceID)
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now().UTC().Truncate(time.Microsecond)
	topicID := repositoryTestID(t, "71000000-0000-4000-8000-000000000002")
	definitionID := repositoryTestID(t, "71000000-0000-4000-8000-000000000003")
	runID := repositoryTestID(t, "71000000-0000-4000-8000-000000000004")
	scanID := repositoryTestID(t, "71000000-0000-4000-8000-000000000005")
	issueID := repositoryTestID(t, "71000000-0000-4000-8000-000000000006")
	seedIssueRepositoryFacts(t, ctx, tx, workspaceID, topicID, definitionID, runID, scanID, now)
	ids := &repositorySequenceIDs{values: []foundation.ID{
		repositoryTestID(t, "72000000-0000-4000-8000-000000000001"),
		repositoryTestID(t, "72000000-0000-4000-8000-000000000002"),
		repositoryTestID(t, "72000000-0000-4000-8000-000000000003"),
		repositoryTestID(t, "72000000-0000-4000-8000-000000000004"),
		repositoryTestID(t, "72000000-0000-4000-8000-000000000005"),
		repositoryTestID(t, "72000000-0000-4000-8000-000000000006"),
		repositoryTestID(t, "72000000-0000-4000-8000-000000000007"),
	}}
	repository, err := NewIssueRepository(tx, ids)
	if err != nil {
		t.Fatal(err)
	}
	observation := repositoryObservation(topicID, strings.Repeat("a", 64), "topic is missing a supporting source")
	created, _, err := repository.UpsertObservation(ctx, workspaceID, scanID, issueID, observation, nil, now)
	if err != nil || created.ID != issueID || created.Version != 1 || created.Status != domain.IssueStatusOpen {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	unchangedAt := now.Add(time.Minute)
	unchanged, outcome, err := repository.UpsertObservation(ctx, workspaceID, scanID, repositoryTestID(t, "71000000-0000-4000-8000-000000000099"), observation, nil, unchangedAt)
	if err != nil || outcome != domain.ObservationOutcomeUnchanged || unchanged.ID != issueID || unchanged.Version != created.Version || !unchanged.LastVerifiedAt.Equal(unchangedAt) || !unchanged.LastDetectedAt.Equal(created.LastDetectedAt) || !unchanged.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("unchanged=%#v outcome=%s err=%v", unchanged, outcome, err)
	}
	metadataChanged := observation
	metadataChanged.Severity = domain.SeverityMedium
	if _, _, err := repository.UpsertObservation(ctx, workspaceID, scanID, repositoryTestID(t, "71000000-0000-4000-8000-000000000098"), metadataChanged, nil, now.Add(90*time.Second)); err == nil {
		t.Fatal("same fingerprint with changed severity unexpectedly succeeded")
	}
	changed := repositoryObservation(topicID, strings.Repeat("b", 64), "topic provenance changed and is still missing")
	reopened, outcome, err := repository.UpsertObservation(ctx, workspaceID, scanID, issueID, changed, nil, now.Add(2*time.Minute))
	if err != nil || outcome != domain.ObservationOutcomeReopened || reopened.Status != domain.IssueStatusReopened || reopened.Version != created.Version+1 || reopened.Fingerprint == created.Fingerprint {
		t.Fatalf("reopened=%#v outcome=%s err=%v", reopened, outcome, err)
	}
	loaded, err := repository.GetIssue(ctx, workspaceID, issueID, nil)
	if err != nil || loaded.Fingerprint != reopened.Fingerprint || len(loaded.Evidence) != 1 || loaded.Evidence[0].Hash != strings.Repeat("b", 64) || len(loaded.RepairOptions) != 1 || loaded.RepairOptions[0].Available || loaded.RepairOptions[0].UnavailableReason == "" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	availableRepairOptions := []domain.RepairOption{{Code: "health.repair.verify-provenance", Title: "绑定来源并重新验证", Available: true}}
	proposalID := repositoryTestID(t, "71000000-0000-4000-8000-000000000008")
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal(id,workspace_id,proposal_type,status,idempotency_key,request_hash,version,created_at,updated_at) VALUES($1,$2,'knowledge_change','ready_for_review','health-repair-proposal',repeat('e',64),1,$3,$3)`, string(proposalID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	proposalDecision := domain.IssueDecision{ExpectedVersion: reopened.Version, IdempotencyKey: "health-proposal-1", Action: domain.IssueDecisionCreateRepairProposal, ProposalID: &proposalID, RepairOptionCode: availableRepairOptions[0].Code}
	proposalCreated, err := repository.ApplyDecision(ctx, workspaceID, issueID, proposalDecision, now.Add(3*time.Minute), availableRepairOptions)
	if err != nil || proposalCreated.Status != domain.IssueStatusProposalCreated || proposalCreated.Version != reopened.Version+1 {
		t.Fatalf("proposal created=%#v err=%v", proposalCreated, err)
	}
	proposalLoaded, err := repository.GetIssue(ctx, workspaceID, issueID, availableRepairOptions)
	if err != nil || proposalLoaded.Proposal == nil || proposalLoaded.Proposal.ProposalID != proposalID || proposalLoaded.Proposal.RepairOptionCode != availableRepairOptions[0].Code || proposalLoaded.Proposal.Fingerprint != reopened.Fingerprint || len(proposalLoaded.Proposal.ObjectVersions) != 1 {
		t.Fatalf("proposal loaded=%#v err=%v", proposalLoaded, err)
	}
	if err := domain.ValidateIssue(proposalLoaded); err != nil {
		t.Fatalf("proposal round trip is invalid: %v", err)
	}
	decision := domain.IssueDecision{ExpectedVersion: proposalCreated.Version, IdempotencyKey: "health-ignore-1", Action: domain.IssueDecisionIgnore, Reason: "confirmed acceptable exception"}
	ignored, err := repository.ApplyDecision(ctx, workspaceID, issueID, decision, now.Add(4*time.Minute), availableRepairOptions)
	if err != nil || ignored.Status != domain.IssueStatusIgnored || ignored.Version != proposalCreated.Version+1 {
		t.Fatalf("ignored=%#v err=%v", ignored, err)
	}
	replayed, err := repository.ApplyDecision(ctx, workspaceID, issueID, decision, now.Add(5*time.Minute), availableRepairOptions)
	if err != nil || replayed.Status != domain.IssueStatusIgnored || replayed.Version != ignored.Version {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	conflicting := decision
	conflicting.Reason = "different request"
	if _, err := repository.ApplyDecision(ctx, workspaceID, issueID, conflicting, now.Add(5*time.Minute), nil); err == nil {
		t.Fatal("conflicting idempotency key unexpectedly succeeded")
	}
	stale := decision
	stale.IdempotencyKey = "health-ignore-stale"
	if _, err := repository.ApplyDecision(ctx, workspaceID, issueID, stale, now.Add(5*time.Minute), nil); err == nil {
		t.Fatal("stale expected version unexpectedly succeeded")
	}
	var observations, evidence, decisions int
	if err := tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM ops.health_issue_observation WHERE workspace_id=$1 AND issue_id=$2),
 (SELECT count(*) FROM ops.health_issue_evidence e JOIN ops.health_issue_observation o ON o.id=e.observation_id WHERE o.workspace_id=$1 AND o.issue_id=$2),
 (SELECT count(*) FROM ops.health_issue_decision WHERE workspace_id=$1 AND issue_id=$2)`, string(workspaceID), string(issueID)).Scan(&observations, &evidence, &decisions); err != nil {
		t.Fatal(err)
	}
	if observations != 2 || evidence != 2 || decisions != 2 {
		t.Fatalf("history observations=%d evidence=%d decisions=%d", observations, evidence, decisions)
	}
	otherWorkspace := repositoryTestID(t, "71000000-0000-4000-8000-000000000007")
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'health-other',$2,$2,$3,'test',1,$3,$3)`, string(otherWorkspace), "/tmp/health-other-"+string(otherWorkspace), now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetIssue(ctx, otherWorkspace, issueID, nil); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross workspace read err=%v", err)
	}
}

func TestIssueRepositoryConcurrentDecisionReplaysWinner(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	workspaceID := repositoryTestID(t, "73000000-0000-4000-8000-000000000001")
	cleanupHealthIntegrationWorkspace(t, pool, workspaceID)
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })
	now := time.Now().UTC().Truncate(time.Microsecond)
	topicID := repositoryTestID(t, "73000000-0000-4000-8000-000000000002")
	definitionID := repositoryTestID(t, "73000000-0000-4000-8000-000000000003")
	runID := repositoryTestID(t, "73000000-0000-4000-8000-000000000004")
	scanID := repositoryTestID(t, "73000000-0000-4000-8000-000000000005")
	issueID := repositoryTestID(t, "73000000-0000-4000-8000-000000000006")
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seedTx.Rollback(context.Background()) }()
	seedIssueRepositoryFacts(t, ctx, seedTx, workspaceID, topicID, definitionID, runID, scanID, now)
	seedRepository, err := NewIssueRepository(seedTx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := seedRepository.UpsertObservation(ctx, workspaceID, scanID, issueID, repositoryObservation(topicID, strings.Repeat("f", 64), "topic is missing a supporting source"), nil, now); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	decision := domain.IssueDecision{ExpectedVersion: 1, IdempotencyKey: "health-concurrent-ignore", Action: domain.IssueDecisionIgnore, Reason: "confirmed acceptable exception"}
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			repository, repositoryErr := NewIssueRepository(pool)
			if repositoryErr != nil {
				results <- repositoryErr
				return
			}
			<-start
			issue, applyErr := repository.ApplyDecision(ctx, workspaceID, issueID, decision, now.Add(time.Minute), nil)
			if applyErr == nil && (issue.Status != domain.IssueStatusIgnored || issue.Version != 2) {
				applyErr = errors.New("concurrent decision returned an invalid replay projection")
			}
			results <- applyErr
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent exact replay failed: %v", err)
		}
	}
	var decisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.health_issue_decision WHERE workspace_id=$1 AND issue_id=$2`, string(workspaceID), string(issueID)).Scan(&decisions); err != nil || decisions != 1 {
		t.Fatalf("decision receipts=%d err=%v", decisions, err)
	}
}

func TestIssueRepositoryResolveMissingFinalizesOnlyAutoResolvableStatusesAndCounters(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	workspaceID := repositoryTestID(t, "74000000-0000-4000-8000-000000000001")
	cleanupHealthIntegrationWorkspace(t, pool, workspaceID)
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now().UTC().Truncate(time.Microsecond)
	topicIDs := []foundation.ID{
		repositoryTestID(t, "74000000-0000-4000-8000-000000000011"),
		repositoryTestID(t, "74000000-0000-4000-8000-000000000012"),
		repositoryTestID(t, "74000000-0000-4000-8000-000000000013"),
		repositoryTestID(t, "74000000-0000-4000-8000-000000000014"),
		repositoryTestID(t, "74000000-0000-4000-8000-000000000015"),
		repositoryTestID(t, "74000000-0000-4000-8000-000000000016"),
		repositoryTestID(t, "74000000-0000-4000-8000-000000000017"),
	}
	definitionID := repositoryTestID(t, "74000000-0000-4000-8000-000000000003")
	runID := repositoryTestID(t, "74000000-0000-4000-8000-000000000004")
	scanID := repositoryTestID(t, "74000000-0000-4000-8000-000000000005")
	seedIssueRepositoryFacts(t, ctx, tx, workspaceID, topicIDs[0], definitionID, runID, scanID, now)
	for index, topicID := range topicIDs[1:] {
		if _, err := tx.Exec(ctx, `INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,'','ACTIVE',1,$4,$4)`, string(topicID), string(workspaceID), "health topic "+string(rune('b'+index)), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.health_scan_detector(scan_id,workspace_id,detector_id,detector_version,status,checkpoint,processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count) VALUES($1,$2,'health.detector.missing_source','detector/v1','PENDING','{}',0,0,0,0,0,0)`, string(scanID), string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.health_scan_detector SET status='RUNNING',started_at=$3 WHERE scan_id=$1 AND workspace_id=$2`, string(scanID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.health_scan_detector SET status='SUCCEEDED',completed_at=$3 WHERE scan_id=$1 AND workspace_id=$2`, string(scanID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}

	repository, err := NewIssueRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	statuses := []domain.IssueStatus{domain.IssueStatusOpen, domain.IssueStatusAcknowledged, domain.IssueStatusDeferred, domain.IssueStatusProposalCreated, domain.IssueStatusIgnored, domain.IssueStatusFalsePositive, domain.IssueStatusReopened}
	issueIDs := []foundation.ID{
		repositoryTestID(t, "75000000-0000-4000-8000-000000000001"),
		repositoryTestID(t, "75000000-0000-4000-8000-000000000002"),
		repositoryTestID(t, "75000000-0000-4000-8000-000000000003"),
		repositoryTestID(t, "75000000-0000-4000-8000-000000000004"),
		repositoryTestID(t, "75000000-0000-4000-8000-000000000005"),
		repositoryTestID(t, "75000000-0000-4000-8000-000000000006"),
		repositoryTestID(t, "75000000-0000-4000-8000-000000000007"),
	}
	options := []domain.RepairOption{{Code: "health.repair.verify-provenance", Title: "绑定来源并重新验证", Available: true}}
	for index, status := range statuses {
		issueID := issueIDs[index]
		observation := repositoryObservation(topicIDs[index], strings.Repeat(string(rune('1'+index)), 64), "topic is missing a supporting source")
		issue, _, err := repository.UpsertObservation(ctx, workspaceID, scanID, issueID, observation, options, now.Add(time.Duration(index+1)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if status == domain.IssueStatusOpen {
			continue
		}
		if status == domain.IssueStatusReopened {
			changed := repositoryObservation(topicIDs[index], strings.Repeat("e", 64), "topic provenance changed and is still missing")
			reopened, outcome, reopenErr := repository.UpsertObservation(ctx, workspaceID, scanID, issueID, changed, options, now.Add(20*time.Second+time.Duration(index)*time.Second))
			if reopenErr != nil || outcome != domain.ObservationOutcomeReopened || reopened.Status != domain.IssueStatusReopened {
				t.Fatalf("reopened=%#v outcome=%s err=%v", reopened, outcome, reopenErr)
			}
			continue
		}
		decision := domain.IssueDecision{ExpectedVersion: issue.Version, IdempotencyKey: "resolve-status-" + string(status)}
		switch status {
		case domain.IssueStatusAcknowledged:
			decision.Action = domain.IssueDecisionAcknowledge
		case domain.IssueStatusDeferred:
			decision.Action, decision.Reason = domain.IssueDecisionDefer, "check after source review"
			deferUntil := now.Add(time.Hour)
			decision.DeferredUntil = &deferUntil
		case domain.IssueStatusProposalCreated:
			proposalID := repositoryTestID(t, "76000000-0000-4000-8000-000000000001")
			if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal(id,workspace_id,proposal_type,status,idempotency_key,request_hash,version,created_at,updated_at) VALUES($1,$2,'knowledge_change','ready_for_review','resolve-status-proposal',repeat('a',64),1,$3,$3)`, string(proposalID), string(workspaceID), now); err != nil {
				t.Fatal(err)
			}
			decision.Action, decision.ProposalID, decision.RepairOptionCode = domain.IssueDecisionCreateRepairProposal, &proposalID, options[0].Code
		case domain.IssueStatusIgnored:
			decision.Action, decision.Reason = domain.IssueDecisionIgnore, "confirmed acceptable exception"
		case domain.IssueStatusFalsePositive:
			decision.Action, decision.Reason = domain.IssueDecisionFalsePositive, "detector does not apply"
		}
		if _, err := repository.ApplyDecision(ctx, workspaceID, issueID, decision, now.Add(30*time.Second+time.Duration(index)*time.Second), options); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ops.health_scan_seen_identity WHERE scan_id=$1`, string(scanID)); err != nil {
		t.Fatal(err)
	}
	resolved, err := repository.ResolveMissingForCompleteScan(ctx, workspaceID, scanID, "health.detector.missing_source")
	if err != nil || resolved != 3 {
		t.Fatalf("resolved=%d err=%v", resolved, err)
	}
	var unresolved int
	var silentStatuses int
	var scanResolved, coverageResolved int64
	if err := tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM ops.health_issue WHERE workspace_id=$1 AND detector_id='health.detector.missing_source' AND status<>'RESOLVED'),
 (SELECT count(*) FROM ops.health_issue WHERE workspace_id=$1 AND detector_id='health.detector.missing_source' AND status IN ('DEFERRED','PROPOSAL_CREATED','IGNORED','FALSE_POSITIVE')),
	 (SELECT resolved_count FROM ops.health_scan WHERE id=$2),
	 (SELECT resolved_count FROM ops.health_scan_detector WHERE scan_id=$2 AND detector_id='health.detector.missing_source')`, string(workspaceID), string(scanID)).Scan(&unresolved, &silentStatuses, &scanResolved, &coverageResolved); err != nil {
		t.Fatal(err)
	}
	if unresolved != 4 || silentStatuses != 4 || scanResolved != resolved || coverageResolved != resolved {
		t.Fatalf("unresolved=%d silent=%d scan_resolved=%d coverage_resolved=%d", unresolved, silentStatuses, scanResolved, coverageResolved)
	}
}

func seedIssueRepositoryFacts(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, topicID, definitionID, runID, scanID foundation.ID, now time.Time) {
	t.Helper()
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'health-repository',$2,$2,$3,'test',1,$3,$3)`, []any{string(workspaceID), "/tmp/health-repository-" + string(workspaceID), now}},
		{`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,'health topic','health topic','','ACTIVE',1,$3,$3)`, []any{string(topicID), string(workspaceID), now}},
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,'health-issue-test',1,'{"nodes":[]}', $3)`, []any{string(definitionID), string(workspaceID), now}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'running','{}',1,$4,$4)`, []any{string(runID), string(workspaceID), string(definitionID), now}},
		{`INSERT INTO ops.health_scan(id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,version,created_at,updated_at) VALUES($1,$2,'WORKSPACE',$2,1,'health-scope/workspace/v1',$3,'health-issue-scan',$4,$5,100,'RUNNING',1,$6,$6)`, []any{string(scanID), string(workspaceID), strings.Repeat("c", 64), strings.Repeat("d", 64), string(runID), now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func repositoryObservation(topicID foundation.ID, hash, summary string) domain.IssueObservation {
	ref := domain.ObjectRef{Type: domain.ObjectTypeTopic, ID: topicID}
	return domain.IssueObservation{Type: domain.IssueTypeMissingSource, Target: ref, DetectorID: "health.detector.missing_source", DetectorVersion: "detector/v1", Severity: domain.SeverityHigh, EvidenceSummary: summary, Evidence: []domain.IssueEvidence{{Ref: ref, Hash: hash, Summary: summary}}, ObjectVersions: []domain.ObjectVersion{{Ref: ref, Version: 1}}}
}

type repositorySequenceIDs struct {
	values []foundation.ID
	index  int
}

func (generator *repositorySequenceIDs) New() (foundation.ID, error) {
	if generator.index >= len(generator.values) {
		return "", errors.New("repository test ids exhausted")
	}
	value := generator.values[generator.index]
	generator.index++
	return value, nil
}

func repositoryTestID(t *testing.T, value string) foundation.ID {
	t.Helper()
	id, err := foundation.ParseID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
