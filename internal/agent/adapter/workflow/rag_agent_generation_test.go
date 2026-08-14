package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestRAGAgentGenerationBindsAgentToolFreeStreamAndMetadata(t *testing.T) {
	finalText := "Final markdown answer."
	agent := &ragAgentGenerationAgentFake{result: agentapplication.AgentRunResult{FinalText: "internal synthesis only"}}
	stream := &ragAgentGenerationStreamFake{result: agentapplication.AnswerStreamResult{Content: finalText}}
	metadata := &ragAgentGenerationMetadataFake{result: ragAgentGenerationMetadataResult(t, agentdomain.ModelCallInitial)}
	sink := &ragAgentGenerationSinkFake{}
	generation := newRAGAgentGenerationForTest(t, agent, stream, metadata, sink)

	result, err := generation.Generate(context.Background(), ragAgentGenerationRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer == nil || result.Refusal != nil || result.Answer.Payload.Conclusion != finalText {
		t.Fatalf("result=%+v", result)
	}
	if agent.calls != 1 || stream.calls != 1 || metadata.calls != 1 {
		t.Fatalf("agent=%d stream=%d metadata=%d", agent.calls, stream.calls, metadata.calls)
	}
	if agent.request.Prompt != RAGAgentPromptRef() || agent.request.Schema != (agentdomain.SchemaRef{ID: agentdomain.RAGAgentTurnSchemaID, Version: agentdomain.OutputSchemaVersionV1}) ||
		len(agent.request.Tools) != 1 || agent.request.Tools[0].Ref != (toolsdomain.ToolRef{Name: "ReadSource", Version: 2}) ||
		!slices.Equal(agent.request.ReturnDirectlyTools, []string{"ReadSource"}) ||
		agent.request.MaxOutputTokens != 10 {
		t.Fatalf("agent request=%+v", agent.request)
	}
	boundTools, ok := agent.request.ToolInvoker.(*RAGAgentToolBridge)
	if !ok || boundTools.scope == nil || !bytes.Contains(agent.request.Tools[0].InputSchema, []byte(`"evidence_ref"`)) ||
		!bytes.Contains(agent.request.Tools[0].InputSchema, []byte(`"E1"`)) || bytes.Contains(agent.request.Tools[0].InputSchema, []byte(string(testSourceID))) {
		t.Fatalf("agent tools were not bound to short references: request=%+v invoker=%T", agent.request.Tools, agent.request.ToolInvoker)
	}
	if stream.request.Prompt != RAGFinalAnswerPromptRef() || stream.request.Schema != (agentdomain.SchemaRef{ID: agentdomain.RAGAnswerContentSchemaID, Version: agentdomain.OutputSchemaVersionV1}) ||
		strings.Contains(stream.request.Messages[1].Content, finalText) || !strings.Contains(stream.request.Messages[1].Content, agent.result.FinalText) ||
		!strings.Contains(stream.request.Messages[0].Content, "concise is at most two short sentences") ||
		!strings.Contains(stream.request.Messages[0].Content, "Respect output_format and answer_depth") {
		t.Fatalf("stream request=%+v", stream.request)
	}
	providerInput, err := json.Marshal(struct {
		Agent  []agentapplication.AgentMessage
		Stream []agentapplication.AgentMessage
	}{Agent: agent.request.Messages, Stream: stream.request.Messages})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"citation-1", string(testWorkspaceID), string(testIndexID), string(testCandidateID),
		string(testChunkID), string(testSourceID), string(testSpanID), string(testModelRunID),
		"2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z",
	} {
		if bytes.Contains(providerInput, []byte(forbidden)) {
			t.Fatalf("agent or answer input leaked server identity %q: %s", forbidden, providerInput)
		}
	}
	providerMessages := agent.request.Messages[1].Content + "\n" + stream.request.Messages[1].Content
	if !strings.Contains(providerMessages, `"ref":"E1"`) || !strings.Contains(providerMessages, `"ref":"T1"`) {
		t.Fatalf("agent and answer input omitted short-reference context: %s", providerInput)
	}
	if strings.Join(sink.chunks, "") != finalText || strings.Contains(strings.Join(sink.chunks, ""), agent.result.FinalText) {
		t.Fatalf("sink chunks=%q", sink.chunks)
	}
	if metadata.request.PromptRef != RAGAnswerMetadataPromptRef() ||
		metadata.request.SchemaRef != (agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2}) ||
		metadata.request.ReducedSchemaRef != (agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2}) ||
		metadata.request.MaxOutputTokens != metadataOutputTokenBudget(finalText, 1, 0, 1) {
		t.Fatalf("metadata request=%+v", metadata.request)
	}
	var metadataInput ragMetadataTaskInput
	if err := json.Unmarshal(metadata.request.Input, &metadataInput); err != nil {
		t.Fatal(err)
	}
	if metadataInput.FinalAnswer != finalText ||
		len(metadataInput.Evidence) != 1 || metadataInput.Evidence[0].Ref != "E1" || metadataInput.Evidence[0].Excerpt != "Approved deployment evidence." ||
		len(metadataInput.Conflicts) != 0 || len(metadataInput.RelatedTopics) != 1 || metadataInput.RelatedTopics[0].Ref != "T1" ||
		metadataInput.RelatedTopics[0].Name != "Deployment" || !slices.Equal(metadataInput.RelatedTopics[0].EvidenceRefs, []string{"E1"}) {
		t.Fatalf("metadata input=%+v", metadataInput)
	}
	for _, forbidden := range [][]byte{
		[]byte(`"generation_context"`), []byte(`"model_run_ref"`), []byte(`"answer_sha256"`), []byte(`"workspace_id"`), []byte(`"index_version_id"`),
		[]byte(`"chunk_id"`), []byte(`"source_version_id"`), []byte(`"source_span_id"`), []byte(`"topic_id"`), []byte(`"claim_id"`),
	} {
		if bytes.Contains(metadata.request.Input, forbidden) {
			t.Fatalf("metadata input contains server-owned field %q: %s", forbidden, metadata.request.Input)
		}
	}
	if len(result.Answer.Payload.Citations) != 1 || result.Answer.Payload.Citations[0].ID != "citation-1" ||
		len(result.Answer.Payload.RelatedTopics) != 1 || result.Answer.Payload.RelatedTopics[0].TopicID != testCandidateID {
		t.Fatalf("server bindings were not restored: %+v", result.Answer.Payload)
	}
}

