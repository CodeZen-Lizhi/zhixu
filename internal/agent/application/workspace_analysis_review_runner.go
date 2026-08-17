package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	workspaceAnalysisReviewFinalizationTimeout = 5 * time.Second
	workspaceAnalysisReviewInputSchemaVersion  = "agent-workspace-analysis-review-input/v1"
	workspaceAnalysisReviewConclusionTargetID  = "@answer/conclusion"
	workspaceAnalysisReviewMaxEvidenceBytes    = 4 * 1024
)

// WorkspaceAnalysisReviewRunnerDependencies 是 Faithfulness Review 单次模型调用的全部项目边界。
type WorkspaceAnalysisReviewRunnerDependencies struct {
	Model      ChatModel
	Catalog    *RuntimeCatalog
	Repository WorkspaceAnalysisModelOperationRepository
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
}

// WorkspaceAnalysisReviewEvidence 是模型可见的单个短引用与有界 Source 摘录。
type WorkspaceAnalysisReviewEvidence struct {
	EvidenceRef string `json:"evidence_ref"`
	Excerpt     string `json:"excerpt"`
}

// String 只返回短引用和字节数，不返回 Source 摘录。
func (evidence WorkspaceAnalysisReviewEvidence) String() string {
	return fmt.Sprintf("WorkspaceAnalysisReviewEvidence{evidence_ref:%s bytes:%d}", evidence.EvidenceRef, len(evidence.Excerpt))
}

// GoString 避免 %#v 调试格式绕过 Source 摘录的安全 String 投影。
func (evidence WorkspaceAnalysisReviewEvidence) GoString() string { return evidence.String() }

// WorkspaceAnalysisReviewRequest 绑定当前持久 Attempt、不可变候选与按候选引用规范化的证据。
type WorkspaceAnalysisReviewRequest struct {
	Identity              WorkspaceAnalysisModelExecutionIdentity
	AnalysisRunID         foundation.ID
	Candidate             domain.WorkspaceAnalysisCandidate
	Evidence              []WorkspaceAnalysisReviewEvidence
	ModelSettingsRevision *int64
	Retrieval             domain.RetrievalRef
	ProfileRef            domain.ModelProfileRef
	PromptRef             domain.PromptRef
}

// WorkspaceAnalysisReviewResult 返回权威 Review 回执及其独立 Model Run/Call。
// Provider 文档的 model_run_ref 是被审查的 Synthesis Model Run；持久回执会改绑实际撰写 Review 的 Model Run。
type WorkspaceAnalysisReviewResult struct {
	Review      domain.FaithfulnessReviewResult
	ModelResult domain.WorkspaceAnalysisModelResult
	Run         domain.ModelRun
	Call        domain.ModelCall
	Usage       domain.TokenUsage
	Passed      bool
	Replayed    bool
}

// WorkspaceAnalysisReviewRunner 只允许 CREATED 越过 Provider；其余状态精确重放或失败关闭。
type WorkspaceAnalysisReviewRunner struct {
	model      ChatModel
	catalog    *RuntimeCatalog
	repository WorkspaceAnalysisModelOperationRepository
	ids        foundation.IDGenerator
	clock      foundation.Clock
	idMu       sync.Mutex
}

// NewWorkspaceAnalysisReviewRunner 创建单次、无 repair/retry/fallback 的 Faithfulness Review Runner。
func NewWorkspaceAnalysisReviewRunner(
	dependencies WorkspaceAnalysisReviewRunnerDependencies,
) (*WorkspaceAnalysisReviewRunner, error) {
	if isNilChatModel(dependencies.Model) || dependencies.Catalog == nil || isNilPort(dependencies.Repository) ||
		isNilPort(dependencies.IDs) || isNilPort(dependencies.Clock) {
		return nil, applicationError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable,
			false,
			errors.New("workspace analysis review dependencies are unavailable"),
		)
	}
	return &WorkspaceAnalysisReviewRunner{
		model: dependencies.Model, catalog: dependencies.Catalog, repository: dependencies.Repository,
		ids: dependencies.IDs, clock: dependencies.Clock,
	}, nil
}

