package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceAnalysisSynthesisRunnerDependencies 是候选答案单次模型调用的全部项目边界。
type WorkspaceAnalysisSynthesisRunnerDependencies struct {
	Runtime    WorkspaceAnalysisCandidateStreamRuntime
	Catalog    *RuntimeCatalog
	Repository WorkspaceAnalysisModelOperationRepository
	Drafts     WorkspaceAnalysisCandidateDraftCoordinator
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
}

// WorkspaceAnalysisSynthesisRequest 绑定当前持久 Attempt、Answer 和已打开的短引用。
type WorkspaceAnalysisSynthesisRequest struct {
	Identity              WorkspaceAnalysisModelExecutionIdentity
	AnalysisRunID         foundation.ID
	AnswerID              foundation.ID
	AttemptNo             int
	ModelSettingsRevision *int64
	Retrieval             domain.RetrievalRef
	ProfileRef            domain.ModelProfileRef
	PromptRef             domain.PromptRef
	Input                 []byte
	AllowedEvidenceRefs   []string
}

// WorkspaceAnalysisSynthesisResult 返回权威不可变候选及实际 Model Run/Call。
type WorkspaceAnalysisSynthesisResult struct {
	Candidate       domain.WorkspaceAnalysisCandidate
	CandidateResult domain.WorkspaceAnalysisCandidateResult
	Run             domain.ModelRun
	Call            domain.ModelCall
	Draft           DraftStreamSession
	Usage           domain.TokenUsage
	Replayed        bool
}

// WorkspaceAnalysisSynthesisRunner 只允许 CREATED 越过 Provider；其余状态精确重放或失败关闭。
type WorkspaceAnalysisSynthesisRunner struct {
	runtime    WorkspaceAnalysisCandidateStreamRuntime
	catalog    *RuntimeCatalog
	repository WorkspaceAnalysisModelOperationRepository
	drafts     WorkspaceAnalysisCandidateDraftCoordinator
	ids        foundation.IDGenerator
	clock      foundation.Clock
	idMu       sync.Mutex
}

// NewWorkspaceAnalysisSynthesisRunner 创建单次、无 repair/retry/fallback 的候选合成 Runner。
func NewWorkspaceAnalysisSynthesisRunner(
	dependencies WorkspaceAnalysisSynthesisRunnerDependencies,
) (*WorkspaceAnalysisSynthesisRunner, error) {
	if isNilPort(dependencies.Runtime) || dependencies.Catalog == nil || isNilPort(dependencies.Repository) || isNilPort(dependencies.Drafts) ||
		isNilPort(dependencies.IDs) || isNilPort(dependencies.Clock) {
		return nil, applicationError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable,
			false,
			errors.New("workspace analysis synthesis dependencies are unavailable"),
		)
	}
	return &WorkspaceAnalysisSynthesisRunner{
		runtime: dependencies.Runtime, catalog: dependencies.Catalog, repository: dependencies.Repository, drafts: dependencies.Drafts,
		ids: dependencies.IDs, clock: dependencies.Clock,
	}, nil
}

