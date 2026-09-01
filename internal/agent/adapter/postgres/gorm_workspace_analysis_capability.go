package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

const gormWorkspaceAnalysisCapabilityAdvertiseSQL = `
WITH clock AS (SELECT clock_timestamp() AS at)
INSERT INTO agent.workspace_analysis_worker_capability(
	worker_instance_id,definition_key,definition_version,definition_hash,tool_catalog_hash,
	policy_version,config_revision,heartbeat_at,lease_until,released_at,version,created_at,updated_at
)
SELECT ?,?,?,?,?,?,?,clock.at,clock.at+?::interval,NULL,1,clock.at,clock.at
FROM clock
ON CONFLICT (worker_instance_id) DO UPDATE
SET heartbeat_at=EXCLUDED.heartbeat_at,
	lease_until=EXCLUDED.lease_until,
	version=agent.workspace_analysis_worker_capability.version+1,
	updated_at=EXCLUDED.updated_at
WHERE agent.workspace_analysis_worker_capability.released_at IS NULL
	AND agent.workspace_analysis_worker_capability.definition_key=EXCLUDED.definition_key
	AND agent.workspace_analysis_worker_capability.definition_version=EXCLUDED.definition_version
	AND agent.workspace_analysis_worker_capability.definition_hash=EXCLUDED.definition_hash
	AND agent.workspace_analysis_worker_capability.tool_catalog_hash=EXCLUDED.tool_catalog_hash
	AND agent.workspace_analysis_worker_capability.policy_version=EXCLUDED.policy_version
	AND agent.workspace_analysis_worker_capability.config_revision=EXCLUDED.config_revision
RETURNING ` + workspaceAnalysisWorkerCapabilityColumns

const gormWorkspaceAnalysisCapabilityHeartbeatSQL = `
WITH clock AS (SELECT clock_timestamp() AS at)
UPDATE agent.workspace_analysis_worker_capability AS capability
SET heartbeat_at=clock.at,
	lease_until=clock.at+?::interval,
	version=capability.version+1,
	updated_at=clock.at
FROM clock
WHERE capability.worker_instance_id=?
	AND capability.definition_key=?
	AND capability.definition_version=?
	AND capability.definition_hash=?
	AND capability.tool_catalog_hash=?
	AND capability.policy_version=?
	AND capability.config_revision=?
	AND capability.released_at IS NULL
	AND capability.heartbeat_at<=clock.at
	AND capability.lease_until>clock.at
RETURNING ` + workspaceAnalysisWorkerCapabilityColumns

const gormWorkspaceAnalysisCapabilityReleaseSQL = `
WITH clock AS (SELECT clock_timestamp() AS at)
UPDATE agent.workspace_analysis_worker_capability AS capability
SET released_at=clock.at,
	version=capability.version+1,
	updated_at=clock.at
FROM clock
WHERE capability.worker_instance_id=?
	AND capability.definition_key=?
	AND capability.definition_version=?
	AND capability.definition_hash=?
	AND capability.tool_catalog_hash=?
	AND capability.policy_version=?
	AND capability.config_revision=?
	AND capability.released_at IS NULL
	AND ?::interval BETWEEN interval '10 seconds' AND interval '60 seconds'
RETURNING ` + workspaceAnalysisWorkerCapabilityColumns

const gormWorkspaceAnalysisCapabilityReadySQL = `
SELECT EXISTS (
	SELECT 1
	FROM agent.workspace_analysis_worker_capability AS capability
	WHERE capability.definition_key=?
		AND capability.definition_version=?
		AND capability.definition_hash=?
		AND capability.tool_catalog_hash=?
		AND capability.policy_version=?
		AND capability.config_revision=?
		AND capability.released_at IS NULL
		AND capability.heartbeat_at<=clock_timestamp()
		AND capability.lease_until>clock_timestamp()
)`

// AdvertiseWorkspaceAnalysisWorker creates or renews one immutable capability contract.
func (repository *GORMRepository) AdvertiseWorkspaceAnalysisWorker(
	ctx context.Context,
	advertisement application.WorkspaceAnalysisWorkerAdvertisement,
) (application.WorkspaceAnalysisWorkerCapability, error) {
	if err := validateGORMWorkspaceAnalysisCapabilityCall(repository, ctx, advertisement); err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, err
	}
	arguments := workspaceAnalysisCapabilityArguments(advertisement)
	return repository.writeGORMWorkspaceAnalysisWorkerCapability(ctx, gormWorkspaceAnalysisCapabilityAdvertiseSQL, arguments...)
}

