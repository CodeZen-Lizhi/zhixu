package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

const maxSourceProfileBackfillBatch = 100

// 共享的资格条件将忙碌来源排除在有界候选分页之外。
const sourceBackfillAdvanceReadySQL = `capture.status IN ('SOURCE_SAVED','READY','READY_DEGRADED','FETCH_FAILED','PROCESSING_FAILED')
			AND capture.fetch_status<>'RUNNING' AND capture.ingestion_status<>'RUNNING'
			AND capture.index_status<>'RUNNING' AND capture.profile_status<>'RUNNING'
			AND NOT EXISTS (
				SELECT 1 FROM ops.capture_outbox AS event
				LEFT JOIN workflow.run AS run ON run.id::text=event.payload->>'workflow_run_id' AND run.workspace_id=event.workspace_id
				WHERE event.workspace_id=capture.workspace_id AND event.capture_id=capture.id
					AND (event.payload->>'capture_id' IS DISTINCT FROM capture.id::text
						OR event.payload->>'workspace_id' IS DISTINCT FROM capture.workspace_id::text
						OR (event.published_at IS NULL AND event.poisoned_at IS NULL)
						OR (event.published_at IS NOT NULL AND (event.payload->>'workflow_run_id' IS NULL
							OR run.id IS NULL OR run.status NOT IN ('succeeded','failed','cancelled'))))
			)
			AND NOT EXISTS (
				SELECT 1 FROM ops.capture_attempt AS attempt
				LEFT JOIN workflow.run AS run ON run.id=attempt.workflow_run_id AND run.workspace_id=attempt.workspace_id
				WHERE attempt.workspace_id=capture.workspace_id AND attempt.capture_id=capture.id
					AND (attempt.status='RUNNING' OR run.id IS NULL OR run.status NOT IN ('succeeded','failed','cancelled'))
			)
			AND NOT EXISTS (
				SELECT 1 FROM learning.document_knowledge_profile AS profile
				JOIN learning.document_knowledge_profile_attempt AS attempt
					ON attempt.profile_id=profile.id AND attempt.workspace_id=profile.workspace_id
				LEFT JOIN workflow.run AS run ON run.id=attempt.workflow_run_id AND run.workspace_id=attempt.workspace_id
				WHERE profile.workspace_id=capture.workspace_id AND profile.capture_id=capture.id
					AND (attempt.status='RUNNING' OR run.id IS NULL OR run.status NOT IN ('succeeded','failed','cancelled'))
			)`

var _ captureapp.SourceBackfillScheduler = (*GORMRepository)(nil)

// BackfillSources 将当前本地工作区文件纳入 Capture
// 流水线。每个来源版本仅调度一个持久事件；
// 重试仍由常规 Capture 工作流负责。
func (repository *GORMRepository) BackfillSources(ctx context.Context, limit int) (int, error) {
	if err := repository.ready(ctx); err != nil {
		return 0, err
	}
	if limit < 1 || limit > maxSourceProfileBackfillBatch {
		return 0, invalid("CAPTURE_SOURCE_BACKFILL_INVALID", "capture source backfill limit is invalid")
	}

	candidates, err := gormLoadSourceProfileBackfillCandidates(ctx, repository.database, limit)
	if err != nil {
		return 0, err
	}

	processed := 0
	for _, candidate := range candidates {
		changed := false
		err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
			var transactionErr error
			changed, transactionErr = gormBackfillSourceProfile(callbackCtx, tx, candidate)
			return transactionErr
		})
		if err != nil {
			return processed, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_FAILED")
		}
		if changed {
			processed++
		}
	}
	return processed, nil
}

type sourceProfileBackfillCandidate struct {
	workspaceID      foundation.ID
	sourceID         foundation.ID
	sourceVersionID  foundation.ID
	logicalName      string
	originalLocation string
	contentHash      string
}

