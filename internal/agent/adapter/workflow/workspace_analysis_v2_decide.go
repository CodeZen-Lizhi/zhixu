package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func (executor *workspaceAnalysisV2NodeExecutor) decideNext(ctx context.Context, state workspaceAnalysisV2Execution) (workflowapplication.ExecutionResult, error) {
	schema := agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisDecisionSchemaID, Version: agentdomain.WorkspaceAnalysisDecisionSchemaVersion}
	snapshot, err := executor.dependencies.Catalog.Snapshot(WorkspaceAnalysisDecisionPromptRef(), schema, schema, DefaultProfileRef())
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	modelCalls, err := executor.dependencies.Decisions.BindWorkspaceAnalysisLoop(agentapplication.WorkspaceAnalysisDecisionRunRequest{
		Identity: workspaceAnalysisModelIdentity(state.execution), AnalysisRunID: state.run.ID,
		ModelSettingsRevision: cloneWorkspaceAnalysisInt64(state.execution.ModelSettingsRevision),
		ProfileRef:            DefaultProfileRef(), PromptRef: WorkspaceAnalysisDecisionPromptRef(),
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	input, err := json.Marshal(workspaceAnalysisV2Question(state.question))
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	tools, err := workspaceAnalysisLoopToolsV2()
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	loop, err := executor.dependencies.Runtime.RunWorkspaceAnalysisLoop(ctx, agentapplication.WorkspaceAnalysisLoopRequest{
		Messages: []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: snapshot.Prompt.System + "\n" + snapshot.Prompt.InitialInstruction},
			{Role: agentapplication.AgentMessageUser, Content: string(input)}},
		Tools: tools, ModelCalls: modelCalls, ToolInvoker: &workspaceAnalysisV2LoopToolInvoker{executor: executor, state: state},
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	journal, err := executor.dependencies.Journal.LoadWorkspaceAnalysisJournal(ctx, state.journalQuery())
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisV2Journal(state, journal); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	finish, toolCount, err := workspaceAnalysisV2Finish(journal)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if loop.Finish.Decision == nil || !sameWorkspaceAnalysisV2DecisionReceipt(finish, *loop.Finish.Decision) || loop.Decisions != finish.Ordinal || loop.ToolCalls != toolCount {
		return workflowapplication.ExecutionResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis loop result differs from durable finish"))
	}
	output, err := json.Marshal(workspaceAnalysisLoopOutputV2{SchemaVersion: 2, FinishDecisionID: finish.ID, FinishDecisionHash: finish.DocumentHash, DecisionCount: finish.Ordinal, ToolCount: toolCount})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: output}, nil
}

type workspaceAnalysisV2QuestionInput struct {
	SchemaVersion int                   `json:"schema_version"`
	UntrustedData bool                  `json:"untrusted_data"`
	Question      string                `json:"question"`
	History       []ragModelHistoryTurn `json:"history"`
	AnswerDepth   string                `json:"answer_depth"`
	OutputFormat  string                `json:"output_format"`
}

func workspaceAnalysisV2Question(value conversationapplication.QuestionExecutionContext) workspaceAnalysisV2QuestionInput {
	history := make([]ragModelHistoryTurn, len(value.History))
	for index, turn := range value.History {
		history[index] = ragModelHistoryTurn{Ordinal: turn.Ordinal, QuestionText: turn.QuestionText, AssistantText: turn.AssistantText, ResultType: turn.ResultType}
	}
	return workspaceAnalysisV2QuestionInput{SchemaVersion: 2, UntrustedData: true, Question: value.Question.Request.QuestionText, History: history,
		AnswerDepth: string(value.Question.Request.AnswerDepth), OutputFormat: string(value.Question.Request.OutputFormat)}
}

func workspaceAnalysisLoopToolsV2() ([]agentapplication.AgentToolSpec, error) {
	ref := workspaceAnalysisEvidenceReferenceSchemaV2()
	definitions := []struct {
		name        string
		version     int64
		description string
		schema      map[string]any
	}{
		{"ReadGitStatus", 3, "Read the authorized workspace's safe Git status aggregate.", strictObject([]string{}, map[string]any{})},
		{"SearchKnowledge", 3, "Search approved knowledge with a concise query; returns permanent run-global evidence references.", strictObject([]string{"query"}, map[string]any{"query": stringSchema(1, 1024)})},
		{"ReadSource", 4, "Read one previously returned evidence reference from its immutable approved source.", strictObject([]string{"evidence_ref"}, map[string]any{"evidence_ref": ref})},
		{"ValidateCitation", 4, "Check one through eight previously read evidence references. This does not publish an answer.", strictObject([]string{"evidence_refs"}, map[string]any{"evidence_refs": map[string]any{"type": "array", "minItems": 1, "maxItems": agentdomain.WorkspaceAnalysisV2MaxSourceReads, "uniqueItems": true, "items": ref}})},
	}
	result := make([]agentapplication.AgentToolSpec, len(definitions))
	for index, definition := range definitions {
		schema, err := json.Marshal(definition.schema)
		if err != nil {
			return nil, err
		}
		result[index] = agentapplication.AgentToolSpec{Ref: toolsdomain.ToolRef{Name: definition.name, Version: definition.version}, Name: definition.name, Description: definition.description, InputSchema: schema}
	}
	return result, nil
}

