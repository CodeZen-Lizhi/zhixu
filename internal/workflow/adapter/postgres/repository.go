// Package workflowpostgres persists durable workflow state in PostgreSQL.
package workflowpostgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the pgx-compatible transaction boundary required by Repository.
type DB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// Repository is the PostgreSQL workflow repository.
type Repository struct{ db DB }

// NewRepository constructs a Repository over a pgx pool or transaction.
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_DATABASE_UNAVAILABLE", true, errors.New("database is nil"))
	}
	return &Repository{db: db}, nil
}

// Start atomically persists a definition, run, first node, and outbox event.
func (r *Repository) Start(ctx context.Context, request domain.StartRequest) (domain.Run, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Run{}, classify(err, "WORKFLOW_START_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var definitionID string
	var graph []byte
	err = tx.QueryRow(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,key,version) DO NOTHING RETURNING id::text,graph`, string(request.Definition.ID), string(request.Definition.WorkspaceID), request.Definition.Key, request.Definition.Version, request.Definition.Graph, request.Definition.CreatedAt.UTC()).Scan(&definitionID, &graph)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id::text,graph FROM workflow.definition WHERE workspace_id=$1 AND key=$2 AND version=$3`, string(request.Definition.WorkspaceID), request.Definition.Key, request.Definition.Version).Scan(&definitionID, &graph)
	}
	if err != nil {
		return domain.Run{}, classify(err, "WORKFLOW_DEFINITION_CREATE_FAILED")
	}
	if !jsonEqual(graph, request.Definition.Graph) {
		return domain.Run{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_DEFINITION_VERSION_CONFLICT", false, errors.New("definition version has different graph"))
	}
	request.Run.DefinitionID = foundation.ID(definitionID)
	run, err := insertRun(ctx, tx, request.Run)
	if err != nil {
		return domain.Run{}, classify(err, "WORKFLOW_RUN_CREATE_FAILED")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, string(request.FirstNode.ID), string(run.ID), request.FirstNode.NodeKey, request.FirstNode.NodeType, string(request.FirstNode.Status), request.FirstNode.Attempt, request.FirstNode.Input, request.FirstNode.Version, request.FirstNode.CreatedAt.UTC(), request.FirstNode.UpdatedAt.UTC()); err != nil {
		return domain.Run{}, classify(err, "WORKFLOW_NODE_CREATE_FAILED")
	}
	if err = validateEventScope(ctx, tx, request.Event, run.ID); err != nil {
		return domain.Run{}, err
	}
	if err = insertEvent(ctx, tx, request.Event); err != nil {
		return domain.Run{}, classify(err, "WORKFLOW_OUTBOX_CREATE_FAILED")
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Run{}, classify(err, "WORKFLOW_START_COMMIT_FAILED")
	}
	return run, nil
}