// Run 原子授权 FAITHFULNESS_REVIEW Operation，并在首次创建时恰好调用一次 Provider。
func (runner *WorkspaceAnalysisReviewRunner) Run(
	ctx context.Context,
	request WorkspaceAnalysisReviewRequest,
) (WorkspaceAnalysisReviewResult, error) {
	if runner == nil || isNilChatModel(runner.model) || runner.catalog == nil || isNilPort(runner.repository) ||
		isNilPort(runner.ids) || isNilPort(runner.clock) {
		return WorkspaceAnalysisReviewResult{}, applicationError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable,
			false,
			errors.New("workspace analysis review runner is unavailable"),
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceAnalysisReviewResult{}, operationContextError(err)
	}
	if err := validateWorkspaceAnalysisReviewRequest(request); err != nil {
		return WorkspaceAnalysisReviewResult{}, err
	}

	schemaRef := domain.SchemaRef{ID: domain.FaithfulnessReviewSchemaID, Version: domain.OutputSchemaVersionV1}
	snapshot, err := runner.catalog.Snapshot(request.PromptRef, schemaRef, schemaRef, request.ProfileRef)
	if err != nil {
		return WorkspaceAnalysisReviewResult{}, err
	}
	if snapshot.Schema.Ref != schemaRef || snapshot.ReducedSchema.Ref != schemaRef ||
		snapshot.Profile.MaxOutputTokens < int(WorkspaceAnalysisV1ReviewMaxOutputTokens) {
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelError(
			errors.New("workspace analysis review runtime snapshot drifted"),
		)
	}

	generated, err := runner.newWorkspaceAnalysisReviewIDs()
	if err != nil {
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := validateWorkspaceAnalysisReviewGeneratedIDs(request, generated); err != nil {
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelError(err)
	}
	canonicalInput, err := canonicalWorkspaceAnalysisReviewInput(request.Candidate, request.Evidence)
	if err != nil {
		return WorkspaceAnalysisReviewResult{}, err
	}

	chatRequest := buildChatRequest(
		snapshot, snapshot.Schema, domain.ModelCallReview,
		snapshot.Prompt.InitialInstruction, canonicalInput, "",
	)
	chatRequest.MaxOutputTokens = int(WorkspaceAnalysisV1ReviewMaxOutputTokens)
	requestDocument, err := canonicalWorkspaceAnalysisChatRequest(chatRequest)
	if err != nil {
		return WorkspaceAnalysisReviewResult{}, err
	}
	startedAt := runner.clock.Now().UTC()
	if startedAt.IsZero() {
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelError(
			errors.New("workspace analysis review clock returned zero time"),
		)
	}
	operationKey := domain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: request.AnalysisRunID,
		NodeKey:       domain.WorkspaceAnalysisOperationNodeReviewPublish,
		Kind:          domain.WorkspaceAnalysisOperationFaithfulnessReview,
		Ordinal:       1,
	}
	run := domain.ModelRun{
		ID: generated.modelRunID, WorkspaceID: request.Identity.WorkspaceID,
		WorkflowRunID: request.Identity.WorkflowRunID, NodeRunID: request.Identity.NodeRunID,
		NodeAttemptID:         request.Identity.NodeAttemptID,
		ModelSettingsRevision: cloneWorkspaceAnalysisOptionalInt64(request.ModelSettingsRevision),
		Model:                 snapshot.Profile.Model, Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref,
		Schema: snapshot.Schema.Ref, ReducedSchema: snapshot.ReducedSchema.Ref,
		Retrieval: cloneWorkspaceAnalysisRetrieval(request.Retrieval),
		Status:    domain.ModelRunRunning, Version: 1, CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	call := domain.ModelCall{
		ID: generated.modelCallID, ModelRunID: generated.modelRunID, CallNo: 1,
		Phase: domain.ModelCallReview, Model: snapshot.Profile.Model, Profile: snapshot.Profile.Ref,
		Prompt: snapshot.Prompt.Ref, Schema: snapshot.Schema.Ref,
		MaxOutputTokens: int(WorkspaceAnalysisV1ReviewMaxOutputTokens),
		Status:          domain.ModelCallStarted, RequestHash: workspaceAnalysisSHA256(requestDocument),
		RequestBytes: int64(len(requestDocument)), Version: 1, StartedAt: startedAt,
	}
	authorizationCommand := AuthorizeWorkspaceAnalysisModelCallCommand{
		Identity: request.Identity, OperationKey: operationKey, OperationID: generated.operationID,
		ReservationID: generated.reservationID, Run: run, Call: call, RequestDocument: requestDocument,
	}
	if err := authorizationCommand.Validate(); err != nil {
		return WorkspaceAnalysisReviewResult{}, err
	}
	authorized, err := runner.repository.AuthorizeWorkspaceAnalysisModelCall(ctx, authorizationCommand)
	if err != nil {
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := authorized.ValidateFor(authorizationCommand); err != nil {
		return WorkspaceAnalysisReviewResult{}, err
	}

	switch authorized.Disposition {
	case WorkspaceAnalysisModelAuthorizationReuseResult:
		return runner.loadWorkspaceAnalysisReviewResult(ctx, request, authorized)
	case WorkspaceAnalysisModelAuthorizationReconcile:
		return WorkspaceAnalysisReviewResult{}, applicationError(
			foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallReplayUnsafe, false,
			errors.New("workspace analysis review call requires exact reconciliation"),
		)
	case WorkspaceAnalysisModelAuthorizationReplayFailure:
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelTerminalError(
			workspaceAnalysisReplayedModelFailure(authorized.Call, authorized.Run), authorized.OperationID, authorized.Call, authorized.Run,
		)
	case WorkspaceAnalysisModelAuthorizationTerminateUnknown:
		terminal := applicationError(
			foundation.ErrorManualRecoveryRequired,
			stableTerminalErrorCode(authorized.Call.ErrorCode, nil, ErrorCodeModelCallPersistenceUnknown),
			false, errors.New("workspace analysis review outcome is unknown"),
		)
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelTerminalError(
			terminal, authorized.OperationID, authorized.Call, authorized.Run,
		)
	case WorkspaceAnalysisModelAuthorizationCreated:
		// Only this branch may cross the Provider boundary.
	default:
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelAuthorizationError(
			errors.New("workspace analysis review authorization disposition is unsupported"),
		)
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceAnalysisReviewResult{}, runner.finalizeWorkspaceAnalysisReviewFailure(
			ctx, authorizationCommand, authorized, ChatResponse{}, operationContextError(err),
		)
	}

	callContext, cancel := context.WithTimeout(ctx, snapshot.Profile.Timeout)
	response, callErr := runner.model.Chat(callContext, cloneChatRequest(chatRequest))
	contextErr := callContext.Err()
	cancel()
	if callErr == nil && contextErr != nil {
		callErr = operationContextError(contextErr)
	}
	callErr = validateWorkspaceAnalysisModelProviderOutcome(chatRequest, response, callErr)
	if callErr != nil {
		return WorkspaceAnalysisReviewResult{}, runner.finalizeWorkspaceAnalysisReviewFailure(
			ctx, authorizationCommand, authorized, response, callErr,
		)
	}
	providerReview, err := decodeWorkspaceAnalysisProviderReview(
		response.Content, snapshot.Schema, request.Candidate.SynthesisModelRunID,
	)
	if err != nil {
		return WorkspaceAnalysisReviewResult{}, runner.finalizeWorkspaceAnalysisReviewFailure(
			ctx, authorizationCommand, authorized, response, err,
		)
	}
	if err := validateWorkspaceAnalysisReviewSemantics(
		providerReview, request.Candidate, request.Candidate.SynthesisModelRunID,
	); err != nil {
		return WorkspaceAnalysisReviewResult{}, runner.finalizeWorkspaceAnalysisReviewFailure(
			ctx, authorizationCommand, authorized, response, err,
		)
	}
	if workspaceAnalysisReviewPayloadLeaksIdentity(providerReview.Payload, request, generated, authorized) {
		return WorkspaceAnalysisReviewResult{}, runner.finalizeWorkspaceAnalysisReviewFailure(
			ctx, authorizationCommand, authorized, response,
			workspaceAnalysisModelError(errors.New("workspace analysis review output contains a server identity")),
		)
	}
	review, document, err := composeCanonicalWorkspaceAnalysisReview(providerReview, authorized.Run.ID)
	if err != nil {
		return WorkspaceAnalysisReviewResult{}, runner.finalizeWorkspaceAnalysisReviewFailure(
			ctx, authorizationCommand, authorized, response, err,
		)
	}
	return runner.finalizeWorkspaceAnalysisReviewSuccess(
		ctx, authorizationCommand, authorized, generated.modelResultID,
		request.Candidate, review, document, response.Usage,
	)
}

type workspaceAnalysisReviewIDs struct {
	operationID   foundation.ID
	reservationID foundation.ID
	modelRunID    foundation.ID
	modelCallID   foundation.ID
	modelResultID foundation.ID
}

func (runner *WorkspaceAnalysisReviewRunner) newWorkspaceAnalysisReviewIDs() (workspaceAnalysisReviewIDs, error) {
	runner.idMu.Lock()
	defer runner.idMu.Unlock()
	values := make([]foundation.ID, 5)
	for index := range values {
		value, err := runner.ids.New()
		if err != nil {
			return workspaceAnalysisReviewIDs{}, err
		}
		values[index] = value
	}
	return workspaceAnalysisReviewIDs{
		operationID: values[0], reservationID: values[1], modelRunID: values[2],
		modelCallID: values[3], modelResultID: values[4],
	}, nil
}

func validateWorkspaceAnalysisReviewRequest(request WorkspaceAnalysisReviewRequest) error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	operationKey := domain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: request.AnalysisRunID,
		NodeKey:       domain.WorkspaceAnalysisOperationNodeReviewPublish,
		Kind:          domain.WorkspaceAnalysisOperationFaithfulnessReview,
		Ordinal:       1,
	}
	if request.Identity.NodeKey != domain.WorkspaceAnalysisOperationNodeReviewPublish || operationKey.Validate() != nil ||
		domain.ValidateWorkspaceAnalysisCandidate(request.Candidate) != nil ||
		request.Candidate.WorkspaceID != request.Identity.WorkspaceID || request.Candidate.AnalysisRunID != request.AnalysisRunID ||
		request.ProfileRef.Validate() != nil || request.PromptRef.Validate() != nil || request.Retrieval.Validate() != nil ||
		(request.ModelSettingsRevision != nil && *request.ModelSettingsRevision < 0) {
		return workspaceAnalysisModelError(errors.New("workspace analysis review request is invalid"))
	}
	candidateResult, err := decodeWorkspaceAnalysisReviewCandidate(request.Candidate)
	if err != nil || validateWorkspaceAnalysisReviewEvidence(candidateResult.Payload.CitationRefs, request.Evidence) != nil {
		return workspaceAnalysisModelError(errors.New("workspace analysis review evidence is invalid"))
	}
	return nil
}

