package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisReviewRunnerAuthorizesOnceAndBindsCanonicalReviewToCandidate(t *testing.T) {
	events := []string{}
	repository := &workspaceAnalysisReviewRepository{events: &events}
	request := workspaceAnalysisReviewRequest(t)
	request.Evidence[0].Excerpt = "bounded evidence\n"
	if formatted := fmt.Sprintf("%#v", request.Evidence); strings.Contains(formatted, "bounded evidence") {
		t.Fatalf("evidence debug projection leaked excerpt: %s", formatted)
	}
	model := &workspaceAnalysisPlanModel{
		events: &events,
		response: ChatResponse{
			Model: workspaceAnalysisReviewModelRef(), Content: workspaceAnalysisProviderReview(t, request.Candidate, true),
			Usage: domain.TokenUsage{InputTokens: 31, OutputTokens: 9, TotalTokens: 40},
		},
	}
	runner := newWorkspaceAnalysisReviewRunner(t, model, repository)

	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"authorize", "provider", "finalize_result"}) {
		t.Fatalf("events = %#v", events)
	}
	if model.CallCount() != 1 || repository.authorizeCalls != 1 || repository.finalizeResultCalls != 1 ||
		repository.finalizeCallCalls != 0 || repository.loadCalls != 0 {
		t.Fatalf("calls = provider:%d authorize:%d finalize_result:%d finalize_call:%d load:%d",
			model.CallCount(), repository.authorizeCalls, repository.finalizeResultCalls,
			repository.finalizeCallCalls, repository.loadCalls)
	}
	providerCall := model.Calls()[0]
	if providerCall.Phase != domain.ModelCallReview ||
		providerCall.SchemaRef != (domain.SchemaRef{ID: domain.FaithfulnessReviewSchemaID, Version: domain.OutputSchemaVersionV1}) ||
		providerCall.MaxOutputTokens != int(WorkspaceAnalysisV1ReviewMaxOutputTokens) || len(providerCall.Messages) != 3 ||
		!strings.Contains(providerCall.Messages[2].Content, `"model_run_ref":"`+string(request.Candidate.SynthesisModelRunID)+`"`) ||
		!strings.Contains(providerCall.Messages[2].Content, `"candidate":{"answer_markdown":"bounded answer [E1]"`) ||
		!strings.Contains(providerCall.Messages[2].Content, `"id":"@answer/conclusion"`) ||
		!strings.Contains(providerCall.Messages[2].Content, `"evidence_ref":"E1","excerpt":"bounded evidence\n"`) ||
		strings.Contains(providerCall.Messages[2].Content, string(request.Candidate.ID)) ||
		strings.Contains(providerCall.Messages[2].Content, request.Candidate.DocumentHash) {
		t.Fatalf("provider call = %#v", providerCall)
	}
	requestDocument, err := json.Marshal(providerCall)
	if err != nil {
		t.Fatal(err)
	}
	authorized := repository.authorizedCommands[0]
	if authorized.OperationKey.Kind != domain.WorkspaceAnalysisOperationFaithfulnessReview ||
		authorized.OperationKey.NodeKey != domain.WorkspaceAnalysisOperationNodeReviewPublish ||
		authorized.OperationKey.Ordinal != 1 || !reflect.DeepEqual(authorized.RequestDocument, requestDocument) ||
		authorized.Call.RequestHash != workspaceAnalysisSHA256(requestDocument) ||
		authorized.Call.Phase != domain.ModelCallReview ||
		authorized.Call.MaxOutputTokens != int(WorkspaceAnalysisV1ReviewMaxOutputTokens) {
		t.Fatalf("authorized command = %#v", authorized)
	}
	if !result.Passed || !result.Review.Payload.Passed || result.Review.ModelRunRef != result.Run.ID ||
		result.ModelResult.SubjectCandidateID == nil || *result.ModelResult.SubjectCandidateID != request.Candidate.ID ||
		result.ModelResult.SubjectCandidateHash != request.Candidate.DocumentHash ||
		result.ModelResult.DocumentHash != result.Call.ResponseHash || result.Replayed ||
		result.Usage != (domain.TokenUsage{InputTokens: 31, OutputTokens: 9, TotalTokens: 40}) {
		t.Fatalf("result = %#v", result)
	}
	canonical, err := json.Marshal(result.Review)
	if err != nil || !reflect.DeepEqual([]byte(result.ModelResult.Document), canonical) {
		t.Fatalf("canonical result = %s, %v", result.ModelResult.Document, err)
	}
	if strings.Contains(string(result.ModelResult.Document), string(request.Candidate.SynthesisModelRunID)) {
		t.Fatalf("persisted review retained the subject model run ref: %s", result.ModelResult.Document)
	}
	finalized := repository.finalizeResultCommands[0]
	if finalized.Result.SubjectCandidateID == nil || *finalized.Result.SubjectCandidateID != request.Candidate.ID ||
		finalized.Result.SubjectCandidateHash != request.Candidate.DocumentHash ||
		finalized.Run.FinalResultType != domain.ResultTypeFaithfulnessReview || finalized.Call.Usage != result.Usage {
		t.Fatalf("finalized command = %#v", finalized)
	}
}

