package postgres

import (
	"context"
	"encoding/json"
	"errors"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ErrorCodeAnswerFinalizeInvalid     = "CONVERSATION_ANSWER_FINALIZE_INVALID"
	ErrorCodeAnswerFinalizeConflict    = "CONVERSATION_ANSWER_FINALIZE_CONFLICT"
	ErrorCodeAnswerFinalizeCorrupt     = "CONVERSATION_ANSWER_FINALIZE_CORRUPT"
	ErrorCodeAnswerFinalizeUnavailable = "CONVERSATION_ANSWER_FINALIZE_UNAVAILABLE"
)

type builtPublication struct {
	answer   conversationdomain.Answer
	modelRun agentdomain.ModelRun
}

func buildPublication(command conversationapplication.FinalizeAnswerCommand, run agentdomain.ModelRun, pending conversationdomain.Answer, now time.Time) (builtPublication, error) {
	proposal := command.Proposal
	terminalCount := 0
	if proposal.Answer != nil {
		terminalCount++
	}
	if proposal.Refusal != nil {
		terminalCount++
	}
	if proposal.Clarification != nil {
		terminalCount++
	}
	if terminalCount != 1 || proposal.Retrieval == nil || proposal.ModelRunRef != command.ModelRunID || run.ID != command.ModelRunID {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("terminal proposal is incomplete or ambiguous"))
	}
	summary, err := canonicalRetrievalSummary(command.WorkspaceID, *proposal.Retrieval)
	if err != nil {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	var resultType conversationdomain.AnswerResultType
	var status conversationdomain.AnswerPublicationStatus
	var document []byte
	terminal := run
	terminal.FinalErrorCode = ""
	switch {
	case proposal.Answer != nil:
		resultType, status = conversationdomain.AnswerResultRAGAnswer, conversationdomain.AnswerPublicationCompleted
		document, err = json.Marshal(proposal.Answer)
		terminal.Status, terminal.FinalResultType = agentdomain.ModelRunSucceeded, agentdomain.ResultTypeRAGAnswer
	case proposal.Refusal != nil:
		resultType, status = conversationdomain.AnswerResultRefusal, conversationdomain.AnswerPublicationRefused
		document, err = json.Marshal(proposal.Refusal)
		terminal.Status, terminal.FinalResultType = agentdomain.ModelRunRefused, agentdomain.ResultTypeRefusal
		terminal.FinalErrorCode = string(proposal.Refusal.Payload.ReasonCode)
	case proposal.Clarification != nil:
		resultType, status = conversationdomain.AnswerResultClarification, conversationdomain.AnswerPublicationClarificationRequired
		suggestedScopes := make([]string, len(proposal.Clarification.SuggestedScopes))
		copy(suggestedScopes, proposal.Clarification.SuggestedScopes)
		result := conversationdomain.ClarificationResult{ResultType: agentdomain.ResultTypeClarification,
			SchemaID: conversationdomain.ClarificationSchemaID, SchemaVersion: conversationdomain.ClarificationSchemaVersionV1,
			ModelRunRef: command.ModelRunID, Payload: conversationdomain.ClarificationPayload{Reason: proposal.Clarification.Reason,
				Question: proposal.Clarification.Question, SuggestedScopes: suggestedScopes}}
		document, err = json.Marshal(result)
		terminal.Status, terminal.FinalResultType = agentdomain.ModelRunSucceeded, agentdomain.ResultTypeClarification
	}
	if err != nil {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	published, err := conversationdomain.CanonicalizePublishedResult(resultType, document)
	if err != nil || published.ModelRunID != command.ModelRunID {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	if summary.IndexVersionID != nil {
		terminal.Retrieval = agentdomain.RetrievalRef{IndexVersionID: *summary.IndexVersionID, EmbeddingVersionID: summary.EmbeddingVersionID}
	}
	terminal.Version = command.ExpectedModelRunVersion + 1
	terminal.UpdatedAt, terminal.CompletedAt = now, &now
	answer := pending
	answer.ModelRunID = &command.ModelRunID
	answer.PublicationStatus, answer.ResultType = status, resultType
	answer.Result, answer.ResultHash, answer.RetrievalSummary = published.Document, published.Hash, &summary
	answer.Version = command.ExpectedAnswerVersion + 1
	answer.UpdatedAt, answer.PublishedAt = now, &now
	if err := agentdomain.ValidateModelRun(terminal); err != nil {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	if err := conversationdomain.ValidateAnswer(answer); err != nil {
		return builtPublication{}, invalid(ErrorCodeAnswerFinalizeInvalid, err)
	}
	return builtPublication{answer: answer, modelRun: terminal}, nil
}

func canonicalRetrievalSummary(workspaceID foundation.ID, source agentapplication.RAGRetrievalSummary) (conversationdomain.RetrievalSummary, error) {
	if source.Rewrites == nil || source.Degradations == nil {
		return conversationdomain.RetrievalSummary{}, errors.New("retrieval proposal requires explicit rewrite and degradation lists")
	}
	rewrites := make([]string, len(source.Rewrites))
	copy(rewrites, source.Rewrites)
	indexID := source.IndexVersionID
	var index *foundation.ID
	if indexID != "" {
		index = &indexID
	}
	degradations := make([]conversationdomain.RetrievalDegradation, len(source.Degradations))
	for i, degradation := range source.Degradations {
		degradations[i] = conversationdomain.RetrievalDegradation{Capability: degradation.Capability, Code: degradation.Code, Retryable: degradation.Retryable}
	}
	return conversationdomain.CanonicalizeRetrievalSummary(workspaceID, conversationdomain.RetrievalSummary{
		Rewrites: rewrites, RequestedMode: source.RequestedMode, EffectiveMode: source.EffectiveMode,
		Scope: conversationdomain.RetrievalScopeSummary{SourceIDs: source.Filter.SourceIDs, SourceVersionIDs: source.Filter.SourceVersionIDs,
			PathPrefixes: source.Filter.PathPrefixes, CapturedAtFrom: source.Filter.CapturedAtFrom, CapturedAtBefore: source.Filter.CapturedAtBefore,
			AllowOriginalSources: source.AllowOriginalSources, AllowWeb: source.AllowWeb},
		IndexVersionID: index, EmbeddingVersionID: source.EmbeddingVersionID, CandidateCount: source.CandidateCount,
		SelectedCount: source.SelectedCount, ConflictCount: source.ConflictCount, Degradations: degradations,
	})
}

func validatePublicationLookup(ctx context.Context, lookup conversationapplication.AnswerPublicationLookup) error {
	if ctx == nil {
		return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer finalizer context is nil"))
	}
	ids := []foundation.ID{lookup.WorkspaceID, lookup.WorkflowRunID, lookup.NodeRunID, lookup.NodeAttemptID, lookup.ConversationID, lookup.QuestionID, lookup.AnswerID}
	seen := map[foundation.ID]struct{}{}
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer finalizer identity is invalid"))
		}
		if _, exists := seen[id]; exists {
			return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer finalizer identity is reused"))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateFinalizeCommand(ctx context.Context, command conversationapplication.FinalizeAnswerCommand) error {
	if err := validatePublicationLookup(ctx, command.AnswerPublicationLookup); err != nil {
		return err
	}
	parsed, err := foundation.ParseID(string(command.ModelRunID))
	if err != nil || parsed != command.ModelRunID || command.ExpectedAnswerVersion < 1 || command.ExpectedModelRunVersion < 1 || command.Proposal.ModelRunRef != command.ModelRunID {
		return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer finalization command is invalid"))
	}
	for _, id := range []foundation.ID{command.WorkspaceID, command.WorkflowRunID, command.NodeRunID, command.NodeAttemptID, command.ConversationID, command.QuestionID, command.AnswerID} {
		if command.ModelRunID == id {
			return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer finalization identity is reused"))
		}
	}
	if command.Draft == nil {
		return nil
	}
	if _, err := expectedDraftTerminalStatus(command.Proposal); err != nil {
		return err
	}
	draft := *command.Draft
	parsed, err = foundation.ParseID(string(draft.SessionID))
	if err != nil || parsed != draft.SessionID || draft.Generation < 1 || draft.AttemptNo < 1 ||
		draft.LeaseOwner == "" || strings.TrimSpace(draft.LeaseOwner) != draft.LeaseOwner || len(draft.LeaseOwner) > 256 || !utf8.ValidString(draft.LeaseOwner) {
		return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer draft terminal binding is invalid"))
	}
	for _, id := range []foundation.ID{command.WorkspaceID, command.WorkflowRunID, command.NodeRunID, command.NodeAttemptID, command.ConversationID, command.QuestionID, command.AnswerID, command.ModelRunID} {
		if draft.SessionID == id {
			return invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer draft terminal identity is reused"))
		}
	}
	return nil
}

func expectedDraftTerminalStatus(proposal agentapplication.RAGTerminalProposal) (agentapplication.DraftStreamStatus, error) {
	terminalCount := 0
	if proposal.Answer != nil {
		terminalCount++
	}
	if proposal.Refusal != nil {
		terminalCount++
	}
	if proposal.Clarification != nil {
		terminalCount++
	}
	if terminalCount != 1 {
		return "", invalid(ErrorCodeAnswerFinalizeInvalid, errors.New("answer draft terminal proposal is incomplete or ambiguous"))
	}
	if proposal.Answer != nil {
		return agentapplication.DraftStreamPublished, nil
	}
	return agentapplication.DraftStreamAborted, nil
}

func validateTerminalBinding(run agentdomain.ModelRun, lookup conversationapplication.AnswerPublicationLookup, answer conversationdomain.Answer) error {
	if run.ID != *answer.ModelRunID || run.WorkspaceID != lookup.WorkspaceID || run.WorkflowRunID != lookup.WorkflowRunID || run.NodeRunID != lookup.NodeRunID || run.NodeAttemptID != lookup.NodeAttemptID || run.Status == agentdomain.ModelRunRunning {
		return consistency(ErrorCodeAnswerFinalizeCorrupt, errors.New("published answer and model run are inconsistent"))
	}
	return nil
}

func receiptFromAnswer(answer conversationdomain.Answer) (conversationworkflow.OutputReceipt, error) {
	if err := conversationdomain.ValidateAnswer(answer); err != nil || answer.ModelRunID == nil {
		return conversationworkflow.OutputReceipt{}, consistency(ErrorCodeAnswerFinalizeCorrupt, err)
	}
	receipt := conversationworkflow.OutputReceipt{SchemaVersion: conversationworkflow.OutputSchemaVersion, AnswerID: answer.ID,
		PublicationStatus: conversationworkflow.PublicationStatus(answer.PublicationStatus), ResultType: conversationworkflow.ResultType(answer.ResultType), ModelRunID: *answer.ModelRunID, ResultHash: answer.ResultHash}
	if _, err := conversationworkflow.EncodeOutputReceipt(receipt); err != nil {
		return conversationworkflow.OutputReceipt{}, consistency(ErrorCodeAnswerFinalizeCorrupt, err)
	}
	return receipt, nil
}

func samePublishedProposal(left, right conversationdomain.Answer) bool {
	return left.ID == right.ID && left.ModelRunID != nil && right.ModelRunID != nil && *left.ModelRunID == *right.ModelRunID && left.PublicationStatus == right.PublicationStatus && left.ResultType == right.ResultType && left.ResultHash == right.ResultHash && reflect.DeepEqual(left.RetrievalSummary, right.RetrievalSummary)
}

func sameTerminalRun(left, right agentdomain.ModelRun) bool {
	return left.ID == right.ID && left.Status == right.Status && left.FinalResultType == right.FinalResultType && left.FinalErrorCode == right.FinalErrorCode && reflect.DeepEqual(left.Retrieval, right.Retrieval)
}
