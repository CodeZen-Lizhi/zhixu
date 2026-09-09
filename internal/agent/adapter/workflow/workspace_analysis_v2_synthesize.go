package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func (executor *workspaceAnalysisV2NodeExecutor) synthesizeAnswer(ctx context.Context, state workspaceAnalysisV2Execution) (workflowapplication.ExecutionResult, error) {
	finish, err := executor.loadFinishedLoop(ctx, state, true)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	evidence, err := executor.loadEvidence(ctx, state)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if len(evidence.items) == 0 {
		published, err := finalizeWorkspaceAnalysisTerminationPublication(ctx, executor.dependencies.Finalizer, conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand{
			WorkspaceAnalysisPublicationLookup: state.lookup(), ExpectedAnswerVersion: state.question.Answer.Version, Reason: agentdomain.WorkspaceAnalysisRunEvidenceInsufficient,
			OperationID: &finish.OperationID, Artifact: &conversationapplication.WorkspaceAnalysisTerminationArtifact{
				Kind: conversationapplication.WorkspaceAnalysisTerminationArtifactDecisionReceipt, ID: finish.ID, Hash: finish.DocumentHash},
		})
		if err != nil {
			return workflowapplication.ExecutionResult{}, err
		}
		return workspaceAnalysisV2PublicationResult(state.root.AnswerID, published)
	}
	git, err := executor.loadGitModelOutput(ctx, state)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	input, err := json.Marshal(struct {
		workspaceAnalysisV2QuestionInput
		GitStatus json.RawMessage                           `json:"git_status"`
		Searches  []workspaceAnalysisSynthesisSearchInput   `json:"searches"`
		Evidence  []workspaceAnalysisSynthesisEvidenceInput `json:"evidence"`
	}{workspaceAnalysisV2QuestionInput: workspaceAnalysisV2Question(state.question), GitStatus: git, Searches: evidence.searches, Evidence: evidence.items})
	if err != nil || len(input) > agentapplication.MaxStructuredInputBytes {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis synthesis input exceeds its bound"))
	}
	request := agentapplication.WorkspaceAnalysisSynthesisRequest{Identity: workspaceAnalysisModelIdentity(state.execution), AnalysisRunID: state.run.ID, AnswerID: state.root.AnswerID,
		AttemptNo: state.execution.AttemptNo, ModelSettingsRevision: cloneWorkspaceAnalysisInt64(state.execution.ModelSettingsRevision), Retrieval: evidence.retrieval,
		ProfileRef: DefaultProfileRef(), PromptRef: WorkspaceAnalysisSynthesisPromptRefV2(), Input: input, AllowedEvidenceRefs: evidence.refs}
	result, err := executor.dependencies.Synthesis.Run(ctx, request)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisSynthesisResultV2(state, request, result); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	output, err := json.Marshal(workspaceAnalysisSynthesisOutputV2{SchemaVersion: 2, CandidateID: result.Candidate.ID, CandidateHash: result.Candidate.DocumentHash,
		SynthesisModelRunID: result.Run.ID, SynthesisModelCallID: result.Call.ID, DraftSessionID: result.Draft.ID, DraftGeneration: result.Draft.Generation})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if _, err := decodeWorkspaceAnalysisSynthesisOutputV2(output); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: output}, nil
}

func (executor *workspaceAnalysisV2NodeExecutor) loadFinishedLoop(ctx context.Context, state workspaceAnalysisV2Execution, predecessor bool) (agentdomain.WorkspaceAnalysisDecisionReceipt, error) {
	raw, err := executor.loadStage(ctx, state, conversationworkflow.WorkspaceAnalysisNodeDecideNext, predecessor)
	if err != nil {
		return agentdomain.WorkspaceAnalysisDecisionReceipt{}, err
	}
	output, err := decodeWorkspaceAnalysisLoopOutputV2(raw)
	if err != nil {
		return agentdomain.WorkspaceAnalysisDecisionReceipt{}, err
	}
	if err := validateWorkspaceAnalysisLoopOutputV2(output, state.journal); err != nil {
		return agentdomain.WorkspaceAnalysisDecisionReceipt{}, err
	}
	finish, _, err := workspaceAnalysisV2Finish(state.journal)
	return finish, err
}

type workspaceAnalysisEvidenceV2 struct {
	retrieval agentdomain.RetrievalRef
	refs      []string
	items     []workspaceAnalysisSynthesisEvidenceInput
	searches  []workspaceAnalysisSynthesisSearchInput
}

