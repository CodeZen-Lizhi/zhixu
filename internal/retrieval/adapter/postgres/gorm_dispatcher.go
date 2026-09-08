package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	reindexriver "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"gorm.io/gorm"
)

// GORMDispatcherStore 在同一个 UoW 中领取 Outbox、更新 Delivery 并插入 River Job。
type GORMDispatcherStore struct {
	database     *gorm.DB
	unitOfWork   foundation.UnitOfWork
	ids          foundation.IDGenerator
	outbox       application.ScopedReindexOutbox
	binding      application.ScopedReindexBindingVerifier
	consumerName string
}

var _ application.ScopedDispatcherStore = (*GORMDispatcherStore)(nil)

// NewGORMDispatcher 从同一 Pool 构造事务型 producer；owner 协作者必须来自同一 Pool。
// Worker/listener 仍由 Composition 使用官方 pgx driver 构造。
func NewGORMDispatcher(
	pool *platformpostgres.Pool,
	ids foundation.IDGenerator,
	options workflowriver.Options,
	fence workflowriver.ScopedEnqueueFence,
	outbox application.ScopedReindexOutbox,
	binding application.ScopedReindexBindingVerifier,
) (*application.ScopedDispatcher, error) {
	store, err := newGORMDispatcherStore(pool, ids, outbox, binding)
	if err != nil {
		return nil, err
	}
	if nilGORMRetrievalDependency(fence) {
		return nil, dependency("WORKFLOW_SCOPED_ENQUEUE_FENCE_MISSING", errors.New("scoped enqueue fence is unavailable"))
	}
	client, err := workflowriver.NewClientWithOptions(pool.DB(), nil, options)
	if err != nil {
		return nil, err
	}
	jobs, err := reindexriver.NewScopedApplicationInserter(pool, client, fence)
	if err != nil {
		return nil, err
	}
	return application.NewScopedDispatcher(store, jobs)
}

func newGORMDispatcherStore(pool *platformpostgres.Pool, ids foundation.IDGenerator, outbox application.ScopedReindexOutbox, binding application.ScopedReindexBindingVerifier) (*GORMDispatcherStore, error) {
	database, unitOfWork, err := gormRetrievalDependencies(pool)
	if err != nil {
		return nil, err
	}
	if nilGORMRetrievalDependency(ids) || nilGORMRetrievalDependency(outbox) || nilGORMRetrievalDependency(binding) {
		return nil, dependency("REINDEX_DISPATCHER_DEPENDENCY_UNAVAILABLE", errors.New("dispatcher owner collaborators or id generator are unavailable"))
	}
	return &GORMDispatcherStore{
		database: database, unitOfWork: unitOfWork, ids: ids, outbox: outbox, binding: binding, consumerName: ReindexConsumerName,
	}, nil
}

// DispatchOneScoped 拥有一条 first/retry 派发的事务；只有提交成功才返回 dispatched。
func (store *GORMDispatcherStore) DispatchOneScoped(ctx context.Context, path application.DispatchPath, jobs application.ScopedJobInserter) (bool, error) {
	if store == nil || !validGORMRetrievalDatabase(store.database) || nilGORMRetrievalDependency(store.unitOfWork) ||
		nilGORMRetrievalDependency(store.ids) || nilGORMRetrievalDependency(store.outbox) ||
		nilGORMRetrievalDependency(store.binding) || nilGORMRetrievalDependency(jobs) {
		return false, dependency("REINDEX_DISPATCHER_DEPENDENCY_UNAVAILABLE", errors.New("scoped dispatcher store is unavailable"))
	}
	if path != application.DispatchPathFirst && path != application.DispatchPathRetry {
		return false, foundation.NewError(foundation.ErrorInvalidInput, "REINDEX_DISPATCH_PATH_INVALID", false, errors.New("unsupported dispatch path"))
	}
	var dispatched bool
	err := withinGORMRetrieval(ctx, store.unitOfWork, foundation.TransactionOptions{},
		"REINDEX_DISPATCH_TRANSACTION_FAILED", "REINDEX_DISPATCH_COMMIT_FAILED",
		func(callbackCtx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
			var err error
			if path == application.DispatchPathFirst {
				dispatched, err = store.dispatchFirstScoped(callbackCtx, tx, scope, jobs)
			} else {
				dispatched, err = store.dispatchRetryScoped(callbackCtx, tx, scope, jobs)
			}
			return err
		})
	return err == nil && dispatched, err
}