func canonicalWorkspaceAnalysisReviewInput(
	candidate domain.WorkspaceAnalysisCandidate,
	evidence []WorkspaceAnalysisReviewEvidence,
) ([]byte, error) {
	candidateResult, err := decodeWorkspaceAnalysisReviewCandidate(candidate)
	if err != nil {
		return nil, workspaceAnalysisModelError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis review candidate cannot be decoded"),
		)
	}
	if err := validateWorkspaceAnalysisReviewEvidence(candidateResult.Payload.CitationRefs, evidence); err != nil {
		return nil, err
	}
	input := struct {
		SchemaVersion string                                `json:"schema_version"`
		ModelRunRef   foundation.ID                         `json:"model_run_ref"`
		Candidate     workspaceAnalysisReviewCandidateInput `json:"candidate"`
		ReviewTargets []faithfulnessReviewTarget            `json:"review_targets"`
		Evidence      []WorkspaceAnalysisReviewEvidence     `json:"evidence"`
	}{
		SchemaVersion: workspaceAnalysisReviewInputSchemaVersion,
		ModelRunRef:   candidate.SynthesisModelRunID,
		Candidate: workspaceAnalysisReviewCandidateInput{
			AnswerMarkdown: candidateResult.Payload.AnswerMarkdown,
			CitationRefs:   append([]string(nil), candidateResult.Payload.CitationRefs...),
		},
		ReviewTargets: []faithfulnessReviewTarget{{
			ID: workspaceAnalysisReviewConclusionTargetID, Text: candidateResult.Payload.AnswerMarkdown,
			Kind: domain.AssertionFactual, CitationIDs: append([]string(nil), candidateResult.Payload.CitationRefs...),
		}},
		Evidence: cloneWorkspaceAnalysisReviewEvidence(evidence),
	}
	canonical, err := json.Marshal(input)
	if err != nil || len(canonical) == 0 || len(canonical) > MaxStructuredInputBytes ||
		bytes.Contains(canonical, []byte(candidate.ID)) || bytes.Contains(canonical, []byte(candidate.DocumentHash)) {
		return nil, workspaceAnalysisModelError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis review input cannot be canonicalized safely"),
		)
	}
	return canonical, nil
}