func TestRAGAgentGenerationCapsAgentOutputAndReturnsOneReadDirectly(t *testing.T) {
	agent := &ragAgentGenerationAgentFake{result: agentapplication.AgentRunResult{FinalText: "internal synthesis only"}}
	stream := &ragAgentGenerationStreamFake{result: agentapplication.AnswerStreamResult{Content: "Final markdown answer."}}
	metadata := &ragAgentGenerationMetadataFake{result: ragAgentGenerationMetadataResult(t, agentdomain.ModelCallInitial)}
	generation := newRAGAgentGenerationForTestWithMaxOutputTokens(t, agent, stream, metadata, nil, 2048)

	if _, err := generation.Generate(context.Background(), ragAgentGenerationRequest(t)); err != nil {
		t.Fatal(err)
	}
	if agent.request.MaxOutputTokens != 96 {
		t.Fatalf("agent max output tokens=%d want=96", agent.request.MaxOutputTokens)
	}
	if stream.request.MaxOutputTokens != 2048 {
		t.Fatalf("answer max output tokens=%d want=2048", stream.request.MaxOutputTokens)
	}
	if len(agent.request.Messages) < 1 || agent.request.Messages[0].Role != agentapplication.AgentMessageSystem {
		t.Fatalf("agent messages=%+v", agent.request.Messages)
	}
	system := agent.request.Messages[0].Content
	readDirective := "Call exactly one explicitly bound ReadSource@2 tool for one supplied E* evidence reference."
	directDirective := "The runtime returns that tool result directly to the separate final-answer model call."
	if readIndex, directIndex := strings.Index(system, readDirective), strings.Index(system, directDirective); readIndex < 0 || directIndex <= readIndex {
		t.Fatalf("agent system prompt does not require one direct-return ReadSource call: %q", system)
	}
	if !slices.Equal(agent.request.ReturnDirectlyTools, []string{"ReadSource"}) {
		t.Fatalf("return directly tools=%v", agent.request.ReturnDirectlyTools)
	}
}

