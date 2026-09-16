package workspacepostgres

import (
	"context"
	"errors"
	"io/fs"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"gorm.io/gorm"
)

// ReconcileLocalSourcePresence 检查一个有界的键集分页。注册时的
// upsert 也会取得来源行锁，包括精确重放。
func (repository *GORMRepository) ReconcileLocalSourcePresence(ctx context.Context, workspaceID, after foundation.ID, limit int, files domain.LocalSourcePresence) (domain.SourcePresencePage, error) {
	var result domain.SourcePresencePage
	if files == nil || limit < 1 || limit > 100 {
		return result, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_PRESENCE_REQUEST_INVALID", false, errors.New("invalid source presence request"))
	}
	workspace, err := repository.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return result, err
	}
	if workspace.Status != domain.WorkspaceStatusActive {
		return result, foundation.NewError(foundation.ErrorVersionConflict, "SOURCE_PRESENCE_WORKSPACE_INACTIVE", true, errors.New("workspace is not active"))
	}
	var ids []string
	statement := repository.database.WithContext(ctx).Raw(`SELECT s.id::text FROM core.source s
 WHERE s.workspace_id=? AND s.removed_at IS NULL AND s.type IN ('markdown','text','pdf','html','file')
 AND (?='' OR s.id>NULLIF(?,'')::uuid) ORDER BY s.id LIMIT ?`, string(workspaceID), string(after), string(after), limit).Scan(&ids)
	if statement.Error != nil {
		return result, classifyGORMWorkspace(ctx, statement.Error, "SOURCE_PRESENCE_QUERY_FAILED")
	}
	for _, id := range ids {
		removed := false
		err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
			var locked []struct {
				Location string `gorm:"column:original_location"`
			}
			if err := tx.Raw(`SELECT original_location FROM core.source WHERE workspace_id=? AND id=? AND removed_at IS NULL FOR UPDATE`, string(workspaceID), id).Scan(&locked).Error; err != nil {
				return err
			}
			if len(locked) == 0 {
				return nil
			}
			relative := locked[0].Location
			if !localSourcePresencePath(relative) {
				return nil
			}
			// 等待取得锁后再读取，不使用分页时的快照。
			var matches bool
			if err := tx.Raw(`SELECT COALESCE((SELECT original_content_location=? FROM core.source_version WHERE workspace_id=? AND source_id=? ORDER BY captured_at DESC,id DESC LIMIT 1),false)`, relative, string(workspaceID), id).Scan(&matches).Error; err != nil {
				return err
			}
			if !matches {
				return nil
			}
			if _, err := repository.gormAuthorizeWorkspace(callbackCtx, workspace); err != nil {
				return err
			}
			missing, err := files.MissingLocalSource(callbackCtx, workspace.RootPath, relative)
			if err != nil {
				return err
			}
			if !missing {
				return nil
			}
			if _, err := repository.gormAuthorizeWorkspace(callbackCtx, workspace); err != nil {
				return err
			}
			updated := tx.Exec(`UPDATE core.source SET removed_at=clock_timestamp() WHERE workspace_id=? AND id=? AND removed_at IS NULL`, string(workspaceID), id)
			removed = updated.RowsAffected == 1
			return updated.Error
		})
		result.After = foundation.ID(id)
		result.Checked++
		if err != nil {
			return result, classifyGORMWorkspace(ctx, err, "SOURCE_PRESENCE_CHECK_FAILED")
		}
		if removed {
			result.Removed++
		}
	}
	return result, nil
}

func localSourcePresencePath(relative string) bool {
	if !fs.ValidPath(relative) || relative == "." || strings.ContainsAny(relative, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(relative, "/") {
		if part == ".git" || part == ".knowledge" || part == "tmp" || part == ".tmp" {
			return false
		}
	}
	return true
}

var _ domain.SourcePresenceRepository = (*GORMRepository)(nil)