type workspaceAnalysisReviewCandidateInput struct {
	AnswerMarkdown string   `json:"answer_markdown"`
	CitationRefs   []string `json:"citation_refs"`
}

func decodeWorkspaceAnalysisReviewCandidate(
	candidate domain.WorkspaceAnalysisCandidate,
) (domain.WorkspaceAnalysisCandidateResult, error) {
	limits := domain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(domain.MaxWorkspaceAnalysisCandidateBytes)
	limits.MaxStringBytes = int(domain.MaxWorkspaceAnalysisCandidateBytes)
	result, err := domain.DecodeWorkspaceAnalysisCandidate(candidate.Document, limits)
	if err != nil || result.ModelRunRef != candidate.SynthesisModelRunID {
		return domain.WorkspaceAnalysisCandidateResult{}, errOrWorkspaceAnalysisPlan(
			err, "workspace analysis review candidate is invalid",
		)
	}
	return result, nil
}

func validateWorkspaceAnalysisReviewEvidence(
	expectedRefs []string,
	evidence []WorkspaceAnalysisReviewEvidence,
) error {
	if len(expectedRefs) < 1 || len(expectedRefs) > int(WorkspaceAnalysisV1MaxSourceReads) ||
		len(evidence) != len(expectedRefs) {
		return workspaceAnalysisModelError(errors.New("workspace analysis review evidence set is incomplete"))
	}
	for index, item := range evidence {
		if item.EvidenceRef != expectedRefs[index] || strings.TrimSpace(item.Excerpt) == "" ||
			len(item.Excerpt) > workspaceAnalysisReviewMaxEvidenceBytes || !utf8.ValidString(item.Excerpt) ||
			strings.ContainsRune(item.Excerpt, '\x00') {
			return workspaceAnalysisModelError(errors.New("workspace analysis review evidence item is invalid"))
		}
	}
	return nil
}

