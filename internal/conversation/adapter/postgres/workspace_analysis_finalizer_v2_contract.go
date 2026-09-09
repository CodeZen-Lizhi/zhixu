package postgres

import (
	"encoding/json"
	"errors"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func workspaceAnalysisPublicationVersion(version int64) int64 {
	if version == 0 {
		return 1
	}
	return version
}

func workspaceAnalysisTerminationOperationBinding(
	command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
	fence workspaceAnalysisFinalizationFence,
	operation workspaceAnalysisOperationRecord,
) bool {
	switch command.Reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient:
		if fence.DefinitionVersion == 2 {
			return fence.NodeKey == conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer &&
				operation.NodeKey == conversationworkflow.WorkspaceAnalysisNodeDecideNext && operation.Kind == agentdomain.WorkspaceAnalysisOperationDecision
		}
		return fence.NodeKey == conversationworkflow.WorkspaceAnalysisNodeReadEvidence &&
			operation.NodeKey == conversationworkflow.WorkspaceAnalysisNodeRetrieveEvidence && operation.Kind == agentdomain.WorkspaceAnalysisOperationKnowledgeSearch && operation.Ordinal == 1
	case agentdomain.WorkspaceAnalysisRunCitationInvalid:
		return fence.NodeKey == conversationworkflow.WorkspaceAnalysisNodeReviewPublish &&
			operation.NodeKey == conversationworkflow.WorkspaceAnalysisNodeValidateCitations && operation.Kind == agentdomain.WorkspaceAnalysisOperationCitationValidation && operation.Ordinal == 1
	default:
		return operation.NodeRunID == command.NodeRunID
	}
}

func workspaceAnalysisV2BudgetRequestMatchesOperation(request conversationapplication.WorkspaceAnalysisBudgetRequest, kind agentdomain.WorkspaceAnalysisOperationKind, fence workspaceAnalysisFinalizationFence) bool {
	want := conversationapplication.WorkspaceAnalysisBudgetRequest{}
	switch kind {
	case agentdomain.WorkspaceAnalysisOperationDecision:
		want.ModelCalls, want.InputTokens, want.OutputTokens = 1, agentdomain.WorkspaceAnalysisV2MaxInputTokensPerModelCall, agentdomain.WorkspaceAnalysisV2DecisionMaxOutputTokens
	case agentdomain.WorkspaceAnalysisOperationAnswerSynthesis:
		want.ModelCalls, want.InputTokens = 1, agentdomain.WorkspaceAnalysisV2MaxInputTokensPerModelCall
		want.OutputTokens = fence.MaxOutputTokens - agentdomain.WorkspaceAnalysisV2MaxDecisions*agentdomain.WorkspaceAnalysisV2DecisionMaxOutputTokens - agentdomain.WorkspaceAnalysisV2ReviewMaxOutputTokens
	case agentdomain.WorkspaceAnalysisOperationFaithfulnessReview:
		want.ModelCalls, want.InputTokens, want.OutputTokens = 1, agentdomain.WorkspaceAnalysisV2MaxInputTokensPerModelCall, agentdomain.WorkspaceAnalysisV2ReviewMaxOutputTokens
	case agentdomain.WorkspaceAnalysisOperationSourceRead:
		want.ToolCalls, want.SourceReads = 1, 1
	case agentdomain.WorkspaceAnalysisOperationGitStatus, agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, agentdomain.WorkspaceAnalysisOperationCitationValidation:
		want.ToolCalls = 1
	default:
		return false
	}
	return request.ModelCalls == want.ModelCalls && request.ToolCalls == want.ToolCalls && request.SourceReads == want.SourceReads &&
		request.InputTokens == want.InputTokens && request.OutputTokens == want.OutputTokens && request.RequestedCostMicrounits == nil
}

