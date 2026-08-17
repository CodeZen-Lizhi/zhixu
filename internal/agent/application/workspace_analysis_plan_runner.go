package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const workspaceAnalysisPlanFinalizationTimeout = 5 * time.Second

// WorkspaceAnalysisRetrievalPlanRepository 组合计划授权终结与跨 Attempt 检查点读取能力。
type WorkspaceAnalysisRetrievalPlanRepository interface {
	WorkspaceAnalysisModelOperationRepository
	WorkspaceAnalysisRetrievalPlanCheckpointReader
}

// WorkspaceAnalysisRetrievalPlanRunnerDependencies 是检索计划模型调用的全部项目边界。
type WorkspaceAnalysisRetrievalPlanRunnerDependencies struct {
	Model      ChatModel
	Catalog    *RuntimeCatalog
	Repository WorkspaceAnalysisRetrievalPlanRepository
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
}

// WorkspaceAnalysisRetrievalPlanRequest 绑定当前持久 Attempt、Analysis Run 与冻结模型版本。
type WorkspaceAnalysisRetrievalPlanRequest struct {
	Identity              WorkspaceAnalysisModelExecutionIdentity
	AnalysisRunID         foundation.ID
	ModelSettingsRevision *int64
	Retrieval             domain.RetrievalRef
	ProfileRef            domain.ModelProfileRef
	PromptRef             domain.PromptRef
	Input                 []byte
}

// WorkspaceAnalysisRetrievalPlanResult 返回权威持久计划及其 Model Run/Call 回执。
type WorkspaceAnalysisRetrievalPlanResult struct {
	Plan        domain.WorkspaceAnalysisPlanResult
	ModelResult domain.WorkspaceAnalysisModelResult
	Run         domain.ModelRun
	Call        domain.ModelCall
	Usage       domain.TokenUsage
	Replayed    bool
}

// WorkspaceAnalysisRetrievalPlanRunner 在 Provider 外拥有原子授权、严格解码与持久终结。
type WorkspaceAnalysisRetrievalPlanRunner struct {
	model      ChatModel
	catalog    *RuntimeCatalog
	repository WorkspaceAnalysisRetrievalPlanRepository
	ids        foundation.IDGenerator
	clock      foundation.Clock
	idMu       sync.Mutex
}

// NewWorkspaceAnalysisRetrievalPlanRunner 创建不会 repair、retry 或降级的单次 PLAN runner。
func NewWorkspaceAnalysisRetrievalPlanRunner(
	dependencies WorkspaceAnalysisRetrievalPlanRunnerDependencies,
) (*WorkspaceAnalysisRetrievalPlanRunner, error) {
	if isNilChatModel(dependencies.Model) || dependencies.Catalog == nil || isNilPort(dependencies.Repository) ||
		isNilPort(dependencies.IDs) || isNilPort(dependencies.Clock) {
		return nil, applicationError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable,
			false,
			errors.New("workspace analysis retrieval plan dependencies are unavailable"),
		)
	}
	return &WorkspaceAnalysisRetrievalPlanRunner{
		model: dependencies.Model, catalog: dependencies.Catalog, repository: dependencies.Repository,
		ids: dependencies.IDs, clock: dependencies.Clock,
	}, nil
}

// FindWorkspaceAnalysisRetrievalPlanCheckpoint 在读取当前 Active Index 前恢复首次授权冻结的 Retrieval。
func (runner *WorkspaceAnalysisRetrievalPlanRunner) FindWorkspaceAnalysisRetrievalPlanCheckpoint(
	ctx context.Context,
	query WorkspaceAnalysisRetrievalPlanCheckpointQuery,
) (WorkspaceAnalysisRetrievalPlanCheckpoint, bool, error) {
	if runner == nil || isNilPort(runner.repository) {
		return WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, applicationError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable,
			false,
			errors.New("workspace analysis retrieval plan checkpoint reader is unavailable"),
		)
	}
	if ctx == nil {
		return WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, workspaceAnalysisModelError(
			errors.New("workspace analysis retrieval plan checkpoint context is nil"),
		)
	}
	if err := query.Validate(); err != nil {
		return WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, err
	}
	checkpoint, found, err := runner.repository.FindWorkspaceAnalysisRetrievalPlanCheckpoint(ctx, query)
	if err != nil {
		return WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, workspaceAnalysisModelRepositoryError(err)
	}
	if !found {
		return WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, nil
	}
	if err := checkpoint.ValidateFor(query); err != nil {
		return WorkspaceAnalysisRetrievalPlanCheckpoint{}, false, err
	}
	return checkpoint, true, nil
}

