package workflowpostgres

import (
	"context"
	"crypto/sha256"
	encodinghex "encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
)

const runtimeEventSchemaVersion = 1

const runtimeRunColumns = runColumns + `,pause_requested_at,cancel_requested_at`

const runtimeNodeColumns = nodeColumns + `,retry_no,next_attempt_at,failure_class,error_kind,error_code,error_summary`

const runtimeAttemptColumns = `id::text,node_run_id::text,attempt_no,dispatch_no,retry_no,river_job_id,river_job_attempt,delivery_id,lease_owner,lease_until,status,output_schema_version,output_hash,failure_class,error_kind,error_code,error_summary,next_attempt_at,started_at,heartbeat_at,ended_at`

// Claim implements the DB-time lease acquisition and lease reclaim contract.
func (r *RuntimeRepository) Claim(ctx context.Context, command application.ClaimCommand) (application.ClaimResult, error) {
	if r == nil || isNilDB(r.db) || isNilJobInserter(r.jobs) {
		return application.ClaimResult{}, runtimeStateError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RUNTIME_DATABASE_UNAVAILABLE")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_CLAIM_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	var runID string
	if err := tx.QueryRow(ctx, `SELECT run_id::text FROM workflow.node_run WHERE id=$1`, string(command.NodeRunID)).Scan(&runID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ClaimResult{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_NODE_NOT_FOUND", false, err)
		}
		return application.ClaimResult{}, classify(err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	run, err := scanRuntimeRun(tx.QueryRow(ctx, `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=$1 FOR UPDATE`, runID))
	if err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	definition, err := scanClaimDefinition(tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,key,version,graph,created_at FROM workflow.definition WHERE id=$1`, string(run.DefinitionID)))
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			return application.ClaimResult{}, err
		}
		return application.ClaimResult{}, classify(err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	if definition.ID != run.DefinitionID || definition.WorkspaceID != run.WorkspaceID {
		return application.ClaimResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_BINDING_INVALID", false, errors.New("workflow run definition binding differs"))
	}
	node, err := scanRuntimeNode(tx.QueryRow(ctx, `SELECT `+runtimeNodeColumns+` FROM workflow.node_run WHERE id=$1 FOR UPDATE`, string(command.NodeRunID)))
	if err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	if node.DispatchNo < 1 || node.IdempotencyKey == "" {
		return application.ClaimResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED", false, errors.New("active workflow node lacks runtime identity"))
	}
	if existing, found, queryErr := findAttemptByDelivery(ctx, tx, command.NodeRunID, command.DispatchNo, command.DeliveryID); queryErr != nil {
		return application.ClaimResult{}, queryErr
	} else if found {
		if existing.Status == domain.AttemptStatusRunning && existing.LeaseOwner == command.LeaseOwner && node.Status == domain.NodeStatusRunning && existing.LeaseUntil.After(now) && node.LeaseUntil != nil && node.LeaseUntil.After(now) {
			return commitClaim(ctx, tx, application.ClaimResult{Disposition: application.ClaimDispositionClaimed, Definition: definition, Run: run, Node: node, Attempt: existing, ObservedNodeKind: node.NodeType})
		}
		if existing.Status != domain.AttemptStatusRunning || existing.LeaseUntil.After(now) {
			return commitClaim(ctx, tx, application.ClaimResult{Disposition: application.ClaimDispositionStale, ObservedNodeKind: node.NodeType, DuplicateDelivery: true})
		}
		// An expired duplicate delivery may reclaim the same River generation;
		// the old Attempt is reduced to lease_lost below before a new Attempt is
		// appended. It must not be treated as a benign stale delivery.
	}
	if domain.IsTerminalRunStatus(run.Status) || run.PauseRequestedAt != nil {
		return commitClaim(ctx, tx, application.ClaimResult{Disposition: application.ClaimDispositionStale})
	}
	if node.DispatchNo != command.DispatchNo {
		return commitClaim(ctx, tx, application.ClaimResult{Disposition: application.ClaimDispositionStale, ObservedNodeKind: node.NodeType, DuplicateDelivery: true})
	}
	if node.Status == domain.NodeStatusRetryWait && (node.NextAttemptAt == nil || node.NextAttemptAt.After(now)) {
		return commitClaim(ctx, tx, application.ClaimResult{Disposition: application.ClaimDispositionStale, ObservedNodeKind: node.NodeType, DuplicateDelivery: true})
	}
	if node.Status != domain.NodeStatusPending && node.Status != domain.NodeStatusRetryWait && node.Status != domain.NodeStatusRunning {
		return commitClaim(ctx, tx, application.ClaimResult{Disposition: application.ClaimDispositionStale, ObservedNodeKind: node.NodeType, DuplicateDelivery: true})
	}
	if node.Status == domain.NodeStatusRunning && node.LeaseUntil != nil && node.LeaseUntil.After(now) {
		var activeRiverJobID *int64
		var activeRiverJobAttempt *int
		if err := tx.QueryRow(ctx, `SELECT river_job_id,river_job_attempt FROM workflow.node_attempt WHERE node_run_id=$1 AND attempt_no=$2 AND status='running'`, string(node.ID), node.Attempt).Scan(&activeRiverJobID, &activeRiverJobAttempt); err != nil {
			return application.ClaimResult{}, classify(err, "WORKFLOW_ACTIVE_ATTEMPT_QUERY_FAILED")
		}
		// A higher River attempt means the previous delivery returned a transport
		// error after its Workflow Claim committed. A benign stale result would let
		// River complete the unique Job while the Workflow lease later expires with
		// no delivery left to reclaim it, so keep the Job retryable until expiry.
		if activeRiverJobID != nil && activeRiverJobAttempt != nil && *activeRiverJobID == command.RiverJobID && command.RiverJobAttempt > *activeRiverJobAttempt {
			return application.ClaimResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_LEASE_HELD", true, errors.New("previous delivery lease is still active"))
		}
		return commitClaim(ctx, tx, application.ClaimResult{Disposition: application.ClaimDispositionStale, ObservedNodeKind: node.NodeType, DuplicateDelivery: true})
	}
	leaseReclaimed := false
	if node.Status == domain.NodeStatusRunning {
		if node.Attempt < 1 {
			return application.ClaimResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_ACTIVE_ATTEMPT_MISSING", false, errors.New("expired running node has no attempt projection"))
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow.node_attempt
SET status='lease_lost',failure_class='lease_lost',error_kind=$2,error_code='WORKFLOW_LEASE_LOST',error_summary='lease expired',lease_owner=NULL,lease_until=NULL,ended_at=$3,heartbeat_at=$3
WHERE node_run_id=$1 AND attempt_no=$4 AND status='running'`, string(node.ID), string(foundation.ErrorVersionConflict), now, node.Attempt); err != nil {
			return application.ClaimResult{}, classify(err, "WORKFLOW_ATTEMPT_RECLAIM_FAILED")
		}
		leaseReclaimed = true
	}
	attemptNo := node.Attempt + 1
	var maxAttempt int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(attempt_no),0) FROM workflow.node_attempt WHERE node_run_id=$1`, string(node.ID)).Scan(&maxAttempt); err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	if maxAttempt >= attemptNo {
		attemptNo = maxAttempt + 1
	}
	attemptID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return application.ClaimResult{}, err
	}
	leaseUntil := now.Add(command.LeaseDuration)
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.node_attempt
(id,node_run_id,attempt_no,dispatch_no,retry_no,river_job_id,river_job_attempt,delivery_id,lease_owner,lease_until,status,started_at,heartbeat_at)
VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,0),$8,$9,$10,'running',$11,$11)`, string(attemptID), string(node.ID), attemptNo, node.DispatchNo, node.RetryNo, command.RiverJobID, command.RiverJobAttempt, command.DeliveryID, command.LeaseOwner, leaseUntil, now); err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_ATTEMPT_CREATE_FAILED")
	}
	node, err = scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status='running',attempt=$2,lease_owner=$3,lease_until=$4,next_attempt_at=NULL,failure_class=NULL,error_kind=NULL,error_code=NULL,error_summary=NULL,updated_at=$5,version=version+1 WHERE id=$1 RETURNING `+runtimeNodeColumns, string(node.ID), attemptNo, command.LeaseOwner, leaseUntil, now))
	if err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_NODE_CLAIM_FAILED")
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow.run SET status='running',version=version+1,updated_at=$2 WHERE id=$1 AND status IN ('pending','retry_wait','waiting_for_human','running') AND pause_requested_at IS NULL AND cancel_requested_at IS NULL`, string(run.ID), now); err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_RUN_START_FAILED")
	}
	run, err = scanRuntimeRun(tx.QueryRow(ctx, `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=$1`, string(run.ID)))
	if err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	attempt, err := scanRuntimeAttempt(tx.QueryRow(ctx, `SELECT `+runtimeAttemptColumns+` FROM workflow.node_attempt WHERE id=$1`, string(attemptID)))
	if err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	return commitClaim(ctx, tx, application.ClaimResult{Disposition: application.ClaimDispositionClaimed, Definition: definition, Run: run, Node: node, Attempt: attempt, ObservedNodeKind: node.NodeType, DuplicateDelivery: leaseReclaimed, LeaseReclaimed: leaseReclaimed})
}