func TestWorkspaceAnalysisReviewRunnerPersistsRejectedVerdictAsReviewSuccess(t *testing.T) {
	request := workspaceAnalysisReviewRequest(t)
	repository := &workspaceAnalysisReviewRepository{}
	model := &workspaceAnalysisPlanModel{response: ChatResponse{
		Model: workspaceAnalysisReviewModelRef(), Content: workspaceAnalysisProviderReview(t, request.Candidate, false),
		Usage: domain.TokenUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28},
	}}
	runner := newWorkspaceAnalysisReviewRunner(t, model, repository)

	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Passed || result.Review.Payload.Passed || result.Run.Status != domain.ModelRunSucceeded ||
		result.Call.Status != domain.ModelCallSucceeded || repository.finalizeResultCalls != 1 || repository.finalizeCallCalls != 0 {
		t.Fatalf("result/calls = %#v finalize:%d/%d", result, repository.finalizeResultCalls, repository.finalizeCallCalls)
	}
}

func TestWorkspaceAnalysisReviewRunnerSettlesCallWhenCancellationWinsResultPublication(t *testing.T) {
	request := workspaceAnalysisReviewRequest(t)
	repository := &workspaceAnalysisReviewRepository{
		finalizeResultErr: foundation.NewError(
			foundation.ErrorVersionConflict,
			ErrorCodeWorkspaceAnalysisModelCancellationConflict,
			false,
			errors.New("cancel requested before review publication"),
		),
	}
	model := &workspaceAnalysisPlanModel{response: ChatResponse{
		Model: workspaceAnalysisReviewModelRef(), Content: workspaceAnalysisProviderReview(t, request.Candidate, true),
		Usage: domain.TokenUsage{InputTokens: 31, OutputTokens: 9, TotalTokens: 40},
	}}
	runner := newWorkspaceAnalysisReviewRunner(t, model, repository)

	_, err := runner.Run(context.Background(), request)
	if err == nil {
		t.Fatal("Run unexpectedly published a review after cancellation")
	}
	if model.CallCount() != 1 || repository.finalizeResultCalls != 1 || repository.finalizeCallCalls != 1 {
		t.Fatalf("calls = provider:%d result:%d failure:%d", model.CallCount(), repository.finalizeResultCalls, repository.finalizeCallCalls)
	}
	if _, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err); !found {
		t.Fatalf("Run error did not retain settled model terminal evidence: %v", err)
	}
	if command := repository.finalizeCallCommands[0]; command.Call.Status != domain.ModelCallFailed ||
		command.Run.Status != domain.ModelRunFailed || command.Call.ErrorCode != ErrorCodeOperationCancelled {
		t.Fatalf("settlement command = %#v", command)
	}
}

func TestWorkspaceAnalysisReviewRunnerReplacementReusesExactCandidateResultWithoutProvider(t *testing.T) {
	request := workspaceAnalysisReviewRequest(t)
	repository := &workspaceAnalysisReviewRepository{}
	repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
		persisted := command
		persisted.OperationID = workspaceAnalysisPlanTestID(810)
		persisted.ReservationID = workspaceAnalysisPlanTestID(811)
		persisted.Run.ID = workspaceAnalysisPlanTestID(812)
		persisted.Run.NodeAttemptID = workspaceAnalysisPlanTestID(813)
		persisted.Call.ID = workspaceAnalysisPlanTestID(814)
		persisted.Call.ModelRunID = persisted.Run.ID
		authorized, result := workspaceAnalysisSuccessfulReviewReplay(t, persisted, request.Candidate, true)
		repository.loadedResult = result
		return authorized, nil
	}
	model := &workspaceAnalysisPlanModel{}
	runner := newWorkspaceAnalysisReviewRunner(t, model, repository)

	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Replayed || !result.Passed || result.Run.NodeAttemptID == request.Identity.NodeAttemptID ||
		result.ModelResult.NodeAttemptID != result.Run.NodeAttemptID || result.ModelResult.ID != repository.loadedResult.ID ||
		model.CallCount() != 0 || repository.loadCalls != 1 || repository.finalizeCallCalls != 0 || repository.finalizeResultCalls != 0 {
		t.Fatalf("result/calls = %#v provider:%d load:%d finalize:%d/%d", result, model.CallCount(), repository.loadCalls,
			repository.finalizeCallCalls, repository.finalizeResultCalls)
	}
}