// Run 先原子授权 exact Model Operation，仅 CREATED 会调用一次 Provider。
func (runner *WorkspaceAnalysisRetrievalPlanRunner) Run(
	ctx context.Context,
	request WorkspaceAnalysisRetrievalPlanRequest,
) (WorkspaceAnalysisRetrievalPlanResult, error) {
	if runner == nil || isNilChatModel(runner.model) || runner.catalog == nil || isNilPort(runner.repository) ||
		isNilPort(runner.ids) || isNilPort(runner.clock) {
		return WorkspaceAnalysisRetrievalPlanResult{}, applicationError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable,
			false,
			errors.New("workspace analysis retrieval plan runner is unavailable"),
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, operationContextError(err)
	}
	if err := validateWorkspaceAnalysisRetrievalPlanRequest(request); err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, err
	}

	schemaRef := domain.SchemaRef{ID: domain.WorkspaceAnalysisPlanSchemaID, Version: domain.OutputSchemaVersionV1}
	snapshot, err := runner.catalog.Snapshot(request.PromptRef, schemaRef, schemaRef, request.ProfileRef)
	if err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, err
	}
	if snapshot.Schema.Ref != schemaRef || snapshot.ReducedSchema.Ref != schemaRef ||
		snapshot.Profile.MaxOutputTokens < int(WorkspaceAnalysisV1PlanMaxOutputTokens) {
		return WorkspaceAnalysisRetrievalPlanResult{}, workspaceAnalysisModelError(
			errors.New("workspace analysis retrieval plan runtime snapshot drifted"),
		)
	}

	ids, err := runner.newWorkspaceAnalysisPlanIDs()
	if err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, applicationError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable,
			true,
			err,
		)
	}
	if err := validateWorkspaceAnalysisPlanGeneratedIDs(request, ids); err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, applicationError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable,
			false,
			err,
		)
	}
	canonicalInput, sourceQuery, err := canonicalWorkspaceAnalysisPlanInput(request.Input)
	if err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, err
	}
	if workspaceAnalysisPlanDocumentLeaksIdentity(canonicalInput, request, ids) {
		return WorkspaceAnalysisRetrievalPlanResult{}, workspaceAnalysisModelError(
			errors.New("workspace analysis retrieval plan input contains server identity"),
		)
	}

	chatRequest := buildChatRequest(
		snapshot,
		snapshot.Schema,
		domain.ModelCallPlan,
		snapshot.Prompt.InitialInstruction,
		canonicalInput,
		"",
	)
	chatRequest.MaxOutputTokens = int(WorkspaceAnalysisV1PlanMaxOutputTokens)
	requestDocument, err := canonicalWorkspaceAnalysisChatRequest(chatRequest)
	if err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, err
	}
	startedAt := runner.clock.Now().UTC()
	if startedAt.IsZero() {
		return WorkspaceAnalysisRetrievalPlanResult{}, workspaceAnalysisModelError(
			errors.New("workspace analysis retrieval plan clock returned zero time"),
		)
	}
	operationKey := domain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: request.AnalysisRunID,
		NodeKey:       domain.WorkspaceAnalysisOperationNodeRetrieveEvidence,
		Kind:          domain.WorkspaceAnalysisOperationRetrievalPlan,
		Ordinal:       1,
	}
	modelSettingsRevision := cloneWorkspaceAnalysisOptionalInt64(request.ModelSettingsRevision)
	run := domain.ModelRun{
		ID: ids.modelRunID, WorkspaceID: request.Identity.WorkspaceID,
		WorkflowRunID: request.Identity.WorkflowRunID, NodeRunID: request.Identity.NodeRunID,
		NodeAttemptID: request.Identity.NodeAttemptID, ModelSettingsRevision: modelSettingsRevision,
		Model: snapshot.Profile.Model, Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref,
		Schema: snapshot.Schema.Ref, ReducedSchema: snapshot.ReducedSchema.Ref, Retrieval: cloneWorkspaceAnalysisRetrieval(request.Retrieval),
		Status: domain.ModelRunRunning, Version: 1, CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	call := domain.ModelCall{
		ID: ids.modelCallID, ModelRunID: ids.modelRunID, CallNo: 1, Phase: domain.ModelCallPlan,
		Model: snapshot.Profile.Model, Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref,
		Schema: snapshot.Schema.Ref, MaxOutputTokens: int(WorkspaceAnalysisV1PlanMaxOutputTokens),
		Status: domain.ModelCallStarted, RequestHash: workspaceAnalysisSHA256(requestDocument),
		RequestBytes: int64(len(requestDocument)), Version: 1, StartedAt: startedAt,
	}
	authorizationCommand := AuthorizeWorkspaceAnalysisModelCallCommand{
		Identity: request.Identity, OperationKey: operationKey, OperationID: ids.operationID,
		ReservationID: ids.reservationID, Run: run, Call: call, RequestDocument: requestDocument,
	}
	if err := authorizationCommand.Validate(); err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, err
	}
	authorized, err := runner.repository.AuthorizeWorkspaceAnalysisModelCall(ctx, authorizationCommand)
	if err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := authorized.ValidateFor(authorizationCommand); err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, err
	}

	switch authorized.Disposition {
	case WorkspaceAnalysisModelAuthorizationReuseResult:
		return runner.loadWorkspaceAnalysisPlanResult(ctx, request, authorized)
	case WorkspaceAnalysisModelAuthorizationReconcile:
		return WorkspaceAnalysisRetrievalPlanResult{}, applicationError(
			foundation.ErrorManualRecoveryRequired,
			ErrorCodeModelCallReplayUnsafe,
			false,
			errors.New("workspace analysis retrieval plan call requires exact reconciliation"),
		)
	case WorkspaceAnalysisModelAuthorizationReplayFailure:
		return WorkspaceAnalysisRetrievalPlanResult{}, workspaceAnalysisModelTerminalError(
			workspaceAnalysisReplayedModelFailure(authorized.Call, authorized.Run), authorized.OperationID, authorized.Call, authorized.Run,
		)
	case WorkspaceAnalysisModelAuthorizationTerminateUnknown:
		terminal := applicationError(
			foundation.ErrorManualRecoveryRequired,
			stableTerminalErrorCode(authorized.Call.ErrorCode, nil, ErrorCodeModelCallPersistenceUnknown),
			false,
			errors.New("workspace analysis retrieval plan outcome is unknown"),
		)
		return WorkspaceAnalysisRetrievalPlanResult{}, workspaceAnalysisModelTerminalError(
			terminal, authorized.OperationID, authorized.Call, authorized.Run,
		)
	case WorkspaceAnalysisModelAuthorizationCreated:
		// The only disposition authorized to cross the Provider boundary.
	default:
		return WorkspaceAnalysisRetrievalPlanResult{}, workspaceAnalysisModelAuthorizationError(
			errors.New("workspace analysis retrieval plan authorization disposition is unsupported"),
		)
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, runner.finalizeWorkspaceAnalysisPlanFailure(
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
		return WorkspaceAnalysisRetrievalPlanResult{}, runner.finalizeWorkspaceAnalysisPlanFailure(
			ctx, authorizationCommand, authorized, response, callErr,
		)
	}

	providerPlan, err := decodeWorkspaceAnalysisProviderPlan(response.Content, snapshot.Schema)
	if err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, runner.finalizeWorkspaceAnalysisPlanFailure(
			ctx, authorizationCommand, authorized, response, err,
		)
	}
	if workspaceAnalysisProviderPlanLeaksIdentity(providerPlan, request, ids, authorized) {
		return WorkspaceAnalysisRetrievalPlanResult{}, runner.finalizeWorkspaceAnalysisPlanFailure(
			ctx,
			authorizationCommand,
			authorized,
			response,
			applicationError(
				foundation.ErrorConsistencyViolation,
				domain.ErrorCodeStructuredOutputInvalid,
				false,
				errors.New("workspace analysis provider plan contains server identity"),
			),
		)
	}
	plan, document, err := composeCanonicalWorkspaceAnalysisPlan(providerPlan, authorized.Run.ID, sourceQuery)
	if err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, runner.finalizeWorkspaceAnalysisPlanFailure(
			ctx, authorizationCommand, authorized, response, err,
		)
	}
	return runner.finalizeWorkspaceAnalysisPlanSuccess(
		ctx, authorizationCommand, authorized, ids.modelResultID, plan, document, response.Usage,
	)
}

