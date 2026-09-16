package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	anchorDiscoveryMaxBatch      = 100
	anchorDiscoveryMaxEvidence   = 256
	anchorDiscoveryMaxRequestIDs = 8
	anchorDiscoveryRequestBatch  = 32
	anchorDiscoveryRetryDelay    = 30 * time.Second
)

type anchorSourceDiscoveryModel struct {
	ID                string          `gorm:"column:id"`
	WorkspaceID       string          `gorm:"column:workspace_id"`
	ProcessingID      string          `gorm:"column:processing_id"`
	AnchorID          string          `gorm:"column:anchor_id"`
	ScopeVersion      int64           `gorm:"column:scope_version"`
	SourceID          string          `gorm:"column:source_id"`
	SourceVersionID   string          `gorm:"column:source_version_id"`
	ContentArtifactID string          `gorm:"column:content_artifact_id"`
	ParseProjectionID string          `gorm:"column:parse_projection_id"`
	SourceContentHash string          `gorm:"column:source_content_hash"`
	Status            string          `gorm:"column:status"`
	ProfileRevisionID *string         `gorm:"column:profile_revision_id"`
	Evidence          organizingJSONB `gorm:"column:evidence"`
	RequestIDs        organizingJSONB `gorm:"column:request_ids"`
	NextCheckAt       time.Time       `gorm:"column:next_check_at"`
	ErrorCode         *string         `gorm:"column:error_code"`
	Version           int64           `gorm:"column:version"`
	CreatedAt         time.Time       `gorm:"column:created_at"`
	UpdatedAt         time.Time       `gorm:"column:updated_at"`
}

func (anchorSourceDiscoveryModel) TableName() string { return "organizing.anchor_source_discovery" }

var _ app.AnchorDiscoveryStore = (*GORMAnchorStore)(nil)

func (row anchorSourceDiscoveryModel) discovery() (app.AnchorSourceDiscovery, error) {
	result := app.AnchorSourceDiscovery{
		ID: foundation.ID(row.ID), WorkspaceID: foundation.ID(row.WorkspaceID), ProcessingID: foundation.ID(row.ProcessingID),
		AnchorID: foundation.ID(row.AnchorID), ScopeVersion: row.ScopeVersion, Status: row.Status, Version: row.Version,
		Source:    domain.SynthesisSourceVersion{WorkspaceID: foundation.ID(row.WorkspaceID), SourceID: foundation.ID(row.SourceID), SourceVersionID: foundation.ID(row.SourceVersionID), ContentArtifactID: foundation.ID(row.ContentArtifactID), ParseProjectionID: foundation.ID(row.ParseProjectionID), ContentHash: row.SourceContentHash},
		ErrorCode: stringValue(row.ErrorCode), Evidence: []domain.SynthesisSourceRef{}, RequestIDs: []foundation.ID{},
	}
	if row.ProfileRevisionID != nil {
		result.ProfileRevisionID = foundation.ID(*row.ProfileRevisionID)
	}
	if err := json.Unmarshal(row.Evidence, &result.Evidence); err != nil || json.Unmarshal(row.RequestIDs, &result.RequestIDs) != nil || !anchorDiscoveryValid(result) {
		return app.AnchorSourceDiscovery{}, app.AnchorConflict()
	}
	return result, nil
}

func anchorDiscoveryValid(value app.AnchorSourceDiscovery) bool {
	if !validID(value.ID) || !validID(value.WorkspaceID) || !validID(value.ProcessingID) || !validID(value.AnchorID) || value.ScopeVersion < 1 || value.Source.Validate() != nil || value.Source.WorkspaceID != value.WorkspaceID || value.Version < 1 {
		return false
	}
	switch value.Status {
	case app.AnchorDiscoveryWaiting:
		return value.ProfileRevisionID == "" && len(value.Evidence) == 0 && len(value.RequestIDs) == 0
	case app.AnchorDiscoveryPrepared:
		return validID(value.ProfileRevisionID) && anchorDiscoveryEvidence(value, value.Evidence) && len(value.RequestIDs) == 0
	case app.AnchorDiscoveryRequested:
		return validID(value.ProfileRevisionID) && anchorDiscoveryEvidence(value, value.Evidence) && anchorDiscoveryIDs(value.RequestIDs)
	case app.AnchorDiscoveryStale:
		return len(value.RequestIDs) == 0 && (value.ProfileRevisionID == "" || (validID(value.ProfileRevisionID) && anchorDiscoveryEvidence(value, value.Evidence)))
	default:
		return false
	}
}