// Run 原子授权 Synthesis Operation，并在首次创建时恰好调用一次 Provider。
func (runner *WorkspaceAnalysisSynthesisRunner) Run(
	ctx context.Context,
	request WorkspaceAnalysisSynthesisRequest,
) (WorkspaceAnalysisSynthesisResult, error) {
	if runner == nil || isNilPort(runner.runtime) || runner.catalog == nil || isNilPort(runner.repository) || isNilPort(runner.drafts) ||
		isNilPort(runner.ids) || isNilPort(runner.clock) {
		return WorkspaceAnalysisSynthesisResult{}, applicationError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable,
			false,
			errors.New("workspace analysis synthesis runner is unavailable"),
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, operationContextError(err)
	}
	if err := validateWorkspaceAnalysisSynthesisRequest(request); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, err
	}

	schemaRef := domain.SchemaRef{ID: domain.WorkspaceAnalysisCandidateSchemaID, Version: strconv.FormatInt(request.Identity.DefinitionVersion, 10)}
	snapshot, err := runner.catalog.Snapshot(request.PromptRef, schemaRef, schemaRef, request.ProfileRef)
	if err != nil {
		return WorkspaceAnalysisSynthesisResult{}, err
	}
	maxOutputTokens := snapshot.Profile.MaxOutputTokens
	if maxOutputTokens > int(WorkspaceAnalysisV1SynthesisMaxOutputTokens) {
		maxOutputTokens = int(WorkspaceAnalysisV1SynthesisMaxOutputTokens)
	}
	if snapshot.Schema.Ref != schemaRef || snapshot.ReducedSchema.Ref != schemaRef || maxOutputTokens < 1 {
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelError(
			errors.New("workspace analysis synthesis runtime snapshot drifted"),
		)
	}

	generated, err := runner.newWorkspaceAnalysisSynthesisIDs()
	if err != nil {
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := validateWorkspaceAnalysisSynthesisGeneratedIDs(request, generated); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelError(err)
	}
	canonicalInput, err := canonicalWorkspaceAnalysisSynthesisInput(request.Input)
	if err != nil {
		return WorkspaceAnalysisSynthesisResult{}, err
	}
	if workspaceAnalysisSynthesisDocumentLeaksIdentity(canonicalInput, request, generated, WorkspaceAnalysisModelAuthorizationResult{}) {
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelError(
			errors.New("workspace analysis synthesis input contains server identity"),
		)
	}

	chatRequest := buildChatRequest(
		snapshot, snapshot.Schema, domain.ModelCallAnswer,
		snapshot.Prompt.InitialInstruction, canonicalInput, "",
	)
	chatRequest.MaxOutputTokens = maxOutputTokens
	requestDocument, err := canonicalWorkspaceAnalysisChatRequest(chatRequest)
	if err != nil {
		return WorkspaceAnalysisSynthesisResult{}, err
	}
	startedAt := runner.clock.Now().UTC()
	if startedAt.IsZero() {
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelError(
			errors.New("workspace analysis synthesis clock returned zero time"),
		)
	}
	operationKey := domain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: request.AnalysisRunID,
		NodeKey:       domain.WorkspaceAnalysisOperationNodeSynthesizeAnswer,
		Kind:          domain.WorkspaceAnalysisOperationAnswerSynthesis,
		Ordinal:       1,
	}
	run := domain.ModelRun{
		ID: generated.modelRunID, WorkspaceID: request.Identity.WorkspaceID,
		WorkflowRunID: request.Identity.WorkflowRunID, NodeRunID: request.Identity.NodeRunID,
		NodeAttemptID:         request.Identity.NodeAttemptID,
		ModelSettingsRevision: cloneWorkspaceAnalysisOptionalInt64(request.ModelSettingsRevision),
		Model:                 snapshot.Profile.Model, Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref,
		Schema: snapshot.Schema.Ref, ReducedSchema: snapshot.Schema.Ref,
		Retrieval: cloneWorkspaceAnalysisRetrieval(request.Retrieval),
		Status:    domain.ModelRunRunning, Version: 1, CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	call := domain.ModelCall{
		ID: generated.modelCallID, ModelRunID: generated.modelRunID, CallNo: 1,
		Phase: domain.ModelCallAnswer, Model: snapshot.Profile.Model, Profile: snapshot.Profile.Ref,
		Prompt: snapshot.Prompt.Ref, Schema: snapshot.Schema.Ref, MaxOutputTokens: maxOutputTokens,
		Status: domain.ModelCallStarted, RequestHash: workspaceAnalysisSHA256(requestDocument),
		RequestBytes: int64(len(requestDocument)), Version: 1, StartedAt: startedAt,
	}
	authorizationCommand := AuthorizeWorkspaceAnalysisModelCallCommand{
		Identity: request.Identity, OperationKey: operationKey, OperationID: generated.operationID,
		ReservationID: generated.reservationID, Run: run, Call: call, RequestDocument: requestDocument,
	}
	if err := authorizationCommand.Validate(); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, err
	}
	authorized, err := runner.repository.AuthorizeWorkspaceAnalysisModelCall(ctx, authorizationCommand)
	if err != nil {
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := authorized.ValidateFor(authorizationCommand); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, err
	}

	switch authorized.Disposition {
	case WorkspaceAnalysisModelAuthorizationReuseResult:
		return runner.loadWorkspaceAnalysisSynthesisResult(ctx, request, authorized)
	case WorkspaceAnalysisModelAuthorizationReconcile:
		return WorkspaceAnalysisSynthesisResult{}, applicationError(
			foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallReplayUnsafe, false,
			errors.New("workspace analysis synthesis call requires exact reconciliation"),
		)
	case WorkspaceAnalysisModelAuthorizationReplayFailure:
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelTerminalError(
			workspaceAnalysisReplayedModelFailure(authorized.Call, authorized.Run), authorized.OperationID, authorized.Call, authorized.Run,
		)
	case WorkspaceAnalysisModelAuthorizationTerminateUnknown:
		terminal := applicationError(
			foundation.ErrorManualRecoveryRequired,
			stableTerminalErrorCode(authorized.Call.ErrorCode, nil, ErrorCodeModelCallPersistenceUnknown),
			false, errors.New("workspace analysis synthesis outcome is unknown"),
		)
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelTerminalError(
			terminal, authorized.OperationID, authorized.Call, authorized.Run,
		)
	case WorkspaceAnalysisModelAuthorizationCreated:
		// Only this branch may cross the Provider boundary.
	default:
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelAuthorizationError(
			errors.New("workspace analysis synthesis authorization disposition is unsupported"),
		)
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizeWorkspaceAnalysisSynthesisFailure(
			ctx, authorizationCommand, authorized, ChatResponse{}, operationContextError(err),
		)
	}
	draft, err := runner.drafts.Begin(ctx, DraftStreamBinding{
		WorkspaceID: request.Identity.WorkspaceID, AnswerID: request.AnswerID,
		WorkflowRunID: request.Identity.WorkflowRunID, NodeRunID: request.Identity.NodeRunID,
		NodeAttemptID: request.Identity.NodeAttemptID, AttemptNo: request.AttemptNo,
		LeaseOwner: request.Identity.LeaseOwner,
	})
	if err != nil {
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizeWorkspaceAnalysisSynthesisFailure(
			ctx, authorizationCommand, authorized, ChatResponse{}, err,
		)
	}

	callContext, cancel := context.WithTimeout(ctx, snapshot.Profile.Timeout)
	response, callErr := runner.runtime.Stream(callContext, cloneChatRequest(chatRequest), draft)
	contextErr := callContext.Err()
	cancel()
	if callErr == nil && contextErr != nil {
		callErr = operationContextError(contextErr)
	}
	callErr = validateWorkspaceAnalysisModelProviderOutcome(chatRequest, response, callErr)
	if callErr != nil {
		callErr = errors.Join(callErr, abortWorkspaceAnalysisSynthesisDraft(ctx, draft))
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizeWorkspaceAnalysisSynthesisFailure(
			ctx, authorizationCommand, authorized, response, callErr,
		)
	}
	providerCandidate, err := decodeWorkspaceAnalysisProviderCandidate(response.Content, snapshot.Schema)
	if err != nil {
		err = errors.Join(err, abortWorkspaceAnalysisSynthesisDraft(ctx, draft))
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizeWorkspaceAnalysisSynthesisFailure(
			ctx, authorizationCommand, authorized, response, err,
		)
	}
	if providerCandidate.SchemaVersion != schemaRef.Version || !workspaceAnalysisCandidateRefsAllowedForVersion(providerCandidate.Payload.CitationRefs, request.AllowedEvidenceRefs, request.Identity.DefinitionVersion) ||
		workspaceAnalysisSynthesisDocumentLeaksIdentity(response.Content, request, generated, authorized) {
		abortErr := abortWorkspaceAnalysisSynthesisDraft(ctx, draft)
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizeWorkspaceAnalysisSynthesisFailure(
			ctx, authorizationCommand, authorized, response,
			errors.Join(workspaceAnalysisModelError(errors.New("workspace analysis synthesis output violates the evidence or identity boundary")), abortErr),
		)
	}
	candidateResult, document, err := composeCanonicalWorkspaceAnalysisCandidate(providerCandidate, authorized.Run.ID)
	if err != nil {
		err = errors.Join(err, abortWorkspaceAnalysisSynthesisDraft(ctx, draft))
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizeWorkspaceAnalysisSynthesisFailure(
			ctx, authorizationCommand, authorized, response, err,
		)
	}
	draftSession, err := completeWorkspaceAnalysisSynthesisDraft(ctx, draft)
	if err != nil {
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizeWorkspaceAnalysisSynthesisFailure(
			ctx, authorizationCommand, authorized, response, err,
		)
	}
	if err := validateWorkspaceAnalysisSynthesisDraftIdentity(
		draftSession, request.Identity.WorkspaceID, request.AnswerID, authorized.Run.NodeAttemptID,
	); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizeWorkspaceAnalysisSynthesisFailure(
			ctx, authorizationCommand, authorized, response, err,
		)
	}
	return runner.finalizeWorkspaceAnalysisSynthesisSuccess(
		ctx, authorizationCommand, authorized, generated.candidateID,
		request.AnswerID, candidateResult, document, response.Usage, draftSession,
	)
}