func TestWorkspaceAnalysisReviewRunnerPropagatesCommitRecoveryReplayAndFailsClosedOnUnconfirmedCommit(t *testing.T) {
	request := workspaceAnalysisReviewRequest(t)
	response := ChatResponse{
		Model: workspaceAnalysisReviewModelRef(), Content: workspaceAnalysisProviderReview(t, request.Candidate, true),
		Usage: domain.TokenUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28},
	}

	t.Run("adapter recovered committed result", func(t *testing.T) {
		repository := &workspaceAnalysisReviewRepository{}
		repository.finalizeResult = func(command FinalizeWorkspaceAnalysisModelResultCommand) (WorkspaceAnalysisModelMutationResult, error) {
			result := command.Result
			return WorkspaceAnalysisModelMutationResult{
				Run: command.Run, Call: command.Call, Result: &result,
				OperationID: command.OperationID, ReservationID: command.ReservationID, Replayed: true,
			}, nil
		}
		runner := newWorkspaceAnalysisReviewRunner(t, &workspaceAnalysisPlanModel{response: response}, repository)
		result, err := runner.Run(context.Background(), request)
		if err != nil || !result.Replayed || repository.finalizeResultCalls != 1 {
			t.Fatalf("result=%#v finalize=%d err=%v", result, repository.finalizeResultCalls, err)
		}
	})

	t.Run("commit acknowledgement remains unknown", func(t *testing.T) {
		repository := &workspaceAnalysisReviewRepository{finalizeResultErr: errors.New("commit acknowledgement lost")}
		runner := newWorkspaceAnalysisReviewRunner(t, &workspaceAnalysisPlanModel{response: response}, repository)
		_, err := runner.Run(context.Background(), request)
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired ||
			classified.Code != ErrorCodeModelCallPersistenceUnknown {
			t.Fatalf("Run error = %#v", err)
		}
		if _, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err); found {
			t.Fatal("unconfirmed successful finalization unexpectedly exposed terminal evidence")
		}
		if repository.finalizeResultCalls != 1 || repository.finalizeCallCalls != 0 {
			t.Fatalf("finalize calls = %d/%d", repository.finalizeResultCalls, repository.finalizeCallCalls)
		}
	})
}

func TestWorkspaceAnalysisReviewRunnerNeverCallsProviderForNonCreatedDisposition(t *testing.T) {
	tests := []struct {
		name        string
		disposition WorkspaceAnalysisModelAuthorizationDisposition
		status      domain.ModelCallStatus
		runStatus   domain.ModelRunStatus
		code        string
		wantCode    string
		wantKind    foundation.ErrorKind
	}{
		{name: "reconcile", disposition: WorkspaceAnalysisModelAuthorizationReconcile, status: domain.ModelCallStarted, runStatus: domain.ModelRunRunning, wantCode: ErrorCodeModelCallReplayUnsafe, wantKind: foundation.ErrorManualRecoveryRequired},
		{name: "failed", disposition: WorkspaceAnalysisModelAuthorizationReplayFailure, status: domain.ModelCallFailed, runStatus: domain.ModelRunFailed, code: "MODEL_PROVIDER_FAILED", wantCode: "MODEL_PROVIDER_FAILED", wantKind: foundation.ErrorNonRetryableFailure},
		{name: "unknown", disposition: WorkspaceAnalysisModelAuthorizationTerminateUnknown, status: domain.ModelCallUnknown, runStatus: domain.ModelRunUnknown, code: "MODEL_PROVIDER_RESULT_UNKNOWN", wantCode: "MODEL_PROVIDER_RESULT_UNKNOWN", wantKind: foundation.ErrorManualRecoveryRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &workspaceAnalysisReviewRepository{}
			persistedOperationID := workspaceAnalysisPlanTestID(903)
			repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
				result := WorkspaceAnalysisModelAuthorizationResult{
					Run: command.Run, Call: command.Call, OperationID: persistedOperationID,
					ReservationID: command.ReservationID, Disposition: test.disposition,
				}
				if test.status != domain.ModelCallStarted {
					completedAt := command.Call.StartedAt.Add(time.Second)
					result.Call.Status, result.Call.ErrorCode, result.Call.Version = test.status, test.code, 2
					result.Call.CompletedAt, result.Call.LatencyMillis = &completedAt, 1000
					result.Run.Status, result.Run.FinalErrorCode, result.Run.Version = test.runStatus, test.code, 2
					result.Run.UpdatedAt, result.Run.CompletedAt = completedAt, &completedAt
				}
				return result, nil
			}
			model := &workspaceAnalysisPlanModel{}
			runner := newWorkspaceAnalysisReviewRunner(t, model, repository)
			_, err := runner.Run(context.Background(), workspaceAnalysisReviewRequest(t))
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != test.wantCode || classified.Kind != test.wantKind {
				t.Fatalf("Run error = %#v", err)
			}
			if test.disposition == WorkspaceAnalysisModelAuthorizationReconcile {
				if _, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err); found {
					t.Fatal("reconciliation unexpectedly exposed terminal evidence")
				}
			} else {
				assertWorkspaceAnalysisModelTerminalEvidence(t, err, persistedOperationID, test.status, test.runStatus, test.wantKind, test.wantCode)
			}
			if model.CallCount() != 0 || repository.finalizeCallCalls != 0 || repository.finalizeResultCalls != 0 || repository.loadCalls != 0 {
				t.Fatalf("calls = provider:%d finalize:%d/%d load:%d", model.CallCount(), repository.finalizeCallCalls,
					repository.finalizeResultCalls, repository.loadCalls)
			}
		})
	}
}