type workspaceAnalysisPlanIDs struct {
	operationID   foundation.ID
	reservationID foundation.ID
	modelRunID    foundation.ID
	modelCallID   foundation.ID
	modelResultID foundation.ID
}

func (runner *WorkspaceAnalysisRetrievalPlanRunner) newWorkspaceAnalysisPlanIDs() (workspaceAnalysisPlanIDs, error) {
	runner.idMu.Lock()
	defer runner.idMu.Unlock()
	values := make([]foundation.ID, 5)
	for index := range values {
		value, err := runner.ids.New()
		if err != nil {
			return workspaceAnalysisPlanIDs{}, err
		}
		values[index] = value
	}
	return workspaceAnalysisPlanIDs{
		operationID: values[0], reservationID: values[1], modelRunID: values[2],
		modelCallID: values[3], modelResultID: values[4],
	}, nil
}

func validateWorkspaceAnalysisRetrievalPlanRequest(request WorkspaceAnalysisRetrievalPlanRequest) error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	operationKey := domain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: request.AnalysisRunID,
		NodeKey:       domain.WorkspaceAnalysisOperationNodeRetrieveEvidence,
		Kind:          domain.WorkspaceAnalysisOperationRetrievalPlan,
		Ordinal:       1,
	}
	if request.Identity.NodeKey != domain.WorkspaceAnalysisOperationNodeRetrieveEvidence || operationKey.Validate() != nil ||
		request.ProfileRef.Validate() != nil || request.PromptRef.Validate() != nil || request.Retrieval.Validate() != nil ||
		(request.ModelSettingsRevision != nil && *request.ModelSettingsRevision < 0) || len(request.Input) == 0 ||
		len(request.Input) > MaxStructuredInputBytes {
		return workspaceAnalysisModelError(errors.New("workspace analysis retrieval plan request is invalid"))
	}
	ids := []foundation.ID{
		request.AnalysisRunID, request.Identity.WorkspaceID, request.Identity.DefinitionID,
		request.Identity.WorkflowRunID, request.Identity.NodeRunID, request.Identity.NodeAttemptID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return workspaceAnalysisModelError(errors.New("workspace analysis retrieval plan request id is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return workspaceAnalysisModelError(errors.New("workspace analysis retrieval plan request id is reused"))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func canonicalWorkspaceAnalysisPlanInput(raw []byte) ([]byte, string, error) {
	limits := domain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = MaxStructuredInputBytes
	value, err := domain.DecodeStrict[map[string]any](raw, limits, nil)
	if err != nil || value == nil {
		return nil, "", workspaceAnalysisModelError(errOrWorkspaceAnalysisPlan(err, "workspace analysis retrieval plan input is invalid"))
	}
	if workspaceAnalysisPlanValueHasReservedIdentity(value) {
		return nil, "", workspaceAnalysisModelError(errors.New("workspace analysis retrieval plan input contains a reserved identity field"))
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) == 0 || len(canonical) > MaxStructuredInputBytes {
		return nil, "", workspaceAnalysisModelError(errOrWorkspaceAnalysisPlan(err, "workspace analysis retrieval plan input cannot be canonicalized"))
	}
	sourceQuery, err := validateIdentitylessQueryPlanInput(canonical)
	if err != nil {
		return nil, "", err
	}
	return canonical, sourceQuery, nil
}

func workspaceAnalysisPlanValueHasReservedIdentity(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		for key, item := range current {
			switch strings.ToLower(key) {
			case "workspace_id", "definition_id", "workflow_run_id", "node_run_id", "node_attempt_id",
				"analysis_run_id", "operation_id", "reservation_id", "model_run_id", "model_run_ref",
				"model_call_id", "index_version_id", "embedding_version_id", "definition_hash",
				"definition_version", "model_settings_revision", "lease_owner", "lease_fence":
				return true
			}
			if workspaceAnalysisPlanValueHasReservedIdentity(item) {
				return true
			}
		}
	case []any:
		for _, item := range current {
			if workspaceAnalysisPlanValueHasReservedIdentity(item) {
				return true
			}
		}
	}
	return false
}

func canonicalWorkspaceAnalysisChatRequest(request ChatRequest) ([]byte, error) {
	if err := ValidateChatRequest(request); err != nil {
		return nil, err
	}
	document, err := json.Marshal(request)
	if err != nil || len(document) == 0 || int64(len(document)) > MaxModelCallRequestBytes {
		return nil, applicationError(
			foundation.ErrorNonRetryableFailure,
			errorCodeRequestEncode,
			false,
			errOrWorkspaceAnalysisPlan(err, "workspace analysis chat request cannot be encoded"),
		)
	}
	var decoded ChatRequest
	if err := json.Unmarshal(document, &decoded); err != nil || !reflect.DeepEqual(decoded, request) {
		return nil, applicationError(
			foundation.ErrorConsistencyViolation,
			errorCodeRequestEncode,
			false,
			errors.New("workspace analysis chat request canonical round trip drifted"),
		)
	}
	return document, nil
}

func decodeWorkspaceAnalysisProviderPlan(
	raw []byte,
	schema SchemaDefinition,
) (domain.RAGQueryPlanProviderResultV2, error) {
	if len(raw) == 0 || int64(len(raw)) > domain.MaxWorkspaceAnalysisModelResultBytes {
		return domain.RAGQueryPlanProviderResultV2{}, applicationError(
			foundation.ErrorConsistencyViolation,
			domain.ErrorCodeStructuredOutputLimitExceeded,
			false,
			errors.New("workspace analysis provider plan exceeds the bounded result limit"),
		)
	}
	decoded, err := schema.Decode(append([]byte(nil), raw...))
	if err != nil {
		return domain.RAGQueryPlanProviderResultV2{}, err
	}
	if !bytes.Equal(decoded, raw) {
		return domain.RAGQueryPlanProviderResultV2{}, applicationError(
			foundation.ErrorConsistencyViolation,
			errorCodeDecoderContract,
			false,
			errors.New("workspace analysis plan decoder transformed provider output"),
		)
	}
	limits := domain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = int(domain.MaxWorkspaceAnalysisModelResultBytes)
	providerPlan, err := domain.DecodeRAGQueryPlanProviderV2(raw, limits)
	if err != nil {
		return domain.RAGQueryPlanProviderResultV2{}, err
	}
	return providerPlan, nil
}

func composeCanonicalWorkspaceAnalysisPlan(
	providerPlan domain.RAGQueryPlanProviderResultV2,
	modelRunID foundation.ID,
	sourceQuery string,
) (domain.WorkspaceAnalysisPlanResult, []byte, error) {
	plan, err := providerPlan.ComposeWorkspaceAnalysis(modelRunID, sourceQuery)
	if err != nil {
		return domain.WorkspaceAnalysisPlanResult{}, nil, err
	}
	document, err := json.Marshal(plan)
	if err != nil || int64(len(document)) > domain.MaxWorkspaceAnalysisModelResultBytes {
		return domain.WorkspaceAnalysisPlanResult{}, nil, workspaceAnalysisModelError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis plan result cannot be canonicalized"),
		)
	}
	decoded, err := domain.DecodeWorkspaceAnalysisPlan(document, domain.DefaultDecodeLimits())
	if err != nil || !reflect.DeepEqual(decoded, plan) {
		return domain.WorkspaceAnalysisPlanResult{}, nil, workspaceAnalysisModelError(
			errOrWorkspaceAnalysisPlan(err, "workspace analysis plan canonical round trip drifted"),
		)
	}
	return plan, document, nil
}