func abortWorkspaceAnalysisSynthesisDraft(ctx context.Context, draft WorkspaceAnalysisCandidateDraft) error {
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceAnalysisPlanFinalizationTimeout)
	defer cancel()
	return draft.Abort(finalizeContext)
}

func completeWorkspaceAnalysisSynthesisDraft(
	ctx context.Context,
	draft WorkspaceAnalysisCandidateDraft,
) (DraftStreamSession, error) {
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceAnalysisPlanFinalizationTimeout)
	defer cancel()
	return draft.Complete(finalizeContext)
}

type workspaceAnalysisSynthesisIDs struct {
	operationID   foundation.ID
	reservationID foundation.ID
	modelRunID    foundation.ID
	modelCallID   foundation.ID
	candidateID   foundation.ID
}

func (runner *WorkspaceAnalysisSynthesisRunner) newWorkspaceAnalysisSynthesisIDs() (workspaceAnalysisSynthesisIDs, error) {
	runner.idMu.Lock()
	defer runner.idMu.Unlock()
	values := make([]foundation.ID, 5)
	for index := range values {
		value, err := runner.ids.New()
		if err != nil {
			return workspaceAnalysisSynthesisIDs{}, err
		}
		values[index] = value
	}
	return workspaceAnalysisSynthesisIDs{
		operationID: values[0], reservationID: values[1], modelRunID: values[2],
		modelCallID: values[3], candidateID: values[4],
	}, nil
}

