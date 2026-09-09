package domain

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const (
	WorkspaceAnalysisOperationNodeDecideNext    WorkspaceAnalysisOperationNodeKey    = "decide_next"
	WorkspaceAnalysisOperationDecision          WorkspaceAnalysisOperationKind       = "DECISION"
	WorkspaceAnalysisOperationResultDecision    WorkspaceAnalysisOperationResultKind = "DECISION_RECEIPT"
	WorkspaceAnalysisDecisionSchemaID                                                = "agent.workspace-analysis-decision"
	WorkspaceAnalysisDecisionSchemaVersion                                           = "1"
	ResultTypeWorkspaceAnalysisDecision                                              = "workspace_analysis_decision"
	ErrorCodeWorkspaceAnalysisDecisionInvalid                                        = "AGENT_WORKSPACE_ANALYSIS_DECISION_INVALID"
	WorkspaceAnalysisDecisionGitStatus                                               = "git_status"
	WorkspaceAnalysisDecisionKnowledgeSearch                                         = "knowledge_search"
	WorkspaceAnalysisDecisionSourceRead                                              = "source_read"
	WorkspaceAnalysisDecisionCitationValidation                                      = "citation_validation"
	WorkspaceAnalysisDecisionFinish                                                  = "finish"
)

// WorkspaceAnalysisDecision is the complete model-controlled part of one step.
// It deliberately contains no object identities, permissions, paths or reasoning.
type WorkspaceAnalysisDecision struct {
	Action       string   `json:"action"`
	Query        *string  `json:"query"`
	EvidenceRef  *string  `json:"evidence_ref"`
	EvidenceRefs []string `json:"evidence_refs"`
}

func (decision WorkspaceAnalysisDecision) Validate() error {
	valid := false
	switch decision.Action {
	case WorkspaceAnalysisDecisionGitStatus, WorkspaceAnalysisDecisionFinish:
		valid = decision.Query == nil && decision.EvidenceRef == nil && decision.EvidenceRefs == nil
	case WorkspaceAnalysisDecisionKnowledgeSearch:
		valid = decision.Query != nil && decision.EvidenceRef == nil && decision.EvidenceRefs == nil && utf8.ValidString(*decision.Query) &&
			strings.TrimSpace(*decision.Query) == *decision.Query && len(*decision.Query) > 0 && len(*decision.Query) <= 1024 &&
			!strings.ContainsRune(*decision.Query, 0)
	case WorkspaceAnalysisDecisionSourceRead:
		valid = decision.Query == nil && decision.EvidenceRef != nil && decision.EvidenceRefs == nil && ValidWorkspaceAnalysisV2EvidenceRef(*decision.EvidenceRef)
	case WorkspaceAnalysisDecisionCitationValidation:
		valid = decision.Query == nil && decision.EvidenceRef == nil && len(decision.EvidenceRefs) > 0 && len(decision.EvidenceRefs) <= WorkspaceAnalysisV2MaxSourceReads
		seen := make(map[string]bool, len(decision.EvidenceRefs))
		for _, ref := range decision.EvidenceRefs {
			if !ValidWorkspaceAnalysisV2EvidenceRef(ref) || seen[ref] {
				valid = false
			}
			seen[ref] = true
		}
	}
	if !valid {
		return invalid(ErrorCodeWorkspaceAnalysisDecisionInvalid, "workspace analysis decision is outside the frozen read-only catalog")
	}
	return nil
}

// Canonical returns the exact private receipt bytes; callers must never log them.
func (decision WorkspaceAnalysisDecision) Canonical() ([]byte, error) {
	if err := decision.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(decision)
}

