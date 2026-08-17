package domain

const (
	// WorkspaceAnalysisModeInvalidCode 表示提交了未知 Question mode。
	WorkspaceAnalysisModeInvalidCode = "WORKSPACE_ANALYSIS_MODE_INVALID"
	// WorkspaceAnalysisScopeUnsupportedCode 表示工作区分析不支持请求中的 Scope 组合。
	WorkspaceAnalysisScopeUnsupportedCode = "WORKSPACE_ANALYSIS_SCOPE_UNSUPPORTED"
	// WorkspaceAnalysisCapabilityUnavailableCode 表示 API 或 Worker 尚未共同具备同版工作区分析能力。
	WorkspaceAnalysisCapabilityUnavailableCode = "WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE"
	// WorkspaceAnalysisClarificationRequired 表示 Planner 要求用户补充范围或意图。
	WorkspaceAnalysisClarificationRequired = "WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED"
)

// WorkspaceAnalysisPublicCodeCategory 区分接受前 Problem 与接受后的 Answer 终态。
type WorkspaceAnalysisPublicCodeCategory string

const (
	// WorkspaceAnalysisCodeProblem 表示请求未被接受，HTTP 边界应返回稳定 Problem。
	WorkspaceAnalysisCodeProblem WorkspaceAnalysisPublicCodeCategory = "problem"
	// WorkspaceAnalysisCodeCompleted 表示已发布成功回答。
	WorkspaceAnalysisCodeCompleted WorkspaceAnalysisPublicCodeCategory = "completed"
	// WorkspaceAnalysisCodeRefusal 表示已接受请求的确定性拒答。
	WorkspaceAnalysisCodeRefusal WorkspaceAnalysisPublicCodeCategory = "refusal"
	// WorkspaceAnalysisCodeClarification 表示已接受请求的澄清终态。
	WorkspaceAnalysisCodeClarification WorkspaceAnalysisPublicCodeCategory = "clarification"
	// WorkspaceAnalysisCodeTermination 表示已接受请求的失败或取消终态。
	WorkspaceAnalysisCodeTermination WorkspaceAnalysisPublicCodeCategory = "termination"
)

// WorkspaceAnalysisPublicCodeContract 冻结稳定 code 的边界分类与 Answer 映射。
type WorkspaceAnalysisPublicCodeContract struct {
	Code              string
	Category          WorkspaceAnalysisPublicCodeCategory
	PublicationStatus AnswerPublicationStatus
	ResultType        AnswerResultType
}

// WorkspaceAnalysisContractForPublicCode 返回稳定 code 唯一允许的公开边界。
func WorkspaceAnalysisContractForPublicCode(code string) (WorkspaceAnalysisPublicCodeContract, bool) {
	contract := WorkspaceAnalysisPublicCodeContract{Code: code}
	switch code {
	case WorkspaceAnalysisModeInvalidCode, WorkspaceAnalysisScopeUnsupportedCode, WorkspaceAnalysisCapabilityUnavailableCode:
		contract.Category = WorkspaceAnalysisCodeProblem
	case WorkspaceAnalysisCompleted:
		contract.Category = WorkspaceAnalysisCodeCompleted
		contract.PublicationStatus = AnswerPublicationCompleted
		contract.ResultType = AnswerResultWorkspaceAnalysis
	case string(WorkspaceAnalysisEvidenceInsufficient), string(WorkspaceAnalysisCitationInvalid),
		string(WorkspaceAnalysisFaithfulnessRejected), string(WorkspaceAnalysisModelRefused):
		contract.Category = WorkspaceAnalysisCodeRefusal
		contract.PublicationStatus = AnswerPublicationRefused
		contract.ResultType = AnswerResultWorkspaceAnalysisRefusal
	case WorkspaceAnalysisClarificationRequired:
		contract.Category = WorkspaceAnalysisCodeClarification
		contract.PublicationStatus = AnswerPublicationClarificationRequired
		contract.ResultType = AnswerResultClarification
	case string(WorkspaceAnalysisBudgetExhausted), string(WorkspaceAnalysisReceiptInvalid), string(WorkspaceAnalysisResultUnknown),
		string(WorkspaceAnalysisDeadlineExceeded), string(WorkspaceAnalysisModelFailed), string(WorkspaceAnalysisToolFailed),
		string(WorkspaceAnalysisRuntimeFailed):
		contract.Category = WorkspaceAnalysisCodeTermination
		contract.PublicationStatus = WorkspaceAnalysisPublicationFailed
		contract.ResultType = AnswerResultWorkspaceAnalysisTermination
	case string(WorkspaceAnalysisCancelled):
		contract.Category = WorkspaceAnalysisCodeTermination
		contract.PublicationStatus = WorkspaceAnalysisPublicationCancelled
		contract.ResultType = AnswerResultWorkspaceAnalysisTermination
	default:
		return WorkspaceAnalysisPublicCodeContract{}, false
	}
	return contract, true
}

// WorkspaceAnalysisRolloutState 是 API 评估提交能力所需的最小默认关闭状态。
type WorkspaceAnalysisRolloutState struct {
	FeatureEnabled bool
	APIReady       bool
	WorkerReady    bool
	RAGReady       bool
}

// WorkspaceAnalysisRolloutDecision 是不影响固定 RAG 的工作区分析提交决策。
type WorkspaceAnalysisRolloutDecision struct {
	CanSubmit   bool
	ProblemCode string
	RAGReady    bool
}

// EvaluateWorkspaceAnalysisRollout 仅在开关、API 与 Worker 同时就绪时允许新提交。
func EvaluateWorkspaceAnalysisRollout(state WorkspaceAnalysisRolloutState) WorkspaceAnalysisRolloutDecision {
	decision := WorkspaceAnalysisRolloutDecision{RAGReady: state.RAGReady}
	if state.FeatureEnabled && state.APIReady && state.WorkerReady {
		decision.CanSubmit = true
		return decision
	}
	decision.ProblemCode = WorkspaceAnalysisCapabilityUnavailableCode
	return decision
}