func (store *GORMDispatcherStore) dispatchFirstScoped(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope, jobs application.ScopedJobInserter) (bool, error) {
	fact, found, err := store.outbox.ClaimReindexOutboxScoped(ctx, scope, application.ReindexOutboxClaimRequest{ConsumerName: store.consumerName})
	if err != nil || !found {
		return false, err
	}
	event := selectedOutbox{ID: fact.EventID, WorkspaceID: fact.WorkspaceID, RunID: fact.WorkflowRunID, Payload: fact.Payload}
	if _, err := gormRetrievalExec(ctx, tx, `SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, string(event.WorkspaceID)); err != nil {
		return false, classifyGORMRetrieval(ctx, err, "REINDEX_DISPATCH_WORKSPACE_LOCK_FAILED")
	}
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT EXISTS(
		SELECT 1 FROM retrieval.reindex_delivery blocker
		WHERE blocker.workspace_id=? AND blocker.status IN ('pending','dispatched','processing','retry_wait','manual_recovery')
		AND NOT (blocker.consumer_name=? AND blocker.outbox_event_id=?)
	)`, string(event.WorkspaceID), store.consumerName, string(event.ID))
	if err != nil {
		return false, classifyGORMRetrieval(ctx, err, "REINDEX_DISPATCH_BLOCKER_QUERY_FAILED")
	}
	var blocked bool
	if err := row.Scan(&blocked); err != nil {
		return false, classifyGORMRetrieval(ctx, err, "REINDEX_DISPATCH_BLOCKER_QUERY_FAILED")
	}
	if blocked {
		return false, nil
	}
	request, err := reindexcontract.DecodeStrict([]byte(event.Payload))
	if err != nil {
		return false, poisonedOutbox(err)
	}
	facts, err := store.binding.VerifyReindexBindingScoped(ctx, scope, request)
	if err != nil {
		return false, err
	}
	if err := validateOutboxBinding(event, request, writebackBinding{
		Binding: facts.Execution, Commit: facts.Commit,
		ExecutionStatus: string(facts.ExecutionStatus), ProposalStatus: string(facts.ProposalStatus),
	}); err != nil {
		return false, poisonedOutbox(err)
	}
	delivery, err := store.pendingDelivery(ctx, tx, event, request.WritebackExecutionID)
	if err != nil {
		return false, err
	}
	receipt, err := jobs.InsertScoped(ctx, scope, application.ReindexJob{DeliveryID: delivery.ID, DispatchNo: 1})
	if err != nil {
		return false, err
	}
	if receipt.Duplicate || receipt.JobID < 1 {
		return false, consistency("REINDEX_DISPATCH_JOB_RECEIPT_CONFLICT", errors.New("first dispatch returned a duplicate or invalid job receipt"))
	}
	row, err = gormRetrievalRawRow(ctx, tx, `UPDATE retrieval.reindex_delivery
		SET status='dispatched',dispatch_no=1,version=version+1,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND status='pending' AND dispatch_no=0 AND version=?
		RETURNING `+deliveryColumns, string(delivery.ID), delivery.Version)
	if err == nil {
		_, err = scanDelivery(row)
	}
	if err != nil {
		return false, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_DISPATCH_FAILED")
	}
	if err := store.outbox.PublishReindexOutboxScoped(ctx, scope, event.ID); err != nil {
		return false, err
	}
	return true, nil
}

