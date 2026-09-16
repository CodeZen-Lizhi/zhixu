package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const anchorRecommendationRuntimeVersion = "v1"

func anchorRecommendationPromptRef() agentdomain.PromptRef {
	return agentdomain.PromptRef{ID: "organizing.anchor-recommendation", Version: anchorRecommendationRuntimeVersion}
}

func anchorRecommendationSchemaRef() agentdomain.SchemaRef {
	return agentdomain.SchemaRef{ID: agentdomain.AnchorRecommendationSchemaID, Version: anchorRecommendationRuntimeVersion}
}

// AnchorRecommendationModelVerifier 在创建提案的同一事务中核验已记录的提供方结果；没有匹配的冻结请求时，不信任调用方提供的模型运行 ID。
type AnchorRecommendationModelVerifier struct {
	runs agentapp.ScopedModelRunFinalizer
}

var _ app.AnchorRecommendationVerifier = (*AnchorRecommendationModelVerifier)(nil)

func NewAnchorRecommendationModelVerifier(runs agentapp.ScopedModelRunFinalizer) (*AnchorRecommendationModelVerifier, error) {
	if isNilInterface(runs) {
		return nil, app.AnchorInvalid()
	}
	return &AnchorRecommendationModelVerifier{runs: runs}, nil
}

func (verifier *AnchorRecommendationModelVerifier) VerifyAnchorRecommendationScoped(ctx context.Context, scope foundation.TransactionScope, anchor domain.Anchor, recommendation app.RecordAnchorRecommendation) error {
	if verifier == nil || isNilInterface(verifier.runs) || !validID(recommendation.RequestID) || !validID(recommendation.ModelRunID) || !validHash(recommendation.ModelOutputHash) || len(recommendation.ModelOutput) == 0 {
		return app.AnchorInvalid()
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	request, err := loadAnchorRecommendationRequest(tx, recommendation.WorkspaceID, recommendation.RequestID, true)
	if err != nil {
		return err
	}
	if request.Status != string(domain.AnchorRecommendationRunning) || request.AnchorID == nil || foundation.ID(*request.AnchorID) != anchor.ID ||
		request.NoteID != string(anchor.NoteID) || request.BasisRevisionID != string(anchor.BasisRevisionID) || request.ExpectedScopeVersion == nil || *request.ExpectedScopeVersion != anchor.ScopeVersion ||
		recommendation.AnchorID != anchor.ID || recommendation.ExpectedScopeVersion != anchor.ScopeVersion {
		return app.AnchorConflict()
	}
	if request.WorkflowRunID == nil || request.NodeRunID == nil || request.NodeAttemptID == nil || request.ModelInputHash == nil {
		return app.AnchorConflict()
	}
	recorded, err := verifier.runs.GetModelRunRecordScoped(ctx, scope, recommendation.WorkspaceID, recommendation.ModelRunID, true)
	if err != nil {
		return err
	}
	run := recorded.Run
	if agentdomain.ValidateModelRun(run) != nil || run.Status != agentdomain.ModelRunSucceeded || run.FinalResultType != agentdomain.ResultTypeAnchorRecommendation ||
		run.WorkflowRunID != foundation.ID(*request.WorkflowRunID) || run.NodeRunID != foundation.ID(*request.NodeRunID) || run.NodeAttemptID != foundation.ID(*request.NodeAttemptID) ||
		run.Prompt != anchorRecommendationPromptRef() || run.Schema != anchorRecommendationSchemaRef() || run.ReducedSchema != anchorRecommendationSchemaRef() ||
		run.Retrieval.IsBound() || run.MemoryContext.IsBound() || len(recorded.Calls) < 1 || len(recorded.Calls) > agentapp.StructuredCallLimit {
		return app.AnchorConflict()
	}
	phases := []agentdomain.ModelCallPhase{agentdomain.ModelCallInitial, agentdomain.ModelCallRepair, agentdomain.ModelCallReduced}
	for index, call := range recorded.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != run.ID || call.CallNo != index+1 || call.Phase != phases[index] || call.Status != agentdomain.ModelCallSucceeded ||
			call.Model != run.Model || call.Profile != run.Profile || call.Prompt != run.Prompt || call.Schema != run.Schema {
			return app.AnchorConflict()
		}
	}
	last := recorded.Calls[len(recorded.Calls)-1]
	if recorded.Calls[0].RequestHash != *request.ModelInputHash || last.ResponseHash != recommendation.ModelOutputHash || last.ResponseBytes != int64(len(recommendation.ModelOutput)) || sha256Hex(recommendation.ModelOutput) != recommendation.ModelOutputHash {
		return app.AnchorConflict()
	}
	output, err := app.DecodeAnchorModelOutput(recommendation.ModelOutput)
	if err != nil || output.NoRecommendation || output.Recommendation == nil || output.Recommendation.Title != anchor.Title || output.Recommendation.Kind != recommendation.Kind || output.Recommendation.Reason != recommendation.Reason || !reflect.DeepEqual(output.Recommendation.Scope, recommendation.Suggested) {
		return app.AnchorConflict()
	}
	var frozen []domain.SynthesisSourceRef
	if json.Unmarshal(request.Evidence, &frozen) != nil || len(output.Recommendation.Evidence) != len(recommendation.Evidence) {
		return app.AnchorConflict()
	}
	for i, label := range output.Recommendation.Evidence {
		index, ok := app.AnchorEvidenceIndex(label, len(frozen))
		if !ok || !reflect.DeepEqual(frozen[index], recommendation.Evidence[i]) {
			return app.AnchorConflict()
		}
	}
	return nil
}

