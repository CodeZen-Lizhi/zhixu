package postgres

import (
	"context"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

// ListProfileDirectory 读取一致的元数据分页，包括不可变
// 证据。安全性和当前版本的筛选条件在 LIMIT 前应用。
func (repository *GORMProfileRepository) ListProfileDirectory(ctx context.Context, query captureapp.ProfileDirectoryQuery) (captureapp.ProfileDirectoryPage, error) {
	result := captureapp.ProfileDirectoryPage{Items: []captureapp.ProfileDirectoryEntry{}}
	if err := repository.ready(ctx); err != nil {
		return result, err
	}
	if !validID(query.WorkspaceID) || query.AfterSourceID != "" && !validID(query.AfterSourceID) || query.SourceVersionID != "" && !validID(query.SourceVersionID) || query.Limit < 1 || query.Limit > captureapp.MaxProfileDirectoryPage {
		return result, invalid(profileQueryInvalidCode, "profile directory query is invalid")
	}
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		type candidate struct {
			SourceID          foundation.ID
			SourceVersionID   foundation.ID
			RevisionID        foundation.ID
			ContentArtifactID foundation.ID
			ContentHash       string
			Title             string
		}
		var candidates []candidate
		err = tx.WithContext(ctx).Raw(`SELECT s.id AS source_id,v.id AS source_version_id,r.id AS revision_id,
   v.content_artifact_id,v.content_hash,s.logical_name AS title
   FROM core.source s
   JOIN core.source_version v ON v.source_id=s.id AND v.workspace_id=s.workspace_id
   JOIN learning.document_knowledge_profile p ON p.source_version_id=v.id AND p.workspace_id=s.workspace_id
   JOIN learning.document_knowledge_profile_revision r ON r.id=p.current_revision_id
    AND r.profile_id=p.id AND r.workspace_id=p.workspace_id AND r.source_version_id=v.id
   JOIN ingestion.source_version_projection vp ON vp.source_version_id=v.id AND vp.workspace_id=s.workspace_id AND vp.parse_projection_id=r.parse_projection_id
   JOIN ingestion.parse_projection pp ON pp.id=vp.parse_projection_id AND pp.workspace_id=s.workspace_id AND pp.content_artifact_id=v.content_artifact_id
   WHERE s.workspace_id=? AND s.removed_at IS NULL AND v.security_status<>'quarantined'
    AND (?='' OR s.id>NULLIF(?,'')::uuid)
    AND (?='' OR v.id=NULLIF(?,'')::uuid)
    AND v.id=(SELECT latest.id FROM core.source_version latest WHERE latest.workspace_id=s.workspace_id AND latest.source_id=s.id ORDER BY latest.captured_at DESC,latest.id DESC LIMIT 1)
    AND EXISTS(SELECT 1 FROM ingestion.attempt ia WHERE ia.workspace_id=s.workspace_id AND ia.source_version_id=v.id AND ia.parse_projection_id=pp.id AND ia.status='chunked' AND ia.security_status='passed' AND ia.parser_id=pp.parser_id AND ia.parser_version=pp.parser_version AND ia.parser_config_hash=pp.parser_config_hash AND ia.schema_version=pp.schema_version)
    AND NOT EXISTS(SELECT 1 FROM ingestion.attempt ia WHERE ia.workspace_id=s.workspace_id AND ia.source_version_id=v.id AND ia.security_status='quarantined')
   ORDER BY s.id LIMIT ?`, string(query.WorkspaceID), string(query.AfterSourceID), string(query.AfterSourceID), string(query.SourceVersionID), string(query.SourceVersionID), query.Limit+1).Scan(&candidates).Error
		if err != nil {
			return err
		}
		if len(candidates) > query.Limit {
			candidates = candidates[:query.Limit]
			after := candidates[len(candidates)-1].SourceID
			result.NextAfterSourceID = &after
		}
		if len(candidates) == 0 {
			return nil
		}
		ids := make([]foundation.ID, len(candidates))
		for i, c := range candidates {
			ids[i] = c.SourceVersionID
		}
		// GetProfiles 仅执行集合读取。将值副本绑定到此快照；
		// 它不会开启另一事务或修改共享仓库。
		scoped := *repository
		scoped.database = tx
		views, err := scoped.GetProfiles(ctx, captureapp.ProfileBatchQuery{WorkspaceID: query.WorkspaceID, SourceVersionIDs: ids})
		if err != nil {
			return err
		}
		bySource := make(map[foundation.ID]captureapp.ProfileView, len(views))
		for _, view := range views {
			bySource[view.Profile.SourceVersionID] = view
		}
		for _, c := range candidates {
			view, ok := bySource[c.SourceVersionID]
			if !ok || view.Revision == nil || view.Revision.ID != c.RevisionID || !validID(c.SourceID) || !validID(c.ContentArtifactID) {
				return inconsistent(profileContextInvalidCode, "profile directory snapshot binding is invalid")
			}
			result.Items = append(result.Items, captureapp.ProfileDirectoryEntry{SourceID: c.SourceID, Title: c.Title, ContentArtifactID: c.ContentArtifactID, ContentHash: c.ContentHash, View: view})
		}
		return nil
	})
	if err != nil {
		return captureapp.ProfileDirectoryPage{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_DIRECTORY_QUERY_FAILED")
	}
	return result, nil
}

var _ captureapp.ProfileDirectoryReader = (*GORMProfileRepository)(nil)
