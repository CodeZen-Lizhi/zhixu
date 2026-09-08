package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestValidateCurrentWorkspaceAnalysisLeaseRequiresExactCallerOwnerAndFence(t *testing.T) {
	lookup := workspaceAnalysisFinalizerAdapterTestLookup()
	now := time.Date(2026, 8, 16, 8, 0, 0, 0, time.UTC)
	leaseUntil := now.Add(time.Minute)
	owner := lookup.ExpectedLeaseOwner
	fence := workspaceAnalysisFinalizationFence{
		WorkflowStatus: "running", NodeStatus: "running", AttemptStatus: "running",
		NodeAttemptNo: int(lookup.ExpectedLeaseFence), AttemptNo: int(lookup.ExpectedLeaseFence),
		NodeLeaseOwner: &owner, AttemptLeaseOwner: &owner,
		NodeLeaseUntil: &leaseUntil, AttemptLeaseUntil: &leaseUntil, DatabaseNow: now,
	}
	if err := validateCurrentWorkspaceAnalysisLease(fence, lookup); err != nil {
		t.Fatalf("validateCurrentWorkspaceAnalysisLease() = %v", err)
	}

	wrongOwner := lookup
	wrongOwner.ExpectedLeaseOwner = "other-workspace-analysis-worker"
	if err := validateCurrentWorkspaceAnalysisLease(fence, wrongOwner); err == nil {
		t.Fatal("caller with the wrong lease owner was accepted")
	}
	wrongFence := lookup
	wrongFence.ExpectedLeaseFence++
	if err := validateCurrentWorkspaceAnalysisLease(fence, wrongFence); err == nil {
		t.Fatal("caller with the wrong lease fence was accepted")
	}
}

