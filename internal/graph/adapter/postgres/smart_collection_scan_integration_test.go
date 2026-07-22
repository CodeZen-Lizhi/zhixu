//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	collectionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectiondomain "github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	graphfixture "github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSmartCollectionScanUsesDurableCrossPageSnapshotAndFailsClosedOnDrift(t *testing.T) {
	ctx := context.Background()
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fixture, err := graphfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	registerSmartCollectionScanCleanup(t, pool, fixture.WorkspaceID)

	now := time.Now().UTC()
	service := newSmartCollectionService(t, pool, now)
	query := collectiondomain.Query{
		SchemaVersion: collectiondomain.QuerySchemaVersionV1,
		Root: collectiondomain.Clause{
			Kind: collectiondomain.ClauseKindGroup, Operator: string(collectiondomain.GroupOperatorAND),
			Clauses: []collectiondomain.Clause{{
				Kind: collectiondomain.ClauseKindPredicate, Field: "object_type", Operator: string(collectiondomain.OperatorIN),
				Values: []json.RawMessage{json.RawMessage(`"CLAIM"`), json.RawMessage(`"TOPIC"`)},
			}},
		},
	}
	created, err := service.Create(ctx, collectionapp.CreateCommand{
		WorkspaceID: fixture.WorkspaceID, Name: "smart collection scan integration", Query: query,
		ViewType: collectiondomain.ViewTypeList, IdempotencyKey: "smart-collection-scan-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := NewSmartCollectionScanPlanner(service)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.PlanSmartCollectionScan(ctx, fixture.WorkspaceID, created.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalNodes != 4 || plan.CollectionVersion != 1 {
		t.Fatalf("plan=%+v", plan)
	}
	scanID := smartCollectionIntegrationID(t)
	scope := smartCollectionScope(plan)
	firstRepository, err := NewSmartCollectionScanPageRepository(service, pool)
	if err != nil {
		t.Fatal(err)
	}
	first, err := firstRepository.LoadPage(ctx, graphapp.SemanticLinkTopicScanPageRequest{
		WorkspaceID: fixture.WorkspaceID, ScanID: scanID, Scope: scope, TotalNodes: plan.TotalNodes, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Complete || first.LastNode == nil || first.ProcessedNodes != 2 || len(first.Pairs) != 5 {
		t.Fatalf("first page=%+v", first)
	}
	mixedExclusion := false
	for _, exclusion := range first.Exclusions {
		mixedExclusion = mixedExclusion || exclusion.Source.Type != exclusion.Target.Type
	}
	if !mixedExclusion {
		t.Fatalf("mixed formal relation exclusion missing: %+v", first.Exclusions)
	}

	// 模拟 Worker 进程重启：Collection repository 会获得新的 HTTP cursor key，
	// 但 scan 只依赖持久 LastNode keyset，必须继续同一 binding。
	restartedService := newSmartCollectionService(t, pool, now.Add(time.Second))
	restartedRepository, err := NewSmartCollectionScanPageRepository(restartedService, pool)
	if err != nil {
		t.Fatal(err)
	}
	second, err := restartedRepository.LoadPage(ctx, graphapp.SemanticLinkTopicScanPageRequest{
		WorkspaceID: fixture.WorkspaceID, ScanID: scanID, Scope: scope, TotalNodes: plan.TotalNodes,
		LastNode: first.LastNode, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Complete || second.LastNode == nil || second.ProcessedNodes != 2 || len(second.Pairs) != 1 {
		t.Fatalf("second page=%+v", second)
	}
	pairKeys := make(map[graphdomain.SemanticLinkDiscoveryPairKey]struct{}, 6)
	mixedPair := false
	for _, page := range []graphapp.SemanticLinkTopicScanPage{first, second} {
		for _, pair := range page.Pairs {
			key, keyErr := pair.PairKey()
			if keyErr != nil {
				t.Fatal(keyErr)
			}
			if _, duplicate := pairKeys[key]; duplicate {
				t.Fatalf("duplicate cross-page pair=%+v", key)
			}
			pairKeys[key] = struct{}{}
			mixedPair = mixedPair || pair.Source.Endpoint.Ref.Type != pair.Target.Endpoint.Ref.Type
		}
	}
	if len(pairKeys) != 6 || !mixedPair {
		t.Fatalf("pair count=%d mixed=%v", len(pairKeys), mixedPair)
	}

	// 在 Collection page 的 repeatable-read 事务提交后改变 Claim version。
	// Graph 必须继续使用 page 同一快照携带的版本，而不是二次 hydration 新状态。
	stablePlan, err := planner.PlanSmartCollectionScan(ctx, fixture.WorkspaceID, created.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	var previousClaimVersion int64
	if err := pool.QueryRow(ctx, `SELECT version FROM core.claim WHERE workspace_id=$1 AND id=$2`, string(fixture.WorkspaceID), string(fixture.FirstClaimID)).Scan(&previousClaimVersion); err != nil {
		t.Fatal(err)
	}
	mutatingReader := &mutateAfterDurableRead{
		delegate: restartedService,
		mutate: func() error {
			_, updateErr := pool.Exec(ctx, `UPDATE core.claim SET version=version+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, string(fixture.WorkspaceID), string(fixture.FirstClaimID))
			return updateErr
		},
	}
	snapshotRepository, err := NewSmartCollectionScanPageRepository(mutatingReader, pool)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPage, err := snapshotRepository.LoadPage(ctx, graphapp.SemanticLinkTopicScanPageRequest{
		WorkspaceID: fixture.WorkspaceID, ScanID: scanID, Scope: smartCollectionScope(stablePlan), TotalNodes: stablePlan.TotalNodes, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	seenSnapshotClaim := false
	for _, pair := range snapshotPage.Pairs {
		for _, node := range []graphdomain.SemanticLinkDiscoveryNode{pair.Source, pair.Target} {
			if node.Endpoint.Ref.ID == fixture.FirstClaimID {
				seenSnapshotClaim = true
				if node.Endpoint.Version != previousClaimVersion {
					t.Fatalf("claim version=%d want snapshot version=%d", node.Endpoint.Version, previousClaimVersion)
				}
			}
		}
	}
	if !seenSnapshotClaim {
		t.Fatal("snapshot page did not contain the first claim")
	}
	if _, err := restartedRepository.LoadPage(ctx, graphapp.SemanticLinkTopicScanPageRequest{
		WorkspaceID: fixture.WorkspaceID, ScanID: scanID, Scope: smartCollectionScope(stablePlan), TotalNodes: stablePlan.TotalNodes, Limit: 2,
	}); !hasFoundationCode(err, collectionapp.ErrorCodeCursorStale) {
		t.Fatalf("read-model stale error=%v", err)
	}

	versionPlan, err := planner.PlanSmartCollectionScan(ctx, fixture.WorkspaceID, created.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, collectionapp.UpdateCommand{
		WorkspaceID: fixture.WorkspaceID, CollectionID: created.Collection.ID, ExpectedVersion: 1,
		Name: "smart collection scan integration updated", Query: query, ViewType: collectiondomain.ViewTypeList,
		IdempotencyKey: "smart-collection-scan-update",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := restartedRepository.LoadPage(ctx, graphapp.SemanticLinkTopicScanPageRequest{
		WorkspaceID: fixture.WorkspaceID, ScanID: scanID, Scope: smartCollectionScope(versionPlan), TotalNodes: versionPlan.TotalNodes, Limit: 2,
	}); !hasFoundationCode(err, collectionapp.ErrorCodeCursorStale) {
		t.Fatalf("collection version stale error=%v", err)
	}
}

func TestSmartCollectionScanStartsRealWorkflowReplaysAndRejectsPlanToStartDrift(t *testing.T) {
	ctx := context.Background()
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fixture, err := graphfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	registerSmartCollectionScanCleanup(t, pool, fixture.WorkspaceID)
	service := newSmartCollectionService(t, pool, time.Now().UTC())
	query := collectiondomain.Query{
		SchemaVersion: collectiondomain.QuerySchemaVersionV1,
		Root: collectiondomain.Clause{Kind: collectiondomain.ClauseKindGroup, Operator: string(collectiondomain.GroupOperatorAND), Clauses: []collectiondomain.Clause{{
			Kind: collectiondomain.ClauseKindPredicate, Field: "object_type", Operator: string(collectiondomain.OperatorEQ), Value: json.RawMessage(`"CLAIM"`),
		}}},
	}
	created, err := service.Create(ctx, collectionapp.CreateCommand{
		WorkspaceID: fixture.WorkspaceID, Name: "smart collection workflow integration", Query: query,
		ViewType: collectiondomain.ViewTypeList, IdempotencyKey: "smart-collection-workflow-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := NewSmartCollectionScanPlanner(service)
	if err != nil {
		t.Fatal(err)
	}
	commandService := newSmartCollectionCommandService(t, ctx, pool, planner, nil)
	request := graphapp.SemanticLinkSmartCollectionScanRequest{
		WorkspaceID: fixture.WorkspaceID, CollectionID: created.Collection.ID, IdempotencyKey: "smart-collection-workflow-start",
	}
	started, err := commandService.StartSmartCollectionScan(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if started.Replayed || started.Scan.Scope.Type != graphdomain.SemanticLinkScanScopeSmartCollection || started.Scan.WorkflowRunID == "" {
		t.Fatalf("started=%+v", started)
	}
	replayed, err := commandService.StartSmartCollectionScan(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Scan.ID != started.Scan.ID || replayed.Scan.WorkflowRunID != started.Scan.WorkflowRunID {
		t.Fatalf("replayed=%+v started=%+v", replayed, started)
	}
	assertScanRuntimeFacts(t, ctx, pool, fixture.WorkspaceID, started.Scan.WorkflowRunID, started.Scan.ID)

	// Start transaction commit 响应丢失后必须从 PostgreSQL receipt 恢复同一 Scan/Run。
	lossDB := scanCommitLossDB{pool: pool}
	lossService := newSmartCollectionCommandService(t, ctx, pool, planner, &lossDB)
	lossRequest := request
	lossRequest.IdempotencyKey = "smart-collection-workflow-loss"
	recovered, err := lossService.StartSmartCollectionScan(ctx, lossRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Replayed || recovered.Scan.ID == "" || recovered.Scan.WorkflowRunID == "" {
		t.Fatalf("response-loss recovery=%+v", recovered)
	}

	// Planner 返回后、Start transaction 前发生事实变化，StartOrReplay 必须在同一
	// repeatable-read transaction 重新验证 binding，不能启动旧 revision。
	mutatingReader := &mutateAfterDurablePlan{
		delegate: service,
		mutate: func() error {
			_, updateErr := pool.Exec(ctx, `UPDATE core.claim SET version=version+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, string(fixture.WorkspaceID), string(fixture.FirstClaimID))
			return updateErr
		},
	}
	stalePlanner, err := NewSmartCollectionScanPlanner(mutatingReader)
	if err != nil {
		t.Fatal(err)
	}
	staleService := newSmartCollectionCommandService(t, ctx, pool, stalePlanner, nil)
	staleRequest := request
	staleRequest.IdempotencyKey = "smart-collection-workflow-stale"
	if _, err := staleService.StartSmartCollectionScan(ctx, staleRequest); !hasFoundationCode(err, collectionapp.ErrorCodeCursorStale) {
		t.Fatalf("plan-to-start stale error=%v", err)
	}
	var staleScans int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_scan WHERE workspace_id=$1 AND idempotency_key=$2`, string(fixture.WorkspaceID), staleRequest.IdempotencyKey).Scan(&staleScans); err != nil {
		t.Fatal(err)
	}
	if staleScans != 0 {
		t.Fatalf("stale plan created %d scans", staleScans)
	}
}

type mutateAfterDurableRead struct {
	delegate graphapp.SmartCollectionScanReader
	mutate   func() error
	once     sync.Once
	err      error
}

type mutateAfterDurablePlan struct {
	delegate graphapp.SmartCollectionScanReader
	mutate   func() error
	once     sync.Once
	err      error
}

func (reader *mutateAfterDurablePlan) PlanDurableScan(ctx context.Context, workspaceID, collectionID foundation.ID) (collectionapp.DurableScanBinding, error) {
	binding, err := reader.delegate.PlanDurableScan(ctx, workspaceID, collectionID)
	if err != nil {
		return collectionapp.DurableScanBinding{}, err
	}
	reader.once.Do(func() { reader.err = reader.mutate() })
	if reader.err != nil {
		return collectionapp.DurableScanBinding{}, reader.err
	}
	return binding, nil
}

func (reader *mutateAfterDurablePlan) ReadDurableScanPage(ctx context.Context, request collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error) {
	return reader.delegate.ReadDurableScanPage(ctx, request)
}

func (reader *mutateAfterDurableRead) PlanDurableScan(ctx context.Context, workspaceID, collectionID foundation.ID) (collectionapp.DurableScanBinding, error) {
	return reader.delegate.PlanDurableScan(ctx, workspaceID, collectionID)
}

func (reader *mutateAfterDurableRead) ReadDurableScanPage(ctx context.Context, request collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error) {
	page, err := reader.delegate.ReadDurableScanPage(ctx, request)
	if err != nil {
		return collectionapp.DurableScanPage{}, err
	}
	reader.once.Do(func() { reader.err = reader.mutate() })
	if reader.err != nil {
		return collectionapp.DurableScanPage{}, reader.err
	}
	return page, nil
}

func newSmartCollectionService(t *testing.T, pool *pgxpool.Pool, now time.Time) *collectionapp.Service {
	t.Helper()
	repository, err := collectionpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := collectionapp.NewService(collectionapp.Dependencies{
		Repository: repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type smartCollectionUnusedTopicPlanner struct{}

func (smartCollectionUnusedTopicPlanner) PlanTopicScan(context.Context, foundation.ID, foundation.ID) (graphapp.SemanticLinkTopicScanPlan, error) {
	return graphapp.SemanticLinkTopicScanPlan{}, errors.New("topic planner is not used by smart collection integration")
}

func newSmartCollectionCommandService(t *testing.T, ctx context.Context, pool *pgxpool.Pool, planner graphapp.SemanticLinkSmartCollectionScanPlanner, lossDB *scanCommitLossDB) *graphapp.SemanticLinkScanCommandService {
	t.Helper()
	planners, err := graphapp.NewSemanticLinkScanPlannerSet(smartCollectionUnusedTopicPlanner{}, planner)
	if err != nil {
		t.Fatal(err)
	}
	var repository *SemanticLinkScanRepository
	if lossDB == nil {
		repository = newSemanticLinkScanTestRepository(t, ctx, pool)
	} else {
		repository = newSemanticLinkScanTestRepositoryWithDB(t, ctx, *lossDB, lossDB.pool)
	}
	scans, err := graphapp.NewSemanticLinkScanService(repository, repository)
	if err != nil {
		t.Fatal(err)
	}
	service, err := graphapp.NewSemanticLinkScanCommandService(planners, scans)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func smartCollectionScope(plan graphapp.SemanticLinkSmartCollectionScanPlan) graphdomain.SemanticLinkScanScope {
	return graphdomain.SemanticLinkScanScope{
		Type: graphdomain.SemanticLinkScanScopeSmartCollection, Ref: string(plan.CollectionID),
		Version: plan.CollectionVersion, SchemaVersion: graphapp.SemanticLinkSmartCollectionScanScopeSchemaVersion,
		QueryHash: plan.QueryHash, ReadModelRevision: plan.ReadModelRevision,
	}
}

func registerSmartCollectionScanCleanup(t *testing.T, pool *pgxpool.Pool, workspaceID foundation.ID) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Errorf("begin smart collection cleanup: %v", err)
			return
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(context.Background())
			}
		}()
		if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM learning.smart_collection_command WHERE workspace_id=$1`, string(workspaceID))
		}
		if err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM learning.smart_collection WHERE workspace_id=$1`, string(workspaceID))
		}
		if err == nil {
			err = tx.Commit(ctx)
			committed = err == nil
		}
		if err != nil {
			t.Errorf("cleanup smart collection facts: %v", err)
			return
		}
		if err := graphfixture.CleanupSemanticLinkBrowser(ctx, pool, workspaceID); err != nil {
			t.Errorf("cleanup smart collection graph fixture: %v", err)
		}
	})
}

func hasFoundationCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}

func smartCollectionIntegrationID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