type workspaceAnalysisV2LoopToolInvoker struct {
	executor *workspaceAnalysisV2NodeExecutor
	state    workspaceAnalysisV2Execution
}

func (invoker *workspaceAnalysisV2LoopToolInvoker) InvokeWorkspaceAnalysisDecisionTool(ctx context.Context, accepted agentapplication.WorkspaceAnalysisDecisionMutationResult) (agentapplication.AgentToolResult, error) {
	if accepted.Decision == nil || accepted.Decision.Validate() != nil {
		return agentapplication.AgentToolResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis tool has no accepted decision"))
	}
	journal, err := invoker.executor.dependencies.Journal.LoadWorkspaceAnalysisJournal(ctx, invoker.state.journalQuery())
	if err != nil {
		return agentapplication.AgentToolResult{}, err
	}
	if err := validateWorkspaceAnalysisV2Journal(invoker.state, journal); err != nil {
		return agentapplication.AgentToolResult{}, err
	}
	var decision *agentdomain.WorkspaceAnalysisDecisionReceipt
	for _, entry := range journal.Entries {
		if entry.Operation.ID == accepted.Operation.ID && entry.Operation.Status == agentdomain.WorkspaceAnalysisOperationSucceeded &&
			entry.Decision != nil && sameWorkspaceAnalysisV2DecisionReceipt(*entry.Decision, *accepted.Decision) {
			decision = entry.Decision
			break
		}
	}
	if decision == nil || decision.AnalysisRunID != invoker.state.run.ID || decision.WorkspaceID != invoker.state.execution.WorkspaceID ||
		decision.Decision.Action == agentdomain.WorkspaceAnalysisDecisionFinish {
		return agentapplication.AgentToolResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis tool decision is not durable"))
	}
	key := agentdomain.WorkspaceAnalysisOperationKey{AnalysisRunID: invoker.state.run.ID, NodeKey: agentdomain.WorkspaceAnalysisOperationNodeDecideNext,
		Kind: workspaceAnalysisV2DecisionToolKind(decision.Decision.Action), Ordinal: decision.Ordinal}
	var arguments any
	var ref toolsdomain.ToolRef
	switch decision.Decision.Action {
	case agentdomain.WorkspaceAnalysisDecisionGitStatus:
		ref, arguments = toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 3}, struct{}{}
	case agentdomain.WorkspaceAnalysisDecisionKnowledgeSearch:
		limit, limitErr := invoker.executor.dependencies.ToolOutputs.WorkspaceAnalysisDynamicSearchLimit(ctx, workspaceAnalysisV2ToolQuery(invoker.state, key))
		if limitErr != nil {
			return agentapplication.AgentToolResult{}, limitErr
		}
		if limit < 0 || limit > 5 {
			return agentapplication.AgentToolResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis search capacity is invalid"))
		}
		// A full alias namespace still passes through durable authorization with
		// a legal request. Its committed PENDING denial forbids external Search.
		limit = max(1, limit)
		ref = toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 3}
		arguments = map[string]any{"query": *decision.Decision.Query, "mode": invoker.state.question.Question.Request.Scope.RetrievalMode, "limit": limit}
	case agentdomain.WorkspaceAnalysisDecisionSourceRead:
		ref, arguments = toolsdomain.ToolRef{Name: "ReadSource", Version: 4}, map[string]any{"evidence_ref": *decision.Decision.EvidenceRef}
	case agentdomain.WorkspaceAnalysisDecisionCitationValidation:
		ref, arguments = toolsdomain.ToolRef{Name: "ValidateCitation", Version: 4}, map[string]any{"candidate_id": nil, "candidate_hash": nil, "evidence_refs": decision.Decision.EvidenceRefs}
	default:
		return agentapplication.AgentToolResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis decision tool is unsupported"))
	}
	result, err := invoker.executor.executeTool(ctx, invoker.state, key, ref, arguments)
	if err != nil {
		return agentapplication.AgentToolResult{}, err
	}
	output, err := invoker.executor.dependencies.ToolOutputs.LoadWorkspaceAnalysisDynamicToolOutput(ctx, workspaceAnalysisV2ToolQuery(invoker.state, key))
	if err != nil {
		return agentapplication.AgentToolResult{}, err
	}
	if result.ResultReceiptID == "" || len(output) == 0 {
		return agentapplication.AgentToolResult{}, workspaceAnalysisReceiptError(errors.New("workspace analysis tool has no verified output"))
	}
	return agentapplication.AgentToolResult{Output: output}, nil
}

