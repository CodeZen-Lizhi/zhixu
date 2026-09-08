package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationredaction "github.com/CodeZen-Lizhi/zhixu/internal/foundation/redaction"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	workspaceAnalysisRunStartedAuditAction = "workspace_analysis.run_started"
	workspaceAnalysisTerminatedAuditAction = "workspace_analysis.terminated"
	workspaceAnalysisAuditResourceType     = "workspace_analysis_run"
	workspaceAnalysisAuditAgentRef         = "workspace-analysis@1"
)

type workspaceAnalysisAuditEventInput struct {
	WorkspaceID    foundation.ID
	AnalysisRunID  foundation.ID
	ActorType      auditdomain.ActorType
	ActorRef       string
	Action         string
	Outcome        auditdomain.Outcome
	ErrorCode      string
	IdempotencyKey string
	Correlation    json.RawMessage
	Metadata       json.RawMessage
	OccurredAt     time.Time
}

func newWorkspaceAnalysisAuditEvent(input workspaceAnalysisAuditEventInput) (auditdomain.Event, error) {
	eventID, err := workspaceAnalysisAuditEventID(input.Action, input.AnalysisRunID)
	if err != nil {
		return auditdomain.Event{}, err
	}
	workspaceID := input.WorkspaceID
	return auditdomain.NewEvent(auditdomain.Event{
		ID: eventID, WorkspaceID: &workspaceID, ActorType: input.ActorType, ActorRef: input.ActorRef,
		Action: input.Action, ResourceType: workspaceAnalysisAuditResourceType,
		ResourceRef: "workspace_analysis:" + string(input.AnalysisRunID), Outcome: input.Outcome,
		ErrorCode: input.ErrorCode, IdempotencyKey: input.IdempotencyKey,
		Correlation: input.Correlation, Metadata: input.Metadata, SchemaVersion: auditdomain.SchemaVersion,
		OccurredAt: input.OccurredAt.UTC().Truncate(time.Microsecond),
	})
}

func workspaceAnalysisTerminalAuditOutcome(
	status agentdomain.WorkspaceAnalysisRunStatus,
	reason agentdomain.WorkspaceAnalysisRunTerminationReason,
) (auditdomain.Outcome, string, error) {
	switch status {
	case agentdomain.WorkspaceAnalysisRunSucceeded:
		if reason == agentdomain.WorkspaceAnalysisRunCompleted {
			return auditdomain.OutcomeSucceeded, "", nil
		}
	case agentdomain.WorkspaceAnalysisRunRefused:
		switch reason {
		case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient, agentdomain.WorkspaceAnalysisRunCitationInvalid,
			agentdomain.WorkspaceAnalysisRunFaithfulnessRejected, agentdomain.WorkspaceAnalysisRunModelRefused:
			return auditdomain.OutcomeRejected, string(reason), nil
		}
	case agentdomain.WorkspaceAnalysisRunClarificationRequired:
		if reason == agentdomain.WorkspaceAnalysisRunNeedsClarification {
			return auditdomain.OutcomeRejected, string(reason), nil
		}
	case agentdomain.WorkspaceAnalysisRunFailed:
		switch reason {
		case agentdomain.WorkspaceAnalysisRunResultUnknown:
			return auditdomain.OutcomeUnknown, string(reason), nil
		case agentdomain.WorkspaceAnalysisRunBudgetExhausted, agentdomain.WorkspaceAnalysisRunReceiptInvalid,
			agentdomain.WorkspaceAnalysisRunDeadlineExceeded, agentdomain.WorkspaceAnalysisRunModelFailed,
			agentdomain.WorkspaceAnalysisRunToolFailed, agentdomain.WorkspaceAnalysisRunRuntimeFailed:
			return auditdomain.OutcomeFailed, string(reason), nil
		}
	case agentdomain.WorkspaceAnalysisRunCancelled:
		if reason == agentdomain.WorkspaceAnalysisRunCancellation {
			return auditdomain.OutcomeRejected, string(reason), nil
		}
	}
	return "", "", errors.New("workspace analysis terminal audit outcome is inconsistent")
}

func workspaceAnalysisAuditEventID(action string, analysisRunID foundation.ID) (foundation.ID, error) {
	if parsed, err := foundation.ParseID(string(analysisRunID)); err != nil || parsed != analysisRunID {
		return "", errors.New("workspace analysis audit run identity is invalid")
	}
	digest := sha256.Sum256([]byte("workspace-analysis-audit/v1\x00" + action + "\x00" + string(analysisRunID)))
	raw := digest[:16]
	raw[6] = raw[6]&0x0f | 0x50
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw)
	return foundation.ParseID(encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32])
}

func validWorkspaceAnalysisAuditActorRef(value string) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\r\n") || foundationredaction.ContainsSecret(value) ||
		foundationredaction.ContainsPII(value) || foundationredaction.ContainsAbsolutePath(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
