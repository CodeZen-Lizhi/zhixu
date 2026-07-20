package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// RAGExecutor 编排 retrieval-first RAG，不拥有任何持久化终结职责。
type RAGExecutor struct {
	planner     RAGQueryPlanPort
	search      ScopedRetrievalPort
	eligibility EvidenceEligibilityPort
	topics      RAGEvidenceTopicPort
	runner      RAGStructuredRunnerPort
	publisher   RAGPublicationGatePort
	progress    RAGProgressPort
}

// NewRAGExecutor 创建 fail-closed 的 RAG 编排器。
func NewRAGExecutor(planner RAGQueryPlanPort, search ScopedRetrievalPort, eligibility EvidenceEligibilityPort, topics RAGEvidenceTopicPort, runner RAGStructuredRunnerPort, publisher RAGPublicationGatePort, progress RAGProgressPort) (*RAGExecutor, error) {
	if isNilPort(planner) || isNilPort(search) || isNilPort(eligibility) || isNilPort(topics) || isNilPort(runner) || isNilPort(publisher) || isNilPort(progress) {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, errorCodeRAGExecutorMissing, false, errors.New("rag executor dependencies are required"))
	}
	return &RAGExecutor{planner: planner, search: search, eligibility: eligibility, topics: topics, runner: runner, publisher: publisher, progress: progress}, nil
}

