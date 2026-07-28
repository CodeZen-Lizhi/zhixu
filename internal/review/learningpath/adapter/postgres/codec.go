package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	pathapp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	pathdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
	"github.com/jackc/pgx/v5"
)

type rowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type queryer interface {
	rowQueryer
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadReservation(ctx context.Context, db rowQueryer, workspaceID, answerID foundation.ID, lock bool) (pathapp.Reservation, bool, error) {
	query := `SELECT workspace_id::text,review_answer_id::text,idempotency_key,request_hash,
		source_snapshot,source_snapshot_digest,attempt_no,COALESCE(artifact_digest,''),status,created_at,updated_at
		FROM learning.learning_path_creation_reservation
		WHERE workspace_id=$1 AND review_answer_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var value pathapp.Reservation
	var snapshotJSON []byte
	var status string
	err := db.QueryRow(ctx, query, string(workspaceID), string(answerID)).Scan(
		&value.WorkspaceID, &value.ReviewAnswerID, &value.IdempotencyKey, &value.RequestHash,
		&snapshotJSON, &value.SourceSnapshotDigest, &value.AttemptNo, &value.ArtifactDigest, &status,
		&value.CreatedAt, &value.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return pathapp.Reservation{}, false, nil
	}
	if err != nil {
		return pathapp.Reservation{}, false, classify(err)
	}
	value.Status = pathapp.ReservationStatus(status)
	snapshot, err := decodeReviewSnapshot(snapshotJSON, value.SourceSnapshotDigest)
	if err != nil {
		return pathapp.Reservation{}, false, err
	}
	value.SourceSnapshot = snapshot
	if err := validateReservation(value, workspaceID, answerID); err != nil {
		return pathapp.Reservation{}, false, err
	}
	return value, true, nil
}

func decodeReviewSnapshot(encoded []byte, wantDigest string) (pathapp.ReviewSnapshot, error) {
	var snapshot pathapp.ReviewSnapshot
	if err := decodeStrict(encoded, &snapshot); err != nil {
		return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted review path snapshot is invalid")
	}
	canonical, _, digest, err := pathapp.CanonicalReviewSnapshot(snapshot)
	if err != nil || digest != wantDigest {
		return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted review path snapshot digest is invalid")
	}
	return canonical, nil
}

func validateReservation(value pathapp.Reservation, workspaceID, answerID foundation.ID) error {
	if value.WorkspaceID != workspaceID || value.ReviewAnswerID != answerID || value.SourceSnapshot.ReviewAnswerID != answerID ||
		!validID(value.WorkspaceID) || !validID(value.ReviewAnswerID) || !validKey(value.IdempotencyKey) ||
		!validHash(value.RequestHash) || !validHash(value.SourceSnapshotDigest) || value.AttemptNo < 1 ||
		(value.ArtifactDigest != "" && !validHash(value.ArtifactDigest)) || value.CreatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted learning path reservation is invalid")
	}
	switch value.Status {
	case pathapp.ReservationPending, pathapp.ReservationCompleted, pathapp.ReservationAbandoned:
		return nil
	default:
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted learning path reservation status is invalid")
	}
}

// loadReviewSnapshot reads the immutable Answer score once and validates every
// scored Evidence against the currently approved Card and formal source graph.
func loadReviewSnapshot(ctx context.Context, db queryer, workspaceID, answerID foundation.ID) (pathapp.ReviewSnapshot, error) {
	var scoreJSON []byte
	var cardID *string
	var scorerVersion string
	err := db.QueryRow(ctx, `SELECT score,card_id::text,scorer_version
		FROM learning.review_answer WHERE workspace_id=$1 AND id=$2 FOR KEY SHARE`,
		string(workspaceID), string(answerID)).Scan(&scoreJSON, &cardID, &scorerVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return pathapp.ReviewSnapshot{}, pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "review answer was not found")
	}
	if err != nil {
		return pathapp.ReviewSnapshot{}, classify(err)
	}
	if cardID == nil {
		return pathapp.ReviewSnapshot{}, pathdomain.ConflictError(pathdomain.ErrorCodeEvidenceStale, "review answer has no review card")
	}
	parsedCardID, err := foundation.ParseID(*cardID)
	if err != nil {
		return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted review answer card is invalid")
	}
	var cardStatus string
	err = db.QueryRow(ctx, `SELECT status FROM learning.review_card
		WHERE workspace_id=$1 AND id=$2 FOR KEY SHARE`, string(workspaceID), *cardID).Scan(&cardStatus)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && cardStatus != string(reviewdomain.CardStatusApproved)) {
		return pathapp.ReviewSnapshot{}, pathdomain.ConflictError(pathdomain.ErrorCodeEvidenceStale, "review card is no longer approved")
	}
	if err != nil {
		return pathapp.ReviewSnapshot{}, classify(err)
	}
	var score reviewdomain.Score
	if err := decodeStrict(scoreJSON, &score); err != nil || reviewdomain.ValidateScore(score) != nil {
		return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted review score is invalid")
	}
	if len(score.Evidence) == 0 {
		return pathapp.ReviewSnapshot{}, pathdomain.ConflictError(pathdomain.ErrorCodeEvidenceStale, "review score has no verifiable evidence")
	}

	claimIDs := make([]string, 0, len(score.Evidence))
	sourceVersionIDs := make([]string, 0, len(score.Evidence))
	spanIDs := make([]string, 0, len(score.Evidence))
	hashes := make([]string, 0, len(score.Evidence))
	for _, evidence := range score.Evidence {
		claimIDs = append(claimIDs, string(evidence.ClaimID))
		sourceVersionIDs = append(sourceVersionIDs, string(evidence.SourceVersionID))
		spanIDs = append(spanIDs, string(evidence.SourceSpanID))
		hashes = append(hashes, evidence.EvidenceHash)
	}

	rows, err := db.Query(ctx, `WITH requested AS (
		SELECT request_no::integer,claim_id,source_version_id,source_span_id,evidence_hash
		FROM unnest($3::uuid[],$4::uuid[],$5::uuid[],$6::text[]) WITH ORDINALITY
			AS value(claim_id,source_version_id,source_span_id,evidence_hash,request_no)
	)
	SELECT requested.request_no,requested.claim_id::text,frozen.index_version_id::text,frozen.chunk_id::text,
		requested.source_version_id::text,requested.source_span_id::text,requested.evidence_hash,topic.id::text
	FROM requested
	JOIN learning.review_card AS card
	  ON card.workspace_id=$1 AND card.id=$2 AND card.status='APPROVED' AND card.claim_id=requested.claim_id
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
	  ON claim.workspace_id=$1 AND claim.id=requested.claim_id AND claim.status='CONFIRMED'
	JOIN core.claim_source AS claim_source
	  ON claim_source.workspace_id=$1 AND claim_source.claim_id=requested.claim_id
	 AND claim_source.source_version_id=requested.source_version_id
	 AND claim_source.source_span_id=requested.source_span_id
	 AND claim_source.evidence_hash=requested.evidence_hash
	 AND claim_source.support_type='SUPPORTS'
	JOIN core.source_version AS source_version
	  ON source_version.workspace_id=$1 AND source_version.id=requested.source_version_id
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
		WHERE active_index.workspace_id=$1 AND active_index.status='active'
		ORDER BY chunk_manifest.sequence,chunk_manifest.chunk_id
		LIMIT 1
	) AS frozen ON true
	LEFT JOIN LATERAL (
		SELECT candidate.id
		FROM core.relation AS relation
		JOIN core.topic AS candidate
		  ON candidate.workspace_id=relation.workspace_id AND candidate.id=relation.target_node_id
		WHERE relation.workspace_id=$1
		  AND relation.source_node_type='CLAIM' AND relation.source_node_id=requested.claim_id
		  AND relation.target_node_type='TOPIC' AND relation.relation_type='BELONGS_TO'
		  AND relation.status='CONFIRMED'
		ORDER BY candidate.id
		LIMIT 1
	) AS topic ON true
	WHERE core.knowledge_validate_provenance_binding($1,requested.source_version_id,requested.source_span_id)
	  AND COALESCE((
		SELECT attempt.security_status
		FROM ingestion.attempt AS attempt
		WHERE attempt.workspace_id=$1 AND attempt.source_version_id=requested.source_version_id
		ORDER BY attempt.started_at DESC,attempt.id DESC
		LIMIT 1
	  ),'passed') <> 'quarantined'
	ORDER BY requested.request_no`, string(workspaceID), string(parsedCardID), claimIDs, sourceVersionIDs, spanIDs, hashes)
	if err != nil {
		return pathapp.ReviewSnapshot{}, classify(err)
	}
	defer rows.Close()
	citations := make([]pathdomain.Citation, 0, len(score.Evidence))
	seen := make(map[int]struct{}, len(score.Evidence))
	for rows.Next() {
		var requestNo int
		var citation pathdomain.Citation
		var topicID *string
		if err := rows.Scan(&requestNo, &citation.ClaimID, &citation.IndexVersionID, &citation.ChunkID,
			&citation.SourceVersionID, &citation.SourceSpanID, &citation.EvidenceHash, &topicID); err != nil {
			return pathapp.ReviewSnapshot{}, classify(err)
		}
		if requestNo < 1 || requestNo > len(score.Evidence) {
			return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "review evidence projection returned an invalid position")
		}
		if _, duplicate := seen[requestNo]; duplicate {
			return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "review evidence projection is ambiguous")
		}
		seen[requestNo] = struct{}{}
		if topicID != nil {
			parsed, err := foundation.ParseID(*topicID)
			if err != nil {
				return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "review evidence topic is invalid")
			}
			citation.TopicID = &parsed
		}
		expected := score.Evidence[requestNo-1]
		if citation.ClaimID != expected.ClaimID || citation.SourceVersionID != expected.SourceVersionID ||
			citation.SourceSpanID != expected.SourceSpanID || citation.EvidenceHash != expected.EvidenceHash ||
			pathdomain.ValidateCitation(citation) != nil {
			return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "review evidence projection drifted")
		}
		citations = append(citations, citation)
	}
	if err := rows.Err(); err != nil {
		return pathapp.ReviewSnapshot{}, classify(err)
	}
	if len(citations) != len(score.Evidence) {
		return pathapp.ReviewSnapshot{}, pathdomain.ConflictError(pathdomain.ErrorCodeEvidenceStale, "one or more review score evidence bindings are no longer verifiable")
	}

	snapshot := pathapp.ReviewSnapshot{
		SchemaVersion: pathapp.ReviewSnapshotSchemaVersion, ReviewAnswerID: answerID,
		ReviewCardID: parsedCardID, ScorerVersion: scorerVersion, PolicyVersion: pathdomain.ReviewGapSchemaVersion,
		Gap: pathdomain.Gap{
			Errors: append([]string(nil), score.Errors...), Omissions: append([]string(nil), score.Omissions...),
			Correctness: score.Correctness.Value, Coverage: score.Coverage.Value, Boundaries: score.Boundaries.Value,
			Citations: citations,
		},
	}
	canonical, _, _, err := pathapp.CanonicalReviewSnapshot(snapshot)
	if err != nil {
		return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "review path source snapshot is invalid")
	}
	return canonical, nil
}

func (repository *Repository) findResultReceipt(ctx context.Context, db rowQueryer, workspaceID foundation.ID, key, requestHash, command string, expectedVersion int64) (pathapp.Result, bool, error) {
	var storedHash, storedCommand, pathID string
	var storedExpected, pathVersion int64
	var response []byte
	err := db.QueryRow(ctx, `SELECT request_hash,command_type,path_id::text,expected_version,path_version,response
		FROM learning.learning_path_command WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(workspaceID), key).Scan(&storedHash, &storedCommand, &pathID, &storedExpected, &pathVersion, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return pathapp.Result{}, false, nil
	}
	if err != nil {
		return pathapp.Result{}, false, classify(err)
	}
	if storedHash != requestHash || storedCommand != command || storedExpected != expectedVersion {
		return pathapp.Result{}, false, pathdomain.ConflictError(pathdomain.ErrorCodeIdempotencyConflict, "learning path idempotency key is bound to another request")
	}
	var result pathapp.Result
	if err := decodeStrict(response, &result); err != nil || result.Replayed || validateResult(result, workspaceID, foundation.ID(pathID), pathVersion) != nil {
		return pathapp.Result{}, false, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path receipt is invalid")
	}
	result.Replayed = true
	return result, true, nil
}