func validateWorkspaceAnalysisSynthesisRequest(request WorkspaceAnalysisSynthesisRequest) error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	key := domain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: request.AnalysisRunID, NodeKey: domain.WorkspaceAnalysisOperationNodeSynthesizeAnswer,
		Kind: domain.WorkspaceAnalysisOperationAnswerSynthesis, Ordinal: 1,
	}
	if request.Identity.NodeKey != domain.WorkspaceAnalysisOperationNodeSynthesizeAnswer || key.Validate() != nil ||
		request.ProfileRef.Validate() != nil || request.PromptRef.Validate() != nil || request.Retrieval.Validate() != nil ||
		(request.ModelSettingsRevision != nil && *request.ModelSettingsRevision < 0) || request.AttemptNo < 1 ||
		request.Identity.LeaseFence != int64(request.AttemptNo) || len(request.Input) == 0 ||
		len(request.Input) > MaxStructuredInputBytes || !workspaceAnalysisAllowedEvidenceRefsForVersion(request.AllowedEvidenceRefs, request.Identity.DefinitionVersion) {
		return workspaceAnalysisModelError(errors.New("workspace analysis synthesis request is invalid"))
	}
	ids := []foundation.ID{
		request.AnalysisRunID, request.AnswerID, request.Identity.WorkspaceID, request.Identity.DefinitionID,
		request.Identity.WorkflowRunID, request.Identity.NodeRunID, request.Identity.NodeAttemptID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return workspaceAnalysisModelError(errors.New("workspace analysis synthesis request id is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return workspaceAnalysisModelError(errors.New("workspace analysis synthesis request id is reused"))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func canonicalWorkspaceAnalysisSynthesisInput(raw []byte) ([]byte, error) {
	limits := domain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = MaxStructuredInputBytes
	value, err := domain.DecodeStrict[map[string]any](raw, limits, nil)
	if err != nil || value == nil || workspaceAnalysisPlanValueHasReservedIdentity(value) {
		return nil, workspaceAnalysisModelError(errOrWorkspaceAnalysisPlan(err, "workspace analysis synthesis input is invalid"))
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) == 0 || len(canonical) > MaxStructuredInputBytes {
		return nil, workspaceAnalysisModelError(errOrWorkspaceAnalysisPlan(err, "workspace analysis synthesis input cannot be canonicalized"))
	}
	return canonical, nil
}

func decodeWorkspaceAnalysisProviderCandidate(
	raw []byte,
	schema SchemaDefinition,
) (domain.WorkspaceAnalysisCandidateProviderResult, error) {
	if len(raw) == 0 || int64(len(raw)) > domain.MaxWorkspaceAnalysisCandidateBytes {
		return domain.WorkspaceAnalysisCandidateProviderResult{}, applicationError(
			foundation.ErrorConsistencyViolation, domain.ErrorCodeStructuredOutputLimitExceeded, false,
			errors.New("workspace analysis provider candidate exceeds the bounded result limit"),
		)
	}
	decoded, err := schema.Decode(append([]byte(nil), raw...))
	if err != nil {
		return domain.WorkspaceAnalysisCandidateProviderResult{}, err
	}
	if !bytes.Equal(decoded, raw) {
		return domain.WorkspaceAnalysisCandidateProviderResult{}, applicationError(
			foundation.ErrorConsistencyViolation, errorCodeDecoderContract, false,
			errors.New("workspace analysis candidate decoder transformed provider output"),
		)
	}
	limits := domain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(domain.MaxWorkspaceAnalysisCandidateBytes)
	return domain.DecodeWorkspaceAnalysisCandidateProvider(raw, limits)
}

func composeCanonicalWorkspaceAnalysisCandidate(
	provider domain.WorkspaceAnalysisCandidateProviderResult,
	modelRunID foundation.ID,
) (domain.WorkspaceAnalysisCandidateResult, []byte, error) {
	result, err := provider.Compose(modelRunID)
	if err != nil {
		return domain.WorkspaceAnalysisCandidateResult{}, nil, err
	}
	document, err := json.Marshal(result)
	if err != nil || int64(len(document)) > domain.MaxWorkspaceAnalysisCandidateBytes {
		return domain.WorkspaceAnalysisCandidateResult{}, nil, workspaceAnalysisModelError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis candidate cannot be canonicalized"),
		)
	}
	limits := domain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(domain.MaxWorkspaceAnalysisCandidateBytes)
	decoded, err := domain.DecodeWorkspaceAnalysisCandidate(document, limits)
	if err != nil || !reflect.DeepEqual(decoded, result) {
		return domain.WorkspaceAnalysisCandidateResult{}, nil, workspaceAnalysisModelError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis candidate canonical round trip drifted"),
		)
	}
	return result, document, nil
}

