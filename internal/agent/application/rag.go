package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	errorCodeRAGExecutorMissing = "AGENT_RAG_EXECUTOR_MISSING"
	errorCodeRAGRequestInvalid  = "AGENT_RAG_REQUEST_INVALID"
	errorCodeRAGResultDrift     = "AGENT_RAG_RETRIEVAL_RESULT_DRIFT"
	errorCodeRAGTopicMismatch   = "AGENT_RAG_RELATED_TOPIC_MISMATCH"
	// ErrorCodeRAGSchedulerContract 表示 RAG 调度器没有沿冻结 Graph 路由推进到终态。
	ErrorCodeRAGSchedulerContract = "AGENT_RAG_SCHEDULER_CONTRACT_VIOLATION"
	// ErrorCodeRAGProgressUnknown 表示阶段事件提交结果无法确认。
	ErrorCodeRAGProgressUnknown = "AGENT_RAG_PROGRESS_UNKNOWN"
)

// RAGQueryPlanPort 执行一次严格 Query Plan。
type RAGQueryPlanPort interface {
	Plan(context.Context, QueryPlanRequest) (QueryPlanRunResult, error)
}

// RAGStructuredRunnerPort 执行 RAG v2 结构化生成。
type RAGStructuredRunnerPort interface {
	Run(context.Context, StructuredRunRequest) (StructuredRunResult, error)
}

// RAGGenerationPort 只负责已经冻结 Evidence 之后的回答生成。生产 v2
// adapter 在此执行 Eino Agent -> tool-free Stream -> metadata；v1 adapter
// 继续包装原有 StructuredRunner。
type RAGGenerationPort interface {
	Generate(context.Context, RAGGenerationRequest) (RAGGenerationResult, error)
}

// RAGGenerationRequest 是 Evidence 阶段生成的强类型上下文和冻结运行引用。
type RAGGenerationRequest struct {
	ModelRunRef      foundation.ID
	Context          RAGGenerationContext
	ProfileRef       domain.ModelProfileRef
	PromptRef        domain.PromptRef
	SchemaRef        domain.SchemaRef
	ReducedSchemaRef domain.SchemaRef
}

// RAGGenerationContext 是 Evidence 节点交给生成 Adapter 的有界项目 DTO。
// 它保留完整服务端事实；模型可见的 metadata 投影由 Adapter 另行收窄。
type RAGGenerationContext struct {
	ModelRunRef   foundation.ID                     `json:"model_run_ref"`
	Request       json.RawMessage                   `json:"request"`
	Evidence      []domain.Evidence                 `json:"evidence"`
	Conflicts     []RAGConflictDisclosure           `json:"conflicts"`
	RelatedTopics map[foundation.ID]RAGAllowedTopic `json:"related_topics"`
}

// RAGGenerationResult 精确返回 Answer 或 Refusal，以及实际 metadata/legacy
// structured 调用结果。两种终态必须二选一。
type RAGGenerationResult struct {
	Answer     *domain.RAGAnswerResultV2
	Refusal    *domain.RefusalResult
	Generation StructuredRunResult
}

// RAGPublicationGatePort 执行 Citation 与 Faithfulness 门禁但不持久化发布。
type RAGPublicationGatePort interface {
	Publish(context.Context, AnswerPublicationRequest) (AnswerPublicationResult, error)
}

// RAGEvidenceTopicPort 单批解析正式证据绑定的 Active Topic。
type RAGEvidenceTopicPort interface {
	ResolveRAGTopics(context.Context, foundation.ID, []knowledgedomain.ProvenanceRef) ([]knowledgedomain.EvidenceTopicBinding, error)
}

// RAGProgressStage 是可持久重放的真实 RAG 阶段边界。
type RAGProgressStage string

const (
	RAGProgressPlanStarted         RAGProgressStage = "plan.started"
	RAGProgressPlanCompleted       RAGProgressStage = "plan.completed"
	RAGProgressRetrievalStarted    RAGProgressStage = "retrieval.started"
	RAGProgressRetrievalCompleted  RAGProgressStage = "retrieval.completed"
	RAGProgressValidationStarted   RAGProgressStage = "validation.started"
	RAGProgressValidationCompleted RAGProgressStage = "validation.completed"
)

// RAGProgressUpdate 只包含稳定阶段与计数，不包含问题、Evidence 或模型正文。
type RAGProgressUpdate struct {
	Stage            RAGProgressStage
	RewriteCount     int
	CandidateCount   int
	SelectedCount    int
	ConflictCount    int
	DegradationCount int
}

// RAGProgressPort 持久化实际执行阶段；失败不得静默忽略。
type RAGProgressPort interface {
	RecordRAGProgress(context.Context, RAGProgressUpdate) error
}

// RAGProgressRecord 绑定持久阶段事件所需身份与脱敏计数。
type RAGProgressRecord struct {
	WorkspaceID    foundation.ID
	WorkflowRunID  foundation.ID
	ConversationID foundation.ID
	QuestionID     foundation.ID
	AnswerID       foundation.ID
	ModelRunID     foundation.ID
	OccurredAt     time.Time
	Update         RAGProgressUpdate
}

// RAGProgressRecorder 持久化可重放、幂等且不含正文的阶段事件。
type RAGProgressRecorder interface {
	// RecordRAGProgress 追加一个真实阶段边界；相同绑定必须精确重放。
	RecordRAGProgress(context.Context, RAGProgressRecord) error
}

