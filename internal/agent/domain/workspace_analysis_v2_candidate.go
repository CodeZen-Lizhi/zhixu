package domain

const WorkspaceAnalysisCandidateSchemaVersionV2 int64 = 2

func validWorkspaceAnalysisCandidateReferencesForVersion(references []string, required bool, version string) bool {
	if version == "1" {
		return validWorkspaceAnalysisCandidateReferences(references, required)
	}
	if version != "2" || references == nil || len(references) > WorkspaceAnalysisV2MaxSourceReads || required && len(references) == 0 {
		return false
	}
	seen := make(map[string]bool, len(references))
	for _, ref := range references {
		if !ValidWorkspaceAnalysisV2EvidenceRef(ref) || seen[ref] {
			return false
		}
		seen[ref] = true
	}
	return true
}

func workspaceAnalysisV2ReservationMatchesOperationPolicy(run WorkspaceAnalysisRun, reservation WorkspaceAnalysisBudgetReservation, kind WorkspaceAnalysisOperationKind) bool {
	if reservation.CallKind == WorkspaceAnalysisOperationCallTool {
		return (reservation.Reserved.SourceReads == 1) == (kind == WorkspaceAnalysisOperationSourceRead)
	}
	if reservation.Reserved.InputTokens != WorkspaceAnalysisV2MaxInputTokensPerModelCall {
		return false
	}
	switch kind {
	case WorkspaceAnalysisOperationDecision:
		return reservation.Reserved.OutputTokens == WorkspaceAnalysisV2DecisionMaxOutputTokens
	case WorkspaceAnalysisOperationAnswerSynthesis:
		return reservation.Reserved.OutputTokens == run.Limits.Amount.OutputTokens-WorkspaceAnalysisV2MaxDecisions*WorkspaceAnalysisV2DecisionMaxOutputTokens-WorkspaceAnalysisV2ReviewMaxOutputTokens
	case WorkspaceAnalysisOperationFaithfulnessReview:
		return reservation.Reserved.OutputTokens == WorkspaceAnalysisV2ReviewMaxOutputTokens
	default:
		return false
	}
}
