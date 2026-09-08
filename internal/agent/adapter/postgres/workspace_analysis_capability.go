package postgres

import (
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const workspaceAnalysisWorkerCapabilityColumns = `
	worker_instance_id::text,definition_key,definition_version,definition_hash,tool_catalog_hash,
	policy_version,config_revision,heartbeat_at,lease_until,released_at,version,created_at,updated_at`

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