// RAGExecutionRequest 冻结一次 retrieval-first 执行的作用域和运行时版本。
type RAGExecutionRequest struct {
	WorkspaceID foundation.ID
	ModelRunRef foundation.ID
	PlanInput   []byte
	AnswerInput []byte
	SearchMode  retrievaldomain.SearchMode
	Filter      retrievaldomain.SearchFilter

	AllowOriginalSources bool
	AllowWeb             bool

	PlanProfileRef         domain.ModelProfileRef
	PlanPromptRef          domain.PromptRef
	PlanSchemaRef          domain.SchemaRef
	AnswerProfileRef       domain.ModelProfileRef
	AnswerPromptRef        domain.PromptRef
	AnswerSchemaRef        domain.SchemaRef
	AnswerReducedSchemaRef domain.SchemaRef
	ReviewProfileRef       domain.ModelProfileRef
	ReviewPromptRef        domain.PromptRef
	ReviewSchemaRef        domain.SchemaRef
}

// RAGRetrievalSummary 是 T09 持久化的有界检索事实。
type RAGRetrievalSummary struct {
	Rewrites             []string
	RequestedMode        retrievaldomain.SearchMode
	EffectiveMode        retrievaldomain.SearchMode
	Filter               retrievaldomain.SearchFilter
	AllowOriginalSources bool
	AllowWeb             bool
	IndexVersionID       foundation.ID
	EmbeddingVersionID   *foundation.ID
	CandidateCount       int
	SelectedCount        int
	ConflictCount        int
	Degradations         []retrievaldomain.SearchDegradation
}

// RAGClarificationProposal 是不经过检索和生成的规范澄清终态。
type RAGClarificationProposal struct {
	Intent          string
	Reason          string
	Question        string
	SuggestedScopes []string
}

// RAGTerminalProposal 是 T09 原子 finalizer 的唯一应用层输入。
type RAGTerminalProposal struct {
	ModelRunRef   foundation.ID
	Answer        *domain.RAGAnswerResultV2
	Refusal       *domain.RefusalResult
	Clarification *RAGClarificationProposal
	Review        *domain.FaithfulnessReviewResult
	Retrieval     *RAGRetrievalSummary
	Generation    *StructuredRunResult
}

// RAGPhaseRoute 是领域节点完成后交给 Graph 的有界路由结果。
// Application 只报告节点事实；下一节点由 Eino Graph 决定。
type RAGPhaseRoute string

const (
	RAGPhaseContinue      RAGPhaseRoute = "continue"
	RAGPhaseClarification RAGPhaseRoute = "clarification"
	RAGPhaseRefusal       RAGPhaseRoute = "refusal"
	RAGPhaseAnswer        RAGPhaseRoute = "answer"
)

// RAGPhaseOutcome 是一个领域节点的稳定输出，不包含 Eino 类型。
type RAGPhaseOutcome struct {
	Route RAGPhaseRoute
}

// RAGExecutionScheduler 只负责调用 RAG Graph，不拥有任何业务事实或 Provider 调用。
type RAGExecutionScheduler interface {
	Schedule(context.Context, *RAGPhaseRun) error
}

// RAGExecutor 提供 retrieval-first RAG 的项目步骤，不拥有任何持久化终结职责。
type RAGExecutor struct {
	planner     RAGQueryPlanPort
	search      ScopedRetrievalPort
	eligibility EvidenceEligibilityPort
	topics      RAGEvidenceTopicPort
	generation  RAGGenerationPort
	publisher   RAGPublicationGatePort
	progress    RAGProgressPort
	scheduler   RAGExecutionScheduler
}

// NewRAGExecutorWithScheduler 创建由指定调度器驱动的历史兼容 RAG 执行器。
func NewRAGExecutorWithScheduler(planner RAGQueryPlanPort, search ScopedRetrievalPort, eligibility EvidenceEligibilityPort, topics RAGEvidenceTopicPort, runner RAGStructuredRunnerPort, publisher RAGPublicationGatePort, progress RAGProgressPort, scheduler RAGExecutionScheduler) (*RAGExecutor, error) {
	if isNilPort(runner) {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, errorCodeRAGExecutorMissing, false, errors.New("rag structured runner is required"))
	}
	return NewRAGExecutorWithGenerationScheduler(
		planner, search, eligibility, topics, structuredRAGGeneration{runner: runner}, publisher, progress, scheduler,
	)
}

// NewRAGExecutorWithGenerationScheduler 使用显式生成端口创建 RAG 执行器。
// 该构造器是 v2 Eino Agent/Stream 的正式入口。
func NewRAGExecutorWithGenerationScheduler(
	planner RAGQueryPlanPort,
	search ScopedRetrievalPort,
	eligibility EvidenceEligibilityPort,
	topics RAGEvidenceTopicPort,
	generation RAGGenerationPort,
	publisher RAGPublicationGatePort,
	progress RAGProgressPort,
	scheduler RAGExecutionScheduler,
) (*RAGExecutor, error) {
	if isNilPort(planner) || isNilPort(search) || isNilPort(eligibility) || isNilPort(topics) || isNilPort(generation) || isNilPort(publisher) || isNilPort(progress) {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, errorCodeRAGExecutorMissing, false, errors.New("rag executor dependencies are required"))
	}
	if isNilPort(scheduler) {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, errorCodeRAGExecutorMissing, false, errors.New("rag execution scheduler is required"))
	}
	return &RAGExecutor{planner: planner, search: search, eligibility: eligibility, topics: topics, generation: generation, publisher: publisher, progress: progress, scheduler: scheduler}, nil
}