func TestWorkspaceAnalysisReviewRunnerStrictlyRejectsSubjectAndIdentityDrift(t *testing.T) {
	request := workspaceAnalysisReviewRequest(t)
	valid := workspaceAnalysisProviderReview(t, request.Candidate, true)
	wrongSubject := workspaceAnalysisProviderReviewResult(request.Candidate, true)
	wrongSubject.ModelRunRef = workspaceAnalysisPlanTestID(999)
	wrongSubjectDocument, err := json.Marshal(wrongSubject)
	if err != nil {
		t.Fatal(err)
	}
	identityLeak := workspaceAnalysisProviderReviewResult(request.Candidate, true)
	identityLeak.Payload.Summary = "candidate " + string(request.Candidate.ID)
	identityLeakDocument, err := json.Marshal(identityLeak)
	if err != nil {
		t.Fatal(err)
	}
	escapedIdentity := fmt.Sprintf(`\u%04x%s`, request.Candidate.ID[0], request.Candidate.ID[1:])
	escapedIdentityLeak := strings.Replace(string(identityLeakDocument), string(request.Candidate.ID), escapedIdentity, 1)
	subjectLeak := workspaceAnalysisProviderReviewResult(request.Candidate, true)
	subjectLeak.Payload.Summary = "subject " + string(request.Candidate.SynthesisModelRunID)
	subjectLeakDocument, err := json.Marshal(subjectLeak)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"wrong subject":               wrongSubjectDocument,
		"identity leak":               identityLeakDocument,
		"escaped identity leak":       []byte(escapedIdentityLeak),
		"subject repeated in payload": subjectLeakDocument,
		"unknown field":               []byte(strings.TrimSuffix(string(valid), "}") + `,"unknown":true}`),
		"duplicate key":               []byte(`{"result_type":"faithfulness_review","result_type":"faithfulness_review","schema_id":"agent.faithfulness-review","schema_version":"v1","model_run_ref":"` + string(request.Candidate.SynthesisModelRunID) + `","payload":{"passed":true,"items":[],"summary":"ok"}}`),
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			repository := &workspaceAnalysisReviewRepository{}
			model := &workspaceAnalysisPlanModel{response: ChatResponse{
				Model: workspaceAnalysisReviewModelRef(), Content: output,
				Usage: domain.TokenUsage{InputTokens: 11, OutputTokens: 3, TotalTokens: 14},
			}}
			runner := newWorkspaceAnalysisReviewRunner(t, model, repository)
			_, err := runner.Run(context.Background(), request)
			if err == nil {
				t.Fatal("invalid provider review unexpectedly succeeded")
			}
			if model.CallCount() != 1 || repository.finalizeCallCalls != 1 || repository.finalizeResultCalls != 0 {
				t.Fatalf("calls = provider:%d finalize:%d/%d", model.CallCount(), repository.finalizeCallCalls,
					repository.finalizeResultCalls)
			}
			terminal := repository.finalizeCallCommands[0]
			if terminal.Call.Status != domain.ModelCallFailed || terminal.Call.ResponseHash != workspaceAnalysisSHA256(output) ||
				terminal.Call.ResponseBytes != int64(len(output)) {
				t.Fatalf("terminal = %#v", terminal)
			}
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified == nil {
				t.Fatalf("Run error = %#v", err)
			}
			assertWorkspaceAnalysisModelTerminalEvidence(t, err, terminal.OperationID, terminal.Call.Status, terminal.Run.Status, classified.Kind, classified.Code)
		})
	}
}