func validateWorkspaceAnalysisPlanGeneratedIDs(
	request WorkspaceAnalysisRetrievalPlanRequest,
	generated workspaceAnalysisPlanIDs,
) error {
	ids := []foundation.ID{
		request.AnalysisRunID, request.Identity.WorkspaceID, request.Identity.DefinitionID,
		request.Identity.WorkflowRunID, request.Identity.NodeRunID, request.Identity.NodeAttemptID,
		generated.operationID, generated.reservationID, generated.modelRunID,
		generated.modelCallID, generated.modelResultID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return errors.New("workspace analysis retrieval plan generated id is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("workspace analysis retrieval plan generated id is reused")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (runner *WorkspaceAnalysisRetrievalPlanRunner) finalizeWorkspaceAnalysisPlanSuccess(
	ctx context.Context,
	authorizationCommand AuthorizeWorkspaceAnalysisModelCallCommand,
	authorized WorkspaceAnalysisModelAuthorizationResult,
	resultID foundation.ID,
	plan domain.WorkspaceAnalysisPlanResult,
	document []byte,
	usage domain.TokenUsage,
) (WorkspaceAnalysisRetrievalPlanResult, error) {
	completedAt := workspaceAnalysisPlanCompletedAt(runner.clock.Now().UTC(), authorized.Run, authorized.Call)
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
	run.FinalResultType = domain.ResultTypeWorkspaceAnalysisPlan
	run.Version++
	run.UpdatedAt = completedAt
	run.CompletedAt = &completedAt
	modelResult := domain.WorkspaceAnalysisModelResult{
		ID: resultID, WorkspaceID: authorizationCommand.Identity.WorkspaceID,
		AnalysisRunID: authorizationCommand.OperationKey.AnalysisRunID, OperationID: authorized.OperationID,
		NodeAttemptID: authorized.Run.NodeAttemptID, ModelRunID: authorized.Run.ID, ModelCallID: authorized.Call.ID,
		OperationKind: domain.WorkspaceAnalysisOperationRetrievalPlan, Schema: authorized.Run.Schema,
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
		return WorkspaceAnalysisRetrievalPlanResult{}, runner.finalizationUnknown(ctx, err)
	}
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceAnalysisPlanFinalizationTimeout)
	defer cancel()
	mutation, err := runner.repository.FinalizeWorkspaceAnalysisModelResult(persistContext, command)
	if err != nil {
		if WorkspaceAnalysisModelCancellationConflict(err) {
			// The Provider response is known, but cancellation won before the
			// immutable plan could be published. Settle the Call and let the
			// workflow cancellation checkpoint close the run.
			return WorkspaceAnalysisRetrievalPlanResult{}, runner.finalizeWorkspaceAnalysisPlanFailure(
				ctx, authorizationCommand, authorized,
				ChatResponse{Content: append([]byte(nil), document...), Usage: usage},
				operationContextError(context.Canceled),
			)
		}
		return WorkspaceAnalysisRetrievalPlanResult{}, runner.finalizationUnknown(ctx, err)
	}
	if err := validateWorkspaceAnalysisPlanResultMutation(command, mutation); err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, runner.finalizationUnknown(ctx, err)
	}
	return workspaceAnalysisRetrievalPlanResult(mutation, plan, mutation.Replayed)
}

