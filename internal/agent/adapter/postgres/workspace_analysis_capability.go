package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

const workspaceAnalysisWorkerCapabilityColumns = `
	worker_instance_id::text,definition_key,definition_version,definition_hash,tool_catalog_hash,
	policy_version,config_revision,heartbeat_at,lease_until,released_at,version,created_at,updated_at`

// AdvertiseWorkspaceAnalysisWorker 在 Repository 自管短事务中创建或续租相同实例的同一合同。
func (repository *Repository) AdvertiseWorkspaceAnalysisWorker(
	ctx context.Context,
	advertisement application.WorkspaceAnalysisWorkerAdvertisement,
) (application.WorkspaceAnalysisWorkerCapability, error) {
	if err := validateWorkspaceAnalysisCapabilityRepositoryCall(repository, ctx, advertisement); err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, err
	}
	return repository.writeWorkspaceAnalysisWorkerCapability(ctx, workspaceAnalysisCapabilityAdvertiseSQL, advertisement)
}

// HeartbeatWorkspaceAnalysisWorker 仅为尚未过期且合同完全一致的广告续租。
func (repository *Repository) HeartbeatWorkspaceAnalysisWorker(
	ctx context.Context,
	advertisement application.WorkspaceAnalysisWorkerAdvertisement,
) (application.WorkspaceAnalysisWorkerCapability, error) {
	if err := validateWorkspaceAnalysisCapabilityRepositoryCall(repository, ctx, advertisement); err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, err
	}
	return repository.writeWorkspaceAnalysisWorkerCapability(ctx, workspaceAnalysisCapabilityHeartbeatSQL, advertisement)
}

// ReleaseWorkspaceAnalysisWorker 保留退役事实，不删除任何已广告能力。
func (repository *Repository) ReleaseWorkspaceAnalysisWorker(
	ctx context.Context,
	advertisement application.WorkspaceAnalysisWorkerAdvertisement,
) (application.WorkspaceAnalysisWorkerCapability, error) {
	if err := validateWorkspaceAnalysisCapabilityRepositoryCall(repository, ctx, advertisement); err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, err
	}
	return repository.writeWorkspaceAnalysisWorkerCapability(ctx, workspaceAnalysisCapabilityReleaseSQL, advertisement)
}

// RequireWorkspaceAnalysisWorkerReadyTx 在 Question 派发的 caller-owned transaction 内查询精确新鲜合同。
func (repository *Repository) RequireWorkspaceAnalysisWorkerReadyTx(
	ctx context.Context,
	transaction any,
	contract application.WorkspaceAnalysisCapabilityContract,
) error {
	tx, ok := workspaceAnalysisTransaction(transaction)
	if repository == nil || !ok {
		return workspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis readiness transaction is unavailable"))
	}
	if ctx == nil {
		return workspaceAnalysisCapabilityInvalid(errors.New("workspace analysis readiness context is nil"))
	}
	if err := contract.Validate(); err != nil {
		return err
	}

	var ready bool
	err := tx.QueryRow(ctx, workspaceAnalysisCapabilityReadySQL,
		contract.DefinitionKey, contract.DefinitionVersion, contract.DefinitionHash, contract.ToolCatalogHash,
		contract.PolicyVersion, contract.ConfigRevision,
	).Scan(&ready)
	if err != nil {
		return classify(err)
	}
	if !ready {
		return workspaceAnalysisCapabilityUnavailable(errors.New("no fresh workspace analysis worker matches the required contract"))
	}
	return nil
}

