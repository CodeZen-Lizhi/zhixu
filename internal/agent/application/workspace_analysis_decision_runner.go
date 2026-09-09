package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceAnalysisDecisionRunnerDependencies are the project-owned ports for
// authorizing and settling the Eino loop's individual model calls.
type WorkspaceAnalysisDecisionRunnerDependencies struct {
	Catalog    *RuntimeCatalog
	Repository WorkspaceAnalysisDecisionRepository
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
}

type WorkspaceAnalysisDecisionRunRequest struct {
	Identity              WorkspaceAnalysisModelExecutionIdentity
	AnalysisRunID         foundation.ID
	ModelSettingsRevision *int64
	Retrieval             domain.RetrievalRef
	ProfileRef            domain.ModelProfileRef
	PromptRef             domain.PromptRef
}

type WorkspaceAnalysisDecisionRunner struct {
	catalog    *RuntimeCatalog
	repository WorkspaceAnalysisDecisionRepository
	ids        foundation.IDGenerator
	clock      foundation.Clock
	idMu       sync.Mutex
}

func NewWorkspaceAnalysisDecisionRunner(dependencies WorkspaceAnalysisDecisionRunnerDependencies) (*WorkspaceAnalysisDecisionRunner, error) {
	if dependencies.Catalog == nil || isNilPort(dependencies.Repository) || isNilPort(dependencies.IDs) || isNilPort(dependencies.Clock) {
		return nil, applicationError(foundation.ErrorDependencyUnavailable, ErrorCodeWorkspaceAnalysisModelAuthorizationUnavailable, false,
			errors.New("workspace analysis decision runner dependencies are unavailable"))
	}
	return &WorkspaceAnalysisDecisionRunner{catalog: dependencies.Catalog, repository: dependencies.Repository, ids: dependencies.IDs, clock: dependencies.Clock}, nil
}

type workspaceAnalysisDecisionCaller struct {
	runner   *WorkspaceAnalysisDecisionRunner
	request  WorkspaceAnalysisDecisionRunRequest
	snapshot RuntimeSnapshot
}

// BindWorkspaceAnalysisLoop freezes the model generation and prompt for one
// claimed attempt. It performs no model call and allocates no budget.
func (runner *WorkspaceAnalysisDecisionRunner) BindWorkspaceAnalysisLoop(request WorkspaceAnalysisDecisionRunRequest) (WorkspaceAnalysisLoopModelCaller, error) {
	if runner == nil || request.Identity.Validate() != nil || request.Identity.DefinitionVersion != 2 ||
		request.Identity.NodeKey != domain.WorkspaceAnalysisOperationNodeDecideNext || !canonicalApplicationID(request.AnalysisRunID) ||
		request.ProfileRef.Validate() != nil || request.PromptRef.Validate() != nil ||
		(request.ModelSettingsRevision != nil && *request.ModelSettingsRevision < 0) ||
		(request.Retrieval != (domain.RetrievalRef{}) && request.Retrieval.Validate() != nil) {
		return nil, workspaceAnalysisModelError(errors.New("workspace analysis decision binding is invalid"))
	}
	schemaRef := domain.SchemaRef{ID: domain.WorkspaceAnalysisDecisionSchemaID, Version: domain.WorkspaceAnalysisDecisionSchemaVersion}
	snapshot, err := runner.catalog.Snapshot(request.PromptRef, schemaRef, schemaRef, request.ProfileRef)
	if err != nil {
		return nil, err
	}
	if snapshot.Profile.MaxOutputTokens < int(domain.WorkspaceAnalysisV2DecisionMaxOutputTokens) {
		return nil, workspaceAnalysisModelError(errors.New("workspace analysis model profile cannot support a decision"))
	}
	request.ModelSettingsRevision = cloneWorkspaceAnalysisOptionalInt64(request.ModelSettingsRevision)
	request.Retrieval = cloneWorkspaceAnalysisRetrieval(request.Retrieval)
	return &workspaceAnalysisDecisionCaller{runner: runner, request: request, snapshot: snapshot}, nil
}

