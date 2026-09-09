package postgres

import (
	"encoding/json"
	"errors"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"reflect"
)

const (
	questionDispatchNo               = 1
	answerActiveWorkflowConstraint   = "agent_answer_active_workflow"
	answerConversationOpenConstraint = "agent_answer_conversation_open"
)

func validateQuestionDispatchRecord(record conversationapplication.SubmitQuestionRecord) (conversationdomain.QuestionRequest, error) {
	if err := record.APIVersion.Validate(); err != nil {
		return conversationdomain.QuestionRequest{}, err
	}
	if !canonicalIdempotencyKey(record.IdempotencyKey) {
		return conversationdomain.QuestionRequest{}, invalid(ErrorCodeQuestionDispatchInvalid, errors.New("question idempotency key is invalid"))
	}
	request, err := conversationdomain.CanonicalizeQuestionRequest(record.Request)
	if err != nil || !reflect.DeepEqual(request, record.Request) {
		return conversationdomain.QuestionRequest{}, invalid(ErrorCodeQuestionDispatchInvalid, err)
	}
	hash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil || hash != record.RequestHash {
		return conversationdomain.QuestionRequest{}, invalid(ErrorCodeQuestionDispatchInvalid, errors.New("question request hash is inconsistent"))
	}
	return request, nil
}

type questionWorkflowDispatchPlan struct {
	definition     workflowdomain.RegisteredDefinition
	input          json.RawMessage
	idempotencyKey string
	root           workflowdomain.NodeDefinition
}

type questionWorkflowInputBinding struct {
	ConversationID  foundation.ID
	QuestionID      foundation.ID
	AnswerID        foundation.ID
	QuestionOrdinal int64
	ContextHash     string
}

func buildQuestionWorkflowDispatchPlan(question conversationdomain.Question, answerID foundation.ID) (questionWorkflowDispatchPlan, error) {
	return buildQuestionWorkflowDispatchPlanForVersion(question, answerID, conversationworkflow.WorkspaceAnalysisDefinitionVersion)
}

// A new submission uses the version selected by trusted composition. Replays
// supply the immutable version read from the already bound Workflow definition.
func buildQuestionWorkflowDispatchPlanForVersion(question conversationdomain.Question, answerID foundation.ID, analysisVersion int64) (questionWorkflowDispatchPlan, error) {
	var definition workflowdomain.RegisteredDefinition
	var input json.RawMessage
	var err error
	switch question.Request.Mode {
	case conversationdomain.QuestionModeRAG:
		definition = conversationworkflow.RegisteredDefinitionV2()
		input, err = conversationworkflow.EncodeInput(conversationworkflow.Input{
			SchemaVersion: conversationworkflow.InputSchemaVersion, ConversationID: question.Request.ConversationID,
			QuestionID: question.ID, AnswerID: answerID, QuestionOrdinal: question.Ordinal, ContextHash: question.ContextHash,
		})
	case conversationdomain.QuestionModeWorkspaceAnalysis:
		switch analysisVersion {
		case 1:
			definition = conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
		case 2:
			definition = conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2()
		default:
			return questionWorkflowDispatchPlan{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("workspace analysis workflow version is unsupported"))
		}
		input, err = conversationworkflow.EncodeWorkspaceAnalysisInput(conversationworkflow.WorkspaceAnalysisInput{
			SchemaVersion: conversationworkflow.WorkspaceAnalysisInputSchemaVersion, ConversationID: question.Request.ConversationID,
			QuestionID: question.ID, AnswerID: answerID, QuestionOrdinal: question.Ordinal, ContextHash: question.ContextHash,
		})
	default:
		return questionWorkflowDispatchPlan{}, invalid(ErrorCodeQuestionDispatchInvalid, errors.New("question workflow mode is unsupported"))
	}
	if err != nil {
		return questionWorkflowDispatchPlan{}, err
	}
	root, err := uniqueQuestionWorkflowRoot(definition)
	if err != nil {
		return questionWorkflowDispatchPlan{}, err
	}
	return questionWorkflowDispatchPlan{
		definition: definition, input: input,
		idempotencyKey: questionWorkflowIdempotencyKeyForMode(question.Request.Mode, question.ID), root: root,
	}, nil
}