func (runner *WorkspaceAnalysisSynthesisRunner) finalizeWorkspaceAnalysisSynthesisSuccess(
	ctx context.Context,
	authorizationCommand AuthorizeWorkspaceAnalysisModelCallCommand,
	authorized WorkspaceAnalysisModelAuthorizationResult,
	candidateID foundation.ID,
	answerID foundation.ID,
	result domain.WorkspaceAnalysisCandidateResult,
	document []byte,
	usage domain.TokenUsage,
	draft DraftStreamSession,
) (WorkspaceAnalysisSynthesisResult, error) {
	completedAt := workspaceAnalysisPlanCompletedAt(runner.clock.Now().UTC(), authorized.Run, authorized.Call)
	call := authorized.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, workspaceAnalysisSHA256(document), int64(len(document))
	call.Usage, call.LatencyMillis, call.Version = usage, completedAt.Sub(call.StartedAt).Milliseconds(), call.Version+1
	call.CompletedAt = &completedAt
	run := authorized.Run
	run.Status, run.FinalResultType, run.Version = domain.ModelRunSucceeded, domain.ResultTypeWorkspaceAnalysisAnswer, run.Version+1
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	candidate := domain.WorkspaceAnalysisCandidate{
		ID: candidateID, WorkspaceID: authorizationCommand.Identity.WorkspaceID,
		AnalysisRunID: authorizationCommand.OperationKey.AnalysisRunID, AnswerID: answerID,
		SynthesisOperationID: authorized.OperationID, NodeAttemptID: authorized.Run.NodeAttemptID,
		SynthesisModelRunID: authorized.Run.ID, SchemaID: authorized.Run.Schema.ID,
		SchemaVersion: authorizationCommand.Identity.DefinitionVersion, Document: append(json.RawMessage(nil), document...),
		DocumentHash: call.ResponseHash, DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	command := FinalizeWorkspaceAnalysisModelCandidateCommand{
		Identity: authorizationCommand.Identity, OperationKey: authorizationCommand.OperationKey,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: authorized.Call.Version, ExpectedRunVersion: authorized.Run.Version,
		Call: call, Run: run, Candidate: candidate,
	}
	if err := command.Validate(); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizationUnknown(ctx, err)
	}
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceAnalysisPlanFinalizationTimeout)
	defer cancel()
	mutation, err := runner.repository.FinalizeWorkspaceAnalysisModelCandidate(persistContext, command)
	if err != nil {
		if WorkspaceAnalysisModelCancellationConflict(err) {
			// Cancellation won after the Provider returned. Close the already
			// authorized Call first; the workflow cancellation checkpoint then
			// owns the public terminal transition. Do not publish the Candidate.
			return WorkspaceAnalysisSynthesisResult{}, runner.finalizeWorkspaceAnalysisSynthesisFailure(
				ctx, authorizationCommand, authorized,
				ChatResponse{Content: append([]byte(nil), document...), Usage: usage},
				operationContextError(context.Canceled),
			)
		}
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizationUnknown(ctx, err)
	}
	if err := validateWorkspaceAnalysisSynthesisMutation(command, mutation); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, runner.finalizationUnknown(ctx, err)
	}
	return workspaceAnalysisSynthesisResult(mutation, result, draft, mutation.Replayed)
}