func (caller *workspaceAnalysisDecisionCaller) CallWorkspaceAnalysisDecision(ctx context.Context, request WorkspaceAnalysisLoopModelCall) (WorkspaceAnalysisDecisionMutationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, operationContextError(err)
	}
	if request.Ordinal < 1 || request.Ordinal > domain.WorkspaceAnalysisV2MaxDecisions+1 || request.Invoke == nil ||
		len(request.RequestDocument) == 0 || len(request.RequestDocument) > MaxStructuredInputBytes ||
		!utf8.Valid(request.RequestDocument) || !json.Valid(request.RequestDocument) || !isJSONObject(request.RequestDocument) {
		return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelError(errors.New("workspace analysis decision request is invalid"))
	}
	journal, err := caller.runner.repository.LoadWorkspaceAnalysisJournal(ctx, caller.journalQuery())
	if err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := caller.validateJournal(journal); err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, err
	}
	command, err := caller.authorization(request, journal)
	if err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, err
	}
	if err := command.Validate(); err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, err
	}
	authorized, err := caller.runner.repository.AuthorizeWorkspaceAnalysisModelCall(ctx, command)
	if err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := authorized.ValidateFor(command); err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, err
	}
	switch authorized.Disposition {
	case WorkspaceAnalysisModelAuthorizationReuseResult:
		return caller.loadDecision(ctx, command, authorized, nil)
	case WorkspaceAnalysisModelAuthorizationReplayFailure:
		return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelTerminalError(
			workspaceAnalysisReplayedModelFailure(authorized.Call, authorized.Run), authorized.OperationID, authorized.Call, authorized.Run)
	case WorkspaceAnalysisModelAuthorizationReconcile, WorkspaceAnalysisModelAuthorizationTerminateUnknown:
		cause := applicationError(foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallReplayUnsafe, false,
			errors.New("workspace analysis decision requires exact reconciliation"))
		return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelTerminalError(cause, authorized.OperationID, authorized.Call, authorized.Run)
	case WorkspaceAnalysisModelAuthorizationCreated:
		// Only this branch may invoke a Provider.
	default:
		return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis decision authorization is inconsistent"))
	}
	if err := ctx.Err(); err != nil {
		return caller.finishDecision(ctx, command, authorized, WorkspaceAnalysisDecisionProviderResult{}, operationContextError(err))
	}
	callContext, cancel := context.WithTimeout(ctx, caller.snapshot.Profile.Timeout)
	response, invokeErr := request.Invoke(callContext)
	contextErr := callContext.Err()
	cancel()
	if invokeErr == nil && contextErr != nil {
		invokeErr = operationContextError(contextErr)
	}
	if response.Usage.Validate() != nil {
		response.Usage = domain.TokenUsage{}
		invokeErr = workspaceAnalysisDecisionUnknown(errors.New("workspace analysis decision returned invalid token usage"))
	}
	if invokeErr == nil {
		if err := response.Decision.Validate(); err != nil {
			invokeErr = err
		} else if document, err := response.Decision.Canonical(); err != nil || caller.leaksIdentity(document, command, authorized) {
			invokeErr = workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis decision contains a server identity"))
		}
	}
	return caller.finishDecision(ctx, command, authorized, response, invokeErr)
}

