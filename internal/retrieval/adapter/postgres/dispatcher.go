package postgres

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reindexriver "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
)

const (
	// ReindexConsumerName 是 v1 Dispatcher 持久化 Delivery 使用的稳定消费者身份。
	ReindexConsumerName   = "retrieval-reindex-v1"
	outboxContractInvalid = "REINDEX_OUTBOX_CONTRACT_INVALID"
)

// DispatcherDB 是 Dispatcher 所需的最小 pgx 事务边界。
type DispatcherDB interface {
	Begin(context.Context) (pgx.Tx, error)
}

// ReindexRiverInserter 是现有 Reindex River Adapter 暴露的事务型 typed insertion seam。
type ReindexRiverInserter interface {
	InsertTx(context.Context, any, reindexriver.Args, time.Time) (workflowapplication.JobReceipt, error)
}

// DispatcherStore 在 PostgreSQL 中实现 first/retry 两条互斥领取路径。
type DispatcherStore struct {
	db           DispatcherDB
	ids          foundation.IDGenerator
	consumerName string
}

var _ application.DispatcherStore = (*DispatcherStore)(nil)

// NewDispatcher 创建使用默认 v1 Consumer identity 的事务型 Dispatcher。
func NewDispatcher(db DispatcherDB, ids foundation.IDGenerator, jobs ReindexRiverInserter) (*application.Dispatcher, error) {
	return NewDispatcherWithConsumer(db, ids, jobs, ReindexConsumerName)
}

// NewDispatcherWithConsumer 创建显式 Consumer identity 的事务型 Dispatcher。
func NewDispatcherWithConsumer(db DispatcherDB, ids foundation.IDGenerator, jobs ReindexRiverInserter, consumerName string) (*application.Dispatcher, error) {
	consumerName = strings.TrimSpace(consumerName)
	if db == nil || nilInterface(ids) || nilInterface(jobs) || consumerName == "" || utf8.RuneCountInString(consumerName) > 128 {
		return nil, dependency("REINDEX_DISPATCHER_DEPENDENCY_UNAVAILABLE", errors.New("dispatcher database, id generator, or consumer identity is invalid"))
	}
	store := &DispatcherStore{db: db, ids: ids, consumerName: consumerName}
	return application.NewDispatcher(store, riverJobInserter{inserter: jobs})
}

type riverJobInserter struct{ inserter ReindexRiverInserter }

func (i riverJobInserter) InsertTx(ctx context.Context, transaction any, job application.ReindexJob) (application.JobReceipt, error) {
	args, err := reindexriver.NewArgs(job.DeliveryID, job.DispatchNo)
	if err != nil {
		return application.JobReceipt{}, err
	}
	receipt, err := i.inserter.InsertTx(ctx, transaction, args, job.ScheduledAt)
	return application.JobReceipt{JobID: receipt.JobID, Duplicate: receipt.Duplicate}, err
}

// DispatchOne 在独立短事务中派发一条 first 或 retry 记录。
func (s *DispatcherStore) DispatchOne(ctx context.Context, path application.DispatchPath, jobs application.JobInserter) (bool, error) {
	if s == nil || s.db == nil || nilInterface(s.ids) || jobs == nil {
		return false, dependency("REINDEX_DISPATCHER_DEPENDENCY_UNAVAILABLE", errors.New("dispatcher store is not initialized"))
	}
	switch path {
	case application.DispatchPathFirst:
		return s.dispatchFirst(ctx, jobs)
	case application.DispatchPathRetry:
		return s.dispatchRetry(ctx, jobs)
	default:
		return false, foundation.NewError(foundation.ErrorInvalidInput, "REINDEX_DISPATCH_PATH_INVALID", false, errors.New("unsupported dispatch path"))
	}
}