func scanClaimDefinition(row pgx.Row) (domain.Definition, error) {
	var definition domain.Definition
	var id, workspaceID string
	var graphJSON []byte
	if err := row.Scan(&id, &workspaceID, &definition.Key, &definition.Version, &graphJSON, &definition.CreatedAt); err != nil {
		return domain.Definition{}, err
	}
	graph, err := application.DecodeCanonicalGraph(graphJSON)
	if err != nil {
		return domain.Definition{}, err
	}
	canonicalGraph, err := json.Marshal(graph)
	if err != nil {
		return domain.Definition{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_GRAPH_INVALID", false, err)
	}
	graphHash, err := application.ComputeCanonicalGraphHash(graph)
	if err != nil {
		return domain.Definition{}, err
	}
	definition.ID = foundation.ID(id)
	definition.WorkspaceID = foundation.ID(workspaceID)
	definition.Graph = canonicalGraph
	definition.GraphHash = graphHash
	return definition, nil
}

func commitClaim(ctx context.Context, tx pgx.Tx, result application.ClaimResult) (application.ClaimResult, error) {
	if err := tx.Commit(ctx); err != nil {
		return application.ClaimResult{}, classify(err, "WORKFLOW_CLAIM_COMMIT_FAILED")
	}
	return result, nil
}

// Heartbeat extends an unexpired lease using a complete CAS fence.
func (r *RuntimeRepository) Heartbeat(ctx context.Context, command application.HeartbeatCommand) (application.HeartbeatResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return application.HeartbeatResult{}, classify(err, "WORKFLOW_HEARTBEAT_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return application.HeartbeatResult{}, classify(err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	var runID string
	if err := tx.QueryRow(ctx, `SELECT run_id::text FROM workflow.node_run WHERE id=$1`, string(command.NodeRunID)).Scan(&runID); err != nil {
		return application.HeartbeatResult{}, classify(err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	var pauseRequestedAt, cancelRequestedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT pause_requested_at,cancel_requested_at FROM workflow.run WHERE id=$1 FOR UPDATE`, runID).Scan(&pauseRequestedAt, &cancelRequestedAt); err != nil {
		return application.HeartbeatResult{}, classify(err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	if cancelRequestedAt != nil {
		safe, safetyErr := r.safeToCancelWorkflowNode(ctx, tx, command.NodeRunID)
		if safetyErr != nil {
			return application.HeartbeatResult{}, safetyErr
		}
		if safe {
			return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CANCEL_REQUESTED", false, errors.New("workflow cancellation checkpoint requested"))
		}
	}
	if pauseRequestedAt != nil {
		return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_PAUSE_REQUESTED", false, errors.New("workflow pause checkpoint requested"))
	}
	leaseUntil := now.Add(command.LeaseDuration)
	node, err := scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run
SET lease_until=$4,updated_at=$3,version=version+1
WHERE id=$1 AND status='running' AND lease_owner=$2 AND version=$5 AND lease_until>$3
RETURNING `+runtimeNodeColumns, string(command.NodeRunID), command.Fence.Owner, now, leaseUntil, command.Fence.NodeVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		var status domain.NodeStatus
		var owner *string
		var currentLeaseUntil *time.Time
		if queryErr := tx.QueryRow(ctx, `SELECT status,lease_owner,lease_until FROM workflow.node_run WHERE id=$1`, string(command.NodeRunID)).Scan(&status, &owner, &currentLeaseUntil); queryErr == nil && status == domain.NodeStatusRunning && owner != nil && *owner == command.Fence.Owner && currentLeaseUntil != nil && !currentLeaseUntil.After(now) {
			return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_LEASE_EXPIRED", true, err)
		}
		return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, err)
	}
	if err != nil {
		return application.HeartbeatResult{}, classify(err, "WORKFLOW_HEARTBEAT_FAILED")
	}
	attempt, err := scanRuntimeAttempt(tx.QueryRow(ctx, `UPDATE workflow.node_attempt SET lease_until=$5,heartbeat_at=$4
WHERE node_run_id=$1 AND attempt_no=$2 AND lease_owner=$3 AND status='running' AND lease_until>$4
RETURNING `+runtimeAttemptColumns, string(command.NodeRunID), command.Fence.AttemptNo, command.Fence.Owner, now, leaseUntil))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.HeartbeatResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, err)
	}
	if err != nil {
		return application.HeartbeatResult{}, classify(err, "WORKFLOW_HEARTBEAT_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return application.HeartbeatResult{}, classify(err, "WORKFLOW_HEARTBEAT_COMMIT_FAILED")
	}
	return application.HeartbeatResult{Node: node, Attempt: attempt}, nil
}

// TransitionDelivery atomically reduces one claimed delivery and creates any
// successor generation, Outbox event and River job in the same transaction.
func (r *RuntimeRepository) TransitionDelivery(ctx context.Context, command application.DeliveryTransition) (application.DeliveryTransitionResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_DELIVERY_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	var runID string
	if err := tx.QueryRow(ctx, `SELECT run_id::text FROM workflow.node_run WHERE id=$1`, string(command.Binding.NodeRunID)).Scan(&runID); err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	run, err := scanRuntimeRun(tx.QueryRow(ctx, `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=$1 FOR UPDATE`, runID))
	if err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	nodes, err := lockRuntimeNodes(ctx, tx, run.ID)
	if err != nil {
		return application.DeliveryTransitionResult{}, err
	}
	node, ok := nodes[command.Binding.NodeRunID]
	if !ok {
		return application.DeliveryTransitionResult{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_NODE_NOT_FOUND", false, errors.New("workflow node not found"))
	}
	attempt, found, err := findAttemptByDelivery(ctx, tx, command.Binding.NodeRunID, command.Binding.DispatchNo, command.Binding.DeliveryID)
	if err != nil {
		return application.DeliveryTransitionResult{}, err
	}
	if !found {
		return application.DeliveryTransitionResult{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_ATTEMPT_NOT_FOUND", false, errors.New("workflow delivery attempt not found"))
	}
	if attempt.Status != domain.AttemptStatusRunning {
		if attempt.Status == domain.AttemptStatusCancelled && (attempt.ErrorCode == "WORKFLOW_PAUSED" || attempt.ErrorCode == "WORKFLOW_CANCELLED") {
			if err := tx.Commit(ctx); err != nil {
				return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_DELIVERY_COMMIT_FAILED")
			}
			return application.DeliveryTransitionResult{Run: run, Node: node, Attempt: attempt, Replayed: true}, nil
		}
		if sameAttemptResult(attempt, command.Result) {
			if err := tx.Commit(ctx); err != nil {
				return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_DELIVERY_COMMIT_FAILED")
			}
			return application.DeliveryTransitionResult{Run: run, Node: node, Attempt: attempt, Replayed: true}, nil
		}
		return application.DeliveryTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_COMPLETION_CONFLICT", false, errors.New("delivery already reduced with a different result"))
	}
	if node.Status != domain.NodeStatusRunning || node.Version != command.Binding.Fence.NodeVersion || node.LeaseOwner != command.Binding.Fence.Owner || node.LeaseUntil == nil || !node.LeaseUntil.After(now) || attempt.AttemptNo != command.Binding.Fence.AttemptNo || attempt.LeaseOwner != command.Binding.Fence.Owner {
		return application.DeliveryTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, errors.New("delivery lease fence failed"))
	}
	if err := domain.ValidateAttemptResult(command.Result); err != nil {
		return application.DeliveryTransitionResult{}, err
	}
	if run.CancelRequestedAt != nil {
		safe, safetyErr := r.safeToCancelWorkflowNode(ctx, tx, node.ID)
		if safetyErr != nil {
			return application.DeliveryTransitionResult{}, safetyErr
		}
		if safe {
			return r.controlCheckpoint(ctx, tx, run, nodes, node, attempt, domain.NodeStatusCancelled, "WORKFLOW_CANCELLED", now)
		}
		return application.DeliveryTransitionResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_CANCELLATION_DEFERRED", true, errors.New("workflow cancellation waits for side-effect recovery checkpoint"))
	}
	if run.PauseRequestedAt != nil {
		return r.controlCheckpoint(ctx, tx, run, nodes, node, attempt, domain.NodeStatusPaused, "WORKFLOW_PAUSED", now)
	}
	if command.Result.Failure == nil {
		return r.completeDelivery(ctx, tx, run, nodes, node, attempt, command.Result, now)
	}
	return r.failDelivery(ctx, tx, run, nodes, node, attempt, *command.Result.Failure, now)
}

// Control persists pause, resume, and cancel commands with idempotent results.
func (r *RuntimeRepository) Control(ctx context.Context, command application.ControlTransition) (application.ControlPersistenceResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	run, err := scanRuntimeRun(tx.QueryRow(ctx, `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=$1 FOR UPDATE`, string(command.WorkflowRunID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_RUN_NOT_FOUND", false, err)
	}
	if err != nil {
		return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	var storedHash string
	var resultJSON []byte
	var storedExpected int64
	err = tx.QueryRow(ctx, `SELECT request_hash,result,expected_version FROM workflow.control_command WHERE run_id=$1 AND command=$2 AND idempotency_key=$3 FOR UPDATE`, string(command.WorkflowRunID), string(command.Action), command.IdempotencyKey).Scan(&storedHash, &resultJSON, &storedExpected)
	if err == nil {
		if storedHash != command.RequestHash || storedExpected != command.ExpectedVersion {
			return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("control idempotency binding differs"))
		}
		var persisted application.ControlPersistenceResult
		if err := json.Unmarshal(resultJSON, &persisted); err != nil {
			return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_CONTROL_RESULT_INVALID", false, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_COMMIT_FAILED")
		}
		return persisted, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_QUERY_FAILED")
	}
	if run.Version != command.ExpectedVersion {
		return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_VERSION_CONFLICT", false, errors.New("workflow version differs"))
	}
	if domain.IsTerminalRunStatus(run.Status) {
		return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("terminal workflow cannot be controlled"))
	}
	nodes, err := lockRuntimeNodes(ctx, tx, run.ID)
	if err != nil {
		return application.ControlPersistenceResult{}, err
	}
	status := run.Status
	switch command.Action {
	case application.ControlActionPause:
		if run.CancelRequestedAt != nil || run.PauseRequestedAt != nil {
			return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("workflow control request already exists"))
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow.run SET pause_requested_at=$2,version=version+1,updated_at=$2 WHERE id=$1`, string(run.ID), now); err != nil {
			return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_UPDATE_FAILED")
		}
		if !hasRunningNode(nodes) {
			for id, node := range nodes {
				if node.Status == domain.NodeStatusPending || node.Status == domain.NodeStatusRetryWait {
					updated, updateErr := pauseNode(ctx, tx, node, now)
					if updateErr != nil {
						return application.ControlPersistenceResult{}, updateErr
					}
					nodes[id] = updated
				}
			}
			status = domain.RunStatusPaused
			if _, err := tx.Exec(ctx, `UPDATE workflow.run SET status='paused',version=version+1,updated_at=$2 WHERE id=$1`, string(run.ID), now); err != nil {
				return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_UPDATE_FAILED")
			}
		}
	case application.ControlActionResume:
		if run.Status != domain.RunStatusPaused || run.PauseRequestedAt == nil {
			return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("workflow is not paused"))
		}
		if err := resumeNodes(ctx, tx, r.jobs, run, nodes, now); err != nil {
			return application.ControlPersistenceResult{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow.run SET pause_requested_at=NULL,status='running',version=version+1,updated_at=$2 WHERE id=$1`, string(run.ID), now); err != nil {
			return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_UPDATE_FAILED")
		}
		run.PauseRequestedAt = nil
		run.Status = domain.RunStatusRunning
		for _, node := range nodes {
			if node.Status == domain.NodeStatusSucceeded {
				if err := r.activateSuccessors(ctx, tx, run, nodes, node, now); err != nil {
					return application.ControlPersistenceResult{}, err
				}
			}
		}
		status, err = reduceRunStatus(run, nodes)
		if err != nil {
			return application.ControlPersistenceResult{}, err
		}
		if status != domain.RunStatusRunning {
			run, err = persistRunReduction(ctx, tx, run, status, now)
			if err != nil {
				return application.ControlPersistenceResult{}, err
			}
		}
	case application.ControlActionCancel:
		if run.CancelRequestedAt != nil {
			return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("workflow cancellation already requested"))
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow.run SET cancel_requested_at=$2,version=version+1,updated_at=$2 WHERE id=$1`, string(run.ID), now); err != nil {
			return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_UPDATE_FAILED")
		}
		deferredCancellation := false
		for id, node := range nodes {
			safe, safetyErr := r.safeToCancelWorkflowNode(ctx, tx, node.ID)
			if safetyErr != nil {
				return application.ControlPersistenceResult{}, safetyErr
			}
			if !safe {
				deferredCancellation = true
				continue
			}
			if node.Status != domain.NodeStatusRunning && !domain.IsTerminalNodeStatus(node.Status) {
				updated, updateErr := cancelNode(ctx, tx, node, now)
				if updateErr != nil {
					return application.ControlPersistenceResult{}, updateErr
				}
				if _, updateErr := tx.Exec(ctx, `UPDATE workflow.human_task SET status='cancelled' WHERE node_run_id=$1 AND status='pending'`, string(node.ID)); updateErr != nil {
					return application.ControlPersistenceResult{}, classify(updateErr, "WORKFLOW_HUMAN_TASK_CANCEL_FAILED")
				}
				nodes[id] = updated
			}
		}
		if !hasRunningNode(nodes) && !deferredCancellation {
			status = domain.RunStatusCancelled
			if _, err := tx.Exec(ctx, `UPDATE workflow.run SET status='cancelled',completed_at=$2,version=version+1,updated_at=$2 WHERE id=$1`, string(run.ID), now); err != nil {
				return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_UPDATE_FAILED")
			}
		}
	default:
		return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CONTROL_INVALID", false, errors.New("unknown workflow control action"))
	}
	var currentVersion int64
	if err := tx.QueryRow(ctx, `SELECT version FROM workflow.run WHERE id=$1`, string(run.ID)).Scan(&currentVersion); err != nil {
		return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_QUERY_FAILED")
	}
	result := application.ControlPersistenceResult{WorkflowRunID: run.ID, Status: status, Version: currentVersion}
	resultJSON, err = json.Marshal(result)
	if err != nil {
		return application.ControlPersistenceResult{}, foundation.NewError(foundation.ErrorNonRetryableFailure, "WORKFLOW_CONTROL_RESULT_ENCODING_FAILED", false, err)
	}
	commandID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return application.ControlPersistenceResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.control_command(id,run_id,command,idempotency_key,request_hash,expected_version,result,created_at,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, string(commandID), string(run.ID), string(command.Action), command.IdempotencyKey, command.RequestHash, command.ExpectedVersion, resultJSON, now); err != nil {
		return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return application.ControlPersistenceResult{}, classify(err, "WORKFLOW_CONTROL_COMMIT_FAILED")
	}
	return result, nil
}

func (r *RuntimeRepository) safeToCancelWorkflowNode(ctx context.Context, transaction any, nodeRunID foundation.ID) (bool, error) {
	if r == nil || r.cancellation == nil {
		return true, nil
	}
	safe, err := r.cancellation.SafeToCancelWorkflowNode(ctx, transaction, nodeRunID)
	if err != nil {
		return false, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_CANCELLATION_SAFETY_UNAVAILABLE", true, err)
	}
	return safe, nil
}

// WaitForHuman releases a leased Attempt and creates a pending Human Task in
// one DB-time transaction. The Worker lease is never held while waiting.
func (r *RuntimeRepository) WaitForHuman(ctx context.Context, command application.HumanWaitTransition) (application.HumanTransitionResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "HUMAN_TASK_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	run, err := scanRuntimeRun(tx.QueryRow(ctx, `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=$1 FOR UPDATE`, string(command.RunID)))
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	nodes, err := lockRuntimeNodes(ctx, tx, run.ID)
	if err != nil {
		return application.HumanTransitionResult{}, err
	}
	node, ok := nodes[command.NodeRunID]
	if !ok {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_NODE_NOT_FOUND", false, errors.New("human node not found"))
	}
	attempt, found, err := findAttemptByNo(ctx, tx, command.NodeRunID, command.Fence.AttemptNo)
	if err != nil {
		return application.HumanTransitionResult{}, err
	}
	if !found || node.Status != domain.NodeStatusRunning || node.Version != command.Fence.NodeVersion || node.LeaseOwner != command.Fence.Owner || node.LeaseUntil == nil || !node.LeaseUntil.After(now) || attempt.Status != domain.AttemptStatusRunning || attempt.LeaseOwner != command.Fence.Owner {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, errors.New("human task lease fence failed"))
	}
	if domain.IsTerminalRunStatus(run.Status) || run.CancelRequestedAt != nil {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("workflow cannot wait for human"))
	}
	expiresAt := now.Add(command.ExpiresIn)
	updatedAttempt, err := scanRuntimeAttempt(tx.QueryRow(ctx, `UPDATE workflow.node_attempt SET status='waiting_for_human',lease_owner=NULL,lease_until=NULL,ended_at=$2,heartbeat_at=$2 WHERE id=$1 RETURNING `+runtimeAttemptColumns, string(attempt.ID), now))
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "HUMAN_TASK_ATTEMPT_UPDATE_FAILED")
	}
	updatedNode, err := scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status='waiting_for_human',lease_owner=NULL,lease_until=NULL,updated_at=$2,version=version+1 WHERE id=$1 RETURNING `+runtimeNodeColumns, string(node.ID), now))
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "HUMAN_TASK_NODE_UPDATE_FAILED")
	}
	nodes[node.ID] = updatedNode
	status, err := reduceRunStatus(run, nodes)
	if err != nil {
		return application.HumanTransitionResult{}, err
	}
	run, err = persistRunReduction(ctx, tx, run, status, now)
	if err != nil {
		return application.HumanTransitionResult{}, err
	}
	task, err := scanHuman(tx.QueryRow(ctx, `INSERT INTO workflow.human_task(id,run_id,node_run_id,status,expected_input_schema,target_version,expires_at,created_at) VALUES($1,$2,$3,'pending',$4,$5,$6,$7) RETURNING `+humanColumns, string(command.TaskID), string(run.ID), string(node.ID), command.ExpectedInputSchema, command.TargetVersion, expiresAt, now))
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "HUMAN_TASK_CREATE_FAILED")
	}
	if err := insertRuntimeEvent(ctx, tx, run, updatedNode, updatedAttempt, "workflow.human.requested"); err != nil {
		return application.HumanTransitionResult{}, err
	}
	run, err = scanRuntimeRun(tx.QueryRow(ctx, `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=$1`, string(run.ID)))
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return application.HumanTransitionResult{}, classify(err, "HUMAN_TASK_COMMIT_FAILED")
	}
	return application.HumanTransitionResult{Task: task, Run: run, Node: updatedNode, Attempt: updatedAttempt}, nil
}

