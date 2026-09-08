package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMDraftStreamRepository 持久化有 TTL 的草稿投影，正式发布仍由 Answer Finalizer 拥有。
type GORMDraftStreamRepository struct {
	db  *gorm.DB
	uow foundation.UnitOfWork
}

// NewGORMDraftStreamRepository 从唯一物理 Pool 构造草稿存储。
func NewGORMDraftStreamRepository(pool *platformpostgres.Pool) (*GORMDraftStreamRepository, error) {
	db, uow, err := conversationGORMDependencies(pool, ErrorCodeDraftStreamUnavailable)
	if err != nil {
		return nil, err
	}
	return &GORMDraftStreamRepository{db: db, uow: uow}, nil
}

// BeginDraftStream 使用数据库时间与原有 lease fence 完成草稿操作。
func (repository *GORMDraftStreamRepository) BeginDraftStream(ctx context.Context, command agentapplication.BeginDraftStreamCommand) (agentapplication.DraftStreamSession, error) {
	if repository == nil || repository.db == nil || isNilInterface(repository.uow) {
		return agentapplication.DraftStreamSession{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if err := validateDraftBegin(ctx, command); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	var result agentapplication.DraftStreamSession
	err := withinConversationTransaction(ctx, repository.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		result, err = repository.beginDraftStreamScoped(ctx, tx, command)
		return err
	})
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return result, nil
}

// AppendDraftStream 使用数据库时间与原有 lease fence 完成草稿操作。
func (repository *GORMDraftStreamRepository) AppendDraftStream(ctx context.Context, command agentapplication.DraftStreamAppendCommand) (agentapplication.DraftStreamChunk, error) {
	if repository == nil || repository.db == nil || isNilInterface(repository.uow) {
		return agentapplication.DraftStreamChunk{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if err := validateDraftAppend(ctx, command); err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	var result agentapplication.DraftStreamChunk
	err := withinConversationTransaction(ctx, repository.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		result, err = repository.appendDraftStreamScoped(ctx, tx, command)
		return err
	})
	if err != nil {
		return agentapplication.DraftStreamChunk{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return result, nil
}

// ReadDraftStream 使用数据库时间与原有 lease fence 完成草稿操作。
func (repository *GORMDraftStreamRepository) ReadDraftStream(ctx context.Context, query agentapplication.DraftStreamReadQuery) (agentapplication.DraftStreamReadResult, error) {
	if repository == nil || repository.db == nil || isNilInterface(repository.uow) {
		return agentapplication.DraftStreamReadResult{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if ctx == nil || !validDraftID(query.WorkspaceID) || !validDraftID(query.AnswerID) || query.Limit < 1 || query.Limit > agentapplication.MaxDraftStreamReplayChunks || (query.After != nil && (query.After.Generation < 1 || query.After.Sequence < 1)) {
		return agentapplication.DraftStreamReadResult{}, invalid(ErrorCodeDraftStreamInvalid, errors.New("draft stream read query is invalid"))
	}
	var result agentapplication.DraftStreamReadResult
	err := withinConversationTransaction(ctx, repository.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		result, err = repository.readDraftStreamScoped(ctx, tx, query)
		return err
	})
	if err != nil {
		return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return result, nil
}

// transitionDraftStream 使用数据库时间与原有 lease fence 完成草稿操作。
func (repository *GORMDraftStreamRepository) transitionDraftStream(ctx context.Context, command agentapplication.DraftStreamTransitionCommand, target agentapplication.DraftStreamStatus) (agentapplication.DraftStreamSession, error) {
	if repository == nil || repository.db == nil || isNilInterface(repository.uow) {
		return agentapplication.DraftStreamSession{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if err := validateDraftTransition(ctx, command); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	var result agentapplication.DraftStreamSession
	err := withinConversationTransaction(ctx, repository.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		result, err = repository.transitionDraftStreamScoped(ctx, tx, command, target)
		return err
	})
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return result, nil
}

// CleanupExpiredDraftStreams 使用数据库时间与原有 lease fence 完成草稿操作。
func (repository *GORMDraftStreamRepository) CleanupExpiredDraftStreams(ctx context.Context, limit int) (int64, error) {
	if repository == nil || repository.db == nil || isNilInterface(repository.uow) {
		return 0, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if ctx == nil || limit < 1 || limit > 1000 {
		return 0, invalid(ErrorCodeDraftStreamInvalid, errors.New("draft cleanup limit is invalid"))
	}
	var result int64
	err := withinConversationTransaction(ctx, repository.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		result, err = repository.cleanupExpiredDraftStreamsScoped(ctx, tx, limit)
		return err
	})
	if err != nil {
		return 0, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return result, nil
}
func (repository *GORMDraftStreamRepository) beginDraftStreamScoped(ctx context.Context, tx *gorm.DB, command agentapplication.BeginDraftStreamCommand) (agentapplication.DraftStreamSession, error) {
	if repository == nil || isNilInterface(repository.db) {
		return agentapplication.DraftStreamSession{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if err := validateDraftBegin(ctx, command); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	if err := gormVerifyDraftPendingAnswer(ctx, tx, command.DraftStreamBinding, true); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	leaseUntil, err := gormLockDraftRuntimeClaim(ctx, tx, command.DraftStreamBinding)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	existing, found, err := gormFindDraftByBinding(ctx, tx, command.DraftStreamBinding)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	now, err := gormValidateDraftClaimLease(ctx, tx, leaseUntil)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	if found {
		if existing.Status != agentapplication.DraftStreamActive && existing.Status != agentapplication.DraftStreamCompleted && existing.Status != agentapplication.DraftStreamDegraded {
			return agentapplication.DraftStreamSession{}, conflict(
				ErrorCodeDraftStreamConflict,
				errors.New("terminal draft session cannot be reopened by the same attempt"),
			)
		}
		if !existing.ExpiresAt.After(now) {
			return agentapplication.DraftStreamSession{}, conflict(
				ErrorCodeDraftStreamConflict,
				errors.New("expired draft session cannot be reopened by the same attempt"),
			)
		}
		return existing, nil
	}
	if err := tx.WithContext(ctx).Exec(`UPDATE agent.answer_draft_session
			SET status='SUPERSEDED',completed_at=clock_timestamp(),updated_at=clock_timestamp()
		WHERE workspace_id=? AND answer_id=? AND status IN ('ACTIVE','COMPLETED','DEGRADED')`, string(command.WorkspaceID), string(command.AnswerID)).Error; err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	now, err = gormValidateDraftClaimLease(ctx, tx, leaseUntil)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	var generation int64
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT COALESCE(MAX(generation),0)+1 FROM agent.answer_draft_session
		WHERE workspace_id=? AND answer_id=?`, string(command.WorkspaceID), string(command.AnswerID))).Scan(&generation); err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	model := draftSessionModel{ID: string(id), WorkspaceID: string(command.WorkspaceID), AnswerID: string(command.AnswerID), WorkflowRunID: string(command.WorkflowRunID), NodeRunID: string(command.NodeRunID), NodeAttemptID: string(command.NodeAttemptID), AttemptNo: command.AttemptNo, LeaseOwner: command.LeaseOwner, Generation: generation, Status: "ACTIVE", NextSequence: 1, TotalBytes: 0, ExpiresAt: now.Add(command.TTL), CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&model).Error; err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	session, err := scanDraftSession(gormScanRow(tx.Model(&draftSessionModel{}).Select(draftSessionColumns).Where("id=?", model.ID)))
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return session, nil
}

func (repository *GORMDraftStreamRepository) appendDraftStreamScoped(ctx context.Context, tx *gorm.DB, command agentapplication.DraftStreamAppendCommand) (agentapplication.DraftStreamChunk, error) {
	if repository == nil || isNilInterface(repository.db) {
		return agentapplication.DraftStreamChunk{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if err := validateDraftAppend(ctx, command); err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	leaseUntil, err := gormLockDraftRuntimeClaim(ctx, tx, command.Binding)
	if err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	session, err := gormLoadDraftSession(ctx, tx, command.SessionID, true)
	if err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	now, err := gormValidateDraftClaimLease(ctx, tx, leaseUntil)
	if err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	if !sameDraftBinding(session.Binding, command.Binding) || session.Status != agentapplication.DraftStreamActive || !session.ExpiresAt.After(now) {
		return agentapplication.DraftStreamChunk{}, conflict(ErrorCodeDraftStreamConflict, errors.New("draft session is no longer appendable"))
	}
	if err := gormVerifyDraftPendingAnswer(ctx, tx, command.Binding, false); err != nil {
		return agentapplication.DraftStreamChunk{}, err
	}
	bytes := len(command.Content)
	if session.TotalBytes > agentapplication.MaxDraftStreamSessionBytes-bytes {
		return agentapplication.DraftStreamChunk{}, conflict(ErrorCodeDraftStreamConflict, errors.New("draft session byte limit reached"))
	}
	sequence := session.NextSeq
	chunk := draftChunkModel{SessionID: string(session.ID), Sequence: sequence, Content: command.Content, CreatedAt: now}
	if err := tx.Create(&chunk).Error; err != nil {
		return agentapplication.DraftStreamChunk{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	tag := tx.WithContext(ctx).Exec(`UPDATE agent.answer_draft_session
		SET next_sequence=next_sequence+1,total_bytes=total_bytes+?,updated_at=?
		WHERE id=? AND status='ACTIVE' AND next_sequence=? AND total_bytes+? <= ?`, bytes, now, string(session.ID), sequence, bytes, agentapplication.MaxDraftStreamSessionBytes)
	if err := tag.Error; err != nil {
		return agentapplication.DraftStreamChunk{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if tag.RowsAffected != 1 {
		return agentapplication.DraftStreamChunk{}, conflict(ErrorCodeDraftStreamConflict, errors.New("draft session append compare-and-swap failed"))
	}
	return agentapplication.DraftStreamChunk{SessionID: session.ID, Generation: session.Generation, Sequence: sequence, Content: command.Content, CreatedAt: now}, nil
}

func (repository *GORMDraftStreamRepository) readDraftStreamScoped(ctx context.Context, tx *gorm.DB, query agentapplication.DraftStreamReadQuery) (agentapplication.DraftStreamReadResult, error) {
	if repository == nil || isNilInterface(repository.db) {
		return agentapplication.DraftStreamReadResult{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if ctx == nil || !validDraftID(query.WorkspaceID) || !validDraftID(query.AnswerID) || query.Limit < 1 || query.Limit > agentapplication.MaxDraftStreamReplayChunks ||
		(query.After != nil && (query.After.Generation < 1 || query.After.Sequence < 1)) {
		return agentapplication.DraftStreamReadResult{}, invalid(ErrorCodeDraftStreamInvalid, errors.New("draft stream read query is invalid"))
	}
	// Linearize replay against finalizer publication and generation supersession.
	// Without this row lock, later lease/chunk queries could return stale ACTIVE data.
	row := gormScanRow(tx.WithContext(ctx).Raw(`SELECT `+draftSessionColumns+`
		FROM agent.answer_draft_session AS session
		WHERE session.workspace_id=? AND session.answer_id=?
		ORDER BY session.generation DESC LIMIT 1 FOR SHARE`, string(query.WorkspaceID), string(query.AnswerID)))
	session, err := scanDraftSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return agentapplication.DraftStreamReadResult{}, nil
	}
	if err != nil {
		return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if session.Status == agentapplication.DraftStreamPublished || session.Status == agentapplication.DraftStreamAborted || session.Status == agentapplication.DraftStreamSuperseded {
		return agentapplication.DraftStreamReadResult{Session: &session}, nil
	}
	var leaseValid bool
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT EXISTS(
		SELECT 1 FROM workflow.node_attempt AS attempt
		JOIN workflow.node_run AS node ON node.id=attempt.node_run_id
		WHERE attempt.id=? AND attempt.attempt_no=? AND attempt.status='running' AND attempt.lease_owner=? AND attempt.lease_until>clock_timestamp()
		  AND node.id=? AND node.attempt=? AND node.status='running' AND node.lease_owner=? AND node.lease_until>clock_timestamp()
		  AND ?>clock_timestamp()
	)`, string(session.Binding.NodeAttemptID), session.Binding.AttemptNo, session.Binding.LeaseOwner, string(session.Binding.NodeRunID), session.Binding.AttemptNo, session.Binding.LeaseOwner, session.ExpiresAt)).Scan(&leaseValid); err != nil {
		return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if !leaseValid {
		return agentapplication.DraftStreamReadResult{Invalidated: true}, nil
	}
	afterSequence := int64(0)
	if query.After != nil && query.After.Generation == session.Generation {
		afterSequence = query.After.Sequence
	}
	rows, err := tx.WithContext(ctx).Raw(`SELECT sequence,content,created_at FROM agent.answer_draft_chunk
		WHERE session_id=? AND sequence>? ORDER BY sequence LIMIT ?`, string(session.ID), afterSequence, query.Limit).Rows()
	if err != nil {
		return agentapplication.DraftStreamReadResult{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	chunks := make([]agentapplication.DraftStreamChunk, 0)
	defer rows.Close()
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
	return agentapplication.DraftStreamReadResult{Session: &session, Chunks: chunks}, nil
}

func (repository *GORMDraftStreamRepository) transitionDraftStreamScoped(ctx context.Context, tx *gorm.DB, command agentapplication.DraftStreamTransitionCommand, target agentapplication.DraftStreamStatus) (agentapplication.DraftStreamSession, error) {
	if repository == nil || isNilInterface(repository.db) {
		return agentapplication.DraftStreamSession{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if err := validateDraftTransition(ctx, command); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	leaseUntil, err := gormLockDraftRuntimeClaim(ctx, tx, command.Binding)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	session, err := gormLoadDraftSession(ctx, tx, command.SessionID, true)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	now, err := gormValidateDraftClaimLease(ctx, tx, leaseUntil)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	if !sameDraftBinding(session.Binding, command.Binding) || !session.ExpiresAt.After(now) {
		return agentapplication.DraftStreamSession{}, conflict(ErrorCodeDraftStreamConflict, errors.New("draft stream binding differs or session has expired"))
	}
	if err := gormVerifyDraftPendingAnswer(ctx, tx, command.Binding, false); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	if transitionSatisfied(session.Status, target) {
		return session, nil
	}
	if !allowedDraftTransition(session.Status, target) {
		return agentapplication.DraftStreamSession{}, conflict(ErrorCodeDraftStreamConflict, errors.New("draft stream transition is invalid"))
	}
	now, err = gormValidateDraftClaimLease(ctx, tx, leaseUntil)
	if err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	completedAt := any(nil)
	if target == agentapplication.DraftStreamCompleted || target == agentapplication.DraftStreamAborted {
		completedAt = now
	}
	session, err = scanDraftSession(gormScanRow(tx.WithContext(ctx).Raw(`UPDATE agent.answer_draft_session
		SET status=?,completed_at=?,updated_at=? WHERE id=? RETURNING `+draftSessionColumns, string(target), completedAt, now, string(session.ID))))
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return session, nil
}

func (repository *GORMDraftStreamRepository) cleanupExpiredDraftStreamsScoped(ctx context.Context, tx *gorm.DB, limit int) (int64, error) {
	if repository == nil || isNilInterface(repository.db) {
		return 0, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if ctx == nil || limit < 1 || limit > 1000 {
		return 0, invalid(ErrorCodeDraftStreamInvalid, errors.New("draft cleanup limit is invalid"))
	}
	tag := tx.WithContext(ctx).Exec(`WITH candidates AS (
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
		LIMIT ?
	)
	DELETE FROM agent.answer_draft_session AS session USING candidates
	WHERE session.id=candidates.id`, limit)
	if err := tag.Error; err != nil {
		return 0, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return tag.RowsAffected, nil
}

func (repository *GORMDraftStreamRepository) CompleteDraftStream(ctx context.Context, command agentapplication.DraftStreamTransitionCommand) (agentapplication.DraftStreamSession, error) {
	return repository.transitionDraftStream(ctx, command, agentapplication.DraftStreamCompleted)
}

func (repository *GORMDraftStreamRepository) DegradeDraftStream(ctx context.Context, command agentapplication.DraftStreamTransitionCommand) (agentapplication.DraftStreamSession, error) {
	return repository.transitionDraftStream(ctx, command, agentapplication.DraftStreamDegraded)
}

func (repository *GORMDraftStreamRepository) AbortDraftStream(ctx context.Context, command agentapplication.DraftStreamTransitionCommand) (agentapplication.DraftStreamSession, error) {
	return repository.transitionDraftStream(ctx, command, agentapplication.DraftStreamAborted)
}

func (repository *GORMDraftStreamRepository) LoadWorkspaceAnalysisCandidateDraftSession(
	ctx context.Context,
	query agentapplication.WorkspaceAnalysisCandidateDraftQuery,
) (agentapplication.DraftStreamSession, error) {
	if repository == nil || isNilInterface(repository.db) {
		return agentapplication.DraftStreamSession{}, dependency(ErrorCodeDraftStreamUnavailable, errors.New("draft stream repository is unavailable"))
	}
	if ctx == nil {
		return agentapplication.DraftStreamSession{}, invalid(ErrorCodeDraftStreamInvalid, errors.New("workspace analysis draft context is nil"))
	}
	if err := query.Validate(); err != nil {
		return agentapplication.DraftStreamSession{}, err
	}
	rows, err := repository.db.WithContext(ctx).Raw(`SELECT `+draftSessionColumns+`
		FROM agent.answer_draft_session
		WHERE workspace_id=? AND answer_id=? AND node_attempt_id=?
		  AND status IN ('COMPLETED','DEGRADED')
		ORDER BY generation DESC LIMIT 2`, string(query.WorkspaceID), string(query.AnswerID), string(query.NodeAttemptID)).Rows()
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	defer rows.Close()
	sessions := make([]agentapplication.DraftStreamSession, 0, 2)
	for rows.Next() {
		session, scanErr := scanDraftSession(rows)
		if scanErr != nil {
			return agentapplication.DraftStreamSession{}, classify(scanErr, ErrorCodeDraftStreamUnavailable)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	if len(sessions) == 0 {
		return agentapplication.DraftStreamSession{}, notFound(ErrorCodeDraftStreamConflict, sql.ErrNoRows)
	}
	if len(sessions) != 1 {
		return agentapplication.DraftStreamSession{}, conflict(ErrorCodeDraftStreamConflict, errors.New("workspace analysis candidate has multiple terminal draft sessions"))
	}
	return sessions[0], nil
}

func gormVerifyDraftPendingAnswer(ctx context.Context, tx *gorm.DB, binding agentapplication.DraftStreamBinding, lock bool) error {
	locking := ""
	if lock {
		locking = " FOR UPDATE"
	}
	var answerID string
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT answer.id::text
		FROM agent.answer AS answer
		WHERE answer.workspace_id=? AND answer.id=? AND answer.workflow_run_id=?
		  AND answer.publication_status='pending'
	`+locking, string(binding.WorkspaceID), string(binding.AnswerID), string(binding.WorkflowRunID))).Scan(&answerID)
	if errors.Is(err, sql.ErrNoRows) {
		return conflict(ErrorCodeDraftStreamConflict, errors.New("answer is no longer pending"))
	}
	if err != nil {
		return classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return nil
}

func gormLockDraftRuntimeClaim(ctx context.Context, tx *gorm.DB, binding agentapplication.DraftStreamBinding) (time.Time, error) {
	var nodeLeaseUntil time.Time
	err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT lease_until
		FROM workflow.node_run
		WHERE id=? AND run_id=? AND attempt=? AND status='running' AND lease_owner=?
		FOR SHARE`, string(binding.NodeRunID), string(binding.WorkflowRunID), binding.AttemptNo, binding.LeaseOwner)).Scan(&nodeLeaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, conflict(ErrorCodeDraftStreamConflict, errors.New("runtime claim is no longer active"))
	}
	if err != nil {
		return time.Time{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	var attemptLeaseUntil time.Time
	err = gormScanRow(tx.WithContext(ctx).Raw(`SELECT lease_until
		FROM workflow.node_attempt
		WHERE id=? AND node_run_id=? AND attempt_no=? AND status='running' AND lease_owner=?
		FOR SHARE`, string(binding.NodeAttemptID), string(binding.NodeRunID), binding.AttemptNo, binding.LeaseOwner)).Scan(&attemptLeaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
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

func gormValidateDraftClaimLease(ctx context.Context, tx *gorm.DB, leaseUntil time.Time) (time.Time, error) {
	now, err := gormDraftDatabaseNow(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	if !leaseUntil.After(now) {
		return time.Time{}, conflict(ErrorCodeDraftStreamConflict, errors.New("runtime claim lease has expired"))
	}
	return now, nil
}

func gormLoadDraftSession(ctx context.Context, tx *gorm.DB, sessionID foundation.ID, forUpdate bool) (agentapplication.DraftStreamSession, error) {
	query := `SELECT ` + draftSessionColumns + ` FROM agent.answer_draft_session WHERE id=?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	session, err := scanDraftSession(gormScanRow(tx.WithContext(ctx).Raw(query, string(sessionID))))
	if errors.Is(err, sql.ErrNoRows) {
		return agentapplication.DraftStreamSession{}, notFound(ErrorCodeDraftStreamConflict, err)
	}
	if err != nil {
		return agentapplication.DraftStreamSession{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return session, nil
}

func gormFindDraftByBinding(ctx context.Context, tx *gorm.DB, binding agentapplication.DraftStreamBinding) (agentapplication.DraftStreamSession, bool, error) {
	session, err := scanDraftSession(gormScanRow(tx.WithContext(ctx).Raw(`SELECT `+draftSessionColumns+`
			FROM agent.answer_draft_session
			WHERE workspace_id=? AND answer_id=? AND workflow_run_id=? AND node_run_id=? AND node_attempt_id=?
			  AND attempt_no=? AND lease_owner=?
			ORDER BY generation DESC LIMIT 1
			FOR UPDATE`, string(binding.WorkspaceID), string(binding.AnswerID), string(binding.WorkflowRunID), string(binding.NodeRunID), string(binding.NodeAttemptID), binding.AttemptNo, binding.LeaseOwner)))
	if errors.Is(err, sql.ErrNoRows) {
		return agentapplication.DraftStreamSession{}, false, nil
	}
	if err != nil {
		return agentapplication.DraftStreamSession{}, false, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return session, true, nil
}

func gormDraftDatabaseNow(ctx context.Context, tx *gorm.DB) (time.Time, error) {
	var now time.Time
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT clock_timestamp()`)).Scan(&now); err != nil {
		return time.Time{}, classify(err, ErrorCodeDraftStreamUnavailable)
	}
	return now.UTC(), nil
}

var _ agentapplication.DraftStreamStore = (*GORMDraftStreamRepository)(nil)
var _ agentapplication.WorkspaceAnalysisCandidateDraftSessionLoader = (*GORMDraftStreamRepository)(nil)