func TestBuildWorkspaceAnalysisSuccessPublicationUsesSynthesisAndServerFacts(t *testing.T) {
	workspaceID := workspaceAnalysisFinalizerAdapterTestID(1)
	synthesisModelRunID := workspaceAnalysisFinalizerAdapterTestID(2)
	reviewModelRunID := workspaceAnalysisFinalizerAdapterTestID(3)
	candidateID := workspaceAnalysisFinalizerAdapterTestID(4)
	candidateDocument := workspaceAnalysisFinalizerAdapterTestJSON(t, agentdomain.WorkspaceAnalysisCandidateResult{
		ResultType: agentdomain.ResultTypeWorkspaceAnalysisCandidate,
		SchemaID:   agentdomain.WorkspaceAnalysisCandidateSchemaID, SchemaVersion: "1",
		ModelRunRef: synthesisModelRunID,
		Payload: agentdomain.WorkspaceAnalysisCandidatePayload{
			AnswerMarkdown: "The implementation preserves the durable publication boundary.",
			CitationRefs:   []string{"E1"},
			ProposalSuggestion: &agentdomain.WorkspaceAnalysisCandidateProposal{
				Summary: "Review the proposed follow-up.", CitationRefs: []string{"E1"},
			},
		},
	})
	candidateHash := workspaceAnalysisFinalizerAdapterTestHash(candidateDocument)
	createdAt := time.Date(2026, 8, 16, 8, 30, 0, 123456000, time.UTC)
	candidate := agentdomain.WorkspaceAnalysisCandidate{
		ID: candidateID, WorkspaceID: workspaceID,
		AnalysisRunID: workspaceAnalysisFinalizerAdapterTestID(5), AnswerID: workspaceAnalysisFinalizerAdapterTestID(6),
		SynthesisOperationID: workspaceAnalysisFinalizerAdapterTestID(7), NodeAttemptID: workspaceAnalysisFinalizerAdapterTestID(8),
		SynthesisModelRunID: synthesisModelRunID,
		SchemaID:            agentdomain.WorkspaceAnalysisCandidateSchemaID,
		SchemaVersion:       agentdomain.WorkspaceAnalysisCandidateSchemaVersion,
		Document:            candidateDocument, DocumentHash: candidateHash, DocumentBytes: int64(len(candidateDocument)), CreatedAt: createdAt,
	}
	gitOutput := workspaceAnalysisFinalizerAdapterTestJSON(t, map[string]any{
		"branch": "main", "head": strings.Repeat("a", 40), "object_format": "sha1", "clean": false,
		"staged_count": 1, "unstaged_count": 0, "untracked_count": 0, "conflict_count": 0,
	})
	gitReceipt := toolsdomain.ResultReceipt{
		ID: workspaceAnalysisFinalizerAdapterTestID(9), Tool: toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2},
		Output: gitOutput, CreatedAt: createdAt,
	}
	validationReceipt := workspaceAnalysisFinalizerAdapterValidationReceipt(
		t, workspaceID, candidateID, candidateHash, createdAt,
	)
	if _, err := agentdomain.DecodeWorkspaceAnalysisCandidate(candidateDocument, agentdomain.DefaultDecodeLimits()); err != nil {
		t.Fatalf("candidate fixture = %#v", err)
	}
	if _, err := conversationworkflow.DecodeWorkspaceAnalysisGitStatusSummary(gitOutput); err != nil {
		t.Fatalf("git fixture = %#v", err)
	}
	if _, err := toolsdomain.ValidateCitationV3ReceiptResults(validationReceipt, candidateID, candidateHash, []string{"E1"}); err != nil {
		var classified *foundation.Error
		_ = errors.As(err, &classified)
		t.Fatalf("citation fixture = %#v cause=%v", err, classified.Cause)
	}
	facts := workspaceAnalysisSuccessFacts{
		Candidate: candidate, GitReceipt: gitReceipt, ValidationReceipt: validationReceipt,
		ReviewResult: agentdomain.WorkspaceAnalysisModelResult{ModelRunID: reviewModelRunID},
	}
	cost := int64(321)
	publication, err := buildWorkspaceAnalysisSuccessPublication(workspaceID, workspaceAnalysisFinalizationFence{
		SettledModelCalls: 3, SettledToolCalls: 4, SettledInputTokens: 1200,
		SettledOutputTokens: 600, SettledCostMicrounits: &cost,
	}, facts)
	if err != nil {
		t.Fatalf("buildWorkspaceAnalysisSuccessPublication() = %#v", err)
	}
	if publication.ModelRunID == nil || *publication.ModelRunID != synthesisModelRunID ||
		*publication.ModelRunID == reviewModelRunID || publication.CitationCount != 1 ||
		publication.Status != conversationdomain.AnswerPublicationCompleted ||
		publication.RunStatus != agentdomain.WorkspaceAnalysisRunSucceeded {
		t.Fatalf("publication authority = %#v", publication)
	}
	var result conversationdomain.WorkspaceAnalysisAnswerResult
	if err := json.Unmarshal(publication.Document, &result); err != nil || result.Validate() != nil {
		t.Fatalf("published document = %s, err=%v", publication.Document, err)
	}
	if result.ModelRunRef != synthesisModelRunID || result.Payload.AnswerMarkdown != "The implementation preserves the durable publication boundary." ||
		len(result.Payload.Citations) != 1 || result.Payload.Citations[0].ID != workspaceAnalysisFinalizerAdapterCitationID() ||
		result.Payload.GitStatus.Branch != "main" || result.Payload.Budget.EstimatedCostMicrounits == nil ||
		*result.Payload.Budget.EstimatedCostMicrounits != cost || result.Payload.ProposalSuggestion == nil ||
		result.Payload.ProposalSuggestion.CitationIDs[0] != workspaceAnalysisFinalizerAdapterCitationID() {
		t.Fatalf("published server projection = %#v", result)
	}
	for _, callerOrReviewFact := range []string{string(candidateID), string(reviewModelRunID)} {
		if strings.Contains(string(publication.Document), callerOrReviewFact) {
			t.Fatalf("published document leaked non-authoring identity %s", callerOrReviewFact)
		}
	}
}