// DecodeWorkspaceAnalysisDecision rejects duplicate, unknown and absent union keys.
func DecodeWorkspaceAnalysisDecision(raw []byte) (WorkspaceAnalysisDecision, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes, limits.MaxStringBytes, limits.MaxObjectFields, limits.MaxArrayItems = 4096, 1024, 4, WorkspaceAnalysisV2MaxSourceReads
	value, err := foundationstrictjson.DecodeObject[WorkspaceAnalysisDecision](raw, limits, nil)
	if err != nil {
		return WorkspaceAnalysisDecision{}, invalid(ErrorCodeWorkspaceAnalysisDecisionInvalid, "workspace analysis decision document is invalid")
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || len(keys) != 4 || keys["action"] == nil || keys["query"] == nil || keys["evidence_ref"] == nil || keys["evidence_refs"] == nil {
		return WorkspaceAnalysisDecision{}, invalid(ErrorCodeWorkspaceAnalysisDecisionInvalid, "workspace analysis decision document is incomplete")
	}
	if err := value.Validate(); err != nil {
		return WorkspaceAnalysisDecision{}, err
	}
	return value, nil
}

// ValidWorkspaceAnalysisV2EvidenceRef accepts only the run-global E1 through E32 labels.
func ValidWorkspaceAnalysisV2EvidenceRef(value string) bool {
	if len(value) < 2 || len(value) > 3 || value[0] != 'E' {
		return false
	}
	number, err := strconv.Atoi(value[1:])
	return err == nil && number >= 1 && number <= WorkspaceAnalysisV2MaxEvidenceRefs && value == "E"+strconv.Itoa(number)
}

func (WorkspaceAnalysisDecision) String() string     { return "WorkspaceAnalysisDecision{redacted}" }
func (d WorkspaceAnalysisDecision) GoString() string { return d.String() }
func (WorkspaceAnalysisDecision) LogValue() slog.Value {
	return slog.StringValue("workspace_analysis_decision:redacted")
}

// WorkspaceAnalysisDecisionReceipt binds one accepted model decision to one actual
// ModelCall. A loop ModelRun may own several of these immutable receipts.
type WorkspaceAnalysisDecisionReceipt struct {
	ID            foundation.ID
	WorkspaceID   foundation.ID
	AnalysisRunID foundation.ID
	OperationID   foundation.ID
	NodeAttemptID foundation.ID
	ModelRunID    foundation.ID
	ModelCallID   foundation.ID
	Ordinal       int
	Decision      WorkspaceAnalysisDecision `json:"-"`
	DocumentHash  string
	DocumentBytes int64
	CreatedAt     time.Time
}

func (receipt WorkspaceAnalysisDecisionReceipt) Validate() error {
	document, err := receipt.Decision.Canonical()
	if err != nil || !canonicalUniqueIDs([]foundation.ID{receipt.ID, receipt.WorkspaceID, receipt.AnalysisRunID, receipt.OperationID, receipt.NodeAttemptID, receipt.ModelRunID, receipt.ModelCallID}, true) ||
		receipt.Ordinal < 1 || receipt.Ordinal > WorkspaceAnalysisV2MaxDecisions || receipt.CreatedAt.IsZero() ||
		receipt.DocumentBytes != int64(len(document)) || receipt.DocumentHash != workspaceAnalysisDocumentHash(document) {
		return invalid(ErrorCodeWorkspaceAnalysisDecisionInvalid, "workspace analysis decision receipt binding is invalid")
	}
	return nil
}

func (receipt WorkspaceAnalysisDecisionReceipt) String() string {
	return fmt.Sprintf("WorkspaceAnalysisDecisionReceipt{id:%s ordinal:%d hash:%s}", receipt.ID, receipt.Ordinal, receipt.DocumentHash)
}
func (receipt WorkspaceAnalysisDecisionReceipt) GoString() string { return receipt.String() }
func (receipt WorkspaceAnalysisDecisionReceipt) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", string(receipt.ID)), slog.Int("ordinal", receipt.Ordinal), slog.String("hash", receipt.DocumentHash))
}

func workspaceAnalysisV2LoopOperationContract(node WorkspaceAnalysisOperationNodeKey, kind WorkspaceAnalysisOperationKind, ordinal int) (WorkspaceAnalysisOperationContract, bool) {
	if node != WorkspaceAnalysisOperationNodeDecideNext || ordinal < 1 || ordinal > WorkspaceAnalysisV2MaxDecisions+1 {
		return WorkspaceAnalysisOperationContract{}, false
	}
	contract := WorkspaceAnalysisOperationContract{NodeKey: node, Kind: kind, Ordinal: ordinal, CallKind: WorkspaceAnalysisOperationCallTool, ResultKind: WorkspaceAnalysisOperationResultToolReceipt}
	switch kind {
	case WorkspaceAnalysisOperationDecision:
		contract.CallKind, contract.ResultKind = WorkspaceAnalysisOperationCallModel, WorkspaceAnalysisOperationResultDecision
	case WorkspaceAnalysisOperationGitStatus, WorkspaceAnalysisOperationKnowledgeSearch, WorkspaceAnalysisOperationSourceRead, WorkspaceAnalysisOperationCitationValidation:
		if ordinal > WorkspaceAnalysisV2MaxDecisions {
			return WorkspaceAnalysisOperationContract{}, false
		}
	default:
		return WorkspaceAnalysisOperationContract{}, false
	}
	return contract, true
}