func workspaceAnalysisV2BudgetExhausted(request conversationapplication.WorkspaceAnalysisBudgetRequest, operation workspaceAnalysisOperationRecord, fence workspaceAnalysisFinalizationFence) bool {
	modelHeadroom, toolHeadroom := 0, 0
	var inputHeadroom, outputHeadroom int64
	if operation.Kind == agentdomain.WorkspaceAnalysisOperationDecision {
		if operation.Ordinal > agentdomain.WorkspaceAnalysisV2MaxDecisions {
			return true
		}
		modelHeadroom, inputHeadroom = 2, 2*agentdomain.WorkspaceAnalysisV2MaxInputTokensPerModelCall
		outputHeadroom = fence.MaxOutputTokens - agentdomain.WorkspaceAnalysisV2MaxDecisions*agentdomain.WorkspaceAnalysisV2DecisionMaxOutputTokens
	}
	if operation.CallKind == agentdomain.WorkspaceAnalysisOperationCallTool && operation.NodeKey == conversationworkflow.WorkspaceAnalysisNodeDecideNext {
		toolHeadroom = 1
	}
	if operation.Kind == agentdomain.WorkspaceAnalysisOperationKnowledgeSearch && fence.EvidenceCount >= agentdomain.WorkspaceAnalysisV2MaxEvidenceRefs {
		return true
	}
	return request.ModelCalls+modelHeadroom > fence.MaxModelCalls-fence.ReservedModelCalls-fence.SettledModelCalls ||
		request.ToolCalls+toolHeadroom > fence.MaxToolCalls-fence.ReservedToolCalls-fence.SettledToolCalls ||
		request.SourceReads > fence.MaxSourceReads-fence.ReservedSourceReads-fence.SettledSourceReads ||
		request.InputTokens+inputHeadroom > fence.MaxInputTokens-fence.ReservedInputTokens-fence.SettledInputTokens ||
		request.OutputTokens+outputHeadroom > fence.MaxOutputTokens-fence.ReservedOutputTokens-fence.SettledOutputTokens
}

