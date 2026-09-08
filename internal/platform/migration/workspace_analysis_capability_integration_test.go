//go:build integration

package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func TestWorkspaceAnalysisWorkerCapabilityPersistenceContract(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}

	runtime := openMigrationRuntimePool(t, ctx, pool)
	defer runtime.Close()
	pool = runtime.DB()
	repository, err := agentpostgres.NewGORMRepository(runtime)
	if err != nil {
		t.Fatal(err)
	}
	contract := workspaceAnalysisCapabilityIntegrationContract('a', 'b', 7)
	first := workspaceAnalysisCapabilityIntegrationAdvertisement(1, contract)
	second := workspaceAnalysisCapabilityIntegrationAdvertisement(2, contract)

	firstRecord, err := repository.AdvertiseWorkspaceAnalysisWorker(ctx, first)
	if err != nil || firstRecord.Version != 1 || firstRecord.ReleasedAt != nil ||
		!firstRecord.HeartbeatAt.Equal(firstRecord.CreatedAt) || !firstRecord.HeartbeatAt.Equal(firstRecord.UpdatedAt) ||
		firstRecord.LeaseUntil.Sub(firstRecord.HeartbeatAt) != first.LeaseDuration {
		t.Fatalf("first advertise=%#v err=%v", firstRecord, err)
	}
	replayed, err := repository.AdvertiseWorkspaceAnalysisWorker(ctx, first)
	if err != nil || replayed.Version != 2 || replayed.HeartbeatAt.Before(firstRecord.HeartbeatAt) {
		t.Fatalf("advertise replay=%#v err=%v", replayed, err)
	}
	heartbeated, err := repository.HeartbeatWorkspaceAnalysisWorker(ctx, first)
	if err != nil || heartbeated.Version != 3 || heartbeated.HeartbeatAt.Before(replayed.HeartbeatAt) ||
		heartbeated.LeaseUntil.Sub(heartbeated.HeartbeatAt) != first.LeaseDuration {
		t.Fatalf("heartbeat=%#v err=%v", heartbeated, err)
	}
	if err := workspaceAnalysisCapabilityRequireReady(ctx, runtime, repository, contract); err != nil {
		t.Fatalf("exact ready check: %v", err)
	}

	drifted := contract
	drifted.ToolCatalogHash = strings.Repeat("c", 64)
	if err := workspaceAnalysisCapabilityRequireReady(ctx, runtime, repository, drifted); !workspaceAnalysisCapabilityUnavailableError(err) {
		t.Fatalf("catalog drift ready error=%#v", err)
	}
	drifted = contract
	drifted.PolicyVersion = 2
	if err := drifted.Validate(); err == nil {
		t.Fatal("unsupported policy contract was accepted before readiness")
	}
	drifted = contract
	drifted.ConfigRevision++
	if err := workspaceAnalysisCapabilityRequireReady(ctx, runtime, repository, drifted); !workspaceAnalysisCapabilityUnavailableError(err) {
		t.Fatalf("config drift ready error=%#v", err)
	}

	if _, err := repository.AdvertiseWorkspaceAnalysisWorker(ctx, second); err != nil {
		t.Fatalf("second worker advertise: %v", err)
	}
	if _, err := repository.ReleaseWorkspaceAnalysisWorker(ctx, first); err != nil {
		t.Fatalf("release first worker: %v", err)
	}
	if err := workspaceAnalysisCapabilityRequireReady(ctx, runtime, repository, contract); err != nil {
		t.Fatalf("second fresh worker did not keep readiness: %v", err)
	}
	if _, err := repository.ReleaseWorkspaceAnalysisWorker(ctx, second); err != nil {
		t.Fatalf("release second worker: %v", err)
	}
	if err := workspaceAnalysisCapabilityRequireReady(ctx, runtime, repository, contract); !workspaceAnalysisCapabilityUnavailableError(err) {
		t.Fatalf("all released ready error=%#v", err)
	}

	// The immutable tuple cannot be changed by raw SQL, and a future heartbeat
	// cannot be inserted to make an API host believe an unavailable Worker is ready.
	_, err = pool.Exec(ctx, `UPDATE agent.workspace_analysis_worker_capability
SET definition_hash=repeat('d',64) WHERE worker_instance_id=$1`, string(first.WorkerInstanceID))
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `
WITH clock AS (SELECT clock_timestamp() AS at)
INSERT INTO agent.workspace_analysis_worker_capability(
    worker_instance_id,definition_key,definition_version,definition_hash,tool_catalog_hash,
    policy_version,config_revision,heartbeat_at,lease_until,released_at,version,created_at,updated_at
)
SELECT '84000000-0000-4000-8000-000000000003','workspace-analysis',1,repeat('a',64),repeat('b',64),
       1,7,clock.at+interval '1 second',clock.at+interval '31 seconds',NULL,1,clock.at+interval '1 second',clock.at+interval '1 second'
FROM clock`)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `
WITH clock AS (SELECT clock_timestamp() AS at)
INSERT INTO agent.workspace_analysis_worker_capability(
    worker_instance_id,definition_key,definition_version,definition_hash,tool_catalog_hash,
    policy_version,config_revision,heartbeat_at,lease_until,released_at,version,created_at,updated_at
)
SELECT '84000000-0000-4000-8000-000000000004','workspace-analysis',1,repeat('a',64),repeat('b',64),
       1,7,clock.at-interval '40 seconds',clock.at-interval '10 seconds',NULL,1,clock.at-interval '40 seconds',clock.at-interval '40 seconds'
FROM clock`)
	if err != nil {
		t.Fatalf("insert expired capability: %v", err)
	}
	expired := workspaceAnalysisCapabilityIntegrationAdvertisement(4, contract)
	if _, err := repository.HeartbeatWorkspaceAnalysisWorker(ctx, expired); err == nil {
		t.Fatal("expired worker heartbeat was accepted")
	} else {
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Kind != foundation.ErrorVersionConflict ||
			classified.Code != agentapplication.ErrorCodeWorkspaceAnalysisCapabilityConflict {
			t.Fatalf("expired worker heartbeat error=%#v", err)
		}
	}
	if err := workspaceAnalysisCapabilityRequireReady(ctx, runtime, repository, contract); !workspaceAnalysisCapabilityUnavailableError(err) {
		t.Fatalf("expired capability ready error=%#v", err)
	}
	_, err = pool.Exec(ctx, `TRUNCATE agent.workspace_analysis_worker_capability`)
	assertPostgresCode(t, err, "55000")
}