func (runner *WorkspaceAnalysisRetrievalPlanRunner) finalizeWorkspaceAnalysisPlanFailure(
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

func (runner *WorkspaceAnalysisRetrievalPlanRunner) loadWorkspaceAnalysisPlanResult(
	ctx context.Context,
	request WorkspaceAnalysisRetrievalPlanRequest,
	authorized WorkspaceAnalysisModelAuthorizationResult,
) (WorkspaceAnalysisRetrievalPlanResult, error) {
	query := WorkspaceAnalysisModelResultQuery{
		WorkspaceID: request.Identity.WorkspaceID, AnalysisRunID: request.AnalysisRunID,
		OperationID: authorized.OperationID, ModelRunID: authorized.Run.ID, ModelCallID: authorized.Call.ID,
	}
	if err := query.Validate(); err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, err
	}
	modelResult, err := runner.repository.LoadWorkspaceAnalysisModelResult(ctx, query)
	if err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := validateLoadedWorkspaceAnalysisPlanResult(request, authorized, modelResult); err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, err
	}
	plan, err := domain.DecodeWorkspaceAnalysisPlan(modelResult.Document, domain.DefaultDecodeLimits())
	if err != nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, workspaceAnalysisModelAuthorizationError(err)
	}
	mutation := WorkspaceAnalysisModelMutationResult{
		Run: authorized.Run, Call: authorized.Call, Result: &modelResult,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID, Replayed: true,
	}
	return workspaceAnalysisRetrievalPlanResult(mutation, plan, true)
}