// GetRun returns one durable workflow run.
func (r *Repository) GetRun(ctx context.Context, id foundation.ID) (domain.Run, error) {
	run, err := scanRun(r.db.QueryRow(ctx, runSelect+` WHERE id=$1`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Run{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_RUN_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Run{}, classify(err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	return run, nil
}

// ClaimNode leases a pending node or reclaims a node after its lease expired.
func (r *Repository) ClaimNode(ctx context.Context, id foundation.ID, owner string, now, until time.Time) (domain.NodeRun, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_CLAIM_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	node, err := scanNode(tx.QueryRow(ctx, nodeSelect+` WHERE id=$1 AND (status='pending' OR (status='running' AND lease_until <= $2)) FOR UPDATE`, string(id), now.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_NODE_NOT_CLAIMABLE", false, err)
	}
	if err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_NODE_CLAIM_FAILED")
	}
	if err = domain.ValidateTransition(node.Status, domain.StatusRunning); err != nil && node.Status != domain.StatusRunning {
		return domain.NodeRun{}, err
	}
	node, err = scanNode(tx.QueryRow(ctx, nodeUpdateReturning+` WHERE id=$1 RETURNING `+nodeColumns, string(id), string(domain.StatusRunning), node.Attempt+1, owner, until.UTC(), now.UTC(), node.Version+1))
	if err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_NODE_CLAIM_FAILED")
	}
	runTag, err := tx.Exec(ctx, `UPDATE workflow.run SET status='running',version=version+1,updated_at=$2 WHERE id=$1 AND status IN ('pending','running')`, string(node.RunID), now.UTC())
	if err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_RUN_START_FAILED")
	}
	if runTag.RowsAffected() != 1 {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_RUN_NOT_RUNNABLE", false, errors.New("workflow run is not pending or running"))
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_CLAIM_COMMIT_FAILED")
	}
	return node, nil
}

// HeartbeatNode extends an owned, unexpired lease.
func (r *Repository) HeartbeatNode(ctx context.Context, id foundation.ID, owner string, now, until time.Time) (domain.NodeRun, error) {
	node, err := scanNode(r.db.QueryRow(ctx, `UPDATE workflow.node_run SET lease_until=$4,updated_at=$3,version=version+1 WHERE id=$1 AND status='running' AND lease_owner=$2 AND lease_until>$3 RETURNING `+nodeColumns, string(id), owner, now.UTC(), until.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, err)
	}
	if err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_HEARTBEAT_FAILED")
	}
	return node, nil
}

// CompleteNode completes a valid lease once; the same output is idempotent.
func (r *Repository) CompleteNode(ctx context.Context, c domain.Completion) (domain.NodeRun, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_COMPLETE_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	node, err := scanNode(tx.QueryRow(ctx, nodeSelect+` WHERE id=$1 FOR UPDATE`, string(c.NodeID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_NODE_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_NODE_COMPLETE_FAILED")
	}
	if node.Status == domain.StatusSucceeded {
		if !jsonEqual(node.Output, c.Output) {
			return domain.NodeRun{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_NODE_ALREADY_COMPLETED", false, errors.New("completed output differs"))
		}
		return node, nil
	}
	if node.Status != domain.StatusRunning || node.LeaseOwner != c.LeaseOwner || node.LeaseUntil == nil || !node.LeaseUntil.After(c.At) {
		return domain.NodeRun{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, errors.New("node lease is not valid"))
	}
	if err = domain.ValidateTransition(node.Status, domain.StatusSucceeded); err != nil {
		return domain.NodeRun{}, err
	}
	if err = validateEventScope(ctx, tx, c.Event, node.RunID); err != nil {
		return domain.NodeRun{}, err
	}
	node, err = scanNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status='succeeded',output=$2,lease_owner=NULL,lease_until=NULL,completed_at=$3,updated_at=$3,version=version+1 WHERE id=$1 RETURNING `+nodeColumns, string(c.NodeID), c.Output, c.At.UTC()))
	if err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_NODE_COMPLETE_FAILED")
	}
	if err = insertEvent(ctx, tx, c.Event); err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_OUTBOX_CREATE_FAILED")
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NodeRun{}, classify(err, "WORKFLOW_COMPLETE_COMMIT_FAILED")
	}
	return node, nil
}

// CreateHumanTask atomically waits the node and run for a human decision.
func (r *Repository) CreateHumanTask(ctx context.Context, task domain.HumanTask, event domain.OutboxEvent, now time.Time) (domain.HumanTask, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_TASK_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = validateEventScope(ctx, tx, event, task.RunID); err != nil {
		return domain.HumanTask{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE workflow.node_run SET status='waiting_for_human',lease_owner=NULL,lease_until=NULL,updated_at=$3,version=version+1 WHERE id=$1 AND run_id=$2 AND status='running'`, string(task.NodeRunID), string(task.RunID), now.UTC())
	if err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_TASK_NODE_UPDATE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return domain.HumanTask{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_NODE_NOT_RUNNING", false, errors.New("node is not running"))
	}
	runTag, err := tx.Exec(ctx, `UPDATE workflow.run SET status='waiting_for_human',updated_at=$2,version=version+1 WHERE id=$1 AND status='running'`, string(task.RunID), now.UTC())
	if err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_TASK_RUN_UPDATE_FAILED")
	}
	if runTag.RowsAffected() != 1 {
		return domain.HumanTask{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_RUN_NOT_RUNNING", false, errors.New("run is not running"))
	}
	persisted, err := scanHuman(tx.QueryRow(ctx, `INSERT INTO workflow.human_task(id,run_id,node_run_id,status,expected_input_schema,target_version,expires_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+humanColumns, string(task.ID), string(task.RunID), string(task.NodeRunID), string(task.Status), task.ExpectedInputSchema, task.TargetVersion, task.ExpiresAt, task.CreatedAt.UTC()))
	if err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_TASK_CREATE_FAILED")
	}
	if err = insertEvent(ctx, tx, event); err != nil {
		return domain.HumanTask{}, classify(err, "WORKFLOW_OUTBOX_CREATE_FAILED")
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_TASK_COMMIT_FAILED")
	}
	return persisted, nil
}

// SubmitHumanTask accepts an unexpired target version exactly once.
func (r *Repository) SubmitHumanTask(ctx context.Context, id foundation.ID, targetVersion int64, decision json.RawMessage, now time.Time, event domain.OutboxEvent) (domain.HumanTask, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_SUBMIT_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanHuman(tx.QueryRow(ctx, `SELECT `+humanColumns+` FROM workflow.human_task WHERE id=$1 FOR UPDATE`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.HumanTask{}, foundation.NewError(foundation.ErrorNotFound, "HUMAN_TASK_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_TASK_QUERY_FAILED")
	}
	if current.Status != domain.HumanTaskPending {
		return domain.HumanTask{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_ALREADY_RESOLVED", false, errors.New("human task is not pending"))
	}
	if current.TargetVersion != targetVersion {
		return domain.HumanTask{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_VERSION_CONFLICT", false, errors.New("target version differs"))
	}
	if current.ExpiresAt != nil && !current.ExpiresAt.After(now) {
		if _, err = tx.Exec(ctx, `UPDATE workflow.human_task SET status='expired' WHERE id=$1`, string(id)); err != nil {
			return domain.HumanTask{}, classify(err, "HUMAN_TASK_EXPIRE_FAILED")
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.HumanTask{}, classify(err, "HUMAN_TASK_EXPIRE_FAILED")
		}
		return domain.HumanTask{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_EXPIRED", false, errors.New("human task expired"))
	}
	if err = validateEventScope(ctx, tx, event, current.RunID); err != nil {
		return domain.HumanTask{}, err
	}
	persisted, err := scanHuman(tx.QueryRow(ctx, `UPDATE workflow.human_task SET status='submitted',decision=$2,submitted_at=$3 WHERE id=$1 RETURNING `+humanColumns, string(id), decision, now.UTC()))
	if err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_TASK_SUBMIT_FAILED")
	}
	nodeTag, err := tx.Exec(ctx, `UPDATE workflow.node_run SET status='pending',updated_at=$2,version=version+1 WHERE id=$1 AND status='waiting_for_human'`, string(current.NodeRunID), now.UTC())
	if err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_TASK_NODE_RESUME_FAILED")
	}
	if nodeTag.RowsAffected() != 1 {
		return domain.HumanTask{}, foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_NODE_STATE_INVALID", false, errors.New("human node is not waiting"))
	}
	runTag, err := tx.Exec(ctx, `UPDATE workflow.run SET status='running',updated_at=$2,version=version+1 WHERE id=$1 AND status='waiting_for_human'`, string(current.RunID), now.UTC())
	if err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_TASK_RUN_RESUME_FAILED")
	}
	if runTag.RowsAffected() != 1 {
		return domain.HumanTask{}, foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_RUN_STATE_INVALID", false, errors.New("human run is not waiting"))
	}
	if err = insertEvent(ctx, tx, event); err != nil {
		return domain.HumanTask{}, classify(err, "WORKFLOW_OUTBOX_CREATE_FAILED")
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.HumanTask{}, classify(err, "HUMAN_SUBMIT_COMMIT_FAILED")
	}
	return persisted, nil
}

const runColumns = `id::text,workspace_id::text,definition_id::text,status,input,output,version,created_at,updated_at,completed_at`
const runSelect = `SELECT ` + runColumns + ` FROM workflow.run`
const nodeColumns = `id::text,run_id::text,node_key,node_type,status,attempt,input,output,lease_owner,lease_until,version,created_at,updated_at,completed_at`
const nodeSelect = `SELECT ` + nodeColumns + ` FROM workflow.node_run`
const nodeUpdateReturning = `UPDATE workflow.node_run SET status=$2,attempt=$3,lease_owner=$4,lease_until=$5,updated_at=$6,version=$7`
const humanColumns = `id::text,run_id::text,node_run_id::text,status,expected_input_schema,target_version,decision,expires_at,submitted_at,created_at`

func insertRun(ctx context.Context, tx pgx.Tx, r domain.Run) (domain.Run, error) {
	return scanRun(tx.QueryRow(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+runColumns, string(r.ID), string(r.WorkspaceID), string(r.DefinitionID), string(r.Status), r.Input, r.Version, r.CreatedAt.UTC(), r.UpdatedAt.UTC()))
}
func insertEvent(ctx context.Context, tx pgx.Tx, e domain.OutboxEvent) error {
	var runID any
	if e.RunID != nil {
		runID = string(*e.RunID)
	}
	_, err := tx.Exec(ctx, `INSERT INTO workflow.outbox_event(id,workspace_id,run_id,event_type,idempotency_key,payload,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, string(e.ID), string(e.WorkspaceID), runID, e.Type, e.IdempotencyKey, e.Payload, e.OccurredAt.UTC())
	return err
}

func validateEventScope(ctx context.Context, tx pgx.Tx, event domain.OutboxEvent, expectedRunID foundation.ID) error {
	if event.RunID == nil || *event.RunID != expectedRunID {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_EVENT_SCOPE_INVALID", false, errors.New("outbox event run does not match workflow state"))
	}
	var workspaceID string
	if err := tx.QueryRow(ctx, `SELECT workspace_id::text FROM workflow.run WHERE id=$1`, string(expectedRunID)).Scan(&workspaceID); err != nil {
		return classify(err, "WORKFLOW_EVENT_SCOPE_QUERY_FAILED")
	}
	if foundation.ID(workspaceID) != event.WorkspaceID {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_EVENT_SCOPE_INVALID", false, errors.New("outbox event workspace does not match workflow state"))
	}
	return nil
}
func scanRun(row pgx.Row) (domain.Run, error) {
	var r domain.Run
	var id, w, d string
	var output []byte
	err := row.Scan(&id, &w, &d, &r.Status, &r.Input, &output, &r.Version, &r.CreatedAt, &r.UpdatedAt, &r.CompletedAt)
	if err != nil {
		return r, err
	}
	r.ID = foundation.ID(id)
	r.WorkspaceID = foundation.ID(w)
	r.DefinitionID = foundation.ID(d)
	r.Output = output
	return r, nil
}
func scanNode(row pgx.Row) (domain.NodeRun, error) {
	var n domain.NodeRun
	var id, run string
	var output []byte
	var leaseOwner *string
	err := row.Scan(&id, &run, &n.NodeKey, &n.NodeType, &n.Status, &n.Attempt, &n.Input, &output, &leaseOwner, &n.LeaseUntil, &n.Version, &n.CreatedAt, &n.UpdatedAt, &n.CompletedAt)
	if err != nil {
		return n, err
	}
	n.ID = foundation.ID(id)
	n.RunID = foundation.ID(run)
	n.Output = output
	if leaseOwner != nil {
		n.LeaseOwner = *leaseOwner
	}
	return n, nil
}
func scanHuman(row pgx.Row) (domain.HumanTask, error) {
	var h domain.HumanTask
	var id, run, node string
	var decision []byte
	err := row.Scan(&id, &run, &node, &h.Status, &h.ExpectedInputSchema, &h.TargetVersion, &decision, &h.ExpiresAt, &h.SubmittedAt, &h.CreatedAt)
	if err != nil {
		return h, err
	}
	h.ID = foundation.ID(id)
	h.RunID = foundation.ID(run)
	h.NodeRunID = foundation.ID(node)
	h.Decision = decision
	return h, nil
}
func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}
func classify(err error, code string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONFLICT", false, err)
		case "23503":
			return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_REFERENCE_INVALID", false, err)
		case "23514", "22P02":
			return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_DATA_INVALID", false, err)
		case "40001", "40P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, fmt.Errorf("workflow database operation: %w", err))
}

var _ domain.Repository = (*Repository)(nil)
