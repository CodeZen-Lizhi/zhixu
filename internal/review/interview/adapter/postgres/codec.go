package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/jackc/pgx/v5"
)

type rowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type queryer interface {
	rowQueryer
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type rowScanner interface {
	Scan(...any) error
}

const completionReservationSelect = `SELECT
	workspace_id::text,session_id::text,idempotency_key,request_hash,manual_end,snapshot_version,artifact_digest,
	report_id::text,report_artifact_id::text,report_artifact_revision_id::text,report_artifact_version,
	path_id::text,path_artifact_id::text,path_artifact_revision_id::text,path_artifact_version,
	status,created_at,prepared_at,completed_at,abandoned_at,updated_at
	FROM learning.interview_completion_reservation`

func loadSnapshot(ctx context.Context, db queryer, workspaceID, sessionID foundation.ID, lock bool) (interviewapp.Snapshot, error) {
	session, err := loadSession(ctx, db, workspaceID, sessionID, lock)
	if err != nil {
		return interviewapp.Snapshot{}, err
	}
	questions, err := loadQuestions(ctx, db, workspaceID, sessionID)
	if err != nil {
		return interviewapp.Snapshot{}, err
	}
	turns, err := loadTurns(ctx, db, workspaceID, sessionID)
	if err != nil {
		return interviewapp.Snapshot{}, err
	}
	questionsByID := make(map[foundation.ID]domain.Question, len(questions))
	for _, question := range questions {
		questionsByID[question.ID] = question
	}
	for _, turn := range turns {
		question, found := questionsByID[turn.QuestionID]
		if !found {
			return interviewapp.Snapshot{}, persistenceInvalid("interview turn references a missing question", nil)
		}
		if err := domain.ValidateTurn(turn, question); err != nil {
			return interviewapp.Snapshot{}, persistenceInvalid("validate persisted interview turn", err)
		}
	}
	report, err := loadReport(ctx, db, workspaceID, sessionID)
	if err != nil {
		return interviewapp.Snapshot{}, err
	}
	var path *domain.LearningPath
	var steps []domain.PathStep
	if report != nil {
		path, steps, err = loadPathBySession(ctx, db, workspaceID, sessionID)
		if err != nil {
			return interviewapp.Snapshot{}, err
		}
		if path == nil {
			return interviewapp.Snapshot{}, persistenceInvalid("completed interview report is missing its learning path", nil)
		}
	}
	return interviewapp.Snapshot{Session: session, Questions: questions, Turns: turns, Report: report, Path: path, Steps: steps}, nil
}

func loadSession(ctx context.Context, db rowQueryer, workspaceID, sessionID foundation.ID, lock bool) (domain.Session, error) {
	query := `
		SELECT s.session_id::text,s.workspace_id::text,rs.config,rs.status,s.version,s.follow_up_count,rs.started_at,rs.ended_at
		  FROM learning.interview_session AS s
		  JOIN learning.review_session AS rs
		    ON rs.id=s.session_id AND rs.workspace_id=s.workspace_id
		 WHERE s.workspace_id=$1 AND s.session_id=$2`
	if lock {
		query += ` FOR UPDATE OF s,rs`
	}
	session, err := scanSession(db.QueryRow(ctx, query, string(workspaceID), string(sessionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, domain.NotFoundError(domain.ErrorCodeSessionNotFound, "interview session was not found")
	}
	if err != nil {
		return domain.Session{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return session, nil
}

func scanSession(row rowScanner) (domain.Session, error) {
	var session domain.Session
	var config []byte
	var status string
	if err := row.Scan(
		&session.ID, &session.WorkspaceID, &config, &status, &session.Version, &session.FollowUpCount, &session.StartedAt, &session.EndedAt,
	); err != nil {
		return domain.Session{}, err
	}
	if err := decodeJSON(config, &session.Config); err != nil {
		return domain.Session{}, persistenceInvalid("decode interview session config", err)
	}
	session.Status = domain.SessionStatus(status)
	session.StartedAt = session.StartedAt.UTC()
	if session.EndedAt != nil {
		value := session.EndedAt.UTC()
		session.EndedAt = &value
	}
	if err := domain.ValidateSession(session); err != nil {
		return domain.Session{}, persistenceInvalid("validate persisted interview session", err)
	}
	return session, nil
}

func loadQuestions(ctx context.Context, db queryer, workspaceID, sessionID foundation.ID) ([]domain.Question, error) {
	rows, err := db.Query(ctx, `
		SELECT id::text,workspace_id::text,session_id::text,question_no,follow_up_no,parent_question_id::text,
		       claim_id::text,topic_id::text,prompt,answer_points,evidence,status,fingerprint,created_at,answered_at
		  FROM learning.interview_question
		 WHERE workspace_id=$1 AND session_id=$2
		 ORDER BY question_no,follow_up_no,id`, string(workspaceID), string(sessionID))
	if err != nil {
		return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	questions := make([]domain.Question, 0)
	for rows.Next() {
		var question domain.Question
		var parentID, topicID *string
		var points, evidence []byte
		var status string
		if err := rows.Scan(&question.ID, &question.WorkspaceID, &question.SessionID, &question.QuestionNo, &question.FollowUpNo, &parentID,
			&question.ClaimID, &topicID, &question.Prompt, &points, &evidence, &status, &question.Fingerprint, &question.CreatedAt, &question.AnsweredAt); err != nil {
			return nil, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if parentID != nil {
			value := foundation.ID(*parentID)
			question.ParentQuestionID = &value
		}
		if topicID != nil {
			value := foundation.ID(*topicID)
			question.TopicID = &value
		}
		if err := decodeJSON(points, &question.AnswerPoints); err != nil {
			return nil, persistenceInvalid("decode interview question points", err)
		}
		if err := decodeJSON(evidence, &question.Evidence); err != nil {
			return nil, persistenceInvalid("decode interview question evidence", err)
		}
		question.Status = domain.QuestionStatus(status)
		question.CreatedAt = question.CreatedAt.UTC()
		if question.AnsweredAt != nil {
			value := question.AnsweredAt.UTC()
			question.AnsweredAt = &value
		}
		if err := domain.ValidateQuestion(question); err != nil {
			return nil, persistenceInvalid("validate persisted interview question", err)
		}
		questions = append(questions, question)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return questions, nil
}

func loadTurns(ctx context.Context, db queryer, workspaceID, sessionID foundation.ID) ([]domain.Turn, error) {
	rows, err := db.Query(ctx, `
		SELECT id::text,workspace_id::text,session_id::text,question_id::text,idempotency_key,request_hash,user_answer,
		       score,decision,scorer_version,created_at
		  FROM learning.interview_turn
		 WHERE workspace_id=$1 AND session_id=$2
		 ORDER BY created_at,id`, string(workspaceID), string(sessionID))
	if err != nil {
		return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	turns := make([]domain.Turn, 0)
	for rows.Next() {
		var turn domain.Turn
		var score, decision []byte
		if err := rows.Scan(&turn.ID, &turn.WorkspaceID, &turn.SessionID, &turn.QuestionID, &turn.IdempotencyKey, &turn.RequestHash, &turn.UserAnswer, &score, &decision, &turn.ScorerVersion, &turn.CreatedAt); err != nil {
			return nil, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if err := decodeJSON(score, &turn.Score); err != nil {
			return nil, persistenceInvalid("decode interview turn score", err)
		}
		if err := decodeJSON(decision, &turn.Decision); err != nil {
			return nil, persistenceInvalid("decode interview turn decision", err)
		}
		turn.CreatedAt = turn.CreatedAt.UTC()
		turns = append(turns, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return turns, nil
}

func loadReport(ctx context.Context, db rowQueryer, workspaceID, sessionID foundation.ID) (*domain.Report, error) {
	var report domain.Report
	var raw []byte
	var reportID string
	var artifactID, revisionID string
	var reportHash string
	var artifactVersion int64
	err := db.QueryRow(ctx, `
		SELECT id::text,report,artifact_id::text,artifact_revision_id::text,artifact_version,report_hash
		  FROM learning.interview_report
		 WHERE workspace_id=$1 AND session_id=$2`, string(workspaceID), string(sessionID)).Scan(
		&reportID, &raw, &artifactID, &revisionID, &artifactVersion, &reportHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	if err := decodeJSON(raw, &report); err != nil {
		return nil, persistenceInvalid("decode interview report", err)
	}
	if report.ID != foundation.ID(reportID) {
		return nil, persistenceInvalid("interview report identity is missing", nil)
	}
	canonicalRaw, err := canonicalJSON(raw)
	if err != nil {
		return nil, persistenceInvalid("canonicalize interview report", err)
	}
	if report.Artifact.ArtifactID != foundation.ID(artifactID) || report.Artifact.RevisionID != foundation.ID(revisionID) || report.Artifact.ArtifactVersion != artifactVersion || !validHash(reportHash) || reportHash != hashBytes(canonicalRaw) {
		return nil, persistenceInvalid("interview report artifact binding drifted", nil)
	}
	if err := domain.ValidateReport(report); err != nil {
		return nil, persistenceInvalid("validate persisted interview report", err)
	}
	return &report, nil
}

func loadPathBySession(ctx context.Context, db queryer, workspaceID, sessionID foundation.ID) (*domain.LearningPath, []domain.PathStep, error) {
	var path domain.LearningPath
	var artifactID, revisionID string
	var status string
	err := db.QueryRow(ctx, `
		SELECT id::text,workspace_id::text,session_id::text,report_id::text,artifact_id::text,artifact_revision_id::text,
		       artifact_version,status,version,created_at,updated_at
		  FROM learning.interview_learning_path
		 WHERE workspace_id=$1 AND session_id=$2`, string(workspaceID), string(sessionID)).Scan(
		&path.ID, &path.WorkspaceID, &path.SessionID, &path.ReportID, &artifactID, &revisionID,
		&path.Artifact.ArtifactVersion, &status, &path.Version, &path.CreatedAt, &path.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	path.Artifact = domain.ArtifactBinding{Kind: "LEARNING_PATH", ArtifactID: foundation.ID(artifactID), RevisionID: foundation.ID(revisionID), ArtifactVersion: path.Artifact.ArtifactVersion}
	path.Status = domain.PathStatus(status)
	path.CreatedAt, path.UpdatedAt = path.CreatedAt.UTC(), path.UpdatedAt.UTC()
	if err := domain.ValidateLearningPath(path); err != nil {
		return nil, nil, persistenceInvalid("validate persisted learning path", err)
	}
	steps, err := loadPathSteps(ctx, db, workspaceID, path.ID)
	if err != nil {
		return nil, nil, err
	}
	return &path, steps, nil
}

func loadPath(ctx context.Context, db queryer, workspaceID, pathID foundation.ID, lock bool) (domain.LearningPath, []domain.PathStep, error) {
	query := `
		SELECT id::text,workspace_id::text,session_id::text,report_id::text,artifact_id::text,artifact_revision_id::text,
		       artifact_version,status,version,created_at,updated_at
		  FROM learning.interview_learning_path
		 WHERE workspace_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var path domain.LearningPath
	var artifactID, revisionID, status string
	err := db.QueryRow(ctx, query, string(workspaceID), string(pathID)).Scan(&path.ID, &path.WorkspaceID, &path.SessionID, &path.ReportID, &artifactID, &revisionID, &path.Artifact.ArtifactVersion, &status, &path.Version, &path.CreatedAt, &path.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.LearningPath{}, nil, domain.NotFoundError(domain.ErrorCodePathInvalid, "learning path was not found")
	}
	if err != nil {
		return domain.LearningPath{}, nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	path.Artifact = domain.ArtifactBinding{Kind: "LEARNING_PATH", ArtifactID: foundation.ID(artifactID), RevisionID: foundation.ID(revisionID), ArtifactVersion: path.Artifact.ArtifactVersion}
	path.Status = domain.PathStatus(status)
	path.CreatedAt, path.UpdatedAt = path.CreatedAt.UTC(), path.UpdatedAt.UTC()
	if err := domain.ValidateLearningPath(path); err != nil {
		return domain.LearningPath{}, nil, persistenceInvalid("validate persisted learning path", err)
	}
	steps, err := loadPathSteps(ctx, db, workspaceID, path.ID)
	if err != nil {
		return domain.LearningPath{}, nil, err
	}
	return path, steps, nil
}

func loadPathSteps(ctx context.Context, db queryer, workspaceID, pathID foundation.ID) ([]domain.PathStep, error) {
	rows, err := db.Query(ctx, `
		SELECT id::text,workspace_id::text,path_id::text,step_no,claim_id::text,topic_id::text,source_version_id::text,
		       source_span_id::text,evidence_hash,title,rationale,status,version,created_at,updated_at
		  FROM learning.interview_learning_path_step
		 WHERE workspace_id=$1 AND path_id=$2
		 ORDER BY step_no,id`, string(workspaceID), string(pathID))
	if err != nil {
		return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rows.Close()
	steps := make([]domain.PathStep, 0)
	for rows.Next() {
		var step domain.PathStep
		var topicID *string
		var status string
		if err := rows.Scan(&step.ID, &step.WorkspaceID, &step.PathID, &step.StepNo, &step.ClaimID, &topicID, &step.SourceVersionID, &step.SourceSpanID, &step.EvidenceHash, &step.Title, &step.Rationale, &status, &step.Version, &step.CreatedAt, &step.UpdatedAt); err != nil {
			return nil, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if topicID != nil {
			value := foundation.ID(*topicID)
			step.TopicID = &value
		}
		step.Status = domain.StepStatus(status)
		step.CreatedAt, step.UpdatedAt = step.CreatedAt.UTC(), step.UpdatedAt.UTC()
		if err := domain.ValidatePathStep(step); err != nil {
			return nil, persistenceInvalid("validate persisted learning path step", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return steps, nil
}

// loadCompletionReservationBySession 按 Session 读取并可锁定 Completion reservation。
func loadCompletionReservationBySession(ctx context.Context, db rowQueryer, workspaceID, sessionID foundation.ID, lock bool) (interviewapp.CompletionReservation, bool, error) {
	query := completionReservationSelect + ` WHERE workspace_id=$1 AND session_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	return loadCompletionReservation(db.QueryRow(ctx, query, string(workspaceID), string(sessionID)))
}

// loadCompletionReservationByKey 按客户端命令键读取并可锁定 Completion reservation。
func loadCompletionReservationByKey(ctx context.Context, db rowQueryer, workspaceID foundation.ID, key string, lock bool) (interviewapp.CompletionReservation, bool, error) {
	query := completionReservationSelect + ` WHERE workspace_id=$1 AND idempotency_key=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	return loadCompletionReservation(db.QueryRow(ctx, query, string(workspaceID), key))
}

// loadCompletionReservation 解码并验证一条 Completion reservation。
func loadCompletionReservation(row rowScanner) (interviewapp.CompletionReservation, bool, error) {
	var reservation interviewapp.CompletionReservation
	var digest, reportID, reportArtifactID, reportRevisionID *string
	var pathID, pathArtifactID, pathRevisionID *string
	var reportVersion, pathVersion *int64
	var status string
	if err := row.Scan(
		&reservation.WorkspaceID, &reservation.SessionID, &reservation.IdempotencyKey, &reservation.RequestHash,
		&reservation.ManualEnd, &reservation.SnapshotVersion, &digest,
		&reportID, &reportArtifactID, &reportRevisionID, &reportVersion,
		&pathID, &pathArtifactID, &pathRevisionID, &pathVersion,
		&status, &reservation.CreatedAt, &reservation.PreparedAt, &reservation.CompletedAt,
		&reservation.AbandonedAt, &reservation.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return interviewapp.CompletionReservation{}, false, nil
		}
		return interviewapp.CompletionReservation{}, false, classify(err, domain.ErrorCodeDependencyUnavailable)
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
			ReportID: foundation.ID(*reportID),
			ReportArtifact: domain.ArtifactBinding{
				Kind: "INTERVIEW_DOC", ArtifactID: foundation.ID(*reportArtifactID),
				RevisionID: foundation.ID(*reportRevisionID), ArtifactVersion: *reportVersion,
			},
			PathID: foundation.ID(*pathID),
			PathArtifact: domain.ArtifactBinding{
				Kind: "LEARNING_PATH", ArtifactID: foundation.ID(*pathArtifactID),
				RevisionID: foundation.ID(*pathRevisionID), ArtifactVersion: *pathVersion,
			},
		}
	}
	if err := validateCompletionReservation(reservation); err != nil {
		return interviewapp.CompletionReservation{}, false, persistenceInvalid("validate interview completion reservation", err)
	}
	return reservation, true, nil
}

// validateCompletionReservation 验证从 PostgreSQL 恢复的 reservation 完整形状。
func validateCompletionReservation(reservation interviewapp.CompletionReservation) error {
	if !validID(reservation.WorkspaceID) || !validID(reservation.SessionID) ||
		domain.ValidateIdempotencyKey(reservation.IdempotencyKey) != nil || !validHash(reservation.RequestHash) ||
		reservation.SnapshotVersion < 1 || reservation.CreatedAt.IsZero() || reservation.UpdatedAt.IsZero() {
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview completion reservation identity is invalid")
	}
	if reservation.ArtifactDigest != "" && !validHash(reservation.ArtifactDigest) {
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview completion reservation digest is invalid")
	}
	hasBindings := reservation.Bindings.ReportID != "" || reservation.Bindings.PathID != ""
	switch reservation.Status {
	case interviewapp.CompletionReservationPending:
		if reservation.CompletedAt != nil || reservation.AbandonedAt != nil || hasBindings {
			return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "pending interview completion reservation is malformed")
		}
	case interviewapp.CompletionReservationAbandoned:
		if reservation.CompletedAt != nil || reservation.AbandonedAt == nil || hasBindings {
			return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "abandoned interview completion reservation is malformed")
		}
	case interviewapp.CompletionReservationCompleted:
		if reservation.ArtifactDigest == "" || reservation.CompletedAt == nil || reservation.AbandonedAt != nil ||
			!validID(reservation.Bindings.ReportID) || !validID(reservation.Bindings.PathID) ||
			domain.ValidateArtifactBinding(reservation.Bindings.ReportArtifact, "INTERVIEW_DOC") != nil ||
			domain.ValidateArtifactBinding(reservation.Bindings.PathArtifact, "LEARNING_PATH") != nil {
			return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "completed interview completion reservation is malformed")
		}
	default:
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview completion reservation status is invalid")
	}
	if (reservation.ArtifactDigest == "") != (reservation.PreparedAt == nil) {
		return domain.InvalidError(domain.ErrorCodePersistenceInvalid, "interview completion reservation prepare time is invalid")
	}
	return nil
}

// utcTimePointer 复制并规范化可空 PostgreSQL 时间。
func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

func decodeJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func encodeJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, persistenceInvalid("encode interview persistence JSON", err)
	}
	return encoded, nil
}

// canonicalJSON makes an object hash stable across PostgreSQL jsonb key-order
// normalization while retaining json.Number instead of coercing numbers to float64.
func canonicalJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return json.Marshal(value)
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, value := range value {
		if !(value >= '0' && value <= '9') && !(value >= 'a' && value <= 'f') {
			return false
		}
	}
	return true
}

type receipt struct {
	RequestHash string
	CommandType string
	SessionID   foundation.ID
	Response    []byte
}

func loadReceipt(ctx context.Context, db rowQueryer, workspaceID foundation.ID, key, requestHash, commandType string) (receipt, bool, error) {
	var value receipt
	err := db.QueryRow(ctx, `SELECT request_hash,command_type,session_id::text,response FROM learning.interview_command WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key).Scan(&value.RequestHash, &value.CommandType, &value.SessionID, &value.Response)
	if errors.Is(err, pgx.ErrNoRows) {
		return receipt{}, false, nil
	}
	if err != nil {
		return receipt{}, false, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	if value.RequestHash != requestHash || value.CommandType != commandType {
		return receipt{}, false, domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview idempotency key is bound to another request")
	}
	return value, true, nil
}

func insertReceipt(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key, requestHash, commandType string, sessionID foundation.ID, response any, createdAt time.Time) error {
	encoded, err := encodeJSON(response)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO learning.interview_command(workspace_id,idempotency_key,request_hash,command_type,session_id,response,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, string(workspaceID), key, requestHash, commandType, string(sessionID), encoded, createdAt.UTC())
	if err != nil {
		return classify(err, domain.ErrorCodeIdempotencyConflict)
	}
	return nil
}