// HeartbeatWorkspaceAnalysisWorker renews an active, unexpired exact contract.
func (repository *GORMRepository) HeartbeatWorkspaceAnalysisWorker(
	ctx context.Context,
	advertisement application.WorkspaceAnalysisWorkerAdvertisement,
) (application.WorkspaceAnalysisWorkerCapability, error) {
	if err := validateGORMWorkspaceAnalysisCapabilityCall(repository, ctx, advertisement); err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, err
	}
	base := workspaceAnalysisCapabilityArguments(advertisement)
	arguments := append([]any{base[7]}, base[:7]...)
	return repository.writeGORMWorkspaceAnalysisWorkerCapability(ctx, gormWorkspaceAnalysisCapabilityHeartbeatSQL, arguments...)
}

// ReleaseWorkspaceAnalysisWorker retires the advertisement without deleting history.
func (repository *GORMRepository) ReleaseWorkspaceAnalysisWorker(
	ctx context.Context,
	advertisement application.WorkspaceAnalysisWorkerAdvertisement,
) (application.WorkspaceAnalysisWorkerCapability, error) {
	if err := validateGORMWorkspaceAnalysisCapabilityCall(repository, ctx, advertisement); err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, err
	}
	arguments := workspaceAnalysisCapabilityArguments(advertisement)
	return repository.writeGORMWorkspaceAnalysisWorkerCapability(ctx, gormWorkspaceAnalysisCapabilityReleaseSQL, arguments...)
}

// RequireWorkspaceAnalysisWorkerReadyScoped checks readiness in the caller scope.
func (repository *GORMRepository) RequireWorkspaceAnalysisWorkerReadyScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	contract application.WorkspaceAnalysisCapabilityContract,
) error {
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) {
		return workspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis readiness repository is unavailable"))
	}
	if ctx == nil {
		return workspaceAnalysisCapabilityInvalid(errors.New("workspace analysis readiness context is nil"))
	}
	if err := contract.Validate(); err != nil {
		return err
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return workspaceAnalysisCapabilityUnavailable(errors.Join(errors.New("workspace analysis readiness scope is unavailable"), err))
	}
	row, err := gormRawRow(transaction.WithContext(ctx), gormWorkspaceAnalysisCapabilityReadySQL,
		contract.DefinitionKey, contract.DefinitionVersion, contract.DefinitionHash, contract.ToolCatalogHash,
		contract.PolicyVersion, contract.ConfigRevision,
	)
	if err != nil {
		return classifyGORM(ctx, err)
	}
	var ready bool
	if err := row.Scan(&ready); err != nil {
		return classifyGORM(ctx, err)
	}
	if !ready {
		return workspaceAnalysisCapabilityUnavailable(errors.New("no fresh workspace analysis worker matches the required contract"))
	}
	return nil
}

func (repository *GORMRepository) writeGORMWorkspaceAnalysisWorkerCapability(
	ctx context.Context,
	statement string,
	arguments ...any,
) (record application.WorkspaceAnalysisWorkerCapability, err error) {
	err = repository.within(ctx, foundation.TransactionOptions{}, func(
		callbackCtx context.Context,
		transaction *gorm.DB,
		_ foundation.TransactionScope,
	) error {
		row, queryErr := gormRawRow(transaction.WithContext(callbackCtx), statement, arguments...)
		if queryErr != nil {
			return queryErr
		}
		persisted, scanErr := scanWorkspaceAnalysisWorkerCapability(row)
		if gormNoRows(scanErr) {
			return workspaceAnalysisCapabilityConflict(
				errors.New("workspace analysis worker capability is absent, stale, released, or contract-drifted"),
			)
		}
		if scanErr != nil {
			return scanErr
		}
		record = persisted
		return nil
	})
	if err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, classifyGORM(ctx, err)
	}
	return record, nil
}

func validateGORMWorkspaceAnalysisCapabilityCall(
	repository *GORMRepository,
	ctx context.Context,
	advertisement application.WorkspaceAnalysisWorkerAdvertisement,
) error {
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) {
		return workspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis capability repository is unavailable"))
	}
	if ctx == nil {
		return workspaceAnalysisCapabilityInvalid(errors.New("workspace analysis capability context is nil"))
	}
	return advertisement.Validate()
}

func workspaceAnalysisCapabilityArguments(advertisement application.WorkspaceAnalysisWorkerAdvertisement) []any {
	return []any{
		string(advertisement.WorkerInstanceID), advertisement.Contract.DefinitionKey,
		advertisement.Contract.DefinitionVersion, advertisement.Contract.DefinitionHash,
		advertisement.Contract.ToolCatalogHash, advertisement.Contract.PolicyVersion,
		advertisement.Contract.ConfigRevision, workspaceAnalysisCapabilityInterval(advertisement.LeaseDuration),
	}
}

var _ application.WorkspaceAnalysisCapabilityLifecyclePort = (*GORMRepository)(nil)
var _ application.ScopedWorkspaceAnalysisReadiness = (*GORMRepository)(nil)