func (executor *workspaceAnalysisV2NodeExecutor) loadEvidence(ctx context.Context, state workspaceAnalysisV2Execution) (workspaceAnalysisEvidenceV2, error) {
	authority, err := executor.dependencies.Evidence.LoadWorkspaceAnalysisSynthesisEvidence(ctx, toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery{
		WorkspaceID: state.execution.WorkspaceID, WorkflowRunID: state.execution.RunID, AnalysisRunID: state.run.ID,
	})
	if err != nil {
		return workspaceAnalysisEvidenceV2{}, err
	}
	return validateWorkspaceAnalysisEvidenceV2(state, authority)
}

func validateWorkspaceAnalysisEvidenceV2(state workspaceAnalysisV2Execution, authority toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority) (workspaceAnalysisEvidenceV2, error) {
	result := workspaceAnalysisEvidenceV2{refs: []string{}, items: []workspaceAnalysisSynthesisEvidenceInput{}, searches: []workspaceAnalysisSynthesisSearchInput{}}
	count := len(authority.ReadSourceReceipts)
	if authority.WorkspaceID != state.execution.WorkspaceID || authority.WorkflowRunID != state.execution.RunID || authority.AnalysisRunID != state.run.ID ||
		count > agentdomain.WorkspaceAnalysisV2MaxSourceReads || len(authority.EvidenceRefs) != count || len(authority.Evidence) != count ||
		len(authority.SearchReceipts) > agentdomain.WorkspaceAnalysisV2MaxDecisions {
		return result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 evidence authority owner or count drifted"))
	}
	searches := map[foundation.ID]toolsdomain.ResultReceipt{}
	for _, receipt := range authority.SearchReceipts {
		identities, err := toolsdomain.SearchKnowledgeV3ReceiptIdentities(receipt)
		if err != nil {
			return result, err
		}
		if _, found := searches[receipt.ID]; found || !workspaceAnalysisV2ReceiptInJournal(state, receipt, agentdomain.WorkspaceAnalysisOperationKnowledgeSearch) {
			return result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 search receipt has no exact journal binding"))
		}
		searches[receipt.ID] = receipt
		var projection struct {
			EffectiveMode string   `json:"effective_mode"`
			Degradations  []string `json:"degradations"`
		}
		if json.Unmarshal(receipt.Output, &projection) != nil {
			return result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 search summary is invalid"))
		}
		result.searches = append(result.searches, workspaceAnalysisSynthesisSearchInput{
			EffectiveMode: projection.EffectiveMode, HitCount: len(identities), DegradationCodes: append([]string{}, projection.Degradations...),
		})
	}
	previous := 0
	for index, receipt := range authority.ReadSourceReceipts {
		binding, err := toolsdomain.ReadSourceV4ReceiptBinding(receipt)
		if err != nil {
			return result, err
		}
		search, found := searches[binding.SearchReceiptID]
		if !found || !workspaceAnalysisV2ReceiptInJournal(state, receipt, agentdomain.WorkspaceAnalysisOperationSourceRead) {
			return result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 source receipt has no exact search and journal binding"))
		}
		item, err := toolsdomain.ReadSourceV4ReceiptEvidenceForSearch(receipt, search)
		if err != nil {
			return result, err
		}
		if item != authority.Evidence[index] || item.EvidenceRef != authority.EvidenceRefs[index] || item.EvidenceRef != binding.Identity.EvidenceRef {
			return result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 source projection drifted"))
		}
		ordinal, _ := strconv.Atoi(item.EvidenceRef[1:])
		if ordinal <= previous {
			return result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 evidence order is invalid"))
		}
		previous = ordinal
		if item.Truncated {
			continue
		}
		if result.retrieval.IndexVersionID == "" {
			result.retrieval = agentdomain.RetrievalRef{IndexVersionID: binding.Identity.IndexVersionID}
		}
		result.refs = append(result.refs, item.EvidenceRef)
		result.items = append(result.items, workspaceAnalysisSynthesisEvidenceInput{EvidenceRef: item.EvidenceRef, Excerpt: item.Excerpt, Truncated: false})
	}
	if len(result.items) > 0 && result.retrieval.Validate() != nil {
		return result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 evidence index is invalid"))
	}
	return result, nil
}

func workspaceAnalysisV2ReceiptInJournal(state workspaceAnalysisV2Execution, receipt toolsdomain.ResultReceipt, kind agentdomain.WorkspaceAnalysisOperationKind) bool {
	if receipt.WorkspaceID != state.execution.WorkspaceID || receipt.WorkflowRunID != state.execution.RunID {
		return false
	}
	for _, entry := range state.journal.Entries {
		op := entry.Operation
		if op.NodeKey == agentdomain.WorkspaceAnalysisOperationNodeDecideNext && op.Kind == kind && op.Status == agentdomain.WorkspaceAnalysisOperationSucceeded &&
			op.Call != nil && op.Call.ID == receipt.ToolCallID && op.Result != nil && op.Result.ID == receipt.ID && op.Result.Hash == receipt.OutputHash {
			return true
		}
	}
	return false
}