func (caller *workspaceAnalysisDecisionCaller) authorization(request WorkspaceAnalysisLoopModelCall, journal WorkspaceAnalysisJournalSnapshot) (AuthorizeWorkspaceAnalysisModelCallCommand, error) {
	runner := caller.runner
	runner.idMu.Lock()
	ids := make([]foundation.ID, 4)
	for index := range ids {
		id, err := runner.ids.New()
		if err != nil {
			runner.idMu.Unlock()
			return AuthorizeWorkspaceAnalysisModelCallCommand{}, workspaceAnalysisModelRepositoryError(err)
		}
		ids[index] = id
	}
	runner.idMu.Unlock()
	now := runner.clock.Now().UTC()
	if now.IsZero() {
		return AuthorizeWorkspaceAnalysisModelCallCommand{}, workspaceAnalysisModelError(errors.New("workspace analysis decision clock is invalid"))
	}
	snapshot, binding := caller.snapshot, caller.request
	run := domain.ModelRun{
		ID: ids[0], WorkspaceID: binding.Identity.WorkspaceID, WorkflowRunID: binding.Identity.WorkflowRunID,
		NodeRunID: binding.Identity.NodeRunID, NodeAttemptID: binding.Identity.NodeAttemptID,
		ModelSettingsRevision: cloneWorkspaceAnalysisOptionalInt64(binding.ModelSettingsRevision),
		Model:                 snapshot.Profile.Model, Profile: snapshot.Profile.Ref, Prompt: snapshot.Prompt.Ref,
		Schema: snapshot.Schema.Ref, ReducedSchema: snapshot.Schema.Ref, Retrieval: cloneWorkspaceAnalysisRetrieval(binding.Retrieval),
		Status: domain.ModelRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	callNo := 1
	var replay *WorkspaceAnalysisJournalEntry
	for index := range journal.Entries {
		entry := &journal.Entries[index]
		if entry.Operation.NodeKey != domain.WorkspaceAnalysisOperationNodeDecideNext || entry.Operation.Kind != domain.WorkspaceAnalysisOperationDecision {
			continue
		}
		if entry.Operation.Ordinal == request.Ordinal {
			replay = entry
			continue
		}
		if entry.ModelRun != nil && entry.ModelRun.NodeAttemptID == binding.Identity.NodeAttemptID && entry.ModelRun.Status == domain.ModelRunRunning {
			if run.ID != ids[0] && run.ID != entry.ModelRun.ID {
				return AuthorizeWorkspaceAnalysisModelCallCommand{}, workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis attempt owns multiple model runs"))
			}
			run = *entry.ModelRun
			if entry.ModelCall != nil {
				callNo = max(callNo, entry.ModelCall.CallNo+1)
			}
		}
	}
	if replay != nil && replay.ModelCall != nil {
		callNo = replay.ModelCall.CallNo
	}
	call := domain.ModelCall{ID: ids[1], ModelRunID: run.ID, CallNo: callNo,
		Phase: domain.ModelCallAgent, Model: snapshot.Profile.Model, Profile: snapshot.Profile.Ref,
		Prompt: snapshot.Prompt.Ref, Schema: snapshot.Schema.Ref,
		MaxOutputTokens: int(domain.WorkspaceAnalysisV2DecisionMaxOutputTokens), Status: domain.ModelCallStarted,
		RequestHash: workspaceAnalysisSHA256(request.RequestDocument), RequestBytes: int64(len(request.RequestDocument)), Version: 1, StartedAt: now,
	}
	command := AuthorizeWorkspaceAnalysisModelCallCommand{Identity: binding.Identity,
		OperationKey: domain.WorkspaceAnalysisOperationKey{AnalysisRunID: binding.AnalysisRunID, NodeKey: domain.WorkspaceAnalysisOperationNodeDecideNext, Kind: domain.WorkspaceAnalysisOperationDecision, Ordinal: request.Ordinal},
		OperationID:  ids[2], ReservationID: ids[3], Run: run, Call: call, RequestDocument: append([]byte(nil), request.RequestDocument...),
	}
	if replay != nil && replay.Operation.Status == domain.WorkspaceAnalysisOperationPending && replay.Operation.Call == nil {
		// A committed admission denial can outlive its failed publication.
		// Keep its journal identity so retry proves the same denied operation.
		command.OperationID = replay.Operation.ID
	}
	if caller.leaksIdentity(request.RequestDocument, command, WorkspaceAnalysisModelAuthorizationResult{}) {
		return AuthorizeWorkspaceAnalysisModelCallCommand{}, workspaceAnalysisModelError(errors.New("workspace analysis model request contains a server identity"))
	}
	return command, nil
}

func (caller *workspaceAnalysisDecisionCaller) finishDecision(ctx context.Context, authorization AuthorizeWorkspaceAnalysisModelCallCommand, authorized WorkspaceAnalysisModelAuthorizationResult, response WorkspaceAnalysisDecisionProviderResult, cause error) (WorkspaceAnalysisDecisionMutationResult, error) {
	status, code := domain.ModelCallSucceeded, ""
	var decision *domain.WorkspaceAnalysisDecision
	var receiptID foundation.ID
	if cause != nil {
		status, cause = classifyWorkspaceAnalysisModelFailure(cause)
		code = stableAgentErrorCode(cause, ErrorCodeModelCallFailed)
		if status == domain.ModelCallUnknown {
			response.Usage = domain.TokenUsage{}
		}
	} else {
		value := response.Decision
		decision = &value
		caller.runner.idMu.Lock()
		generated, err := caller.runner.ids.New()
		caller.runner.idMu.Unlock()
		if err != nil {
			return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisDecisionUnknown(err)
		}
		receiptID = generated
	}
	completedAt := workspaceAnalysisPlanCompletedAt(caller.runner.clock.Now().UTC(), authorized.Run, authorized.Call)
	command := FinalizeWorkspaceAnalysisDecisionCommand{Identity: caller.request.Identity, OperationKey: authorization.OperationKey,
		OperationID: authorized.OperationID, ExpectedCallVersion: authorized.Call.Version, ReceiptID: receiptID,
		Status: status, Decision: decision, Usage: response.Usage, LatencyMillis: max(int64(0), completedAt.Sub(authorized.Call.StartedAt).Milliseconds()), ErrorCode: code,
	}
	if err := command.Validate(); err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisDecisionUnknown(err)
	}
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceAnalysisPlanFinalizationTimeout)
	defer cancel()
	result, err := caller.runner.repository.FinalizeWorkspaceAnalysisDecision(persistContext, command)
	if err != nil {
		// A lost commit acknowledgement cannot authorize a second Provider call.
		// Recover only the exact operation and its immutable terminal receipt.
		if recovered, lookupErr := caller.loadDecision(persistContext, authorization, authorized, &command); lookupErr == nil {
			result, err = recovered, nil
		} else if cause == nil && WorkspaceAnalysisModelCancellationConflict(err) {
			return caller.finishDecision(ctx, authorization, authorized, response, operationContextError(context.Canceled))
		} else {
			return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisDecisionUnknown(errors.Join(err, lookupErr))
		}
	}
	if err := validateWorkspaceAnalysisDecisionMutation(authorization, authorized, result, &command); err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisDecisionUnknown(err)
	}
	if cause != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelTerminalError(cause, result.Operation.ID, result.Call, result.Run)
	}
	return result, nil
}