func TestWorkspaceAnalysisTerminationPublicationsFollowFrozenStatusAndModelMatrix(t *testing.T) {
	modelRunID := workspaceAnalysisFinalizerAdapterTestID(30)
	tests := []struct {
		reason    agentdomain.WorkspaceAnalysisRunTerminationReason
		model     *foundation.ID
		status    conversationdomain.AnswerPublicationStatus
		runStatus agentdomain.WorkspaceAnalysisRunStatus
	}{
		{agentdomain.WorkspaceAnalysisRunEvidenceInsufficient, nil, conversationdomain.AnswerPublicationRefused, agentdomain.WorkspaceAnalysisRunRefused},
		{agentdomain.WorkspaceAnalysisRunCitationInvalid, nil, conversationdomain.AnswerPublicationRefused, agentdomain.WorkspaceAnalysisRunRefused},
		{agentdomain.WorkspaceAnalysisRunFaithfulnessRejected, nil, conversationdomain.AnswerPublicationRefused, agentdomain.WorkspaceAnalysisRunRefused},
		{agentdomain.WorkspaceAnalysisRunModelRefused, &modelRunID, conversationdomain.AnswerPublicationRefused, agentdomain.WorkspaceAnalysisRunRefused},
		{agentdomain.WorkspaceAnalysisRunBudgetExhausted, nil, conversationdomain.WorkspaceAnalysisPublicationFailed, agentdomain.WorkspaceAnalysisRunFailed},
		{agentdomain.WorkspaceAnalysisRunReceiptInvalid, nil, conversationdomain.WorkspaceAnalysisPublicationFailed, agentdomain.WorkspaceAnalysisRunFailed},
		{agentdomain.WorkspaceAnalysisRunResultUnknown, &modelRunID, conversationdomain.WorkspaceAnalysisPublicationFailed, agentdomain.WorkspaceAnalysisRunFailed},
		{agentdomain.WorkspaceAnalysisRunDeadlineExceeded, nil, conversationdomain.WorkspaceAnalysisPublicationFailed, agentdomain.WorkspaceAnalysisRunFailed},
		{agentdomain.WorkspaceAnalysisRunModelFailed, &modelRunID, conversationdomain.WorkspaceAnalysisPublicationFailed, agentdomain.WorkspaceAnalysisRunFailed},
		{agentdomain.WorkspaceAnalysisRunToolFailed, nil, conversationdomain.WorkspaceAnalysisPublicationFailed, agentdomain.WorkspaceAnalysisRunFailed},
		{agentdomain.WorkspaceAnalysisRunRuntimeFailed, nil, conversationdomain.WorkspaceAnalysisPublicationFailed, agentdomain.WorkspaceAnalysisRunFailed},
		{agentdomain.WorkspaceAnalysisRunCancellation, nil, conversationdomain.WorkspaceAnalysisPublicationCancelled, agentdomain.WorkspaceAnalysisRunCancelled},
	}
	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			publication, err := canonicalWorkspaceAnalysisTerminationPublication(test.reason, test.model)
			if err != nil {
				t.Fatalf("canonicalWorkspaceAnalysisTerminationPublication() = %v", err)
			}
			if publication.Status != test.status || publication.RunStatus != test.runStatus || publication.Reason != test.reason ||
				!equalOptionalWorkspaceAnalysisID(publication.ModelRunID, test.model) || len(publication.Document) == 0 || publication.ResultHash == "" {
				t.Fatalf("publication = %#v", publication)
			}
			canonical, canonicalErr := conversationdomain.CanonicalizeWorkspaceAnalysisPublishedResult(
				publication.Status, publication.ResultType, publication.Document,
			)
			if canonicalErr != nil || canonical.Hash != publication.ResultHash || !equalOptionalWorkspaceAnalysisID(canonical.ModelRunID, test.model) {
				t.Fatalf("canonical publication = %#v, err=%v", canonical, canonicalErr)
			}
		})
	}
	if _, err := canonicalWorkspaceAnalysisTerminationPublication(agentdomain.WorkspaceAnalysisRunModelRefused, nil); err == nil {
		t.Fatal("model refusal accepted no authoring Model Run")
	}
	if _, err := canonicalWorkspaceAnalysisTerminationPublication(agentdomain.WorkspaceAnalysisRunNeedsClarification, &modelRunID); err == nil {
		t.Fatal("clarification bypassed its dedicated publication projection")
	}
}

