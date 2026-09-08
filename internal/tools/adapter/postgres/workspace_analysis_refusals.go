package postgres

import (
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