func (caller *workspaceAnalysisDecisionCaller) loadDecision(ctx context.Context, authorization AuthorizeWorkspaceAnalysisModelCallCommand, authorized WorkspaceAnalysisModelAuthorizationResult, terminal *FinalizeWorkspaceAnalysisDecisionCommand) (WorkspaceAnalysisDecisionMutationResult, error) {
	journal, err := caller.runner.repository.LoadWorkspaceAnalysisJournal(ctx, caller.journalQuery())
	if err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelRepositoryError(err)
	}
	if err := caller.validateJournal(journal); err != nil {
		return WorkspaceAnalysisDecisionMutationResult{}, err
	}
	for _, entry := range journal.Entries {
		if entry.Operation.ID != authorized.OperationID {
			continue
		}
		if entry.ModelRun == nil || entry.ModelCall == nil {
			break
		}
		result := WorkspaceAnalysisDecisionMutationResult{Run: *entry.ModelRun, Call: *entry.ModelCall, Operation: entry.Operation, Decision: entry.Decision, Replayed: true}
		if err := validateWorkspaceAnalysisDecisionMutation(authorization, authorized, result, terminal); err != nil {
			return WorkspaceAnalysisDecisionMutationResult{}, err
		}
		return result, nil
	}
	return WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis decision receipt is not proven"))
}

func validateWorkspaceAnalysisDecisionMutation(authorization AuthorizeWorkspaceAnalysisModelCallCommand, authorized WorkspaceAnalysisModelAuthorizationResult, result WorkspaceAnalysisDecisionMutationResult, terminal *FinalizeWorkspaceAnalysisDecisionCommand) error {
	if domain.ValidateModelRun(result.Run) != nil || domain.ValidateModelCall(result.Call) != nil || domain.ValidateWorkspaceAnalysisOperation(result.Operation) != nil ||
		result.Run.ID != authorized.Run.ID || result.Call.ID != authorized.Call.ID || result.Call.ModelRunID != result.Run.ID ||
		result.Run.NodeAttemptID != authorized.Run.NodeAttemptID || !sameWorkspaceAnalysisModelRequest(authorized.Run, authorized.Call, result.Run, result.Call) ||
		result.Operation.ID != authorized.OperationID || result.Operation.LogicalKey() != authorization.OperationKey || result.Operation.RequestHash != result.Call.RequestHash ||
		result.Operation.BudgetReservationID == nil || *result.Operation.BudgetReservationID != authorized.ReservationID ||
		result.Operation.Call == nil || result.Operation.Call.ID != result.Call.ID || result.Call.CompletedAt == nil {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis decision mutation has a different binding"))
	}
	if terminal != nil && (result.Call.Status != terminal.Status || result.Call.Usage != terminal.Usage || result.Call.ErrorCode != terminal.ErrorCode || result.Call.LatencyMillis != terminal.LatencyMillis) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis decision settlement drifted"))
	}
	if result.Call.Status == domain.ModelCallSucceeded {
		if result.Operation.Status != domain.WorkspaceAnalysisOperationSucceeded || result.Decision == nil || result.Decision.Validate() != nil || result.Decision.Ordinal != authorization.OperationKey.Ordinal ||
			result.Decision.OperationID != result.Operation.ID || result.Decision.WorkspaceID != authorization.Identity.WorkspaceID ||
			result.Decision.AnalysisRunID != authorization.OperationKey.AnalysisRunID || result.Decision.NodeAttemptID != result.Run.NodeAttemptID ||
			result.Decision.ModelCallID != result.Call.ID || result.Decision.ModelRunID != result.Run.ID ||
			result.Decision.DocumentHash != result.Call.ResponseHash || result.Decision.DocumentBytes != result.Call.ResponseBytes ||
			result.Operation.Result == nil || result.Operation.Result.ID != result.Decision.ID || result.Operation.Result.Hash != result.Decision.DocumentHash ||
			terminal != nil && (!reflect.DeepEqual(terminal.Decision, &result.Decision.Decision) || terminal.ReceiptID != result.Decision.ID) {
			return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis immutable decision receipt drifted"))
		}
		if result.Decision.Decision.Action == domain.WorkspaceAnalysisDecisionFinish &&
			(result.Run.Status != domain.ModelRunSucceeded || result.Run.FinalResultType != domain.ResultTypeWorkspaceAnalysisDecision) {
			return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis finish did not close its model run"))
		}
	} else if result.Decision != nil || terminal == nil ||
		!(result.Call.Status == domain.ModelCallFailed && result.Run.Status == domain.ModelRunFailed && result.Operation.Status == domain.WorkspaceAnalysisOperationFailed ||
			result.Call.Status == domain.ModelCallUnknown && result.Run.Status == domain.ModelRunUnknown && result.Operation.Status == domain.WorkspaceAnalysisOperationUnknown) {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis decision terminal state is inconsistent"))
	}
	return nil
}