func TestWorkspaceAnalysisReviewRunnerRejectsIncompleteOrFabricatedSemanticCoverage(t *testing.T) {
	request := workspaceAnalysisReviewRequest(t)
	tests := map[string]domain.FaithfulnessReviewResult{}

	missing := workspaceAnalysisProviderReviewResult(request.Candidate, true)
	missing.Payload.Items = []domain.FaithfulnessReviewItem{}
	tests["missing conclusion"] = missing

	extra := workspaceAnalysisProviderReviewResult(request.Candidate, true)
	extra.Payload.Items = append(extra.Payload.Items, domain.FaithfulnessReviewItem{
		AssertionID: "extra-claim", Verdict: domain.FaithfulnessSupported,
		CitationIDs: []string{"E1"}, Reason: "extra target",
	})
	tests["extra target"] = extra

	wrongRefs := workspaceAnalysisProviderReviewResult(request.Candidate, true)
	wrongRefs.Payload.Items[0].CitationIDs = []string{"E2"}
	tests["wrong citation set"] = wrongRefs

	wrongTarget := workspaceAnalysisProviderReviewResult(request.Candidate, true)
	wrongTarget.Payload.Items[0].AssertionID = "different-conclusion"
	tests["wrong target identity"] = wrongTarget

	fakePass := workspaceAnalysisProviderReviewResult(request.Candidate, true)
	fakePass.Payload.Items[0].Verdict = domain.FaithfulnessInferenceDisclosed
	fakePass.Payload.Items[0].CitationIDs = []string{}
	tests["inference masquerades as passed conclusion"] = fakePass

	for name, review := range tests {
		t.Run(name, func(t *testing.T) {
			output, err := json.Marshal(review)
			if err != nil {
				t.Fatal(err)
			}
			repository := &workspaceAnalysisReviewRepository{}
			model := &workspaceAnalysisPlanModel{response: ChatResponse{
				Model: workspaceAnalysisReviewModelRef(), Content: output,
				Usage: domain.TokenUsage{InputTokens: 11, OutputTokens: 3, TotalTokens: 14},
			}}
			runner := newWorkspaceAnalysisReviewRunner(t, model, repository)
			if _, err := runner.Run(context.Background(), request); err == nil {
				t.Fatal("semantically incomplete review unexpectedly succeeded")
			}
			if model.CallCount() != 1 || repository.finalizeCallCalls != 1 || repository.finalizeResultCalls != 0 {
				t.Fatalf("calls = provider:%d finalize:%d/%d", model.CallCount(), repository.finalizeCallCalls,
					repository.finalizeResultCalls)
			}
		})
	}
}

func TestWorkspaceAnalysisReviewRunnerRejectsCandidateHashDriftOnReplay(t *testing.T) {
	request := workspaceAnalysisReviewRequest(t)
	repository := &workspaceAnalysisReviewRepository{}
	repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
		authorized, result := workspaceAnalysisSuccessfulReviewReplay(t, command, request.Candidate, true)
		result.SubjectCandidateHash = strings.Repeat("f", 64)
		repository.loadedResult = result
		return authorized, nil
	}
	runner := newWorkspaceAnalysisReviewRunner(t, &workspaceAnalysisPlanModel{}, repository)
	_, err := runner.Run(context.Background(), request)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid {
		t.Fatalf("Run error = %#v", err)
	}
	if repository.loadCalls != 1 {
		t.Fatalf("load calls = %d", repository.loadCalls)
	}
}

func TestWorkspaceAnalysisReviewRunnerRejectsSemanticReplayDrift(t *testing.T) {
	request := workspaceAnalysisReviewRequest(t)
	repository := &workspaceAnalysisReviewRepository{}
	repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
		authorized, result := workspaceAnalysisSuccessfulReviewReplay(t, command, request.Candidate, true)
		review, err := domain.DecodeFaithfulnessReview(result.Document, domain.DefaultDecodeLimits())
		if err != nil {
			t.Fatal(err)
		}
		review.Payload.Items[0].CitationIDs = []string{"E2"}
		document, err := json.Marshal(review)
		if err != nil {
			t.Fatal(err)
		}
		result.Document, result.DocumentHash, result.DocumentBytes = document, workspaceAnalysisSHA256(document), int64(len(document))
		authorized.Call.ResponseHash, authorized.Call.ResponseBytes = result.DocumentHash, result.DocumentBytes
		repository.loadedResult = result
		return authorized, nil
	}
	runner := newWorkspaceAnalysisReviewRunner(t, &workspaceAnalysisPlanModel{}, repository)
	_, err := runner.Run(context.Background(), request)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid {
		t.Fatalf("Run error = %#v", err)
	}
	if repository.loadCalls != 1 {
		t.Fatalf("load calls = %d", repository.loadCalls)
	}
}