// Execute 生成 canonical terminal proposal；调用方必须交给 T09 在一个事务中终结。
func (e *RAGExecutor) Execute(ctx context.Context, request RAGExecutionRequest) (RAGTerminalProposal, error) {
	if e == nil || isNilPort(e.planner) || isNilPort(e.search) || isNilPort(e.eligibility) || isNilPort(e.topics) || isNilPort(e.generation) || isNilPort(e.publisher) || isNilPort(e.progress) || isNilPort(e.scheduler) {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorDependencyUnavailable, errorCodeRAGExecutorMissing, false, errors.New("rag executor is unavailable"))
	}
	if ctx == nil {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorInvalidInput, errorCodeRAGRequestInvalid, false, errors.New("rag execution context is required"))
	}
	if !canonicalApplicationID(request.WorkspaceID) || !canonicalApplicationID(request.ModelRunRef) ||
		len(request.PlanInput) == 0 || len(request.AnswerInput) == 0 || len(request.AnswerInput) > MaxStructuredInputBytes ||
		!json.Valid(request.AnswerInput) || bytes.IndexByte(request.AnswerInput, 0) >= 0 {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorInvalidInput, errorCodeRAGRequestInvalid, false, errors.New("rag execution identity or input is invalid"))
	}
	canonicalScope, err := retrievaldomain.CanonicalizeSearchRequest(retrievaldomain.SearchRequest{
		WorkspaceID: request.WorkspaceID,
		Query:       "rag-scope-validation",
		Mode:        request.SearchMode,
		Filter:      request.Filter,
		Limit:       1,
	})
	if err != nil {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorInvalidInput, errorCodeRAGRequestInvalid, false, err)
	}
	request.SearchMode = canonicalScope.Mode
	request.Filter = canonicalScope.Filter
	if request.AllowOriginalSources || request.AllowWeb {
		summary := initialRetrievalSummary(request)
		return refusalProposal(request.ModelRunRef, domain.RefusalExternalFactUnauthorized, "requested external or original-source evidence is not available in this release", &summary), nil
	}
	run := &RAGPhaseRun{executor: e, request: request}
	if err := e.scheduler.Schedule(ctx, run); err != nil {
		if failure := run.Failure(); failure != nil {
			return RAGTerminalProposal{}, failure
		}
		return RAGTerminalProposal{}, err
	}
	if failure := run.Failure(); failure != nil {
		return RAGTerminalProposal{}, failure
	}
	if !run.Completed() {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorConsistencyViolation, ErrorCodeRAGSchedulerContract, false, errors.New("rag scheduler returned before a terminal proposal"))
	}
	return run.Result(), nil
}

// RAGPhaseRun 是由 RAGExecutor 创建的领域节点状态句柄。
type RAGPhaseRun struct {
	mu sync.Mutex

	executor *RAGExecutor
	request  RAGExecutionRequest

	plan              QueryPlanRunResult
	merged            RetrievalBatch
	eligible          RetrievalBatch
	summary           RAGRetrievalSummary
	allowedTopics     map[foundation.ID]RAGAllowedTopic
	generationContext RAGGenerationContext
	generation        StructuredRunResult
	answer            domain.RAGAnswerResultV2
	proposal          RAGTerminalProposal
	planDone          bool
	retrievalDone     bool
	evidenceDone      bool
	generationDone    bool
	publicationDone   bool
	completed         bool
	failure           error
}

// Plan 执行 Query Plan 领域节点，并只返回 Graph 的下一路由。
func (run *RAGPhaseRun) Plan(ctx context.Context) (RAGPhaseOutcome, error) {
	return run.runNode(ctx, func() bool {
		return !run.planDone && !run.retrievalDone && !run.evidenceDone && !run.generationDone && !run.publicationDone
	}, func() { run.planDone = true }, func() (RAGPhaseOutcome, error) {
		if err := run.executor.progress.RecordRAGProgress(ctx, RAGProgressUpdate{Stage: RAGProgressPlanStarted}); err != nil {
			return RAGPhaseOutcome{}, err
		}
		plan, err := run.executor.planner.Plan(ctx, QueryPlanRequest{
			ModelRunRef: run.request.ModelRunRef, ProfileRef: run.request.PlanProfileRef,
			PromptRef: run.request.PlanPromptRef, SchemaRef: run.request.PlanSchemaRef, Input: run.request.PlanInput,
		})
		if err != nil {
			return RAGPhaseOutcome{}, err
		}
		run.plan = plan
		if err := run.executor.recordPostProviderProgress(ctx, RAGProgressUpdate{Stage: RAGProgressPlanCompleted, RewriteCount: len(plan.Plan.Payload.Rewrites)}); err != nil {
			return RAGPhaseOutcome{}, err
		}
		if plan.Plan.Payload.RequiresClarification {
			payload := plan.Plan.Payload
			summary := initialRetrievalSummary(run.request)
			run.finish(RAGTerminalProposal{ModelRunRef: run.request.ModelRunRef, Clarification: &RAGClarificationProposal{
				Intent: payload.Intent, Reason: payload.ClarificationReason, Question: payload.ClarificationQuestion,
				SuggestedScopes: append([]string(nil), payload.SuggestedScopes...),
			}, Retrieval: &summary})
			return RAGPhaseOutcome{Route: RAGPhaseClarification}, nil
		}
		return RAGPhaseOutcome{Route: RAGPhaseContinue}, nil
	})
}

// Retrieval 执行项目检索节点；空结果由 Graph 路由到拒答终态。
func (run *RAGPhaseRun) Retrieval(ctx context.Context) (RAGPhaseOutcome, error) {
	return run.runNode(ctx, func() bool {
		return run.planDone && !run.retrievalDone && !run.evidenceDone && !run.generationDone && !run.publicationDone
	}, func() { run.retrievalDone = true }, func() (RAGPhaseOutcome, error) {
		rewrites := run.plan.Plan.Payload.Rewrites
		if err := run.executor.recordPostProviderProgress(ctx, RAGProgressUpdate{Stage: RAGProgressRetrievalStarted, RewriteCount: len(rewrites)}); err != nil {
			return RAGPhaseOutcome{}, err
		}
		merged, summary, err := run.executor.retrieve(ctx, run.request, rewrites)
		if err != nil {
			return RAGPhaseOutcome{}, err
		}
		run.merged, run.summary = merged, summary
		if err := run.executor.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressRetrievalCompleted, summary)); err != nil {
			return RAGPhaseOutcome{}, err
		}
		if len(merged.Items) == 0 {
			run.finish(refusalProposal(run.request.ModelRunRef, domain.RefusalNoRelevantEvidence, "no relevant evidence was found in the approved knowledge scope", &summary))
			return RAGPhaseOutcome{Route: RAGPhaseRefusal}, nil
		}
		return RAGPhaseOutcome{Route: RAGPhaseContinue}, nil
	})
}