func (repository *Repository) loadPathResult(ctx context.Context, db queryer, workspaceID, answerID foundation.ID) (pathapp.Result, error) {
	return loadResult(ctx, db, `SELECT id::text,workspace_id::text,origin_type,interview_session_id::text,
		interview_report_id::text,review_answer_id::text,source_policy_version,artifact_id::text,
		artifact_revision_id::text,artifact_version,status,version,created_at,updated_at
		FROM learning.learning_path WHERE workspace_id=$1 AND review_answer_id=$2 AND origin_type='REVIEW'`, workspaceID, answerID)
}

func loadResultByPath(ctx context.Context, db queryer, workspaceID, pathID foundation.ID, lock bool) (pathapp.Result, error) {
	query := `SELECT id::text,workspace_id::text,origin_type,interview_session_id::text,
		interview_report_id::text,review_answer_id::text,source_policy_version,artifact_id::text,
		artifact_revision_id::text,artifact_version,status,version,created_at,updated_at
		FROM learning.learning_path WHERE workspace_id=$1 AND id=$2 AND origin_type='REVIEW'`
	if lock {
		query += ` FOR UPDATE`
	}
	return loadResult(ctx, db, query, workspaceID, pathID)
}

func loadResult(ctx context.Context, db queryer, query string, workspaceID, bindingID foundation.ID) (pathapp.Result, error) {
	var path pathdomain.Path
	var origin, status string
	var sessionID, reportID, answerID *string
	err := db.QueryRow(ctx, query, string(workspaceID), string(bindingID)).Scan(
		&path.ID, &path.WorkspaceID, &origin, &sessionID, &reportID, &answerID,
		&path.SourcePolicyVersion, &path.Artifact.ArtifactID, &path.Artifact.ArtifactRevisionID,
		&path.Artifact.ArtifactVersion, &status, &path.Version, &path.CreatedAt, &path.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return pathapp.Result{}, pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "learning path was not found")
	}
	if err != nil {
		return pathapp.Result{}, classify(err)
	}
	path.OriginType, path.Status = pathdomain.Origin(origin), pathdomain.Status(status)
	if sessionID != nil {
		value := foundation.ID(*sessionID)
		path.InterviewSessionID = &value
	}
	if reportID != nil {
		value := foundation.ID(*reportID)
		path.InterviewReportID = &value
	}
	if answerID != nil {
		value := foundation.ID(*answerID)
		path.ReviewAnswerID = &value
	}

	rows, err := db.Query(ctx, `SELECT id::text,workspace_id::text,path_id::text,step_no,claim_id::text,
		topic_id::text,source_version_id::text,source_span_id::text,evidence_hash,title,rationale,
		status,version,created_at,updated_at
		FROM learning.learning_path_step WHERE workspace_id=$1 AND path_id=$2 ORDER BY step_no,id`,
		string(workspaceID), string(path.ID))
	if err != nil {
		return pathapp.Result{}, classify(err)
	}
	defer rows.Close()
	steps := make([]pathdomain.Step, 0)
	for rows.Next() {
		var step pathdomain.Step
		var topicID *string
		var stepStatus string
		if err := rows.Scan(&step.ID, &step.WorkspaceID, &step.PathID, &step.StepNo, &step.ClaimID,
			&topicID, &step.SourceVersionID, &step.SourceSpanID, &step.EvidenceHash,
			&step.Title, &step.Rationale, &stepStatus, &step.Version, &step.CreatedAt, &step.UpdatedAt); err != nil {
			return pathapp.Result{}, classify(err)
		}
		if topicID != nil {
			value := foundation.ID(*topicID)
			step.TopicID = &value
		}
		step.Status = pathdomain.StepStatus(stepStatus)
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return pathapp.Result{}, classify(err)
	}
	result := pathapp.Result{Path: path, Steps: steps}
	if err := validateResult(result, workspaceID, path.ID, path.Version); err != nil {
		return pathapp.Result{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted learning path is invalid")
	}
	return result, nil
}