// SubmitHuman completes a pending Human Task and activates canonical
// successors unless the Run is paused, in which case Resume owns dispatch.
func (r *RuntimeRepository) SubmitHuman(ctx context.Context, command application.HumanDecisionTransition) (application.HumanTransitionResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "HUMAN_SUBMIT_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "WORKFLOW_DB_TIME_UNAVAILABLE")
	}
	run, err := scanRuntimeRun(tx.QueryRow(ctx, `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=$1 FOR UPDATE`, string(command.RunID)))
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	nodes, err := lockRuntimeNodes(ctx, tx, run.ID)
	if err != nil {
		return application.HumanTransitionResult{}, err
	}
	task, err := scanHuman(tx.QueryRow(ctx, `SELECT `+humanColumns+` FROM workflow.human_task WHERE id=$1 AND run_id=$2 FOR UPDATE`, string(command.TaskID), string(run.ID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorNotFound, "HUMAN_TASK_NOT_FOUND", false, err)
	}
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "HUMAN_TASK_QUERY_FAILED")
	}
	if task.TargetVersion != command.TargetVersion {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_VERSION_CONFLICT", false, errors.New("target version differs"))
	}
	if task.Status == domain.HumanTaskSubmitted {
		if !jsonEqual(task.Decision, command.Decision) {
			return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_DECISION_CONFLICT", false, errors.New("submitted human decision differs"))
		}
		node := nodes[task.NodeRunID]
		attempt, _, _ := findLatestAttempt(ctx, tx, task.NodeRunID)
		if err := tx.Commit(ctx); err != nil {
			return application.HumanTransitionResult{}, classify(err, "HUMAN_SUBMIT_COMMIT_FAILED")
		}
		return application.HumanTransitionResult{Task: task, Run: run, Node: node, Attempt: attempt}, nil
	}
	if task.Status != domain.HumanTaskPending {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_ALREADY_RESOLVED", false, errors.New("human task is not pending"))
	}
	if task.ExpiresAt != nil && !task.ExpiresAt.After(now) {
		if _, err := tx.Exec(ctx, `UPDATE workflow.human_task SET status='expired' WHERE id=$1`, string(task.ID)); err != nil {
			return application.HumanTransitionResult{}, classify(err, "HUMAN_TASK_EXPIRE_FAILED")
		}
		if err := tx.Commit(ctx); err != nil {
			return application.HumanTransitionResult{}, classify(err, "HUMAN_TASK_EXPIRE_COMMIT_FAILED")
		}
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "HUMAN_TASK_EXPIRED", false, errors.New("human task expired"))
	}
	if run.CancelRequestedAt != nil || run.Status == domain.RunStatusCancelled {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CONFLICT", false, errors.New("cancelled workflow rejects human decision"))
	}
	node, ok := nodes[task.NodeRunID]
	if !ok || node.Status != domain.NodeStatusWaitingForHuman {
		return application.HumanTransitionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_NODE_STATE_INVALID", false, errors.New("human node is not waiting"))
	}
	if err := validateDecisionSchema(task.ExpectedInputSchema, command.Decision); err != nil {
		return application.HumanTransitionResult{}, err
	}
	updatedTask, err := scanHuman(tx.QueryRow(ctx, `UPDATE workflow.human_task SET status='submitted',decision=$2,submitted_at=$3 WHERE id=$1 RETURNING `+humanColumns, string(task.ID), command.Decision, now))
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "HUMAN_TASK_SUBMIT_FAILED")
	}
	updatedNode, err := scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status='succeeded',output=$2,completed_at=$3,updated_at=$3,version=version+1 WHERE id=$1 RETURNING `+runtimeNodeColumns, string(node.ID), command.Decision, now))
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "HUMAN_TASK_NODE_COMPLETE_FAILED")
	}
	nodes[node.ID] = updatedNode
	if run.Status != domain.RunStatusPaused && run.PauseRequestedAt == nil {
		if err := r.activateSuccessors(ctx, tx, run, nodes, updatedNode, now); err != nil {
			return application.HumanTransitionResult{}, err
		}
		status, reduceErr := reduceRunStatus(run, nodes)
		if reduceErr != nil {
			return application.HumanTransitionResult{}, reduceErr
		}
		run, err = persistRunReduction(ctx, tx, run, status, now)
		if err != nil {
			return application.HumanTransitionResult{}, err
		}
	}
	attempt, _, err := findLatestAttempt(ctx, tx, node.ID)
	if err != nil {
		return application.HumanTransitionResult{}, err
	}
	if err := insertRuntimeEvent(ctx, tx, run, updatedNode, attempt, "workflow.human.submitted"); err != nil {
		return application.HumanTransitionResult{}, err
	}
	run, err = scanRuntimeRun(tx.QueryRow(ctx, `SELECT `+runtimeRunColumns+` FROM workflow.run WHERE id=$1`, string(run.ID)))
	if err != nil {
		return application.HumanTransitionResult{}, classify(err, "WORKFLOW_RUN_QUERY_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return application.HumanTransitionResult{}, classify(err, "HUMAN_SUBMIT_COMMIT_FAILED")
	}
	return application.HumanTransitionResult{Task: updatedTask, Run: run, Node: updatedNode, Attempt: attempt}, nil
}

func databaseNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&now)
	return now, err
}

func validateDecisionSchema(schema, decision json.RawMessage) error {
	var schemaObject map[string]any
	var decisionObject map[string]any
	if json.Unmarshal(schema, &schemaObject) != nil || json.Unmarshal(decision, &decisionObject) != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "HUMAN_DECISION_SCHEMA_INVALID", false, errors.New("human decision schema or value is invalid"))
	}
	if schemaType, ok := schemaObject["type"].(string); ok && schemaType != "object" {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_SCHEMA_UNSUPPORTED", false, errors.New("human task schema root must be object"))
	}
	if required, ok := schemaObject["required"].([]any); ok {
		for _, rawName := range required {
			name, valid := rawName.(string)
			if !valid || strings.TrimSpace(name) == "" {
				return foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_SCHEMA_UNSUPPORTED", false, errors.New("human task required field is invalid"))
			}
			if _, exists := decisionObject[name]; !exists {
				return foundation.NewError(foundation.ErrorInvalidInput, "HUMAN_DECISION_SCHEMA_INVALID", false, errors.New("human decision misses required field"))
			}
		}
	}
	properties, _ := schemaObject["properties"].(map[string]any)
	for name, rawProperty := range properties {
		value, exists := decisionObject[name]
		if !exists {
			continue
		}
		property, valid := rawProperty.(map[string]any)
		if !valid {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_TASK_SCHEMA_UNSUPPORTED", false, errors.New("human task property schema is invalid"))
		}
		expected, _ := property["type"].(string)
		if expected != "" && !matchesJSONType(value, expected) {
			return foundation.NewError(foundation.ErrorInvalidInput, "HUMAN_DECISION_SCHEMA_INVALID", false, errors.New("human decision field type differs"))
		}
	}
	if additional, ok := schemaObject["additionalProperties"].(bool); ok && !additional {
		for name := range decisionObject {
			if _, exists := properties[name]; !exists {
				return foundation.NewError(foundation.ErrorInvalidInput, "HUMAN_DECISION_SCHEMA_INVALID", false, errors.New("human decision contains unknown field"))
			}
		}
	}
	return nil
}

func matchesJSONType(value any, expected string) bool {
	switch expected {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && number == float64(int64(number))
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func runtimeStateError(kind foundation.ErrorKind, code string) error {
	return foundation.NewError(kind, code, kind == foundation.ErrorDependencyUnavailable, errors.New("workflow runtime state dependency is unavailable"))
}

func scanRuntimeRun(row pgx.Row) (domain.Run, error) {
	var run domain.Run
	var id, workspace, definition string
	var idempotencyKey, requestHash *string
	if err := row.Scan(&id, &workspace, &definition, &run.Status, &run.Input, &run.Output, &idempotencyKey, &requestHash, &run.Version, &run.CreatedAt, &run.UpdatedAt, &run.CompletedAt, &run.PauseRequestedAt, &run.CancelRequestedAt); err != nil {
		return run, err
	}
	run.ID, run.WorkspaceID, run.DefinitionID = foundation.ID(id), foundation.ID(workspace), foundation.ID(definition)
	if idempotencyKey != nil {
		run.IdempotencyKey = *idempotencyKey
	}
	if requestHash != nil {
		run.RequestHash = *requestHash
	}
	return run, nil
}

func scanRuntimeNode(row pgx.Row) (domain.NodeRun, error) {
	var node domain.NodeRun
	var id, runID string
	var output []byte
	var idempotencyKey, leaseOwner, failureClass, errorKind, errorCode, errorSummary *string
	var inputSchema, outputSchema, dispatch *int
	if err := row.Scan(&id, &runID, &node.NodeKey, &node.NodeType, &node.Status, &node.Attempt, &node.Input, &output, &idempotencyKey, &inputSchema, &outputSchema, &dispatch, &leaseOwner, &node.LeaseUntil, &node.Version, &node.CreatedAt, &node.UpdatedAt, &node.CompletedAt, &node.RetryNo, &node.NextAttemptAt, &failureClass, &errorKind, &errorCode, &errorSummary); err != nil {
		return node, err
	}
	node.ID, node.RunID, node.Output = foundation.ID(id), foundation.ID(runID), output
	if idempotencyKey != nil {
		node.IdempotencyKey = *idempotencyKey
	}
	if inputSchema != nil {
		node.InputSchemaVersion = *inputSchema
	}
	if outputSchema != nil {
		node.OutputSchemaVersion = *outputSchema
	}
	if dispatch != nil {
		node.DispatchNo = *dispatch
	}
	if leaseOwner != nil {
		node.LeaseOwner = *leaseOwner
	}
	if failureClass != nil {
		node.FailureClass = domain.FailureClass(*failureClass)
	}
	if errorKind != nil {
		node.ErrorKind = foundation.ErrorKind(*errorKind)
	}
	if errorCode != nil {
		node.ErrorCode = *errorCode
	}
	if errorSummary != nil {
		node.ErrorSummary = *errorSummary
	}
	return node, nil
}

func scanRuntimeAttempt(row pgx.Row) (domain.NodeAttempt, error) {
	var attempt domain.NodeAttempt
	var id, nodeID string
	var riverJobAttempt *int
	var riverJobID64 *int64
	var leaseOwner, failureClass, errorKind, errorCode, errorSummary *string
	var outputSchemaVersion *int
	var outputHash *string
	var leaseUntil *time.Time
	if err := row.Scan(&id, &nodeID, &attempt.AttemptNo, &attempt.DispatchNo, &attempt.RetryNo, &riverJobID64, &riverJobAttempt, &attempt.DeliveryID, &leaseOwner, &leaseUntil, &attempt.Status, &outputSchemaVersion, &outputHash, &failureClass, &errorKind, &errorCode, &errorSummary, &attempt.NextAttemptAt, &attempt.StartedAt, &attempt.HeartbeatAt, &attempt.EndedAt); err != nil {
		return attempt, err
	}
	if riverJobID64 != nil {
		attempt.RiverJobID = *riverJobID64
	}
	if riverJobAttempt != nil {
		attempt.RiverJobAttempt = *riverJobAttempt
	}
	if outputSchemaVersion != nil {
		attempt.OutputSchemaVersion = *outputSchemaVersion
	}
	if outputHash != nil {
		attempt.OutputHash = *outputHash
	}
	if leaseUntil != nil {
		attempt.LeaseUntil = *leaseUntil
	}
	attempt.ID, attempt.NodeRunID = foundation.ID(id), foundation.ID(nodeID)
	if leaseOwner != nil {
		attempt.LeaseOwner = *leaseOwner
	}
	if failureClass != nil {
		attempt.FailureClass = domain.FailureClass(*failureClass)
	}
	if errorKind != nil {
		attempt.ErrorKind = foundation.ErrorKind(*errorKind)
	}
	if errorCode != nil {
		attempt.ErrorCode = *errorCode
	}
	if errorSummary != nil {
		attempt.ErrorSummary = *errorSummary
	}
	return attempt, nil
}

func findAttemptByDelivery(ctx context.Context, tx pgx.Tx, nodeID foundation.ID, dispatchNo int, deliveryID string) (domain.NodeAttempt, bool, error) {
	attempt, err := scanRuntimeAttempt(tx.QueryRow(ctx, `SELECT `+runtimeAttemptColumns+` FROM workflow.node_attempt WHERE node_run_id=$1 AND dispatch_no=$2 AND delivery_id=$3 ORDER BY attempt_no DESC LIMIT 1 FOR UPDATE`, string(nodeID), dispatchNo, deliveryID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NodeAttempt{}, false, nil
	}
	if err != nil {
		return domain.NodeAttempt{}, false, classify(err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	return attempt, true, nil
}

func findAttemptByNo(ctx context.Context, tx pgx.Tx, nodeID foundation.ID, attemptNo int) (domain.NodeAttempt, bool, error) {
	attempt, err := scanRuntimeAttempt(tx.QueryRow(ctx, `SELECT `+runtimeAttemptColumns+` FROM workflow.node_attempt WHERE node_run_id=$1 AND attempt_no=$2 FOR UPDATE`, string(nodeID), attemptNo))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NodeAttempt{}, false, nil
	}
	if err != nil {
		return domain.NodeAttempt{}, false, classify(err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	return attempt, true, nil
}

func findLatestAttempt(ctx context.Context, tx pgx.Tx, nodeID foundation.ID) (domain.NodeAttempt, bool, error) {
	attempt, err := scanRuntimeAttempt(tx.QueryRow(ctx, `SELECT `+runtimeAttemptColumns+` FROM workflow.node_attempt WHERE node_run_id=$1 ORDER BY attempt_no DESC LIMIT 1 FOR UPDATE`, string(nodeID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NodeAttempt{}, false, nil
	}
	if err != nil {
		return domain.NodeAttempt{}, false, classify(err, "WORKFLOW_ATTEMPT_QUERY_FAILED")
	}
	return attempt, true, nil
}

func lockRuntimeNodes(ctx context.Context, tx pgx.Tx, runID foundation.ID) (map[foundation.ID]domain.NodeRun, error) {
	rows, err := tx.Query(ctx, `SELECT `+runtimeNodeColumns+` FROM workflow.node_run WHERE run_id=$1 ORDER BY node_key,id FOR UPDATE`, string(runID))
	if err != nil {
		return nil, classify(err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	defer rows.Close()
	result := make(map[foundation.ID]domain.NodeRun)
	for rows.Next() {
		node, scanErr := scanRuntimeNode(rows)
		if scanErr != nil {
			return nil, classify(scanErr, "WORKFLOW_NODE_QUERY_FAILED")
		}
		result[node.ID] = node
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "WORKFLOW_NODE_QUERY_FAILED")
	}
	return result, nil
}

func sameAttemptResult(attempt domain.NodeAttempt, result domain.AttemptResult) bool {
	if result.Failure == nil {
		hash := sha256.Sum256(result.Output)
		return attempt.Status == domain.AttemptStatusSucceeded && attempt.OutputSchemaVersion == result.OutputSchemaVersion && strings.EqualFold(attempt.OutputHash, encodinghex.EncodeToString(hash[:]))
	}
	return attempt.FailureClass == result.Failure.Class && attempt.ErrorCode == result.Failure.Code
}

func (r *RuntimeRepository) completeDelivery(ctx context.Context, tx pgx.Tx, run domain.Run, nodes map[foundation.ID]domain.NodeRun, node domain.NodeRun, attempt domain.NodeAttempt, result domain.AttemptResult, now time.Time) (application.DeliveryTransitionResult, error) {
	hash := sha256.Sum256(result.Output)
	outputHash := encodinghex.EncodeToString(hash[:])
	updatedAttempt, err := scanRuntimeAttempt(tx.QueryRow(ctx, `UPDATE workflow.node_attempt SET status='succeeded',output_schema_version=$2,output_hash=$3,lease_owner=NULL,lease_until=NULL,ended_at=$4,heartbeat_at=$4 WHERE id=$1 RETURNING `+runtimeAttemptColumns, string(attempt.ID), result.OutputSchemaVersion, outputHash, now))
	if err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_ATTEMPT_COMPLETE_FAILED")
	}
	updatedNode, err := scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status='succeeded',output=$2,lease_owner=NULL,lease_until=NULL,next_attempt_at=NULL,completed_at=$3,updated_at=$3,version=version+1 WHERE id=$1 RETURNING `+runtimeNodeColumns, string(node.ID), result.Output, now))
	if err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_NODE_COMPLETE_FAILED")
	}
	nodes[node.ID] = updatedNode
	if run.PauseRequestedAt == nil && run.CancelRequestedAt == nil {
		if err := r.activateSuccessors(ctx, tx, run, nodes, updatedNode, now); err != nil {
			return application.DeliveryTransitionResult{}, err
		}
	}
	status, err := reduceRunStatus(run, nodes)
	if err != nil {
		return application.DeliveryTransitionResult{}, err
	}
	run, err = persistRunReduction(ctx, tx, run, status, now)
	if err != nil {
		return application.DeliveryTransitionResult{}, err
	}
	if err := insertRuntimeEvent(ctx, tx, run, updatedNode, updatedAttempt, "workflow.node.succeeded"); err != nil {
		return application.DeliveryTransitionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_COMPLETE_COMMIT_FAILED")
	}
	return application.DeliveryTransitionResult{Run: run, Node: updatedNode, Attempt: updatedAttempt}, nil
}