func (executor *workspaceAnalysisV2NodeExecutor) loadGitModelOutput(ctx context.Context, state workspaceAnalysisV2Execution) (json.RawMessage, error) {
	var key *agentdomain.WorkspaceAnalysisOperationKey
	for _, entry := range state.journal.Entries {
		op := entry.Operation
		if op.NodeKey == agentdomain.WorkspaceAnalysisOperationNodeDecideNext && op.Kind == agentdomain.WorkspaceAnalysisOperationGitStatus && op.Status == agentdomain.WorkspaceAnalysisOperationSucceeded {
			value := op.LogicalKey()
			key = &value
		}
	}
	if key == nil {
		return json.RawMessage("null"), nil
	}
	return executor.dependencies.ToolOutputs.LoadWorkspaceAnalysisDynamicToolOutput(ctx, workspaceAnalysisV2ToolQuery(state, *key))
}

func validateWorkspaceAnalysisSynthesisResultV2(state workspaceAnalysisV2Execution, request agentapplication.WorkspaceAnalysisSynthesisRequest, result agentapplication.WorkspaceAnalysisSynthesisResult) error {
	candidate, run, call := result.Candidate, result.Run, result.Call
	document, err := json.Marshal(result.CandidateResult)
	if err != nil || agentdomain.ValidateWorkspaceAnalysisCandidate(candidate) != nil || agentdomain.ValidateModelRun(run) != nil || agentdomain.ValidateModelCall(call) != nil ||
		candidate.SchemaVersion != 2 || candidate.SchemaID != agentdomain.WorkspaceAnalysisCandidateSchemaID || candidate.WorkspaceID != state.execution.WorkspaceID ||
		candidate.AnalysisRunID != state.run.ID || candidate.AnswerID != state.root.AnswerID || candidate.SynthesisModelRunID != run.ID ||
		candidate.NodeAttemptID != run.NodeAttemptID || !bytes.Equal(document, candidate.Document) || run.WorkspaceID != state.execution.WorkspaceID || run.WorkflowRunID != state.execution.RunID ||
		run.NodeRunID != state.execution.NodeRunID || (!result.Replayed && run.NodeAttemptID != state.execution.NodeAttemptID) ||
		run.Schema != (agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "2"}) || run.ReducedSchema != run.Schema ||
		run.Status != agentdomain.ModelRunSucceeded || run.FinalResultType != agentdomain.ResultTypeWorkspaceAnalysisAnswer ||
		run.Profile != request.ProfileRef || run.Prompt != request.PromptRef || !sameWorkspaceAnalysisRetrievalRef(run.Retrieval, request.Retrieval) ||
		call.ModelRunID != run.ID || call.CallNo != 1 || call.Phase != agentdomain.ModelCallAnswer || call.Schema != run.Schema || call.Status != agentdomain.ModelCallSucceeded ||
		call.ResponseHash != candidate.DocumentHash || call.ResponseBytes != candidate.DocumentBytes || result.Usage != call.Usage ||
		result.Draft.Binding.WorkspaceID != state.execution.WorkspaceID || result.Draft.Binding.AnswerID != state.root.AnswerID ||
		result.Draft.Binding.WorkflowRunID != state.execution.RunID || result.Draft.Binding.NodeRunID != state.execution.NodeRunID || result.Draft.Binding.NodeAttemptID != candidate.NodeAttemptID ||
		(result.Draft.Status != agentapplication.DraftStreamCompleted && result.Draft.Status != agentapplication.DraftStreamDegraded) ||
		!workspaceAnalysisV2ReferencesSubset(result.CandidateResult.Payload.CitationRefs, request.AllowedEvidenceRefs) {
		return workspaceAnalysisReceiptError(errors.New("workspace analysis v2 synthesis result binding drifted"))
	}
	return nil
}

func workspaceAnalysisV2ReferencesSubset(refs, allowed []string) bool {
	if len(refs) == 0 || len(refs) > agentdomain.WorkspaceAnalysisV2MaxSourceReads {
		return false
	}
	set := map[string]bool{}
	for _, ref := range allowed {
		set[ref] = true
	}
	for _, ref := range refs {
		if !set[ref] {
			return false
		}
		set[ref] = false
	}
	return true
}