func validateResult(result pathapp.Result, workspaceID, pathID foundation.ID, pathVersion int64) error {
	if result.Replayed || pathdomain.ValidatePath(result.Path) != nil || result.Path.WorkspaceID != workspaceID ||
		result.Path.ID != pathID || result.Path.Version != pathVersion || result.Path.OriginType != pathdomain.OriginReview ||
		result.Path.ReviewAnswerID == nil || len(result.Steps) == 0 || len(result.Steps) > pathdomain.MaxSteps {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path aggregate binding is invalid")
	}
	seenIDs := make(map[foundation.ID]struct{}, len(result.Steps))
	for index, step := range result.Steps {
		if pathdomain.ValidateStep(step) != nil || step.WorkspaceID != workspaceID || step.PathID != pathID || step.StepNo != index+1 {
			return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step binding is invalid")
		}
		if _, duplicate := seenIDs[step.ID]; duplicate {
			return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step identity is duplicated")
		}
		seenIDs[step.ID] = struct{}{}
	}
	if result.Path.Status == pathdomain.StatusCompleted && !pathdomain.AllStepsTerminal(result.Steps) {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "completed learning path has non-terminal steps")
	}
	return nil
}

func insertResultReceipt(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key, hash, command string, expectedVersion int64, result pathapp.Result) error {
	if err := validateResult(result, workspaceID, result.Path.ID, result.Path.Version); err != nil {
		return err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path receipt cannot be encoded")
	}
	_, err = tx.Exec(ctx, `INSERT INTO learning.learning_path_command(
		workspace_id,idempotency_key,request_hash,command_type,path_id,expected_version,path_version,response,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,statement_timestamp())`,
		string(workspaceID), key, hash, command, string(result.Path.ID), expectedVersion, result.Path.Version, payload)
	return classify(err)
}