func cloneWorkspaceAnalysisReviewEvidence(values []WorkspaceAnalysisReviewEvidence) []WorkspaceAnalysisReviewEvidence {
	return append([]WorkspaceAnalysisReviewEvidence(nil), values...)
}

func decodeWorkspaceAnalysisProviderReview(
	raw []byte,
	schema SchemaDefinition,
	expectedModelRunRef foundation.ID,
) (domain.FaithfulnessReviewResult, error) {
	if len(raw) == 0 || int64(len(raw)) > domain.MaxWorkspaceAnalysisModelResultBytes {
		return domain.FaithfulnessReviewResult{}, applicationError(
			foundation.ErrorConsistencyViolation, domain.ErrorCodeStructuredOutputLimitExceeded, false,
			errors.New("workspace analysis provider review exceeds the bounded result limit"),
		)
	}
	decoded, err := schema.Decode(append([]byte(nil), raw...))
	if err != nil {
		return domain.FaithfulnessReviewResult{}, err
	}
	if !bytes.Equal(decoded, raw) {
		return domain.FaithfulnessReviewResult{}, applicationError(
			foundation.ErrorConsistencyViolation, errorCodeDecoderContract, false,
			errors.New("workspace analysis review decoder transformed provider output"),
		)
	}
	limits := domain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(domain.MaxWorkspaceAnalysisModelResultBytes)
	review, err := domain.DecodeFaithfulnessReview(raw, limits)
	if err != nil || review.ModelRunRef != expectedModelRunRef {
		return domain.FaithfulnessReviewResult{}, workspaceAnalysisModelError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis provider review subject drifted"),
		)
	}
	return review, nil
}

func composeCanonicalWorkspaceAnalysisReview(
	provider domain.FaithfulnessReviewResult,
	modelRunID foundation.ID,
) (domain.FaithfulnessReviewResult, []byte, error) {
	review := provider
	review.ModelRunRef = modelRunID
	if err := review.Validate(); err != nil {
		return domain.FaithfulnessReviewResult{}, nil, err
	}
	document, err := json.Marshal(review)
	if err != nil || len(document) == 0 || int64(len(document)) > domain.MaxWorkspaceAnalysisModelResultBytes {
		return domain.FaithfulnessReviewResult{}, nil, workspaceAnalysisModelError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis review result cannot be encoded"),
		)
	}
	decoded, err := domain.DecodeFaithfulnessReview(document, domain.DefaultDecodeLimits())
	if err != nil || !reflect.DeepEqual(decoded, review) {
		return domain.FaithfulnessReviewResult{}, nil, workspaceAnalysisModelError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis review canonical round trip drifted"),
		)
	}
	return review, document, nil
}

func validateWorkspaceAnalysisReviewSemantics(
	review domain.FaithfulnessReviewResult,
	candidate domain.WorkspaceAnalysisCandidate,
	expectedModelRunRef foundation.ID,
) error {
	if err := review.Validate(); err != nil || review.ModelRunRef != expectedModelRunRef {
		return workspaceAnalysisModelError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis review envelope is invalid"),
		)
	}
	candidateResult, err := decodeWorkspaceAnalysisReviewCandidate(candidate)
	if err != nil {
		return workspaceAnalysisModelError(err)
	}
	if len(review.Payload.Items) != 1 {
		return workspaceAnalysisModelError(errors.New("workspace analysis review must cover the exact conclusion target"))
	}
	item := review.Payload.Items[0]
	if item.AssertionID != workspaceAnalysisReviewConclusionTargetID ||
		!sameReferenceSet(item.CitationIDs, candidateResult.Payload.CitationRefs) {
		return workspaceAnalysisModelError(errors.New("workspace analysis review target or citation set drifted"))
	}
	switch item.Verdict {
	case domain.FaithfulnessSupported:
		if !review.Payload.Passed {
			return workspaceAnalysisModelError(errors.New("workspace analysis supported review did not pass"))
		}
	case domain.FaithfulnessUnsupported:
		if review.Payload.Passed {
			return workspaceAnalysisModelError(errors.New("workspace analysis unsupported review cannot pass"))
		}
	default:
		return workspaceAnalysisModelError(errors.New("workspace analysis review verdict is not allowed for a factual conclusion"))
	}
	return nil
}