func TestWorkspaceAnalysisReviewRunnerRejectsInvalidEvidenceBeforeAuthorization(t *testing.T) {
	tests := map[string][]WorkspaceAnalysisReviewEvidence{
		"missing":         {},
		"wrong ref":       {{EvidenceRef: "E2", Excerpt: "bounded evidence"}},
		"whitespace only": {{EvidenceRef: "E1", Excerpt: " \n\t"}},
		"nul":             {{EvidenceRef: "E1", Excerpt: "bounded\x00evidence"}},
		"oversize":        {{EvidenceRef: "E1", Excerpt: strings.Repeat("x", workspaceAnalysisReviewMaxEvidenceBytes+1)}},
	}
	for name, evidence := range tests {
		t.Run(name, func(t *testing.T) {
			request := workspaceAnalysisReviewRequest(t)
			request.Evidence = evidence
			repository := &workspaceAnalysisReviewRepository{}
			model := &workspaceAnalysisPlanModel{}
			runner := newWorkspaceAnalysisReviewRunner(t, model, repository)
			if _, err := runner.Run(context.Background(), request); err == nil {
				t.Fatal("invalid evidence unexpectedly accepted")
			}
			if repository.authorizeCalls != 0 || model.CallCount() != 0 {
				t.Fatalf("calls = authorize:%d provider:%d", repository.authorizeCalls, model.CallCount())
			}
		})
	}
}

type workspaceAnalysisReviewRepository struct {
	authorize         func(AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error)
	finalizeResult    func(FinalizeWorkspaceAnalysisModelResultCommand) (WorkspaceAnalysisModelMutationResult, error)
	finalizeCall      func(FinalizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelMutationResult, error)
	loadedResult      domain.WorkspaceAnalysisModelResult
	loadErr           error
	finalizeCallErr   error
	finalizeResultErr error
	events            *[]string

	authorizeCalls         int
	finalizeResultCalls    int
	finalizeCallCalls      int
	loadCalls              int
	authorizedCommands     []AuthorizeWorkspaceAnalysisModelCallCommand
	finalizeResultCommands []FinalizeWorkspaceAnalysisModelResultCommand
	finalizeCallCommands   []FinalizeWorkspaceAnalysisModelCallCommand
}

func (repository *workspaceAnalysisReviewRepository) AuthorizeWorkspaceAnalysisModelCall(
	_ context.Context,
	command AuthorizeWorkspaceAnalysisModelCallCommand,
) (WorkspaceAnalysisModelAuthorizationResult, error) {
	repository.authorizeCalls++
	repository.authorizedCommands = append(repository.authorizedCommands, command)
	repository.record("authorize")
	if repository.authorize != nil {
		return repository.authorize(command)
	}
	return WorkspaceAnalysisModelAuthorizationResult{
		Run: command.Run, Call: command.Call, OperationID: command.OperationID,
		ReservationID: command.ReservationID, Disposition: WorkspaceAnalysisModelAuthorizationCreated,
	}, nil
}

func (repository *workspaceAnalysisReviewRepository) FinalizeWorkspaceAnalysisModelCall(
	_ context.Context,
	command FinalizeWorkspaceAnalysisModelCallCommand,
) (WorkspaceAnalysisModelMutationResult, error) {
	repository.finalizeCallCalls++
	repository.finalizeCallCommands = append(repository.finalizeCallCommands, command)
	repository.record("finalize_call")
	if repository.finalizeCall != nil {
		return repository.finalizeCall(command)
	}
	if repository.finalizeCallErr != nil {
		return WorkspaceAnalysisModelMutationResult{}, repository.finalizeCallErr
	}
	return WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call, OperationID: command.OperationID, ReservationID: command.ReservationID,
	}, nil
}

func (repository *workspaceAnalysisReviewRepository) FinalizeWorkspaceAnalysisModelResult(
	_ context.Context,
	command FinalizeWorkspaceAnalysisModelResultCommand,
) (WorkspaceAnalysisModelMutationResult, error) {
	repository.finalizeResultCalls++
	repository.finalizeResultCommands = append(repository.finalizeResultCommands, command)
	repository.record("finalize_result")
	if repository.finalizeResult != nil {
		return repository.finalizeResult(command)
	}
	if repository.finalizeResultErr != nil {
		return WorkspaceAnalysisModelMutationResult{}, repository.finalizeResultErr
	}
	result := command.Result
	return WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call, Result: &result,
		OperationID: command.OperationID, ReservationID: command.ReservationID,
	}, nil
}