func findStepReceipt(ctx context.Context, db rowQueryer, workspaceID foundation.ID, key, requestHash string, expectedVersion int64) (pathapp.StepResult, bool, error) {
	var storedHash, command, pathID string
	var storedExpected, pathVersion int64
	var response []byte
	err := db.QueryRow(ctx, `SELECT request_hash,command_type,path_id::text,expected_version,path_version,response
		FROM learning.learning_path_command WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(workspaceID), key).Scan(&storedHash, &command, &pathID, &storedExpected, &pathVersion, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return pathapp.StepResult{}, false, nil
	}
	if err != nil {
		return pathapp.StepResult{}, false, classify(err)
	}
	if storedHash != requestHash || command != pathapp.CommandTypePathStep || storedExpected != expectedVersion {
		return pathapp.StepResult{}, false, pathdomain.ConflictError(pathdomain.ErrorCodeIdempotencyConflict, "learning path idempotency key is bound to another request")
	}
	var value pathapp.StepResult
	if err := decodeStrict(response, &value); err != nil || value.Replayed || pathdomain.ValidatePath(value.Path) != nil ||
		pathdomain.ValidateStep(value.Step) != nil || value.Path.WorkspaceID != workspaceID || value.Path.ID != foundation.ID(pathID) ||
		value.Path.Version != pathVersion || value.Path.OriginType != pathdomain.OriginReview || value.Step.WorkspaceID != workspaceID ||
		value.Step.PathID != value.Path.ID {
		return pathapp.StepResult{}, false, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step receipt is invalid")
	}
	value.Replayed = true
	return value, true, nil
}

func insertStepReceipt(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key, hash string, expectedVersion int64, value pathapp.StepResult) error {
	if value.Replayed || pathdomain.ValidatePath(value.Path) != nil || pathdomain.ValidateStep(value.Step) != nil ||
		value.Path.WorkspaceID != workspaceID || value.Step.WorkspaceID != workspaceID || value.Step.PathID != value.Path.ID ||
		value.Path.OriginType != pathdomain.OriginReview {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step receipt cannot be encoded")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step receipt cannot be encoded")
	}
	_, err = tx.Exec(ctx, `INSERT INTO learning.learning_path_command(
		workspace_id,idempotency_key,request_hash,command_type,path_id,expected_version,path_version,response,created_at
	) VALUES($1,$2,$3,'PATH_STEP',$4,$5,$6,$7,statement_timestamp())`,
		string(workspaceID), key, hash, string(value.Path.ID), expectedVersion, value.Path.Version, payload)
	return classify(err)
}

func decodeStrict(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON value")
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validKey(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n")
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