func (store *GORMDispatcherStore) dispatchRetryScoped(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope, jobs application.ScopedJobInserter) (bool, error) {
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT `+deliveryColumns+`
		FROM retrieval.reindex_delivery
		WHERE consumer_name=? AND status='retry_wait' AND next_attempt_at<=CURRENT_TIMESTAMP
		ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, store.consumerName)
	if err != nil {
		return false, classifyGORMRetrieval(ctx, err, "REINDEX_RETRY_SELECT_FAILED")
	}
	delivery, err := scanDelivery(row)
	if gormRetrievalNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, classifyGORMRetrieval(ctx, err, "REINDEX_RETRY_SELECT_FAILED")
	}
	row, err = gormRetrievalRawRow(ctx, tx, `SELECT pg_try_advisory_xact_lock(hashtextextended(?,0))`, string(delivery.WorkspaceID))
	if err != nil {
		return false, classifyGORMRetrieval(ctx, err, "REINDEX_DISPATCH_WORKSPACE_LOCK_FAILED")
	}
	var locked bool
	if err := row.Scan(&locked); err != nil {
		return false, classifyGORMRetrieval(ctx, err, "REINDEX_DISPATCH_WORKSPACE_LOCK_FAILED")
	}
	if !locked {
		return false, nil
	}
	nextDispatch := delivery.DispatchNo + 1
	row, err = gormRetrievalRawRow(ctx, tx, `UPDATE retrieval.reindex_delivery SET
		status='dispatched',dispatch_no=?,next_attempt_at=NULL,failure_class=NULL,error_kind=NULL,error_code=NULL,error_summary=NULL,
		version=version+1,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND status='retry_wait' AND dispatch_no=? AND version=? AND next_attempt_at<=CURRENT_TIMESTAMP
		RETURNING `+deliveryColumns, nextDispatch, string(delivery.ID), delivery.DispatchNo, delivery.Version)
	if err != nil {
		return false, classifyGORMRetrieval(ctx, err, "REINDEX_RETRY_PREPARE_FAILED")
	}
	updated, err := scanDelivery(row)
	if err != nil {
		return false, classifyGORMRetrieval(ctx, err, "REINDEX_RETRY_PREPARE_FAILED")
	}
	receipt, err := jobs.InsertScoped(ctx, scope, application.ReindexJob{DeliveryID: updated.ID, DispatchNo: nextDispatch})
	if err != nil {
		return false, err
	}
	if receipt.Duplicate || receipt.JobID < 1 {
		return false, consistency("REINDEX_DISPATCH_JOB_RECEIPT_CONFLICT", errors.New("retry dispatch returned a duplicate or invalid job receipt"))
	}
	return true, nil
}

func (store *GORMDispatcherStore) pendingDelivery(ctx context.Context, tx *gorm.DB, event selectedOutbox, executionID foundation.ID) (domain.Delivery, error) {
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT `+deliveryColumns+` FROM retrieval.reindex_delivery
		WHERE consumer_name=? AND outbox_event_id=? FOR UPDATE`, store.consumerName, string(event.ID))
	if err != nil {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_QUERY_FAILED")
	}
	delivery, err := scanDelivery(row)
	if err == nil {
		if err := validatePendingDeliveryReplay(delivery, event, executionID); err != nil {
			return domain.Delivery{}, err
		}
		return delivery, nil
	}
	if !gormRetrievalNoRows(err) {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_QUERY_FAILED")
	}
	id, err := store.ids.New()
	if err != nil {
		return domain.Delivery{}, err
	}
	row, err = gormRetrievalRawRow(ctx, tx, `INSERT INTO retrieval.reindex_delivery(
		id,consumer_name,outbox_event_id,workspace_id,writeback_execution_id,status,dispatch_no,attempt_no,version,
		manual_recovery_required,created_at,updated_at)
		VALUES(?,?,?,?,?,'pending',0,0,1,false,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
		RETURNING `+deliveryColumns, string(id), store.consumerName, string(event.ID), string(event.WorkspaceID), string(executionID))
	if err == nil {
		delivery, err = scanDelivery(row)
	}
	if err != nil {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_CREATE_FAILED")
	}
	return delivery, nil
}
