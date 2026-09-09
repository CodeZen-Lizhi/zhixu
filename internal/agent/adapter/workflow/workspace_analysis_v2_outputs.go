package workflow

import (
	"encoding/json"
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

// Intermediate v2 outputs contain immutable proof references only. Each next
// node reloads their actual contents through the journal and receipt authorities.
type workspaceAnalysisLoopOutputV2 struct {
	SchemaVersion      int           `json:"schema_version"`
	FinishDecisionID   foundation.ID `json:"finish_decision_id"`
	FinishDecisionHash string        `json:"finish_decision_hash"`
	DecisionCount      int           `json:"decision_count"`
	ToolCount          int           `json:"tool_count"`
}

type workspaceAnalysisSynthesisOutputV2 struct {
	SchemaVersion        int           `json:"schema_version"`
	CandidateID          foundation.ID `json:"candidate_id"`
	CandidateHash        string        `json:"candidate_hash"`
	SynthesisModelRunID  foundation.ID `json:"synthesis_model_run_id"`
	SynthesisModelCallID foundation.ID `json:"synthesis_model_call_id"`
	DraftSessionID       foundation.ID `json:"draft_session_id"`
	DraftGeneration      int64         `json:"draft_generation"`
}

type workspaceAnalysisValidationOutputV2 struct {
	SchemaVersion int           `json:"schema_version"`
	CandidateID   foundation.ID `json:"candidate_id"`
	CandidateHash string        `json:"candidate_hash"`
	OperationID   foundation.ID `json:"validation_operation_id"`
	ToolCallID    foundation.ID `json:"validation_tool_call_id"`
	ReceiptID     foundation.ID `json:"validation_receipt_id"`
	ReceiptHash   string        `json:"validation_receipt_hash"`
}

func decodeWorkspaceAnalysisLoopOutputV2(raw []byte) (workspaceAnalysisLoopOutputV2, error) {
	return decodeWorkspaceAnalysisV2Output(raw, []string{"schema_version", "finish_decision_id", "finish_decision_hash", "decision_count", "tool_count"}, func(output workspaceAnalysisLoopOutputV2) bool {
		return output.SchemaVersion == 2 && validExecutionID(output.FinishDecisionID) && validWorkspaceAnalysisLowerHash(output.FinishDecisionHash) &&
			output.DecisionCount > 0 && output.DecisionCount <= agentdomain.WorkspaceAnalysisV2MaxDecisions && output.ToolCount == output.DecisionCount-1
	})
}

func decodeWorkspaceAnalysisSynthesisOutputV2(raw []byte) (workspaceAnalysisSynthesisOutputV2, error) {
	return decodeWorkspaceAnalysisV2Output(raw, []string{"schema_version", "candidate_id", "candidate_hash", "synthesis_model_run_id", "synthesis_model_call_id", "draft_session_id", "draft_generation"}, func(output workspaceAnalysisSynthesisOutputV2) bool {
		return output.SchemaVersion == 2 && validWorkspaceAnalysisV2DistinctIDs(output.CandidateID, output.SynthesisModelRunID, output.SynthesisModelCallID, output.DraftSessionID) &&
			validWorkspaceAnalysisLowerHash(output.CandidateHash) && output.DraftGeneration > 0
	})
}

func decodeWorkspaceAnalysisValidationOutputV2(raw []byte) (workspaceAnalysisValidationOutputV2, error) {
	return decodeWorkspaceAnalysisV2Output(raw, []string{"schema_version", "candidate_id", "candidate_hash", "validation_operation_id", "validation_tool_call_id", "validation_receipt_id", "validation_receipt_hash"}, func(output workspaceAnalysisValidationOutputV2) bool {
		return output.SchemaVersion == 2 && validWorkspaceAnalysisV2DistinctIDs(output.CandidateID, output.OperationID, output.ToolCallID, output.ReceiptID) &&
			validWorkspaceAnalysisLowerHash(output.CandidateHash) && validWorkspaceAnalysisLowerHash(output.ReceiptHash)
	})
}

func decodeWorkspaceAnalysisV2Output[T any](raw []byte, required []string, validate func(T) bool) (T, error) {
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes, limits.MaxStringBytes, limits.MaxObjectFields, limits.MaxArrayItems, limits.MaxDepth = 4096, 128, len(required), 0, 2
	value, err := strictjson.DecodeObject[T](raw, limits, nil)
	if err != nil {
		return value, workspaceAnalysisReceiptError(err)
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || len(keys) != len(required) {
		return value, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 output fields are invalid"))
	}
	for _, key := range required {
		if len(keys[key]) == 0 || string(keys[key]) == "null" {
			return value, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 output is incomplete"))
		}
	}
	if !validate(value) {
		return value, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 output binding is invalid"))
	}
	return value, nil
}

func validWorkspaceAnalysisV2DistinctIDs(ids ...foundation.ID) bool {
	seen := make(map[foundation.ID]bool, len(ids))
	for _, id := range ids {
		if !validExecutionID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func workspaceAnalysisV2DecisionToolKind(action string) agentdomain.WorkspaceAnalysisOperationKind {
	switch action {
	case agentdomain.WorkspaceAnalysisDecisionGitStatus:
		return agentdomain.WorkspaceAnalysisOperationGitStatus
	case agentdomain.WorkspaceAnalysisDecisionKnowledgeSearch:
		return agentdomain.WorkspaceAnalysisOperationKnowledgeSearch
	case agentdomain.WorkspaceAnalysisDecisionSourceRead:
		return agentdomain.WorkspaceAnalysisOperationSourceRead
	case agentdomain.WorkspaceAnalysisDecisionCitationValidation:
		return agentdomain.WorkspaceAnalysisOperationCitationValidation
	default:
		return ""
	}
}

func workspaceAnalysisV2Finish(journal agentapplication.WorkspaceAnalysisJournalSnapshot) (agentdomain.WorkspaceAnalysisDecisionReceipt, int, error) {
	var finish agentdomain.WorkspaceAnalysisDecisionReceipt
	var pending *agentdomain.WorkspaceAnalysisDecisionReceipt
	decisions, tools := 0, 0
	for _, entry := range journal.Entries {
		op := entry.Operation
		if op.NodeKey != agentdomain.WorkspaceAnalysisOperationNodeDecideNext {
			continue
		}
		if finish.ID != "" || agentdomain.ValidateWorkspaceAnalysisOperation(op) != nil || op.Status != agentdomain.WorkspaceAnalysisOperationSucceeded || op.Result == nil || op.Call == nil {
			return finish, 0, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 loop has no complete finish closure"))
		}
		if op.Kind == agentdomain.WorkspaceAnalysisOperationDecision {
			decisions++
			d := entry.Decision
			if pending != nil || d == nil || d.Validate() != nil || entry.ModelRun == nil || entry.ModelCall == nil ||
				agentdomain.ValidateModelRun(*entry.ModelRun) != nil || agentdomain.ValidateModelCall(*entry.ModelCall) != nil ||
				entry.ModelRun.WorkspaceID != journal.Run.WorkspaceID || entry.ModelRun.WorkflowRunID != journal.Run.WorkflowRunID ||
				d.NodeAttemptID != entry.ModelRun.NodeAttemptID || op.FirstNodeAttemptID == nil || *op.FirstNodeAttemptID != d.NodeAttemptID ||
				op.Ordinal != decisions || d.Ordinal != decisions || d.WorkspaceID != journal.Run.WorkspaceID || d.AnalysisRunID != journal.Run.ID ||
				d.OperationID != op.ID || op.Result.ID != d.ID || op.Result.Hash != d.DocumentHash || d.ModelRunID != entry.ModelRun.ID ||
				d.ModelCallID != entry.ModelCall.ID || entry.ModelCall.ID != op.Call.ID || entry.ModelCall.ModelRunID != entry.ModelRun.ID ||
				entry.ModelCall.Status != agentdomain.ModelCallSucceeded || entry.ModelCall.Phase != agentdomain.ModelCallAgent || entry.ModelCall.RequestHash != op.RequestHash ||
				entry.ModelCall.Schema != (agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisDecisionSchemaID, Version: agentdomain.WorkspaceAnalysisDecisionSchemaVersion}) ||
				entry.ModelCall.Schema != entry.ModelRun.Schema || entry.ModelCall.Model != entry.ModelRun.Model || entry.ModelCall.Profile != entry.ModelRun.Profile || entry.ModelCall.Prompt != entry.ModelRun.Prompt ||
				entry.ModelCall.ResponseHash != d.DocumentHash || entry.ModelCall.ResponseBytes != d.DocumentBytes {
				return finish, 0, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 decision journal binding drifted"))
			}
			if d.Decision.Action == agentdomain.WorkspaceAnalysisDecisionFinish {
				if entry.ModelRun.Status != agentdomain.ModelRunSucceeded || entry.ModelRun.FinalResultType != agentdomain.ResultTypeWorkspaceAnalysisDecision {
					return finish, 0, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 finish did not terminate its model run"))
				}
				finish = *d
			} else {
				pending = d
			}
		} else {
			if pending == nil || op.Ordinal != pending.Ordinal || op.Kind != workspaceAnalysisV2DecisionToolKind(pending.Decision.Action) ||
				op.Call.Kind != agentdomain.WorkspaceAnalysisOperationCallTool || entry.DecisionID == nil || *entry.DecisionID != pending.ID {
				return finish, 0, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 tool journal differs from its decision"))
			}
			pending = nil
			tools++
		}
	}
	if finish.ID == "" || pending != nil || decisions < 1 || decisions > agentdomain.WorkspaceAnalysisV2MaxDecisions || tools != decisions-1 {
		return finish, 0, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 loop did not finish"))
	}
	return finish, tools, nil
}

func validateWorkspaceAnalysisLoopOutputV2(output workspaceAnalysisLoopOutputV2, journal agentapplication.WorkspaceAnalysisJournalSnapshot) error {
	finish, toolCount, err := workspaceAnalysisV2Finish(journal)
	if err != nil {
		return err
	}
	if output.FinishDecisionID != finish.ID || output.FinishDecisionHash != finish.DocumentHash || output.DecisionCount != finish.Ordinal || output.ToolCount != toolCount {
		return workspaceAnalysisReceiptError(errors.New("workspace analysis v2 loop output differs from its finish receipt"))
	}
	return nil
}