// Evidence 执行资格、冲突和 Topic 投影节点；拒答分支由 Graph 选择终态。
func (run *RAGPhaseRun) Evidence(ctx context.Context) (RAGPhaseOutcome, error) {
	return run.runNode(ctx, func() bool {
		return run.retrievalDone && !run.evidenceDone && !run.generationDone && !run.publicationDone
	}, func() { run.evidenceDone = true }, func() (RAGPhaseOutcome, error) {
		if err := run.executor.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressValidationStarted, run.summary)); err != nil {
			return RAGPhaseOutcome{}, err
		}
		eligible, evidence, provenances, conflicts, unconditionable, err := run.executor.projectEligibility(ctx, run.request.WorkspaceID, run.merged)
		if err != nil {
			return RAGPhaseOutcome{}, err
		}
		if len(eligible.Items) == 0 {
			if err := run.finishValidation(ctx, refusalProposal(run.request.ModelRunRef, domain.RefusalUnapprovedEvidenceOnly, "retrieval found candidates, but none are approved knowledge evidence", &run.summary)); err != nil {
				return RAGPhaseOutcome{}, err
			}
			return RAGPhaseOutcome{Route: RAGPhaseRefusal}, nil
		}
		run.summary.SelectedCount = len(eligible.Items)
		run.summary.ConflictCount = uniqueConflictCount(conflicts)
		if unconditionable {
			if err := run.finishValidation(ctx, refusalProposal(run.request.ModelRunRef, domain.RefusalConflictNotConditionable, "retrieved conflict evidence does not contain at least two selected disputed claim positions", &run.summary)); err != nil {
				return RAGPhaseOutcome{}, err
			}
			return RAGPhaseOutcome{Route: RAGPhaseRefusal}, nil
		}
		bindings, err := run.executor.topics.ResolveRAGTopics(ctx, run.request.WorkspaceID, provenances)
		if err != nil {
			return RAGPhaseOutcome{}, err
		}
		allowed, err := topicAllowlist(bindings, eligible)
		if err != nil {
			return RAGPhaseOutcome{}, err
		}
		if len(allowed) == 0 {
			if err := run.finishValidation(ctx, refusalProposal(run.request.ModelRunRef, domain.RefusalEvidenceInsufficient, "eligible evidence has no active related topic binding", &run.summary)); err != nil {
				return RAGPhaseOutcome{}, err
			}
			return RAGPhaseOutcome{Route: RAGPhaseRefusal}, nil
		}
		generationContext := RAGGenerationContext{
			ModelRunRef: run.request.ModelRunRef, Request: append(json.RawMessage(nil), run.request.AnswerInput...),
			Evidence: evidence, Conflicts: conflicts, RelatedTopics: allowed,
		}
		encoded, err := json.Marshal(generationContext)
		if err != nil {
			return RAGPhaseOutcome{}, applicationError(foundation.ErrorNonRetryableFailure, errorCodeRAGRequestInvalid, false, err)
		}
		if len(encoded) > MaxStructuredInputBytes {
			return RAGPhaseOutcome{}, applicationError(foundation.ErrorNonRetryableFailure, errorCodeRAGRequestInvalid, false, errors.New("rag generation context exceeds its bounded input"))
		}
		run.eligible, run.allowedTopics, run.generationContext = eligible, allowed, cloneRAGGenerationContext(generationContext)
		return RAGPhaseOutcome{Route: RAGPhaseContinue}, nil
	})
}

// Generation 执行 Agent/Stream/metadata 生成节点；结构化拒答走 Graph 拒答分支。
func (run *RAGPhaseRun) Generation(ctx context.Context) (RAGPhaseOutcome, error) {
	return run.runNode(ctx, func() bool { return run.evidenceDone && !run.generationDone && !run.publicationDone }, func() { run.generationDone = true }, func() (RAGPhaseOutcome, error) {
		result, err := run.executor.generation.Generate(ctx, RAGGenerationRequest{
			ModelRunRef: run.request.ModelRunRef, Context: cloneRAGGenerationContext(run.generationContext),
			ProfileRef: run.request.AnswerProfileRef, PromptRef: run.request.AnswerPromptRef,
			SchemaRef: run.request.AnswerSchemaRef, ReducedSchemaRef: run.request.AnswerReducedSchemaRef,
		})
		if err != nil {
			return RAGPhaseOutcome{}, err
		}
		run.generation = result.Generation
		if (result.Answer == nil) == (result.Refusal == nil) {
			return RAGPhaseOutcome{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("rag generation returned no unique terminal result"))
		}
		if result.Refusal != nil {
			if result.Generation.Phase != domain.ModelCallReduced || result.Generation.Runtime.Schema != run.request.AnswerReducedSchemaRef ||
				result.Refusal.ModelRunRef != run.request.ModelRunRef {
				return RAGPhaseOutcome{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("refusal schema is only valid in the reduced phase"))
			}
			if err := run.finishValidation(ctx, RAGTerminalProposal{ModelRunRef: run.request.ModelRunRef, Refusal: result.Refusal, Retrieval: &run.summary, Generation: &run.generation}); err != nil {
				return RAGPhaseOutcome{}, err
			}
			return RAGPhaseOutcome{Route: RAGPhaseRefusal}, nil
		}
		answer := *result.Answer
		if answer.Validate() != nil || answer.ModelRunRef != run.request.ModelRunRef {
			return RAGPhaseOutcome{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("rag v2 generation is not bound to the model run"))
		}
		if err := validateRelatedTopics(answer.Payload.RelatedTopics, run.allowedTopics); err != nil {
			return RAGPhaseOutcome{}, err
		}
		run.answer = answer
		return RAGPhaseOutcome{Route: RAGPhaseContinue}, nil
	})
}