func validateLoadedWorkspaceAnalysisPlanResult(
	request WorkspaceAnalysisRetrievalPlanRequest,
	authorized WorkspaceAnalysisModelAuthorizationResult,
	result domain.WorkspaceAnalysisModelResult,
) error {
	if err := domain.ValidateWorkspaceAnalysisModelResult(result); err != nil {
		return workspaceAnalysisModelAuthorizationError(err)
	}
	if result.WorkspaceID != request.Identity.WorkspaceID || result.AnalysisRunID != request.AnalysisRunID ||
		result.OperationID != authorized.OperationID || result.NodeAttemptID != authorized.Run.NodeAttemptID ||
		result.ModelRunID != authorized.Run.ID || result.ModelCallID != authorized.Call.ID ||
		result.OperationKind != domain.WorkspaceAnalysisOperationRetrievalPlan || result.Schema != authorized.Run.Schema ||
		authorized.Run.Status != domain.ModelRunSucceeded || authorized.Run.FinalResultType != domain.ResultTypeWorkspaceAnalysisPlan ||
		authorized.Run.FinalErrorCode != "" || authorized.Call.Status != domain.ModelCallSucceeded ||
		authorized.Call.ResponseHash != result.DocumentHash || authorized.Call.ResponseBytes != result.DocumentBytes ||
		authorized.Call.Usage.Validate() != nil || authorized.Run.CompletedAt == nil || authorized.Call.CompletedAt == nil ||
		result.CreatedAt.Before(*authorized.Run.CompletedAt) || result.CreatedAt.Before(*authorized.Call.CompletedAt) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis retrieval plan replay result drifted"))
	}
	return nil
}

