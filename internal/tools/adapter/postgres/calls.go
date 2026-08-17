package postgres

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/jackc/pgx/v5"
)

// RecordRefused 保存不占执行幂等键的 REFUSED 事实；同逻辑拒绝可安全重放。
func (repository *Repository) RecordRefused(ctx context.Context, command application.RecordRefusedCommand) (application.ToolCallMutationResult, error) {
	call, err := validateRefusedCommand(command)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := loadPersistedPolicy(ctx, tx, command.Identity); err != nil {
		return application.ToolCallMutationResult{}, err
	}
	created, err := scanToolCall(tx.QueryRow(ctx, `
		WITH database_time AS (SELECT clock_timestamp() AS value)
		INSERT INTO workflow.tool_call(
			id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
			requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
			output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
			idempotency_key,request_hash,request_bytes,request_summary,
			status,error_code,retryable,version,started_at,completed_at,duration_ms
		)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,
			NULL,$17,$18,$19,'REFUSED',$20,false,1,database_time.value,database_time.value,0
		FROM database_time
		ON CONFLICT DO NOTHING
		RETURNING `+toolCallColumns,
		string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID), call.CallNo,
		call.RequestedToolName, optionalToolVersion(call.Tool), optionalText(call.DefinitionHash), optionalSchemaID(call.InputSchema), optionalSchemaVersion(call.InputSchema),
		optionalSchemaID(call.OutputSchema), optionalSchemaVersion(call.OutputSchema), optionalText(string(call.Capability)), optionalText(string(call.SideEffectLevel)), optionalText(string(call.InvocationPolicy)),
		call.RequestHash, call.RequestBytes, call.RequestSummary, call.ErrorCode,
	))
	if err == nil {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return repository.recoverRefusedAfterCommitError(ctx, call, commitErr)
		}
		return application.ToolCallMutationResult{Call: created}, nil
	}
	if !noRows(err) {
		return application.ToolCallMutationResult{}, classify(err)
	}
	existing, err := loadCallByAttemptNumber(ctx, tx, call.NodeAttemptID, call.CallNo)
	if err != nil {
		if noRows(err) {
			return application.ToolCallMutationResult{}, idempotencyConflict(errors.New("refused call identity conflicts with existing record"))
		}
		return application.ToolCallMutationResult{}, classify(err)
	}
	if !sameTerminalResult(existing, call) {
		return application.ToolCallMutationResult{}, idempotencyConflict(errors.New("refused call replay binding differs"))
	}
	if err := tx.Commit(ctx); err != nil {
		return application.ToolCallMutationResult{}, classify(err)
	}
	return application.ToolCallMutationResult{Call: existing, Replayed: true}, nil
}

// StartCall 在同一事务复核持久 Workflow policy 并只向 CREATED 返回 Executor 资格。
func (repository *Repository) StartCall(ctx context.Context, command application.StartCallCommand) (application.StartCallResult, error) {
	call, err := validateStartCommand(command)
	if err != nil {
		return application.StartCallResult{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return application.StartCallResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	persisted, err := loadPersistedPolicy(ctx, tx, command.Identity)
	if err != nil {
		return application.StartCallResult{}, err
	}
	if err := validateStartPolicy(persisted.policy, command); err != nil {
		return application.StartCallResult{}, err
	}
	created, err := scanToolCall(tx.QueryRow(ctx, `
		INSERT INTO workflow.tool_call(
			id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
			requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
			output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
			idempotency_key,request_hash,request_bytes,request_summary,status,retryable,version,started_at
		)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,'STARTED',false,1,clock_timestamp())
		ON CONFLICT DO NOTHING
		RETURNING `+toolCallColumns,
		string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID), call.CallNo,
		call.RequestedToolName, call.Tool.Version, call.DefinitionHash, call.InputSchema.ID, call.InputSchema.Version,
		call.OutputSchema.ID, call.OutputSchema.Version, optionalText(string(call.Capability)), string(call.SideEffectLevel), string(call.InvocationPolicy),
		optionalText(call.IdempotencyKey), call.RequestHash, call.RequestBytes, call.RequestSummary,
	))
	if err == nil {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return repository.recoverStartedAfterCommitError(ctx, call, commitErr)
		}
		return application.StartCallResult{Call: created, Disposition: application.StartCallCreated}, nil
	}
	if !noRows(err) {
		return application.StartCallResult{}, classify(err)
	}
	existing, err := findReplayCandidate(ctx, tx, call)
	if err != nil {
		return application.StartCallResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return application.StartCallResult{}, classify(err)
	}
	return application.StartCallResult{Call: existing, Disposition: application.StartCallReplayed}, nil
}

