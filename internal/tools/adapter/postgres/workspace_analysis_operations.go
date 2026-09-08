package postgres

import (
	"errors"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

var (
	workspaceAnalysisGitStatusRef = domain.ToolRef{Name: "ReadGitStatus", Version: 2}
	workspaceAnalysisCitationRef  = domain.ToolRef{Name: "ValidateCitation", Version: 3}
)

func validateWorkspaceAnalysisToolAuthorization(command application.AuthorizeWorkspaceAnalysisToolCallCommand) (domain.ToolCall, domain.Definition, agentdomain.WorkspaceAnalysisOperationContract, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, err
	}
	operationID, operationIDErr := foundation.ParseID(string(command.OperationID))
	reservationID, reservationIDErr := foundation.ParseID(string(command.ReservationID))
	if err := command.OperationKey.Validate(); err != nil || command.OperationKey.AnalysisRunID == command.Identity.WorkspaceID ||
		operationIDErr != nil || reservationIDErr != nil || operationID != command.OperationID || reservationID != command.ReservationID ||
		command.OperationID == command.ReservationID || command.OperationID == command.OperationKey.AnalysisRunID || command.ReservationID == command.OperationKey.AnalysisRunID {
		if err == nil {
			err = errors.New("workspace analysis authorization ids are invalid")
		}
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, foundation.NewError(foundation.ErrorInvalidInput, agentdomain.ErrorCodeWorkspaceAnalysisOperationInvalid, false, err)
	}
	contract, err := agentdomain.WorkspaceAnalysisOperationContractForKey(command.OperationKey)
	if err != nil || contract.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || string(contract.NodeKey) != command.Identity.NodeKey {
		if err == nil {
			err = errors.New("workspace analysis tool operation is not assigned to the current node")
		}
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, foundation.NewError(foundation.ErrorInvalidInput, agentdomain.ErrorCodeWorkspaceAnalysisOperationInvalid, false, err)
	}
	call := command.Call
	call.StartedAt, call.CompletedAt, call.DurationMillis = time.Unix(1, 0).UTC(), nil, 0
	if call.Status != domain.CallStarted || call.Version != 1 || call.Tool == nil || call.IdempotencyKey != "" ||
		call.InvocationPolicy != domain.InvocationTrustedWorkflowOnly || call.SideEffectLevel != domain.SideEffectNone ||
		call.RequestHash == "" || domain.ValidateToolCall(call) != nil {
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("workspace analysis STARTED tool call is invalid"))
	}
	definition, err := exactWorkspaceAnalysisToolDefinition(command.Definition, contract, *call.Tool)
	expectedCallNo := 1
	if contract.Kind == agentdomain.WorkspaceAnalysisOperationSourceRead {
		expectedCallNo = contract.Ordinal
	}
	if err != nil || call.DefinitionHash != definition.DefinitionHash || call.InputSchema == nil || *call.InputSchema != definition.InputSchema ||
		call.OutputSchema == nil || *call.OutputSchema != definition.OutputSchema || call.Capability != definition.RequiredCapability ||
		call.CallNo != expectedCallNo {
		if err == nil {
			err = errors.New("workspace analysis call definition binding differs")
		}
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, err)
	}
	if call.RequestedToolName != definition.Ref.Name {
		return domain.ToolCall{}, domain.Definition{}, agentdomain.WorkspaceAnalysisOperationContract{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("workspace analysis requested tool differs"))
	}
	return call, definition, contract, nil
}

func validateWorkspaceAnalysisToolFinalization(command application.FinalizeWorkspaceAnalysisToolCallCommand) (domain.ToolCall, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, err
	}
	unknown := command.Call.Status == domain.CallUnknown
	if command.Call.Status != domain.CallFailed && !unknown {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("workspace analysis finalizer only accepts failed or unknown calls"))
	}
	return validateTerminalCommand(command.ExpectedVersion, command.Call, unknown)
}

func exactWorkspaceAnalysisToolDefinition(input domain.Definition, contract agentdomain.WorkspaceAnalysisOperationContract, ref domain.ToolRef) (domain.Definition, error) {
	expected, ok := workspaceAnalysisToolForOperation(contract)
	if !ok || expected != ref {
		return domain.Definition{}, errors.New("workspace analysis operation tool is not exact")
	}
	canonical, err := workspaceAnalysisBuiltinDefinition(ref)
	if err != nil {
		return domain.Definition{}, err
	}
	candidate, err := domain.CanonicalizeDefinition(input)
	if err != nil || candidate.DefinitionHash != input.DefinitionHash || candidate.DefinitionHash != canonical.DefinitionHash {
		return domain.Definition{}, errors.New("workspace analysis tool definition drifted")
	}
	return canonical, nil
}

func workspaceAnalysisToolForOperation(contract agentdomain.WorkspaceAnalysisOperationContract) (domain.ToolRef, bool) {
	switch contract.Kind {
	case agentdomain.WorkspaceAnalysisOperationGitStatus:
		return workspaceAnalysisGitStatusRef, contract.Ordinal == 1
	case agentdomain.WorkspaceAnalysisOperationKnowledgeSearch:
		return workspaceAnalysisSearchRef, contract.Ordinal == 1
	case agentdomain.WorkspaceAnalysisOperationSourceRead:
		return workspaceAnalysisSourceRef, contract.Ordinal >= 1 && contract.Ordinal <= 3
	case agentdomain.WorkspaceAnalysisOperationCitationValidation:
		return workspaceAnalysisCitationRef, contract.Ordinal == 1
	default:
		return domain.ToolRef{}, false
	}
}

func sameWorkspaceAnalysisToolRequestBinding(existing, requested domain.ToolCall) bool {
	return existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID &&
		existing.NodeRunID == requested.NodeRunID && existing.CallNo == requested.CallNo &&
		existing.RequestedToolName == requested.RequestedToolName && sameToolRef(existing.Tool, requested.Tool) &&
		existing.DefinitionHash == requested.DefinitionHash && sameSchemaRef(existing.InputSchema, requested.InputSchema) &&
		sameSchemaRef(existing.OutputSchema, requested.OutputSchema) && existing.Capability == requested.Capability &&
		existing.SideEffectLevel == requested.SideEffectLevel && existing.InvocationPolicy == requested.InvocationPolicy &&
		existing.IdempotencyKey == requested.IdempotencyKey && existing.RequestHash == requested.RequestHash &&
		existing.RequestBytes == requested.RequestBytes && jsonEqual(existing.RequestSummary, requested.RequestSummary)
}

func workspaceAnalysisCatalogHash() string {
	snapshot, err := catalog.WorkspaceAnalysisToolCatalogSnapshot()
	if err != nil {
		return ""
	}
	return snapshot.Hash
}

func workspaceAnalysisToolAdmissionError(deadlineExceeded, toolBudgetAvailable, sourceReadBudgetAvailable, concurrencyAvailable bool) error {
	if !toolBudgetAvailable || !sourceReadBudgetAvailable {
		return stale(errors.New("workspace analysis tool budget is exhausted"))
	}
	if !concurrencyAvailable {
		return stale(errors.New("workspace analysis tool concurrency is exhausted"))
	}
	if deadlineExceeded {
		return agentdomain.NewWorkspaceAnalysisPreAuthorizationDeadlineError()
	}
	return nil
}
