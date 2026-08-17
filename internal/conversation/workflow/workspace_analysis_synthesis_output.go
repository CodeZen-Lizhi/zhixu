package workflow

import (
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const workspaceAnalysisMaxSynthesisOutputBytes = 4 * 1024

// WorkspaceAnalysisSynthesisOutput 是 synthesize_answer@1 的可恢复、安全持久输出。
// 候选正文与 Draft chunks 仍由各自权威表拥有，不进入 Workflow Node output。
type WorkspaceAnalysisSynthesisOutput struct {
	SchemaVersion        int           `json:"schema_version"`
	CandidateID          foundation.ID `json:"candidate_id"`
	CandidateHash        string        `json:"candidate_hash"`
	SynthesisModelRunID  foundation.ID `json:"synthesis_model_run_id"`
	SynthesisModelCallID foundation.ID `json:"synthesis_model_call_id"`
	DraftSessionID       foundation.ID `json:"draft_session_id"`
	DraftGeneration      int64         `json:"draft_generation"`
}

type persistedWorkspaceAnalysisSynthesisOutput struct {
	SchemaVersion        *int           `json:"schema_version"`
	CandidateID          *foundation.ID `json:"candidate_id"`
	CandidateHash        *string        `json:"candidate_hash"`
	SynthesisModelRunID  *foundation.ID `json:"synthesis_model_run_id"`
	SynthesisModelCallID *foundation.ID `json:"synthesis_model_call_id"`
	DraftSessionID       *foundation.ID `json:"draft_session_id"`
	DraftGeneration      *int64         `json:"draft_generation"`
}

// EncodeWorkspaceAnalysisSynthesisOutput 校验并编码稳定的 synthesize_answer@1 文档。
func EncodeWorkspaceAnalysisSynthesisOutput(output WorkspaceAnalysisSynthesisOutput) (json.RawMessage, error) {
	if err := validateWorkspaceAnalysisSynthesisOutput(output); err != nil {
		return nil, err
	}
	document, err := json.Marshal(output)
	if err != nil || len(document) > workspaceAnalysisMaxSynthesisOutputBytes {
		return nil, workspaceAnalysisOutputError(err)
	}
	return document, nil
}

// DecodeWorkspaceAnalysisSynthesisOutput 严格拒绝 unknown、duplicate、null、trailing 和越界文档。
func DecodeWorkspaceAnalysisSynthesisOutput(raw json.RawMessage) (WorkspaceAnalysisSynthesisOutput, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = workspaceAnalysisMaxSynthesisOutputBytes
	limits.MaxDepth = 2
	limits.MaxStringBytes = 128
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 7
	persisted, err := foundationstrictjson.DecodeObject[persistedWorkspaceAnalysisSynthesisOutput](raw, limits, nil)
	if err != nil || persisted.SchemaVersion == nil || persisted.CandidateID == nil || persisted.CandidateHash == nil ||
		persisted.SynthesisModelRunID == nil || persisted.SynthesisModelCallID == nil || persisted.DraftSessionID == nil ||
		persisted.DraftGeneration == nil {
		return WorkspaceAnalysisSynthesisOutput{}, workspaceAnalysisOutputError(err)
	}
	output := WorkspaceAnalysisSynthesisOutput{
		SchemaVersion: *persisted.SchemaVersion, CandidateID: *persisted.CandidateID,
		CandidateHash: *persisted.CandidateHash, SynthesisModelRunID: *persisted.SynthesisModelRunID,
		SynthesisModelCallID: *persisted.SynthesisModelCallID, DraftSessionID: *persisted.DraftSessionID,
		DraftGeneration: *persisted.DraftGeneration,
	}
	if err := validateWorkspaceAnalysisSynthesisOutput(output); err != nil {
		return WorkspaceAnalysisSynthesisOutput{}, err
	}
	return output, nil
}

// String 不输出 Candidate、Model 或 Draft 身份和 hash。
func (output WorkspaceAnalysisSynthesisOutput) String() string {
	return "WorkspaceAnalysisSynthesisOutput{redacted}"
}

// GoString 避免 %#v 展开持久身份。
func (output WorkspaceAnalysisSynthesisOutput) GoString() string { return output.String() }

// LogValue 只投影固定版本与 Draft generation，不记录任何身份或 hash。
func (output WorkspaceAnalysisSynthesisOutput) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("schema_version", output.SchemaVersion),
		slog.Int64("draft_generation", output.DraftGeneration),
	)
}

func validateWorkspaceAnalysisSynthesisOutput(output WorkspaceAnalysisSynthesisOutput) error {
	if output.SchemaVersion != WorkspaceAnalysisOutputSchemaVersion || !validHash(output.CandidateHash) ||
		output.DraftGeneration < 1 {
		return workspaceAnalysisOutputError(errors.New("workspace analysis synthesis output version, hash, or generation is invalid"))
	}
	ids := []foundation.ID{
		output.CandidateID, output.SynthesisModelRunID, output.SynthesisModelCallID, output.DraftSessionID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !validWorkspaceAnalysisOutputID(id) {
			return workspaceAnalysisOutputError(errors.New("workspace analysis synthesis output identity is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return workspaceAnalysisOutputError(errors.New("workspace analysis synthesis output identity is reused"))
		}
		seen[id] = struct{}{}
	}
	return nil
}