func anchorDiscoveryEvidence(discovery app.AnchorSourceDiscovery, evidence []domain.SynthesisSourceRef) bool {
	if len(evidence) < 1 || len(evidence) > anchorDiscoveryMaxEvidence {
		return false
	}
	seen := make(map[string]struct{}, len(evidence))
	for _, ref := range evidence {
		key, err := ref.IdentityKey()
		if err != nil || ref.Source != discovery.Source {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func anchorDiscoveryIDs(ids []foundation.ID) bool {
	if len(ids) < 1 || len(ids) > anchorDiscoveryMaxRequestIDs {
		return false
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !validID(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func (s *GORMAnchorStore) SeedAnchorSourceDiscoveries(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > anchorDiscoveryMaxBatch {
		return 0, app.AnchorInvalid()
	}
	created := 0
	err := s.within(ctx, true, func(ctx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`WITH candidates AS (
 SELECT p.workspace_id,p.id AS processing_id,a.id AS anchor_id,a.scope_version,
        p.source_id,p.source_version_id,p.content_artifact_id,p.parse_projection_id,p.source_content_hash
 FROM organizing.synthesis_processing p
 JOIN organizing.knowledge_anchor a ON a.workspace_id=p.workspace_id
 WHERE p.status<>'SKIPPED' AND p.fusion_request_id IS NULL
   AND NOT EXISTS (SELECT 1 FROM organizing.anchor_source_discovery d
     WHERE d.workspace_id=p.workspace_id AND d.source_version_id=p.source_version_id AND d.parse_projection_id=p.parse_projection_id
       AND d.anchor_id=a.id AND d.scope_version=a.scope_version)
 ORDER BY p.created_at,p.id,a.id LIMIT ?
)
INSERT INTO organizing.anchor_source_discovery(
 workspace_id,processing_id,anchor_id,scope_version,source_id,source_version_id,content_artifact_id,parse_projection_id,source_content_hash,
 status,evidence,request_ids,next_check_at,version,created_at,updated_at
)
SELECT workspace_id,processing_id,anchor_id,scope_version,source_id,source_version_id,content_artifact_id,parse_projection_id,source_content_hash,
 'WAITING_PROFILE','[]'::jsonb,'[]'::jsonb,clock_timestamp(),1,clock_timestamp(),clock_timestamp()
FROM candidates ON CONFLICT (workspace_id,source_version_id,parse_projection_id,anchor_id,scope_version) DO NOTHING`, limit)
		if result.Error != nil {
			return result.Error
		}
		created = int(result.RowsAffected)
		return nil
	})
	return created, err
}

func (s *GORMAnchorStore) ListDueAnchorSourceDiscoveries(ctx context.Context, limit int) ([]app.AnchorSourceDiscovery, error) {
	if limit < 1 || limit > anchorDiscoveryMaxBatch {
		return nil, app.AnchorInvalid()
	}
	result := make([]app.AnchorSourceDiscovery, 0)
	err := s.within(ctx, false, func(ctx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var rows []anchorSourceDiscoveryModel
		if err := tx.WithContext(ctx).Where("status IN ? AND next_check_at<=clock_timestamp()", []string{app.AnchorDiscoveryWaiting, app.AnchorDiscoveryPrepared}).Order("created_at,id").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			item, err := row.discovery()
			if err != nil {
				return err
			}
			result = append(result, item)
		}
		return nil
	})
	return result, err
}

// Freeze 保存唯一不可变的 Profile 与证据快照。行锁让并发扫描者返回已提交的胜出结果，即使其读取后又观察到更新的 Profile 指针。
func (s *GORMAnchorStore) FreezeAnchorSourceDiscovery(ctx context.Context, expected app.AnchorSourceDiscovery, profileRevisionID foundation.ID, evidence []domain.SynthesisSourceRef) (app.AnchorSourceDiscovery, error) {
	var result app.AnchorSourceDiscovery
	if !anchorDiscoveryExpected(expected) || !validID(profileRevisionID) || !anchorDiscoveryEvidence(expected, evidence) {
		return result, app.AnchorInvalid()
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return result, err
	}
	err = s.within(ctx, true, func(ctx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var row anchorSourceDiscoveryModel
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("workspace_id=? AND id=?", string(expected.WorkspaceID), string(expected.ID)).Take(&row).Error; err != nil {
			if gormNoRows(err) {
				return anchorNotFound()
			}
			return err
		}
		current, err := row.discovery()
		if err != nil {
			return err
		}
		if current.Status != app.AnchorDiscoveryWaiting {
			result = current
			return nil
		}
		if !anchorDiscoveryMatchesExpected(current, expected) {
			return app.AnchorConflict()
		}
		anchor, err := readAnchor(tx, current.WorkspaceID, current.AnchorID, true)
		if err != nil {
			return err
		}
		if anchor.ScopeVersion != current.ScopeVersion {
			return app.AnchorConflict()
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		profile := string(profileRevisionID)
		updated := tx.Model(&anchorSourceDiscoveryModel{}).Where("workspace_id=? AND id=? AND version=? AND status=?", row.WorkspaceID, row.ID, row.Version, app.AnchorDiscoveryWaiting).Updates(map[string]any{
			"status": app.AnchorDiscoveryPrepared, "profile_revision_id": profile, "evidence": organizingJSONB(encoded), "error_code": nil,
			"next_check_at": now, "version": row.Version + 1, "updated_at": now,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return app.AnchorConflict()
		}
		if err := tx.Where("workspace_id=? AND id=?", row.WorkspaceID, row.ID).Take(&row).Error; err != nil {
			return err
		}
		result, err = row.discovery()
		return err
	})
	return result, err
}

func anchorDiscoveryExpected(value app.AnchorSourceDiscovery) bool {
	return validID(value.ID) && validID(value.WorkspaceID) && validID(value.ProcessingID) && validID(value.AnchorID) && value.ScopeVersion > 0 && value.Source.Validate() == nil && value.Source.WorkspaceID == value.WorkspaceID && value.Version > 0
}

func anchorDiscoveryMatchesExpected(current, expected app.AnchorSourceDiscovery) bool {
	return current.ID == expected.ID && current.WorkspaceID == expected.WorkspaceID && current.ProcessingID == expected.ProcessingID && current.AnchorID == expected.AnchorID && current.ScopeVersion == expected.ScopeVersion && current.Source == expected.Source && current.Version == expected.Version
}

func (s *GORMAnchorStore) CompleteAnchorSourceDiscovery(ctx context.Context, expected app.AnchorSourceDiscovery, requestIDs []foundation.ID) error {
	if !anchorDiscoveryExpected(expected) || expected.Status != app.AnchorDiscoveryPrepared || !anchorDiscoveryEvidence(expected, expected.Evidence) || !anchorDiscoveryIDs(requestIDs) {
		return app.AnchorInvalid()
	}
	encoded, err := json.Marshal(requestIDs)
	if err != nil {
		return err
	}
	return s.within(ctx, true, func(ctx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var row anchorSourceDiscoveryModel
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("workspace_id=? AND id=?", string(expected.WorkspaceID), string(expected.ID)).Take(&row).Error; err != nil {
			if gormNoRows(err) {
				return anchorNotFound()
			}
			return err
		}
		current, err := row.discovery()
		if err != nil {
			return err
		}
		if current.Status == app.AnchorDiscoveryRequested {
			if reflect.DeepEqual(current.RequestIDs, requestIDs) {
				return nil
			}
			return app.AnchorConflict()
		}
		if current.Status != app.AnchorDiscoveryPrepared || !anchorDiscoveryMatchesExpected(current, expected) || current.ProfileRevisionID != expected.ProfileRevisionID || !reflect.DeepEqual(current.Evidence, expected.Evidence) {
			return app.AnchorConflict()
		}
		anchor, err := readAnchor(tx, current.WorkspaceID, current.AnchorID, true)
		if err != nil {
			return err
		}
		if anchor.ScopeVersion != current.ScopeVersion {
			return app.AnchorConflict()
		}
		if err := verifyDiscoveryRequests(tx, current, requestIDs); err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		updated := tx.Model(&anchorSourceDiscoveryModel{}).Where("workspace_id=? AND id=? AND version=? AND status=?", row.WorkspaceID, row.ID, row.Version, app.AnchorDiscoveryPrepared).Updates(map[string]any{
			"status": app.AnchorDiscoveryRequested, "request_ids": organizingJSONB(encoded), "error_code": nil,
			"next_check_at": now, "version": row.Version + 1, "updated_at": now,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return app.AnchorConflict()
		}
		return nil
	})
}

func verifyDiscoveryRequests(tx *gorm.DB, discovery app.AnchorSourceDiscovery, requestIDs []foundation.ID) error {
	var rows []anchorRecommendationRequestModel
	values := make([]string, len(requestIDs))
	for i, id := range requestIDs {
		values[i] = string(id)
	}
	if err := tx.Where("workspace_id=? AND id IN ?", string(discovery.WorkspaceID), values).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(requestIDs) {
		return app.AnchorConflict()
	}
	byID := make(map[string]anchorRecommendationRequestModel, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	position := 0
	for _, id := range requestIDs {
		row, found := byID[string(id)]
		if !found || row.Kind != domain.AnchorSourceAssociation || row.AnchorID == nil || *row.AnchorID != string(discovery.AnchorID) || row.ExpectedScopeVersion == nil || *row.ExpectedScopeVersion != discovery.ScopeVersion || row.Source == nil {
			return app.AnchorConflict()
		}
		var source domain.SynthesisSourceVersion
		var evidence []domain.SynthesisSourceRef
		if json.Unmarshal(*row.Source, &source) != nil || source != discovery.Source || json.Unmarshal(row.Evidence, &evidence) != nil || len(evidence) < 1 || len(evidence) > anchorDiscoveryRequestBatch || position+len(evidence) > len(discovery.Evidence) || !reflect.DeepEqual(evidence, discovery.Evidence[position:position+len(evidence)]) {
			return app.AnchorConflict()
		}
		position += len(evidence)
	}
	if position != len(discovery.Evidence) {
		return app.AnchorConflict()
	}
	return nil
}

func (s *GORMAnchorStore) DeferAnchorSourceDiscovery(ctx context.Context, expected app.AnchorSourceDiscovery, errorCode string, stale bool) error {
	if !anchorDiscoveryExpected(expected) || (expected.Status != app.AnchorDiscoveryWaiting && expected.Status != app.AnchorDiscoveryPrepared) || errorCode == "" {
		return app.AnchorInvalid()
	}
	return s.within(ctx, true, func(ctx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var row anchorSourceDiscoveryModel
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("workspace_id=? AND id=?", string(expected.WorkspaceID), string(expected.ID)).Take(&row).Error; err != nil {
			if gormNoRows(err) {
				return anchorNotFound()
			}
			return err
		}
		current, err := row.discovery()
		if err != nil {
			return err
		}
		if current.Status == app.AnchorDiscoveryRequested || current.Status == app.AnchorDiscoveryStale {
			return nil
		}
		if !anchorDiscoveryMatchesExpected(current, expected) || (current.Status == app.AnchorDiscoveryPrepared && (current.ProfileRevisionID != expected.ProfileRevisionID || !reflect.DeepEqual(current.Evidence, expected.Evidence))) {
			return app.AnchorConflict()
		}
		anchor, err := readAnchor(tx, current.WorkspaceID, current.AnchorID, true)
		if err != nil {
			return err
		}
		if anchor.ScopeVersion != current.ScopeVersion {
			stale = true
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		status := current.Status
		next := now.Add(anchorDiscoveryRetryDelay)
		if stale {
			status, next = app.AnchorDiscoveryStale, now
		}
		updated := tx.Model(&anchorSourceDiscoveryModel{}).Where("workspace_id=? AND id=? AND version=? AND status=?", row.WorkspaceID, row.ID, row.Version, row.Status).Updates(map[string]any{
			"status": status, "error_code": errorCode, "next_check_at": next, "version": row.Version + 1, "updated_at": now,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return app.AnchorConflict()
		}
		return nil
	})
}
