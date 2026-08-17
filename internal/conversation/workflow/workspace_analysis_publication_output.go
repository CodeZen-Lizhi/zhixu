package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
)

const workspaceAnalysisMaxPublicationOutputBytes = 4 * 1024

// WorkspaceAnalysisPublicationOutput 是 review_publish@1 的稳定 Answer 终态回执。
// 最终正文、Citation、预算和证明文档只能从各自权威事实重新加载。
type WorkspaceAnalysisPublicationOutput struct {
	SchemaVersion     int                                        `json:"schema_version"`
	AnswerID          foundation.ID                              `json:"answer_id"`
	PublicationStatus conversationdomain.AnswerPublicationStatus `json:"publication_status"`
	ResultType        conversationdomain.AnswerResultType        `json:"result_type"`
	ModelRunID        *foundation.ID                             `json:"model_run_id"`
	ResultHash        string                                     `json:"result_hash"`
	ProofID           foundation.ID                              `json:"proof_id"`
}

type persistedWorkspaceAnalysisPublicationOutput struct {
	SchemaVersion     *int                                        `json:"schema_version"`
	AnswerID          *foundation.ID                              `json:"answer_id"`
	PublicationStatus *conversationdomain.AnswerPublicationStatus `json:"publication_status"`
	ResultType        *conversationdomain.AnswerResultType        `json:"result_type"`
	ModelRunID        json.RawMessage                             `json:"model_run_id"`
	ResultHash        *string                                     `json:"result_hash"`
	ProofID           *foundation.ID                              `json:"proof_id"`
}

// EncodeWorkspaceAnalysisPublicationOutput 校验并编码不含公开正文或私有证据的终态回执。
func EncodeWorkspaceAnalysisPublicationOutput(output WorkspaceAnalysisPublicationOutput) (json.RawMessage, error) {
	if err := validateWorkspaceAnalysisPublicationOutput(output); err != nil {
		return nil, err
	}
	document, err := json.Marshal(output)
	if err != nil || len(document) > workspaceAnalysisMaxPublicationOutputBytes {
		return nil, workspaceAnalysisOutputError(err)
	}
	return document, nil
}

// DecodeWorkspaceAnalysisPublicationOutput 严格拒绝 unknown、duplicate、缺失 null、trailing 和越界文档。
func DecodeWorkspaceAnalysisPublicationOutput(raw json.RawMessage) (WorkspaceAnalysisPublicationOutput, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = workspaceAnalysisMaxPublicationOutputBytes
	limits.MaxDepth = 2
	limits.MaxStringBytes = 128
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 7
	persisted, err := foundationstrictjson.DecodeObject[persistedWorkspaceAnalysisPublicationOutput](raw, limits, nil)
	if err != nil || persisted.SchemaVersion == nil || persisted.AnswerID == nil || persisted.PublicationStatus == nil ||
		persisted.ResultType == nil || len(persisted.ModelRunID) == 0 || persisted.ResultHash == nil || persisted.ProofID == nil {
		return WorkspaceAnalysisPublicationOutput{}, workspaceAnalysisOutputError(err)
	}
	modelRunID, err := decodeWorkspaceAnalysisPublicationModelRun(persisted.ModelRunID)
	if err != nil {
		return WorkspaceAnalysisPublicationOutput{}, err
	}
	output := WorkspaceAnalysisPublicationOutput{
		SchemaVersion: *persisted.SchemaVersion, AnswerID: *persisted.AnswerID,
		PublicationStatus: *persisted.PublicationStatus, ResultType: *persisted.ResultType,
		ModelRunID: modelRunID, ResultHash: *persisted.ResultHash, ProofID: *persisted.ProofID,
	}
	if err := validateWorkspaceAnalysisPublicationOutput(output); err != nil {
		return WorkspaceAnalysisPublicationOutput{}, err
	}
	return output, nil
}

// String 不输出 Answer、Model Run、proof 身份或结果哈希。
func (output WorkspaceAnalysisPublicationOutput) String() string {
	return "WorkspaceAnalysisPublicationOutput{status:" + string(output.PublicationStatus) + " result_type:" + string(output.ResultType) + "}"
}

// GoString 避免 %#v 展开持久身份。
func (output WorkspaceAnalysisPublicationOutput) GoString() string { return output.String() }

// LogValue 只投影稳定终态，不记录任何身份或 hash。
func (output WorkspaceAnalysisPublicationOutput) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("publication_status", string(output.PublicationStatus)),
		slog.String("result_type", string(output.ResultType)),
	)
}

func validateWorkspaceAnalysisPublicationOutput(output WorkspaceAnalysisPublicationOutput) error {
	if output.SchemaVersion != WorkspaceAnalysisOutputSchemaVersion || !validHash(output.ResultHash) ||
		!validWorkspaceAnalysisOutputID(output.AnswerID) || !validWorkspaceAnalysisOutputID(output.ProofID) ||
		output.AnswerID == output.ProofID {
		return workspaceAnalysisOutputError(errors.New("workspace analysis publication output binding is invalid"))
	}
	rule, err := conversationdomain.WorkspaceAnalysisRuleForPublication(output.PublicationStatus)
	if err != nil || rule.ResultType != output.ResultType {
		return workspaceAnalysisOutputError(errors.New("workspace analysis publication output terminal matrix is invalid"))
	}
	if output.ModelRunID != nil {
		if !validWorkspaceAnalysisOutputID(*output.ModelRunID) || *output.ModelRunID == output.AnswerID ||
			*output.ModelRunID == output.ProofID {
			return workspaceAnalysisOutputError(errors.New("workspace analysis publication model identity is invalid"))
		}
	} else if rule.ModelRunRequirement == conversationdomain.WorkspaceAnalysisModelRunRequired {
		return workspaceAnalysisOutputError(errors.New("workspace analysis publication requires an authoring model run"))
	}
	return nil
}

func decodeWorkspaceAnalysisPublicationModelRun(raw json.RawMessage) (*foundation.ID, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value foundation.ID
	if err := json.Unmarshal(raw, &value); err != nil || !validWorkspaceAnalysisOutputID(value) {
		return nil, workspaceAnalysisOutputError(errors.New("workspace analysis publication model identity is invalid"))
	}
	return &value, nil
}