// FinalizeCall 使用 version CAS 将 STARTED 归约为唯一成功或已证明失败终态。
func (repository *Repository) FinalizeCall(ctx context.Context, command application.FinalizeCallCommand) (application.ToolCallMutationResult, error) {
	call, err := validateTerminalCommand(command.ExpectedVersion, command.Call, false)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	return repository.finalize(ctx, command.ExpectedVersion, call)
}

// MarkUnknown 使用 version CAS 记录崩溃、超时或响应丢失后的未知副作用结果。
func (repository *Repository) MarkUnknown(ctx context.Context, command application.MarkUnknownCommand) (application.ToolCallMutationResult, error) {
	call, err := validateTerminalCommand(command.ExpectedVersion, command.Call, true)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	return repository.finalize(ctx, command.ExpectedVersion, call)
}

func (repository *Repository) finalize(ctx context.Context, expectedVersion int64, call domain.ToolCall) (application.ToolCallMutationResult, error) {
	tx, err := repository.begin(ctx)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := finalizeCallTx(ctx, tx, expectedVersion, call)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return repository.recoverTerminalAfterCommitError(ctx, call, commitErr)
	}
	return result, nil
}

func finalizeCallTx(ctx context.Context, tx pgx.Tx, expectedVersion int64, call domain.ToolCall) (application.ToolCallMutationResult, error) {
	updated, err := scanToolCall(tx.QueryRow(ctx, `
		UPDATE workflow.tool_call SET
			response_hash=$1,response_bytes=$2,response_summary=$3,result_ref=$4,
			side_effect_type=$5,side_effect_id=$6,status=$7,error_code=$8,retryable=$9,
			version=$10,completed_at=clock_timestamp(),
			duration_ms=GREATEST(0, floor(extract(epoch FROM (clock_timestamp()-started_at))*1000)::bigint)
		WHERE id=$11 AND workspace_id=$12 AND workflow_run_id=$13 AND node_run_id=$14 AND node_attempt_id=$15
		  AND call_no=$16 AND status='STARTED' AND version=$17
		  AND requested_tool_name=$18
		  AND tool_version IS NOT DISTINCT FROM $19
		  AND definition_hash IS NOT DISTINCT FROM $20
		  AND input_schema_id IS NOT DISTINCT FROM $21
		  AND input_schema_version IS NOT DISTINCT FROM $22
		  AND output_schema_id IS NOT DISTINCT FROM $23
		  AND output_schema_version IS NOT DISTINCT FROM $24
		  AND capability IS NOT DISTINCT FROM $25
		  AND side_effect_level IS NOT DISTINCT FROM $26
		  AND invocation_policy IS NOT DISTINCT FROM $27
		  AND idempotency_key IS NOT DISTINCT FROM $28
		  AND request_hash=$29 AND request_bytes=$30 AND request_summary=$31
		RETURNING `+toolCallColumns,
		optionalText(call.ResponseHash), call.ResponseBytes, optionalJSON(call.ResponseSummary), optionalText(call.ResultRef),
		optionalText(call.SideEffectType), optionalText(call.SideEffectID), string(call.Status), optionalText(call.ErrorCode), call.Retryable,
		call.Version, string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID),
		call.CallNo, expectedVersion, call.RequestedToolName, optionalToolVersion(call.Tool), optionalText(call.DefinitionHash),
		optionalSchemaID(call.InputSchema), optionalSchemaVersion(call.InputSchema), optionalSchemaID(call.OutputSchema), optionalSchemaVersion(call.OutputSchema),
		optionalText(string(call.Capability)), optionalText(string(call.SideEffectLevel)), optionalText(string(call.InvocationPolicy)), optionalText(call.IdempotencyKey),
		call.RequestHash, call.RequestBytes, call.RequestSummary,
	))
	if err == nil {
		return application.ToolCallMutationResult{Call: updated}, nil
	}
	if !noRows(err) {
		return application.ToolCallMutationResult{}, classify(err)
	}
	existing, err := loadCallByID(ctx, tx, call.WorkspaceID, call.ID)
	if noRows(err) {
		return application.ToolCallMutationResult{}, notFound(err)
	}
	if err != nil {
		return application.ToolCallMutationResult{}, classify(err)
	}
	if !sameTerminalResult(existing, call) {
		return application.ToolCallMutationResult{}, versionConflict(errors.New("tool call terminal result differs"))
	}
	return application.ToolCallMutationResult{Call: existing, Replayed: true}, nil
}