func (repository *workspaceAnalysisReviewRepository) FinalizeWorkspaceAnalysisModelCandidate(
	context.Context,
	FinalizeWorkspaceAnalysisModelCandidateCommand,
) (WorkspaceAnalysisModelMutationResult, error) {
	return WorkspaceAnalysisModelMutationResult{}, errors.New("unexpected candidate finalization")
}

func (repository *workspaceAnalysisReviewRepository) LoadWorkspaceAnalysisModelResult(
	_ context.Context,
	_ WorkspaceAnalysisModelResultQuery,
) (domain.WorkspaceAnalysisModelResult, error) {
	repository.loadCalls++
	repository.record("load")
	if repository.loadErr != nil {
		return domain.WorkspaceAnalysisModelResult{}, repository.loadErr
	}
	result := repository.loadedResult
	result.Document = append(json.RawMessage(nil), repository.loadedResult.Document...)
	return result, nil
}

func (*workspaceAnalysisReviewRepository) LoadWorkspaceAnalysisCandidate(
	context.Context,
	WorkspaceAnalysisCandidateQuery,
) (domain.WorkspaceAnalysisCandidate, error) {
	return domain.WorkspaceAnalysisCandidate{}, errors.New("unexpected candidate load")
}

func (repository *workspaceAnalysisReviewRepository) record(event string) {
	if repository.events != nil {
		*repository.events = append(*repository.events, event)
	}
}