func (run *RAGPhaseRun) runNode(
	ctx context.Context,
	canEnter func() bool,
	markEntered func(),
	execute func() (RAGPhaseOutcome, error),
) (RAGPhaseOutcome, error) {
	if run == nil {
		return RAGPhaseOutcome{}, applicationError(foundation.ErrorConsistencyViolation, ErrorCodeRAGSchedulerContract, false, errors.New("rag phase run is nil"))
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.failure != nil {
		return RAGPhaseOutcome{}, run.failure
	}
	if run.completed || run.executor == nil || canEnter == nil || !canEnter() || markEntered == nil || execute == nil {
		return RAGPhaseOutcome{}, run.fail(applicationError(foundation.ErrorConsistencyViolation, ErrorCodeRAGSchedulerContract, false, errors.New("rag graph entered a domain node outside its valid predecessor path")))
	}
	if ctx == nil {
		return RAGPhaseOutcome{}, run.fail(applicationError(foundation.ErrorInvalidInput, errorCodeRAGRequestInvalid, false, errors.New("rag phase context is required")))
	}
	if err := ctx.Err(); err != nil {
		return RAGPhaseOutcome{}, run.fail(operationContextError(err))
	}
	markEntered()
	outcome, err := execute()
	if err != nil {
		return RAGPhaseOutcome{}, run.fail(err)
	}
	validRoute := outcome.Route == RAGPhaseContinue || outcome.Route == RAGPhaseClarification ||
		outcome.Route == RAGPhaseRefusal || outcome.Route == RAGPhaseAnswer
	terminalRoute := outcome.Route != RAGPhaseContinue
	if !validRoute || terminalRoute != run.completed {
		return RAGPhaseOutcome{}, run.fail(applicationError(foundation.ErrorConsistencyViolation, ErrorCodeRAGSchedulerContract, false, errors.New("rag domain node returned an unsupported or inconsistent graph route")))
	}
	return outcome, nil
}

// Completed 报告 RAG 是否已经形成唯一终态提案。
func (run *RAGPhaseRun) Completed() bool {
	if run == nil {
		return false
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.completed
}

// Failure 返回 Application 产生的原始错误。
func (run *RAGPhaseRun) Failure() error {
	if run == nil {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.failure
}

// Result 返回已经完成的终态提案。
func (run *RAGPhaseRun) Result() RAGTerminalProposal {
	if run == nil {
		return RAGTerminalProposal{}
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.proposal
}

type structuredRAGGeneration struct {
	runner RAGStructuredRunnerPort
}

func (generation structuredRAGGeneration) Generate(ctx context.Context, request RAGGenerationRequest) (RAGGenerationResult, error) {
	if request.Context.ModelRunRef != request.ModelRunRef || len(request.Context.Request) == 0 ||
		!json.Valid(request.Context.Request) || bytes.IndexByte(request.Context.Request, 0) >= 0 {
		return RAGGenerationResult{}, applicationError(foundation.ErrorInvalidInput, errorCodeRAGRequestInvalid, false, errors.New("rag generation context is invalid"))
	}
	input, err := json.Marshal(request.Context)
	if err != nil || len(input) > MaxStructuredInputBytes {
		return RAGGenerationResult{}, applicationError(foundation.ErrorInvalidInput, errorCodeRAGRequestInvalid, false, errors.New("rag generation context exceeds its bounded input"))
	}
	result, err := generation.runner.Run(ctx, StructuredRunRequest{
		ProfileRef: request.ProfileRef, PromptRef: request.PromptRef, SchemaRef: request.SchemaRef,
		ReducedSchemaRef: request.ReducedSchemaRef, Input: input,
	})
	if err != nil {
		return RAGGenerationResult{}, err
	}
	if result.Runtime.Schema.ID == domain.RefusalSchemaID {
		if result.Phase != domain.ModelCallReduced || result.Runtime.Schema.Version != domain.OutputSchemaVersionV1 {
			return RAGGenerationResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("refusal schema is only valid in the reduced phase"))
		}
		refusal, decodeErr := domain.DecodeRefusal(result.Output, domain.DefaultDecodeLimits())
		if decodeErr != nil || refusal.ModelRunRef != request.ModelRunRef {
			return RAGGenerationResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("reduced refusal is invalid or not bound to the model run"))
		}
		return RAGGenerationResult{Refusal: &refusal, Generation: result}, nil
	}
	if result.Runtime.Schema.ID != domain.RAGAnswerSchemaID || result.Runtime.Schema.Version != domain.OutputSchemaVersionV2 {
		return RAGGenerationResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("rag generation returned an unexpected schema"))
	}
	answer, decodeErr := domain.DecodeRAGAnswerV2(result.Output, domain.DefaultDecodeLimits())
	if decodeErr != nil {
		return RAGGenerationResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, fmt.Errorf("rag v2 generation is invalid: %w", decodeErr))
	}
	if answer.ModelRunRef != request.ModelRunRef {
		return RAGGenerationResult{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("rag v2 generation is not bound to the model run"))
	}
	return RAGGenerationResult{Answer: &answer, Generation: result}, nil
}

var _ RAGGenerationPort = structuredRAGGeneration{}

