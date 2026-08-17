package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/jackc/pgx/v5"
)

const (
	workspaceAnalysisToolRefusalAuditAction = "workspace_analysis.tool_refused"
	workspaceAnalysisToolRefusalActorRef    = "workspace-analysis@1"
)

var workspaceAnalysisToolRefusalCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

var workspaceAnalysisToolRefusalCodes = map[string]struct{}{
	"TOOL_ALLOWED_VERSION_AMBIGUOUS":          {},
	"TOOL_INVOCATION_DENIED":                  {},
	"TOOL_WORKFLOW_BINDING_DENIED":            {},
	"TOOL_NOT_ALLOWED":                        {},
	"TOOL_PERMISSION_DENIED":                  {},
	"TOOL_INPUT_TOO_LARGE":                    {},
	"TOOL_INPUT_INVALID":                      {},
	"TOOL_IDEMPOTENCY_REQUIRED":               {},
	"TOOL_IDEMPOTENCY_UNEXPECTED":             {},
	"TOOL_WORKSPACE_ANALYSIS_CONTRACT_DENIED": {},
}

type workspaceAnalysisToolRefusalAuditRecorder interface {
	RecordTx(context.Context, any, auditdomain.Event) (auditdomain.Event, bool, error)
}

type workspaceAnalysisToolRefusalFact struct {
	id, workspaceID, analysisRunID, workflowRunID, nodeRunID, auditEventID foundation.ID
	nodeKey, operationKind, errorCode                                      string
	ordinal                                                                int
	createdAt                                                              time.Time
	fenceValid                                                             bool
}