func (repository *Repository) writeWorkspaceAnalysisWorkerCapability(
	ctx context.Context,
	statement string,
	advertisement application.WorkspaceAnalysisWorkerAdvertisement,
) (application.WorkspaceAnalysisWorkerCapability, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, classify(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	record, err := scanWorkspaceAnalysisWorkerCapability(tx.QueryRow(ctx, statement,
		string(advertisement.WorkerInstanceID), advertisement.Contract.DefinitionKey,
		advertisement.Contract.DefinitionVersion, advertisement.Contract.DefinitionHash,
		advertisement.Contract.ToolCatalogHash, advertisement.Contract.PolicyVersion,
		advertisement.Contract.ConfigRevision, workspaceAnalysisCapabilityInterval(advertisement.LeaseDuration),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.WorkspaceAnalysisWorkerCapability{}, workspaceAnalysisCapabilityConflict(
			errors.New("workspace analysis worker capability is absent, stale, released, or contract-drifted"),
		)
	}
	if err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, classify(err)
	}
	return record, nil
}

func validateWorkspaceAnalysisCapabilityRepositoryCall(
	repository *Repository,
	ctx context.Context,
	advertisement application.WorkspaceAnalysisWorkerAdvertisement,
) error {
	if repository == nil || repository.db == nil {
		return workspaceAnalysisCapabilityUnavailable(errors.New("workspace analysis capability repository is unavailable"))
	}
	if ctx == nil {
		return workspaceAnalysisCapabilityInvalid(errors.New("workspace analysis capability context is nil"))
	}
	return advertisement.Validate()
}

func scanWorkspaceAnalysisWorkerCapability(row rowScanner) (application.WorkspaceAnalysisWorkerCapability, error) {
	var record application.WorkspaceAnalysisWorkerCapability
	var workerInstanceID string
	var releasedAt *time.Time
	if err := row.Scan(
		&workerInstanceID, &record.Contract.DefinitionKey, &record.Contract.DefinitionVersion,
		&record.Contract.DefinitionHash, &record.Contract.ToolCatalogHash, &record.Contract.PolicyVersion,
		&record.Contract.ConfigRevision, &record.HeartbeatAt, &record.LeaseUntil, &releasedAt,
		&record.Version, &record.CreatedAt, &record.UpdatedAt,
	); err != nil {
		return application.WorkspaceAnalysisWorkerCapability{}, err
	}
	record.WorkerInstanceID = foundation.ID(workerInstanceID)
	record.LeaseDuration = record.LeaseUntil.Sub(record.HeartbeatAt)
	record.ReleasedAt = releasedAt
	if err := record.WorkspaceAnalysisWorkerAdvertisement.Validate(); err != nil || record.Version < 1 ||
		record.HeartbeatAt.IsZero() || record.LeaseUntil.IsZero() || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() ||
		record.HeartbeatAt.After(record.LeaseUntil) || record.CreatedAt.After(record.HeartbeatAt) ||
		record.UpdatedAt.Before(record.CreatedAt) ||
		(record.ReleasedAt != nil && (record.ReleasedAt.Before(record.HeartbeatAt) || !record.UpdatedAt.Equal(*record.ReleasedAt))) {
		if err == nil {
			err = errors.New("workspace analysis worker capability record is inconsistent")
		}
		return application.WorkspaceAnalysisWorkerCapability{}, consistency(err)
	}
	return record, nil
}

func workspaceAnalysisCapabilityInterval(duration time.Duration) string {
	return fmt.Sprintf("%d microseconds", duration/time.Microsecond)
}

func workspaceAnalysisCapabilityInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, application.ErrorCodeWorkspaceAnalysisCapabilityInvalid, false, cause)
}

func workspaceAnalysisCapabilityUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeWorkspaceAnalysisCapabilityUnavailable, false, cause)
}

func workspaceAnalysisCapabilityConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, application.ErrorCodeWorkspaceAnalysisCapabilityConflict, false, cause)
}

const workspaceAnalysisCapabilityAdvertiseSQL = `
WITH clock AS (SELECT clock_timestamp() AS at)
INSERT INTO agent.workspace_analysis_worker_capability(
	worker_instance_id,definition_key,definition_version,definition_hash,tool_catalog_hash,
	policy_version,config_revision,heartbeat_at,lease_until,released_at,version,created_at,updated_at
)
SELECT $1,$2,$3,$4,$5,$6,$7,clock.at,clock.at+$8::interval,NULL,1,clock.at,clock.at
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

const workspaceAnalysisCapabilityHeartbeatSQL = `
WITH clock AS (SELECT clock_timestamp() AS at)
UPDATE agent.workspace_analysis_worker_capability AS capability
SET heartbeat_at=clock.at,
	lease_until=clock.at+$8::interval,
	version=capability.version+1,
	updated_at=clock.at
FROM clock
WHERE capability.worker_instance_id=$1
	AND capability.definition_key=$2
	AND capability.definition_version=$3
	AND capability.definition_hash=$4
	AND capability.tool_catalog_hash=$5
	AND capability.policy_version=$6
	AND capability.config_revision=$7
	AND capability.released_at IS NULL
	AND capability.heartbeat_at<=clock.at
	AND capability.lease_until>clock.at
RETURNING ` + workspaceAnalysisWorkerCapabilityColumns

const workspaceAnalysisCapabilityReleaseSQL = `
WITH clock AS (SELECT clock_timestamp() AS at)
UPDATE agent.workspace_analysis_worker_capability AS capability
SET released_at=clock.at,
	version=capability.version+1,
	updated_at=clock.at
FROM clock
WHERE capability.worker_instance_id=$1
	AND capability.definition_key=$2
	AND capability.definition_version=$3
	AND capability.definition_hash=$4
	AND capability.tool_catalog_hash=$5
	AND capability.policy_version=$6
	AND capability.config_revision=$7
	AND capability.released_at IS NULL
	AND $8::interval BETWEEN interval '10 seconds' AND interval '60 seconds'
RETURNING ` + workspaceAnalysisWorkerCapabilityColumns

const workspaceAnalysisCapabilityReadySQL = `
SELECT EXISTS (
	SELECT 1
	FROM agent.workspace_analysis_worker_capability AS capability
	WHERE capability.definition_key=$1
		AND capability.definition_version=$2
		AND capability.definition_hash=$3
		AND capability.tool_catalog_hash=$4
		AND capability.policy_version=$5
		AND capability.config_revision=$6
		AND capability.released_at IS NULL
		AND capability.heartbeat_at<=clock_timestamp()
		AND capability.lease_until>clock_timestamp()
)`

var _ application.WorkspaceAnalysisCapabilityRepository = (*Repository)(nil)