func (s *DispatcherStore) dispatchFirst(ctx context.Context, jobs application.JobInserter) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, classify(err, "REINDEX_DISPATCH_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	event, err := selectFirstOutbox(ctx, tx, s.consumerName)
	if errors.Is(err, pgx.ErrNoRows) {
		return commitDispatch(ctx, tx, false)
	}
	if err != nil {
		return false, classify(err, "REINDEX_OUTBOX_SELECT_FAILED")
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(event.WorkspaceID)); err != nil {
		return false, classify(err, "REINDEX_DISPATCH_WORKSPACE_LOCK_FAILED")
	}
	blocked, err := workspaceBlocked(ctx, tx, event, s.consumerName)
	if err != nil {
		return false, err
	}
	if blocked {
		return commitDispatch(ctx, tx, false)
	}

	request, err := reindexcontract.DecodeStrict([]byte(event.Payload))
	if err != nil {
		return false, poisonedOutbox(err)
	}
	binding, err := loadWritebackBinding(ctx, tx, request.WritebackExecutionID)
	if err != nil {
		return false, err
	}
	if err := validateOutboxBinding(event, request, binding); err != nil {
		return false, poisonedOutbox(err)
	}

	delivery, err := getOrCreatePendingDelivery(ctx, tx, s.ids, s.consumerName, event, request.WritebackExecutionID)
	if err != nil {
		return false, err
	}
	receipt, err := jobs.InsertTx(ctx, tx, application.ReindexJob{DeliveryID: delivery.ID, DispatchNo: 1})
	if err != nil {
		return false, err
	}
	if receipt.Duplicate || receipt.JobID < 1 {
		return false, consistency("REINDEX_DISPATCH_JOB_RECEIPT_CONFLICT", errors.New("first dispatch returned a duplicate or invalid job receipt"))
	}
	if _, err = scanDelivery(tx.QueryRow(ctx, `UPDATE retrieval.reindex_delivery
		SET status='dispatched',dispatch_no=1,version=version+1,updated_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND status='pending' AND dispatch_no=0 AND version=$2
		RETURNING `+deliveryColumns, string(delivery.ID), delivery.Version)); err != nil {
		return false, classify(err, "REINDEX_DELIVERY_DISPATCH_FAILED")
	}
	var published string
	if err = tx.QueryRow(ctx, `UPDATE workflow.outbox_event SET published_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND published_at IS NULL RETURNING id::text`, string(event.ID)).Scan(&published); err != nil {
		return false, classify(err, "REINDEX_OUTBOX_PUBLISH_FAILED")
	}
	return commitDispatch(ctx, tx, true)
}

func (s *DispatcherStore) dispatchRetry(ctx context.Context, jobs application.JobInserter) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, classify(err, "REINDEX_DISPATCH_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	delivery, err := scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+`
		FROM retrieval.reindex_delivery
		WHERE consumer_name=$1 AND status='retry_wait' AND next_attempt_at<=CURRENT_TIMESTAMP
		ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, s.consumerName))
	if errors.Is(err, pgx.ErrNoRows) {
		return commitDispatch(ctx, tx, false)
	}
	if err != nil {
		return false, classify(err, "REINDEX_RETRY_SELECT_FAILED")
	}
	var workspaceLocked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, string(delivery.WorkspaceID)).Scan(&workspaceLocked); err != nil {
		return false, classify(err, "REINDEX_DISPATCH_WORKSPACE_LOCK_FAILED")
	}
	if !workspaceLocked {
		return commitDispatch(ctx, tx, false)
	}
	nextDispatch := delivery.DispatchNo + 1
	updated, err := scanDelivery(tx.QueryRow(ctx, `UPDATE retrieval.reindex_delivery SET
		status='dispatched',dispatch_no=$2,next_attempt_at=NULL,failure_class=NULL,error_kind=NULL,error_code=NULL,error_summary=NULL,
		version=version+1,updated_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND status='retry_wait' AND dispatch_no=$3 AND version=$4 AND next_attempt_at<=CURRENT_TIMESTAMP
		RETURNING `+deliveryColumns, string(delivery.ID), nextDispatch, delivery.DispatchNo, delivery.Version))
	if err != nil {
		return false, classify(err, "REINDEX_RETRY_PREPARE_FAILED")
	}
	receipt, err := jobs.InsertTx(ctx, tx, application.ReindexJob{DeliveryID: updated.ID, DispatchNo: nextDispatch})
	if err != nil {
		return false, err
	}
	if receipt.Duplicate || receipt.JobID < 1 {
		return false, consistency("REINDEX_DISPATCH_JOB_RECEIPT_CONFLICT", errors.New("retry dispatch returned a duplicate or invalid job receipt"))
	}
	return commitDispatch(ctx, tx, true)
}