// RecordWorkspaceAnalysisToolRefusal commits a deterministic pre-executor
// refusal and its append-only audit event together. This boundary never writes
// workflow.tool_call, workspace_analysis_operation, or a budget reservation.
func (repository *Repository) RecordWorkspaceAnalysisToolRefusal(
	ctx context.Context,
	command toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand,
) (toolsapplication.WorkspaceAnalysisToolRefusalResult, error) {
	if err := validateWorkspaceAnalysisToolRefusalCommand(command); err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, err
	}
	if repository == nil || isNilWorkspaceAnalysisToolRefusalAuditRecorder(repository.audit) {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, foundation.NewError(
			foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, errors.New("workspace analysis tool refusal audit is unavailable"),
		)
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockWorkspaceAnalysisToolRefusal(ctx, tx, command)
	if err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, err
	}
	if err := validateWorkspaceAnalysisToolRefusalFence(locked, command); err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, err
	}
	if locked.id != "" {
		if locked.errorCode != command.ErrorCode {
			return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, idempotencyConflict(errors.New("workspace analysis tool refusal reason differs"))
		}
		if err := tx.Commit(ctx); err != nil {
			return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classify(err)
		}
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{
			RefusalID: locked.id, ErrorCode: locked.errorCode, Replayed: true,
		}, nil
	}

	var operationID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM agent.workspace_analysis_operation
		WHERE analysis_run_id=$1 AND node_key=$2 AND operation_kind=$3 AND ordinal=$4 FOR UPDATE`,
		string(command.OperationKey.AnalysisRunID), string(command.OperationKey.NodeKey), string(command.OperationKey.Kind), command.OperationKey.Ordinal,
	).Scan(&operationID)
	if err == nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, idempotencyConflict(errors.New("workspace analysis tool slot already has an execution operation"))
	}
	if !noRows(err) {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classify(err)
	}

	auditEventID, err := workspaceAnalysisToolRefusalAuditEventID(command.OperationKey)
	if err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, consistency(err)
	}
	created, err := insertWorkspaceAnalysisToolRefusal(ctx, tx, command, auditEventID)
	if err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, err
	}
	auditEvent, err := newWorkspaceAnalysisToolRefusalAuditEvent(command, created.createdAt)
	if err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, consistency(err)
	}
	if _, replayed, err := repository.audit.RecordTx(ctx, tx, auditEvent); err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, err
	} else if replayed {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, consistency(errors.New("new workspace analysis tool refusal reused audit event"))
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return repository.recoverWorkspaceAnalysisToolRefusalAfterCommitError(ctx, command, auditEvent.ID, err)
	}
	return toolsapplication.WorkspaceAnalysisToolRefusalResult{RefusalID: created.id, ErrorCode: created.errorCode}, nil
}

func validateWorkspaceAnalysisToolRefusalCommand(command toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand) error {
	parsed, err := foundation.ParseID(string(command.RefusalID))
	if err != nil || parsed != command.RefusalID || command.Identity.Validate() != nil || command.OperationKey.Validate() != nil ||
		!workspaceAnalysisToolRefusalCodePattern.MatchString(command.ErrorCode) || !workspaceAnalysisToolRefusalCodeAllowed(command.ErrorCode) {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeWorkspaceAnalysisOperationInvalid, false, errors.New("workspace analysis tool refusal command is invalid"))
	}
	contract, err := domain.WorkspaceAnalysisOperationContractForKey(command.OperationKey)
	if err != nil || contract.CallKind != domain.WorkspaceAnalysisOperationCallTool || string(contract.NodeKey) != command.Identity.NodeKey {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeWorkspaceAnalysisOperationInvalid, false, errors.New("workspace analysis tool refusal slot is invalid"))
	}
	return nil
}

func workspaceAnalysisToolRefusalCodeAllowed(code string) bool {
	_, allowed := workspaceAnalysisToolRefusalCodes[code]
	return allowed
}

func lockWorkspaceAnalysisToolRefusal(
	ctx context.Context,
	tx pgx.Tx,
	command toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand,
) (workspaceAnalysisToolRefusalFact, error) {
	var fact workspaceAnalysisToolRefusalFact
	var definitionID string
	var workflowStatus string
	var cancelRequestedAt *time.Time
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT definition_id::text,status,cancel_requested_at,clock_timestamp()
		FROM workflow.run WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, string(command.Identity.WorkflowRunID), string(command.Identity.WorkspaceID),
	).Scan(&definitionID, &workflowStatus, &cancelRequestedAt, &databaseNow); err != nil {
		return fact, classifyReceiptLockError(err)
	}
	var nodeKey, nodeStatus string
	var nodeAttempt int64
	var nodeOwner *string
	var nodeLease *time.Time
	if err := tx.QueryRow(ctx, `SELECT node_key,status,attempt,lease_owner,lease_until
		FROM workflow.node_run WHERE id=$1 AND run_id=$2 FOR UPDATE`, string(command.Identity.NodeRunID), string(command.Identity.WorkflowRunID),
	).Scan(&nodeKey, &nodeStatus, &nodeAttempt, &nodeOwner, &nodeLease); err != nil {
		return fact, classifyReceiptLockError(err)
	}
	var attemptStatus string
	var attemptNo int64
	var attemptOwner *string
	var attemptLease *time.Time
	if err := tx.QueryRow(ctx, `SELECT status,attempt_no,lease_owner,lease_until
		FROM workflow.node_attempt WHERE id=$1 AND node_run_id=$2 FOR UPDATE`, string(command.Identity.NodeAttemptID), string(command.Identity.NodeRunID),
	).Scan(&attemptStatus, &attemptNo, &attemptOwner, &attemptLease); err != nil {
		return fact, classifyReceiptLockError(err)
	}
	var analysisRunID, analysisStatus, analysisHash, catalogHash string
	var analysisVersion int64
	var deadlineAt time.Time
	if err := tx.QueryRow(ctx, `SELECT id::text,status,definition_version,definition_hash,tool_catalog_hash,deadline_at
		FROM agent.workspace_analysis_run WHERE workspace_id=$1 AND workflow_run_id=$2 FOR UPDATE`, string(command.Identity.WorkspaceID), string(command.Identity.WorkflowRunID),
	).Scan(&analysisRunID, &analysisStatus, &analysisVersion, &analysisHash, &catalogHash, &deadlineAt); err != nil {
		return fact, classifyReceiptLockError(err)
	}
	fact.fenceValid = foundation.ID(analysisRunID) == command.OperationKey.AnalysisRunID && foundation.ID(definitionID) == command.Identity.DefinitionID &&
		workflowStatus == "running" && cancelRequestedAt == nil && (analysisStatus == "queued" || analysisStatus == "running") &&
		analysisVersion == command.Identity.DefinitionVersion && analysisHash == command.Identity.DefinitionHash && catalogHash == workspaceAnalysisCatalogHash() &&
		nodeKey == command.Identity.NodeKey && nodeStatus == "running" && attemptStatus == "running" &&
		nodeAttempt == command.Identity.LeaseFence && attemptNo == command.Identity.LeaseFence &&
		canonicalActiveLease(nodeOwner, nodeLease, attemptOwner, attemptLease, command.Identity.LeaseOwner, databaseNow) && deadlineAt.After(databaseNow)
	err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,analysis_run_id::text,workflow_run_id::text,node_run_id::text,
		node_key,operation_kind,ordinal,error_code,audit_event_id::text,created_at
		FROM agent.workspace_analysis_tool_refusal
		WHERE analysis_run_id=$1 AND node_key=$2 AND operation_kind=$3 AND ordinal=$4 FOR UPDATE`,
		string(command.OperationKey.AnalysisRunID), string(command.OperationKey.NodeKey), string(command.OperationKey.Kind), command.OperationKey.Ordinal,
	).Scan(&fact.id, &fact.workspaceID, &fact.analysisRunID, &fact.workflowRunID, &fact.nodeRunID,
		&fact.nodeKey, &fact.operationKind, &fact.ordinal, &fact.errorCode, &fact.auditEventID, &fact.createdAt)
	if noRows(err) {
		fact.workspaceID, fact.analysisRunID, fact.workflowRunID, fact.nodeRunID = command.Identity.WorkspaceID, command.OperationKey.AnalysisRunID, command.Identity.WorkflowRunID, command.Identity.NodeRunID
		fact.nodeKey, fact.operationKind, fact.ordinal, fact.createdAt = string(command.OperationKey.NodeKey), string(command.OperationKey.Kind), command.OperationKey.Ordinal, databaseNow.UTC().Truncate(time.Microsecond)
		return fact, nil
	}
	if err != nil {
		return fact, classify(err)
	}
	fact.createdAt = fact.createdAt.UTC().Truncate(time.Microsecond)
	return fact, nil
}