func buildReplayedQuestionWorkflowDispatchPlan(question conversationdomain.Question, answerID foundation.ID, key string, version int64, graphJSON json.RawMessage) (questionWorkflowDispatchPlan, error) {
	plan, err := buildQuestionWorkflowDispatchPlanForVersion(question, answerID, version)
	if err != nil {
		return questionWorkflowDispatchPlan{}, err
	}
	if question.Request.Mode == conversationdomain.QuestionModeRAG && version == 1 {
		plan.definition = conversationworkflow.RegisteredDefinitionV1()
		plan.root, err = uniqueQuestionWorkflowRoot(plan.definition)
		if err != nil {
			return questionWorkflowDispatchPlan{}, err
		}
	}
	graph, err := workflowapplication.DecodeCanonicalGraph(graphJSON)
	if err != nil {
		return questionWorkflowDispatchPlan{}, consistency(ErrorCodeQuestionDispatchCorrupt, err)
	}
	hash, err := workflowapplication.ComputeCanonicalGraphHash(graph)
	if err != nil || key != plan.definition.Key || version != plan.definition.Version || hash != plan.definition.GraphHash {
		return questionWorkflowDispatchPlan{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("replayed question workflow definition differs from its immutable contract"))
	}
	return plan, nil
}

func uniqueQuestionWorkflowRoot(definition workflowdomain.RegisteredDefinition) (workflowdomain.NodeDefinition, error) {
	var root workflowdomain.NodeDefinition
	count := 0
	for _, node := range definition.Graph.Nodes {
		if len(node.Dependencies) == 0 {
			root = node
			count++
		}
	}
	if count != 1 {
		return workflowdomain.NodeDefinition{}, consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("question workflow definition does not have exactly one root"))
	}
	return root, nil
}

