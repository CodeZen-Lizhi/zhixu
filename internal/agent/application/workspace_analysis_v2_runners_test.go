package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisV2SynthesisPreservesGlobalRefsAndReplays(t *testing.T) {
	request := workspaceAnalysisSynthesisRequest()
	request.Identity.DefinitionVersion = 2
	request.PromptRef.Version = "v2"
	request.Input = []byte(`{"schema_version":2,"question":"policy?","git_status":null,"evidence":[{"evidence_ref":"E17","excerpt":"bounded policy"}]}`)
	request.AllowedEvidenceRefs = []string{"E17", "E32"}
	provider := domain.WorkspaceAnalysisCandidateProviderResult{ResultType: domain.ResultTypeWorkspaceAnalysisCandidate, SchemaID: domain.WorkspaceAnalysisCandidateSchemaID, SchemaVersion: "2",
		Payload: domain.WorkspaceAnalysisCandidatePayload{AnswerMarkdown: "Policy [E17].", CitationRefs: []string{"E17"}}}
	raw, _ := json.Marshal(provider)
	runtime := &workspaceAnalysisSynthesisRuntime{response: ChatResponse{Model: workspaceAnalysisPlanModelRef(), Content: raw, Usage: domain.TokenUsage{InputTokens: 5, OutputTokens: 4, TotalTokens: 9}}}
	repository := &workspaceAnalysisSynthesisRepository{}
	drafts := &workspaceAnalysisSynthesisDraftCoordinator{}
	runner, err := NewWorkspaceAnalysisSynthesisRunner(WorkspaceAnalysisSynthesisRunnerDependencies{Runtime: runtime,
		Catalog:    workspaceAnalysisV2TestCatalog(t, request.PromptRef, request.ProfileRef, domain.SchemaRef{ID: domain.WorkspaceAnalysisCandidateSchemaID, Version: "2"}),
		Repository: repository, Drafts: drafts, IDs: &workspaceAnalysisPlanIDGenerator{}, Clock: foundation.FixedClock{Value: workspaceAnalysisPlanNow()}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidate.SchemaVersion != 2 || result.Run.Schema.Version != "2" || !reflect.DeepEqual(result.CandidateResult.Payload.CitationRefs, []string{"E17"}) {
		t.Fatal("v2 synthesis reference or version drifted")
	}
	repository.loadedCandidate = result.Candidate
	drafts.loaded = result.Draft
	repository.authorize = func(AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
		return WorkspaceAnalysisModelAuthorizationResult{Run: result.Run, Call: result.Call, OperationID: repository.authorizedCommands[0].OperationID,
			ReservationID: repository.authorizedCommands[0].ReservationID, Disposition: WorkspaceAnalysisModelAuthorizationReuseResult}, nil
	}
	request.Identity.NodeAttemptID = workspaceAnalysisPlanTestID(99)
	request.AttemptNo, request.Identity.LeaseFence = 2, 2
	replayed, err := runner.Run(context.Background(), request)
	if err != nil || !replayed.Replayed || runtime.CallCount() != 1 {
		t.Fatalf("v2 synthesis replay repeated work: %v", err)
	}
}

func TestWorkspaceAnalysisV2ReviewAcceptsGlobalRefWithoutRelaxingV1(t *testing.T) {
	request := workspaceAnalysisReviewRequest(t)
	request.Identity.DefinitionVersion = 2
	result, err := domain.DecodeWorkspaceAnalysisCandidate(request.Candidate.Document, domain.DefaultDecodeLimits())
	if err != nil {
		t.Fatal(err)
	}
	result.SchemaVersion = "2"
	result.Payload.AnswerMarkdown = "bounded answer [E17]"
	result.Payload.CitationRefs = []string{"E17"}
	raw, _ := json.Marshal(result)
	request.Candidate.SchemaVersion = 2
	request.Candidate.Document, request.Candidate.DocumentHash, request.Candidate.DocumentBytes = raw, workspaceAnalysisSHA256(raw), int64(len(raw))
	request.Evidence = []WorkspaceAnalysisReviewEvidence{{EvidenceRef: "E17", Excerpt: "bounded evidence"}}
	if err := validateWorkspaceAnalysisReviewRequest(request); err != nil {
		t.Fatal(err)
	}
	input, err := canonicalWorkspaceAnalysisReviewInput(request.Candidate, request.Evidence)
	if err != nil || !bytes.Contains(input, []byte(workspaceAnalysisReviewInputSchemaVersionV2)) {
		t.Fatalf("v2 review input: %v", err)
	}
	request.Identity.DefinitionVersion = 1
	if validateWorkspaceAnalysisReviewRequest(request) == nil {
		t.Fatal("v1 review accepted a v2 candidate")
	}
	if workspaceAnalysisAllowedEvidenceRefsForVersion([]string{"E17"}, 1) || !workspaceAnalysisAllowedEvidenceRefsForVersion([]string{"E17", "E32"}, 2) ||
		workspaceAnalysisAllowedEvidenceRefsForVersion([]string{"E32", "E17"}, 2) {
		t.Fatal("versioned evidence order was relaxed")
	}
}

func TestWorkspaceAnalysisDecisionRunnerSharesAttemptRunAndResetsCallNumberOnReplacement(t *testing.T) {
	runner, repository, request := workspaceAnalysisDecisionTestRunner(t)
	caller, err := runner.BindWorkspaceAnalysisLoop(request)
	if err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	invoke := func(context.Context) (WorkspaceAnalysisDecisionProviderResult, error) {
		providerCalls++
		return WorkspaceAnalysisDecisionProviderResult{Decision: domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionGitStatus}, Usage: domain.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
	}
	first, err := caller.CallWorkspaceAnalysisDecision(context.Background(), WorkspaceAnalysisLoopModelCall{Ordinal: 1, RequestDocument: []byte(`{"step":1}`), Invoke: invoke})
	if err != nil {
		t.Fatal(err)
	}
	second, err := caller.CallWorkspaceAnalysisDecision(context.Background(), WorkspaceAnalysisLoopModelCall{Ordinal: 2, RequestDocument: []byte(`{"step":2}`), Invoke: invoke})
	if err != nil {
		t.Fatal(err)
	}
	if first.Run.ID != second.Run.ID || second.Call.CallNo != 2 || repository.authorizedCommands[1].Run.Version <= 1 {
		t.Fatal("calls did not reuse the persisted running model run/version")
	}
	request.Identity.NodeAttemptID = workspaceAnalysisPlanTestID(90)
	request.Identity.LeaseFence = 2
	replacement, err := runner.BindWorkspaceAnalysisLoop(request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := replacement.CallWorkspaceAnalysisDecision(context.Background(), WorkspaceAnalysisLoopModelCall{Ordinal: 1, RequestDocument: []byte(`{"step":1}`), Invoke: invoke})
	if err != nil || !replay.Replayed || providerCalls != 2 {
		t.Fatalf("replacement replay: %v", err)
	}
	third, err := replacement.CallWorkspaceAnalysisDecision(context.Background(), WorkspaceAnalysisLoopModelCall{Ordinal: 3, RequestDocument: []byte(`{"step":3}`), Invoke: func(context.Context) (WorkspaceAnalysisDecisionProviderResult, error) {
		providerCalls++
		return WorkspaceAnalysisDecisionProviderResult{Decision: domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionFinish}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if third.Call.CallNo != 1 || third.Decision.Ordinal != 3 || third.Run.ID == first.Run.ID || third.Run.NodeAttemptID != request.Identity.NodeAttemptID {
		t.Fatal("global ordinal was confused with the replacement run call number")
	}
}

func TestWorkspaceAnalysisDecisionRunnerRecoversCommitLossWithoutProviderRetry(t *testing.T) {
	runner, repository, request := workspaceAnalysisDecisionTestRunner(t)
	caller, err := runner.BindWorkspaceAnalysisLoop(request)
	if err != nil {
		t.Fatal(err)
	}
	repository.loseCommit = true
	calls := 0
	result, err := caller.CallWorkspaceAnalysisDecision(context.Background(), WorkspaceAnalysisLoopModelCall{Ordinal: 1, RequestDocument: []byte(`{"step":1}`), Invoke: func(context.Context) (WorkspaceAnalysisDecisionProviderResult, error) {
		calls++
		return WorkspaceAnalysisDecisionProviderResult{Decision: domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionFinish}}, nil
	}})
	if err != nil || !result.Replayed || calls != 1 || repository.completions != 1 {
		t.Fatalf("commit loss was not exactly recovered: %v", err)
	}
}

func TestWorkspaceAnalysisDecisionRunnerNeverInvokesAfterDurableDenialOrReconcile(t *testing.T) {
	for _, disposition := range []WorkspaceAnalysisModelAuthorizationDisposition{WorkspaceAnalysisModelAuthorizationReconcile, "DENIED"} {
		t.Run(string(disposition), func(t *testing.T) {
			runner, repository, request := workspaceAnalysisDecisionTestRunner(t)
			repository.disposition = disposition
			caller, err := runner.BindWorkspaceAnalysisLoop(request)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, err = caller.CallWorkspaceAnalysisDecision(context.Background(), WorkspaceAnalysisLoopModelCall{Ordinal: 1, RequestDocument: []byte(`{"step":1}`), Invoke: func(context.Context) (WorkspaceAnalysisDecisionProviderResult, error) {
				calls++
				return WorkspaceAnalysisDecisionProviderResult{}, nil
			}})
			if err == nil || calls != 0 {
				t.Fatal("non-created authorization reached Provider")
			}
			if disposition == "DENIED" {
				if _, found := WorkspaceAnalysisAdmissionDenialFromError(err); !found {
					t.Fatal("durable denial was lost in the error chain")
				}
			}
		})
	}
}

func TestWorkspaceAnalysisDecisionRunnerReusesPendingDenialOperationOnReplacement(t *testing.T) {
	runner, repository, request := workspaceAnalysisDecisionTestRunner(t)
	raw := []byte(`{"step":1}`)
	pendingID := workspaceAnalysisPlanTestID(980)
	repository.journal.Entries = []WorkspaceAnalysisJournalEntry{{Sequence: 1, Operation: domain.WorkspaceAnalysisOperation{
		ID: pendingID, AnalysisRunID: request.AnalysisRunID, NodeKey: domain.WorkspaceAnalysisOperationNodeDecideNext,
		Kind: domain.WorkspaceAnalysisOperationDecision, Ordinal: 1, RequestHash: workspaceAnalysisSHA256(raw),
		Status: domain.WorkspaceAnalysisOperationPending, Version: 1, CreatedAt: workspaceAnalysisPlanNow(), UpdatedAt: workspaceAnalysisPlanNow(),
	}}}
	repository.disposition = "DENIED"
	request.Identity.NodeAttemptID, request.Identity.LeaseFence = workspaceAnalysisPlanTestID(99), 2
	caller, err := runner.BindWorkspaceAnalysisLoop(request)
	if err != nil {
		t.Fatal(err)
	}
	invoked := false
	_, err = caller.CallWorkspaceAnalysisDecision(context.Background(), WorkspaceAnalysisLoopModelCall{Ordinal: 1, RequestDocument: raw,
		Invoke: func(context.Context) (WorkspaceAnalysisDecisionProviderResult, error) {
			invoked = true
			return WorkspaceAnalysisDecisionProviderResult{}, nil
		}})
	denial, found := WorkspaceAnalysisAdmissionDenialFromError(err)
	if !found || denial.OperationID != pendingID || invoked || len(repository.authorizedCommands) != 1 || repository.authorizedCommands[0].OperationID != pendingID {
		t.Fatalf("replacement did not retain pending denial identity: %v", err)
	}
}

func TestWorkspaceAnalysisDecisionRunnerTreatsUnusableUsageAsUnknown(t *testing.T) {
	runner, repository, request := workspaceAnalysisDecisionTestRunner(t)
	caller, err := runner.BindWorkspaceAnalysisLoop(request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = caller.CallWorkspaceAnalysisDecision(context.Background(), WorkspaceAnalysisLoopModelCall{Ordinal: 1, RequestDocument: []byte(`{"step":1}`),
		Invoke: func(context.Context) (WorkspaceAnalysisDecisionProviderResult, error) {
			return WorkspaceAnalysisDecisionProviderResult{Decision: domain.WorkspaceAnalysisDecision{Action: domain.WorkspaceAnalysisDecisionFinish}, Usage: domain.TokenUsage{InputTokens: -1}}, nil
		}})
	if err == nil || repository.completions != 1 || len(repository.journal.Entries) != 1 {
		t.Fatal("unknown usage did not settle exactly once")
	}
	entry := repository.journal.Entries[0]
	if entry.Decision != nil || entry.Operation.Status != domain.WorkspaceAnalysisOperationUnknown || entry.ModelCall.Status != domain.ModelCallUnknown || entry.ModelCall.Usage != (domain.TokenUsage{}) {
		t.Fatal("unusable usage falsely settled an accepted decision")
	}
}

func workspaceAnalysisV2TestCatalog(t *testing.T, promptRef domain.PromptRef, profileRef domain.ModelProfileRef, schemaRef domain.SchemaRef) *RuntimeCatalog {
	t.Helper()
	catalog := NewRuntimeCatalog()
	for _, err := range []error{
		catalog.RegisterPrompt(PromptDefinition{Ref: promptRef, System: "approved read-only policy", InitialInstruction: "return one action", RepairInstruction: "unused", ReducedInstruction: "unused"}),
		catalog.RegisterProfile(ModelProfile{Ref: profileRef, Model: workspaceAnalysisPlanModelRef(), Timeout: time.Minute, MaxOutputTokens: 4096}),
		catalog.RegisterSchema(SchemaDefinition{Ref: schemaRef, JSONSchema: []byte(`{"type":"object"}`), Decode: func(raw []byte) (json.RawMessage, error) { return append(json.RawMessage(nil), raw...), nil }}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	return catalog
}

type workspaceAnalysisDecisionRepositoryFake struct {
	workspaceAnalysisPlanRepository
	journal     WorkspaceAnalysisJournalSnapshot
	commands    map[int]AuthorizeWorkspaceAnalysisModelCallCommand
	disposition WorkspaceAnalysisModelAuthorizationDisposition
	loseCommit  bool
	completions int
}

func (r *workspaceAnalysisDecisionRepositoryFake) LoadWorkspaceAnalysisJournal(context.Context, WorkspaceAnalysisJournalQuery) (WorkspaceAnalysisJournalSnapshot, error) {
	return r.journal, nil
}

func (r *workspaceAnalysisDecisionRepositoryFake) AuthorizeWorkspaceAnalysisModelCall(_ context.Context, command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
	r.authorizedCommands = append(r.authorizedCommands, command)
	if r.disposition == "DENIED" {
		return WorkspaceAnalysisModelAuthorizationResult{}, &WorkspaceAnalysisAdmissionDenial{OperationID: command.OperationID, Reason: domain.WorkspaceAnalysisRunBudgetExhausted, Requested: domain.WorkspaceAnalysisBudgetAmount{ModelCalls: 1, InputTokens: 65536, OutputTokens: 512}}
	}
	for _, entry := range r.journal.Entries {
		if entry.Operation.Ordinal == command.OperationKey.Ordinal {
			return WorkspaceAnalysisModelAuthorizationResult{Run: *entry.ModelRun, Call: *entry.ModelCall, OperationID: entry.Operation.ID, ReservationID: *entry.Operation.BudgetReservationID, Disposition: WorkspaceAnalysisModelAuthorizationReuseResult}, nil
		}
	}
	r.commands[command.OperationKey.Ordinal] = command
	disposition := r.disposition
	if disposition == "" {
		disposition = WorkspaceAnalysisModelAuthorizationCreated
	}
	return WorkspaceAnalysisModelAuthorizationResult{Run: command.Run, Call: command.Call, OperationID: command.OperationID, ReservationID: command.ReservationID, Disposition: disposition}, nil
}

func (r *workspaceAnalysisDecisionRepositoryFake) FinalizeWorkspaceAnalysisDecision(_ context.Context, command FinalizeWorkspaceAnalysisDecisionCommand) (WorkspaceAnalysisDecisionMutationResult, error) {
	r.completions++
	started := r.commands[command.OperationKey.Ordinal]
	call, run := started.Call, started.Run
	now := call.StartedAt.Add(time.Duration(command.OperationKey.Ordinal) * time.Millisecond)
	call.Status, call.Version, call.CompletedAt = command.Status, 2, &now
	call.Usage, call.LatencyMillis, call.ErrorCode = command.Usage, command.LatencyMillis, command.ErrorCode
	run.Version++
	run.UpdatedAt = now
	var receipt *domain.WorkspaceAnalysisDecisionReceipt
	if command.Decision != nil {
		raw, _ := command.Decision.Canonical()
		call.ResponseHash, call.ResponseBytes = workspaceAnalysisSHA256(raw), int64(len(raw))
		receipt = &domain.WorkspaceAnalysisDecisionReceipt{ID: command.ReceiptID, WorkspaceID: command.Identity.WorkspaceID, AnalysisRunID: command.OperationKey.AnalysisRunID,
			OperationID: command.OperationID, NodeAttemptID: run.NodeAttemptID, ModelRunID: run.ID, ModelCallID: call.ID, Ordinal: command.OperationKey.Ordinal,
			Decision: *command.Decision, DocumentHash: call.ResponseHash, DocumentBytes: call.ResponseBytes, CreatedAt: now}
		if command.Decision.Action == domain.WorkspaceAnalysisDecisionFinish {
			run.Status = domain.ModelRunSucceeded
			run.CompletedAt = &now
			run.FinalResultType = domain.ResultTypeWorkspaceAnalysisDecision
		}
	} else {
		run.Status = domain.ModelRunFailed
		if command.Status == domain.ModelCallUnknown {
			run.Status = domain.ModelRunUnknown
		}
		run.CompletedAt = &now
		run.FinalErrorCode = command.ErrorCode
	}
	op := domain.WorkspaceAnalysisOperation{ID: command.OperationID, AnalysisRunID: command.OperationKey.AnalysisRunID, NodeKey: command.OperationKey.NodeKey, Kind: command.OperationKey.Kind,
		Ordinal: command.OperationKey.Ordinal, RequestHash: call.RequestHash, Status: domain.WorkspaceAnalysisOperationSucceeded,
		FirstNodeAttemptID: &run.NodeAttemptID, LatestNodeAttemptID: &run.NodeAttemptID, BudgetReservationID: &started.ReservationID,
		Call: &domain.WorkspaceAnalysisOperationCallRef{Kind: domain.WorkspaceAnalysisOperationCallModel, ID: call.ID}, Version: 3, CreatedAt: call.StartedAt, UpdatedAt: now, StartedAt: &call.StartedAt, CompletedAt: &now}
	if receipt != nil {
		op.Result = &domain.WorkspaceAnalysisOperationResultRef{Kind: domain.WorkspaceAnalysisOperationResultDecision, ID: receipt.ID, Hash: receipt.DocumentHash}
	} else {
		op.Status = domain.WorkspaceAnalysisOperationFailed
		if command.Status == domain.ModelCallUnknown {
			op.Status = domain.WorkspaceAnalysisOperationUnknown
		}
		op.ErrorCode = command.ErrorCode
	}
	for index := range r.journal.Entries {
		if r.journal.Entries[index].ModelRun.ID == run.ID {
			copy := run
			r.journal.Entries[index].ModelRun = &copy
		}
	}
	r.journal.Entries = append(r.journal.Entries, WorkspaceAnalysisJournalEntry{Sequence: int64(len(r.journal.Entries) + 1), Operation: op, ModelRun: &run, ModelCall: &call, Decision: receipt})
	result := WorkspaceAnalysisDecisionMutationResult{Run: run, Call: call, Operation: op, Decision: receipt}
	if r.loseCommit {
		return WorkspaceAnalysisDecisionMutationResult{}, errors.New("commit acknowledgement lost")
	}
	return result, nil
}

func workspaceAnalysisDecisionTestRunner(t *testing.T) (*WorkspaceAnalysisDecisionRunner, *workspaceAnalysisDecisionRepositoryFake, WorkspaceAnalysisDecisionRunRequest) {
	t.Helper()
	base := workspaceAnalysisPlanRequest()
	base.Identity.DefinitionVersion = 2
	base.Identity.NodeKey = domain.WorkspaceAnalysisOperationNodeDecideNext
	request := WorkspaceAnalysisDecisionRunRequest{Identity: base.Identity, AnalysisRunID: base.AnalysisRunID, ProfileRef: base.ProfileRef, PromptRef: domain.PromptRef{ID: "workspace-analysis-decision", Version: "v2"}}
	config := workspaceAnalysisRunStartTestConfig()
	deadlines, err := domain.DeriveWorkspaceAnalysisV2Deadlines(config.Timeouts)
	if err != nil {
		t.Fatal(err)
	}
	now := workspaceAnalysisPlanNow().Add(-time.Minute)
	run := domain.WorkspaceAnalysisRun{ID: request.AnalysisRunID, WorkspaceID: request.Identity.WorkspaceID, WorkflowRunID: request.Identity.WorkflowRunID,
		ConversationID: workspaceAnalysisPlanTestID(30), QuestionID: workspaceAnalysisPlanTestID(31), AnswerID: workspaceAnalysisPlanTestID(32), DefinitionKey: "workspace-analysis", DefinitionVersion: 2,
		DefinitionHash: request.Identity.DefinitionHash, ToolCatalogHash: hashHex('b'), PolicyVersion: 2, DeadlineAt: now.Add(deadlines.RunDeadline()), Timeouts: config.Timeouts,
		Limits: domain.WorkspaceAnalysisBudgetLimits{Nodes: 4, ToolConcurrency: 1, Amount: domain.WorkspaceAnalysisBudgetAmount{ModelCalls: 14, ToolCalls: 13, SourceReads: 8, InputTokens: domain.WorkspaceAnalysisV2MaxRunInputTokens, OutputTokens: domain.WorkspaceAnalysisV2MaxRunOutputTokens}},
		Status: domain.WorkspaceAnalysisRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := domain.ValidateWorkspaceAnalysisRun(run); err != nil {
		t.Fatal(err)
	}
	repository := &workspaceAnalysisDecisionRepositoryFake{journal: WorkspaceAnalysisJournalSnapshot{Run: run}, commands: map[int]AuthorizeWorkspaceAnalysisModelCallCommand{}}
	runner, err := NewWorkspaceAnalysisDecisionRunner(WorkspaceAnalysisDecisionRunnerDependencies{Catalog: workspaceAnalysisV2TestCatalog(t, request.PromptRef, request.ProfileRef, domain.SchemaRef{ID: domain.WorkspaceAnalysisDecisionSchemaID, Version: domain.WorkspaceAnalysisDecisionSchemaVersion}),
		Repository: repository, IDs: &workspaceAnalysisPlanIDGenerator{}, Clock: foundation.FixedClock{Value: workspaceAnalysisPlanNow()}})
	if err != nil {
		t.Fatal(err)
	}
	return runner, repository, request
}