func TestWorkspaceAnalysisPublicationLookupFailsClosedOnSplitStateAndReturnsExactProof(t *testing.T) {
	lookup := workspaceAnalysisFinalizerAdapterTestLookup()
	pending := workspaceAnalysisFinalizerAdapterPendingAnswer(lookup)
	if output, found, err := workspaceAnalysisOutputFromState(lookup, workspaceAnalysisProofState{
		Answer: pending, RunStatus: agentdomain.WorkspaceAnalysisRunRunning,
	}); err != nil || found || output.AnswerID != "" || output.ProofID != "" {
		t.Fatalf("pending lookup = %#v found=%t err=%v", output, found, err)
	}
	if _, _, err := workspaceAnalysisOutputFromState(lookup, workspaceAnalysisProofState{
		Answer: pending, RunStatus: agentdomain.WorkspaceAnalysisRunFailed,
	}); err == nil {
		t.Fatal("pending Answer with terminal Analysis Run did not fail closed")
	}

	publication, err := canonicalWorkspaceAnalysisTerminationPublication(agentdomain.WorkspaceAnalysisRunCancellation, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	answer := pending
	answer.PublicationStatus, answer.ResultType, answer.Result, answer.ResultHash = publication.Status, publication.ResultType, publication.Document, publication.ResultHash
	answer.Version, answer.UpdatedAt, answer.PublishedAt = 2, now, &now
	proofID := workspaceAnalysisFinalizerAdapterTestID(40)
	reason := agentdomain.WorkspaceAnalysisRunCancellation
	output, found, err := workspaceAnalysisOutputFromState(lookup, workspaceAnalysisProofState{
		Answer: answer, RunStatus: agentdomain.WorkspaceAnalysisRunCancelled, RunReason: &reason,
		Termination: &workspaceAnalysisTerminationProofRecord{
			ID: proofID, NodeRunID: lookup.NodeRunID, NodeAttemptID: workspaceAnalysisFinalizerAdapterTestID(41),
			Reason: reason, PublishedDocument: publication.Document, PublishedResultHash: publication.ResultHash,
		},
	})
	if err != nil || !found || output.AnswerID != lookup.AnswerID || output.ProofID != proofID ||
		output.PublicationStatus != conversationdomain.WorkspaceAnalysisPublicationCancelled || output.ResultHash != publication.ResultHash {
		t.Fatalf("exact replacement-attempt lookup = %#v found=%t err=%v", output, found, err)
	}
	if _, _, err := workspaceAnalysisOutputFromState(lookup, workspaceAnalysisProofState{
		Answer: answer, RunStatus: agentdomain.WorkspaceAnalysisRunCancelled, RunReason: &reason,
		Termination: &workspaceAnalysisTerminationProofRecord{
			ID: proofID, NodeRunID: lookup.NodeRunID, NodeAttemptID: lookup.NodeAttemptID,
			Reason: reason, PublishedDocument: publication.Document, PublishedResultHash: strings.Repeat("f", 64),
		},
	}); err == nil {
		t.Fatal("split proof hash did not fail closed")
	}
}

func TestWorkspaceAnalysisDraftAndEventTerminalContracts(t *testing.T) {
	attemptID := workspaceAnalysisFinalizerAdapterTestID(50)
	candidate := agentdomain.WorkspaceAnalysisCandidate{NodeAttemptID: attemptID}
	for _, status := range []string{"COMPLETED", "DEGRADED"} {
		if err := validateSuccessDraft(&workspaceAnalysisDraftRecord{NodeAttemptID: attemptID, Status: status}, candidate); err != nil {
			t.Fatalf("success draft %s = %v", status, err)
		}
	}
	if err := validateSuccessDraft(&workspaceAnalysisDraftRecord{NodeAttemptID: attemptID, Status: "ACTIVE"}, candidate); err == nil {
		t.Fatal("success accepted an ACTIVE draft")
	}

	appender := &workspaceAnalysisEventCapture{}
	finalizer := &GORMWorkspaceAnalysisFinalizer{events: appender}
	answerID := workspaceAnalysisFinalizerAdapterTestID(51)
	answer := conversationdomain.Answer{
		ID: answerID, WorkspaceID: workspaceAnalysisFinalizerAdapterTestID(52),
		ConversationID: workspaceAnalysisFinalizerAdapterTestID(53), WorkflowRunID: workspaceAnalysisFinalizerAdapterTestID(54),
		PublicationStatus: conversationdomain.WorkspaceAnalysisPublicationFailed,
		ResultType:        conversationdomain.AnswerResultWorkspaceAnalysisTermination,
		Version:           2,
	}
	analysisRunID := workspaceAnalysisFinalizerAdapterTestID(55)
	if err := finalizer.appendWorkspaceAnalysisTerminalEvents(
		context.Background(), nil, analysisRunID, agentdomain.WorkspaceAnalysisRunFailed, answer, 0, time.Now().UTC(), false,
	); err != nil {
		t.Fatalf("appendWorkspaceAnalysisTerminalEvents() = %v", err)
	}
	if len(appender.requests) != 2 {
		t.Fatalf("event count=%d requests=%#v", len(appender.requests), appender.requests)
	}
	answerEvent, analysisEvent := appender.requests[0], appender.requests[1]
	if answerEvent.Type != "answer.failed" || answerEvent.SourceEventRef != "answer.failed:"+string(answerID)+":v2" ||
		answerEvent.ResourceRef != "answer:"+string(answerID) || answerEvent.ResourceVersion != 2 {
		t.Fatalf("answer event protocol = %#v", answerEvent)
	}
	if analysisEvent.Type != "workspace_analysis.terminated" ||
		analysisEvent.SourceEventRef != "workspace_analysis.terminated:"+string(analysisRunID)+":v1" ||
		analysisEvent.ResourceRef != "workspace_analysis:"+string(analysisRunID) || analysisEvent.ResourceVersion != 2 ||
		analysisEvent.PayloadSummary.Status != string(agentdomain.WorkspaceAnalysisRunFailed) ||
		analysisEvent.PayloadSummary.PublicationStatus != "failed" ||
		analysisEvent.PayloadSummary.ResultType != string(conversationdomain.AnswerResultWorkspaceAnalysisTermination) ||
		analysisEvent.PayloadSummary.CitationCount == nil || *analysisEvent.PayloadSummary.CitationCount != 0 ||
		analysisEvent.PayloadSummary.ModelRunID != nil {
		t.Fatalf("analysis terminal event protocol = %#v", analysisEvent)
	}
}

type workspaceAnalysisEventCapture struct {
	requests []eventsdomain.AppendRequest
	replay   bool
}

func (capture *workspaceAnalysisEventCapture) AppendScoped(
	_ context.Context,
	_ foundation.TransactionScope,
	request eventsdomain.AppendRequest,
) (eventsdomain.ServerEvent, bool, error) {
	capture.requests = append(capture.requests, request)
	return eventsdomain.ServerEvent{}, capture.replay, nil
}

func workspaceAnalysisFinalizerAdapterValidationReceipt(
	t *testing.T,
	workspaceID, candidateID foundation.ID,
	candidateHash string,
	createdAt time.Time,
) toolsdomain.ResultReceipt {
	t.Helper()
	output := workspaceAnalysisFinalizerAdapterTestJSON(t, map[string]any{
		"results": []any{map[string]any{"evidence_ref": "E1", "valid": true, "reason_code": "OK"}},
	})
	bindingDocument := workspaceAnalysisFinalizerAdapterTestJSON(t, map[string]any{
		"candidate_id": candidateID, "candidate_hash": candidateHash,
		"results": []any{map[string]any{
			"evidence_ref": "E1", "citation_id": workspaceAnalysisFinalizerAdapterCitationID(),
			"index_version_id":  workspaceAnalysisFinalizerAdapterTestID(20),
			"chunk_id":          workspaceAnalysisFinalizerAdapterTestID(21),
			"source_version_id": workspaceAnalysisFinalizerAdapterTestID(22),
			"source_span_id":    workspaceAnalysisFinalizerAdapterTestID(23),
			"content_hash":      strings.Repeat("b", 64),
		}},
	})
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(toolsdomain.ToolRef{Name: "ValidateCitation", Version: 3})
	if !found {
		t.Fatal("ValidateCitation@3 receipt contract missing")
	}
	return toolsdomain.ResultReceipt{
		ID: workspaceAnalysisFinalizerAdapterTestID(10), ToolCallID: workspaceAnalysisFinalizerAdapterTestID(11),
		WorkspaceID: workspaceID, WorkflowRunID: workspaceAnalysisFinalizerAdapterTestID(12),
		NodeRunID: workspaceAnalysisFinalizerAdapterTestID(13), NodeAttemptID: workspaceAnalysisFinalizerAdapterTestID(14),
		Tool: toolsdomain.ToolRef{Name: "ValidateCitation", Version: 3}, OutputSchema: contract.OutputSchema,
		DefinitionHash: strings.Repeat("d", 64), PersistencePolicy: toolsdomain.ResultPersistenceCanonical,
		MaxOutputBytes: contract.MaxOutputBytes, MaxPrivateBindingBytes: contract.MaxPrivateBindingBytes,
		Output: output, OutputHash: workspaceAnalysisFinalizerAdapterTestHash(output), OutputBytes: int64(len(output)),
		PrivateBinding: &toolsdomain.ResultReceiptPrivateBinding{
			Schema: contract.PrivateBindingSchema, Document: bindingDocument,
			Hash: workspaceAnalysisFinalizerAdapterTestHash(bindingDocument), Bytes: int64(len(bindingDocument)),
		},
		CreatedAt: createdAt,
	}
}

func workspaceAnalysisFinalizerAdapterTestLookup() conversationapplication.WorkspaceAnalysisPublicationLookup {
	return conversationapplication.WorkspaceAnalysisPublicationLookup{
		AnswerPublicationLookup: conversationapplication.AnswerPublicationLookup{
			WorkspaceID: workspaceAnalysisFinalizerAdapterTestID(60), WorkflowRunID: workspaceAnalysisFinalizerAdapterTestID(61),
			NodeRunID: workspaceAnalysisFinalizerAdapterTestID(62), NodeAttemptID: workspaceAnalysisFinalizerAdapterTestID(63),
			ConversationID: workspaceAnalysisFinalizerAdapterTestID(64), QuestionID: workspaceAnalysisFinalizerAdapterTestID(65),
			AnswerID: workspaceAnalysisFinalizerAdapterTestID(66),
		},
		AnalysisRunID:      workspaceAnalysisFinalizerAdapterTestID(67),
		ExpectedLeaseOwner: "workspace-analysis-worker",
		ExpectedLeaseFence: 3,
	}
}

func workspaceAnalysisFinalizerAdapterPendingAnswer(
	lookup conversationapplication.WorkspaceAnalysisPublicationLookup,
) conversationdomain.Answer {
	createdAt := time.Date(2026, 8, 16, 8, 0, 0, 0, time.UTC)
	return conversationdomain.Answer{
		ID: lookup.AnswerID, WorkspaceID: lookup.WorkspaceID, ConversationID: lookup.ConversationID,
		QuestionID: lookup.QuestionID, WorkflowRunID: lookup.WorkflowRunID,
		PublicationStatus: conversationdomain.AnswerPublicationPending, Version: 1,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

func workspaceAnalysisFinalizerAdapterTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	document, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return document
}

func workspaceAnalysisFinalizerAdapterTestHash(document []byte) string {
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:])
}

func workspaceAnalysisFinalizerAdapterTestID(suffix int) foundation.ID {
	return foundation.ID(fmt.Sprintf("98000000-0000-4000-8000-%012d", suffix))
}

func workspaceAnalysisFinalizerAdapterCitationID() string {
	return "cite-" + strings.Repeat("c", 64)
}
