package postgres

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

const draftSessionColumns = `
	id::text,workspace_id::text,answer_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,
	attempt_no,lease_owner,generation,status,next_sequence,total_bytes,expires_at,created_at,updated_at`

// DraftStreamRepository stores the short-lived final-answer stream projection.
// It does not publish canonical Answers; AnswerFinalizer owns that transaction.
type DraftStreamRepository struct{ db DB }

// NewDraftStreamRepository builds the PostgreSQL implementation of the
// application-owned draft store.
func NewDraftStreamRepository(db DB) (*DraftStreamRepository, error) {
	if isNilInterface(db) {
		return nil, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream database is nil"))
	}
	return &DraftStreamRepository{db: db}, nil
}

// BeginDraftStream validates the active runtime claim with database time,
// supersedes a prior recoverable generation, and creates the next generation.
func (repository *DraftStreamRepository) BeginDraftStream(ctx context.Context, command agentapplication.BeginDraftStreamCommand) (agentapplication.DraftStreamSession, error) {
	if repository == nil || isNilInterface(repository.db) {
		return agentapplication.DraftStreamSession{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if err := validateDraftBegin(ctx, command); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := verifyDraftPendingAnswer(ctx, tx, command.DraftStreamBinding, true); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	leaseUntil, err := lockDraftRuntimeClaim(ctx, tx, command.DraftStreamBinding)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	existing, found, err := findCurrentDraftByBinding(ctx, tx, command.DraftStreamBinding)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	now, err := validateDraftClaimLease(ctx, tx, leaseUntil)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	if found && existing.ExpiresAt.After(now) {
		if err := tx.Commit(ctx); err != nil {
			return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
		}
		return existing, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.answer_draft_session
			SET status='SUPERSEDED',completed_at=clock_timestamp(),updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND answer_id=$2 AND status IN ('ACTIVE','COMPLETED','DEGRADED')`,
		string(command.WorkspaceID), string(command.AnswerID)); err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	now, err = validateDraftClaimLease(ctx, tx, leaseUntil)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(generation),0)+1 FROM agent.answer_draft_session
		WHERE workspace_id=$1 AND answer_id=$2`, string(command.WorkspaceID), string(command.AnswerID)).Scan(&generation); err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	session, err := scanDraftSession(tx.QueryRow(ctx, `INSERT INTO agent.answer_draft_session(
		id,workspace_id,answer_id,workflow_run_id,node_run_id,node_attempt_id,attempt_no,lease_owner,generation,status,
		next_sequence,total_bytes,expires_at,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'ACTIVE',1,0,$10,$11,$11)
	RETURNING `+draftSessionColumns,
		string(id), string(command.WorkspaceID), string(command.AnswerID), string(command.WorkflowRunID), string(command.NodeRunID),
		string(command.NodeAttemptID), command.AttemptNo, command.LeaseOwner, generation, now.Add(command.TTL), now))
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return session, nil
}

// AppendDraftStream allocates exactly the next sequence after rechecking the
// active Attempt lease. node_run.version is intentionally absent from the
// fence because heartbeat advances it while the stream is being produced.
func (repository *DraftStreamRepository) AppendDraftStream(ctx context.Context, command agentapplication.DraftStreamAppendCommand) (agentapplication.DraftStreamChunk, error) {
	if repository == nil || isNilInterface(repository.db) {
		return agentapplication.DraftStreamChunk{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if err := validateDraftAppend(ctx, command); err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return agentapplication.DraftStreamChunk{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	leaseUntil, err := lockDraftRuntimeClaim(ctx, tx, command.Binding)
	if err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	session, err := loadDraftSession(ctx, tx, command.SessionID, true)
	if err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	now, err := validateDraftClaimLease(ctx, tx, leaseUntil)
	if err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	if !sameDraftBinding(session.Binding, command.Binding) || session.Status != agentapplication.DraftStreamActive || !session.ExpiresAt.After(now) {
		return agentapplication.DraftStreamChunk{}, conflict(ErrorCodeDraftStreamConflict, errors.New("draft session is no longer appendable"))
	}
	if err := verifyDraftPendingAnswer(ctx, tx, command.Binding, false); err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	bytes := len(command.Content)
	if session.TotalBytes > agentapplication.MaxDraftStreamSessionBytes-bytes {
		return agentapplication.DraftStreamChunk{}, conflict(ErrorCodeDraftStreamConflict, errors.New("draft session byte limit reached"))
	}
	sequence := session.NextSeq
	if _, err := tx.Exec(ctx, `INSERT INTO agent.answer_draft_chunk(session_id,sequence,content,created_at)
		VALUES($1,$2,$3,$4)`, string(session.ID), sequence, command.Content, now); err != nil {
		return agentapplication.DraftStreamChunk{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.answer_draft_session
		SET next_sequence=next_sequence+1,total_bytes=total_bytes+$2,updated_at=$3
		WHERE id=$1 AND status='ACTIVE' AND next_sequence=$4 AND total_bytes+$2 <= $5`,
		string(session.ID), bytes, now, sequence, agentapplication.MaxDraftStreamSessionBytes)
	if err != nil {
		return agentapplication.DraftStreamChunk{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return agentapplication.DraftStreamChunk{}, conflict(ErrorCodeDraftStreamConflict, errors.New("draft session append compare-and-swap failed"))
	}
	if err := tx.Commit(ctx); err != nil {
		return agentapplication.DraftStreamChunk{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return agentapplication.DraftStreamChunk{SessionID: session.ID, Generation: session.Generation, Sequence: sequence, Content: command.Content, CreatedAt: now}, nil
}

// CompleteDraftStream records Provider EOF. It never makes an Answer public.
func (repository *DraftStreamRepository) CompleteDraftStream(ctx context.Context, command agentapplication.DraftStreamTransitionCommand) (agentapplication.DraftStreamSession, error) {
	return repository.transitionDraftStream(ctx, command, agentapplication.DraftStreamCompleted)
}

// DegradeDraftStream stops draft projection after a bounded writer failure;
// final-answer generation remains free to drain and pass the normal gates.
func (repository *DraftStreamRepository) DegradeDraftStream(ctx context.Context, command agentapplication.DraftStreamTransitionCommand) (agentapplication.DraftStreamSession, error) {
	return repository.transitionDraftStream(ctx, command, agentapplication.DraftStreamDegraded)
}

// AbortDraftStream discards an unverified projection after cancellation or a
// downstream validation/finalization failure.
func (repository *DraftStreamRepository) AbortDraftStream(ctx context.Context, command agentapplication.DraftStreamTransitionCommand) (agentapplication.DraftStreamSession, error) {
	return repository.transitionDraftStream(ctx, command, agentapplication.DraftStreamAborted)
}

// ReadDraftStream returns only a current generation whose source Attempt still
// owns an unexpired DB-time lease. Published/aborted generations are returned
// without chunks so the HTTP adapter can emit their terminal SSE semantics.
func (repository *DraftStreamRepository) ReadDraftStream(ctx context.Context, query agentapplication.DraftStreamReadQuery) (agentapplication.DraftStreamReadResult, error) {
	if repository == nil || isNilInterface(repository.db) {
		return agentapplication.DraftStreamReadResult{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if ctx == nil || !validDraftID(query.WorkspaceID) || !validDraftID(query.AnswerID) || query.Limit < 1 || query.Limit > agentapplication.MaxDraftStreamReplayChunks ||
		(query.After != nil && (query.After.Generation < 1 || query.After.Sequence < 1)) {
		return agentapplication.DraftStreamReadResult{}, invalid(ErrorCodeDraftStreamInvalid, errors.New("draft stream read query is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Linearize replay against finalizer publication and generation supersession.
	// Without this row lock, later lease/chunk queries could return stale ACTIVE data.
	row := tx.QueryRow(ctx, `SELECT `+draftSessionColumns+`
		FROM agent.answer_draft_session AS session
		WHERE session.workspace_id=$1 AND session.answer_id=$2
		ORDER BY session.generation DESC LIMIT 1 FOR SHARE`, string(query.WorkspaceID), string(query.AnswerID))
	session, err := scanDraftSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
		}
		return agentapplication.DraftStreamReadResult{}, nil
	}
	if err != nil {
		return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if session.Status == agentapplication.DraftStreamPublished || session.Status == agentapplication.DraftStreamAborted || session.Status == agentapplication.DraftStreamSuperseded {
		if err := tx.Commit(ctx); err != nil {
			return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
		}
		return agentapplication.DraftStreamReadResult{Session: &session}, nil
	}
	var leaseValid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM workflow.node_attempt AS attempt
		JOIN workflow.node_run AS node ON node.id=attempt.node_run_id
		WHERE attempt.id=$1 AND attempt.attempt_no=$2 AND attempt.status='running' AND attempt.lease_owner=$3 AND attempt.lease_until>clock_timestamp()
		  AND node.id=$4 AND node.attempt=$2 AND node.status='running' AND node.lease_owner=$3 AND node.lease_until>clock_timestamp()
		  AND $5>clock_timestamp()
	)`, string(session.Binding.NodeAttemptID), session.Binding.AttemptNo, session.Binding.LeaseOwner, string(session.Binding.NodeRunID), session.ExpiresAt).Scan(&leaseValid); err != nil {
		return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if !leaseValid {
		if err := tx.Commit(ctx); err != nil {
			return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
		}
		return agentapplication.DraftStreamReadResult{Invalidated: true}, nil
	}
	afterSequence := int64(0)
	if query.After != nil && query.After.Generation == session.Generation {
		afterSequence = query.After.Sequence
	}
	rows, err := tx.Query(ctx, `SELECT sequence,content,created_at FROM agent.answer_draft_chunk
		WHERE session_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3`, string(session.ID), afterSequence, query.Limit)
	if err != nil {
		return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	chunks := make([]agentapplication.DraftStreamChunk, 0)
	for rows.Next() {
		chunk := agentapplication.DraftStreamChunk{SessionID: session.ID, Generation: session.Generation}
		if err := rows.Scan(&chunk.Sequence, &chunk.Content, &chunk.CreatedAt); err != nil {
			return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return agentapplication.DraftStreamReadResult{Session: &session, Chunks: chunks}, nil
}

func (repository *DraftStreamRepository) transitionDraftStream(ctx context.Context, command agentapplication.DraftStreamTransitionCommand, target agentapplication.DraftStreamStatus) (agentapplication.DraftStreamSession, error) {
	if repository == nil || isNilInterface(repository.db) {
		return agentapplication.DraftStreamSession{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if err := validateDraftTransition(ctx, command); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	leaseUntil, err := lockDraftRuntimeClaim(ctx, tx, command.Binding)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	session, err := loadDraftSession(ctx, tx, command.SessionID, true)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	now, err := validateDraftClaimLease(ctx, tx, leaseUntil)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	if !sameDraftBinding(session.Binding, command.Binding) || !session.ExpiresAt.After(now) {
		return agentapplication.DraftStreamSession{}, conflict(ErrorCodeDraftStreamConflict, errors.New("draft stream binding differs or session has expired"))
	}
	if err := verifyDraftPendingAnswer(ctx, tx, command.Binding, false); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	if transitionSatisfied(session.Status, target) {
		if err := tx.Commit(ctx); err != nil {
			return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
		}
		return session, nil
	}
	if !allowedDraftTransition(session.Status, target) {
		return agentapplication.DraftStreamSession{}, conflict(ErrorCodeDraftStreamConflict, errors.New("draft stream transition is invalid"))
	}
	now, err = validateDraftClaimLease(ctx, tx, leaseUntil)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	completedAt := any(nil)
	if target == agentapplication.DraftStreamCompleted || target == agentapplication.DraftStreamAborted {
		completedAt = now
	}
	session, err = scanDraftSession(tx.QueryRow(ctx, `UPDATE agent.answer_draft_session
		SET status=$2,completed_at=$3,updated_at=$4 WHERE id=$1 RETURNING `+draftSessionColumns,
		string(session.ID), string(target), completedAt, now))
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return session, nil
}

// CleanupExpiredDraftStreams removes terminal sessions and abandoned leases in
// bounded batches. Live claims are retained even when a caller runs cleanup.
func (repository *DraftStreamRepository) CleanupExpiredDraftStreams(ctx context.Context, limit int) (int64, error) {
	if repository == nil || isNilInterface(repository.db) {
		return 0, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if ctx == nil || limit < 1 || limit > 1000 {
		return 0, invalid(ErrorCodeDraftStreamInvalid, errors.New("draft cleanup limit is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return 0, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `WITH candidates AS (
		SELECT session.id
		FROM agent.answer_draft_session AS session
		WHERE session.expires_at <= clock_timestamp()
		  AND (
			session.status IN ('PUBLISHED','ABORTED','SUPERSEDED')
			OR NOT EXISTS (
				SELECT 1 FROM workflow.node_attempt AS attempt
				JOIN workflow.node_run AS node ON node.id=attempt.node_run_id
				WHERE attempt.id=session.node_attempt_id AND attempt.attempt_no=session.attempt_no
				  AND attempt.status='running' AND attempt.lease_owner=session.lease_owner AND attempt.lease_until>clock_timestamp()
				  AND node.id=session.node_run_id AND node.attempt=session.attempt_no
				  AND node.status='running' AND node.lease_owner=session.lease_owner AND node.lease_until>clock_timestamp()
			)
		  )
		ORDER BY session.expires_at,session.id
		FOR UPDATE SKIP LOCKED
		LIMIT $1
	)
	DELETE FROM agent.answer_draft_session AS session USING candidates
	WHERE session.id=candidates.id`, limit)
	if err != nil {
		return 0, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return tag.RowsAffected(), nil
}

func verifyDraftPendingAnswer(ctx context.Context, tx pgx.Tx, binding agentapplication.DraftStreamBinding, lock bool) error {
	locking := ""
	if lock {
		locking = " FOR UPDATE"
	}
	var answerID string
	err := tx.QueryRow(ctx, `SELECT answer.id::text
		FROM agent.answer AS answer
		WHERE answer.workspace_id=$1 AND answer.id=$2 AND answer.workflow_run_id=$3
		  AND answer.publication_status='pending'
	`+locking, string(binding.WorkspaceID), string(binding.AnswerID), string(binding.WorkflowRunID)).Scan(&answerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return conflict(ErrorCodeDraftStreamConflict, errors.New("answer is no longer pending"))
	}
	if err != nil {
		return classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return nil
}

func lockDraftRuntimeClaim(ctx context.Context, tx pgx.Tx, binding agentapplication.DraftStreamBinding) (time.Time, error) {
	var nodeLeaseUntil time.Time
	err := tx.QueryRow(ctx, `SELECT lease_until
		FROM workflow.node_run
		WHERE id=$1 AND run_id=$2 AND attempt=$3 AND status='running' AND lease_owner=$4
		FOR SHARE`, string(binding.NodeRunID), string(binding.WorkflowRunID), binding.AttemptNo, binding.LeaseOwner).Scan(&nodeLeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, conflict(ErrorCodeDraftStreamConflict, errors.New("runtime claim is no longer active"))
	}
	if err != nil {
		return time.Time{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	var attemptLeaseUntil time.Time
	err = tx.QueryRow(ctx, `SELECT lease_until
		FROM workflow.node_attempt
		WHERE id=$1 AND node_run_id=$2 AND attempt_no=$3 AND status='running' AND lease_owner=$4
		FOR SHARE`, string(binding.NodeAttemptID), string(binding.NodeRunID), binding.AttemptNo, binding.LeaseOwner).Scan(&attemptLeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, conflict(ErrorCodeDraftStreamConflict, errors.New("runtime claim is no longer active"))
	}
	if err != nil {
		return time.Time{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if attemptLeaseUntil.Before(nodeLeaseUntil) {
		return attemptLeaseUntil, nil
	}
	return nodeLeaseUntil, nil
}

func validateDraftClaimLease(ctx context.Context, tx pgx.Tx, leaseUntil time.Time) (time.Time, error) {
	now, err := draftDatabaseNow(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	if !leaseUntil.After(now) {
		return time.Time{}, conflict(ErrorCodeDraftStreamConflict, errors.New("runtime claim lease has expired"))
	}
	return now, nil
}

func loadDraftSession(ctx context.Context, tx pgx.Tx, sessionID foundation.ID, forUpdate bool) (agentapplication.DraftStreamSession, error) {
	query := `SELECT ` + draftSessionColumns + ` FROM agent.answer_draft_session WHERE id=$1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	session, err := scanDraftSession(tx.QueryRow(ctx, query, string(sessionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentapplication.DraftStreamSession{}, notFound(ErrorCodeDraftStreamConflict, err)
	}
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return session, nil
}

func findCurrentDraftByBinding(ctx context.Context, tx pgx.Tx, binding agentapplication.DraftStreamBinding) (agentapplication.DraftStreamSession, bool, error) {
	session, err := scanDraftSession(tx.QueryRow(ctx, `SELECT `+draftSessionColumns+`
		FROM agent.answer_draft_session
		WHERE workspace_id=$1 AND answer_id=$2 AND workflow_run_id=$3 AND node_run_id=$4 AND node_attempt_id=$5
		  AND attempt_no=$6 AND lease_owner=$7 AND status IN ('ACTIVE','COMPLETED','DEGRADED')
		FOR UPDATE`, string(binding.WorkspaceID), string(binding.AnswerID), string(binding.WorkflowRunID), string(binding.NodeRunID),
		string(binding.NodeAttemptID), binding.AttemptNo, binding.LeaseOwner))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentapplication.DraftStreamSession{}, false, nil
	}
	if err != nil {
		return agentapplication.DraftStreamSession{}, false, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return session, true, nil
}

func scanDraftSession(row pgx.Row) (agentapplication.DraftStreamSession, error) {
	var session agentapplication.DraftStreamSession
	var id, workspaceID, answerID, runID, nodeID, attemptID, status string
	if err := row.Scan(&id, &workspaceID, &answerID, &runID, &nodeID, &attemptID, &session.Binding.AttemptNo, &session.Binding.LeaseOwner,
		&session.Generation, &status, &session.NextSeq, &session.TotalBytes, &session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	session.ID, session.Binding.WorkspaceID, session.Binding.AnswerID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(answerID)
	session.Binding.WorkflowRunID, session.Binding.NodeRunID, session.Binding.NodeAttemptID = foundation.ID(runID), foundation.ID(nodeID), foundation.ID(attemptID)
	session.Status = agentapplication.DraftStreamStatus(status)
	return session, nil
}

func draftDatabaseNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return now.UTC(), nil
}

func validateDraftBegin(ctx context.Context, command agentapplication.BeginDraftStreamCommand) error {
	if ctx == nil || !validDraftBinding(command.DraftStreamBinding) || command.TTL <= 0 || command.TTL > agentapplication.MaxDraftStreamTTL {
		return invalid(ErrorCodeDraftStreamInvalid, errors.New("draft begin command is invalid"))
	}
	return nil
}

func validateDraftAppend(ctx context.Context, command agentapplication.DraftStreamAppendCommand) error {
	if ctx == nil || !validDraftBinding(command.Binding) || !validDraftID(command.SessionID) || command.Content == "" ||
		len(command.Content) > agentapplication.MaxAnswerStreamChunkBytes || !utf8.ValidString(command.Content) {
		return invalid(ErrorCodeDraftStreamInvalid, errors.New("draft append command is invalid"))
	}
	return nil
}

func validateDraftTransition(ctx context.Context, command agentapplication.DraftStreamTransitionCommand) error {
	if ctx == nil || !validDraftBinding(command.Binding) || !validDraftID(command.SessionID) {
		return invalid(ErrorCodeDraftStreamInvalid, errors.New("draft transition command is invalid"))
	}
	return nil
}

func validDraftBinding(binding agentapplication.DraftStreamBinding) bool {
	for _, id := range []foundation.ID{binding.WorkspaceID, binding.AnswerID, binding.WorkflowRunID, binding.NodeRunID, binding.NodeAttemptID} {
		if !validDraftID(id) {
			return false
		}
	}
	return binding.AttemptNo > 0 && strings.TrimSpace(binding.LeaseOwner) == binding.LeaseOwner && binding.LeaseOwner != "" && len(binding.LeaseOwner) <= 256
}

func validDraftID(id foundation.ID) bool {
	_, err := foundation.ParseID(string(id))
	return err == nil
}

func sameDraftBinding(left, right agentapplication.DraftStreamBinding) bool {
	return left == right
}

func transitionSatisfied(current, target agentapplication.DraftStreamStatus) bool {
	return current == target || (current == agentapplication.DraftStreamDegraded && target == agentapplication.DraftStreamCompleted)
}

func allowedDraftTransition(current, target agentapplication.DraftStreamStatus) bool {
	switch target {
	case agentapplication.DraftStreamCompleted:
		return current == agentapplication.DraftStreamActive
	case agentapplication.DraftStreamDegraded:
		return current == agentapplication.DraftStreamActive
	case agentapplication.DraftStreamAborted:
		return current == agentapplication.DraftStreamActive || current == agentapplication.DraftStreamCompleted || current == agentapplication.DraftStreamDegraded
	default:
		return false
	}
}

var _ agentapplication.DraftStreamStore = (*DraftStreamRepository)(nil)