func gormLoadSourceProfileBackfillCandidates(ctx context.Context, database *gorm.DB, limit int) ([]sourceProfileBackfillCandidate, error) {
	rows, err := gormCaptureRows(database.WithContext(ctx), `WITH latest AS (
		SELECT DISTINCT ON (source.id)
			source.workspace_id::text AS workspace_id,source.id::text AS source_id,source_version.id::text AS source_version_id,
			source.logical_name,source.original_location,source_version.content_hash,source_version.captured_at
		FROM core.workspace AS workspace
		JOIN core.source AS source ON source.workspace_id=workspace.id
		JOIN core.source_version AS source_version
			ON source_version.source_id=source.id AND source_version.workspace_id=source.workspace_id
		JOIN core.content_artifact AS artifact
			ON artifact.id=source_version.content_artifact_id AND artifact.workspace_id=source.workspace_id
		WHERE workspace.status='active' AND source.removed_at IS NULL
			AND source.type IN ('markdown','text','pdf','html')
			AND NOT EXISTS (SELECT 1 FROM core.source_version newer WHERE newer.source_id=source.id
				AND (newer.captured_at,newer.id)>(source_version.captured_at,source_version.id))
		ORDER BY source.id,source_version.captured_at DESC,source_version.id DESC
	)
	SELECT workspace_id,source_id,source_version_id,logical_name,original_location,content_hash
	FROM latest
	WHERE NOT EXISTS (SELECT 1 FROM learning.document_knowledge_profile profile
		WHERE profile.workspace_id=latest.workspace_id::uuid AND profile.source_version_id=latest.source_version_id::uuid)
	AND NOT EXISTS (SELECT 1 FROM core.capture capture
		WHERE capture.workspace_id=latest.workspace_id::uuid AND capture.source_id=latest.source_id::uuid
		AND (capture.kind<>'FILE' OR capture.original_location<>latest.original_location
			OR capture.latest_source_version_id=latest.source_version_id::uuid
			OR NOT (`+sourceBackfillAdvanceReadySQL+`)))
	ORDER BY captured_at,source_id LIMIT ?`, limit)
	if err != nil {
		return nil, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_LIST_FAILED")
	}
	defer rows.Close()

	result := make([]sourceProfileBackfillCandidate, 0, limit)
	for rows.Next() {
		var rawWorkspaceID, rawSourceID, rawVersionID string
		var candidate sourceProfileBackfillCandidate
		if err := rows.Scan(&rawWorkspaceID, &rawSourceID, &rawVersionID, &candidate.logicalName, &candidate.originalLocation, &candidate.contentHash); err != nil {
			return nil, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_LIST_FAILED")
		}
		var parseErr error
		if candidate.workspaceID, parseErr = foundation.ParseID(rawWorkspaceID); parseErr != nil {
			return nil, inconsistent("CAPTURE_SOURCE_BACKFILL_BINDING_INVALID", "source backfill workspace identity is invalid")
		}
		if candidate.sourceID, parseErr = foundation.ParseID(rawSourceID); parseErr != nil {
			return nil, inconsistent("CAPTURE_SOURCE_BACKFILL_BINDING_INVALID", "source backfill source identity is invalid")
		}
		if candidate.sourceVersionID, parseErr = foundation.ParseID(rawVersionID); parseErr != nil {
			return nil, inconsistent("CAPTURE_SOURCE_BACKFILL_BINDING_INVALID", "source backfill version identity is invalid")
		}
		if !validHash(candidate.contentHash) {
			return nil, inconsistent("CAPTURE_SOURCE_BACKFILL_BINDING_INVALID", "source backfill content hash is invalid")
		}
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_LIST_FAILED")
	}
	return result, nil
}