// RecoverStaleStarted 使用数据库可信时间与 Node→Attempt→Call 锁序恢复普通失租调用。
// 带 writeback_execution 预期 receipt 的 trusted write 由 Safe Writeback reconciler 处理，不能抢先归约 UNKNOWN。
func (repository *Repository) RecoverStaleStarted(ctx context.Context, limit int) ([]domain.ToolCall, error) {
	if limit < 1 || limit > application.MaxToolStaleRecoveryLimit {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("tool stale recovery limit is invalid"))
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	type staleCandidate struct {
		callID, workspaceID, runID, nodeRunID, attemptID string
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text,c.workspace_id::text,c.workflow_run_id::text,c.node_run_id::text,c.node_attempt_id::text
		FROM workflow.tool_call c
		JOIN workflow.run r ON r.id=c.workflow_run_id AND r.workspace_id=c.workspace_id
		JOIN workflow.node_run n ON n.id=c.node_run_id AND n.run_id=c.workflow_run_id
		JOIN workflow.node_attempt a ON a.id=c.node_attempt_id AND a.node_run_id=c.node_run_id
		WHERE c.status='STARTED' AND c.side_effect_type IS DISTINCT FROM 'writeback_execution' AND (
			r.status<>'running' OR n.status<>'running' OR a.status<>'running'
			OR n.attempt IS DISTINCT FROM a.attempt_no
			OR n.lease_owner IS DISTINCT FROM a.lease_owner
			OR n.lease_until IS NULL OR a.lease_until IS NULL
			OR n.lease_until IS DISTINCT FROM a.lease_until
			OR n.lease_until<=clock_timestamp()
		)
		ORDER BY c.started_at,c.call_no,c.id LIMIT $1`, limit)
	if err != nil {
		return nil, classify(err)
	}
	candidates := make([]staleCandidate, 0, limit)
	for rows.Next() {
		var candidate staleCandidate
		if scanErr := rows.Scan(&candidate.callID, &candidate.workspaceID, &candidate.runID, &candidate.nodeRunID, &candidate.attemptID); scanErr != nil {
			rows.Close()
			return nil, classifyScan(scanErr)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, classify(err)
	}
	rows.Close()

	result := make([]domain.ToolCall, 0, len(candidates))
	for _, candidate := range candidates {
		var runStatus, nodeStatus string
		var nodeAttempt int64
		var nodeOwner *string
		var nodeLease *time.Time
		var databaseNow time.Time
		err = tx.QueryRow(ctx, `SELECT r.status,n.status,n.attempt,n.lease_owner,n.lease_until,clock_timestamp()
			FROM workflow.node_run n
			JOIN workflow.run r ON r.id=n.run_id AND r.workspace_id=$2
			WHERE n.id=$1 AND n.run_id=$3
			FOR UPDATE OF n SKIP LOCKED`, candidate.nodeRunID, candidate.workspaceID, candidate.runID).
			Scan(&runStatus, &nodeStatus, &nodeAttempt, &nodeOwner, &nodeLease, &databaseNow)
		if noRows(err) {
			continue
		}
		if err != nil {
			return nil, classify(err)
		}

		var attemptStatus string
		var attemptNo int64
		var attemptOwner *string
		var attemptLease *time.Time
		err = tx.QueryRow(ctx, `SELECT status,attempt_no,lease_owner,lease_until
			FROM workflow.node_attempt
			WHERE id=$1 AND node_run_id=$2
			FOR UPDATE SKIP LOCKED`, candidate.attemptID, candidate.nodeRunID).
			Scan(&attemptStatus, &attemptNo, &attemptOwner, &attemptLease)
		if noRows(err) {
			continue
		}
		if err != nil {
			return nil, classify(err)
		}

		started, scanErr := scanToolCall(tx.QueryRow(ctx, `SELECT `+toolCallColumns+` FROM workflow.tool_call
			WHERE id=$1 AND workspace_id=$2 AND status='STARTED'
			  AND side_effect_type IS DISTINCT FROM 'writeback_execution'
			FOR UPDATE SKIP LOCKED`, candidate.callID, candidate.workspaceID))
		if noRows(scanErr) {
			continue
		}
		if scanErr != nil {
			return nil, classifyScan(scanErr)
		}
		if started.WorkflowRunID != foundation.ID(candidate.runID) || started.NodeRunID != foundation.ID(candidate.nodeRunID) || started.NodeAttemptID != foundation.ID(candidate.attemptID) {
			return nil, consistency(errors.New("stale recovery candidate binding changed"))
		}
		if !staleLockedExecution(runStatus, nodeStatus, attemptStatus, nodeAttempt, attemptNo, nodeOwner, attemptOwner, nodeLease, attemptLease, databaseNow) {
			continue
		}

		recovered, updateErr := scanToolCall(tx.QueryRow(ctx, `UPDATE workflow.tool_call SET
			status='UNKNOWN',error_code='TOOL_OUTCOME_UNKNOWN',retryable=false,
			version=version+1,completed_at=clock_timestamp(),
			duration_ms=GREATEST(0,floor(extract(epoch FROM (clock_timestamp()-started_at))*1000)::bigint)
			WHERE id=$1 AND workspace_id=$2 AND status='STARTED' AND version=$3
			  AND side_effect_type IS DISTINCT FROM 'writeback_execution'
			RETURNING `+toolCallColumns, candidate.callID, candidate.workspaceID, started.Version))
		if noRows(updateErr) {
			continue
		}
		if updateErr != nil {
			return nil, classifyScan(updateErr)
		}
		if recovered.Status != domain.CallUnknown || recovered.ErrorCode != "TOOL_OUTCOME_UNKNOWN" {
			return nil, consistency(errors.New("stale recovery update returned an invalid terminal call"))
		}
		result = append(result, recovered)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, classify(err)
	}
	sort.Slice(result, func(left, right int) bool { return timelineLess(result[left], result[right]) })
	return result, nil
}

func staleLockedExecution(
	runStatus, nodeStatus, attemptStatus string,
	nodeAttempt, attemptNo int64,
	nodeOwner, attemptOwner *string,
	nodeLease, attemptLease *time.Time,
	databaseNow time.Time,
) bool {
	if runStatus != "running" || nodeStatus != "running" || attemptStatus != "running" || nodeAttempt != attemptNo {
		return true
	}
	if nodeOwner == nil || attemptOwner == nil || *nodeOwner != *attemptOwner || nodeLease == nil || attemptLease == nil {
		return true
	}
	return !nodeLease.Equal(*attemptLease) || !nodeLease.After(databaseNow)
}

// ListTimeline 返回按 started_at、call_no、id 稳定排序且经过领域校验的受限时间线。
func (repository *Repository) ListTimeline(ctx context.Context, query application.ToolCallTimelineQuery) ([]domain.ToolCall, error) {
	if !validID(query.WorkspaceID) || !validID(query.WorkflowRunID) ||
		(query.NodeRunID != "" && !validID(query.NodeRunID)) || query.Limit < 1 || query.Limit > application.MaxToolCallTimelineLimit {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("tool call timeline query is invalid"))
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var rows pgx.Rows
	if query.NodeRunID == "" {
		rows, err = tx.Query(ctx, `SELECT `+toolCallColumns+` FROM workflow.tool_call
			WHERE workspace_id=$1 AND workflow_run_id=$2
			ORDER BY started_at,call_no,id LIMIT $3`, string(query.WorkspaceID), string(query.WorkflowRunID), query.Limit)
	} else {
		rows, err = tx.Query(ctx, `SELECT `+toolCallColumns+` FROM workflow.tool_call
			WHERE workspace_id=$1 AND workflow_run_id=$2 AND node_run_id=$3
			ORDER BY started_at,call_no,id LIMIT $4`, string(query.WorkspaceID), string(query.WorkflowRunID), string(query.NodeRunID), query.Limit)
	}
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	result := make([]domain.ToolCall, 0)
	var previous domain.ToolCall
	for rows.Next() {
		call, scanErr := scanToolCall(rows)
		if scanErr != nil {
			return nil, classifyScan(scanErr)
		}
		if len(result) > 0 && !timelineLess(previous, call) {
			return nil, consistency(errors.New("tool call timeline is not stably ordered"))
		}
		previous = call
		result = append(result, call)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, classify(err)
	}
	return result, nil
}

func validateRefusedCommand(command application.RecordRefusedCommand) (domain.ToolCall, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, err
	}
	call := command.Call
	if call.Status != domain.CallRefused || call.Version != 1 || call.IdempotencyKey != "" || call.Retryable {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("refused tool call must be version one without execution idempotency"))
	}
	now := time.Unix(1, 0).UTC()
	call.StartedAt, call.CompletedAt, call.DurationMillis = now, &now, 0
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, err
	}
	return call, nil
}

func validateStartCommand(command application.StartCallCommand) (domain.ToolCall, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, err
	}
	call := command.Call
	if call.Status != domain.CallStarted || call.Version != 1 || call.Tool == nil ||
		(call.InvocationPolicy == domain.InvocationModelRequestable &&
			(call.SideEffectLevel == domain.SideEffectDomainWrite || call.SideEffectLevel == domain.SideEffectUnknown)) ||
		((call.SideEffectLevel == domain.SideEffectDomainWrite || call.SideEffectLevel == domain.SideEffectUnknown) && call.IdempotencyKey == "") {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("started tool call policy is invalid"))
	}
	call.StartedAt, call.CompletedAt, call.DurationMillis = time.Unix(1, 0).UTC(), nil, 0
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, err
	}
	return call, nil
}

func validateTerminalCommand(expectedVersion int64, input domain.ToolCall, unknown bool) (domain.ToolCall, error) {
	if expectedVersion < 1 || input.Version != expectedVersion+1 ||
		(unknown && input.Status != domain.CallUnknown) || (!unknown && input.Status != domain.CallSucceeded && input.Status != domain.CallFailed) {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("tool call terminal command is invalid"))
	}
	call := input
	started := time.Unix(1, 0).UTC()
	completed := started.Add(time.Millisecond)
	call.StartedAt, call.CompletedAt, call.DurationMillis = started, &completed, 1
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, err
	}
	if err := domain.ValidateToolCallTransition(domain.CallStarted, call.Status); err != nil {
		return domain.ToolCall{}, err
	}
	return call, nil
}

func validateIdentityCallBinding(identity domain.TrustedExecutionIdentity, call domain.ToolCall) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if call.WorkspaceID != identity.WorkspaceID || call.WorkflowRunID != identity.WorkflowRunID ||
		call.NodeRunID != identity.NodeRunID || call.NodeAttemptID != identity.NodeAttemptID || call.CallNo < 1 || call.CallNo > math.MaxInt16 {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("tool call does not match trusted execution identity"))
	}
	return nil
}

func findReplayCandidate(ctx context.Context, tx pgx.Tx, call domain.ToolCall) (domain.ToolCall, error) {
	if call.IdempotencyKey != "" && (call.SideEffectLevel == domain.SideEffectDomainWrite || call.SideEffectLevel == domain.SideEffectUnknown) {
		existing, err := loadSideEffectCallByKey(ctx, tx, call.WorkspaceID, call.IdempotencyKey)
		if err == nil {
			if sameStartBinding(existing, call, false) {
				return existing, nil
			}
			return domain.ToolCall{}, idempotencyConflict(errors.New("side effect idempotency binding differs"))
		}
		if !noRows(err) {
			return domain.ToolCall{}, classify(err)
		}
	}
	existing, err := loadCallByAttemptNumber(ctx, tx, call.NodeAttemptID, call.CallNo)
	if err == nil {
		if sameStartBinding(existing, call, true) {
			return existing, nil
		}
		return domain.ToolCall{}, idempotencyConflict(errors.New("tool call number binding differs"))
	}
	if !noRows(err) {
		return domain.ToolCall{}, classify(err)
	}
	active, err := loadActiveCallByAttempt(ctx, tx, call.NodeAttemptID)
	if err == nil {
		return domain.ToolCall{}, idempotencyConflict(errors.New("node attempt already has an active tool call: " + string(active.Status)))
	}
	if !noRows(err) {
		return domain.ToolCall{}, classify(err)
	}
	byID, err := loadCallByID(ctx, tx, call.WorkspaceID, call.ID)
	if err == nil {
		if sameStartBinding(byID, call, true) {
			return byID, nil
		}
		return domain.ToolCall{}, idempotencyConflict(errors.New("tool call id binding differs"))
	}
	if !noRows(err) {
		return domain.ToolCall{}, classify(err)
	}
	return domain.ToolCall{}, idempotencyConflict(errors.New("tool call conflicts with an existing execution"))
}

func (repository *Repository) recoverStartedAfterCommitError(ctx context.Context, call domain.ToolCall, commitErr error) (application.StartCallResult, error) {
	tx, err := repository.begin(ctx)
	if err == nil {
		defer func() { _ = tx.Rollback(ctx) }()
		if existing, loadErr := findReplayCandidate(ctx, tx, call); loadErr == nil {
			_ = tx.Commit(ctx)
			return application.StartCallResult{Call: existing, Disposition: application.StartCallReplayed}, nil
		}
	}
	return application.StartCallResult{}, classify(commitErr)
}

func (repository *Repository) recoverRefusedAfterCommitError(ctx context.Context, call domain.ToolCall, commitErr error) (application.ToolCallMutationResult, error) {
	tx, err := repository.begin(ctx)
	if err == nil {
		defer func() { _ = tx.Rollback(ctx) }()
		if existing, loadErr := loadCallByAttemptNumber(ctx, tx, call.NodeAttemptID, call.CallNo); loadErr == nil && sameTerminalResult(existing, call) {
			_ = tx.Commit(ctx)
			return application.ToolCallMutationResult{Call: existing, Replayed: true}, nil
		}
	}
	return application.ToolCallMutationResult{}, classify(commitErr)
}

func (repository *Repository) recoverTerminalAfterCommitError(ctx context.Context, call domain.ToolCall, commitErr error) (application.ToolCallMutationResult, error) {
	tx, err := repository.begin(ctx)
	if err == nil {
		defer func() { _ = tx.Rollback(ctx) }()
		if existing, loadErr := loadCallByID(ctx, tx, call.WorkspaceID, call.ID); loadErr == nil && sameTerminalResult(existing, call) {
			_ = tx.Commit(ctx)
			return application.ToolCallMutationResult{Call: existing, Replayed: true}, nil
		}
	}
	return application.ToolCallMutationResult{}, classify(commitErr)
}

func timelineLess(left, right domain.ToolCall) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.Before(right.StartedAt)
	}
	if left.CallNo != right.CallNo {
		return left.CallNo < right.CallNo
	}
	return string(left.ID) < string(right.ID)
}

func classifyScan(err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	return classify(err)
}