func (runner *WorkspaceAnalysisSynthesisRunner) finalizeWorkspaceAnalysisSynthesisFailure(
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
		fallback := ErrorCodeModelCallFailed
		if status == domain.ModelCallUnknown {
			fallback = ErrorCodeModelCallPersistenceUnknown
		}
		call.ErrorCode = stableAgentErrorCode(classified, fallback)
		call.LatencyMillis, call.Version, call.CompletedAt = completedAt.Sub(call.StartedAt).Milliseconds(), call.Version+1, &completedAt
		if status == domain.ModelCallFailed {
			if len(response.Content) > 0 && int64(len(response.Content)) <= MaxModelCallResponseBytes {
				call.ResponseHash, call.ResponseBytes = workspaceAnalysisSHA256(response.Content), int64(len(response.Content))
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
		run.FinalErrorCode, run.Version, run.UpdatedAt, run.CompletedAt = call.ErrorCode, run.Version+1, completedAt, &completedAt
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
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceAnalysisPlanFinalizationTimeout)
	defer cancel()
	mutation, err := runner.repository.FinalizeWorkspaceAnalysisModelCall(persistContext, command)
	if err != nil {
		return runner.finalizationUnknown(ctx, errors.Join(classified, err))
	}
	if err := validateWorkspaceAnalysisPlanFailureMutation(command, mutation); err != nil {
		return runner.finalizationUnknown(ctx, errors.Join(classified, err))
	}
	return workspaceAnalysisModelTerminalError(classified, mutation.OperationID, mutation.Call, mutation.Run)
}

func (runner *WorkspaceAnalysisSynthesisRunner) loadWorkspaceAnalysisSynthesisResult(
	ctx context.Context,
	request WorkspaceAnalysisSynthesisRequest,
	authorized WorkspaceAnalysisModelAuthorizationResult,
) (WorkspaceAnalysisSynthesisResult, error) {
	query := WorkspaceAnalysisCandidateQuery{
		WorkspaceID: request.Identity.WorkspaceID, AnalysisRunID: request.AnalysisRunID,
		OperationID: authorized.OperationID, ModelRunID: authorized.Run.ID, ModelCallID: authorized.Call.ID,
	}
	if err := query.Validate(); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, err
	}
	candidate, err := runner.repository.LoadWorkspaceAnalysisCandidate(ctx, query)
	if err != nil {
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := validateLoadedWorkspaceAnalysisSynthesis(request, authorized, candidate); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, err
	}
	draft, err := runner.drafts.Load(ctx, WorkspaceAnalysisCandidateDraftQuery{
		WorkspaceID: request.Identity.WorkspaceID, AnswerID: request.AnswerID, NodeAttemptID: candidate.NodeAttemptID,
	})
	if err != nil {
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := validateWorkspaceAnalysisSynthesisDraft(draft, candidate); err != nil {
		return WorkspaceAnalysisSynthesisResult{}, err
	}
	limits := domain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(domain.MaxWorkspaceAnalysisCandidateBytes)
	result, err := domain.DecodeWorkspaceAnalysisCandidate(candidate.Document, limits)
	if err != nil || !workspaceAnalysisCandidateRefsAllowedForVersion(result.Payload.CitationRefs, request.AllowedEvidenceRefs, request.Identity.DefinitionVersion) {
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelAuthorizationError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis synthesis replay references drifted"),
		)
	}
	mutation := WorkspaceAnalysisModelMutationResult{
		Run: authorized.Run, Call: authorized.Call, Candidate: &candidate,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID, Replayed: true,
	}
	return workspaceAnalysisSynthesisResult(mutation, result, draft, true)
}