func gormBackfillSourceProfile(ctx context.Context, tx *gorm.DB, candidate sourceProfileBackfillCandidate) (bool, error) {
	current, found, err := gormLockCurrentBackfillSource(ctx, tx, candidate)
	if err != nil || !found {
		return false, err
	}
	// 扫描可能观察到旧版本。来源锁确保入队前，
	// 此比较与来源版本注册之间保持稳定。
	if current.sourceVersionID != candidate.sourceVersionID {
		return false, nil
	}

	capture, hasCapture, err := gormLockBackfillCapture(ctx, tx, current.workspaceID, current.sourceID)
	if err != nil {
		return false, err
	}
	if !hasCapture {
		return gormCreateBackfillCapture(ctx, tx, current)
	}
	if capture.Kind != domain.KindFile || capture.OriginalLocation != current.originalLocation || capture.SourceID != current.sourceID {
		return false, nil
	}
	if capture.LatestSourceVersionID == current.sourceVersionID {
		return false, nil
	}

	profileExists, err := gormBackfillProfileExists(ctx, tx, current.workspaceID, current.sourceVersionID)
	if err != nil {
		return false, err
	}
	// 此不可变版本已有画像，说明先前的调度决策
	// 已经持久化。不得伪造第二个事件来修复遗留状态。
	if profileExists {
		return false, nil
	}
	ready, err := gormBackfillVersionAdvanceReady(ctx, tx, capture)
	if err != nil {
		return false, err
	}
	if !ready {
		return false, nil
	}

	updated, err := gormAdvanceBackfillCapture(ctx, tx, capture, current.sourceVersionID)
	if err != nil {
		return false, err
	}
	if err := gormInsertBackfillProfileAndOutbox(ctx, tx, updated, current.sourceVersionID); err != nil {
		return false, err
	}
	return true, nil
}

func gormLockCurrentBackfillSource(ctx context.Context, tx *gorm.DB, candidate sourceProfileBackfillCandidate) (sourceProfileBackfillCandidate, bool, error) {
	// 读取最新版本前先取得 Source 锁。单条
	// 横向 SELECT FOR UPDATE 可能保留等待锁之前的快照。
	lock, err := gormCaptureRawRow(tx.WithContext(ctx), `SELECT id::text FROM core.source WHERE workspace_id=? AND id=? FOR UPDATE`, string(candidate.workspaceID), string(candidate.sourceID))
	if err != nil {
		return sourceProfileBackfillCandidate{}, false, err
	}
	var lockedID string
	if err := lock.Scan(&lockedID); gormCaptureNoRows(err) {
		return sourceProfileBackfillCandidate{}, false, nil
	} else if err != nil {
		return sourceProfileBackfillCandidate{}, false, err
	}
	row, err := gormCaptureRawRow(tx.WithContext(ctx), `SELECT source.workspace_id::text AS workspace_id,source.id::text AS source_id,source_version.id::text AS source_version_id,
		source.logical_name,source.original_location,source_version.content_hash
		FROM core.workspace AS workspace
		JOIN core.source AS source ON source.workspace_id=workspace.id
		JOIN LATERAL (
			SELECT version.id,version.content_hash FROM core.source_version AS version
			JOIN core.content_artifact AS artifact ON artifact.id=version.content_artifact_id AND artifact.workspace_id=source.workspace_id
			WHERE version.source_id=source.id AND version.workspace_id=source.workspace_id
			AND NOT EXISTS (SELECT 1 FROM core.source_version newer WHERE newer.source_id=source.id
				AND (newer.captured_at,newer.id)>(version.captured_at,version.id))
			ORDER BY version.captured_at DESC,version.id DESC LIMIT 1
		) AS source_version ON true
		WHERE workspace.id=? AND workspace.status='active' AND source.id=? AND source.removed_at IS NULL
			AND source.type IN ('markdown','text','pdf','html') FOR UPDATE OF source`, string(candidate.workspaceID), string(candidate.sourceID))
	if err != nil {
		return sourceProfileBackfillCandidate{}, false, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_LOCK_FAILED")
	}
	var rawWorkspaceID, rawSourceID, rawVersionID string
	var current sourceProfileBackfillCandidate
	if err := row.Scan(&rawWorkspaceID, &rawSourceID, &rawVersionID, &current.logicalName, &current.originalLocation, &current.contentHash); gormCaptureNoRows(err) {
		return sourceProfileBackfillCandidate{}, false, nil
	} else if err != nil {
		return sourceProfileBackfillCandidate{}, false, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_LOCK_FAILED")
	}
	var parseErr error
	if current.workspaceID, parseErr = foundation.ParseID(rawWorkspaceID); parseErr != nil {
		return sourceProfileBackfillCandidate{}, false, inconsistent("CAPTURE_SOURCE_BACKFILL_BINDING_INVALID", "locked source workspace identity is invalid")
	}
	if current.sourceID, parseErr = foundation.ParseID(rawSourceID); parseErr != nil {
		return sourceProfileBackfillCandidate{}, false, inconsistent("CAPTURE_SOURCE_BACKFILL_BINDING_INVALID", "locked source identity is invalid")
	}
	if current.sourceVersionID, parseErr = foundation.ParseID(rawVersionID); parseErr != nil {
		return sourceProfileBackfillCandidate{}, false, inconsistent("CAPTURE_SOURCE_BACKFILL_BINDING_INVALID", "locked source version identity is invalid")
	}
	if !validHash(current.contentHash) {
		return sourceProfileBackfillCandidate{}, false, inconsistent("CAPTURE_SOURCE_BACKFILL_BINDING_INVALID", "locked source content hash is invalid")
	}
	return current, true, nil
}

