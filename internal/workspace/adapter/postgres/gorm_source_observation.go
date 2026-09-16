package workspacepostgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"gorm.io/gorm"
)

// 调用方已通过注册时的 upsert 持有 Source 行锁。
// 命令重放保持规范的来源/哈希身份。新的文件系统
// 观察则可记录旧字节再次成为当前内容。
func gormObserveSourceVersion(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, requested domain.SourceVersion) (domain.SourceVersion, bool, error) {
	// PostgreSQL 持久化精度为微秒。按此精度比较，以免较新的
	// 纳秒值插入后与已持久化的当前时间戳相等。
	requested.CapturedAt = requested.CapturedAt.UTC().Truncate(time.Microsecond)
	row, err := gormWorkspaceRawRow(tx.WithContext(ctx), `SELECT id::text,source_id::text,content_artifact_id::text,content_hash,byte_size,mime_type,original_content_location,security_status,COALESCE(parser_version,''),captured_at FROM core.source_version WHERE workspace_id=? AND source_id=? ORDER BY captured_at DESC,id DESC LIMIT 1`, string(workspaceID), string(requested.SourceID))
	if err != nil {
		return domain.SourceVersion{}, false, err
	}
	current, err := scanSourceVersionWithArtifactPolicy(row, true)
	if gormWorkspaceNoRows(err) {
		return gormInsertOrGetSourceVersion(ctx, tx, workspaceID, requested)
	}
	if err != nil {
		return domain.SourceVersion{}, false, err
	}
	if current.ContentHash == requested.ContentHash {
		if current.ContentArtifactID == "" {
			return gormInsertOrGetSourceVersion(ctx, tx, workspaceID, requested)
		}
		if !sameSourceVersionMetadata(current, requested) {
			return domain.SourceVersion{}, false, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_VERSION_METADATA_CONFLICT", false, errors.New("current source metadata differs from observation"))
		}
		return current, false, nil
	}
	// 较旧的采集晚于较新的采集完成时，不得恢复过时
	// 字节。下一轮会以新的时间戳再次观察它。
	if !requested.CapturedAt.After(current.CapturedAt) {
		return domain.SourceVersion{}, false, foundation.NewError(foundation.ErrorVersionConflict, "SOURCE_OBSERVATION_STALE", true, errors.New("source observation precedes current version"))
	}
	canonical, created, err := gormInsertOrGetSourceVersion(ctx, tx, workspaceID, requested)
	if err != nil || created {
		return canonical, created, err
	}
	row, err = gormWorkspaceRawRow(tx.WithContext(ctx), `INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,parser_version,captured_at,observation_predecessor_id) VALUES(?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?) RETURNING id::text,source_id::text,content_artifact_id::text,content_hash,byte_size,mime_type,original_content_location,security_status,COALESCE(parser_version,''),captured_at`, string(requested.ID), string(requested.SourceID), string(workspaceID), string(requested.ContentArtifactID), requested.ContentHash, requested.ByteSize, requested.MediaType, requested.OriginalContentLocation, requested.SecurityStatus, requested.ParserVersion, requested.CapturedAt.UTC(), string(current.ID))
	if err != nil {
		return domain.SourceVersion{}, false, err
	}
	observed, err := scanSourceVersion(row)
	return observed, err == nil, err
}