func validateLoadedWorkspaceAnalysisSynthesis(
	request WorkspaceAnalysisSynthesisRequest,
	authorized WorkspaceAnalysisModelAuthorizationResult,
	candidate domain.WorkspaceAnalysisCandidate,
) error {
	if domain.ValidateWorkspaceAnalysisCandidate(candidate) != nil || candidate.WorkspaceID != request.Identity.WorkspaceID ||
		candidate.SchemaVersion != request.Identity.DefinitionVersion || candidate.SchemaID != authorized.Run.Schema.ID ||
		candidate.AnalysisRunID != request.AnalysisRunID || candidate.AnswerID != request.AnswerID ||
		candidate.SynthesisOperationID != authorized.OperationID || candidate.NodeAttemptID != authorized.Run.NodeAttemptID ||
		candidate.SynthesisModelRunID != authorized.Run.ID || authorized.Run.Status != domain.ModelRunSucceeded ||
		authorized.Run.FinalResultType != domain.ResultTypeWorkspaceAnalysisAnswer || authorized.Run.FinalErrorCode != "" ||
		authorized.Call.Status != domain.ModelCallSucceeded || authorized.Call.ResponseHash != candidate.DocumentHash ||
		authorized.Call.ResponseBytes != candidate.DocumentBytes || authorized.Call.Usage.Validate() != nil ||
		authorized.Run.CompletedAt == nil || authorized.Call.CompletedAt == nil ||
		candidate.CreatedAt.Before(*authorized.Run.CompletedAt) || candidate.CreatedAt.Before(*authorized.Call.CompletedAt) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis synthesis replay candidate drifted"))
	}
	return nil
}

func validateWorkspaceAnalysisSynthesisMutation(
	command FinalizeWorkspaceAnalysisModelCandidateCommand,
	mutation WorkspaceAnalysisModelMutationResult,
) error {
	if mutation.Result != nil || mutation.Candidate == nil || mutation.OperationID != command.OperationID ||
		mutation.ReservationID != command.ReservationID || !reflect.DeepEqual(mutation.Run, command.Run) ||
		!reflect.DeepEqual(mutation.Call, command.Call) || !reflect.DeepEqual(*mutation.Candidate, command.Candidate) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis synthesis finalization drifted"))
	}
	return nil
}