func validateWorkspaceAnalysisToolRefusalFence(fact workspaceAnalysisToolRefusalFact, command toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand) error {
	if fact.workspaceID != command.Identity.WorkspaceID || fact.analysisRunID != command.OperationKey.AnalysisRunID ||
		fact.workflowRunID != command.Identity.WorkflowRunID || fact.nodeRunID != command.Identity.NodeRunID ||
		fact.nodeKey != string(command.OperationKey.NodeKey) || fact.operationKind != string(command.OperationKey.Kind) || fact.ordinal != command.OperationKey.Ordinal {
		return consistency(errors.New("workspace analysis tool refusal binding differs"))
	}
	if fact.id == "" && !fact.fenceValid {
		return stale(errors.New("workspace analysis tool refusal fence is stale"))
	}
	return nil
}

func insertWorkspaceAnalysisToolRefusal(
	ctx context.Context,
	tx pgx.Tx,
	command toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand,
	auditEventID foundation.ID,
) (workspaceAnalysisToolRefusalFact, error) {
	var fact workspaceAnalysisToolRefusalFact
	err := tx.QueryRow(ctx, `INSERT INTO agent.workspace_analysis_tool_refusal(
		id,workspace_id,analysis_run_id,workflow_run_id,node_run_id,node_key,operation_kind,ordinal,error_code,audit_event_id,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,clock_timestamp())
	RETURNING id::text,workspace_id::text,analysis_run_id::text,workflow_run_id::text,node_run_id::text,node_key,operation_kind,ordinal,error_code,audit_event_id::text,created_at`,
		string(command.RefusalID), string(command.Identity.WorkspaceID), string(command.OperationKey.AnalysisRunID),
		string(command.Identity.WorkflowRunID), string(command.Identity.NodeRunID), string(command.OperationKey.NodeKey), string(command.OperationKey.Kind),
		command.OperationKey.Ordinal, command.ErrorCode, string(auditEventID),
	).Scan(&fact.id, &fact.workspaceID, &fact.analysisRunID, &fact.workflowRunID, &fact.nodeRunID,
		&fact.nodeKey, &fact.operationKind, &fact.ordinal, &fact.errorCode, &fact.auditEventID, &fact.createdAt)
	if err != nil {
		return workspaceAnalysisToolRefusalFact{}, classify(err)
	}
	fact.createdAt = fact.createdAt.UTC().Truncate(time.Microsecond)
	return fact, nil
}