func (runner *WorkspaceAnalysisReviewRunner) finalizeWorkspaceAnalysisReviewSuccess(
	ctx context.Context,
	authorizationCommand AuthorizeWorkspaceAnalysisModelCallCommand,
	authorized WorkspaceAnalysisModelAuthorizationResult,
	resultID foundation.ID,
	candidate domain.WorkspaceAnalysisCandidate,
	review domain.FaithfulnessReviewResult,
	document []byte,
	usage domain.TokenUsage,
) (WorkspaceAnalysisReviewResult, error) {
	completedAt := workspaceAnalysisPlanCompletedAt(runner.clock.Now().UTC(), authorized.Run, authorized.Call)
	if completedAt.Before(candidate.CreatedAt) {
		completedAt = candidate.CreatedAt
	}
	call := authorized.Call
	call.Status = domain.ModelCallSucceeded
	call.ResponseHash = workspaceAnalysisSHA256(document)
	call.ResponseBytes = int64(len(document))
	call.Usage = usage
	call.LatencyMillis = completedAt.Sub(call.StartedAt).Milliseconds()
	call.Version++
	call.CompletedAt = &completedAt
	run := authorized.Run
	run.Status = domain.ModelRunSucceeded
	run.FinalResultType = domain.ResultTypeFaithfulnessReview
	run.Version++
	run.UpdatedAt = completedAt
	run.CompletedAt = &completedAt
	candidateID := candidate.ID
	modelResult := domain.WorkspaceAnalysisModelResult{
		ID: resultID, WorkspaceID: authorizationCommand.Identity.WorkspaceID,
		AnalysisRunID: authorizationCommand.OperationKey.AnalysisRunID, OperationID: authorized.OperationID,
		NodeAttemptID: authorized.Run.NodeAttemptID, ModelRunID: authorized.Run.ID, ModelCallID: authorized.Call.ID,
		OperationKind: domain.WorkspaceAnalysisOperationFaithfulnessReview, Schema: authorized.Run.Schema,
		SubjectCandidateID: &candidateID, SubjectCandidateHash: candidate.DocumentHash,
		Document: append(json.RawMessage(nil), document...), DocumentHash: call.ResponseHash,
		DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	command := FinalizeWorkspaceAnalysisModelResultCommand{
		Identity: authorizationCommand.Identity, OperationKey: authorizationCommand.OperationKey,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: authorized.Call.Version, ExpectedRunVersion: authorized.Run.Version,
		Call: call, Run: run, Result: modelResult,
	}
	if err := command.Validate(); err != nil {
		return WorkspaceAnalysisReviewResult{}, runner.finalizationUnknown(ctx, err)
	}
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceAnalysisReviewFinalizationTimeout)
	defer cancel()
	mutation, err := runner.repository.FinalizeWorkspaceAnalysisModelResult(persistContext, command)
	if err != nil {
		if WorkspaceAnalysisModelCancellationConflict(err) {
			// Do not leave the authorized review Call STARTED when cancellation
			// wins the result-publication race.
			return WorkspaceAnalysisReviewResult{}, runner.finalizeWorkspaceAnalysisReviewFailure(
				ctx, authorizationCommand, authorized,
				ChatResponse{Content: append([]byte(nil), document...), Usage: usage},
				operationContextError(context.Canceled),
			)
		}
		return WorkspaceAnalysisReviewResult{}, runner.finalizationUnknown(ctx, err)
	}
	if err := validateWorkspaceAnalysisReviewResultMutation(command, mutation); err != nil {
		return WorkspaceAnalysisReviewResult{}, runner.finalizationUnknown(ctx, err)
	}
	return workspaceAnalysisReviewResult(mutation, review, mutation.Replayed)
}

func (runner *WorkspaceAnalysisReviewRunner) finalizeWorkspaceAnalysisReviewFailure(
	ctx context.Context,
	authorizationCommand AuthorizeWorkspaceAnalysisModelCallCommand,
	authorized WorkspaceAnalysisModelAuthorizationResult,
	response ChatResponse,
	cause error,
) error {
	status, classified := classifyWorkspaceAnalysisModelFailure(cause)
	completedAt := workspaceAnalysisPlanCompletedAt(runner.clock.Now().UTC(), authorized.Run, authorized.Call)
	call, run := authorized.Call, authorized.Run
	if workspaceAnalysisModelRefusalSignal(classified) {
		call, run = workspaceAnalysisModelRefusalTerminal(authorized, response, completedAt)
	} else {
		call.Status = status
		fallbackCode := ErrorCodeModelCallFailed
		if status == domain.ModelCallUnknown {
			fallbackCode = ErrorCodeModelCallPersistenceUnknown
		}
		call.ErrorCode = stableAgentErrorCode(classified, fallbackCode)
		call.LatencyMillis = completedAt.Sub(call.StartedAt).Milliseconds()
		call.Version++
		call.CompletedAt = &completedAt
		if status == domain.ModelCallFailed {
			if len(response.Content) > 0 && int64(len(response.Content)) <= MaxModelCallResponseBytes {
				call.ResponseHash = workspaceAnalysisSHA256(response.Content)
				call.ResponseBytes = int64(len(response.Content))
			}
			if response.Usage.Validate() == nil {
				call.Usage = response.Usage
			}
		}
		if status == domain.ModelCallUnknown {
			run.Status = domain.ModelRunUnknown
		} else {
			run.Status = domain.ModelRunFailed
		}
		run.FinalErrorCode = call.ErrorCode
		run.Version++
		run.UpdatedAt = completedAt
		run.CompletedAt = &completedAt
	}
	command := FinalizeWorkspaceAnalysisModelCallCommand{
		Identity: authorizationCommand.Identity, OperationKey: authorizationCommand.OperationKey,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: authorized.Call.Version, ExpectedRunVersion: authorized.Run.Version,
		Call: call, Run: run,
	}
	if err := command.Validate(); err != nil {
		return runner.finalizationUnknown(ctx, errors.Join(classified, err))
	}
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceAnalysisReviewFinalizationTimeout)
	defer cancel()
	mutation, err := runner.repository.FinalizeWorkspaceAnalysisModelCall(persistContext, command)
	if err != nil {
		return runner.finalizationUnknown(ctx, errors.Join(classified, err))
	}
	if err := validateWorkspaceAnalysisReviewFailureMutation(command, mutation); err != nil {
		return runner.finalizationUnknown(ctx, errors.Join(classified, err))
	}
	return workspaceAnalysisModelTerminalError(classified, mutation.OperationID, mutation.Call, mutation.Run)
}