func (executor *workspaceAnalysisV2NodeExecutor) loadCandidate(ctx context.Context, state workspaceAnalysisV2Execution, predecessor bool) (agentdomain.WorkspaceAnalysisCandidate, agentdomain.WorkspaceAnalysisCandidateResult, error) {
	if _, err := executor.loadFinishedLoop(ctx, state, false); err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{}, err
	}
	raw, err := executor.loadStage(ctx, state, conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer, predecessor)
	if err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{}, err
	}
	output, err := decodeWorkspaceAnalysisSynthesisOutputV2(raw)
	if err != nil {
		return agentdomain.WorkspaceAnalysisCandidate{}, agentdomain.WorkspaceAnalysisCandidateResult{}, err
	}
	candidate, err := executor.dependencies.Candidates.LoadWorkspaceAnalysisCandidateAuthority(ctx, agentapplication.WorkspaceAnalysisCandidateAuthorityQuery{
		WorkspaceID: state.execution.WorkspaceID, WorkflowRunID: state.execution.RunID, AnalysisRunID: state.run.ID, CandidateID: output.CandidateID, CandidateHash: output.CandidateHash})
	if err != nil {
		return candidate, agentdomain.WorkspaceAnalysisCandidateResult{}, err
	}
	if agentdomain.ValidateWorkspaceAnalysisCandidate(candidate) != nil || candidate.SchemaVersion != 2 || candidate.ID != output.CandidateID || candidate.DocumentHash != output.CandidateHash ||
		candidate.SynthesisModelRunID != output.SynthesisModelRunID || candidate.WorkspaceID != state.execution.WorkspaceID || candidate.AnalysisRunID != state.run.ID || candidate.AnswerID != state.root.AnswerID {
		return candidate, agentdomain.WorkspaceAnalysisCandidateResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 candidate authority drifted"))
	}
	bound := false
	for _, entry := range state.journal.Entries {
		op := entry.Operation
		if op.ID == candidate.SynthesisOperationID && op.NodeKey == agentdomain.WorkspaceAnalysisOperationNodeSynthesizeAnswer &&
			op.Status == agentdomain.WorkspaceAnalysisOperationSucceeded && op.Call != nil && op.Call.ID == output.SynthesisModelCallID &&
			op.Result != nil && op.Result.ID == candidate.ID && op.Result.Hash == candidate.DocumentHash &&
			entry.ModelRun != nil && entry.ModelCall != nil && agentdomain.ValidateModelRun(*entry.ModelRun) == nil && agentdomain.ValidateModelCall(*entry.ModelCall) == nil &&
			entry.ModelRun.ID == candidate.SynthesisModelRunID && entry.ModelRun.NodeAttemptID == candidate.NodeAttemptID &&
			entry.ModelRun.WorkspaceID == state.execution.WorkspaceID && entry.ModelRun.WorkflowRunID == state.execution.RunID &&
			entry.ModelRun.Status == agentdomain.ModelRunSucceeded && entry.ModelRun.FinalResultType == agentdomain.ResultTypeWorkspaceAnalysisAnswer &&
			entry.ModelRun.Schema == (agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "2"}) && entry.ModelRun.ReducedSchema == entry.ModelRun.Schema &&
			entry.ModelCall.ID == output.SynthesisModelCallID && entry.ModelCall.ModelRunID == candidate.SynthesisModelRunID && entry.ModelCall.Status == agentdomain.ModelCallSucceeded &&
			entry.ModelCall.CallNo == 1 && entry.ModelCall.Phase == agentdomain.ModelCallAnswer && entry.ModelCall.Schema == entry.ModelRun.Schema &&
			entry.ModelCall.ResponseHash == candidate.DocumentHash && entry.ModelCall.ResponseBytes == candidate.DocumentBytes && op.RequestHash == entry.ModelCall.RequestHash {
			bound = true
			break
		}
	}
	if !bound {
		return candidate, agentdomain.WorkspaceAnalysisCandidateResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 candidate has no journal closure"))
	}
	limits := agentdomain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(agentdomain.MaxWorkspaceAnalysisCandidateBytes)
	result, err := agentdomain.DecodeWorkspaceAnalysisCandidate(candidate.Document, limits)
	if err != nil || result.ModelRunRef != candidate.SynthesisModelRunID {
		return candidate, result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 candidate document drifted"))
	}
	canonical, err := json.Marshal(result)
	if err != nil || !reflect.DeepEqual(json.RawMessage(canonical), candidate.Document) {
		return candidate, result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 candidate document is not canonical"))
	}
	return candidate, result, nil
}

func workspaceAnalysisV2Hash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
