package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestBuildPublicationCanonicalizesAllTerminalKinds(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	lookup := finalizerTestLookup()
	run := finalizerTestRun(lookup, now.Add(-time.Minute))
	pending := conversationdomain.Answer{ID: lookup.AnswerID, WorkspaceID: lookup.WorkspaceID, ConversationID: lookup.ConversationID,
		QuestionID: lookup.QuestionID, WorkflowRunID: lookup.WorkflowRunID, PublicationStatus: conversationdomain.AnswerPublicationPending,
		Version: 1, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}
	tests := []struct {
		name      string
		proposal  agentapplication.RAGTerminalProposal
		status    conversationdomain.AnswerPublicationStatus
		runStatus agentdomain.ModelRunStatus
	}{
		{name: "completed", proposal: finalizerCompletedProposal(run.ID, lookup.WorkspaceID), status: conversationdomain.AnswerPublicationCompleted, runStatus: agentdomain.ModelRunSucceeded},
		{name: "refusal", proposal: finalizerRefusalProposal(run.ID), status: conversationdomain.AnswerPublicationRefused, runStatus: agentdomain.ModelRunRefused},
		{name: "clarification", proposal: finalizerClarificationProposal(run.ID), status: conversationdomain.AnswerPublicationClarificationRequired, runStatus: agentdomain.ModelRunSucceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			built, err := buildPublication(conversationapplication.FinalizeAnswerCommand{AnswerPublicationLookup: lookup, ModelRunID: run.ID,
				ExpectedAnswerVersion: 1, ExpectedModelRunVersion: 1, Proposal: test.proposal}, run, pending, now)
			if err != nil || built.answer.PublicationStatus != test.status || built.modelRun.Status != test.runStatus || built.answer.ResultHash == "" {
				t.Fatalf("buildPublication=%#v err=%v", built, err)
			}
		})
	}
}

func TestValidateFinalizeCommandKeepsDraftTerminalBindingStrictAndOptional(t *testing.T) {
	lookup := finalizerTestLookup()
	run := finalizerTestRun(lookup, time.Now().UTC())
	command := conversationapplication.FinalizeAnswerCommand{
		AnswerPublicationLookup: lookup,
		ModelRunID:              run.ID,
		ExpectedAnswerVersion:   1,
		ExpectedModelRunVersion: 1,
		Proposal:                finalizerCompletedProposal(run.ID, lookup.WorkspaceID),
	}
	if err := validateFinalizeCommand(context.Background(), command); err != nil {
		t.Fatalf("legacy command without draft = %v", err)
	}
	validDraft := conversationapplication.AnswerDraftTerminalBinding{
		SessionID:  foundation.ID("73000000-0000-4000-8000-000000000009"),
		Generation: 1,
		AttemptNo:  1,
		LeaseOwner: "worker-a",
	}
	command.Draft = &validDraft
	if err := validateFinalizeCommand(context.Background(), command); err != nil {
		t.Fatalf("valid draft command = %v", err)
	}
	for _, proposal := range []agentapplication.RAGTerminalProposal{
		finalizerRefusalProposal(run.ID),
		finalizerClarificationProposal(run.ID),
	} {
		candidate := command
		candidate.Proposal = proposal
		if err := validateFinalizeCommand(context.Background(), candidate); err != nil {
			t.Fatalf("valid terminal draft command = %v", err)
		}
	}

	tests := []struct {
		name   string
		mutate func(*conversationapplication.FinalizeAnswerCommand)
	}{
		{name: "invalid session", mutate: func(candidate *conversationapplication.FinalizeAnswerCommand) { candidate.Draft.SessionID = "invalid" }},
		{name: "reused session identity", mutate: func(candidate *conversationapplication.FinalizeAnswerCommand) {
			candidate.Draft.SessionID = candidate.AnswerID
		}},
		{name: "zero generation", mutate: func(candidate *conversationapplication.FinalizeAnswerCommand) { candidate.Draft.Generation = 0 }},
		{name: "zero attempt", mutate: func(candidate *conversationapplication.FinalizeAnswerCommand) { candidate.Draft.AttemptNo = 0 }},
		{name: "padded owner", mutate: func(candidate *conversationapplication.FinalizeAnswerCommand) {
			candidate.Draft.LeaseOwner = " worker-a"
		}},
		{name: "invalid owner encoding", mutate: func(candidate *conversationapplication.FinalizeAnswerCommand) {
			candidate.Draft.LeaseOwner = string([]byte{0xff})
		}},
		{name: "missing terminal proposal", mutate: func(candidate *conversationapplication.FinalizeAnswerCommand) {
			candidate.Proposal = agentapplication.RAGTerminalProposal{ModelRunRef: candidate.ModelRunID}
		}},
		{name: "ambiguous terminal proposal", mutate: func(candidate *conversationapplication.FinalizeAnswerCommand) {
			candidate.Proposal.Refusal = finalizerRefusalProposal(candidate.ModelRunID).Refusal
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := command
			draft := *command.Draft
			candidate.Draft = &draft
			test.mutate(&candidate)
			err := validateFinalizeCommand(context.Background(), candidate)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != ErrorCodeAnswerFinalizeInvalid {
				t.Fatalf("error = %#v, want %s", err, ErrorCodeAnswerFinalizeInvalid)
			}
		})
	}
}