func validateQuestionRuntime(result workflowapplication.RuntimeStartResult, request workflowapplication.RuntimeStartRequest, plan questionWorkflowDispatchPlan, question conversationdomain.Question) error {
	if result.Run.WorkspaceID != question.Request.WorkspaceID || result.Run.ID == "" || result.Run.DefinitionID == "" ||
		result.Run.IdempotencyKey != request.Run.IdempotencyKey || result.Run.RequestHash != request.RequestHash ||
		result.FirstNode.ID == "" || result.Job.JobID < 1 || result.Replayed != result.Job.Duplicate ||
		result.FirstNode.RunID != result.Run.ID || result.FirstNode.NodeKey != plan.root.Key ||
		result.FirstNode.NodeType != plan.root.Kind || result.FirstNode.InputSchemaVersion != plan.root.InputSchemaVersion ||
		result.FirstNode.OutputSchemaVersion != plan.root.OutputSchemaVersion || result.FirstNode.DispatchNo != questionDispatchNo ||
		result.FirstNode.IdempotencyKey != request.FirstNode.IdempotencyKey || !validWorkflowRunStatus(result.Run.Status) ||
		!validQuestionNodeStatus(result.FirstNode.Status) || result.Run.Version < 1 || result.FirstNode.Version < 1 ||
		result.Run.CreatedAt.IsZero() || result.Run.UpdatedAt.Before(result.Run.CreatedAt) || result.FirstNode.CreatedAt.IsZero() ||
		result.FirstNode.UpdatedAt.Before(result.FirstNode.CreatedAt) ||
		(!result.Replayed && (result.Run.Status != workflowdomain.RunStatusPending || result.FirstNode.Status != workflowdomain.NodeStatusPending)) {
		return consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("RAG workflow start result binding is invalid"))
	}
	runInput, err := decodeQuestionWorkflowInput(question.Request.Mode, result.Run.Input)
	if err != nil {
		return consistency(ErrorCodeQuestionDispatchCorrupt, err)
	}
	nodeInput, err := decodeQuestionWorkflowInput(question.Request.Mode, result.FirstNode.Input)
	if err != nil || runInput != nodeInput {
		return consistency(ErrorCodeQuestionDispatchCorrupt, err)
	}
	expected, err := decodeQuestionWorkflowInput(question.Request.Mode, plan.input)
	if err != nil || expected != runInput {
		return consistency(ErrorCodeQuestionDispatchCorrupt, err)
	}
	identities := []foundation.ID{
		question.Request.WorkspaceID, runInput.ConversationID, runInput.QuestionID, runInput.AnswerID,
		result.Run.DefinitionID, result.Run.ID, result.FirstNode.ID,
	}
	seen := make(map[foundation.ID]struct{}, len(identities))
	for _, identity := range identities {
		parsed, parseErr := foundation.ParseID(string(identity))
		if parseErr != nil || parsed != identity {
			return consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("RAG workflow start identity is invalid"))
		}
		if _, duplicate := seen[identity]; duplicate {
			return consistency(ErrorCodeQuestionDispatchCorrupt, errors.New("RAG workflow start identity is reused"))
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func decodeQuestionWorkflowInput(mode conversationdomain.QuestionMode, raw json.RawMessage) (questionWorkflowInputBinding, error) {
	switch mode {
	case conversationdomain.QuestionModeRAG:
		input, err := conversationworkflow.DecodeInput(raw)
		if err != nil {
			return questionWorkflowInputBinding{}, err
		}
		return questionWorkflowInputBinding{
			ConversationID: input.ConversationID, QuestionID: input.QuestionID, AnswerID: input.AnswerID,
			QuestionOrdinal: input.QuestionOrdinal, ContextHash: input.ContextHash,
		}, nil
	case conversationdomain.QuestionModeWorkspaceAnalysis:
		input, err := conversationworkflow.DecodeWorkspaceAnalysisInput(raw)
		if err != nil {
			return questionWorkflowInputBinding{}, err
		}
		return questionWorkflowInputBinding{
			ConversationID: input.ConversationID, QuestionID: input.QuestionID, AnswerID: input.AnswerID,
			QuestionOrdinal: input.QuestionOrdinal, ContextHash: input.ContextHash,
		}, nil
	default:
		return questionWorkflowInputBinding{}, invalid(ErrorCodeQuestionDispatchInvalid, errors.New("question workflow mode is unsupported"))
	}
}

func validQuestionNodeStatus(status workflowdomain.NodeStatus) bool {
	switch status {
	case workflowdomain.NodeStatusPending, workflowdomain.NodeStatusRunning, workflowdomain.NodeStatusWaitingForHuman,
		workflowdomain.NodeStatusRetryWait, workflowdomain.NodeStatusPaused, workflowdomain.NodeStatusSucceeded,
		workflowdomain.NodeStatusFailed, workflowdomain.NodeStatusCancelled:
		return true
	default:
		return false
	}
}

func classifyQuestionAnswerInsertFailure(cause error) error {
	switch platformpostgres.ConstraintName(cause) {
	case answerActiveWorkflowConstraint:
		return conflict(ErrorCodeQuestionActiveWorkflow, cause)
	case answerConversationOpenConstraint:
		return conflict(ErrorCodeQuestionConversationArchived, cause)
	}
	return classify(cause, ErrorCodeQuestionDispatchUnavailable)
}

func submitQuestionResult(question conversationdomain.Question, answerView conversationapplication.AnswerView, runtime workflowapplication.RuntimeStartResult, replayed bool) conversationapplication.SubmitQuestionResult {
	return conversationapplication.SubmitQuestionResult{
		Question: question, Answer: answerView.Answer,
		Workflow: conversationapplication.WorkflowRunView{
			RunID: runtime.Run.ID, Status: runtime.Run.Status, Version: runtime.Run.Version, UpdatedAt: runtime.Run.UpdatedAt,
			DefinitionKey: answerView.Workflow.DefinitionKey, DefinitionVersion: answerView.Workflow.DefinitionVersion,
		},
		NodeRunID: runtime.FirstNode.ID, JobID: runtime.Job.JobID, Replayed: replayed,
	}
}

func questionWorkflowIdempotencyKey(questionID foundation.ID) string {
	return "rag-question:" + string(questionID)
}

func questionWorkflowIdempotencyKeyForMode(mode conversationdomain.QuestionMode, questionID foundation.ID) string {
	if mode == conversationdomain.QuestionModeWorkspaceAnalysis {
		return "workspace-analysis-question:" + string(questionID)
	}
	return questionWorkflowIdempotencyKey(questionID)
}

func workspaceAnalysisCapabilityUnavailable() error {
	return foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		conversationdomain.WorkspaceAnalysisCapabilityUnavailableCode,
		false,
		errors.New("workspace analysis dispatch capability is unavailable"),
	)
}

func sameQuestionDispatchBinding(left, right conversationdomain.Question) bool {
	return left.ID == right.ID && left.Request.WorkspaceID == right.Request.WorkspaceID &&
		left.Request.ConversationID == right.Request.ConversationID && left.RequestHash == right.RequestHash &&
		left.Ordinal == right.Ordinal && left.ContextThroughOrdinal == right.ContextThroughOrdinal &&
		left.ContextHash == right.ContextHash && left.CreatedAt.Equal(right.CreatedAt)
}

func samePendingAnswerDispatchBinding(left, right conversationdomain.Answer) bool {
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID && left.ConversationID == right.ConversationID &&
		left.QuestionID == right.QuestionID && left.WorkflowRunID == right.WorkflowRunID && left.ModelRunID == nil &&
		left.PublicationStatus == right.PublicationStatus && left.ResultType == "" && len(left.Result) == 0 &&
		left.ResultHash == "" && left.RetrievalSummary == nil && left.Version == right.Version &&
		left.CreatedAt.Equal(right.CreatedAt) && left.UpdatedAt.Equal(right.UpdatedAt) && left.PublishedAt == nil
}