func (runner *WorkspaceAnalysisReviewRunner) loadWorkspaceAnalysisReviewResult(
	ctx context.Context,
	request WorkspaceAnalysisReviewRequest,
	authorized WorkspaceAnalysisModelAuthorizationResult,
) (WorkspaceAnalysisReviewResult, error) {
	query := WorkspaceAnalysisModelResultQuery{
		WorkspaceID: request.Identity.WorkspaceID, AnalysisRunID: request.AnalysisRunID,
		OperationID: authorized.OperationID, ModelRunID: authorized.Run.ID, ModelCallID: authorized.Call.ID,
	}
	if err := query.Validate(); err != nil {
		return WorkspaceAnalysisReviewResult{}, err
	}
	modelResult, err := runner.repository.LoadWorkspaceAnalysisModelResult(ctx, query)
	if err != nil {
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := validateLoadedWorkspaceAnalysisReviewResult(request, authorized, modelResult); err != nil {
		return WorkspaceAnalysisReviewResult{}, err
	}
	review, err := domain.DecodeFaithfulnessReview(modelResult.Document, domain.DefaultDecodeLimits())
	if err != nil {
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelAuthorizationError(err)
	}
	if err := validateWorkspaceAnalysisReviewSemantics(review, request.Candidate, authorized.Run.ID); err != nil {
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelAuthorizationError(err)
	}
	mutation := WorkspaceAnalysisModelMutationResult{
		Run: authorized.Run, Call: authorized.Call, Result: &modelResult,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID, Replayed: true,
	}
	return workspaceAnalysisReviewResult(mutation, review, true)
}

func validateLoadedWorkspaceAnalysisReviewResult(
	request WorkspaceAnalysisReviewRequest,
	authorized WorkspaceAnalysisModelAuthorizationResult,
	result domain.WorkspaceAnalysisModelResult,
) error {
	if err := domain.ValidateWorkspaceAnalysisModelResult(result); err != nil {
		return workspaceAnalysisModelAuthorizationError(err)
	}
	if result.SubjectCandidateID == nil || *result.SubjectCandidateID != request.Candidate.ID ||
		result.SubjectCandidateHash != request.Candidate.DocumentHash ||
		result.WorkspaceID != request.Identity.WorkspaceID || result.AnalysisRunID != request.AnalysisRunID ||
		result.OperationID != authorized.OperationID || result.NodeAttemptID != authorized.Run.NodeAttemptID ||
		result.ModelRunID != authorized.Run.ID || result.ModelCallID != authorized.Call.ID ||
		result.OperationKind != domain.WorkspaceAnalysisOperationFaithfulnessReview || result.Schema != authorized.Run.Schema ||
		authorized.Run.Status != domain.ModelRunSucceeded || authorized.Run.FinalResultType != domain.ResultTypeFaithfulnessReview ||
		authorized.Run.FinalErrorCode != "" || authorized.Call.Status != domain.ModelCallSucceeded ||
		authorized.Call.ResponseHash != result.DocumentHash || authorized.Call.ResponseBytes != result.DocumentBytes ||
		authorized.Call.Usage.Validate() != nil || authorized.Run.CompletedAt == nil || authorized.Call.CompletedAt == nil ||
		result.CreatedAt.Before(*authorized.Run.CompletedAt) || result.CreatedAt.Before(*authorized.Call.CompletedAt) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis review replay result drifted"))
	}
	return nil
}