type selectedOutbox struct {
	ID          foundation.ID
	WorkspaceID foundation.ID
	RunID       *foundation.ID
	Payload     string
}

func selectFirstOutbox(ctx context.Context, tx pgx.Tx, consumerName string) (selectedOutbox, error) {
	var event selectedOutbox
	var runID *string
	err := tx.QueryRow(ctx, `SELECT event.id::text,event.workspace_id::text,event.run_id::text,event.payload::text
		FROM workflow.outbox_event event
		WHERE event.event_type=$1 AND event.published_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM retrieval.reindex_delivery blocker
			 WHERE blocker.workspace_id=event.workspace_id
			   AND blocker.status IN ('pending','dispatched','processing','retry_wait','manual_recovery')
			   AND NOT (blocker.consumer_name=$2 AND blocker.outbox_event_id=event.id)
		  )
		ORDER BY event.occurred_at,event.id
		FOR UPDATE OF event SKIP LOCKED LIMIT 1`, reindexcontract.EventTypeReindexRequested, consumerName).
		Scan(&event.ID, &event.WorkspaceID, &runID, &event.Payload)
	if runID != nil {
		value := foundation.ID(*runID)
		event.RunID = &value
	}
	return event, err
}

func workspaceBlocked(ctx context.Context, tx pgx.Tx, event selectedOutbox, consumerName string) (bool, error) {
	var blocked bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM retrieval.reindex_delivery blocker
		 WHERE blocker.workspace_id=$1
		   AND blocker.status IN ('pending','dispatched','processing','retry_wait','manual_recovery')
		   AND NOT (blocker.consumer_name=$2 AND blocker.outbox_event_id=$3)
	)`, string(event.WorkspaceID), consumerName, string(event.ID)).Scan(&blocked)
	if err != nil {
		return false, classify(err, "REINDEX_DISPATCH_BLOCKER_QUERY_FAILED")
	}
	return blocked, nil
}

type writebackBinding struct {
	Binding         reindexcontract.Binding
	ExecutionStatus string
	ProposalStatus  string
	Commit          reindexcontract.Binding
}

func loadWritebackBinding(ctx context.Context, tx pgx.Tx, executionID foundation.ID) (writebackBinding, error) {
	var value writebackBinding
	err := tx.QueryRow(ctx, `SELECT
		execution.workspace_id::text,execution.workflow_run_id::text,execution.node_run_id::text,
		execution.proposal_id::text,execution.revision_id::text,execution.approval_id::text,
		execution.id::text,execution.target_path,execution.result_hash,execution.git_commit,
		execution.status,proposal.status,
		commit_mapping.workspace_id::text,execution.workflow_run_id::text,execution.node_run_id::text,
		commit_mapping.proposal_id::text,commit_mapping.revision_id::text,commit_mapping.approval_id::text,
		commit_mapping.writeback_execution_id::text,commit_mapping.target_path,commit_mapping.result_hash,commit_mapping.git_commit
		FROM change_control.writeback_execution execution
		JOIN change_control.proposal proposal ON proposal.id=execution.proposal_id AND proposal.workspace_id=execution.workspace_id
		JOIN change_control.proposal_commit commit_mapping ON commit_mapping.writeback_execution_id=execution.id
		WHERE execution.id=$1`, string(executionID)).Scan(
		&value.Binding.WorkspaceID, &value.Binding.WorkflowRunID, &value.Binding.NodeRunID,
		&value.Binding.ProposalID, &value.Binding.RevisionID, &value.Binding.ApprovalID,
		&value.Binding.WritebackExecutionID, &value.Binding.TargetPath, &value.Binding.ResultHash, &value.Binding.GitCommit,
		&value.ExecutionStatus, &value.ProposalStatus,
		&value.Commit.WorkspaceID, &value.Commit.WorkflowRunID, &value.Commit.NodeRunID,
		&value.Commit.ProposalID, &value.Commit.RevisionID, &value.Commit.ApprovalID,
		&value.Commit.WritebackExecutionID, &value.Commit.TargetPath, &value.Commit.ResultHash, &value.Commit.GitCommit,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return writebackBinding{}, poisonedOutbox(errors.New("writeback execution or commit mapping is missing"))
	}
	if err != nil {
		return writebackBinding{}, classify(err, "REINDEX_WRITEBACK_BINDING_QUERY_FAILED")
	}
	return value, nil
}

func validateOutboxBinding(event selectedOutbox, request reindexcontract.RequestV1, value writebackBinding) error {
	if event.RunID == nil || event.ID == "" || event.WorkspaceID != request.WorkspaceID || *event.RunID != request.WorkflowRunID ||
		value.ExecutionStatus != "verifying" || value.ProposalStatus != "verifying" {
		return errors.New("outbox identity or writeback lifecycle is not dispatchable")
	}
	if err := reindexcontract.ValidateBinding(request, value.Binding); err != nil {
		return err
	}
	if err := reindexcontract.ValidateBinding(request, value.Commit); err != nil {
		return err
	}
	return nil
}

func getOrCreatePendingDelivery(ctx context.Context, tx pgx.Tx, ids foundation.IDGenerator, consumerName string, event selectedOutbox, executionID foundation.ID) (domain.Delivery, error) {
	delivery, err := scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM retrieval.reindex_delivery
		WHERE consumer_name=$1 AND outbox_event_id=$2 FOR UPDATE`, consumerName, string(event.ID)))
	if err == nil {
		if domain.ValidateDelivery(delivery) != nil || delivery.WorkspaceID != event.WorkspaceID || delivery.WritebackExecutionID != executionID ||
			delivery.Status != domain.DeliveryStatusPending || delivery.DispatchNo != 0 || delivery.AttemptNo != 0 ||
			delivery.CurrentAttemptID != nil || delivery.Version != 1 || delivery.NextAttemptAt != nil || delivery.Failure != nil ||
			delivery.SourceVersionID != nil || delivery.ParseProjectionID != nil || delivery.IndexVersionID != nil || delivery.ActivationID != nil ||
			delivery.ExcludedSourceCount != nil || delivery.Regression != nil || delivery.ManualRecoveryRequired || delivery.CompletedAt != nil {
			return domain.Delivery{}, consistency("REINDEX_DELIVERY_REPLAY_CONFLICT", errors.New("existing delivery is not the exact pending binding"))
		}
		return delivery, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Delivery{}, classify(err, "REINDEX_DELIVERY_QUERY_FAILED")
	}
	id, err := ids.New()
	if err != nil {
		return domain.Delivery{}, err
	}
	delivery, err = scanDelivery(tx.QueryRow(ctx, `INSERT INTO retrieval.reindex_delivery(
		id,consumer_name,outbox_event_id,workspace_id,writeback_execution_id,status,dispatch_no,attempt_no,version,
		manual_recovery_required,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'pending',0,0,1,false,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
		RETURNING `+deliveryColumns, string(id), consumerName, string(event.ID), string(event.WorkspaceID), string(executionID)))
	if err != nil {
		return domain.Delivery{}, classify(err, "REINDEX_DELIVERY_CREATE_FAILED")
	}
	return delivery, nil
}

func commitDispatch(ctx context.Context, tx pgx.Tx, dispatched bool) (bool, error) {
	if err := tx.Commit(ctx); err != nil {
		return false, classify(err, "REINDEX_DISPATCH_COMMIT_FAILED")
	}
	return dispatched, nil
}

func poisonedOutbox(cause error) error {
	return foundation.NewError(foundation.ErrorNonRetryableFailure, outboxContractInvalid, false, cause)
}
