package postgres

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

const (
	gormLockWorkspaceSQL = `SELECT id::text
		FROM core.workspace
		WHERE id=?
		FOR UPDATE`

	gormLockReviewAnswerSQL = `SELECT id::text
		FROM learning.review_answer
		WHERE workspace_id=? AND id=?
		FOR UPDATE`

	gormLockCommandSQL = `SELECT pg_advisory_xact_lock(hashtextextended(?,0))`

	gormReservationSQL = `SELECT workspace_id::text,review_answer_id::text,idempotency_key,request_hash,
		source_snapshot,source_snapshot_digest,attempt_no,COALESCE(artifact_digest,''),status,created_at,updated_at
		FROM learning.learning_path_creation_reservation
		WHERE workspace_id=? AND review_answer_id=?`

	gormReservationForUpdateSQL = gormReservationSQL + ` FOR UPDATE`

	gormReviewAnswerSnapshotSQL = `SELECT score,card_id::text,scorer_version
		FROM learning.review_answer
		WHERE workspace_id=? AND id=?
		FOR KEY SHARE`

	gormReviewCardStatusSQL = `SELECT status
		FROM learning.review_card
		WHERE workspace_id=? AND id=?
		FOR KEY SHARE`

	gormReviewEvidenceSQL = `WITH requested AS (
		SELECT request_no::integer,claim_id,source_version_id,source_span_id,evidence_hash
		FROM unnest(?::uuid[],?::uuid[],?::uuid[],?::text[]) WITH ORDINALITY
			AS value(claim_id,source_version_id,source_span_id,evidence_hash,request_no)
	)
	SELECT requested.request_no,requested.claim_id::text,frozen.index_version_id::text,frozen.chunk_id::text,
		requested.source_version_id::text,requested.source_span_id::text,requested.evidence_hash,topic.id::text
	FROM requested
	JOIN learning.review_card AS card
	  ON card.workspace_id=? AND card.id=? AND card.status='APPROVED' AND card.claim_id=requested.claim_id
	JOIN LATERAL (
		SELECT true AS matched
		FROM jsonb_to_recordset(card.evidence) AS evidence(
			schema_version text,claim_id uuid,source_version_id uuid,source_span_id uuid,evidence_hash text
		)
		WHERE evidence.schema_version='review-evidence/v1'
		  AND evidence.claim_id=requested.claim_id
		  AND evidence.source_version_id=requested.source_version_id
		  AND evidence.source_span_id=requested.source_span_id
		  AND evidence.evidence_hash=requested.evidence_hash
		LIMIT 1
	) AS card_evidence ON true
	JOIN core.claim AS claim
	  ON claim.workspace_id=? AND claim.id=requested.claim_id AND claim.status='CONFIRMED'
	JOIN core.claim_source AS claim_source
	  ON claim_source.workspace_id=? AND claim_source.claim_id=requested.claim_id
	 AND claim_source.source_version_id=requested.source_version_id
	 AND claim_source.source_span_id=requested.source_span_id
	 AND claim_source.evidence_hash=requested.evidence_hash
	 AND claim_source.support_type='SUPPORTS'
	JOIN core.source_version AS source_version
	  ON source_version.workspace_id=? AND source_version.id=requested.source_version_id
	 AND source_version.security_status <> 'quarantined'
	JOIN LATERAL (
		SELECT active_index.id AS index_version_id,chunk.id AS chunk_id
		FROM retrieval.index_version AS active_index
		JOIN retrieval.index_manifest_source AS source_manifest
		  ON source_manifest.workspace_id=active_index.workspace_id
		 AND source_manifest.index_version_id=active_index.id
		 AND source_manifest.source_version_id=requested.source_version_id
		 AND source_manifest.selection_status='included'
		JOIN ingestion.canonical_chunk AS chunk
		  ON chunk.workspace_id=active_index.workspace_id
		 AND chunk.parse_projection_id=source_manifest.parse_projection_id
		 AND chunk.source_span_id=requested.source_span_id
		JOIN retrieval.index_manifest_chunk AS chunk_manifest
		  ON chunk_manifest.workspace_id=active_index.workspace_id
		 AND chunk_manifest.index_version_id=active_index.id
		 AND chunk_manifest.chunk_id=chunk.id
		 AND chunk_manifest.content_hash=chunk.content_hash
		WHERE active_index.workspace_id=? AND active_index.status='active'
		ORDER BY chunk_manifest.sequence,chunk_manifest.chunk_id
		LIMIT 1
	) AS frozen ON true
	LEFT JOIN LATERAL (
		SELECT candidate.id
		FROM core.relation AS relation
		JOIN core.topic AS candidate
		  ON candidate.workspace_id=relation.workspace_id AND candidate.id=relation.target_node_id
		WHERE relation.workspace_id=?
		  AND relation.source_node_type='CLAIM' AND relation.source_node_id=requested.claim_id
		  AND relation.target_node_type='TOPIC' AND relation.relation_type='BELONGS_TO'
		  AND relation.status='CONFIRMED'
		ORDER BY candidate.id
		LIMIT 1
	) AS topic ON true
	WHERE core.knowledge_validate_provenance_binding(?,requested.source_version_id,requested.source_span_id)
	  AND COALESCE((
		SELECT attempt.security_status
		FROM ingestion.attempt AS attempt
		WHERE attempt.workspace_id=? AND attempt.source_version_id=requested.source_version_id
		ORDER BY attempt.started_at DESC,attempt.id DESC
		LIMIT 1
	  ),'passed') <> 'quarantined'
	ORDER BY requested.request_no`

	gormResultReceiptSQL = `SELECT request_hash,command_type,path_id::text,expected_version,path_version,response
		FROM learning.learning_path_command
		WHERE workspace_id=? AND idempotency_key=?`

	gormPathByAnswerSQL = `SELECT id::text,workspace_id::text,origin_type,interview_session_id::text,
		interview_report_id::text,review_answer_id::text,source_policy_version,artifact_id::text,
		artifact_revision_id::text,artifact_version,status,version,created_at,updated_at
		FROM learning.learning_path
		WHERE workspace_id=? AND review_answer_id=? AND origin_type='REVIEW'`

	gormPathByAnswerForUpdateSQL = gormPathByAnswerSQL + ` FOR UPDATE`

	gormPathByIDSQL = `SELECT id::text,workspace_id::text,origin_type,interview_session_id::text,
		interview_report_id::text,review_answer_id::text,source_policy_version,artifact_id::text,
		artifact_revision_id::text,artifact_version,status,version,created_at,updated_at
		FROM learning.learning_path
		WHERE workspace_id=? AND id=? AND origin_type='REVIEW'`

	gormPathByIDForUpdateSQL = gormPathByIDSQL + ` FOR UPDATE`

	gormStepsSQL = `SELECT id::text,workspace_id::text,path_id::text,step_no,claim_id::text,
		topic_id::text,source_version_id::text,source_span_id::text,evidence_hash,title,rationale,
		status,version,created_at,updated_at
		FROM learning.learning_path_step
		WHERE workspace_id=? AND path_id=?
		ORDER BY step_no,id`

	gormStepsForUpdateSQL = gormStepsSQL + ` FOR UPDATE`

	gormReopenReservationSQL = `UPDATE learning.learning_path_creation_reservation
		SET source_snapshot=?::jsonb,source_snapshot_digest=?,attempt_no=attempt_no+1,
			artifact_digest=NULL,path_id=NULL,artifact_id=NULL,artifact_revision_id=NULL,artifact_version=NULL,
			status='PENDING',prepared_at=NULL,completed_at=NULL,abandoned_at=NULL,
			created_at=statement_timestamp(),updated_at=statement_timestamp()
		WHERE workspace_id=? AND review_answer_id=? AND idempotency_key=? AND request_hash=?
		  AND status='ABANDONED' AND attempt_no=?
		RETURNING attempt_no,created_at,updated_at`

	gormInsertReservationSQL = `INSERT INTO learning.learning_path_creation_reservation(
		workspace_id,review_answer_id,idempotency_key,request_hash,source_snapshot,source_snapshot_digest,
		attempt_no,status,created_at,updated_at
	) VALUES(?,?,?,?,?::jsonb,?,1,'PENDING',statement_timestamp(),statement_timestamp())
	RETURNING created_at,updated_at`

	gormPrepareReservationSQL = `UPDATE learning.learning_path_creation_reservation
		SET artifact_digest=?,prepared_at=statement_timestamp(),updated_at=statement_timestamp()
		WHERE workspace_id=? AND review_answer_id=? AND status='PENDING' AND artifact_digest IS NULL
		  AND attempt_no=?
		RETURNING updated_at`

	gormInsertResultReceiptSQL = `INSERT INTO learning.learning_path_command(
		workspace_id,idempotency_key,request_hash,command_type,path_id,expected_version,path_version,response,created_at
	) VALUES(?,?,?,?,?,?,?,?::jsonb,statement_timestamp())`

	gormCompleteReservationSQL = `UPDATE learning.learning_path_creation_reservation
		SET status='COMPLETED',path_id=?,artifact_id=?,artifact_revision_id=?,artifact_version=?,
			completed_at=statement_timestamp(),updated_at=statement_timestamp()
		WHERE workspace_id=? AND review_answer_id=? AND status='PENDING'
		  AND idempotency_key=? AND request_hash=? AND source_snapshot_digest=? AND artifact_digest=?
		  AND attempt_no=?`

	gormReleasePathHoldSQL = `DELETE FROM learning.artifact_visibility_hold
		WHERE workspace_id=? AND artifact_id=? AND owner_type='LEARNING_PATH_CREATE'
		  AND owner_id=? AND review_answer_id=? AND owner_role='PATH'
		  AND attempt_digest=? AND disposition='ACTIVE'`

	gormUpdatePathStatusSQL = `UPDATE learning.learning_path
		SET status=?,version=version+1,updated_at=?
		WHERE workspace_id=? AND id=? AND version=?`

	gormUpdateStepSQL = `UPDATE learning.learning_path_step
		SET status=?,version=version+1,updated_at=?
		WHERE workspace_id=? AND path_id=? AND id=? AND version=?`

	gormUpdateParentPathSQL = `UPDATE learning.learning_path
		SET status=?,version=version+1,updated_at=?
		WHERE workspace_id=? AND id=? AND version=?`

	gormMaintainReservationsSQL = `WITH candidates AS (
		SELECT workspace_id,review_answer_id
		FROM learning.learning_path_creation_reservation
		WHERE status='PENDING' AND updated_at <= ?
		ORDER BY updated_at,workspace_id,review_answer_id
		LIMIT ? FOR UPDATE SKIP LOCKED
	), abandoned AS (
		UPDATE learning.learning_path_creation_reservation AS reservation
		SET status='ABANDONED',abandoned_at=statement_timestamp(),updated_at=statement_timestamp()
		FROM candidates
		WHERE reservation.workspace_id=candidates.workspace_id
		  AND reservation.review_answer_id=candidates.review_answer_id
		  AND reservation.status='PENDING'
		RETURNING reservation.workspace_id,reservation.review_answer_id,reservation.artifact_digest
	), orphaned AS (
		UPDATE learning.artifact_visibility_hold AS hold
		SET disposition='ORPHANED'
		FROM abandoned
		WHERE hold.workspace_id=abandoned.workspace_id
		  AND hold.owner_type='LEARNING_PATH_CREATE'
		  AND hold.owner_id=abandoned.review_answer_id
		  AND hold.review_answer_id=abandoned.review_answer_id
		  AND hold.owner_role='PATH'
		  AND hold.attempt_digest=abandoned.artifact_digest
		  AND hold.disposition='ACTIVE'
		RETURNING hold.artifact_id
	)
	SELECT (SELECT count(*) FROM abandoned),(SELECT count(*) FROM orphaned)`
)

func gormLearningPathRawRow(database *gorm.DB, query string, args ...any) (*sql.Row, error) {
	if !validLearningPathGORMDatabase(database) {
		return nil, errors.New("learning path GORM database is unavailable")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("learning path GORM query returned nil row")
	}
	return row, nil
}

func gormLearningPathRawRows(database *gorm.DB, query string, args ...any) (*sql.Rows, error) {
	if !validLearningPathGORMDatabase(database) {
		return nil, errors.New("learning path GORM database is unavailable")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("learning path GORM query returned nil rows")
	}
	return rows, nil
}

func gormLearningPathExec(ctx context.Context, database *gorm.DB, query string, args ...any) (int64, error) {
	if !validLearningPathGORMDatabase(database) {
		return 0, errors.New("learning path GORM database is unavailable")
	}
	result := database.WithContext(ctx).Exec(query, args...)
	return result.RowsAffected, result.Error
}