func (r *RuntimeRepository) failDelivery(ctx context.Context, tx pgx.Tx, run domain.Run, nodes map[foundation.ID]domain.NodeRun, node domain.NodeRun, attempt domain.NodeAttempt, failure domain.FailureEnvelope, now time.Time) (application.DeliveryTransitionResult, error) {
	if run.CancelRequestedAt != nil {
		return r.controlCheckpoint(ctx, tx, run, nodes, node, attempt, domain.NodeStatusCancelled, "WORKFLOW_CANCELLED", now)
	}
	if run.PauseRequestedAt != nil {
		return r.controlCheckpoint(ctx, tx, run, nodes, node, attempt, domain.NodeStatusPaused, "WORKFLOW_PAUSED", now)
	}
	if failure.Class == domain.FailureClassRetryable {
		policy, err := retryPolicyForNode(ctx, tx, run.DefinitionID, node.NodeKey)
		if err != nil {
			return application.DeliveryTransitionResult{}, err
		}
		schedule, err := domain.CalculateRetry(policy, domain.RetryIdentity{RunID: run.ID, NodeKey: node.NodeKey}, node.RetryNo, failure.RetryAfter)
		if err != nil {
			return application.DeliveryTransitionResult{}, err
		}
		if !schedule.Exhausted {
			dispatchNo := node.DispatchNo + 1
			nextAt := now.Add(schedule.Delay)
			updatedAttempt, updateErr := scanRuntimeAttempt(tx.QueryRow(ctx, `UPDATE workflow.node_attempt SET status='retry_scheduled',failure_class=$2,error_kind=$3,error_code=$4,error_summary=$5,next_attempt_at=$6,lease_owner=NULL,lease_until=NULL,ended_at=$7,heartbeat_at=$7 WHERE id=$1 RETURNING `+runtimeAttemptColumns, string(attempt.ID), string(failure.Class), string(failure.ErrorKind), failure.Code, failure.Summary, nextAt, now))
			if updateErr != nil {
				return application.DeliveryTransitionResult{}, classify(updateErr, "WORKFLOW_ATTEMPT_RETRY_FAILED")
			}
			updatedNode, updateErr := scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status='retry_wait',retry_no=$2,dispatch_no=$3,next_attempt_at=$4,failure_class=$5,error_kind=$6,error_code=$7,error_summary=$8,lease_owner=NULL,lease_until=NULL,updated_at=$9,version=version+1 WHERE id=$1 RETURNING `+runtimeNodeColumns, string(node.ID), schedule.NextRetryNo, dispatchNo, nextAt, string(failure.Class), string(failure.ErrorKind), failure.Code, failure.Summary, now))
			if updateErr != nil {
				return application.DeliveryTransitionResult{}, classify(updateErr, "WORKFLOW_NODE_RETRY_FAILED")
			}
			nodes[node.ID] = updatedNode
			if err := insertRuntimeEvent(ctx, tx, run, updatedNode, updatedAttempt, "workflow.node.retry_scheduled"); err != nil {
				return application.DeliveryTransitionResult{}, err
			}
			args, argsErr := riveradapter.NewNodeJobArgs(updatedNode.ID, dispatchNo)
			if argsErr != nil {
				return application.DeliveryTransitionResult{}, argsErr
			}
			if _, insertErr := r.jobs.InsertTx(ctx, tx, args, riveradapter.InsertOptions{ScheduledAt: nextAt}); insertErr != nil {
				return application.DeliveryTransitionResult{}, insertErr
			}
			status, reduceErr := reduceRunStatus(run, nodes)
			if reduceErr != nil {
				return application.DeliveryTransitionResult{}, reduceErr
			}
			run, reduceErr = persistRunReduction(ctx, tx, run, status, now)
			if reduceErr != nil {
				return application.DeliveryTransitionResult{}, reduceErr
			}
			if err := tx.Commit(ctx); err != nil {
				return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_RETRY_COMMIT_FAILED")
			}
			return application.DeliveryTransitionResult{Run: run, Node: updatedNode, Attempt: updatedAttempt}, nil
		}
		failure = domain.FailureEnvelope{Class: domain.FailureClassNonRetryable, ErrorKind: foundation.ErrorNonRetryableFailure, Code: "WORKFLOW_RETRY_EXHAUSTED", Summary: "retry limit exhausted"}
	}
	attemptStatus := domain.AttemptStatusFailed
	nodeStatus := domain.NodeStatusFailed
	if failure.Class == domain.FailureClassLeaseLost {
		attemptStatus = domain.AttemptStatusLeaseLost
	}
	if failure.Class == domain.FailureClassManualRecovery {
		attemptStatus = domain.AttemptStatusManualRecovery
	}
	if failure.Class == domain.FailureClassCancelled {
		attemptStatus = domain.AttemptStatusCancelled
		nodeStatus = domain.NodeStatusCancelled
	}
	updatedAttempt, err := scanRuntimeAttempt(tx.QueryRow(ctx, `UPDATE workflow.node_attempt SET status=$2,failure_class=$3,error_kind=$4,error_code=$5,error_summary=$6,lease_owner=NULL,lease_until=NULL,ended_at=$7,heartbeat_at=$7 WHERE id=$1 RETURNING `+runtimeAttemptColumns, string(attempt.ID), string(attemptStatus), string(failure.Class), string(failure.ErrorKind), failure.Code, failure.Summary, now))
	if err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_ATTEMPT_FAIL_FAILED")
	}
	updatedNode, err := scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status=$2,failure_class=$3,error_kind=$4,error_code=$5,error_summary=$6,lease_owner=NULL,lease_until=NULL,completed_at=$7,updated_at=$7,version=version+1 WHERE id=$1 RETURNING `+runtimeNodeColumns, string(node.ID), string(nodeStatus), string(failure.Class), string(failure.ErrorKind), failure.Code, failure.Summary, now))
	if err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_NODE_FAIL_FAILED")
	}
	nodes[node.ID] = updatedNode
	status, err := reduceRunStatus(run, nodes)
	if err != nil {
		return application.DeliveryTransitionResult{}, err
	}
	run, err = persistRunReduction(ctx, tx, run, status, now)
	if err != nil {
		return application.DeliveryTransitionResult{}, err
	}
	if err := insertRuntimeEvent(ctx, tx, run, updatedNode, updatedAttempt, "workflow.node.failed"); err != nil {
		return application.DeliveryTransitionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_FAIL_COMMIT_FAILED")
	}
	return application.DeliveryTransitionResult{Run: run, Node: updatedNode, Attempt: updatedAttempt}, nil
}