func gormLockBackfillCapture(ctx context.Context, tx *gorm.DB, workspaceID, sourceID foundation.ID) (domain.Capture, bool, error) {
	row, err := gormCaptureRawRow(tx.WithContext(ctx), captureSelect+` WHERE capture.workspace_id=? AND capture.source_id=? FOR UPDATE`, string(workspaceID), string(sourceID))
	if err != nil {
		return domain.Capture{}, false, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_CAPTURE_LOCK_FAILED")
	}
	capture, err := scanCapture(row)
	if gormCaptureNoRows(err) {
		return domain.Capture{}, false, nil
	}
	if err != nil {
		return domain.Capture{}, false, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_CAPTURE_LOCK_FAILED")
	}
	return capture, true, nil
}

func gormCreateBackfillCapture(ctx context.Context, tx *gorm.DB, source sourceProfileBackfillCandidate) (bool, error) {
	generator := foundation.NewUUIDGenerator(nil)
	captureID, err := generator.New()
	if err != nil {
		return false, err
	}
	profileID, err := generator.New()
	if err != nil {
		return false, err
	}
	outboxID, err := generator.New()
	if err != nil {
		return false, err
	}
	row, err := gormCaptureRawRow(tx.WithContext(ctx), `INSERT INTO core.capture(
		id,workspace_id,kind,display_name,original_location,original_url,original_input_hash,
		source_id,latest_source_version_id,status,fetch_status,ingestion_status,index_status,
		profile_status,failure_stage,error_code,retryable,version,captured_at,updated_at
	) VALUES(?,?, 'FILE',?,?,NULL,?,?,?,'SOURCE_SAVED','NOT_APPLICABLE','PENDING','PENDING','PENDING','', '',false,1,clock_timestamp(),clock_timestamp())
	RETURNING `+captureReturning, string(captureID), string(source.workspaceID), source.logicalName,
		source.originalLocation, source.contentHash, string(source.sourceID), string(source.sourceVersionID))
	if err != nil {
		return false, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_CAPTURE_INSERT_FAILED")
	}
	capture, err := scanCapture(row)
	if err != nil {
		return false, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_CAPTURE_INSERT_FAILED")
	}
	if err := gormInsertBackfillProfileAndOutboxWithIDs(ctx, tx, capture, source.sourceVersionID, profileID, outboxID); err != nil {
		return false, err
	}
	return true, nil
}

func gormBackfillProfileExists(ctx context.Context, tx *gorm.DB, workspaceID, sourceVersionID foundation.ID) (bool, error) {
	row, err := gormCaptureRawRow(tx.WithContext(ctx), `SELECT EXISTS(
		SELECT 1 FROM learning.document_knowledge_profile
		WHERE workspace_id=? AND source_version_id=?)`, string(workspaceID), string(sourceVersionID))
	if err != nil {
		return false, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_PROFILE_QUERY_FAILED")
	}
	var exists bool
	if err := row.Scan(&exists); err != nil {
		return false, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_PROFILE_QUERY_FAILED")
	}
	return exists, nil
}

func gormBackfillVersionAdvanceReady(ctx context.Context, tx *gorm.DB, capture domain.Capture) (bool, error) {
	row, err := gormCaptureRawRow(tx.WithContext(ctx), `SELECT `+sourceBackfillAdvanceReadySQL+`
		FROM core.capture AS capture WHERE capture.workspace_id=? AND capture.id=?`, string(capture.WorkspaceID), string(capture.ID))
	if err != nil {
		return false, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_READINESS_FAILED")
	}
	var ready bool
	if err := row.Scan(&ready); err != nil {
		return false, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_READINESS_FAILED")
	}
	return ready, nil
}