func cloneRAGGenerationContext(input RAGGenerationContext) RAGGenerationContext {
	cloned := RAGGenerationContext{
		ModelRunRef: input.ModelRunRef,
		Request:     append(json.RawMessage(nil), input.Request...),
	}
	if input.Evidence != nil {
		cloned.Evidence = make([]domain.Evidence, len(input.Evidence))
		copy(cloned.Evidence, input.Evidence)
	}
	if input.Conflicts != nil {
		cloned.Conflicts = make([]RAGConflictDisclosure, len(input.Conflicts))
		copy(cloned.Conflicts, input.Conflicts)
	}
	for index := range cloned.Evidence {
		if input.Evidence[index].ConflictIDs != nil {
			cloned.Evidence[index].ConflictIDs = make([]foundation.ID, len(input.Evidence[index].ConflictIDs))
			copy(cloned.Evidence[index].ConflictIDs, input.Evidence[index].ConflictIDs)
		}
	}
	for index := range cloned.Conflicts {
		if input.Conflicts[index].ConflictIDs != nil {
			cloned.Conflicts[index].ConflictIDs = make([]foundation.ID, len(input.Conflicts[index].ConflictIDs))
			copy(cloned.Conflicts[index].ConflictIDs, input.Conflicts[index].ConflictIDs)
		}
		if input.Conflicts[index].Applicability != nil {
			cloned.Conflicts[index].Applicability = make(json.RawMessage, len(input.Conflicts[index].Applicability))
			copy(cloned.Conflicts[index].Applicability, input.Conflicts[index].Applicability)
		}
		if input.Conflicts[index].CitationIDs != nil {
			cloned.Conflicts[index].CitationIDs = make([]string, len(input.Conflicts[index].CitationIDs))
			copy(cloned.Conflicts[index].CitationIDs, input.Conflicts[index].CitationIDs)
		}
	}
	if input.RelatedTopics != nil {
		cloned.RelatedTopics = make(map[foundation.ID]RAGAllowedTopic, len(input.RelatedTopics))
		for topicID, topic := range input.RelatedTopics {
			if topic.CitationIDs != nil {
				topic.CitationIDs = make([]string, len(topic.CitationIDs))
				copy(topic.CitationIDs, input.RelatedTopics[topicID].CitationIDs)
			}
			cloned.RelatedTopics[topicID] = topic
		}
	}
	return cloned
}

// Publication 执行 Citation/Faithfulness 门禁节点，并把终态类型交给 Graph。
func (run *RAGPhaseRun) Publication(ctx context.Context) (RAGPhaseOutcome, error) {
	return run.runNode(ctx, func() bool { return run.generationDone && !run.publicationDone }, func() { run.publicationDone = true }, func() (RAGPhaseOutcome, error) {
		answer := run.answer
		base := domain.RAGAnswerResult{ResultType: answer.ResultType, SchemaID: answer.SchemaID, SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: answer.ModelRunRef, Payload: answer.Payload.RAGAnswerPayload}
		publication, err := run.executor.publisher.Publish(ctx, AnswerPublicationRequest{
			WorkspaceID: run.request.WorkspaceID, Answer: base, Retrieval: run.eligible,
			ReviewProfileRef: run.request.ReviewProfileRef, ReviewPromptRef: run.request.ReviewPromptRef, ReviewSchemaRef: run.request.ReviewSchemaRef,
		})
		if err != nil {
			return RAGPhaseOutcome{}, err
		}
		if publication.Refusal != nil {
			if err := run.finishValidation(ctx, RAGTerminalProposal{ModelRunRef: run.request.ModelRunRef, Refusal: publication.Refusal, Review: publication.Review, Retrieval: &run.summary, Generation: &run.generation}); err != nil {
				return RAGPhaseOutcome{}, err
			}
			return RAGPhaseOutcome{Route: RAGPhaseRefusal}, nil
		}
		if !publication.Publishable() {
			return RAGPhaseOutcome{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("publication gate returned no terminal decision"))
		}
		if err := run.finishValidation(ctx, RAGTerminalProposal{ModelRunRef: run.request.ModelRunRef, Answer: &run.answer, Review: publication.Review, Retrieval: &run.summary, Generation: &run.generation}); err != nil {
			return RAGPhaseOutcome{}, err
		}
		return RAGPhaseOutcome{Route: RAGPhaseAnswer}, nil
	})
}

func (run *RAGPhaseRun) finishValidation(ctx context.Context, proposal RAGTerminalProposal) error {
	if err := run.executor.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressValidationCompleted, run.summary)); err != nil {
		return err
	}
	run.finish(proposal)
	return nil
}

func (run *RAGPhaseRun) finish(proposal RAGTerminalProposal) {
	run.proposal = proposal
	run.completed = true
}

func (run *RAGPhaseRun) fail(err error) error {
	if err == nil {
		err = applicationError(foundation.ErrorConsistencyViolation, ErrorCodeRAGSchedulerContract, false, errors.New("rag phase run failed without an error"))
	}
	if run.failure == nil {
		run.failure = err
	}
	return run.failure
}

// RAGAllowedTopic 是进入不可信模型输入的服务端 Topic allowlist。
type RAGAllowedTopic struct {
	Name        string   `json:"name"`
	CitationIDs []string `json:"citation_ids"`
}

// RAGConflictDisclosure 是服务端提供给模型精确复制的 disputed Claim 事实。
type RAGConflictDisclosure struct {
	ClaimID       foundation.ID   `json:"claim_id"`
	ConflictIDs   []foundation.ID `json:"conflict_ids"`
	Applicability json.RawMessage `json:"applicability"`
	UpdatedAt     time.Time       `json:"updated_at"`
	CitationIDs   []string        `json:"citation_ids"`
}