// Execute 生成 canonical terminal proposal；调用方必须交给 T09 在一个事务中终结。
func (e *RAGExecutor) Execute(ctx context.Context, request RAGExecutionRequest) (RAGTerminalProposal, error) {
	if e == nil || isNilPort(e.planner) || isNilPort(e.search) || isNilPort(e.eligibility) || isNilPort(e.topics) || isNilPort(e.runner) || isNilPort(e.publisher) || isNilPort(e.progress) {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorDependencyUnavailable, errorCodeRAGExecutorMissing, false, errors.New("rag executor is unavailable"))
	}
	if ctx == nil {
		ctx = context.Background()
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
	if err := e.progress.RecordRAGProgress(ctx, RAGProgressUpdate{Stage: RAGProgressPlanStarted}); err != nil {
		return RAGTerminalProposal{}, err
	}
	planRun, err := e.planner.Plan(ctx, QueryPlanRequest{ModelRunRef: request.ModelRunRef, ProfileRef: request.PlanProfileRef, PromptRef: request.PlanPromptRef, SchemaRef: request.PlanSchemaRef, Input: request.PlanInput})
	if err != nil {
		return RAGTerminalProposal{}, err
	}
	if err := e.recordPostProviderProgress(ctx, RAGProgressUpdate{Stage: RAGProgressPlanCompleted, RewriteCount: len(planRun.Plan.Payload.Rewrites)}); err != nil {
		return RAGTerminalProposal{}, err
	}
	if planRun.Plan.Payload.RequiresClarification {
		payload := planRun.Plan.Payload
		summary := initialRetrievalSummary(request)
		return RAGTerminalProposal{ModelRunRef: request.ModelRunRef, Clarification: &RAGClarificationProposal{Intent: payload.Intent, Reason: payload.ClarificationReason, Question: payload.ClarificationQuestion, SuggestedScopes: append([]string(nil), payload.SuggestedScopes...)}, Retrieval: &summary}, nil
	}

	if err := e.recordPostProviderProgress(ctx, RAGProgressUpdate{Stage: RAGProgressRetrievalStarted, RewriteCount: len(planRun.Plan.Payload.Rewrites)}); err != nil {
		return RAGTerminalProposal{}, err
	}
	merged, summary, err := e.retrieve(ctx, request, planRun.Plan.Payload.Rewrites)
	if err != nil {
		return RAGTerminalProposal{}, err
	}
	if err := e.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressRetrievalCompleted, summary)); err != nil {
		return RAGTerminalProposal{}, err
	}
	if len(merged.Items) == 0 {
		return refusalProposal(request.ModelRunRef, domain.RefusalNoRelevantEvidence, "no relevant evidence was found in the approved knowledge scope", &summary), nil
	}
	if err := e.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressValidationStarted, summary)); err != nil {
		return RAGTerminalProposal{}, err
	}
	eligible, evidence, provenances, conflicts, unconditionable, err := e.projectEligibility(ctx, request.WorkspaceID, merged)
	if err != nil {
		return RAGTerminalProposal{}, err
	}
	if len(eligible.Items) == 0 {
		if err := e.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressValidationCompleted, summary)); err != nil {
			return RAGTerminalProposal{}, err
		}
		return refusalProposal(request.ModelRunRef, domain.RefusalUnapprovedEvidenceOnly, "retrieval found candidates, but none are approved knowledge evidence", &summary), nil
	}
	summary.SelectedCount = len(eligible.Items)
	summary.ConflictCount = uniqueConflictCount(conflicts)
	if unconditionable {
		if err := e.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressValidationCompleted, summary)); err != nil {
			return RAGTerminalProposal{}, err
		}
		return refusalProposal(request.ModelRunRef, domain.RefusalConflictNotConditionable, "retrieved conflict evidence does not contain at least two selected disputed claim positions", &summary), nil
	}

	bindings, err := e.topics.ResolveRAGTopics(ctx, request.WorkspaceID, provenances)
	if err != nil {
		return RAGTerminalProposal{}, err
	}
	allowed, err := topicAllowlist(bindings, eligible)
	if err != nil {
		return RAGTerminalProposal{}, err
	}
	if len(allowed) == 0 {
		if err := e.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressValidationCompleted, summary)); err != nil {
			return RAGTerminalProposal{}, err
		}
		return refusalProposal(request.ModelRunRef, domain.RefusalEvidenceInsufficient, "eligible evidence has no active related topic binding", &summary), nil
	}
	input, err := json.Marshal(struct {
		ModelRunRef   foundation.ID                     `json:"model_run_ref"`
		Request       json.RawMessage                   `json:"request"`
		Evidence      []domain.Evidence                 `json:"evidence"`
		Conflicts     []RAGConflictDisclosure           `json:"conflicts"`
		RelatedTopics map[foundation.ID]RAGAllowedTopic `json:"related_topics"`
	}{request.ModelRunRef, append(json.RawMessage(nil), request.AnswerInput...), evidence, conflicts, allowed})
	if err != nil {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorNonRetryableFailure, errorCodeRAGRequestInvalid, false, err)
	}
	generation, err := e.runner.Run(ctx, StructuredRunRequest{ProfileRef: request.AnswerProfileRef, PromptRef: request.AnswerPromptRef, SchemaRef: request.AnswerSchemaRef, ReducedSchemaRef: request.AnswerReducedSchemaRef, Input: input})
	if err != nil {
		return RAGTerminalProposal{}, err
	}
	if generation.Runtime.Schema.ID == domain.RefusalSchemaID {
		if generation.Phase != domain.ModelCallReduced || generation.Runtime.Schema.Version != domain.OutputSchemaVersionV1 {
			return RAGTerminalProposal{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("refusal schema is only valid in the reduced phase"))
		}
		refusal, decodeErr := domain.DecodeRefusal(generation.Output, domain.DefaultDecodeLimits())
		if decodeErr != nil || refusal.ModelRunRef != request.ModelRunRef {
			return RAGTerminalProposal{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("reduced refusal is invalid or not bound to the model run"))
		}
		if err := e.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressValidationCompleted, summary)); err != nil {
			return RAGTerminalProposal{}, err
		}
		return RAGTerminalProposal{ModelRunRef: request.ModelRunRef, Refusal: &refusal, Retrieval: &summary, Generation: &generation}, nil
	}
	if generation.Runtime.Schema.ID != domain.RAGAnswerSchemaID || generation.Runtime.Schema.Version != domain.OutputSchemaVersionV2 {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("rag generation returned an unexpected schema"))
	}
	answer, err := domain.DecodeRAGAnswerV2(generation.Output, domain.DefaultDecodeLimits())
	if err != nil {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, fmt.Errorf("rag v2 generation is invalid: %w", err))
	}
	if answer.ModelRunRef != request.ModelRunRef {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("rag v2 generation is not bound to the model run"))
	}
	if err := validateRelatedTopics(answer.Payload.RelatedTopics, allowed); err != nil {
		return RAGTerminalProposal{}, err
	}
	base := domain.RAGAnswerResult{ResultType: answer.ResultType, SchemaID: answer.SchemaID, SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: answer.ModelRunRef, Payload: answer.Payload.RAGAnswerPayload}
	publication, err := e.publisher.Publish(ctx, AnswerPublicationRequest{WorkspaceID: request.WorkspaceID, Answer: base, Retrieval: eligible, ReviewProfileRef: request.ReviewProfileRef, ReviewPromptRef: request.ReviewPromptRef, ReviewSchemaRef: request.ReviewSchemaRef})
	if err != nil {
		return RAGTerminalProposal{}, err
	}
	if publication.Refusal != nil {
		if err := e.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressValidationCompleted, summary)); err != nil {
			return RAGTerminalProposal{}, err
		}
		return RAGTerminalProposal{ModelRunRef: request.ModelRunRef, Refusal: publication.Refusal, Review: publication.Review, Retrieval: &summary, Generation: &generation}, nil
	}
	if !publication.Publishable() {
		return RAGTerminalProposal{}, applicationError(foundation.ErrorConsistencyViolation, errorCodeRAGRequestInvalid, false, errors.New("publication gate returned no terminal decision"))
	}
	if err := e.recordPostProviderProgress(ctx, progressFromSummary(RAGProgressValidationCompleted, summary)); err != nil {
		return RAGTerminalProposal{}, err
	}
	return RAGTerminalProposal{ModelRunRef: request.ModelRunRef, Answer: &answer, Review: publication.Review, Retrieval: &summary, Generation: &generation}, nil
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