func gormAdvanceBackfillCapture(ctx context.Context, tx *gorm.DB, capture domain.Capture, sourceVersionID foundation.ID) (domain.Capture, error) {
	row, err := gormCaptureRawRow(tx.WithContext(ctx), `UPDATE core.capture SET
		latest_source_version_id=?,status='SOURCE_SAVED',fetch_status='NOT_APPLICABLE',
		ingestion_status='PENDING',index_status='PENDING',profile_status='PENDING',
		failure_stage='',error_code='',retryable=false,version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=? AND id=? AND version=? RETURNING `+captureReturning,
		string(sourceVersionID), string(capture.WorkspaceID), string(capture.ID), capture.Version)
	if err != nil {
		return domain.Capture{}, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_CAPTURE_ADVANCE_FAILED")
	}
	updated, err := scanCapture(row)
	if gormCaptureNoRows(err) {
		return domain.Capture{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_SOURCE_BACKFILL_CAPTURE_CONFLICT", true, errors.New("capture changed before source backfill advance"))
	}
	if err != nil {
		return domain.Capture{}, classifyGORMCapture(ctx, err, "CAPTURE_SOURCE_BACKFILL_CAPTURE_ADVANCE_FAILED")
	}
	return updated, nil
}

func gormInsertBackfillProfileAndOutbox(ctx context.Context, tx *gorm.DB, capture domain.Capture, sourceVersionID foundation.ID) error {
	generator := foundation.NewUUIDGenerator(nil)
	profileID, err := generator.New()
	if err != nil {
		return err
	}
	outboxID, err := generator.New()
	if err != nil {
		return err
	}
	return gormInsertBackfillProfileAndOutboxWithIDs(ctx, tx, capture, sourceVersionID, profileID, outboxID)
}

func gormInsertBackfillProfileAndOutboxWithIDs(ctx context.Context, tx *gorm.DB, capture domain.Capture, sourceVersionID, profileID, outboxID foundation.ID) error {
	result := tx.WithContext(ctx).Exec(`INSERT INTO learning.document_knowledge_profile(
		id,workspace_id,capture_id,source_version_id,current_revision_id,status,error_code,retryable,version,created_at,updated_at
	) VALUES(?,?,?, ?,NULL,'PENDING','',false,1,clock_timestamp(),clock_timestamp())`,
		string(profileID), string(capture.WorkspaceID), string(capture.ID), string(sourceVersionID))
	if result.Error != nil {
		return classifyGORMCapture(ctx, result.Error, "CAPTURE_SOURCE_BACKFILL_PROFILE_INSERT_FAILED")
	}
	if result.RowsAffected != 1 {
		return inconsistent("CAPTURE_SOURCE_BACKFILL_PROFILE_INSERT_INCOMPLETE", "source profile was not inserted")
	}
	payload, err := json.Marshal(map[string]string{"capture_id": string(capture.ID), "workspace_id": string(capture.WorkspaceID)})
	if err != nil {
		return inconsistent("CAPTURE_SOURCE_BACKFILL_OUTBOX_ENCODING_FAILED", "capture source backfill outbox payload could not be encoded")
	}
	eventKey := fmt.Sprintf("capture-source-backfill:%s", sourceVersionID)
	result = tx.WithContext(ctx).Exec(`INSERT INTO ops.capture_outbox(
		id,workspace_id,capture_id,schema_version,event_type,event_key,payload,available_at,
		attempt_count,manual_recovery_required,version,created_at,updated_at
	) VALUES(?,?,?,'capture-process-event/v1','capture.process_requested',?,?::jsonb,clock_timestamp(),0,false,1,clock_timestamp(),clock_timestamp())`,
		string(outboxID), string(capture.WorkspaceID), string(capture.ID), eventKey, captureJSONB(payload))
	if result.Error != nil {
		return classifyGORMCapture(ctx, result.Error, "CAPTURE_SOURCE_BACKFILL_OUTBOX_INSERT_FAILED")
	}
	if result.RowsAffected != 1 {
		return inconsistent("CAPTURE_SOURCE_BACKFILL_OUTBOX_INSERT_INCOMPLETE", "capture source backfill outbox was not inserted")
	}
	return nil
}