func sameWorkspaceAnalysisV2DecisionReceipt(left, right agentdomain.WorkspaceAnalysisDecisionReceipt) bool {
	// PostgreSQL can return the same instant with a different time.Location.
	// Preserve every immutable binding while comparing CreatedAt by instant.
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID && left.AnalysisRunID == right.AnalysisRunID &&
		left.OperationID == right.OperationID && left.NodeAttemptID == right.NodeAttemptID &&
		left.ModelRunID == right.ModelRunID && left.ModelCallID == right.ModelCallID && left.Ordinal == right.Ordinal &&
		left.DocumentHash == right.DocumentHash && left.DocumentBytes == right.DocumentBytes &&
		left.CreatedAt.Equal(right.CreatedAt) && reflect.DeepEqual(left.Decision, right.Decision)
}

func workspaceAnalysisV2ToolQuery(state workspaceAnalysisV2Execution, key agentdomain.WorkspaceAnalysisOperationKey) toolsapplication.WorkspaceAnalysisDynamicToolQuery {
	return toolsapplication.WorkspaceAnalysisDynamicToolQuery{WorkspaceID: state.execution.WorkspaceID, WorkflowRunID: state.execution.RunID, AnalysisRunID: state.run.ID, OperationKey: key}
}

func (executor *workspaceAnalysisV2NodeExecutor) executeTool(ctx context.Context, state workspaceAnalysisV2Execution, key agentdomain.WorkspaceAnalysisOperationKey, ref toolsdomain.ToolRef, arguments any) (toolsapplication.ToolExecutionResult, error) {
	raw, err := json.Marshal(arguments)
	if err != nil {
		return toolsapplication.ToolExecutionResult{}, err
	}
	result, err := executor.dependencies.Tools.ExecuteWorkspaceAnalysisTool(ctx, toolsapplication.ExecuteWorkspaceAnalysisToolCommand{OperationKey: key,
		Tool: toolsapplication.ExecuteToolCommand{Identity: workspaceAnalysisToolIdentity(state.execution), Invocation: toolsdomain.InvocationSourceTrustedWorkflow, CallNo: key.Ordinal,
			Request: toolsdomain.ToolRequestV1{SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1, ToolName: ref.Name, Arguments: raw, Reason: "collect and validate approved workspace evidence"}},
	})
	if err != nil {
		return result, err
	}
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(ref)
	call := result.Call
	if !found || toolsdomain.ValidateToolCall(call) != nil || call.Status != toolsdomain.CallSucceeded || call.WorkspaceID != state.execution.WorkspaceID ||
		call.WorkflowRunID != state.execution.RunID || call.NodeRunID != state.execution.NodeRunID || (!result.Replayed && call.NodeAttemptID != state.execution.NodeAttemptID) ||
		call.CallNo != key.Ordinal || call.Tool == nil || *call.Tool != ref || call.OutputSchema == nil || *call.OutputSchema != contract.OutputSchema ||
		call.SideEffectLevel != toolsdomain.SideEffectNone || call.InvocationPolicy != toolsdomain.InvocationTrustedWorkflowOnly || !result.UntrustedData ||
		!validExecutionID(result.ResultReceiptID) || result.ResultReceiptID == call.ID || call.ResponseHash != workspaceAnalysisV2Hash(result.Output) || call.ResponseBytes != int64(len(result.Output)) {
		return result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 tool result binding drifted"))
	}
	journal, err := executor.dependencies.Journal.LoadWorkspaceAnalysisJournal(ctx, state.journalQuery())
	if err != nil {
		return result, err
	}
	if err := validateWorkspaceAnalysisV2Journal(state, journal); err != nil {
		return result, err
	}
	for _, entry := range journal.Entries {
		op := entry.Operation
		if op.LogicalKey() == key && op.Status == agentdomain.WorkspaceAnalysisOperationSucceeded && op.Call != nil && op.Result != nil &&
			op.Call.ID == call.ID && op.Result.ID == result.ResultReceiptID && op.Result.Hash == call.ResponseHash {
			result.OperationID = op.ID
			return result, nil
		}
	}
	return result, workspaceAnalysisReceiptError(errors.New("workspace analysis v2 tool result has no journal closure"))
}

var _ agentapplication.WorkspaceAnalysisLoopToolInvoker = (*workspaceAnalysisV2LoopToolInvoker)(nil)