func (e *RAGExecutor) retrieve(ctx context.Context, request RAGExecutionRequest, rewrites []string) (RetrievalBatch, RAGRetrievalSummary, error) {
	var merged RetrievalBatch
	summary := initialRetrievalSummary(request)
	summary.Rewrites = append([]string(nil), rewrites...)
	seen := make(map[string]struct{})
	seenChunks := make(map[foundation.ID]struct{})
	for _, rewrite := range rewrites {
		result, err := e.search.Search(ctx, retrievaldomain.SearchRequest{WorkspaceID: request.WorkspaceID, Query: rewrite, Mode: request.SearchMode, Filter: request.Filter, Limit: retrievaldomain.MaxSearchLimit})
		if err != nil {
			return RetrievalBatch{}, summary, err
		}
		searchResult, retrieval := result.SearchResult, result.RetrievalBatch
		if searchResult.RequestedMode != request.SearchMode || retrieval.WorkspaceID != request.WorkspaceID || retrieval.IndexVersionID != searchResult.IndexVersionID || !sameID(retrieval.EmbeddingVersionID, searchResult.EmbeddingVersionID) {
			return RetrievalBatch{}, summary, driftError()
		}
		if merged.WorkspaceID == "" {
			merged = RetrievalBatch{WorkspaceID: request.WorkspaceID, IndexVersionID: searchResult.IndexVersionID, EmbeddingVersionID: cloneID(searchResult.EmbeddingVersionID)}
			summary.EffectiveMode, summary.IndexVersionID, summary.EmbeddingVersionID = searchResult.EffectiveMode, searchResult.IndexVersionID, cloneID(searchResult.EmbeddingVersionID)
			summary.Degradations = append([]retrievaldomain.SearchDegradation{}, searchResult.Degradations...)
		} else if merged.IndexVersionID != searchResult.IndexVersionID || !sameID(merged.EmbeddingVersionID, searchResult.EmbeddingVersionID) || summary.EffectiveMode != searchResult.EffectiveMode || !slices.Equal(summary.Degradations, searchResult.Degradations) {
			return RetrievalBatch{}, summary, driftError()
		}
		for _, item := range retrieval.Items {
			seenChunks[item.Citation.ChunkID] = struct{}{}
			if _, ok := seen[item.Citation.ID]; ok {
				continue
			}
			seen[item.Citation.ID] = struct{}{}
			merged.Items = append(merged.Items, item)
		}
	}
	summary.CandidateCount = len(seenChunks)
	if len(merged.Items) > int(MaxRetrievalCandidates) {
		merged.Items = merged.Items[:int(MaxRetrievalCandidates)]
		merged.Truncated = true
	}
	return merged, summary, nil
}