func TestExpectedDraftTerminalStatusFollowsProposalKind(t *testing.T) {
	lookup := finalizerTestLookup()
	run := finalizerTestRun(lookup, time.Now().UTC())
	tests := []struct {
		name     string
		proposal agentapplication.RAGTerminalProposal
		want     agentapplication.DraftStreamStatus
	}{
		{name: "answer", proposal: finalizerCompletedProposal(run.ID, lookup.WorkspaceID), want: agentapplication.DraftStreamPublished},
		{name: "refusal", proposal: finalizerRefusalProposal(run.ID), want: agentapplication.DraftStreamAborted},
		{name: "clarification", proposal: finalizerClarificationProposal(run.ID), want: agentapplication.DraftStreamAborted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := expectedDraftTerminalStatus(test.proposal)
			if err != nil || got != test.want {
				t.Fatalf("expectedDraftTerminalStatus=%s err=%v, want %s", got, err, test.want)
			}
		})
	}
}

func finalizerCompletedProposal(runID, workspaceID foundation.ID) agentapplication.RAGTerminalProposal {
	indexID := foundation.ID("73000000-0000-4000-8000-000000000020")
	citation := agentdomain.Citation{ID: "cite-1", WorkspaceID: workspaceID, IndexVersionID: indexID,
		ChunkID: foundation.ID("73000000-0000-4000-8000-000000000021"), SourceVersionID: foundation.ID("73000000-0000-4000-8000-000000000022"),
		SourceSpanID: foundation.ID("73000000-0000-4000-8000-000000000023")}
	answer := agentdomain.RAGAnswerResultV2{ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV2, ModelRunRef: runID, Payload: agentdomain.RAGAnswerPayloadV2{
			RAGAnswerPayload: agentdomain.RAGAnswerPayload{Conclusion: "Approved evidence supports the result.",
				Assertions: []agentdomain.Assertion{{ID: "assertion-1", Text: "The result is supported.", Kind: agentdomain.AssertionFactual, CitationIDs: []string{"cite-1"}}},
				Citations:  []agentdomain.Citation{citation}, ConflictPositions: []agentdomain.ConflictPosition{}, ConflictSummary: ""},
			RelatedTopics:     []agentdomain.RelatedTopic{{TopicID: foundation.ID("73000000-0000-4000-8000-000000000024"), Name: "Approved result", CitationIDs: []string{"cite-1"}}},
			FollowUpQuestions: []string{"What changed?"}}}
	return agentapplication.RAGTerminalProposal{ModelRunRef: runID, Answer: &answer, Retrieval: &agentapplication.RAGRetrievalSummary{
		Rewrites: []string{"approved result"}, RequestedMode: retrievaldomain.SearchModeKeyword, EffectiveMode: retrievaldomain.SearchModeKeyword,
		Filter: retrievaldomain.SearchFilter{}, IndexVersionID: indexID, CandidateCount: 1, SelectedCount: 1, Degradations: []retrievaldomain.SearchDegradation{}}}
}

func finalizerTestLookup() conversationapplication.AnswerPublicationLookup {
	return conversationapplication.AnswerPublicationLookup{WorkspaceID: foundation.ID("73000000-0000-4000-8000-000000000001"),
		WorkflowRunID: foundation.ID("73000000-0000-4000-8000-000000000002"), NodeRunID: foundation.ID("73000000-0000-4000-8000-000000000003"),
		NodeAttemptID: foundation.ID("73000000-0000-4000-8000-000000000004"), ConversationID: foundation.ID("73000000-0000-4000-8000-000000000005"),
		QuestionID: foundation.ID("73000000-0000-4000-8000-000000000006"), AnswerID: foundation.ID("73000000-0000-4000-8000-000000000007")}
}

func finalizerTestRun(lookup conversationapplication.AnswerPublicationLookup, at time.Time) agentdomain.ModelRun {
	return agentdomain.ModelRun{ID: foundation.ID("73000000-0000-4000-8000-000000000008"), WorkspaceID: lookup.WorkspaceID,
		WorkflowRunID: lookup.WorkflowRunID, NodeRunID: lookup.NodeRunID, NodeAttemptID: lookup.NodeAttemptID,
		Model:   agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "model", ModelVersion: "v1"},
		Profile: agentdomain.ModelProfileRef{ID: "rag", Version: "v1"}, Prompt: agentdomain.PromptRef{ID: "rag-answer", Version: "v1"},
		Schema:        agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		ReducedSchema: agentdomain.SchemaRef{ID: agentdomain.RefusalSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		Status:        agentdomain.ModelRunRunning, Version: 1, CreatedAt: at, UpdatedAt: at}
}

func finalizerRefusalProposal(runID foundation.ID) agentapplication.RAGTerminalProposal {
	refusal := agentdomain.RefusalResult{ResultType: agentdomain.ResultTypeRefusal, SchemaID: agentdomain.RefusalSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: runID,
		Payload: agentdomain.RefusalPayload{ReasonCode: agentdomain.RefusalNoRelevantEvidence, Summary: "No relevant evidence.",
			RetrievalScope: "approved knowledge", MissingRequirements: []string{"approved evidence"}, SuggestedActions: []string{"narrow the scope"}}}
	return agentapplication.RAGTerminalProposal{ModelRunRef: runID, Refusal: &refusal, Retrieval: finalizerEmptySummary()}
}

func finalizerClarificationProposal(runID foundation.ID) agentapplication.RAGTerminalProposal {
	return agentapplication.RAGTerminalProposal{ModelRunRef: runID,
		Clarification: &agentapplication.RAGClarificationProposal{Intent: "find policy", Reason: "scope is ambiguous", Question: "Which policy?", SuggestedScopes: []string{}},
		Retrieval:     finalizerEmptySummary()}
}

func finalizerEmptySummary() *agentapplication.RAGRetrievalSummary {
	return &agentapplication.RAGRetrievalSummary{Rewrites: []string{}, RequestedMode: retrievaldomain.SearchModeHybrid,
		EffectiveMode: retrievaldomain.SearchModeHybrid, Filter: retrievaldomain.SearchFilter{}, Degradations: []retrievaldomain.SearchDegradation{}}
}