func buildWorkspaceAnalysisSuccessPublicationV2(workspaceID foundation.ID, fence workspaceAnalysisFinalizationFence, facts workspaceAnalysisSuccessFacts) (workspaceAnalysisPublication, error) {
	candidate, err := agentdomain.DecodeWorkspaceAnalysisCandidate(facts.Candidate.Document, agentdomain.DefaultDecodeLimits())
	if err != nil || facts.Candidate.SchemaVersion != 2 || candidate.SchemaVersion != "2" || candidate.ModelRunRef != facts.Candidate.SynthesisModelRunID {
		return workspaceAnalysisPublication{}, workspaceAnalysisTerminationAuthorityError("dynamic candidate version or model binding is invalid")
	}
	var gitStatus *conversationdomain.WorkspaceAnalysisGitStatus
	if facts.GitReceipt.ID != "" {
		if _, err := toolsdomain.ReadGitStatusV3ReceiptModelOutput(facts.GitReceipt); err != nil {
			return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
		}
		git, err := conversationworkflow.DecodeWorkspaceAnalysisGitStatusSummary(facts.GitReceipt.Output)
		if err != nil {
			return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
		}
		gitStatus = &conversationdomain.WorkspaceAnalysisGitStatus{
			Branch: git.Branch, Head: git.Head, Clean: git.Clean, StagedCount: git.StagedCount,
			UnstagedCount: git.UnstagedCount, UntrackedCount: git.UntrackedCount, ConflictCount: git.ConflictCount,
		}
	}
	results, err := toolsdomain.ValidateCitationV4ReceiptResults(facts.ValidationReceipt, facts.Candidate.ID, facts.Candidate.DocumentHash, candidate.Payload.CitationRefs)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	for _, result := range results {
		if !result.Valid || result.ReasonCode != "OK" {
			return workspaceAnalysisPublication{}, conflict(ErrorCodeWorkspaceAnalysisFinalizeConflict, errors.New("dynamic citations are not all valid"))
		}
	}
	trusted, err := toolsdomain.ValidateCitationV4ReceiptCitations(facts.ValidationReceipt, facts.Candidate.ID, facts.Candidate.DocumentHash, candidate.Payload.CitationRefs)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	citations := make([]agentdomain.Citation, 0, len(trusted))
	citationByRef := make(map[string]string, len(trusted))
	seenTuple := make(map[[4]foundation.ID]string, len(trusted))
	seenID := make(map[string][4]foundation.ID, len(trusted))
	for _, item := range trusted {
		citation := agentdomain.Citation{ID: item.CitationID, WorkspaceID: workspaceID, IndexVersionID: item.IndexVersionID, ChunkID: item.ChunkID, SourceVersionID: item.SourceVersionID, SourceSpanID: item.SourceSpanID}
		if err := citation.Validate(); err != nil {
			return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
		}
		tuple := [4]foundation.ID{item.IndexVersionID, item.ChunkID, item.SourceVersionID, item.SourceSpanID}
		if previous, found := seenTuple[tuple]; found && previous != item.CitationID {
			return workspaceAnalysisPublication{}, workspaceAnalysisTerminationAuthorityError("dynamic citation aliases disagree on their public identity")
		}
		if previous, found := seenID[item.CitationID]; found && previous != tuple {
			return workspaceAnalysisPublication{}, workspaceAnalysisTerminationAuthorityError("dynamic citation identity reuses a different tuple")
		}
		if _, found := seenTuple[tuple]; !found {
			citations = append(citations, citation)
		}
		seenTuple[tuple], seenID[item.CitationID], citationByRef[item.EvidenceRef] = item.CitationID, tuple, item.CitationID
	}
	var proposal *conversationdomain.WorkspaceAnalysisProposalSuggestion
	if source := candidate.Payload.ProposalSuggestion; source != nil {
		ids := make([]string, 0, len(source.CitationRefs))
		seen := make(map[string]bool, len(source.CitationRefs))
		for _, ref := range source.CitationRefs {
			id, found := citationByRef[ref]
			if !found {
				return workspaceAnalysisPublication{}, workspaceAnalysisTerminationAuthorityError("dynamic proposal references an unpublished citation")
			}
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
		proposal = &conversationdomain.WorkspaceAnalysisProposalSuggestion{Summary: source.Summary, CitationIDs: ids, Href: conversationdomain.WorkspaceAnalysisProposalHref}
	}
	var cost *int64
	if fence.SettledCostMicrounits != nil {
		value := *fence.SettledCostMicrounits
		cost = &value
	}
	result := conversationdomain.WorkspaceAnalysisAnswerResultV2{
		ResultType: conversationdomain.AnswerResultWorkspaceAnalysis, SchemaID: conversationdomain.WorkspaceAnalysisAnswerSchemaID,
		SchemaVersion: conversationdomain.WorkspaceAnalysisResultSchemaVersionV2, ModelRunRef: facts.Candidate.SynthesisModelRunID,
		Payload: conversationdomain.WorkspaceAnalysisAnswerPayloadV2{
			AnswerMarkdown: candidate.Payload.AnswerMarkdown, Citations: citations, GitStatus: gitStatus, ProposalSuggestion: proposal,
			Budget:            conversationdomain.WorkspaceAnalysisBudgetSummaryV2{ModelCalls: fence.SettledModelCalls, ToolCalls: fence.SettledToolCalls, InputTokens: fence.SettledInputTokens, OutputTokens: fence.SettledOutputTokens, EstimatedCostMicrounits: cost},
			TerminationReason: conversationdomain.WorkspaceAnalysisCompleted,
		},
	}
	document, err := json.Marshal(result)
	if err != nil {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	published, err := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(conversationdomain.AnswerPublicationCompleted, conversationdomain.AnswerResultWorkspaceAnalysis, document)
	if err != nil || published.ModelRunID == nil || *published.ModelRunID != facts.Candidate.SynthesisModelRunID {
		return workspaceAnalysisPublication{}, consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	return workspaceAnalysisPublication{Status: conversationdomain.AnswerPublicationCompleted, ResultType: published.Type,
		ModelRunID: copyWorkspaceAnalysisFinalizerID(published.ModelRunID), Document: published.Document, ResultHash: published.Hash,
		RunStatus: agentdomain.WorkspaceAnalysisRunSucceeded, Reason: agentdomain.WorkspaceAnalysisRunCompleted, CitationCount: int64(len(citations))}, nil
}