func retryPolicyForNode(ctx context.Context, tx pgx.Tx, definitionID foundation.ID, nodeKey string) (domain.RetryPolicy, error) {
	var graph []byte
	if err := tx.QueryRow(ctx, `SELECT graph FROM workflow.definition WHERE id=$1`, string(definitionID)).Scan(&graph); err != nil {
		return domain.RetryPolicy{}, classify(err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	var canonical domain.CanonicalGraph
	if err := json.Unmarshal(graph, &canonical); err != nil {
		return domain.RetryPolicy{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_GRAPH_INVALID", false, err)
	}
	for _, node := range canonical.Nodes {
		if node.Key == nodeKey {
			return node.RetryPolicy, nil
		}
	}
	return domain.RetryPolicy{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_DEFINITION_NODE_NOT_FOUND", false, errors.New("node definition not found"))
}

func (r *RuntimeRepository) activateSuccessors(ctx context.Context, tx pgx.Tx, run domain.Run, nodes map[foundation.ID]domain.NodeRun, completed domain.NodeRun, now time.Time) error {
	if run.PauseRequestedAt != nil || run.CancelRequestedAt != nil {
		return nil
	}
	var graphJSON []byte
	if err := tx.QueryRow(ctx, `SELECT graph FROM workflow.definition WHERE id=$1`, string(run.DefinitionID)).Scan(&graphJSON); err != nil {
		return classify(err, "WORKFLOW_DEFINITION_QUERY_FAILED")
	}
	var graph domain.CanonicalGraph
	if err := json.Unmarshal(graphJSON, &graph); err != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_DEFINITION_GRAPH_INVALID", false, err)
	}
	statusByKey := make(map[string]domain.NodeStatus)
	for _, node := range nodes {
		statusByKey[node.NodeKey] = node.Status
	}
	for _, successor := range graph.Nodes {
		depends := false
		for _, dependency := range successor.Dependencies {
			if dependency == completed.NodeKey {
				depends = true
				break
			}
		}
		if !depends {
			continue
		}
		allSucceeded := true
		for _, dependency := range successor.Dependencies {
			if statusByKey[dependency] != domain.NodeStatusSucceeded {
				allSucceeded = false
				break
			}
		}
		if !allSucceeded {
			continue
		}
		var existing domain.NodeRun
		var existingID string
		err := tx.QueryRow(ctx, `SELECT id::text FROM workflow.node_run WHERE run_id=$1 AND node_key=$2 FOR UPDATE`, string(run.ID), successor.Key).Scan(&existingID)
		if err == nil {
			existing, err = scanRuntimeNode(tx.QueryRow(ctx, `SELECT `+runtimeNodeColumns+` FROM workflow.node_run WHERE id=$1`, existingID))
			if err != nil {
				return classify(err, "WORKFLOW_NODE_QUERY_FAILED")
			}
			if existing.Status != domain.NodeStatusPending || existing.DispatchNo < 1 {
				continue
			}
			args, argsErr := riveradapter.NewNodeJobArgs(existing.ID, existing.DispatchNo)
			if argsErr != nil {
				return argsErr
			}
			if _, insertErr := r.jobs.InsertTx(ctx, tx, args, riveradapter.InsertOptions{}); insertErr != nil {
				return insertErr
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return classify(err, "WORKFLOW_NODE_QUERY_FAILED")
		}
		id, idErr := foundation.NewUUIDGenerator(nil).New()
		if idErr != nil {
			return idErr
		}
		input := completed.Output
		idempotency := "node:" + string(run.ID) + ":" + successor.Key
		created, createErr := scanRuntimeNode(tx.QueryRow(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,version,created_at,updated_at,idempotency_key,input_schema_version,output_schema_version,dispatch_no,retry_no)
VALUES($1,$2,$3,$4,'pending',0,$5,1,$6,$6,$7,$8,$9,1,0) RETURNING `+runtimeNodeColumns, string(id), string(run.ID), successor.Key, successor.Kind, input, now, idempotency, successor.InputSchemaVersion, successor.OutputSchemaVersion))
		if createErr != nil {
			return classify(createErr, "WORKFLOW_NODE_CREATE_FAILED")
		}
		nodes[created.ID] = created
		statusByKey[created.NodeKey] = created.Status
		args, argsErr := riveradapter.NewNodeJobArgs(created.ID, created.DispatchNo)
		if argsErr != nil {
			return argsErr
		}
		if _, insertErr := r.jobs.InsertTx(ctx, tx, args, riveradapter.InsertOptions{}); insertErr != nil {
			return insertErr
		}
	}
	return nil
}

func reduceRunStatus(run domain.Run, nodes map[foundation.ID]domain.NodeRun) (domain.RunStatus, error) {
	if domain.IsTerminalRunStatus(run.Status) {
		return run.Status, nil
	}
	allSucceeded := len(nodes) > 0
	hasFailed, hasCancelled, hasWaiting, hasRetry, hasRunning, hasPending := false, false, false, false, false, false
	for _, node := range nodes {
		switch node.Status {
		case domain.NodeStatusSucceeded:
		case domain.NodeStatusFailed:
			hasFailed = true
			allSucceeded = false
		case domain.NodeStatusCancelled:
			hasCancelled = true
			allSucceeded = false
		case domain.NodeStatusWaitingForHuman:
			hasWaiting = true
			allSucceeded = false
		case domain.NodeStatusRetryWait:
			hasRetry = true
			allSucceeded = false
		case domain.NodeStatusRunning:
			hasRunning = true
			allSucceeded = false
		case domain.NodeStatusPending, domain.NodeStatusPaused:
			hasPending = true
			allSucceeded = false
		default:
			return domain.RunStatusFailed, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_NODE_STATUS_INVALID", false, errors.New("unknown node status"))
		}
	}
	if allSucceeded {
		return domain.RunStatusSucceeded, nil
	}
	if hasCancelled && !hasRunning {
		return domain.RunStatusCancelled, nil
	}
	if hasFailed && !hasRunning && !hasWaiting && !hasRetry && !hasPending {
		return domain.RunStatusFailed, nil
	}
	if run.PauseRequestedAt != nil && !hasRunning {
		return domain.RunStatusPaused, nil
	}
	if hasWaiting && !hasRunning && !hasPending {
		return domain.RunStatusWaitingForHuman, nil
	}
	if hasRetry && !hasRunning && !hasPending {
		return domain.RunStatusRetryWait, nil
	}
	return domain.RunStatusRunning, nil
}

func persistRunReduction(ctx context.Context, tx pgx.Tx, run domain.Run, status domain.RunStatus, now time.Time) (domain.Run, error) {
	if status == run.Status {
		return run, nil
	}
	completedAt := any(nil)
	if domain.IsTerminalRunStatus(status) {
		completedAt = now
	}
	updated, err := scanRuntimeRun(tx.QueryRow(ctx, `UPDATE workflow.run SET status=$2,completed_at=$3,pause_requested_at=CASE WHEN $3::timestamptz IS NOT NULL THEN NULL ELSE pause_requested_at END,cancel_requested_at=CASE WHEN $3::timestamptz IS NOT NULL THEN NULL ELSE cancel_requested_at END,updated_at=$4,version=version+1 WHERE id=$1 RETURNING `+runtimeRunColumns, string(run.ID), string(status), completedAt, now))
	if err != nil {
		return domain.Run{}, classify(err, "WORKFLOW_RUN_REDUCE_FAILED")
	}
	return updated, nil
}

// controlCheckpoint safely ends a running delivery after a persisted pause or
// cancel request wins the Run lock. The executor result is deliberately not
// applied, so no post-control side effect or successor generation is created.
func (r *RuntimeRepository) controlCheckpoint(ctx context.Context, tx pgx.Tx, run domain.Run, nodes map[foundation.ID]domain.NodeRun, node domain.NodeRun, attempt domain.NodeAttempt, nodeStatus domain.NodeStatus, code string, now time.Time) (application.DeliveryTransitionResult, error) {
	updatedAttempt, err := scanRuntimeAttempt(tx.QueryRow(ctx, `UPDATE workflow.node_attempt SET status='cancelled',failure_class='cancelled',error_kind=$2,error_code=$3,error_summary=$4,lease_owner=NULL,lease_until=NULL,ended_at=$5,heartbeat_at=$5 WHERE id=$1 RETURNING `+runtimeAttemptColumns, string(attempt.ID), string(foundation.ErrorVersionConflict), code, code, now))
	if err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_CONTROL_CHECKPOINT_FAILED")
	}
	nodeCompletedAt := any(nil)
	if nodeStatus == domain.NodeStatusCancelled {
		nodeCompletedAt = now
	}
	updatedNode, err := scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status=$2,lease_owner=NULL,lease_until=NULL,next_attempt_at=NULL,failure_class=NULL,error_kind=NULL,error_code=$3,error_summary=$4,completed_at=$5,updated_at=$6,version=version+1 WHERE id=$1 RETURNING `+runtimeNodeColumns, string(node.ID), string(nodeStatus), code, code, nodeCompletedAt, now))
	if err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_CONTROL_CHECKPOINT_FAILED")
	}
	nodes[node.ID] = updatedNode
	completedAt := any(nil)
	status := run.Status
	if !hasRunningNode(nodes) {
		status = domain.RunStatusPaused
	}
	if nodeStatus == domain.NodeStatusCancelled && !hasRunningNode(nodes) {
		completedAt = now
		status = domain.RunStatusCancelled
	}
	updatedRun, err := scanRuntimeRun(tx.QueryRow(ctx, `UPDATE workflow.run SET status=$2,completed_at=$3,pause_requested_at=CASE WHEN $2='cancelled' THEN NULL ELSE pause_requested_at END,updated_at=$4,version=version+1 WHERE id=$1 RETURNING `+runtimeRunColumns, string(run.ID), string(status), completedAt, now))
	if err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_CONTROL_CHECKPOINT_FAILED")
	}
	if err := insertRuntimeEvent(ctx, tx, updatedRun, updatedNode, updatedAttempt, "workflow.node."+strings.ToLower(code)); err != nil {
		return application.DeliveryTransitionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return application.DeliveryTransitionResult{}, classify(err, "WORKFLOW_CONTROL_CHECKPOINT_COMMIT_FAILED")
	}
	return application.DeliveryTransitionResult{Run: updatedRun, Node: updatedNode, Attempt: updatedAttempt}, nil
}

func insertRuntimeEvent(ctx context.Context, tx pgx.Tx, run domain.Run, node domain.NodeRun, attempt domain.NodeAttempt, eventType string) error {
	eventKey := eventType + ":" + string(node.ID) + ":" + strconv.Itoa(attempt.AttemptNo)
	eventID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		return err
	}
	occurredAt := attempt.StartedAt
	if attempt.EndedAt != nil {
		occurredAt = *attempt.EndedAt
	} else if !attempt.HeartbeatAt.IsZero() {
		occurredAt = attempt.HeartbeatAt
	}
	_, err = tx.Exec(ctx, `INSERT INTO workflow.outbox_event(id,workspace_id,run_id,event_type,idempotency_key,event_key,schema_version,event_version,payload,occurred_at)
VALUES($1,$2,$3,$4,$5,$5,$6,1,'{}'::jsonb,$7)
ON CONFLICT(workspace_id,event_key,event_version) WHERE event_key IS NOT NULL DO NOTHING`, string(eventID), string(run.WorkspaceID), string(run.ID), eventType, eventKey, runtimeEventSchemaVersion, occurredAt)
	if err != nil {
		return classify(err, "WORKFLOW_OUTBOX_CREATE_FAILED")
	}
	return nil
}

func pauseNode(ctx context.Context, tx pgx.Tx, node domain.NodeRun, now time.Time) (domain.NodeRun, error) {
	return scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status='paused',next_attempt_at=NULL,updated_at=$2,version=version+1 WHERE id=$1 RETURNING `+runtimeNodeColumns, string(node.ID), now))
}

func cancelNode(ctx context.Context, tx pgx.Tx, node domain.NodeRun, now time.Time) (domain.NodeRun, error) {
	return scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status='cancelled',completed_at=$2,updated_at=$2,version=version+1,lease_owner=NULL,lease_until=NULL WHERE id=$1 RETURNING `+runtimeNodeColumns, string(node.ID), now))
}