func validateWorkspaceAnalysisPlanResultMutation(
	command FinalizeWorkspaceAnalysisModelResultCommand,
	mutation WorkspaceAnalysisModelMutationResult,
) error {
	if mutation.Result == nil || mutation.Candidate != nil || mutation.OperationID != command.OperationID || mutation.ReservationID != command.ReservationID ||
		!reflect.DeepEqual(mutation.Run, command.Run) || !reflect.DeepEqual(mutation.Call, command.Call) ||
		!reflect.DeepEqual(*mutation.Result, command.Result) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis retrieval plan finalization drifted"))
	}
	return nil
}

func validateWorkspaceAnalysisPlanFailureMutation(
	command FinalizeWorkspaceAnalysisModelCallCommand,
	mutation WorkspaceAnalysisModelMutationResult,
) error {
	if mutation.Result != nil || mutation.Candidate != nil || mutation.OperationID != command.OperationID || mutation.ReservationID != command.ReservationID ||
		!reflect.DeepEqual(mutation.Run, command.Run) || !reflect.DeepEqual(mutation.Call, command.Call) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis retrieval plan failure finalization drifted"))
	}
	return nil
}

func workspaceAnalysisRetrievalPlanResult(
	mutation WorkspaceAnalysisModelMutationResult,
	plan domain.WorkspaceAnalysisPlanResult,
	replayed bool,
) (WorkspaceAnalysisRetrievalPlanResult, error) {
	if mutation.Result == nil {
		return WorkspaceAnalysisRetrievalPlanResult{}, workspaceAnalysisModelAuthorizationError(
			errors.New("workspace analysis retrieval plan result is missing"),
		)
	}
	modelResult := *mutation.Result
	modelResult.Document = append(json.RawMessage(nil), mutation.Result.Document...)
	return WorkspaceAnalysisRetrievalPlanResult{
		Plan: plan, ModelResult: modelResult, Run: mutation.Run, Call: mutation.Call,
		Usage: mutation.Call.Usage, Replayed: replayed,
	}, nil
}