func (caller *workspaceAnalysisDecisionCaller) journalQuery() WorkspaceAnalysisJournalQuery {
	return WorkspaceAnalysisJournalQuery{WorkspaceID: caller.request.Identity.WorkspaceID, AnalysisRunID: caller.request.AnalysisRunID, WorkflowRunID: caller.request.Identity.WorkflowRunID}
}

func (caller *workspaceAnalysisDecisionCaller) validateJournal(journal WorkspaceAnalysisJournalSnapshot) error {
	if domain.ValidateWorkspaceAnalysisRun(journal.Run) != nil || journal.Run.ID != caller.request.AnalysisRunID ||
		journal.Run.WorkspaceID != caller.request.Identity.WorkspaceID || journal.Run.WorkflowRunID != caller.request.Identity.WorkflowRunID ||
		journal.Run.DefinitionVersion != 2 || journal.Run.PolicyVersion != domain.WorkspaceAnalysisPolicyVersionV2 ||
		journal.Run.DefinitionHash != caller.request.Identity.DefinitionHash {
		return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis journal has a different owner or version"))
	}
	for index, entry := range journal.Entries {
		if entry.Sequence != int64(index+1) || entry.Operation.AnalysisRunID != journal.Run.ID || domain.ValidateWorkspaceAnalysisOperation(entry.Operation) != nil {
			return workspaceAnalysisModelAuthorizationError(errors.New("workspace analysis journal sequence is invalid"))
		}
	}
	return nil
}

func (caller *workspaceAnalysisDecisionCaller) leaksIdentity(document []byte, command AuthorizeWorkspaceAnalysisModelCallCommand, authorized WorkspaceAnalysisModelAuthorizationResult) bool {
	ids := []foundation.ID{caller.request.AnalysisRunID, caller.request.Identity.WorkspaceID, caller.request.Identity.DefinitionID,
		caller.request.Identity.WorkflowRunID, caller.request.Identity.NodeRunID, caller.request.Identity.NodeAttemptID,
		caller.request.Retrieval.IndexVersionID, command.OperationID, command.ReservationID, command.Run.ID, command.Call.ID,
		authorized.Run.ID, authorized.Call.ID, authorized.OperationID, authorized.ReservationID}
	if caller.request.Retrieval.EmbeddingVersionID != nil {
		ids = append(ids, *caller.request.Retrieval.EmbeddingVersionID)
	}
	for _, id := range ids {
		if id != "" && bytes.Contains(document, []byte(id)) {
			return true
		}
	}
	return bytes.Contains(document, []byte(caller.request.Identity.DefinitionHash))
}

func workspaceAnalysisDecisionUnknown(cause error) error {
	return applicationError(foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallPersistenceUnknown, false, cause)
}

var _ WorkspaceAnalysisLoopModelCaller = (*workspaceAnalysisDecisionCaller)(nil)