func TestMetadataOutputTokenBudgetCanExceedSmallResponseProfiles(t *testing.T) {
	budget := metadataOutputTokenBudget(strings.Repeat("a", 10_000), 1, 0, 1)
	if budget <= 2048 || budget > agentapplication.MaxOutputTokens {
		t.Fatalf("metadata output budget=%d", budget)
	}
}

func TestRAGAgentGenerationRejectsWrongMetadataRefsBeforeProvider(t *testing.T) {
	agent := &ragAgentGenerationAgentFake{result: agentapplication.AgentRunResult{FinalText: "unused"}}
	stream := &ragAgentGenerationStreamFake{result: agentapplication.AnswerStreamResult{Content: "unused"}}
	metadata := &ragAgentGenerationMetadataFake{}
	generation := newRAGAgentGenerationForTest(t, agent, stream, metadata, nil)
	request := ragAgentGenerationRequest(t)
	request.PromptRef = RAGAnswerPromptRef()

	if _, err := generation.Generate(context.Background(), request); errorCode(err) != ragAgentGenerationInputCode {
		t.Fatalf("err=%v code=%q", err, errorCode(err))
	}
	if agent.calls != 0 || stream.calls != 0 || metadata.calls != 0 {
		t.Fatalf("provider calls agent=%d stream=%d metadata=%d", agent.calls, stream.calls, metadata.calls)
	}
}

func TestRAGAgentGenerationRejectsUntypedContextBeforeProvider(t *testing.T) {
	agent := &ragAgentGenerationAgentFake{result: agentapplication.AgentRunResult{FinalText: "unused"}}
	stream := &ragAgentGenerationStreamFake{result: agentapplication.AnswerStreamResult{Content: "unused"}}
	metadata := &ragAgentGenerationMetadataFake{}
	generation := newRAGAgentGenerationForTest(t, agent, stream, metadata, nil)
	request := ragAgentGenerationRequest(t)
	request.Context = agentapplication.RAGGenerationContext{
		ModelRunRef: request.ModelRunRef,
		Request:     json.RawMessage(`{"question":"not the complete typed generation context"}`),
	}

	if _, err := generation.Generate(context.Background(), request); errorCode(err) != ragAgentGenerationInputCode {
		t.Fatalf("err=%v code=%q", err, errorCode(err))
	}
	if agent.calls != 0 || stream.calls != 0 || metadata.calls != 0 {
		t.Fatalf("provider calls agent=%d stream=%d metadata=%d", agent.calls, stream.calls, metadata.calls)
	}
}