func resumeNodes(ctx context.Context, tx pgx.Tx, jobs riveradapter.JobInserter, run domain.Run, nodes map[foundation.ID]domain.NodeRun, now time.Time) error {
	ids := make([]foundation.ID, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		node := nodes[id]
		if node.Status != domain.NodeStatusPaused && node.Status != domain.NodeStatusPending && node.Status != domain.NodeStatusRetryWait {
			continue
		}
		status := domain.NodeStatusPending
		dispatch := node.DispatchNo + 1
		updated, err := scanRuntimeNode(tx.QueryRow(ctx, `UPDATE workflow.node_run SET status=$2,dispatch_no=$3,next_attempt_at=NULL,updated_at=$4,version=version+1 WHERE id=$1 RETURNING `+runtimeNodeColumns, string(node.ID), string(status), dispatch, now))
		if err != nil {
			return classify(err, "WORKFLOW_NODE_RESUME_FAILED")
		}
		nodes[id] = updated
		args, err := riveradapter.NewNodeJobArgs(updated.ID, updated.DispatchNo)
		if err != nil {
			return err
		}
		if _, err := jobs.InsertTx(ctx, tx, args, riveradapter.InsertOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func hasRunningNode(nodes map[foundation.ID]domain.NodeRun) bool {
	for _, node := range nodes {
		if node.Status == domain.NodeStatusRunning {
			return true
		}
	}
	return false
}

// Keep imported types tied to the public ports even when a build only uses the
// legacy Repository methods.
var _ application.RuntimeStatePort = (*RuntimeRepository)(nil)
var _ application.RuntimeHumanStatePort = (*RuntimeRepository)(nil)