func validateWorkspaceAnalysisReviewResultMutation(
	command FinalizeWorkspaceAnalysisModelResultCommand,
	mutation WorkspaceAnalysisModelMutationResult,
) error {
	if mutation.Result == nil || mutation.Candidate != nil || mutation.OperationID != command.OperationID ||
		mutation.ReservationID != command.ReservationID || !reflect.DeepEqual(mutation.Run, command.Run) ||
		!reflect.DeepEqual(mutation.Call, command.Call) || !reflect.DeepEqual(*mutation.Result, command.Result) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis review finalization drifted"))
	}
	return nil
}

func validateWorkspaceAnalysisReviewFailureMutation(
	command FinalizeWorkspaceAnalysisModelCallCommand,
	mutation WorkspaceAnalysisModelMutationResult,
) error {
	if mutation.Result != nil || mutation.Candidate != nil || mutation.OperationID != command.OperationID ||
		mutation.ReservationID != command.ReservationID || !reflect.DeepEqual(mutation.Run, command.Run) ||
		!reflect.DeepEqual(mutation.Call, command.Call) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis review failure finalization drifted"))
	}
	return nil
}

func workspaceAnalysisReviewResult(
	mutation WorkspaceAnalysisModelMutationResult,
	review domain.FaithfulnessReviewResult,
	replayed bool,
) (WorkspaceAnalysisReviewResult, error) {
	if mutation.Result == nil {
		return WorkspaceAnalysisReviewResult{}, workspaceAnalysisModelAuthorizationError(
			errors.New("workspace analysis review result is missing"),
		)
	}
	modelResult := *mutation.Result
	modelResult.Document = append(json.RawMessage(nil), mutation.Result.Document...)
	return WorkspaceAnalysisReviewResult{
		Review: review, ModelResult: modelResult, Run: mutation.Run, Call: mutation.Call,
		Usage: mutation.Call.Usage, Passed: review.Payload.Passed, Replayed: replayed,
	}, nil
}

func validateWorkspaceAnalysisReviewGeneratedIDs(
	request WorkspaceAnalysisReviewRequest,
	generated workspaceAnalysisReviewIDs,
) error {
	ids := []foundation.ID{
		request.Identity.WorkspaceID, request.Identity.DefinitionID, request.Identity.WorkflowRunID,
		request.Identity.NodeRunID, request.Identity.NodeAttemptID, request.AnalysisRunID,
		request.Candidate.ID, request.Candidate.AnswerID, request.Candidate.SynthesisOperationID,
		request.Candidate.NodeAttemptID, request.Candidate.SynthesisModelRunID,
		generated.operationID, generated.reservationID, generated.modelRunID,
		generated.modelCallID, generated.modelResultID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return errors.New("workspace analysis review generated id is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("workspace analysis review generated id is reused")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func workspaceAnalysisReviewPayloadLeaksIdentity(
	payload domain.FaithfulnessReviewPayload,
	request WorkspaceAnalysisReviewRequest,
	generated workspaceAnalysisReviewIDs,
	authorized WorkspaceAnalysisModelAuthorizationResult,
) bool {
	identities := []string{
		string(request.Identity.WorkspaceID), string(request.Identity.DefinitionID), string(request.Identity.WorkflowRunID),
		string(request.Identity.NodeRunID), string(request.Identity.NodeAttemptID), string(request.AnalysisRunID),
		string(request.Candidate.ID), string(request.Candidate.AnswerID), string(request.Candidate.SynthesisOperationID),
		string(request.Candidate.NodeAttemptID), string(request.Candidate.SynthesisModelRunID), request.Candidate.DocumentHash,
		string(generated.operationID), string(generated.reservationID), string(generated.modelRunID),
		string(generated.modelCallID), string(generated.modelResultID), string(authorized.OperationID),
		string(authorized.ReservationID), string(authorized.Run.ID), string(authorized.Call.ID),
		request.Identity.DefinitionHash, string(request.Retrieval.IndexVersionID),
	}
	if request.Retrieval.EmbeddingVersionID != nil {
		identities = append(identities, string(*request.Retrieval.EmbeddingVersionID))
	}
	values := []string{payload.Summary}
	for _, item := range payload.Items {
		values = append(values, item.AssertionID, string(item.Verdict), item.Reason)
		values = append(values, item.CitationIDs...)
	}
	for _, identity := range identities {
		if identity == "" {
			continue
		}
		for _, value := range values {
			if strings.Contains(value, identity) {
				return true
			}
		}
	}
	return false
}

func (runner *WorkspaceAnalysisReviewRunner) finalizationUnknown(ctx context.Context, cause error) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			cause = errors.Join(cause, err)
		}
	}
	return applicationError(
		foundation.ErrorManualRecoveryRequired,
		ErrorCodeModelCallPersistenceUnknown,
		false,
		cause,
	)
}
