package postgres

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"gorm.io/gorm"
)

func gormInterviewLockCommand(ctx context.Context, transaction *gorm.DB, workspaceID foundation.ID, key string) error {
	_, err := gormInterviewExec(ctx, transaction, `SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, advisoryKey(workspaceID, key))
	return gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
}

func gormInterviewLoadReceipt(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key, requestHash, commandType string) (receipt, bool, error) {
	row, err := gormInterviewRawRow(ctx, database, `
		SELECT request_hash,command_type,session_id::text,response::text
		  FROM learning.interview_command
		 WHERE workspace_id=?::uuid AND idempotency_key=?`, string(workspaceID), key)
	if err != nil {
		return receipt{}, false, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var value receipt
	var response string
	err = row.Scan(&value.RequestHash, &value.CommandType, &value.SessionID, &response)
	if gormInterviewNoRows(err) {
		return receipt{}, false, nil
	}
	if err != nil {
		return receipt{}, false, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	value.Response = []byte(response)
	if value.RequestHash != requestHash || value.CommandType != commandType {
		return receipt{}, false, domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview idempotency key is bound to another request")
	}
	return value, true, nil
}

func gormInterviewInsertReceipt(ctx context.Context, transaction *gorm.DB, workspaceID foundation.ID, key, requestHash, commandType string, sessionID foundation.ID, response any, createdAt time.Time) error {
	encoded, err := encodeJSON(response)
	if err != nil {
		return err
	}
	_, err = gormInterviewExec(ctx, transaction, `
		INSERT INTO learning.interview_command(
			workspace_id,idempotency_key,request_hash,command_type,session_id,response,created_at
		) VALUES(?::uuid,?,?,?,?,?::jsonb,?::timestamptz)`,
		string(workspaceID), key, requestHash, commandType, string(sessionID), interviewJSONB(encoded), createdAt.UTC())
	return gormInterviewClassify(ctx, err, domain.ErrorCodeIdempotencyConflict)
}

func gormInterviewInsertQuestion(ctx context.Context, transaction *gorm.DB, question domain.Question) error {
	if err := domain.ValidateQuestion(question); err != nil {
		return err
	}
	answerPoints, err := encodeJSON(question.AnswerPoints)
	if err != nil {
		return err
	}
	evidence, err := encodeJSON(question.Evidence)
	if err != nil {
		return err
	}
	if question.NoteSource != nil {
		evidence = []byte("[]")
	}
	noteSource, err := encodeNoteSource(question.NoteSource)
	if err != nil {
		return err
	}
	plan, err := encodeJSON(domain.CloneNoteFollowUps(question.FollowUpPlan))
	if err != nil {
		return err
	}
	if len(question.FollowUpPlan) == 0 {
		plan = []byte("[]")
	}
	var planBinding any
	if question.SourceKind == domain.QuestionSourceNoteRevision {
		planBinding = interviewJSONB(plan)
	}
	_, err = gormInterviewExec(ctx, transaction, `
		INSERT INTO learning.interview_question(
			id,workspace_id,session_id,question_no,follow_up_no,parent_question_id,claim_id,topic_id,prompt,
			answer_points,evidence,status,fingerprint,created_at,answered_at,source_kind,note_source,follow_up_plan
		) VALUES(?::uuid,?::uuid,?::uuid,?,?,?::uuid,?::uuid,?::uuid,?,?::jsonb,?::jsonb,?,?,?::timestamptz,?::timestamptz,?,?::jsonb,?::jsonb)`,
		string(question.ID), string(question.WorkspaceID), string(question.SessionID), question.QuestionNo, question.FollowUpNo,
		nullableID(question.ParentQuestionID), nullableNoteString(string(question.ClaimID)), nullableID(question.TopicID), question.Prompt,
		interviewJSONB(answerPoints), interviewJSONB(evidence), string(question.Status), question.Fingerprint,
		question.CreatedAt.UTC(), nullableTime(question.AnsweredAt), persistedSourceKind(question.SourceKind), noteSource, planBinding)
	return gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
}

func gormInterviewInsertPathStep(ctx context.Context, transaction *gorm.DB, step domain.PathStep) error {
	if err := domain.ValidatePathStep(step); err != nil {
		return err
	}
	noteSource, err := encodeNoteSource(step.NoteSource)
	if err != nil {
		return err
	}
	_, err = gormInterviewExec(ctx, transaction, `
		INSERT INTO learning.interview_learning_path_step(
			id,workspace_id,path_id,step_no,claim_id,topic_id,source_version_id,source_span_id,evidence_hash,
			title,rationale,status,version,created_at,updated_at,source_kind,note_source
		) VALUES(?::uuid,?::uuid,?::uuid,?,?::uuid,?::uuid,?::uuid,?::uuid,?,?,?,?,?,?::timestamptz,?::timestamptz,?,?::jsonb)`,
		string(step.ID), string(step.WorkspaceID), string(step.PathID), step.StepNo, nullableNoteString(string(step.ClaimID)), nullableID(step.TopicID),
		nullableNoteString(string(step.SourceVersionID)), nullableNoteString(string(step.SourceSpanID)), nullableNoteString(step.EvidenceHash), step.Title, step.Rationale, string(step.Status),
		step.Version, step.CreatedAt.UTC(), step.UpdatedAt.UTC(), persistedSourceKind(step.SourceKind), noteSource)
	return gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
}

func gormInterviewLoadCompletionReservationBySession(ctx context.Context, database *gorm.DB, workspaceID, sessionID foundation.ID, lock bool) (interviewapp.CompletionReservation, bool, error) {
	query := completionReservationSelect + ` WHERE workspace_id=?::uuid AND session_id=?::uuid`
	if lock {
		query += ` FOR UPDATE`
	}
	row, err := gormInterviewRawRow(ctx, database, query, string(workspaceID), string(sessionID))
	if err != nil {
		return interviewapp.CompletionReservation{}, false, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return gormInterviewScanCompletionReservation(ctx, row)
}

func gormInterviewLoadCompletionReservationByKey(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key string, lock bool) (interviewapp.CompletionReservation, bool, error) {
	query := completionReservationSelect + ` WHERE workspace_id=?::uuid AND idempotency_key=?`
	if lock {
		query += ` FOR UPDATE`
	}
	row, err := gormInterviewRawRow(ctx, database, query, string(workspaceID), key)
	if err != nil {
		return interviewapp.CompletionReservation{}, false, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return gormInterviewScanCompletionReservation(ctx, row)
}

func gormInterviewScanCompletionReservation(ctx context.Context, row interface{ Scan(...any) error }) (interviewapp.CompletionReservation, bool, error) {
	var reservation interviewapp.CompletionReservation
	var digest, reportID, reportArtifactID, reportRevisionID *string
	var pathID, pathArtifactID, pathRevisionID *string
	var reportVersion, pathVersion *int64
	var status string
	err := row.Scan(
		&reservation.WorkspaceID, &reservation.SessionID, &reservation.IdempotencyKey, &reservation.RequestHash,
		&reservation.ManualEnd, &reservation.SnapshotVersion, &digest,
		&reportID, &reportArtifactID, &reportRevisionID, &reportVersion,
		&pathID, &pathArtifactID, &pathRevisionID, &pathVersion,
		&status, &reservation.CreatedAt, &reservation.PreparedAt, &reservation.CompletedAt,
		&reservation.AbandonedAt, &reservation.UpdatedAt,
	)
	if gormInterviewNoRows(err) {
		return interviewapp.CompletionReservation{}, false, nil
	}
	if err != nil {
		return interviewapp.CompletionReservation{}, false, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	reservation.Status = interviewapp.CompletionReservationStatus(status)
	reservation.CreatedAt = reservation.CreatedAt.UTC()
	reservation.UpdatedAt = reservation.UpdatedAt.UTC()
	reservation.PreparedAt = utcTimePointer(reservation.PreparedAt)
	reservation.CompletedAt = utcTimePointer(reservation.CompletedAt)
	reservation.AbandonedAt = utcTimePointer(reservation.AbandonedAt)
	if digest != nil {
		reservation.ArtifactDigest = *digest
	}
	if reportID != nil && reportArtifactID != nil && reportRevisionID != nil && reportVersion != nil &&
		pathID != nil && pathArtifactID != nil && pathRevisionID != nil && pathVersion != nil {
		reservation.Bindings = interviewapp.CompletionArtifactBindings{
			ReportID:       foundation.ID(*reportID),
			ReportArtifact: domain.ArtifactBinding{Kind: "INTERVIEW_DOC", ArtifactID: foundation.ID(*reportArtifactID), RevisionID: foundation.ID(*reportRevisionID), ArtifactVersion: *reportVersion},
			PathID:         foundation.ID(*pathID),
			PathArtifact:   domain.ArtifactBinding{Kind: "LEARNING_PATH", ArtifactID: foundation.ID(*pathArtifactID), RevisionID: foundation.ID(*pathRevisionID), ArtifactVersion: *pathVersion},
		}
	}
	if err := validateCompletionReservation(reservation); err != nil {
		return interviewapp.CompletionReservation{}, false, persistenceInvalid("validate interview completion reservation", err)
	}
	return reservation, true, nil
}

func gormInterviewLoadTerminalCompleteReplay(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key, requestHash string) (*interviewapp.CompleteResult, bool, error) {
	value, found, err := gormInterviewLoadReceipt(ctx, database, workspaceID, key, requestHash, "COMPLETE")
	if err != nil || !found {
		return nil, found, err
	}
	result, err := decodeCompleteResult(value.Response)
	if err != nil {
		return nil, false, err
	}
	result.Replayed = true
	return &result, true, nil
}

func gormInterviewReconcileCompletionArtifactVisibility(ctx context.Context, transaction *gorm.DB, record interviewapp.CompleteRecord) error {
	_, err := gormInterviewExec(ctx, transaction, `
		UPDATE learning.artifact_visibility_hold AS candidate
		   SET disposition='ACTIVE'
		 WHERE candidate.workspace_id=?::uuid
		   AND candidate.owner_type='INTERVIEW_COMPLETE'
		   AND candidate.owner_id=?::uuid
		   AND candidate.disposition='ORPHANED'
		   AND candidate.attempt_digest=?
		   AND ((candidate.owner_role='REPORT' AND candidate.artifact_id=?::uuid)
		        OR (candidate.owner_role='PATH' AND candidate.artifact_id=?::uuid))
		   AND NOT EXISTS (
		       SELECT 1 FROM learning.artifact_visibility_hold AS active
		        WHERE active.workspace_id=candidate.workspace_id
		          AND active.owner_type=candidate.owner_type
		          AND active.owner_id=candidate.owner_id
		          AND active.owner_role=candidate.owner_role
		          AND active.disposition='ACTIVE'
		   )`, string(record.WorkspaceID), string(record.SessionID), record.ArtifactDigest,
		string(record.Report.Artifact.ArtifactID), string(record.Path.Artifact.ArtifactID))
	if err != nil {
		return gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
	}
	rows, err := gormInterviewRawRows(ctx, transaction, `
		SELECT artifact_id::text,owner_role,attempt_digest
		  FROM learning.artifact_visibility_hold
		 WHERE workspace_id=?::uuid AND owner_type='INTERVIEW_COMPLETE' AND owner_id=?::uuid AND disposition='ACTIVE'
		 ORDER BY owner_role,artifact_id
		 FOR UPDATE`, string(record.WorkspaceID), string(record.SessionID))
	if err != nil {
		return gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
	}
	defer rows.Close()
	matched := 0
	for rows.Next() {
		var artifactID, role string
		var digest *string
		if err := rows.Scan(&artifactID, &role, &digest); err != nil {
			return gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
		}
		if digest == nil || *digest != record.ArtifactDigest ||
			(role == "REPORT" && artifactID != string(record.Report.Artifact.ArtifactID)) ||
			(role == "PATH" && artifactID != string(record.Path.Artifact.ArtifactID)) ||
			(role != "REPORT" && role != "PATH") {
			return persistenceInvalid("interview completion has a mismatched active artifact visibility hold", nil)
		}
		matched++
	}
	if err := rows.Err(); err != nil {
		return gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
	}
	if matched != 2 {
		return persistenceInvalid("interview completion requires exactly two active artifact visibility holds", nil)
	}
	return nil
}

func gormInterviewReleaseCompletionArtifactVisibility(ctx context.Context, transaction *gorm.DB, record interviewapp.CompleteRecord) error {
	deleted, err := gormInterviewExec(ctx, transaction, `
		DELETE FROM learning.artifact_visibility_hold
		 WHERE workspace_id=?::uuid
		   AND owner_type='INTERVIEW_COMPLETE'
		   AND owner_id=?::uuid
		   AND disposition='ACTIVE'
		   AND attempt_digest=?
		   AND ((owner_role='REPORT' AND artifact_id=?::uuid) OR (owner_role='PATH' AND artifact_id=?::uuid))`,
		string(record.WorkspaceID), string(record.SessionID), record.ArtifactDigest,
		string(record.Report.Artifact.ArtifactID), string(record.Path.Artifact.ArtifactID))
	if err != nil {
		return gormInterviewClassify(ctx, err, domain.ErrorCodePersistenceInvalid)
	}
	if deleted != 2 {
		return persistenceInvalid("interview completion artifact visibility holds are incomplete", nil)
	}
	return nil
}