func newWorkspaceAnalysisReviewRunner(
	t *testing.T,
	model ChatModel,
	repository WorkspaceAnalysisModelOperationRepository,
) *WorkspaceAnalysisReviewRunner {
	t.Helper()
	catalog, _, _, _ := faithfulnessCatalog(t)
	runner, err := NewWorkspaceAnalysisReviewRunner(WorkspaceAnalysisReviewRunnerDependencies{
		Model: model, Catalog: catalog, Repository: repository,
		IDs: &workspaceAnalysisPlanIDGenerator{}, Clock: foundation.FixedClock{Value: workspaceAnalysisPlanNow()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func workspaceAnalysisReviewRequest(t *testing.T) WorkspaceAnalysisReviewRequest {
	t.Helper()
	return WorkspaceAnalysisReviewRequest{
		Identity: WorkspaceAnalysisModelExecutionIdentity{
			WorkspaceID: workspaceAnalysisPlanTestID(1), DefinitionID: workspaceAnalysisPlanTestID(2),
			DefinitionVersion: 1, DefinitionHash: hashHex('a'), WorkflowRunID: workspaceAnalysisPlanTestID(3),
			NodeKey: domain.WorkspaceAnalysisOperationNodeReviewPublish, NodeRunID: workspaceAnalysisPlanTestID(4),
			NodeAttemptID: workspaceAnalysisPlanTestID(5), LeaseOwner: "worker-review", LeaseFence: 1,
		},
		AnalysisRunID: workspaceAnalysisPlanTestID(6), Candidate: workspaceAnalysisReviewCandidate(t),
		Evidence:   []WorkspaceAnalysisReviewEvidence{{EvidenceRef: "E1", Excerpt: "bounded evidence"}},
		Retrieval:  domain.RetrievalRef{IndexVersionID: workspaceAnalysisPlanTestID(7)},
		ProfileRef: domain.ModelProfileRef{ID: "review", Version: "v1"},
		PromptRef:  domain.PromptRef{ID: "faithfulness-review", Version: "v1"},
	}
}

func workspaceAnalysisReviewCandidate(t *testing.T) domain.WorkspaceAnalysisCandidate {
	t.Helper()
	result := domain.WorkspaceAnalysisCandidateResult{
		ResultType: domain.ResultTypeWorkspaceAnalysisCandidate, SchemaID: domain.WorkspaceAnalysisCandidateSchemaID,
		SchemaVersion: "1", ModelRunRef: workspaceAnalysisPlanTestID(24),
		Payload: domain.WorkspaceAnalysisCandidatePayload{
			AnswerMarkdown: "bounded answer [E1]", CitationRefs: []string{"E1"}, ProposalSuggestion: nil,
		},
	}
	document, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	candidate := domain.WorkspaceAnalysisCandidate{
		ID: workspaceAnalysisPlanTestID(20), WorkspaceID: workspaceAnalysisPlanTestID(1),
		AnalysisRunID: workspaceAnalysisPlanTestID(6), AnswerID: workspaceAnalysisPlanTestID(21),
		SynthesisOperationID: workspaceAnalysisPlanTestID(22), NodeAttemptID: workspaceAnalysisPlanTestID(23),
		SynthesisModelRunID: result.ModelRunRef, SchemaID: result.SchemaID, SchemaVersion: 1,
		Document: document, DocumentHash: workspaceAnalysisSHA256(document), DocumentBytes: int64(len(document)),
		CreatedAt: workspaceAnalysisPlanNow().Add(-time.Minute),
	}
	if err := domain.ValidateWorkspaceAnalysisCandidate(candidate); err != nil {
		t.Fatalf("candidate: %v", err)
	}
	return candidate
}

func workspaceAnalysisProviderReview(
	t *testing.T,
	candidate domain.WorkspaceAnalysisCandidate,
	passed bool,
) []byte {
	t.Helper()
	document, err := json.Marshal(workspaceAnalysisProviderReviewResult(candidate, passed))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func workspaceAnalysisProviderReviewResult(
	candidate domain.WorkspaceAnalysisCandidate,
	passed bool,
) domain.FaithfulnessReviewResult {
	verdict := domain.FaithfulnessSupported
	if !passed {
		verdict = domain.FaithfulnessUnsupported
	}
	return domain.FaithfulnessReviewResult{
		ResultType: domain.ResultTypeFaithfulnessReview, SchemaID: domain.FaithfulnessReviewSchemaID,
		SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: candidate.SynthesisModelRunID,
		Payload: domain.FaithfulnessReviewPayload{
			Passed: passed,
			Items: []domain.FaithfulnessReviewItem{{
				AssertionID: workspaceAnalysisReviewConclusionTargetID, Verdict: verdict,
				CitationIDs: []string{"E1"}, Reason: "checked against E1",
			}},
			Summary: "bounded faithfulness decision",
		},
	}
}

func workspaceAnalysisSuccessfulReviewReplay(
	t *testing.T,
	command AuthorizeWorkspaceAnalysisModelCallCommand,
	candidate domain.WorkspaceAnalysisCandidate,
	passed bool,
) (WorkspaceAnalysisModelAuthorizationResult, domain.WorkspaceAnalysisModelResult) {
	t.Helper()
	provider := workspaceAnalysisProviderReviewResult(candidate, passed)
	review, document, err := composeCanonicalWorkspaceAnalysisReview(provider, command.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := command.Call.StartedAt.Add(time.Second)
	call := command.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, workspaceAnalysisSHA256(document), int64(len(document))
	call.Usage = domain.TokenUsage{InputTokens: 19, OutputTokens: 6, TotalTokens: 25}
	call.LatencyMillis, call.Version, call.CompletedAt = 1000, 2, &completedAt
	run := command.Run
	run.Status, run.FinalResultType, run.Version = domain.ModelRunSucceeded, domain.ResultTypeFaithfulnessReview, 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	candidateID := candidate.ID
	result := domain.WorkspaceAnalysisModelResult{
		ID: workspaceAnalysisPlanTestID(900), WorkspaceID: command.Identity.WorkspaceID,
		AnalysisRunID: command.OperationKey.AnalysisRunID, OperationID: command.OperationID,
		NodeAttemptID: command.Run.NodeAttemptID, ModelRunID: command.Run.ID, ModelCallID: command.Call.ID,
		OperationKind: domain.WorkspaceAnalysisOperationFaithfulnessReview, Schema: command.Run.Schema,
		SubjectCandidateID: &candidateID, SubjectCandidateHash: candidate.DocumentHash,
		Document: document, DocumentHash: call.ResponseHash, DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	if err := review.Validate(); err != nil {
		t.Fatal(err)
	}
	return WorkspaceAnalysisModelAuthorizationResult{
		Run: run, Call: call, OperationID: command.OperationID, ReservationID: command.ReservationID,
		Disposition: WorkspaceAnalysisModelAuthorizationReuseResult,
	}, result
}

func workspaceAnalysisReviewModelRef() domain.ModelRef {
	return domain.ModelRef{AdapterName: "fake", AdapterVersion: "v1", ModelID: "review-model", ModelVersion: "v1"}
}

func (repository *workspaceAnalysisReviewRepository) String() string {
	return fmt.Sprintf("workspaceAnalysisReviewRepository{authorize:%d finalize:%d/%d load:%d}",
		repository.authorizeCalls, repository.finalizeCallCalls, repository.finalizeResultCalls, repository.loadCalls)
}