func (e *RAGExecutor) projectEligibility(ctx context.Context, workspaceID foundation.ID, batch RetrievalBatch) (RetrievalBatch, []domain.Evidence, []knowledgedomain.ProvenanceRef, []RAGConflictDisclosure, bool, error) {
	refs := make([]knowledgedomain.ProvenanceRef, 0, len(batch.Items))
	seen := map[knowledgedomain.ProvenanceRef]struct{}{}
	for _, item := range batch.Items {
		ref := provenance(item.Citation)
		if _, ok := seen[ref]; !ok {
			seen[ref] = struct{}{}
			refs = append(refs, ref)
		}
	}
	results, err := e.eligibility.CheckEvidenceEligibility(ctx, knowledgedomain.EvidenceEligibilityQuery{WorkspaceID: workspaceID, Provenance: refs})
	if err != nil {
		return RetrievalBatch{}, nil, nil, nil, false, err
	}
	byRef := make(map[knowledgedomain.ProvenanceRef]knowledgedomain.ProvenanceEligibility, len(results))
	for _, result := range results {
		if result.Provenance.WorkspaceID != workspaceID || knowledgedomain.ValidateProvenanceEligibility(result) != nil {
			return RetrievalBatch{}, nil, nil, nil, false, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("eligibility returned an invalid result"))
		}
		if _, duplicate := byRef[result.Provenance]; duplicate {
			return RetrievalBatch{}, nil, nil, nil, false, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("eligibility returned duplicate results"))
		}
		if _, expected := seen[result.Provenance]; !expected {
			return RetrievalBatch{}, nil, nil, nil, false, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("eligibility returned an unexpected provenance"))
		}
		byRef[result.Provenance] = result
	}
	if len(byRef) != len(refs) {
		return RetrievalBatch{}, nil, nil, nil, false, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("eligibility result set is incomplete"))
	}
	selected := RetrievalBatch{WorkspaceID: batch.WorkspaceID, IndexVersionID: batch.IndexVersionID, EmbeddingVersionID: cloneID(batch.EmbeddingVersionID)}
	evidence := make([]domain.Evidence, 0)
	selectedRefs := make([]knowledgedomain.ProvenanceRef, 0)
	selectedRefSet := make(map[knowledgedomain.ProvenanceRef]struct{})
	conflictClaims := make(map[foundation.ID]map[foundation.ID]struct{})
	disclosures := make(map[foundation.ID]RAGConflictDisclosure)
	for _, item := range batch.Items {
		result, ok := byRef[provenance(item.Citation)]
		if !ok || result.Eligibility == knowledgedomain.EvidenceIneligible {
			continue
		}
		selected.Items = append(selected.Items, item)
		if _, exists := selectedRefSet[result.Provenance]; !exists {
			selectedRefSet[result.Provenance] = struct{}{}
			selectedRefs = append(selectedRefs, result.Provenance)
		}
		projected, err := domain.EvidenceFromKnowledgeEligibility(item.Citation, item.SearchExcerpt, result)
		if err != nil {
			return RetrievalBatch{}, nil, nil, nil, false, err
		}
		evidence = append(evidence, projected)
		for _, binding := range result.Bindings {
			if binding.OwnerType == knowledgedomain.EvidenceOwnerClaim && binding.ClaimStatus == knowledgedomain.ClaimStatusDisputed {
				disclosure := disclosures[binding.OwnerID]
				if disclosure.ClaimID != "" && (!bytes.Equal(disclosure.Applicability, binding.DisputedApplicability.CanonicalJSON) || !disclosure.UpdatedAt.Equal(binding.DisputedClaimUpdatedAtUTC)) {
					return RetrievalBatch{}, nil, nil, nil, false, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("disputed claim facts drift across eligibility bindings"))
				}
				disclosure.ClaimID = binding.OwnerID
				disclosure.Applicability = append(json.RawMessage(nil), binding.DisputedApplicability.CanonicalJSON...)
				disclosure.UpdatedAt = binding.DisputedClaimUpdatedAtUTC
				disclosure.CitationIDs = append(disclosure.CitationIDs, item.Citation.ID)
				disclosure.ConflictIDs = append(disclosure.ConflictIDs, binding.ConflictIDs...)
				disclosures[binding.OwnerID] = disclosure
				for _, conflictID := range binding.ConflictIDs {
					if conflictClaims[conflictID] == nil {
						conflictClaims[conflictID] = map[foundation.ID]struct{}{}
					}
					conflictClaims[conflictID][binding.OwnerID] = struct{}{}
				}
			}
		}
	}
	unconditionable := false
	for _, claims := range conflictClaims {
		if len(claims) < 2 {
			unconditionable = true
		}
	}
	orderedDisclosures := make([]RAGConflictDisclosure, 0, len(disclosures))
	for _, disclosure := range disclosures {
		slices.Sort(disclosure.CitationIDs)
		disclosure.CitationIDs = slices.Compact(disclosure.CitationIDs)
		slices.Sort(disclosure.ConflictIDs)
		disclosure.ConflictIDs = slices.Compact(disclosure.ConflictIDs)
		orderedDisclosures = append(orderedDisclosures, disclosure)
	}
	slices.SortFunc(orderedDisclosures, func(left, right RAGConflictDisclosure) int {
		return strings.Compare(string(left.ClaimID), string(right.ClaimID))
	})
	return selected, evidence, selectedRefs, orderedDisclosures, unconditionable, nil
}
func topicAllowlist(bindings []knowledgedomain.EvidenceTopicBinding, batch RetrievalBatch) (map[foundation.ID]RAGAllowedTopic, error) {
	allowed := map[foundation.ID]RAGAllowedTopic{}
	citations := map[knowledgedomain.ProvenanceRef][]string{}
	for _, item := range batch.Items {
		ref := provenance(item.Citation)
		citations[ref] = append(citations[ref], item.Citation.ID)
	}
	for _, binding := range bindings {
		citationIDs, exists := citations[binding.Provenance]
		if !exists || knowledgedomain.ValidateEvidenceTopicBinding(binding) != nil {
			return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGTopicMismatch, false, errors.New("knowledge returned an invalid or unrelated topic binding"))
		}
		v := allowed[binding.TopicID]
		if v.Name != "" && v.Name != binding.TopicName {
			return nil, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGTopicMismatch, false, errors.New("knowledge returned conflicting names for one topic"))
		}
		v.Name = binding.TopicName
		v.CitationIDs = append(v.CitationIDs, citationIDs...)
		slices.Sort(v.CitationIDs)
		v.CitationIDs = slices.Compact(v.CitationIDs)
		allowed[binding.TopicID] = v
	}
	return allowed, nil
}
func validateRelatedTopics(topics []domain.RelatedTopic, allowed map[foundation.ID]RAGAllowedTopic) error {
	for _, topic := range topics {
		candidate, ok := allowed[topic.TopicID]
		if !ok || candidate.Name != topic.Name {
			return applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGTopicMismatch, false, errors.New("model returned a topic outside the server allowlist"))
		}
		for _, citationID := range topic.CitationIDs {
			if !slices.Contains(candidate.CitationIDs, citationID) {
				return applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGTopicMismatch, false, errors.New("model returned an unbound topic citation"))
			}
		}
	}
	return nil
}
func refusalProposal(run foundation.ID, code domain.RefusalReasonCode, summary string, retrieval *RAGRetrievalSummary) RAGTerminalProposal {
	p := newRefusalPayload(code, summary)
	r := domain.RefusalResult{ResultType: domain.ResultTypeRefusal, SchemaID: domain.RefusalSchemaID, SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: run, Payload: p}
	return RAGTerminalProposal{ModelRunRef: run, Refusal: &r, Retrieval: retrieval}
}
func provenance(c domain.Citation) knowledgedomain.ProvenanceRef {
	return knowledgedomain.ProvenanceRef{WorkspaceID: c.WorkspaceID, SourceVersionID: c.SourceVersionID, SourceSpanID: c.SourceSpanID}
}
func cloneID(v *foundation.ID) *foundation.ID {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func sameID(a, b *foundation.ID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func driftError() error {
	return applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGResultDrift, false, errors.New("retrieval tuple or degradation drifted across rewrites"))
}

func initialRetrievalSummary(request RAGExecutionRequest) RAGRetrievalSummary {
	return RAGRetrievalSummary{
		Rewrites:             []string{},
		RequestedMode:        request.SearchMode,
		EffectiveMode:        request.SearchMode,
		Filter:               request.Filter,
		AllowOriginalSources: request.AllowOriginalSources,
		AllowWeb:             request.AllowWeb,
		Degradations:         []retrievaldomain.SearchDegradation{},
	}
}

func uniqueConflictCount(disclosures []RAGConflictDisclosure) int {
	conflicts := make(map[foundation.ID]struct{})
	for _, disclosure := range disclosures {
		for _, conflictID := range disclosure.ConflictIDs {
			conflicts[conflictID] = struct{}{}
		}
	}
	return len(conflicts)
}

func progressFromSummary(stage RAGProgressStage, summary RAGRetrievalSummary) RAGProgressUpdate {
	return RAGProgressUpdate{
		Stage: stage, RewriteCount: len(summary.Rewrites), CandidateCount: summary.CandidateCount,
		SelectedCount: summary.SelectedCount, ConflictCount: summary.ConflictCount,
		DegradationCount: len(summary.Degradations),
	}
}

func (e *RAGExecutor) recordPostProviderProgress(ctx context.Context, update RAGProgressUpdate) error {
	if err := e.progress.RecordRAGProgress(ctx, update); err != nil {
		return foundation.NewError(foundation.ErrorManualRecoveryRequired, ErrorCodeRAGProgressUnknown, false, err)
	}
	return nil
}