func TestBuildRAGMetadataContextUsesDeterministicShortRefsWithoutServerIdentities(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	secondCitation := agentdomain.Citation{
		ID: "citation-2", WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID,
		ChunkID: testChunkID2, SourceVersionID: testSourceID2, SourceSpanID: testSpanID2,
	}
	input := agentapplication.RAGGenerationContext{
		ModelRunRef: testModelRunID, Request: json.RawMessage(`{"question":"compare recovery positions"}`),
		Evidence: []agentdomain.Evidence{
			{
				Citation: agentdomain.Citation{
					ID: "citation-1", WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID,
					ChunkID: testChunkID, SourceVersionID: testSourceID, SourceSpanID: testSpanID,
				},
				Excerpt: "Production recovery position.", Eligibility: knowledgedomain.EvidenceEligibleWithConflict,
				ConflictIDs: []foundation.ID{testWorkflowRunID},
			},
			{
				Citation: secondCitation, Excerpt: "Development recovery position.", Eligibility: knowledgedomain.EvidenceEligibleWithConflict,
				ConflictIDs: []foundation.ID{testWorkflowRunID},
			},
		},
		Conflicts: []agentapplication.RAGConflictDisclosure{
			{
				ClaimID: testCandidateID, ConflictIDs: []foundation.ID{testWorkflowRunID}, Applicability: json.RawMessage(`{"environment":"prod"}`),
				UpdatedAt: now, CitationIDs: []string{"citation-1"},
			},
			{
				ClaimID: testExistingID, ConflictIDs: []foundation.ID{testWorkflowRunID}, Applicability: json.RawMessage(`{"environment":"dev"}`),
				UpdatedAt: now, CitationIDs: []string{"citation-2"},
			},
		},
		RelatedTopics: map[foundation.ID]agentapplication.RAGAllowedTopic{
			testExistingID:  {Name: "Recovery", CitationIDs: []string{"citation-2"}},
			testCandidateID: {Name: "Deployment", CitationIDs: []string{"citation-1"}},
		},
	}

	projected, bindings, err := buildRAGMetadataContext(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.Evidence) != 2 || projected.Evidence[0].Ref != "E1" || projected.Evidence[1].Ref != "E2" ||
		len(projected.Conflicts) != 2 || projected.Conflicts[0].Ref != "C1" || projected.Conflicts[1].Ref != "C2" ||
		!slices.Equal(projected.Conflicts[0].EvidenceRefs, []string{"E1"}) || !slices.Equal(projected.Conflicts[1].EvidenceRefs, []string{"E2"}) ||
		len(projected.RelatedTopics) != 2 || projected.RelatedTopics[0].Ref != "T1" || projected.RelatedTopics[0].Name != "Deployment" ||
		projected.RelatedTopics[1].Ref != "T2" || projected.RelatedTopics[1].Name != "Recovery" {
		t.Fatalf("projected=%+v", projected)
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"citation-1", "citation-2", string(testWorkspaceID), string(testIndexID), string(testCandidateID), string(testExistingID),
		string(testChunkID), string(testSourceID), string(testSpanID),
	} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("metadata projection leaked server identity %q: %s", forbidden, encoded)
		}
	}
	metadata := agentdomain.RAGAnswerMetadataResultV2{
		ResultType: agentdomain.ResultTypeRAGAnswerMetadata, SchemaID: agentdomain.RAGAnswerMetadataSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV2,
		Payload: agentdomain.RAGAnswerMetadataPayloadV2{
			Assertions: []agentdomain.RAGAnswerMetadataAssertionV2{{
				ID: "recovery-assertion", Text: "Development recovery is documented.", Kind: agentdomain.AssertionFactual,
				EvidenceRefs: []string{"E2"},
			}},
			ConflictPositions: []agentdomain.RAGAnswerMetadataConflictPositionV2{
				{ConflictRef: "C2", Position: "Use development recovery."},
				{ConflictRef: "C1", Position: "Use production recovery."},
			},
			ConflictSummary: "The positions apply in different environments.", RelatedTopicRefs: []string{"T2"},
			FollowUpQuestions: []string{"Which environment applies?"},
		},
	}
	answer, err := metadata.ComposeRAGAnswerV2(testModelRunID, "Exact streamed answer.", bindings)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Payload.ConflictPositions[0].ClaimID != testCandidateID || answer.Payload.ConflictPositions[1].ClaimID != testExistingID ||
		answer.Payload.RelatedTopics[0].TopicID != testExistingID || !slices.Equal(answer.Payload.Assertions[0].CitationIDs, []string{"citation-2"}) {
		t.Fatalf("answer=%+v", answer.Payload)
	}
}