// VerifyAnchorRecommendationCompletionScoped 证明不创建提案的终态结果：初始范围草稿等待用户确认，或明确的无推荐结果。
func (verifier *AnchorRecommendationModelVerifier) VerifyAnchorRecommendationCompletionScoped(ctx context.Context, scope foundation.TransactionScope, request anchorRecommendationRequestModel, command app.CompleteAnchorRecommendationCommand) error {
	if verifier == nil || isNilInterface(verifier.runs) || request.Status != string(domain.AnchorRecommendationRunning) || !validID(command.ModelRunID) || !validHash(command.ModelOutputHash) || len(command.ModelOutput) == 0 || request.ModelInputHash == nil || request.WorkflowRunID == nil || request.NodeRunID == nil || request.NodeAttemptID == nil {
		return app.AnchorInvalid()
	}
	recorded, err := verifier.runs.GetModelRunRecordScoped(ctx, scope, foundation.ID(request.WorkspaceID), command.ModelRunID, true)
	if err != nil {
		return err
	}
	run := recorded.Run
	if agentdomain.ValidateModelRun(run) != nil || run.Status != agentdomain.ModelRunSucceeded || run.FinalResultType != agentdomain.ResultTypeAnchorRecommendation || run.WorkflowRunID != foundation.ID(*request.WorkflowRunID) || run.NodeRunID != foundation.ID(*request.NodeRunID) || run.NodeAttemptID != foundation.ID(*request.NodeAttemptID) || run.Prompt != anchorRecommendationPromptRef() || run.Schema != anchorRecommendationSchemaRef() || run.ReducedSchema != anchorRecommendationSchemaRef() || run.Retrieval.IsBound() || run.MemoryContext.IsBound() || len(recorded.Calls) < 1 || len(recorded.Calls) > agentapp.StructuredCallLimit {
		return app.AnchorConflict()
	}
	for index, call := range recorded.Calls {
		phase := []agentdomain.ModelCallPhase{agentdomain.ModelCallInitial, agentdomain.ModelCallRepair, agentdomain.ModelCallReduced}[index]
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != run.ID || call.CallNo != index+1 || call.Phase != phase || call.Status != agentdomain.ModelCallSucceeded || call.Model != run.Model || call.Profile != run.Profile || call.Prompt != run.Prompt || call.Schema != run.Schema {
			return app.AnchorConflict()
		}
	}
	last := recorded.Calls[len(recorded.Calls)-1]
	if recorded.Calls[0].RequestHash != *request.ModelInputHash || last.ResponseHash != command.ModelOutputHash || last.ResponseBytes != int64(len(command.ModelOutput)) || sha256Hex(command.ModelOutput) != command.ModelOutputHash {
		return app.AnchorConflict()
	}
	output, err := app.DecodeAnchorModelOutput(command.ModelOutput)
	if err != nil {
		return app.AnchorConflict()
	}
	if output.NoRecommendation {
		return nil
	}
	if request.Kind != app.AnchorInitialScopeRecommendation || output.Recommendation == nil || output.Recommendation.Kind != app.AnchorInitialScopeRecommendation {
		return app.AnchorConflict()
	}
	var evidence []domain.SynthesisSourceRef
	if json.Unmarshal(request.Evidence, &evidence) != nil {
		return app.AnchorConflict()
	}
	for _, label := range output.Recommendation.Evidence {
		if _, ok := app.AnchorEvidenceIndex(label, len(evidence)); !ok {
			return app.AnchorConflict()
		}
	}
	return nil
}

func sha256Hex(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func loadAnchorRecommendationRequest(tx *gorm.DB, workspaceID, requestID foundation.ID, lock bool) (anchorRecommendationRequestModel, error) {
	var row anchorRecommendationRequestModel
	query := tx.Where("workspace_id=? AND id=?", string(workspaceID), string(requestID))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&row).Error; err != nil {
		if gormNoRows(err) {
			return row, anchorNotFound()
		}
		return row, err
	}
	return row, nil
}

func anchorRecommendationEvidenceEqual(raw organizingJSONB, evidence []domain.SynthesisSourceRef) bool {
	var frozen []domain.SynthesisSourceRef
	if jsonErr := json.Unmarshal(raw, &frozen); jsonErr != nil || len(frozen) != len(evidence) {
		return false
	}
	return reflect.DeepEqual(frozen, evidence)
}
