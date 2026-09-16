package workspacepostgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"gorm.io/gorm"
)

func (repository *GORMRepository) RecordDiscoveryObservations(ctx context.Context, expected domain.Workspace, observations []domain.DiscoveryObservation) error {
	if len(observations) > 200 {
		return discoveryFailureInvalid()
	}
	for _, o := range observations {
		if !o.Valid() {
			return discoveryFailureInvalid()
		}
	}
	if err := repository.ready(ctx); err != nil {
		return err
	}
	return repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		current, err := gormLoadWorkspaceForUpdate(ctx, tx, expected.ID)
		if err != nil {
			return err
		}
		if current.Status != domain.WorkspaceStatusActive || current.BindingVersion != expected.BindingVersion || current.RootFingerprint != expected.RootFingerprint || current.RootPath != expected.RootPath {
			return discoveryFailureInvalid()
		}
		if _, err := repository.gormAuthorizeWorkspace(ctx, current); err != nil {
			return err
		}
		for _, o := range observations {
			if o.Code == "" {
				// 成功枚举目录不能恢复不可读的文件。
				if err := tx.Exec(`UPDATE core.workspace_discovery_failure SET status='RECOVERED',recovered_at=GREATEST(clock_timestamp(),last_failed_at)
     WHERE workspace_id=? AND binding_version=? AND relative_path=? AND status='FAILED' AND ((?='REGISTER' AND stage IN ('OBSERVE','REGISTER')) OR (?='WALK' AND stage='WALK'))`, string(current.ID), current.BindingVersion, o.Path, o.Stage, o.Stage).Error; err != nil {
					return classifyGORMWorkspace(ctx, err, "DISCOVERY_FAILURE_WRITE_FAILED")
				}
			} else {
				if err := tx.Exec(`INSERT INTO core.workspace_discovery_failure (workspace_id,binding_version,relative_path,stage,error_code,status,failure_count,last_failed_at)
     VALUES (?,?,?,?,?,'FAILED',1,clock_timestamp()) ON CONFLICT (workspace_id,binding_version,relative_path) DO UPDATE SET
     stage=EXCLUDED.stage,error_code=EXCLUDED.error_code,status='FAILED',failure_count=LEAST(core.workspace_discovery_failure.failure_count+1,9007199254740991),last_failed_at=GREATEST(core.workspace_discovery_failure.last_failed_at,EXCLUDED.last_failed_at),recovered_at=NULL`, string(current.ID), current.BindingVersion, o.Path, o.Stage, o.Code).Error; err != nil {
					return classifyGORMWorkspace(ctx, err, "DISCOVERY_FAILURE_WRITE_FAILED")
				}
			}
		}
		_, err = repository.gormAuthorizeWorkspace(ctx, current)
		return err
	})
}

func (repository *GORMRepository) ListDiscoveryFailures(ctx context.Context, id foundation.ID, after string, limit int) (domain.DiscoveryFailurePage, error) {
	out := domain.DiscoveryFailurePage{WorkspaceID: id, Items: []domain.DiscoveryFailure{}}
	if limit < 1 || limit > 100 || (after != "" && !domain.ValidDiscoveryPath(after)) {
		return out, discoveryFailureInvalid()
	}
	if err := repository.ready(ctx); err != nil {
		return out, err
	}
	err := repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		current, err := gormLoadWorkspaceForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Status != domain.WorkspaceStatusActive {
			return discoveryFailureInvalid()
		}
		if _, err := repository.gormAuthorizeWorkspace(ctx, current); err != nil {
			return err
		}
		out.BindingVersion = current.BindingVersion
		var rows []struct {
			Path         string
			Stage        string
			Code         string
			Status       string
			FailureCount int64
			LastFailedAt time.Time
			RecoveredAt  *time.Time
		}
		if err := tx.Raw(`SELECT relative_path AS path,stage,error_code AS code,status,failure_count,last_failed_at,recovered_at FROM core.workspace_discovery_failure
    WHERE workspace_id=? AND binding_version=? AND relative_path>? ORDER BY relative_path LIMIT ?`, string(id), current.BindingVersion, after, limit+1).Scan(&rows).Error; err != nil {
			return classifyGORMWorkspace(ctx, err, "DISCOVERY_FAILURE_QUERY_FAILED")
		}
		if len(rows) > limit {
			rows = rows[:limit]
			out.NextCursor = rows[limit-1].Path
		}
		for _, r := range rows {
			var recovered *time.Time
			if r.RecoveredAt != nil {
				at := r.RecoveredAt.UTC()
				recovered = &at
			}
			out.Items = append(out.Items, domain.DiscoveryFailure{WorkspaceID: id, BindingVersion: current.BindingVersion, Path: r.Path, Stage: r.Stage, Code: r.Code, Status: r.Status, FailureCount: r.FailureCount, LastFailedAt: r.LastFailedAt.UTC(), RecoveredAt: recovered})
		}
		_, err = repository.gormAuthorizeWorkspace(ctx, current)
		return err
	})
	return out, err
}
func discoveryFailureInvalid() error {
	return foundation.NewError(foundation.ErrorVersionConflict, "DISCOVERY_FAILURE_BINDING_INVALID", false, errors.New("invalid discovery observation or root binding"))
}

var _ domain.DiscoveryFailureRepository = (*GORMRepository)(nil)
