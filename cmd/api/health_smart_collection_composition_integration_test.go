//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	collectionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectiondomain "github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	collectionhttp "github.com/CodeZen-Lizhi/zhixu/internal/collection/http"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphpostgres "github.com/CodeZen-Lizhi/zhixu/internal/graph/adapter/postgres"
	healthpostgres "github.com/CodeZen-Lizhi/zhixu/internal/health/adapter/postgres"
	healthdomain "github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	healthhttp "github.com/CodeZen-Lizhi/zhixu/internal/health/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAPIHealthSmartCollectionCompositionStartsAndRejectsStaleBinding(t *testing.T) {
	database := newMigratedAPIHealthTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	workspaceID, topicID := seedAPIHealthCollectionFixture(t, ctx, database.DB())
	service := newAPIHealthCollectionService(t, database.DB())
	created, err := service.Create(ctx, collectionapp.CreateCommand{
		WorkspaceID: workspaceID, Name: "API Health Collection", Query: topicCollectionQuery(),
		ViewType: collectiondomain.ViewTypeList, IdempotencyKey: "api-health-collection-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := service.PlanDurableScan(ctx, workspaceID, created.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}

	events, err := eventspostgres.NewStore(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	changeControlRepository, err := changecontrolpostgres.NewRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	healthCancellationGuard, err := healthpostgres.NewScanCancellationGuard(events)
	if err != nil {
		t.Fatal(err)
	}
	cancellationGuard, err := workflowapp.NewCompositeCancellationSafetyGuard(
		changeControlRepository,
		graphpostgres.NewSemanticLinkScanCancellationGuard(),
		healthCancellationGuard,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, runtime, err := newWorkflowComponents(database.DB(), config.Defaults(), cancellationGuard)
	if err != nil {
		t.Fatal(err)
	}
	collectionHandler := collectionhttp.NewHandler(nil, time.Second)
	healthHandler := healthhttp.NewHandler(nil, nil, nil, nil, nil)
	if err := configureCollectionHealth(database, config.Defaults(), &collectionHandler, &healthHandler, runtime); err != nil {
		t.Fatal(err)
	}
	if !collectionHandler.Available() || !healthHandler.Available() {
		t.Fatal("production collection/health handlers are unavailable")
	}
	router := chi.NewRouter()
	router.Route("/api/v1", healthHandler.Routes)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	requestBody := smartCollectionHealthStartBody(t, workspaceID, binding)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/health/scans", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "api-health-smart-start")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("smart-collection start status=%d", response.StatusCode)
	}
	var started struct {
		HealthScanID  string `json:"health_scan_id"`
		WorkflowRunID string `json:"workflow_run_id"`
		StatusURL     string `json:"status_url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	if started.HealthScanID == "" || started.WorkflowRunID == "" || !strings.Contains(started.StatusURL, string(workspaceID)) {
		t.Fatalf("start response=%+v", started)
	}

	if _, err := database.DB().Exec(ctx, `UPDATE core.topic SET version=version+1,updated_at=updated_at+interval '1 second' WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(topicID)); err != nil {
		t.Fatal(err)
	}
	staleRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/health/scans", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	staleRequest.Header.Set("Content-Type", "application/json")
	staleRequest.Header.Set("Idempotency-Key", "api-health-smart-stale")
	staleResponse, err := server.Client().Do(staleRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer staleResponse.Body.Close()
	if staleResponse.StatusCode != http.StatusConflict {
		t.Fatalf("stale smart-collection start status=%d", staleResponse.StatusCode)
	}
	var problem struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.NewDecoder(staleResponse.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.ErrorCode != healthdomain.ErrorCodeScanScopeStale {
		t.Fatalf("stale error_code=%q", problem.ErrorCode)
	}
	var scans, runs, nodes, jobs int
	if err := database.DB().QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ops.health_scan WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.node_run node JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1),
		(SELECT count(*) FROM workflow.river_job job JOIN workflow.node_run node ON job.args->>'node_run_id'=node.id::text JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1)`, string(workspaceID)).Scan(&scans, &runs, &nodes, &jobs); err != nil {
		t.Fatal(err)
	}
	if scans != 1 || runs != 1 || nodes != 1 || jobs != 1 {
		t.Fatalf("health scans=%d runs=%d nodes=%d jobs=%d", scans, runs, nodes, jobs)
	}
	coordinator, err := workflowapp.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	var runVersion int64
	if err := database.DB().QueryRow(ctx, `SELECT version FROM workflow.run WHERE id=$1 AND workspace_id=$2`, started.WorkflowRunID, string(workspaceID)).Scan(&runVersion); err != nil {
		t.Fatal(err)
	}
	cancelCommand := workflowapp.RunControlCommand{
		WorkflowRunID: foundation.ID(started.WorkflowRunID), ExpectedVersion: runVersion, IdempotencyKey: "api-health-smart-cancel",
	}
	if _, err := coordinator.Cancel(ctx, cancelCommand); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Cancel(ctx, cancelCommand); err != nil {
		t.Fatalf("cancel exact replay=%v", err)
	}
	var scanStatus, eventStatus, eventWorkflowRunID string
	var scanVersion, eventVersion int64
	var completionEvents int
	if err := database.DB().QueryRow(ctx, `SELECT scan.status,scan.version,event.payload_summary->>'status',event.resource_version,event.workflow_run_id::text,
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND resource_ref=$2)
		FROM ops.health_scan scan JOIN ops.server_event event
		  ON event.workspace_id=scan.workspace_id AND event.resource_ref='health_scan:' || scan.id::text
		WHERE scan.id=$3 AND scan.workspace_id=$1`, string(workspaceID), "health_scan:"+started.HealthScanID, started.HealthScanID).Scan(
		&scanStatus, &scanVersion, &eventStatus, &eventVersion, &eventWorkflowRunID, &completionEvents,
	); err != nil {
		t.Fatal(err)
	}
	if scanStatus != string(healthdomain.ScanStatusCancelled) || eventStatus != "cancelled" || eventWorkflowRunID != started.WorkflowRunID || eventVersion != scanVersion || completionEvents != 1 {
		t.Fatalf("cancel scan=%q/v%d event=%q/v%d run=%q count=%d", scanStatus, scanVersion, eventStatus, eventVersion, eventWorkflowRunID, completionEvents)
	}
}

func smartCollectionHealthStartBody(t *testing.T, workspaceID foundation.ID, binding collectionapp.DurableScanBinding) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"workspace_id": string(workspaceID),
		"scope": map[string]any{
			"type": healthdomain.ScanScopeTypeSmartCollection, "ref": string(binding.CollectionID),
			"version": binding.CollectionVersion, "schema_version": "health-scope/smart-collection/v1",
			"hash": binding.QueryHash, "read_model_revision": binding.ReadModelRevision, "exact_count": binding.ExactCount,
		},
		"max_items": 100, "prevent_scope_concurrency": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func newAPIHealthCollectionService(t *testing.T, pool *pgxpool.Pool) *collectionapp.Service {
	t.Helper()
	repository, err := collectionpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := collectionapp.NewService(collectionapp.Dependencies{Repository: repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func topicCollectionQuery() collectiondomain.Query {
	return collectiondomain.Query{SchemaVersion: collectiondomain.QuerySchemaVersionV1, Root: collectiondomain.Clause{
		Kind: collectiondomain.ClauseKindGroup, Operator: "AND", Clauses: []collectiondomain.Clause{{
			Kind: collectiondomain.ClauseKindPredicate, Field: "object_type", Operator: "EQ", Value: json.RawMessage(`"TOPIC"`),
		}},
	}}
}

func seedAPIHealthCollectionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (foundation.ID, foundation.ID) {
	t.Helper()
	workspaceID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	topicID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	root := "/tmp/api-health-composition-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'api-health-composition',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), root, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,'api health topic','api health topic','','ACTIVE',1,$3,$3)`, string(topicID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	return workspaceID, topicID
}

func newMigratedAPIHealthTestPool(t *testing.T) *platformpostgres.Pool {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a PostgreSQL admin database")
	}
	ctx := context.Background()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_api_health_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 4, 0)
	if err == nil {
		var runner *platformmigration.Runner
		runner, err = platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
		if err == nil {
			err = runner.Up(ctx)
		}
		migrationPool.Close()
	}
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	database, err := platformpostgres.Open(ctx, databaseURL, 4, 0)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	return database
}