func TestRAGAgentGenerationReturnsOnlyValidatedMetadataRefusal(t *testing.T) {
	agent := &ragAgentGenerationAgentFake{result: agentapplication.AgentRunResult{FinalText: "internal synthesis only"}}
	stream := &ragAgentGenerationStreamFake{result: agentapplication.AnswerStreamResult{Content: "Final markdown answer."}}
	metadata := &ragAgentGenerationMetadataFake{result: ragAgentGenerationRefusalResult(t)}
	generation := newRAGAgentGenerationForTest(t, agent, stream, metadata, nil)

	result, err := generation.Generate(context.Background(), ragAgentGenerationRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != nil || result.Refusal == nil || result.Refusal.ModelRunRef != testModelRunID ||
		result.Refusal.Payload.ReasonCode != agentdomain.RefusalEvidenceInsufficient {
		t.Fatalf("result=%+v", result)
	}
	if bytes.Contains(metadata.result.Output, []byte(`"model_run_ref"`)) || bytes.Contains(metadata.result.Output, []byte(testModelRunID)) {
		t.Fatalf("metadata refusal leaked server identity: %s", metadata.result.Output)
	}

	metadata.result.Phase = agentdomain.ModelCallInitial
	if _, err := generation.Generate(context.Background(), ragAgentGenerationRequest(t)); errorCode(err) != ragAgentGenerationOutputCode {
		t.Fatalf("wrong phase err=%v code=%q", err, errorCode(err))
	}
}

func TestRAGAgentGenerationFailsClosedWhenMetadataOutputIsNotStrict(t *testing.T) {
	finalText := "Final markdown answer."
	agent := &ragAgentGenerationAgentFake{result: agentapplication.AgentRunResult{FinalText: "internal synthesis only"}}
	stream := &ragAgentGenerationStreamFake{result: agentapplication.AnswerStreamResult{Content: finalText}}
	metadataResult := ragAgentGenerationMetadataResult(t, agentdomain.ModelCallInitial)
	var document map[string]any
	if err := json.Unmarshal(metadataResult.Output, &document); err != nil {
		t.Fatal(err)
	}
	document["payload"].(map[string]any)["conclusion"] = "model-owned body"
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	metadataResult.Output = encoded
	metadata := &ragAgentGenerationMetadataFake{result: metadataResult}
	generation := newRAGAgentGenerationForTest(t, agent, stream, metadata, nil)

	if _, err := generation.Generate(context.Background(), ragAgentGenerationRequest(t)); errorCode(err) != ragAgentGenerationOutputCode {
		t.Fatalf("err=%v code=%q", err, errorCode(err))
	}
}

func TestRAGAgentGenerationStopsAfterAgentError(t *testing.T) {
	agent := &ragAgentGenerationAgentFake{err: errors.New("agent provider failed")}
	stream := &ragAgentGenerationStreamFake{result: agentapplication.AnswerStreamResult{Content: "unused"}}
	metadata := &ragAgentGenerationMetadataFake{}
	generation := newRAGAgentGenerationForTest(t, agent, stream, metadata, nil)

	if _, err := generation.Generate(context.Background(), ragAgentGenerationRequest(t)); err == nil {
		t.Fatal("expected agent error")
	}
	if agent.calls != 1 || stream.calls != 0 || metadata.calls != 0 {
		t.Fatalf("provider calls agent=%d stream=%d metadata=%d", agent.calls, stream.calls, metadata.calls)
	}
}

func newRAGAgentGenerationForTest(
	t *testing.T,
	agent agentapplication.AgentRuntime,
	stream agentapplication.AnswerStreamRuntime,
	metadata agentapplication.RAGStructuredRunnerPort,
	sink agentapplication.AnswerStreamSink,
) agentapplication.RAGGenerationPort {
	t.Helper()
	return newRAGAgentGenerationForTestWithMaxOutputTokens(t, agent, stream, metadata, sink, 10)
}

func newRAGAgentGenerationForTestWithMaxOutputTokens(
	t *testing.T,
	agent agentapplication.AgentRuntime,
	stream agentapplication.AnswerStreamRuntime,
	metadata agentapplication.RAGStructuredRunnerPort,
	sink agentapplication.AnswerStreamSink,
	maxOutputTokens int,
) agentapplication.RAGGenerationPort {
	t.Helper()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	reservations := map[agentapplication.RunBudgetPhase]agentapplication.RunBudgetReservation{}
	for _, phase := range []agentapplication.RunBudgetPhase{
		agentapplication.RunBudgetPhaseAnswer, agentapplication.RunBudgetPhaseInitial, agentapplication.RunBudgetPhaseRepair,
		agentapplication.RunBudgetPhaseReduced, agentapplication.RunBudgetPhaseReview,
	} {
		reservations[phase] = agentapplication.RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: int64(maxOutputTokens)}
	}
	ledger, err := agentapplication.NewRunBudgetLedger(agentapplication.RunBudgetLedgerConfig{
		NodeAttemptID: testAttemptID, ModelRunID: testModelRunID, MaxTotalModelCalls: 8, MaxAgentIterations: 3, MaxToolCalls: 2,
		MaxInputTokens: 80, MaxOutputTokens: int64(maxOutputTokens) * 8, Deadline: now.Add(time.Minute), Clock: foundation.FixedClock{Value: now}, DownstreamReservations: reservations,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := agentapplication.NewModelCallRecorder(agentapplication.ModelCallRecorderDependencies{
		Repository: &workflowRepository{}, WorkspaceID: testWorkspaceID, ModelRunID: testModelRunID,
		IDs: &workflowIDs{values: []foundation.ID{testCallID, testCallID2, testCallID3}}, Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := &RAGAgentToolBridge{
		service:  &ragAgentGenerationExecutionServiceFake{},
		identity: toolsdomain.TrustedExecutionIdentity{WorkspaceID: testWorkspaceID},
		specs: []agentapplication.AgentToolSpec{{
			Ref: toolsdomain.ToolRef{Name: "ReadSource", Version: 2}, Name: "ReadSource", Description: "Read approved evidence",
			InputSchema: []byte(`{"type":"object","additionalProperties":false}`),
		}},
	}
	generation, err := NewRAGAgentGeneration(RAGAgentGenerationDependencies{
		Agent: agent, AnswerStream: stream, Metadata: metadata, Tools: tools, Budget: ledger, Recorder: recorder, Sink: sink,
		Model: testModelRef(), MaxIterations: 3, MaxInputTokens: 10, MaxOutputTokens: maxOutputTokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

type ragAgentGenerationExecutionServiceFake struct{}

func (*ragAgentGenerationExecutionServiceFake) Execute(context.Context, toolsapplication.ExecuteToolCommand) (toolsapplication.ToolExecutionResult, error) {
	return toolsapplication.ToolExecutionResult{}, errors.New("unexpected tool execution")
}

func ragAgentGenerationRequest(t *testing.T) agentapplication.RAGGenerationRequest {
	t.Helper()
	citation := agentdomain.Citation{
		ID: "citation-1", WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID,
		ChunkID: testChunkID, SourceVersionID: testSourceID, SourceSpanID: testSpanID,
	}
	generationContext := agentapplication.RAGGenerationContext{
		ModelRunRef: testModelRunID,
		Request: json.RawMessage(`{"question":"What does the approved source say?","scope":{"retrieval_mode":"hybrid","source_ids":["` +
			string(testChunkID) + `"],"source_version_ids":["` + string(testSourceID) +
			`"],"captured_at_from":"2026-08-01T00:00:00Z","captured_at_before":"2026-08-02T00:00:00Z"}}`),
		Evidence: []agentdomain.Evidence{{
			Citation: citation, Excerpt: "Approved deployment evidence.", Eligibility: knowledgedomain.EvidenceEligible,
			ConflictIDs: []foundation.ID{},
		}},
		Conflicts: []agentapplication.RAGConflictDisclosure{},
		RelatedTopics: map[foundation.ID]agentapplication.RAGAllowedTopic{
			testCandidateID: {Name: "Deployment", CitationIDs: []string{citation.ID}},
		},
	}
	return agentapplication.RAGGenerationRequest{
		ModelRunRef: testModelRunID, Context: generationContext, ProfileRef: DefaultProfileRef(),
		PromptRef:        RAGAnswerMetadataPromptRef(),
		SchemaRef:        agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		ReducedSchemaRef: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2},
	}
}

func ragAgentGenerationMetadataResult(t *testing.T, phase agentdomain.ModelCallPhase) agentapplication.StructuredRunResult {
	t.Helper()
	metadata := agentdomain.RAGAnswerMetadataResultV2{
		ResultType: agentdomain.ResultTypeRAGAnswerMetadata, SchemaID: agentdomain.RAGAnswerMetadataSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV2,
		Payload: agentdomain.RAGAnswerMetadataPayloadV2{
			Assertions: []agentdomain.RAGAnswerMetadataAssertionV2{{
				ID: "assertion-1", Text: "The approved source supports the answer.", Kind: agentdomain.AssertionFactual,
				EvidenceRefs: []string{"E1"},
			}},
			ConflictPositions: []agentdomain.RAGAnswerMetadataConflictPositionV2{}, ConflictSummary: "",
			RelatedTopicRefs:  []string{"T1"},
			FollowUpQuestions: []string{"What source detail should be reviewed next?"},
		},
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return agentapplication.StructuredRunResult{
		Output: raw, Phase: phase,
		Runtime: agentapplication.FrozenRuntimeRefs{Profile: DefaultProfileRef(), Prompt: RAGAnswerMetadataPromptRef(),
			Schema: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2}, Model: testModelRef()},
	}
}

func ragAgentGenerationRefusalResult(t *testing.T) agentapplication.StructuredRunResult {
	t.Helper()
	refusal := agentdomain.RAGAnswerMetadataRefusalResultV2{
		ResultType: agentdomain.ResultTypeRefusal, SchemaID: agentdomain.RAGAnswerMetadataRefusalSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV2,
		Payload: agentdomain.RefusalPayload{
			ReasonCode: agentdomain.RefusalEvidenceInsufficient, Summary: "Approved evidence is insufficient.", RetrievalScope: "approved workspace evidence",
			MissingRequirements: []string{"one supporting source"}, SuggestedActions: []string{"add approved evidence"},
		},
	}
	raw, err := json.Marshal(refusal)
	if err != nil {
		t.Fatal(err)
	}
	return agentapplication.StructuredRunResult{
		Output: raw, Phase: agentdomain.ModelCallReduced,
		Runtime: agentapplication.FrozenRuntimeRefs{Profile: DefaultProfileRef(), Prompt: RAGAnswerMetadataPromptRef(),
			Schema: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2}, Model: testModelRef()},
	}
}

type ragAgentGenerationAgentFake struct {
	request agentapplication.AgentRunRequest
	result  agentapplication.AgentRunResult
	err     error
	calls   int
}

func (fake *ragAgentGenerationAgentFake) Run(_ context.Context, request agentapplication.AgentRunRequest) (agentapplication.AgentRunResult, error) {
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}

type ragAgentGenerationStreamFake struct {
	request agentapplication.AnswerStreamRequest
	result  agentapplication.AnswerStreamResult
	err     error
	calls   int
}

func (fake *ragAgentGenerationStreamFake) Stream(ctx context.Context, request agentapplication.AnswerStreamRequest, sink agentapplication.AnswerStreamSink) (agentapplication.AnswerStreamResult, error) {
	fake.calls++
	fake.request = request
	if fake.err == nil && sink != nil && fake.result.Content != "" {
		if err := sink.Append(ctx, agentapplication.AnswerStreamChunk{Content: fake.result.Content}); err != nil {
			return agentapplication.AnswerStreamResult{}, err
		}
	}
	return fake.result, fake.err
}

type ragAgentGenerationMetadataFake struct {
	request agentapplication.StructuredRunRequest
	result  agentapplication.StructuredRunResult
	err     error
	calls   int
}

func (fake *ragAgentGenerationMetadataFake) Run(_ context.Context, request agentapplication.StructuredRunRequest) (agentapplication.StructuredRunResult, error) {
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}

type ragAgentGenerationSinkFake struct{ chunks []string }

func (sink *ragAgentGenerationSinkFake) Append(_ context.Context, chunk agentapplication.AnswerStreamChunk) error {
	sink.chunks = append(sink.chunks, chunk.Content)
	return nil
}

var _ agentapplication.AgentRuntime = (*ragAgentGenerationAgentFake)(nil)
var _ agentapplication.AnswerStreamRuntime = (*ragAgentGenerationStreamFake)(nil)
var _ agentapplication.RAGStructuredRunnerPort = (*ragAgentGenerationMetadataFake)(nil)
var _ agentapplication.AnswerStreamSink = (*ragAgentGenerationSinkFake)(nil)
