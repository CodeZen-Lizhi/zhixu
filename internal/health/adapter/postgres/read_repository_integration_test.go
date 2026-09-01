//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReadRepositoryIssueHistoryIsBoundedStableAndUsesFixedStatements(t *testing.T) {
	ctx := context.Background()
	pool := newHealthIntegrationPool(t)

	workspaceID := newHealthReadTestID(t)
	topicID := newHealthReadTestID(t)
	definitionID := newHealthReadTestID(t)
	runID := newHealthReadTestID(t)
	scanID := newHealthReadTestID(t)
	issueID := newHealthReadTestID(t)
	cleanupHealthIntegrationWorkspace(t, pool, workspaceID)
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })
	now := time.Now().UTC().Truncate(time.Microsecond)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	seedBoundedHistoryFacts(t, ctx, tx, workspaceID, topicID, definitionID, runID, scanID, now)
	seedRepository, err := NewIssueRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := seedRepository.UpsertObservation(ctx, workspaceID, scanID, issueID, repositoryObservation(topicID, strings.Repeat("f", 64), "current bounded history observation"), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	historyStart := now.Add(time.Hour)
	for index := range 260 {
		eventAt := historyStart.Add(time.Duration(index/2) * time.Microsecond)
		current, err = seedRepository.ApplyDecision(ctx, workspaceID, issueID, domain.IssueDecision{
			ExpectedVersion: current.Version,
			IdempotencyKey:  fmt.Sprintf("bounded-history-%03d", index),
			Action:          domain.IssueDecisionAcknowledge,
		}, eventAt, nil)
		if err != nil {
			t.Fatalf("decision %d: %v", index, err)
		}
		changed := repositoryObservation(topicID, fmt.Sprintf("%064x", index+1), "bounded history evidence")
		current, _, err = seedRepository.UpsertObservation(ctx, workspaceID, scanID, issueID, changed, nil, eventAt)
		if err != nil {
			t.Fatalf("observation %d: %v", index, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE ops.health_issue_observation; ANALYZE ops.health_issue_decision; ANALYZE ops.health_issue_evidence`); err != nil {
		t.Fatal(err)
	}
	planCursorAt := historyStart.Add(100 * time.Microsecond)
	assertHealthHistoryIndexPlan(t, ctx, pool,
		`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+issueObservationHistorySelect+` AND observation.observed_at <= $3 AND (observation.observed_at < $3 OR (observation.observed_at = $3 AND observation.id > $4)) ORDER BY observation.observed_at DESC,observation.id ASC LIMIT $5`,
		[]any{string(workspaceID), string(issueID), planCursorAt, string(issueID), 26}, "health_issue_observation", "idx_ops_health_issue_observation_issue_observed", "observed_at")
	assertHealthHistoryIndexPlan(t, ctx, pool,
		`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+issueDecisionHistorySelect+` AND decision.created_at <= $3 AND (decision.created_at < $3 OR (decision.created_at = $3 AND decision.id > $4)) ORDER BY decision.created_at DESC,decision.id ASC LIMIT $5`,
		[]any{string(workspaceID), string(issueID), planCursorAt, string(issueID), 26}, "health_issue_decision", "idx_ops_health_issue_decision_issue_created", "created_at")
	evidenceIDs := healthHistoryObservationIDs(t, ctx, pool, workspaceID, issueID, 1)
	assertHealthHistoryIndexPlan(t, ctx, pool,
		`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+issueObservationEvidenceSelect,
		[]any{string(workspaceID), evidenceIDs}, "health_issue_evidence", "idx_ops_health_issue_evidence_observation", "")

	recorder := &healthStatementRecorder{}
	readRepository, err := NewReadRepository(&healthStatementReadDB{ReadDB: pool, recorder: recorder})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := healthapp.NewIssueCursorCodec([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	service, err := healthapp.NewIssueReadService(readRepository, codec)
	if err != nil {
		t.Fatal(err)
	}

	observationIDs := make(map[foundation.ID]struct{}, 261)
	observationCursor := ""
	for {
		recorder.reset()
		pageCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		page, err := service.ListIssueObservations(pageCtx, healthapp.IssueHistoryRequest{WorkspaceID: workspaceID, IssueID: issueID, Limit: 25, Cursor: observationCursor})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if statements := recorder.snapshot(); len(statements) != 3 {
			t.Fatalf("observation statements=%d want=3\n%s", len(statements), strings.Join(statements, "\n---\n"))
		}
		for _, item := range page.Items {
			if _, duplicate := observationIDs[item.ID]; duplicate {
				t.Fatalf("duplicate observation %s", item.ID)
			}
			observationIDs[item.ID] = struct{}{}
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" {
			t.Fatal("observation page omitted cursor")
		}
		observationCursor = page.NextCursor
	}
	if len(observationIDs) != 261 {
		t.Fatalf("observation count=%d want=261", len(observationIDs))
	}

	decisionIDs := make(map[foundation.ID]struct{}, 260)
	decisionCursor := ""
	for {
		recorder.reset()
		pageCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		page, err := service.ListIssueDecisions(pageCtx, healthapp.IssueHistoryRequest{WorkspaceID: workspaceID, IssueID: issueID, Limit: 25, Cursor: decisionCursor})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if statements := recorder.snapshot(); len(statements) != 2 {
			t.Fatalf("decision statements=%d want=2\n%s", len(statements), strings.Join(statements, "\n---\n"))
		}
		for _, item := range page.Items {
			if _, duplicate := decisionIDs[item.ID]; duplicate {
				t.Fatalf("duplicate decision %s", item.ID)
			}
			decisionIDs[item.ID] = struct{}{}
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" {
			t.Fatal("decision page omitted cursor")
		}
		decisionCursor = page.NextCursor
	}
	if len(decisionIDs) != 260 {
		t.Fatalf("decision count=%d want=260", len(decisionIDs))
	}

	recorder.reset()
	detailCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	detail, err := service.GetIssueDetail(detailCtx, workspaceID, issueID)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Observations) != healthapp.DefaultIssueHistoryLimit || !detail.ObservationsHasMore || detail.ObservationsNextCursor == "" ||
		len(detail.Decisions) != healthapp.DefaultIssueHistoryLimit || !detail.DecisionsHasMore || detail.DecisionsNextCursor == "" {
		t.Fatalf("detail observation=%d/%v decision=%d/%v", len(detail.Observations), detail.ObservationsHasMore, len(detail.Decisions), detail.DecisionsHasMore)
	}
	if detail.LatestObservation.Fingerprint != current.Fingerprint || detail.Issue.Fingerprint != current.Fingerprint ||
		len(detail.LatestObservation.Evidence) != len(current.Evidence) || len(detail.LatestObservation.TargetVersions) != len(current.ObjectVersions) {
		t.Fatalf("detail current observation is stale: issue=%s latest=%s current=%s", detail.Issue.Fingerprint, detail.LatestObservation.Fingerprint, current.Fingerprint)
	}
	if statements := recorder.snapshot(); len(statements) != 4 {
		t.Fatalf("detail statements=%d want=4\n%s", len(statements), strings.Join(statements, "\n---\n"))
	}
}

type healthStatementReadDB struct {
	ReadDB
	recorder *healthStatementRecorder
}

func (database *healthStatementReadDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	database.recorder.record(query)
	return database.ReadDB.Query(ctx, query, args...)
}

func (database *healthStatementReadDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	database.recorder.record(query)
	return database.ReadDB.QueryRow(ctx, query, args...)
}

type healthHistoryExplainPlan struct {
	NodeType     string                     `json:"Node Type"`
	RelationName string                     `json:"Relation Name"`
	IndexName    string                     `json:"Index Name"`
	IndexCond    string                     `json:"Index Cond"`
	Plans        []healthHistoryExplainPlan `json:"Plans"`
}

func assertHealthHistoryIndexPlan(t *testing.T, ctx context.Context, database ReadDB, query string, args []any, relation, index, rangeColumn string) {
	t.Helper()
	var raw []byte
	if err := database.QueryRow(ctx, query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []struct {
		Plan healthHistoryExplainPlan `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid health history explain: %v %s", err, raw)
	}
	foundIndex, foundRange, foundSequentialScan := false, rangeColumn == "", false
	visitHealthHistoryPlan(documents[0].Plan, func(node healthHistoryExplainPlan) {
		if node.IndexName == index {
			foundIndex = true
			if strings.Contains(node.IndexCond, rangeColumn) {
				foundRange = true
			}
		}
		if node.NodeType == "Seq Scan" && node.RelationName == relation {
			foundSequentialScan = true
		}
	})
	if !foundIndex || !foundRange || foundSequentialScan {
		t.Fatalf("health history plan index=%v range=%v seq_scan=%v expected=%s plan=%s", foundIndex, foundRange, foundSequentialScan, index, raw)
	}
}

func visitHealthHistoryPlan(plan healthHistoryExplainPlan, visit func(healthHistoryExplainPlan)) {
	visit(plan)
	for _, child := range plan.Plans {
		visitHealthHistoryPlan(child, visit)
	}
}

func healthHistoryObservationIDs(t *testing.T, ctx context.Context, database ReadDB, workspaceID, issueID foundation.ID, limit int) []string {
	t.Helper()
	rows, err := database.Query(ctx, `SELECT observation.id::text FROM ops.health_issue_observation observation WHERE observation.workspace_id=$1 AND observation.issue_id=$2 ORDER BY observation.observed_at DESC,observation.id ASC LIMIT $3`, string(workspaceID), string(issueID), limit)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		result = append(result, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(result) != limit {
		t.Fatalf("health history explain ids=%d want=%d", len(result), limit)
	}
	return result
}

func seedBoundedHistoryFacts(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, topicID, definitionID, runID, scanID foundation.ID, now time.Time) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{query: `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
VALUES($1,'health-bounded-history',$2,$2,$3,'inactive',1,$3,$3)`, args: []any{string(workspaceID), "/tmp/health-bounded-history-" + string(workspaceID), now}},
		{query: `INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
VALUES($1,$2,'health bounded history','health bounded history','','ACTIVE',1,$3,$3)`, args: []any{string(topicID), string(workspaceID), now}},
		{query: `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
VALUES($1,$2,'health-bounded-history',1,'{"nodes":[]}',$3)`, args: []any{string(definitionID), string(workspaceID), now}},
		{query: `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
VALUES($1,$2,$3,'running','{}',1,$4,$4)`, args: []any{string(runID), string(workspaceID), string(definitionID), now}},
		{query: `INSERT INTO ops.health_scan(id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,version,created_at,updated_at)
VALUES($1,$2,'WORKSPACE',$2,1,'health-scope/workspace/v1',$3,'health-bounded-history-scan',$4,$5,100,'RUNNING',1,$6,$6)`, args: []any{string(scanID), string(workspaceID), strings.Repeat("c", 64), strings.Repeat("d", 64), string(runID), now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

type healthTrendCountingDB struct {
	*pgxpool.Pool
	trendQueries int
	queries      int
	queryRows    int
}

func (database *healthTrendCountingDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	database.queries++
	if strings.Contains(query, "WITH utc_days AS") {
		database.trendQueries++
	}
	return database.Pool.Query(ctx, query, args...)
}

func (database *healthTrendCountingDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	database.queryRows++
	return database.Pool.QueryRow(ctx, query, args...)
}

func seedHealthReadWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	root := "/tmp/health-read-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
	VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(workspaceID), "health-read-"+string(workspaceID), root, now); err != nil {
		t.Fatal(err)
	}
	definitionID := newHealthReadTestID(t)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
	VALUES($1,$2,'health-read-test',1,'{"nodes":[]}',$3)`, string(definitionID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
}

func seedHealthTrendScan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, occurredAt time.Time, status string, created, reopened, resolved int64) {
	t.Helper()
	scanID := seedPendingHealthTrendScan(t, ctx, pool, workspaceID, occurredAt.Add(-time.Minute))
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan SET status='RUNNING',version=2,updated_at=$2 WHERE id=$1`, string(scanID), occurredAt.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	var failure any
	if status == "FAILED" {
		failure = `{"stage":"detector","code":"HEALTH_TREND_TEST_FAILURE","retryable":false}`
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan SET status=$2,created_count=$3,reopened_count=$4,resolved_count=$5,
	last_error=$6::jsonb,version=3,updated_at=$7,completed_at=$7 WHERE id=$1`, string(scanID), status, created, reopened, resolved, failure, occurredAt); err != nil {
		t.Fatal(err)
	}
}

func seedActiveHealthTrendScan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, updatedAt time.Time, created int64) {
	t.Helper()
	scanID := seedPendingHealthTrendScan(t, ctx, pool, workspaceID, updatedAt.Add(-time.Minute))
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan SET status='RUNNING',created_count=$2,version=2,updated_at=$3 WHERE id=$1`, string(scanID), created, updatedAt); err != nil {
		t.Fatal(err)
	}
}

func seedPendingHealthTrendScan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, createdAt time.Time) foundation.ID {
	t.Helper()
	scanID := newHealthReadTestID(t)
	runID := newHealthReadTestID(t)
	var definitionID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM workflow.definition WHERE workspace_id=$1 AND key='health-read-test' AND version=1`, string(workspaceID)).Scan(&definitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
	VALUES($1,$2,$3,'running','{}',1,$4,$4)`, string(runID), string(workspaceID), definitionID, createdAt); err != nil {
		t.Fatal(err)
	}
	fingerprintValue := sha256.Sum256([]byte("fingerprint:" + string(scanID)))
	requestHashValue := sha256.Sum256([]byte("request:" + string(scanID)))
	fingerprint := hex.EncodeToString(fingerprintValue[:])
	requestHash := hex.EncodeToString(requestHashValue[:])
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_scan(
	id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,fingerprint,idempotency_key,request_hash,
	workflow_run_id,max_items,status,version,created_at,updated_at)
	VALUES($1,$2,'WORKSPACE',$2,1,'health-scope/workspace/v1',$3,$4,$5,$6,100,'PENDING',1,$7,$7)`,
		string(scanID), string(workspaceID), fingerprint, "health-trend-"+string(scanID), requestHash, string(runID), createdAt); err != nil {
		t.Fatal(err)
	}
	return scanID
}

func newHealthReadTestID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