func newWorkspaceAnalysisToolRefusalAuditEvent(
	command toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand,
	occurredAt time.Time,
) (auditdomain.Event, error) {
	eventID, err := workspaceAnalysisToolRefusalAuditEventID(command.OperationKey)
	if err != nil {
		return auditdomain.Event{}, err
	}
	workspaceID := command.Identity.WorkspaceID
	correlation, err := json.Marshal(map[string]any{
		"analysis_run_id": command.OperationKey.AnalysisRunID,
		"node_key":        command.OperationKey.NodeKey,
		"operation_kind":  command.OperationKey.Kind,
		"ordinal":         command.OperationKey.Ordinal,
		"workflow_run_id": command.Identity.WorkflowRunID,
	})
	if err != nil {
		return auditdomain.Event{}, err
	}
	metadata, err := json.Marshal(map[string]any{"reason_code": command.ErrorCode})
	if err != nil {
		return auditdomain.Event{}, err
	}
	return auditdomain.NewEvent(auditdomain.Event{
		ID: eventID, WorkspaceID: &workspaceID, ActorType: auditdomain.ActorAgent, ActorRef: workspaceAnalysisToolRefusalActorRef,
		Action: workspaceAnalysisToolRefusalAuditAction, ResourceType: "workspace_analysis_tool_refusal",
		ResourceRef: "workspace_analysis:" + string(command.OperationKey.AnalysisRunID) + ":" + string(command.OperationKey.Kind) + ":" + strconv.Itoa(command.OperationKey.Ordinal),
		Outcome:     auditdomain.OutcomeRejected, ErrorCode: command.ErrorCode,
		IdempotencyKey: "workspace-analysis:tool-refused:" + string(command.OperationKey.AnalysisRunID) + ":" + string(command.OperationKey.Kind) + ":" + strconv.Itoa(command.OperationKey.Ordinal) + ":v1",
		Correlation:    correlation, Metadata: metadata, SchemaVersion: auditdomain.SchemaVersion,
		OccurredAt: occurredAt.UTC().Truncate(time.Microsecond),
	})
}

func workspaceAnalysisToolRefusalAuditEventID(key domain.WorkspaceAnalysisOperationKey) (foundation.ID, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("workspace-analysis-tool-refusal-audit/v1\x00" + string(key.AnalysisRunID) + "\x00" + string(key.NodeKey) + "\x00" + string(key.Kind) + "\x00" + strconv.Itoa(key.Ordinal)))
	raw := digest[:16]
	raw[6] = raw[6]&0x0f | 0x50
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw)
	return foundation.ParseID(encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32])
}

func (repository *Repository) recoverWorkspaceAnalysisToolRefusalAfterCommitError(
	ctx context.Context,
	command toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand,
	auditEventID foundation.ID,
	commitErr error,
) (toolsapplication.WorkspaceAnalysisToolRefusalResult, error) {
	tx, err := repository.begin(ctx)
	if err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classify(commitErr)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var refusalID, code, persistedAuditID string
	err = tx.QueryRow(ctx, `SELECT id::text,error_code,audit_event_id::text FROM agent.workspace_analysis_tool_refusal
		WHERE analysis_run_id=$1 AND node_key=$2 AND operation_kind=$3 AND ordinal=$4`,
		string(command.OperationKey.AnalysisRunID), string(command.OperationKey.NodeKey), string(command.OperationKey.Kind), command.OperationKey.Ordinal,
	).Scan(&refusalID, &code, &persistedAuditID)
	if err != nil || code != command.ErrorCode || foundation.ID(persistedAuditID) != auditEventID {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classify(commitErr)
	}
	var found bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ops.audit_event WHERE id=$1)`, string(auditEventID)).Scan(&found); err != nil || !found {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classify(commitErr)
	}
	if err := tx.Commit(ctx); err != nil {
		return toolsapplication.WorkspaceAnalysisToolRefusalResult{}, classify(commitErr)
	}
	return toolsapplication.WorkspaceAnalysisToolRefusalResult{RefusalID: foundation.ID(refusalID), ErrorCode: code, Replayed: true}, nil
}

var _ toolsapplication.WorkspaceAnalysisToolRefusalRepository = (*Repository)(nil)