func classifyWorkspaceAnalysisModelFailure(cause error) (domain.ModelCallStatus, error) {
	if cause == nil {
		cause = errors.New("workspace analysis model call failed without a cause")
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		cause = operationContextError(cause)
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		if classified.Kind == foundation.ErrorManualRecoveryRequired {
			return domain.ModelCallUnknown, cause
		}
		return domain.ModelCallFailed, cause
	}
	return domain.ModelCallUnknown, applicationError(
		foundation.ErrorManualRecoveryRequired,
		ErrorCodeModelCallPersistenceUnknown,
		false,
		errors.New("workspace analysis provider outcome is unclassified"),
	)
}

func (runner *WorkspaceAnalysisRetrievalPlanRunner) finalizationUnknown(ctx context.Context, cause error) error {
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

func workspaceAnalysisModelRepositoryError(err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	return applicationError(
		foundation.ErrorDependencyUnavailable,
		ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable,
		true,
		err,
	)
}

func workspaceAnalysisPlanCompletedAt(now time.Time, run domain.ModelRun, call domain.ModelCall) time.Time {
	completedAt := now.UTC()
	if completedAt.IsZero() || completedAt.Before(call.StartedAt) {
		completedAt = call.StartedAt
	}
	if completedAt.Before(run.UpdatedAt) {
		completedAt = run.UpdatedAt
	}
	return completedAt
}

func cloneWorkspaceAnalysisOptionalInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneWorkspaceAnalysisRetrieval(value domain.RetrievalRef) domain.RetrievalRef {
	cloned := value
	if value.EmbeddingVersionID != nil {
		embeddingID := *value.EmbeddingVersionID
		cloned.EmbeddingVersionID = &embeddingID
	}
	return cloned
}

func workspaceAnalysisPlanDocumentLeaksIdentity(
	document []byte,
	request WorkspaceAnalysisRetrievalPlanRequest,
	ids workspaceAnalysisPlanIDs,
) bool {
	values := []foundation.ID{
		request.Identity.WorkspaceID, request.Identity.DefinitionID, request.Identity.WorkflowRunID,
		request.Identity.NodeRunID, request.Identity.NodeAttemptID, request.AnalysisRunID,
		request.Retrieval.IndexVersionID,
		ids.operationID, ids.reservationID, ids.modelRunID, ids.modelCallID, ids.modelResultID,
	}
	if request.Retrieval.EmbeddingVersionID != nil {
		values = append(values, *request.Retrieval.EmbeddingVersionID)
	}
	for _, value := range values {
		if bytes.Contains(document, []byte(value)) {
			return true
		}
	}
	return bytes.Contains(document, []byte(request.Identity.DefinitionHash))
}

func workspaceAnalysisProviderPlanLeaksIdentity(
	plan domain.RAGQueryPlanProviderResultV2,
	request WorkspaceAnalysisRetrievalPlanRequest,
	ids workspaceAnalysisPlanIDs,
	authorized WorkspaceAnalysisModelAuthorizationResult,
) bool {
	document, err := json.Marshal(plan)
	if err != nil {
		return true
	}
	if workspaceAnalysisPlanDocumentLeaksIdentity(document, request, ids) {
		return true
	}
	for _, value := range []foundation.ID{authorized.Run.ID, authorized.Call.ID, authorized.OperationID, authorized.ReservationID} {
		if bytes.Contains(document, []byte(value)) {
			return true
		}
	}
	return false
}

func errOrWorkspaceAnalysisPlan(err error, message string) error {
	if err != nil {
		return err
	}
	return errors.New(message)
}
