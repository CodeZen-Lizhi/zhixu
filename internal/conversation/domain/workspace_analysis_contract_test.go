package domain

import "testing"

func TestWorkspaceAnalysisPublicCodesAreUniqueAndFullyClassified(t *testing.T) {
	tests := []WorkspaceAnalysisPublicCodeContract{
		{Code: WorkspaceAnalysisModeInvalidCode, Category: WorkspaceAnalysisCodeProblem},
		{Code: WorkspaceAnalysisScopeUnsupportedCode, Category: WorkspaceAnalysisCodeProblem},
		{Code: WorkspaceAnalysisCapabilityUnavailableCode, Category: WorkspaceAnalysisCodeProblem},
		{Code: WorkspaceAnalysisCompleted, Category: WorkspaceAnalysisCodeCompleted, PublicationStatus: AnswerPublicationCompleted, ResultType: AnswerResultWorkspaceAnalysis},
		{Code: string(WorkspaceAnalysisEvidenceInsufficient), Category: WorkspaceAnalysisCodeRefusal, PublicationStatus: AnswerPublicationRefused, ResultType: AnswerResultWorkspaceAnalysisRefusal},
		{Code: string(WorkspaceAnalysisCitationInvalid), Category: WorkspaceAnalysisCodeRefusal, PublicationStatus: AnswerPublicationRefused, ResultType: AnswerResultWorkspaceAnalysisRefusal},
		{Code: string(WorkspaceAnalysisFaithfulnessRejected), Category: WorkspaceAnalysisCodeRefusal, PublicationStatus: AnswerPublicationRefused, ResultType: AnswerResultWorkspaceAnalysisRefusal},
		{Code: string(WorkspaceAnalysisModelRefused), Category: WorkspaceAnalysisCodeRefusal, PublicationStatus: AnswerPublicationRefused, ResultType: AnswerResultWorkspaceAnalysisRefusal},
		{Code: WorkspaceAnalysisClarificationRequired, Category: WorkspaceAnalysisCodeClarification, PublicationStatus: AnswerPublicationClarificationRequired, ResultType: AnswerResultClarification},
		{Code: string(WorkspaceAnalysisBudgetExhausted), Category: WorkspaceAnalysisCodeTermination, PublicationStatus: WorkspaceAnalysisPublicationFailed, ResultType: AnswerResultWorkspaceAnalysisTermination},
		{Code: string(WorkspaceAnalysisReceiptInvalid), Category: WorkspaceAnalysisCodeTermination, PublicationStatus: WorkspaceAnalysisPublicationFailed, ResultType: AnswerResultWorkspaceAnalysisTermination},
		{Code: string(WorkspaceAnalysisResultUnknown), Category: WorkspaceAnalysisCodeTermination, PublicationStatus: WorkspaceAnalysisPublicationFailed, ResultType: AnswerResultWorkspaceAnalysisTermination},
		{Code: string(WorkspaceAnalysisDeadlineExceeded), Category: WorkspaceAnalysisCodeTermination, PublicationStatus: WorkspaceAnalysisPublicationFailed, ResultType: AnswerResultWorkspaceAnalysisTermination},
		{Code: string(WorkspaceAnalysisModelFailed), Category: WorkspaceAnalysisCodeTermination, PublicationStatus: WorkspaceAnalysisPublicationFailed, ResultType: AnswerResultWorkspaceAnalysisTermination},
		{Code: string(WorkspaceAnalysisToolFailed), Category: WorkspaceAnalysisCodeTermination, PublicationStatus: WorkspaceAnalysisPublicationFailed, ResultType: AnswerResultWorkspaceAnalysisTermination},
		{Code: string(WorkspaceAnalysisRuntimeFailed), Category: WorkspaceAnalysisCodeTermination, PublicationStatus: WorkspaceAnalysisPublicationFailed, ResultType: AnswerResultWorkspaceAnalysisTermination},
		{Code: string(WorkspaceAnalysisCancelled), Category: WorkspaceAnalysisCodeTermination, PublicationStatus: WorkspaceAnalysisPublicationCancelled, ResultType: AnswerResultWorkspaceAnalysisTermination},
	}
	seen := make(map[string]struct{}, len(tests))
	for _, want := range tests {
		if _, duplicate := seen[want.Code]; duplicate {
			t.Fatalf("duplicate public code %q", want.Code)
		}
		seen[want.Code] = struct{}{}
		got, ok := WorkspaceAnalysisContractForPublicCode(want.Code)
		if !ok || got != want {
			t.Fatalf("code %q contract=%#v ok=%v, want=%#v", want.Code, got, ok, want)
		}
	}
	if _, ok := WorkspaceAnalysisContractForPublicCode("WORKSPACE_ANALYSIS_UNCLASSIFIED"); ok {
		t.Fatal("unknown public code was classified")
	}
}

func TestEvaluateWorkspaceAnalysisRolloutIsDefaultOffAndDoesNotAffectRAG(t *testing.T) {
	for bits := 0; bits < 16; bits++ {
		state := WorkspaceAnalysisRolloutState{
			FeatureEnabled: bits&1 != 0,
			APIReady:       bits&2 != 0,
			WorkerReady:    bits&4 != 0,
			RAGReady:       bits&8 != 0,
		}
		decision := EvaluateWorkspaceAnalysisRollout(state)
		wantSubmit := state.FeatureEnabled && state.APIReady && state.WorkerReady
		if decision.CanSubmit != wantSubmit || decision.RAGReady != state.RAGReady {
			t.Fatalf("state=%#v decision=%#v", state, decision)
		}
		if wantSubmit && decision.ProblemCode != "" {
			t.Fatalf("ready state returned a problem: %#v", decision)
		}
		if !wantSubmit && decision.ProblemCode != WorkspaceAnalysisCapabilityUnavailableCode {
			t.Fatalf("unavailable state returned unstable problem: %#v", decision)
		}
	}
}

func TestWorkspaceAnalysisPhaseZeroDoesNotWidenLegacyStatusMapping(t *testing.T) {
	if ResultTypeForPublicationStatus(AnswerPublicationCompleted) != AnswerResultRAGAnswer ||
		ResultTypeForPublicationStatus(AnswerPublicationRefused) != AnswerResultRefusal ||
		ResultTypeForPublicationStatus(AnswerPublicationClarificationRequired) != AnswerResultClarification ||
		ResultTypeForPublicationStatus(WorkspaceAnalysisPublicationFailed) != "" ||
		ResultTypeForPublicationStatus(WorkspaceAnalysisPublicationCancelled) != "" {
		t.Fatal("legacy status-only result mapping changed before mode-aware integration")
	}
}