func workspaceAnalysisSynthesisResult(
	mutation WorkspaceAnalysisModelMutationResult,
	result domain.WorkspaceAnalysisCandidateResult,
	draft DraftStreamSession,
	replayed bool,
) (WorkspaceAnalysisSynthesisResult, error) {
	if mutation.Candidate == nil {
		return WorkspaceAnalysisSynthesisResult{}, workspaceAnalysisModelAuthorizationError(
			errors.New("workspace analysis synthesis candidate is missing"),
		)
	}
	candidate := *mutation.Candidate
	candidate.Document = append(json.RawMessage(nil), mutation.Candidate.Document...)
	return WorkspaceAnalysisSynthesisResult{
		Candidate: candidate, CandidateResult: result, Run: mutation.Run, Call: mutation.Call, Draft: draft,
		Usage: mutation.Call.Usage, Replayed: replayed,
	}, nil
}

func validateWorkspaceAnalysisSynthesisDraft(draft DraftStreamSession, candidate domain.WorkspaceAnalysisCandidate) error {
	return validateWorkspaceAnalysisSynthesisDraftIdentity(
		draft, candidate.WorkspaceID, candidate.AnswerID, candidate.NodeAttemptID,
	)
}

func validateWorkspaceAnalysisSynthesisDraftIdentity(
	draft DraftStreamSession,
	workspaceID foundation.ID,
	answerID foundation.ID,
	nodeAttemptID foundation.ID,
) error {
	if !canonicalApplicationID(draft.ID) || draft.Binding.WorkspaceID != workspaceID ||
		draft.Binding.AnswerID != answerID || draft.Binding.NodeAttemptID != nodeAttemptID ||
		draft.Generation < 1 || (draft.Status != DraftStreamCompleted && draft.Status != DraftStreamDegraded) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis synthesis draft binding drifted"))
	}
	return nil
}

func validateWorkspaceAnalysisSynthesisGeneratedIDs(
	request WorkspaceAnalysisSynthesisRequest,
	generated workspaceAnalysisSynthesisIDs,
) error {
	ids := []foundation.ID{
		request.AnalysisRunID, request.AnswerID, request.Identity.WorkspaceID, request.Identity.DefinitionID,
		request.Identity.WorkflowRunID, request.Identity.NodeRunID, request.Identity.NodeAttemptID,
		generated.operationID, generated.reservationID, generated.modelRunID, generated.modelCallID, generated.candidateID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return errors.New("workspace analysis synthesis generated id is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("workspace analysis synthesis generated id is reused")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func workspaceAnalysisAllowedEvidenceRefs(refs []string) bool {
	if len(refs) < 1 || len(refs) > 3 {
		return false
	}
	for index, ref := range refs {
		if ref != "E"+string(rune('1'+index)) {
			return false
		}
	}
	return true
}

func workspaceAnalysisCandidateRefsAllowed(candidateRefs, allowedRefs []string) bool {
	if len(candidateRefs) == 0 || !workspaceAnalysisAllowedEvidenceRefs(allowedRefs) {
		return false
	}
	allowed := make(map[string]struct{}, len(allowedRefs))
	for _, ref := range allowedRefs {
		allowed[ref] = struct{}{}
	}
	for _, ref := range candidateRefs {
		if _, exists := allowed[ref]; !exists {
			return false
		}
	}
	return true
}

func workspaceAnalysisSynthesisDocumentLeaksIdentity(
	document []byte,
	request WorkspaceAnalysisSynthesisRequest,
	generated workspaceAnalysisSynthesisIDs,
	authorized WorkspaceAnalysisModelAuthorizationResult,
) bool {
	values := []foundation.ID{
		request.Identity.WorkspaceID, request.Identity.DefinitionID, request.Identity.WorkflowRunID,
		request.Identity.NodeRunID, request.Identity.NodeAttemptID, request.AnalysisRunID, request.AnswerID,
		request.Retrieval.IndexVersionID, generated.operationID, generated.reservationID,
		generated.modelRunID, generated.modelCallID, generated.candidateID,
		authorized.Run.ID, authorized.Call.ID, authorized.OperationID, authorized.ReservationID,
	}
	if request.Retrieval.EmbeddingVersionID != nil {
		values = append(values, *request.Retrieval.EmbeddingVersionID)
	}
	for _, value := range values {
		if value != "" && bytes.Contains(document, []byte(value)) {
			return true
		}
	}
	return strings.Contains(string(document), request.Identity.DefinitionHash)
}

func (runner *WorkspaceAnalysisSynthesisRunner) finalizationUnknown(ctx context.Context, cause error) error {
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
