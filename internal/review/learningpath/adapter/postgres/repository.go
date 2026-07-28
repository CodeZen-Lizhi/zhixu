package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	pathapp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	pathdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB 是 Learning Path Repository 需要的最小 pgx 边界。
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Repository 是共享 Learning Path 的 PostgreSQL Store。
type Repository struct{ db DB }

var _ pathapp.Store = (*Repository)(nil)

// NewRepository 构造共享 Learning Path PostgreSQL Repository。
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, pathdomain.UnavailableError(pathdomain.ErrorCodeDependencyUnavailable, "learning path database is required")
	}
	return &Repository{db: db}, nil
}

// FindCreateReplay 按请求哈希精确读取创建 receipt。
func (repository *Repository) FindCreateReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (pathapp.Result, bool, error) {
	return repository.findResultReceipt(ctx, repository.db, workspaceID, key, requestHash, pathapp.CommandTypeCreateReviewPath, 0)
}

// BeginReviewCreate locks the Workspace and Answer, validates the complete
// score Evidence set, and creates a reservation only for an actionable gap.
func (repository *Repository) BeginReviewCreate(ctx context.Context, workspaceID, answerID foundation.ID, key, requestHash string) (pathapp.Reservation, pathapp.ReviewSnapshot, *pathapp.Result, error) {
	tx, err := repository.begin(ctx)
	if err != nil {
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
	}
	defer rollback(tx)
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
	}
	if receipt, found, err := repository.findResultReceipt(ctx, tx, workspaceID, key, requestHash, pathapp.CommandTypeCreateReviewPath, 0); err != nil || found {
		if err != nil {
			return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
		}
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, &receipt, commit(ctx, tx)
	}
	if err := lockReviewAnswer(ctx, tx, workspaceID, answerID); err != nil {
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
	}

	reservation, found, err := loadReservation(ctx, tx, workspaceID, answerID, true)
	if err != nil {
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
	}
	if found {
		if reservation.Status == pathapp.ReservationCompleted {
			result, err := repository.loadPathResult(ctx, tx, workspaceID, answerID)
			if err != nil {
				return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
			}
			if reservation.IdempotencyKey == key && reservation.RequestHash == requestHash {
				return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "completed learning path reservation has no create receipt")
			}
			if err := insertResultReceipt(ctx, tx, workspaceID, key, requestHash, pathapp.CommandTypeCreateReviewPath, 0, result); err != nil {
				return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
			}
			result.Replayed = true
			return reservation, pathapp.ReviewSnapshot{}, &result, commit(ctx, tx)
		}
		if reservation.IdempotencyKey != key || reservation.RequestHash != requestHash {
			return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, pathdomain.ConflictError(pathdomain.ErrorCodeReservationPending, "review answer already has a learning path reservation")
		}
		if reservation.Status == pathapp.ReservationPending {
			if err := commit(ctx, tx); err != nil {
				return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
			}
			return reservation, reservation.SourceSnapshot, nil, nil
		}
		if reservation.Status != pathapp.ReservationAbandoned {
			return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path reservation status is invalid")
		}
	}

	snapshot, err := loadReviewSnapshot(ctx, tx, workspaceID, answerID)
	if err != nil {
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
	}
	if !snapshot.Gap.Actionable() {
		if err := commit(ctx, tx); err != nil {
			return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
		}
		return pathapp.Reservation{}, snapshot, nil, nil
	}
	canonical, snapshotJSON, snapshotDigest, err := pathapp.CanonicalReviewSnapshot(snapshot)
	if err != nil {
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
	}
	currentAttempt := int64(0)
	if found {
		currentAttempt = reservation.AttemptNo
	}
	reservation = pathapp.Reservation{
		WorkspaceID: workspaceID, ReviewAnswerID: answerID, IdempotencyKey: key, RequestHash: requestHash,
		SourceSnapshot: canonical, SourceSnapshotDigest: snapshotDigest, AttemptNo: 1, Status: pathapp.ReservationPending,
	}
	if found {
		err = tx.QueryRow(ctx, `UPDATE learning.learning_path_creation_reservation
			SET source_snapshot=$5,source_snapshot_digest=$6,attempt_no=attempt_no+1,
				artifact_digest=NULL,path_id=NULL,artifact_id=NULL,artifact_revision_id=NULL,artifact_version=NULL,
				status='PENDING',prepared_at=NULL,completed_at=NULL,abandoned_at=NULL,
				created_at=statement_timestamp(),updated_at=statement_timestamp()
			WHERE workspace_id=$1 AND review_answer_id=$2 AND idempotency_key=$3 AND request_hash=$4
			  AND status='ABANDONED' AND attempt_no=$7
			RETURNING attempt_no,created_at,updated_at`, string(workspaceID), string(answerID), key, requestHash,
			snapshotJSON, snapshotDigest, currentAttempt).
			Scan(&reservation.AttemptNo, &reservation.CreatedAt, &reservation.UpdatedAt)
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO learning.learning_path_creation_reservation(
			workspace_id,review_answer_id,idempotency_key,request_hash,source_snapshot,source_snapshot_digest,
			attempt_no,status,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,1,'PENDING',statement_timestamp(),statement_timestamp())
		RETURNING created_at,updated_at`, string(workspaceID), string(answerID), key, requestHash, snapshotJSON, snapshotDigest).
			Scan(&reservation.CreatedAt, &reservation.UpdatedAt)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, pathdomain.ConflictError(pathdomain.ErrorCodeReservationPending, "learning path reservation retry lost its compare-and-swap")
	}
	if err != nil {
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, classify(err)
	}
	if err := commit(ctx, tx); err != nil {
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
	}
	return reservation, canonical, nil, nil
}

// PrepareReviewCreate fences a pending reservation to one deterministic Artifact digest.
func (repository *Repository) PrepareReviewCreate(ctx context.Context, reservation pathapp.Reservation, digest string) (pathapp.Reservation, *pathapp.Result, error) {
	if !validHash(digest) {
		return pathapp.Reservation{}, nil, pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path artifact digest is invalid")
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return pathapp.Reservation{}, nil, err
	}
	defer rollback(tx)
	current, found, err := loadReservation(ctx, tx, reservation.WorkspaceID, reservation.ReviewAnswerID, true)
	if err != nil || !found {
		if err != nil {
			return pathapp.Reservation{}, nil, err
		}
		return pathapp.Reservation{}, nil, pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "learning path reservation was not found")
	}
	if current.IdempotencyKey != reservation.IdempotencyKey || current.RequestHash != reservation.RequestHash ||
		current.SourceSnapshotDigest != reservation.SourceSnapshotDigest || current.AttemptNo != reservation.AttemptNo {
		return pathapp.Reservation{}, nil, pathdomain.ConflictError(pathdomain.ErrorCodeIdempotencyConflict, "learning path reservation binding drifted")
	}
	if current.Status == pathapp.ReservationCompleted {
		result, err := repository.loadPathResult(ctx, tx, current.WorkspaceID, current.ReviewAnswerID)
		if err != nil {
			return pathapp.Reservation{}, nil, err
		}
		result.Replayed = true
		return current, &result, commit(ctx, tx)
	}
	if current.Status != pathapp.ReservationPending {
		return pathapp.Reservation{}, nil, pathdomain.ConflictError(pathdomain.ErrorCodeReservationPending, "learning path reservation is not pending")
	}
	if current.ArtifactDigest != "" && current.ArtifactDigest != digest {
		return pathapp.Reservation{}, nil, pathdomain.ConflictError(pathdomain.ErrorCodeArtifactConflict, "learning path artifact digest conflicts")
	}
	if current.ArtifactDigest == "" {
		err := tx.QueryRow(ctx, `UPDATE learning.learning_path_creation_reservation
			SET artifact_digest=$3,prepared_at=statement_timestamp(),updated_at=statement_timestamp()
			WHERE workspace_id=$1 AND review_answer_id=$2 AND status='PENDING' AND artifact_digest IS NULL
			  AND attempt_no=$4
			RETURNING updated_at`, string(current.WorkspaceID), string(current.ReviewAnswerID), digest, current.AttemptNo).Scan(&current.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return pathapp.Reservation{}, nil, pathdomain.ConflictError(pathdomain.ErrorCodeArtifactConflict, "learning path reservation preparation lost its compare-and-swap")
		}
		if err != nil {
			return pathapp.Reservation{}, nil, classify(err)
		}
		current.ArtifactDigest = digest
	}
	if err := commit(ctx, tx); err != nil {
		return pathapp.Reservation{}, nil, err
	}
	return current, nil, nil
}

// CompleteReviewCreate atomically writes Path, Steps, receipt, final reservation
// binding, and releases only the exact hidden Artifact hold.
func (repository *Repository) CompleteReviewCreate(ctx context.Context, reservation pathapp.Reservation, path pathdomain.Path, steps []pathdomain.Step) (pathapp.Result, error) {
	result := pathapp.Result{Path: path, Steps: append([]pathdomain.Step(nil), steps...)}
	if err := validateResult(result, reservation.WorkspaceID, path.ID, path.Version); err != nil {
		return pathapp.Result{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return pathapp.Result{}, err
	}
	defer rollback(tx)
	if err := lockReviewAnswer(ctx, tx, reservation.WorkspaceID, reservation.ReviewAnswerID); err != nil {
		return pathapp.Result{}, err
	}
	current, found, err := loadReservation(ctx, tx, reservation.WorkspaceID, reservation.ReviewAnswerID, true)
	if err != nil || !found {
		if err != nil {
			return pathapp.Result{}, err
		}
		return pathapp.Result{}, pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "learning path reservation was not found")
	}
	if current.IdempotencyKey != reservation.IdempotencyKey || current.RequestHash != reservation.RequestHash ||
		current.SourceSnapshotDigest != reservation.SourceSnapshotDigest || current.AttemptNo != reservation.AttemptNo {
		return pathapp.Result{}, pathdomain.ConflictError(pathdomain.ErrorCodeIdempotencyConflict, "learning path completion reservation drifted")
	}
	if current.Status == pathapp.ReservationCompleted {
		replayed, err := repository.loadPathResult(ctx, tx, reservation.WorkspaceID, reservation.ReviewAnswerID)
		if err != nil {
			return pathapp.Result{}, err
		}
		replayed.Replayed = true
		return replayed, commit(ctx, tx)
	}
	if current.Status != pathapp.ReservationPending || current.ArtifactDigest == "" || current.ArtifactDigest != reservation.ArtifactDigest {
		return pathapp.Result{}, pathdomain.ConflictError(pathdomain.ErrorCodeArtifactConflict, "learning path completion reservation drifted")
	}
	if path.WorkspaceID != reservation.WorkspaceID || path.ReviewAnswerID == nil ||
		*path.ReviewAnswerID != reservation.ReviewAnswerID || !path.CreatedAt.Equal(current.CreatedAt) {
		return pathapp.Result{}, pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path source binding is invalid")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO learning.learning_path(
		id,workspace_id,origin_type,review_answer_id,source_policy_version,artifact_id,
		artifact_revision_id,artifact_version,status,version,created_at,updated_at
	) VALUES($1,$2,'REVIEW',$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		string(path.ID), string(path.WorkspaceID), string(reservation.ReviewAnswerID), path.SourcePolicyVersion,
		string(path.Artifact.ArtifactID), string(path.Artifact.ArtifactRevisionID), path.Artifact.ArtifactVersion,
		string(path.Status), path.Version, path.CreatedAt.UTC(), path.UpdatedAt.UTC()); err != nil {
		return pathapp.Result{}, classify(err)
	}
	for _, step := range steps {
		var topicID any
		if step.TopicID != nil {
			topicID = string(*step.TopicID)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.learning_path_step(
			id,workspace_id,path_id,step_no,claim_id,topic_id,source_version_id,source_span_id,
			evidence_hash,title,rationale,status,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			string(step.ID), string(step.WorkspaceID), string(step.PathID), step.StepNo, string(step.ClaimID), topicID,
			string(step.SourceVersionID), string(step.SourceSpanID), step.EvidenceHash, step.Title, step.Rationale,
			string(step.Status), step.Version, step.CreatedAt.UTC(), step.UpdatedAt.UTC()); err != nil {
			return pathapp.Result{}, classify(err)
		}
	}
	if err := insertResultReceipt(ctx, tx, path.WorkspaceID, reservation.IdempotencyKey, reservation.RequestHash, pathapp.CommandTypeCreateReviewPath, 0, result); err != nil {
		return pathapp.Result{}, err
	}
	completed, err := tx.Exec(ctx, `UPDATE learning.learning_path_creation_reservation
		SET status='COMPLETED',path_id=$3,artifact_id=$4,artifact_revision_id=$5,artifact_version=$6,
			completed_at=statement_timestamp(),updated_at=statement_timestamp()
		WHERE workspace_id=$1 AND review_answer_id=$2 AND status='PENDING'
		  AND idempotency_key=$7 AND request_hash=$8 AND source_snapshot_digest=$9 AND artifact_digest=$10
		  AND attempt_no=$11`,
		string(path.WorkspaceID), string(reservation.ReviewAnswerID), string(path.ID), string(path.Artifact.ArtifactID),
		string(path.Artifact.ArtifactRevisionID), path.Artifact.ArtifactVersion, reservation.IdempotencyKey,
		reservation.RequestHash, reservation.SourceSnapshotDigest, reservation.ArtifactDigest, reservation.AttemptNo)
	if err != nil {
		return pathapp.Result{}, classify(err)
	}
	if completed.RowsAffected() != 1 {
		return pathapp.Result{}, pathdomain.ConflictError(pathdomain.ErrorCodeArtifactConflict, "learning path reservation completion lost its compare-and-swap")
	}
	released, err := tx.Exec(ctx, `DELETE FROM learning.artifact_visibility_hold
		WHERE workspace_id=$1 AND artifact_id=$2 AND owner_type='LEARNING_PATH_CREATE'
		  AND owner_id=$3 AND review_answer_id=$3 AND owner_role='PATH'
		  AND attempt_digest=$4 AND disposition='ACTIVE'`,
		string(path.WorkspaceID), string(path.Artifact.ArtifactID), string(reservation.ReviewAnswerID), reservation.ArtifactDigest)
	if err != nil {
		return pathapp.Result{}, classify(err)
	}
	if released.RowsAffected() != 1 {
		return pathapp.Result{}, pathdomain.ConflictError(pathdomain.ErrorCodeArtifactConflict, "learning path hold was not released exactly once")
	}
	if err := commit(ctx, tx); err != nil {
		return pathapp.Result{}, err
	}
	return result, nil
}

// GetByReviewAnswer reads the unique Review-origin Path.
func (repository *Repository) GetByReviewAnswer(ctx context.Context, workspaceID, answerID foundation.ID) (pathapp.Result, error) {
	return repository.loadPathResult(ctx, repository.db, workspaceID, answerID)
}

// Get reads one Review-origin Path by identity.
func (repository *Repository) Get(ctx context.Context, workspaceID, pathID foundation.ID) (pathapp.Result, error) {
	return loadResultByPath(ctx, repository.db, workspaceID, pathID, false)
}

// FindPathStatusReplay reads one exact path-state command receipt.
func (repository *Repository) FindPathStatusReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string, expectedVersion int64) (pathapp.Result, bool, error) {
	return repository.findResultReceipt(ctx, repository.db, workspaceID, key, requestHash, pathapp.CommandTypePathStatus, expectedVersion)
}

// UpdatePathStatus persists a user-authorized lifecycle transition with aggregate CAS.
func (repository *Repository) UpdatePathStatus(ctx context.Context, record pathapp.UpdatePathStatusRecord) (pathapp.Result, error) {
	tx, err := repository.begin(ctx)
	if err != nil {
		return pathapp.Result{}, err
	}
	defer rollback(tx)
	if err := lockCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
		return pathapp.Result{}, err
	}
	if replay, found, err := repository.findResultReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, pathapp.CommandTypePathStatus, record.ExpectedVersion); err != nil || found {
		if err != nil {
			return pathapp.Result{}, err
		}
		return replay, commit(ctx, tx)
	}
	result, err := loadResultByPath(ctx, tx, record.WorkspaceID, record.PathID, true)
	if err != nil {
		return pathapp.Result{}, err
	}
	if result.Path.Version != record.ExpectedVersion || !pathdomain.CanTransitionPath(result.Path.Status, record.Status) ||
		(record.Status == pathdomain.StatusCompleted && !pathdomain.AllStepsTerminal(result.Steps)) {
		return pathapp.Result{}, pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path status transition is invalid")
	}
	updated, err := tx.Exec(ctx, `UPDATE learning.learning_path
		SET status=$3,version=version+1,updated_at=$4
		WHERE workspace_id=$1 AND id=$2 AND version=$5`, string(record.WorkspaceID), string(record.PathID),
		string(record.Status), record.At.UTC(), record.ExpectedVersion)
	if err != nil {
		return pathapp.Result{}, classify(err)
	}
	if updated.RowsAffected() != 1 {
		return pathapp.Result{}, pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path version changed")
	}
	result.Path.Status, result.Path.Version, result.Path.UpdatedAt = record.Status, record.ExpectedVersion+1, record.At.UTC()
	if err := insertResultReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, pathapp.CommandTypePathStatus, record.ExpectedVersion, result); err != nil {
		return pathapp.Result{}, err
	}
	if err := commit(ctx, tx); err != nil {
		return pathapp.Result{}, err
	}
	return result, nil
}

// FindStepReplay reads one exact step command receipt.
func (repository *Repository) FindStepReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string, expectedVersion int64) (pathapp.StepResult, bool, error) {
	return findStepReceipt(ctx, repository.db, workspaceID, key, requestHash, expectedVersion)
}

// UpdateStep advances one Step and the parent Path version in the same transaction.
func (repository *Repository) UpdateStep(ctx context.Context, record pathapp.UpdateStepRecord) (pathapp.StepResult, error) {
	tx, err := repository.begin(ctx)
	if err != nil {
		return pathapp.StepResult{}, err
	}
	defer rollback(tx)
	if err := lockCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
		return pathapp.StepResult{}, err
	}
	if replay, found, err := findStepReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, record.ExpectedVersion); err != nil || found {
		if err != nil {
			return pathapp.StepResult{}, err
		}
		return replay, commit(ctx, tx)
	}
	result, err := loadResultByPath(ctx, tx, record.WorkspaceID, record.PathID, true)
	if err != nil {
		return pathapp.StepResult{}, err
	}
	if result.Path.Version != record.ExpectedVersion || result.Path.Status != pathdomain.StatusActive {
		return pathapp.StepResult{}, pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path is not active at the expected version")
	}
	selectedIndex := -1
	for index := range result.Steps {
		if result.Steps[index].ID == record.StepID {
			selectedIndex = index
			break
		}
	}
	if selectedIndex < 0 || !pathdomain.CanTransitionStep(result.Steps[selectedIndex].Status, record.Status) {
		return pathapp.StepResult{}, pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path step transition is invalid")
	}
	currentStepVersion := result.Steps[selectedIndex].Version
	updated, err := tx.Exec(ctx, `UPDATE learning.learning_path_step
		SET status=$4,version=version+1,updated_at=$5
		WHERE workspace_id=$1 AND path_id=$2 AND id=$3 AND version=$6`,
		string(record.WorkspaceID), string(record.PathID), string(record.StepID), string(record.Status),
		record.At.UTC(), currentStepVersion)
	if err != nil {
		return pathapp.StepResult{}, classify(err)
	}
	if updated.RowsAffected() != 1 {
		return pathapp.StepResult{}, pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path step version changed")
	}
	result.Steps[selectedIndex].Status = record.Status
	result.Steps[selectedIndex].Version++
	result.Steps[selectedIndex].UpdatedAt = record.At.UTC()
	nextPathStatus := result.Path.Status
	if pathdomain.AllStepsTerminal(result.Steps) {
		nextPathStatus = pathdomain.StatusCompleted
	}
	updated, err = tx.Exec(ctx, `UPDATE learning.learning_path
		SET status=$4,version=version+1,updated_at=$5
		WHERE workspace_id=$1 AND id=$2 AND version=$3`, string(record.WorkspaceID), string(record.PathID),
		record.ExpectedVersion, string(nextPathStatus), record.At.UTC())
	if err != nil {
		return pathapp.StepResult{}, classify(err)
	}
	if updated.RowsAffected() != 1 {
		return pathapp.StepResult{}, pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path version changed")
	}
	result.Path.Status = nextPathStatus
	result.Path.Version = record.ExpectedVersion + 1
	result.Path.UpdatedAt = record.At.UTC()
	response := pathapp.StepResult{Path: result.Path, Step: result.Steps[selectedIndex]}
	if err := insertStepReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, record.ExpectedVersion, response); err != nil {
		return pathapp.StepResult{}, err
	}
	if err := commit(ctx, tx); err != nil {
		return pathapp.StepResult{}, err
	}
	return response, nil
}

// MaintainReservations abandons a bounded, locked batch and orphans only each
// reservation's exact active Artifact hold.
func (repository *Repository) MaintainReservations(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit < 1 || limit > pathapp.MaxMaintenanceBatch {
		return 0, pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path maintenance limit is invalid")
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	var abandoned, orphaned int
	err = tx.QueryRow(ctx, `WITH candidates AS (
		SELECT workspace_id,review_answer_id
		FROM learning.learning_path_creation_reservation
		WHERE status='PENDING' AND updated_at <= $1
		ORDER BY updated_at,workspace_id,review_answer_id
		LIMIT $2 FOR UPDATE SKIP LOCKED
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
	SELECT (SELECT count(*) FROM abandoned),(SELECT count(*) FROM orphaned)`, before.UTC(), limit).Scan(&abandoned, &orphaned)
	if err != nil {
		return 0, classify(err)
	}
	if orphaned > abandoned {
		return 0, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path maintenance found duplicate active holds")
	}
	if err := commit(ctx, tx); err != nil {
		return 0, err
	}
	return abandoned, nil
}

func (repository *Repository) begin(ctx context.Context) (pgx.Tx, error) {
	if repository == nil || repository.db == nil {
		return nil, pathdomain.UnavailableError(pathdomain.ErrorCodeDependencyUnavailable, "learning path database is unavailable")
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return nil, classify(err)
	}
	return tx, nil
}

func rollback(tx pgx.Tx) { _ = tx.Rollback(context.Background()) }

func commit(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return classify(err)
	}
	return nil
}

func lockWorkspace(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR UPDATE`, string(workspaceID)).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "workspace was not found")
	} else if err != nil {
		return classify(err)
	}
	return nil
}

func lockReviewAnswer(ctx context.Context, tx pgx.Tx, workspaceID, answerID foundation.ID) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM learning.review_answer
		WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(workspaceID), string(answerID)).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "review answer was not found")
	} else if err != nil {
		return classify(err)
	}
	return nil
}

func lockCommand(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(workspaceID)+":"+key); err != nil {
		return classify(err)
	}
	return nil
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var known *foundation.Error
	if errors.As(err, &known) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, pathdomain.ErrorCodeDependencyUnavailable, false, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return pathdomain.ConflictError(pathdomain.ErrorCodeIdempotencyConflict, "learning path state already exists")
		case "23503", "23514", "23502", "22001", "22P02":
			return foundation.NewError(foundation.ErrorConsistencyViolation, pathdomain.ErrorCodePersistenceInvalid, false, err)
		}
	}
	return pathdomain.UnavailableError(pathdomain.ErrorCodeDependencyUnavailable, fmt.Sprintf("learning path persistence failed: %v", err))
}