func workspaceAnalysisCapabilityRequireReady(
	ctx context.Context,
	pool *platformpostgres.Pool,
	repository agentapplication.ScopedWorkspaceAnalysisReadiness,
	contract agentapplication.WorkspaceAnalysisCapabilityContract,
) error {
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return err
	}
	return unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		return repository.RequireWorkspaceAnalysisWorkerReadyScoped(callbackCtx, scope, contract)
	})
}

func workspaceAnalysisCapabilityIntegrationContract(definition, catalog byte, revision int64) agentapplication.WorkspaceAnalysisCapabilityContract {
	return agentapplication.WorkspaceAnalysisCapabilityContract{
		DefinitionKey: "workspace-analysis", DefinitionVersion: 1,
		DefinitionHash: strings.Repeat(string(definition), 64), ToolCatalogHash: strings.Repeat(string(catalog), 64),
		PolicyVersion: 1, ConfigRevision: revision,
	}
}

func workspaceAnalysisCapabilityIntegrationAdvertisement(
	instance int,
	contract agentapplication.WorkspaceAnalysisCapabilityContract,
) agentapplication.WorkspaceAnalysisWorkerAdvertisement {
	return agentapplication.WorkspaceAnalysisWorkerAdvertisement{
		WorkerInstanceID: foundation.ID(fmt.Sprintf("84000000-0000-4000-8000-%012d", instance)),
		Contract:         contract,
		LeaseDuration:    agentapplication.DefaultWorkspaceAnalysisWorkerCapabilityLease,
	}
}

func workspaceAnalysisCapabilityUnavailableError(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == foundation.ErrorDependencyUnavailable &&
		classified.Code == agentapplication.ErrorCodeWorkspaceAnalysisCapabilityUnavailable && !classified.Retryable
}
