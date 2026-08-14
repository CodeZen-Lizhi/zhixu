package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

const fixtureAPIKey = "fixture-secret-canary"

func TestFixtureBarrierRequiresExplicitRelease(t *testing.T) {
	releasePath := t.TempDir() + "/release"
	handler := newHandlerWithBarrier("fixture-model-v2", fixtureAPIKey, fixtureBarrier{
		stage: "structured_plan", releasePath: releasePath, maxWait: 2 * time.Second, poll: 5 * time.Millisecond,
	})
	body := fixtureRequest(t, "rag_query_plan", "agent.rag-query-plan", map[string]any{"model_run_ref": "10000000-0000-4000-8000-000000000123"})
	type barrierResponse struct {
		status int
		body   string
	}
	result := make(chan barrierResponse, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		result <- barrierResponse{status: response.Code, body: response.Body.String()}
	}()
	select {
	case response := <-result:
		t.Fatalf("barrier returned before release: %#v", response)
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-result:
		if response.status != http.StatusOK || !strings.Contains(response.body, "rag_query_plan") {
			t.Fatalf("released barrier response=%#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("barrier did not release")
	}
}

func TestFixtureBuildsStructuredPlanAndMetadataFromBoundInput(t *testing.T) {
	handler := newHandler("fixture-model-v1", fixtureAPIKey)
	plan := fixtureRequest(t, "rag_query_plan", "agent.rag-query-plan", map[string]any{"model_run_ref": "10000000-0000-4000-8000-000000000001"})
	first := callFixture(t, handler, plan)
	second := callFixture(t, handler, plan)
	if first != second || !bytes.Contains([]byte(first), []byte(`"result_type":"rag_query_plan"`)) {
		t.Fatalf("deterministic plan responses differ: %s / %s", first, second)
	}

	metadataV2Input := fixtureMetadataV2Input(fixtureFinalAnswer)
	metadataV2 := callFixture(t, handler, fixtureRequestVersion(t, "rag_answer_metadata", "agent.rag-answer-metadata", "v2", metadataV2Input))
	if bytes.Contains([]byte(metadataV2), []byte(`"answer_sha256"`)) || bytes.Contains([]byte(metadataV2), []byte(`"conclusion"`)) ||
		bytes.Contains([]byte(metadataV2), []byte(`"model_run_ref"`)) || bytes.Contains([]byte(metadataV2), []byte(`"citation_ids"`)) ||
		!bytes.Contains([]byte(metadataV2), []byte(`"evidence_refs":["E2","E7"]`)) ||
		!bytes.Contains([]byte(metadataV2), []byte(`"conflict_ref":"C4"`)) || !bytes.Contains([]byte(metadataV2), []byte(`"conflict_ref":"C9"`)) ||
		!bytes.Contains([]byte(metadataV2), []byte(`"related_topic_refs":["T3","T8"]`)) {
		t.Fatalf("v2 metadata response did not preserve the metadata-only contract: %s", metadataV2)
	}

	answerSHA256 := fmt.Sprintf("%x", sha256.Sum256([]byte(fixtureFinalAnswer)))
	metadataV1Input := map[string]any{
		"model_run_ref": "10000000-0000-4000-8000-000000000002", "final_answer_markdown": fixtureFinalAnswer,
		"answer_sha256": answerSHA256, "generation_context": fixtureGenerationContext(),
	}
	metadataV1 := callFixture(t, handler, fixtureRequestVersion(t, "rag_answer_metadata", "agent.rag-answer-metadata", "v1", metadataV1Input))
	if !bytes.Contains([]byte(metadataV1), []byte(`"answer_sha256":"`+answerSHA256+`"`)) {
		t.Fatalf("legacy v1 metadata response did not retain answer binding: %s", metadataV1)
	}
}

func TestFixtureKeepsHistoricalRAGAnswerV2Contract(t *testing.T) {
	input := fixtureGenerationContext()
	input["model_run_ref"] = "10000000-0000-4000-8000-000000000003"
	response := callFixture(t, newHandler("fixture-model-v1", fixtureAPIKey),
		fixtureRequestVersion(t, "rag_answer", "agent.rag-answer", "v2", input))
	if !bytes.Contains([]byte(response), []byte(`"result_type":"rag_answer"`)) ||
		!bytes.Contains([]byte(response), []byte(`"conclusion":"`+fixtureFinalAnswer+`"`)) ||
		!bytes.Contains([]byte(response), []byte(`"citation_ids":["cite-smoke"]`)) {
		t.Fatalf("historical RAG answer response did not preserve v2 bindings: %s", response)
	}
}

func TestFixtureAcceptsExactModelSettingsConnectionSchema(t *testing.T) {
	handler := newHandler("fixture-model-v2", fixtureAPIKey)
	body := map[string]any{
		"model": "fixture-model", "max_tokens": 16,
		"messages": []any{
			map[string]any{"role": "system", "content": "Return JSON that matches the supplied schema."},
			map[string]any{"role": "user", "content": "Return ok=true."},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "model_settings_connection",
				"strict": true,
				"schema": map[string]any{
					"type": "object", "additionalProperties": false,
					"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
					"required":   []string{"ok"},
				},
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response := callFixture(t, handler, encoded)
	if response != `{"ok":true}` {
		t.Fatalf("connection response omitted ok=true: %s", response)
	}
}

func TestFixtureRejectsBroadenedModelSettingsConnectionSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"ok":{"type":"boolean"},"extra":{"type":"string"}},"required":["ok"]}`)
	if isModelSettingsConnectionSchema(schema) {
		t.Fatal("broadened connection schema was accepted")
	}
}

func TestFixtureAcceptsProductionEinoStructuredPlanWireContract(t *testing.T) {
	server, rejection := newProductionFixtureServer(t)
	model, catalog := newProductionStructuredRuntime(t, server)
	planner, err := agentapplication.NewQueryPlanner(model, catalog)
	if err != nil {
		t.Fatal(err)
	}
	_, err = planner.Plan(context.Background(), agentapplication.QueryPlanRequest{
		ModelRunRef: "10000000-0000-4000-8000-000000000099",
		ProfileRef:  agentworkflow.DefaultProfileRef(), PromptRef: agentworkflow.QueryPlanProviderPromptRef(),
		SchemaRef: agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		Input:     []byte(`{"question":"approved recovery"}`),
	})
	if err != nil {
		t.Fatalf("production Eino plan failed: %v fixture_rejection=%q", err, rejection())
	}
}

func TestFixtureProviderPlanV2IsIdentityless(t *testing.T) {
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{
		Model:   agentdomain.ModelRef{AdapterName: "fixture", AdapterVersion: "v1", ModelID: "fixture-model", ModelVersion: "fixture-model"},
		Timeout: time.Second, MaxOutputTokens: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot(
		agentworkflow.QueryPlanProviderPromptRef(),
		agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"model": "fixture-model", "max_tokens": 128,
		"messages": []any{
			map[string]any{"role": "system", "content": "policy"},
			map[string]any{"role": "user", "content": `UNTRUSTED TASK INPUT\n{"question":"approved recovery"}`},
		},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name": "fixture", "strict": true, "schema": json.RawMessage(snapshot.Schema.JSONSchema),
		}},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := callFixture(t, newHandler("fixture-model", fixtureAPIKey), encoded)
	if strings.Contains(response, "model_run_ref") || strings.Contains(response, "result_type") ||
		response != `{"i":"answer from approved recovery evidence","r":["approved recovery"],"d":"","q":"","s":[]}` {
		t.Fatalf("provider v2 response = %s", response)
	}
}

func TestFixtureAcceptsProductionEinoStructuredMetadataWireContract(t *testing.T) {
	server, rejection := newProductionFixtureServer(t)
	model, catalog := newProductionStructuredRuntime(t, server)
	scheduler, err := agenteino.NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agentapplication.NewStructuredRunnerWithScheduler(model, catalog, agentapplication.DefaultRunBudget(), scheduler)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(fixtureMetadataV2Input(fixtureFinalAnswer))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), agentapplication.StructuredRunRequest{
		ProfileRef: agentworkflow.DefaultProfileRef(), PromptRef: agentworkflow.RAGAnswerMetadataPromptRef(),
		SchemaRef:        agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		ReducedSchemaRef: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		Input:            input,
	})
	if err != nil {
		t.Fatalf("production Eino metadata failed: %v fixture_rejection=%q", err, rejection())
	}
	metadata, err := agentdomain.DecodeRAGAnswerMetadataV2(result.Output, agentdomain.DefaultDecodeLimits())
	if err != nil || result.Phase != agentdomain.ModelCallInitial ||
		bytes.Contains(result.Output, []byte(`"model_run_ref"`)) || bytes.Contains(result.Output, []byte(`"answer_sha256"`)) ||
		bytes.Contains(result.Output, []byte(`"conclusion"`)) || bytes.Contains(result.Output, []byte(`"citation_ids"`)) {
		t.Fatalf("metadata=%#v phase=%s decode_error=%v", metadata, result.Phase, err)
	}
}

func TestFixtureAcceptsProductionEinoFaithfulnessReviewWireContract(t *testing.T) {
	server, rejection := newProductionFixtureServer(t)
	model, catalog := newProductionStructuredRuntime(t, server)
	reviewer, err := agentapplication.NewStructuredFaithfulnessReviewer(model, catalog, agentapplication.DefaultRunBudget())
	if err != nil {
		t.Fatal(err)
	}
	citation := fixtureCitation()
	answer := agentdomain.RAGAnswerResult{
		ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: "10000000-0000-4000-8000-000000000097",
		Payload: agentdomain.RAGAnswerPayload{
			Conclusion: fixtureFinalAnswer,
			Assertions: []agentdomain.Assertion{{
				ID: "assertion-smoke", Text: fixtureFinalAnswer, Kind: agentdomain.AssertionFactual, CitationIDs: []string{citation.ID},
			}},
			Citations: []agentdomain.Citation{citation}, ConflictPositions: []agentdomain.ConflictPosition{},
		},
	}
	result, err := reviewer.Review(context.Background(), agentapplication.FaithfulnessReviewRequest{
		ProfileRef: agentworkflow.DefaultProfileRef(), PromptRef: agentworkflow.FaithfulnessReviewPromptRef(),
		SchemaRef: agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		Answer:    answer, Evidence: []agentdomain.Evidence{{
			Citation: citation, Excerpt: fixtureFinalAnswer, Eligibility: knowledgedomain.EvidenceEligible,
		}},
	})
	if err != nil {
		t.Fatalf("production Eino review failed: %v fixture_rejection=%q", err, rejection())
	}
	if err := agentapplication.ValidateFaithfulnessReview(answer, result.Review); err != nil {
		t.Fatalf("review did not cover the production targets: %v review=%#v", err, result.Review)
	}
}

func TestFixtureAcceptsProductionEinoAgentAndToolFreeStreamWireContracts(t *testing.T) {
	server, rejection := newProductionFixtureServer(t)
	runtimeModel, err := platformmodels.NewEinoRuntimeChatModel(platformmodels.OpenAIChatOptions{
		Client: server.Client(), BaseURL: server.URL, APIKey: fixtureAPIKey,
		Model: "rag-smoke", ModelVersion: "rag-smoke-v1", AdapterVersion: "fixture-v1",
		Timeout: 5 * time.Second, MaxRequestBytes: maxRequestBytes, MaxResponseBytes: maxRequestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	toolModel, err := runtimeModel.WithTools(productionRAGToolInfos(t))
	if err != nil {
		t.Fatal(err)
	}
	contextJSON, err := json.Marshal(fixtureAgentContext())
	if err != nil {
		t.Fatal(err)
	}
	messages := []*schema.Message{
		schema.SystemMessage("policy"),
		schema.UserMessage("Frozen RAG generation context:\n" + string(contextJSON)),
	}
	first, err := toolModel.Generate(context.Background(), messages, einomodel.WithMaxTokens(1024))
	if err != nil {
		t.Fatalf("first Agent call failed: %v fixture_rejection=%q", err, rejection())
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Function.Name != "ReadSource" {
		t.Fatalf("first agent response=%#v", first)
	}
	messages = append(messages, first, schema.ToolMessage(`{"evidence_ref":"E1","excerpt":"approved"}`, first.ToolCalls[0].ID, schema.WithToolName("ReadSource")))
	second, err := toolModel.Generate(context.Background(), messages, einomodel.WithMaxTokens(1024))
	if err != nil {
		t.Fatalf("second Agent call failed: %v fixture_rejection=%q", err, rejection())
	}
	if second.Role != schema.Assistant || second.Content == "" || len(second.ToolCalls) != 0 {
		t.Fatalf("second agent response=%#v", second)
	}

	stream, err := runtimeModel.Stream(context.Background(), messages,
		einomodel.WithMaxTokens(1024), einomodel.WithToolChoice(schema.ToolChoiceForbidden))
	if err != nil {
		t.Fatalf("final stream failed: %v fixture_rejection=%q", err, rejection())
	}
	defer stream.Close()
	var content strings.Builder
	for {
		chunk, receiveErr := stream.Recv()
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
		content.WriteString(chunk.Content)
	}
	if content.String() != fixtureFinalAnswer {
		t.Fatalf("stream content=%q", content.String())
	}
}

func TestFixtureFormalProviderInputsUseOnlyShortReferences(t *testing.T) {
	server, bodies := newRecordingFixtureServer(t)
	runtimeModel, err := platformmodels.NewEinoRuntimeChatModel(platformmodels.OpenAIChatOptions{
		Client: server.Client(), BaseURL: server.URL, APIKey: fixtureAPIKey,
		Model: "rag-smoke", ModelVersion: "rag-smoke-v1", AdapterVersion: "fixture-v1",
		Timeout: 5 * time.Second, MaxRequestBytes: maxRequestBytes, MaxResponseBytes: maxRequestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	toolModel, err := runtimeModel.WithTools([]*schema.ToolInfo{fixtureShortReadSourceToolInfo(t)})
	if err != nil {
		t.Fatal(err)
	}
	contextJSON, err := json.Marshal(fixtureAgentContext())
	if err != nil {
		t.Fatal(err)
	}
	messages := []*schema.Message{schema.SystemMessage("policy"), schema.UserMessage("Frozen RAG generation context:\n" + string(contextJSON))}
	first, err := toolModel.Generate(context.Background(), messages, einomodel.WithMaxTokens(1024))
	if err != nil || len(first.ToolCalls) != 1 || first.ToolCalls[0].Function.Arguments != `{"evidence_ref":"E1"}` {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	messages = append(messages, first, schema.ToolMessage(`{"evidence_ref":"E1","excerpt":"approved"}`, first.ToolCalls[0].ID, schema.WithToolName("ReadSource")))
	if _, err := toolModel.Generate(context.Background(), messages, einomodel.WithMaxTokens(1024)); err != nil {
		t.Fatal(err)
	}
	stream, err := runtimeModel.Stream(context.Background(), messages, einomodel.WithMaxTokens(1024), einomodel.WithToolChoice(schema.ToolChoiceForbidden))
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, receiveErr := stream.Recv()
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
	}
	stream.Close()

	structuredModel, catalog := newProductionStructuredRuntime(t, server)
	scheduler, err := agenteino.NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agentapplication.NewStructuredRunnerWithScheduler(structuredModel, catalog, agentapplication.DefaultRunBudget(), scheduler)
	if err != nil {
		t.Fatal(err)
	}
	metadataInput, err := json.Marshal(fixtureMetadataV2Input(fixtureFinalAnswer))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), agentapplication.StructuredRunRequest{
		ProfileRef: agentworkflow.DefaultProfileRef(), PromptRef: agentworkflow.RAGAnswerMetadataPromptRef(),
		SchemaRef:        agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		ReducedSchemaRef: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		Input:            metadataInput,
	}); err != nil {
		t.Fatal(err)
	}
	for index, body := range bodies() {
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("provider body[%d] cannot decode: %v", index, err)
		}
		messages, ok := request["messages"].([]any)
		if !ok {
			t.Fatalf("provider body[%d] has no messages", index)
		}
		visible := make([]any, 0, len(messages))
		for _, rawMessage := range messages {
			message, ok := rawMessage.(map[string]any)
			if !ok || message["role"] != "system" {
				visible = append(visible, rawMessage)
			}
		}
		request["messages"] = visible
		providerInput, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"model_run_ref", "citation", "source_", "span", "10000000-", "20000000-"} {
			if bytes.Contains(providerInput, []byte(forbidden)) {
				t.Fatalf("provider input[%d] leaked %q: %s", index, forbidden, providerInput)
			}
		}
	}
}

func TestFixtureAcceptsProductionEinoChatModelAgentWireContract(t *testing.T) {
	server, rejection := newProductionFixtureServer(t)
	runtimeModel, err := platformmodels.NewEinoRuntimeChatModel(platformmodels.OpenAIChatOptions{
		Client: server.Client(), BaseURL: server.URL, APIKey: fixtureAPIKey,
		Model: "rag-smoke", ModelVersion: "rag-smoke-v1", AdapterVersion: "fixture-v1",
		Timeout: 5 * time.Second, MaxRequestBytes: maxRequestBytes, MaxResponseBytes: maxRequestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agenteino.NewAgentRuntime(runtimeModel)
	if err != nil {
		t.Fatal(err)
	}
	ledger, recorder := newFixtureLedgerAndRecorder(t)
	contextJSON, err := json.Marshal(fixtureAgentContext())
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Run(context.Background(), agentapplication.AgentRunRequest{
		Model: agentdomain.ModelRef{
			AdapterName: "eino-openai", AdapterVersion: "fixture-v1", ModelID: "rag-smoke", ModelVersion: "rag-smoke-v1",
		},
		Profile: agentworkflow.DefaultProfileRef(), Prompt: agentworkflow.RAGAgentPromptRef(),
		Schema: agentdomain.SchemaRef{ID: agentdomain.RAGAgentTurnSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		Messages: []agentapplication.AgentMessage{
			{Role: agentapplication.AgentMessageSystem, Content: "policy"},
			{Role: agentapplication.AgentMessageUser, Content: "Frozen RAG generation context:\n" + string(contextJSON)},
		},
		Tools: productionRAGToolSpecs(t), MaxIterations: 2, MaxInputTokens: 1024, MaxOutputTokens: 1024,
		Budget: ledger, Recorder: recorder, ToolInvoker: fixtureToolInvoker{},
	})
	if err != nil {
		t.Fatalf("production Eino ChatModelAgent failed: %v cause=%v fixture_rejection=%q", err, errors.Unwrap(err), rejection())
	}
	if result.Iterations != 2 || result.ToolCalls != 1 || result.FinalText == "" {
		t.Fatalf("agent result=%#v", result)
	}
}

func newProductionFixtureServer(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	fixture := newHandler("rag-smoke-v1", fixtureAPIKey)
	rejected := ""
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		response := httptest.NewRecorder()
		fixture.ServeHTTP(response, request)
		if response.Code >= http.StatusBadRequest {
			rejected = response.Body.String()
		}
		for name, values := range response.Header() {
			for _, value := range values {
				writer.Header().Add(name, value)
			}
		}
		writer.WriteHeader(response.Code)
		_, _ = writer.Write(response.Body.Bytes())
	}))
	t.Cleanup(server.Close)
	return server, func() string { return rejected }
}

func newRecordingFixtureServer(t *testing.T) (*httptest.Server, func() [][]byte) {
	t.Helper()
	fixture := newHandler("rag-smoke-v1", fixtureAPIKey)
	var mu sync.Mutex
	bodies := make([][]byte, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, "request read failed", http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, append([]byte(nil), body...))
		mu.Unlock()
		request.Body = io.NopCloser(bytes.NewReader(body))
		fixture.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	return server, func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		result := make([][]byte, len(bodies))
		for index := range bodies {
			result[index] = append([]byte(nil), bodies[index]...)
		}
		return result
	}
}

func fixtureShortReadSourceToolInfo(t *testing.T) *schema.ToolInfo {
	t.Helper()
	var document jsonschema.Schema
	if err := json.Unmarshal([]byte(`{"type":"object","additionalProperties":false,"required":["evidence_ref"],"properties":{"evidence_ref":{"type":"string"}}}`), &document); err != nil {
		t.Fatal(err)
	}
	return &schema.ToolInfo{Name: "ReadSource", Desc: "Read approved evidence by short reference", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&document)}
}

func newProductionStructuredRuntime(t *testing.T, server *httptest.Server) (*platformmodels.EinoOpenAIChatModel, *agentapplication.RuntimeCatalog) {
	t.Helper()
	model, err := platformmodels.NewEinoOpenAIChatModel(platformmodels.OpenAIChatOptions{
		Client: server.Client(), BaseURL: server.URL, APIKey: fixtureAPIKey,
		Model: "rag-smoke", ModelVersion: "rag-smoke-v1", AdapterVersion: "fixture-v1",
		Timeout: 5 * time.Second, MaxRequestBytes: maxRequestBytes, MaxResponseBytes: maxRequestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{
		Model: model.Contract().Model, Timeout: 5 * time.Second, MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	return model, catalog
}

func productionRAGToolInfos(t *testing.T) []*schema.ToolInfo {
	t.Helper()
	specs := productionRAGToolSpecs(t)
	tools := make([]*schema.ToolInfo, len(specs))
	for index, spec := range specs {
		var document jsonschema.Schema
		if decodeErr := json.Unmarshal(spec.InputSchema, &document); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		tools[index] = &schema.ToolInfo{
			Name: spec.Name, Desc: spec.Description,
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&document),
		}
	}
	return tools
}

func productionRAGToolSpecs(t *testing.T) []agentapplication.AgentToolSpec {
	t.Helper()
	registry, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := conversationworkflow.RegisteredDefinitionV2()
	refs := definition.Graph.Nodes[0].AllowedTools
	tools := make([]agentapplication.AgentToolSpec, len(refs))
	for index, ref := range refs {
		contract, resolveErr := registry.ResolveContract(ref)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		tools[index] = agentapplication.AgentToolSpec{
			Ref: contract.Definition.Ref, Name: contract.Definition.Ref.Name, Description: contract.Definition.Description,
			InputSchema: append([]byte(nil), contract.Definition.InputSchemaDocument...),
		}
	}
	return tools
}

func fixtureCitation() agentdomain.Citation {
	return agentdomain.Citation{
		ID: "cite-smoke", WorkspaceID: "10000000-0000-4000-8000-000000000010",
		IndexVersionID: "10000000-0000-4000-8000-000000000011", ChunkID: "10000000-0000-4000-8000-000000000012",
		SourceVersionID: "10000000-0000-4000-8000-000000000013", SourceSpanID: "10000000-0000-4000-8000-000000000014",
	}
}

type fixtureToolInvoker struct{}

func (fixtureToolInvoker) Invoke(context.Context, agentapplication.AgentToolInvocation) (agentapplication.AgentToolResult, error) {
	return agentapplication.AgentToolResult{Output: []byte(`{"evidence_ref":"E1","excerpt":"approved"}`)}, nil
}

type fixtureModelCallRepository struct{}

func (*fixtureModelCallRepository) CreateModelRun(context.Context, agentdomain.ModelRun) (agentdomain.ModelRun, bool, error) {
	return agentdomain.ModelRun{}, false, errors.New("unused")
}

func (*fixtureModelCallRepository) GetModelRun(context.Context, foundation.ID, foundation.ID) (agentapplication.ModelRunRecord, error) {
	return agentapplication.ModelRunRecord{}, errors.New("unused")
}

func (*fixtureModelCallRepository) StartModelCall(_ context.Context, _ foundation.ID, call agentdomain.ModelCall) (agentdomain.ModelCall, bool, error) {
	return call, false, nil
}

func (*fixtureModelCallRepository) CompleteModelCall(_ context.Context, command agentapplication.CompleteModelCallCommand) (agentdomain.ModelCall, bool, error) {
	return command.Call, false, nil
}

func (*fixtureModelCallRepository) FinalizeModelRun(context.Context, agentapplication.FinalizeModelRunCommand) (agentdomain.ModelRun, bool, error) {
	return agentdomain.ModelRun{}, false, errors.New("unused")
}

func (*fixtureModelCallRepository) MarkStaleModelCallsUnknown(context.Context, agentapplication.UnknownRecoveryQuery) ([]agentdomain.ModelCall, error) {
	return nil, errors.New("unused")
}

func (*fixtureModelCallRepository) MarkStaleModelRunsUnknown(context.Context, agentapplication.UnknownRecoveryQuery) ([]agentdomain.ModelRun, error) {
	return nil, errors.New("unused")
}

type fixtureIDs struct{ next int }

func (ids *fixtureIDs) New() (foundation.ID, error) {
	ids.next++
	return foundation.ParseID(fmt.Sprintf("20000000-0000-4000-8000-%012d", ids.next))
}

func newFixtureLedgerAndRecorder(t *testing.T) (*agentapplication.RunBudgetLedger, *agentapplication.ModelCallRecorder) {
	t.Helper()
	now := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	reservations := make(map[agentapplication.RunBudgetPhase]agentapplication.RunBudgetReservation, 5)
	for _, phase := range []agentapplication.RunBudgetPhase{
		agentapplication.RunBudgetPhaseAnswer, agentapplication.RunBudgetPhaseInitial, agentapplication.RunBudgetPhaseRepair,
		agentapplication.RunBudgetPhaseReduced, agentapplication.RunBudgetPhaseReview,
	} {
		reservations[phase] = agentapplication.RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 1024, ReservedOutputTokens: 1024}
	}
	ledger, err := agentapplication.NewRunBudgetLedger(agentapplication.RunBudgetLedgerConfig{
		NodeAttemptID: "20000000-0000-4000-8000-000000000090", ModelRunID: "20000000-0000-4000-8000-000000000091",
		MaxTotalModelCalls: 7, MaxAgentIterations: 2, MaxToolCalls: 1,
		MaxInputTokens: 7 * 1024, MaxOutputTokens: 7 * 1024, Deadline: now.Add(time.Minute),
		Clock: foundation.FixedClock{Value: now}, DownstreamReservations: reservations,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := agentapplication.NewModelCallRecorder(agentapplication.ModelCallRecorderDependencies{
		Repository: &fixtureModelCallRepository{}, WorkspaceID: "20000000-0000-4000-8000-000000000092",
		ModelRunID: "20000000-0000-4000-8000-000000000091", IDs: &fixtureIDs{}, Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ledger, recorder
}

func TestFixtureCompletesBoundReadSourceAgentLoop(t *testing.T) {
	handler := newHandler("fixture-model-v1", fixtureAPIKey)
	context := fixtureAgentContext()
	first := callFixtureResponse(t, handler, fixtureAgentRequest(t, context, nil, nil))
	call := first.Choices[0].Message.ToolCalls[0]
	if first.Choices[0].FinishReason != "tool_calls" || call.ID != "fixture-read-source-1" || call.Function.Name != "ReadSource" ||
		call.Function.Arguments != `{"evidence_ref":"E1"}` {
		t.Fatalf("first agent response=%+v", first)
	}
	second := callFixtureResponse(t, handler, fixtureAgentRequest(t, context,
		map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Function.Name, "arguments": call.Function.Arguments}}}},
		map[string]any{"role": "tool", "tool_call_id": call.ID, "content": `{"evidence_ref":"E1","excerpt":"approved"}`}))
	if second.Choices[0].FinishReason != "stop" || second.Choices[0].Message.Content == "" {
		t.Fatalf("second agent response=%+v", second)
	}
}

func TestFixtureStreamsMultipleFramesWithUsage(t *testing.T) {
	handler := newHandler("fixture-model-v1", fixtureAPIKey)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(fixtureStreamRequest(t, fixtureAgentContext())))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	body := response.Body.String()
	if bytes.Count([]byte(body), []byte("data: ")) != 4 || !bytes.Contains([]byte(body), []byte(`"content":"Approved recovery requires durable "`)) ||
		!bytes.Contains([]byte(body), []byte(`"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}`)) || !bytes.Contains([]byte(body), []byte("data: [DONE]")) {
		t.Fatalf("stream body=%s", body)
	}
}

func TestFixtureBuildsFaithfulnessItemsFromReviewTargets(t *testing.T) {
	input := map[string]any{"model_run_ref": "20000000-0000-4000-8000-000000000001", "review_targets": []any{
		map[string]any{"id": "fact", "kind": "FACTUAL", "citation_ids": []string{"cite-1"}},
		map[string]any{"id": "inference", "kind": "MODEL_INFERENCE", "citation_ids": []string{}},
	}}
	response := callFixture(t, newHandler("fixture-model-v1", fixtureAPIKey), fixtureRequest(t, "faithfulness_review", "agent.faithfulness-review", input))
	if !bytes.Contains([]byte(response), []byte(`"verdict":"SUPPORTED"`)) || !bytes.Contains([]byte(response), []byte(`"verdict":"INFERENCE_DISCLOSED"`)) {
		t.Fatalf("faithfulness response=%s", response)
	}
}

func TestFixtureLogsOnlyStableContractRejection(t *testing.T) {
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousWriter) })

	privateRequestCanary := "private-request-body-canary"
	input := fixtureMetadataV2Input(privateRequestCanary)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(
		fixtureRequestVersion(t, "rag_answer_metadata", "agent.rag-answer-metadata", "v2", input),
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
	response := httptest.NewRecorder()
	newHandler("fixture-model-v1", fixtureAPIKey).ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(logs.String(), "stage=structured_metadata reason=metadata_answer_binding_invalid status=422") {
		t.Fatalf("missing stable rejection log: %q", logs.String())
	}
	for _, secret := range []string{privateRequestCanary, fixtureAPIKey, fixtureFinalAnswer} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("fixture rejection log leaked private data: %q", logs.String())
		}
	}
}

func TestValidateAddressFailsClosedOutsideExplicitCompose(t *testing.T) {
	for _, test := range []struct {
		address string
		compose bool
		valid   bool
	}{
		{"127.0.0.1:18080", false, true}, {"localhost:18080", false, true}, {"0.0.0.0:18080", false, false}, {"0.0.0.0:18080", true, true}, {"example.com:18080", true, false},
	} {
		if got := validateAddress(test.address, test.compose) == nil; got != test.valid {
			t.Fatalf("validateAddress(%q,%t) valid=%t", test.address, test.compose, got)
		}
	}
}

func TestFixtureRejectsTrailingJSONDocument(t *testing.T) {
	body := append(fixtureRequest(t, "rag_query_plan", "agent.rag-query-plan", map[string]any{"model_run_ref": "30000000-0000-4000-8000-000000000001"}), []byte(`{}`)...)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
	response := httptest.NewRecorder()
	newHandler("fixture-model-v1", fixtureAPIKey).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFixtureRejectsUnknownRequestFields(t *testing.T) {
	var request map[string]any
	if err := json.Unmarshal(fixtureRequest(t, "rag_query_plan", "agent.rag-query-plan", map[string]any{"model_run_ref": "30000000-0000-4000-8000-000000000004"}), &request); err != nil {
		t.Fatal(err)
	}
	request["unexpected"] = true
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
	response := httptest.NewRecorder()
	newHandler("fixture-model-v1", fixtureAPIKey).ServeHTTP(response, httpRequest)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFixtureRequiresExactBearerCanary(t *testing.T) {
	handler := newHandler("fixture-model-v1", fixtureAPIKey)
	body := fixtureRequest(t, "rag_query_plan", "agent.rag-query-plan", map[string]any{"model_run_ref": "30000000-0000-4000-8000-000000000002"})
	for _, authorization := range []string{"", fixtureAPIKey, "Bearer wrong-canary", "bearer " + fixtureAPIKey} {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("authorization=%q status=%d body=%s", authorization, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Add("Authorization", "Bearer "+fixtureAPIKey)
	request.Header.Add("Authorization", "Bearer "+fixtureAPIKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate authorization status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFixtureRejectsSchemaContractOutsideAllowlist(t *testing.T) {
	handler := newHandler("fixture-model-v1", fixtureAPIKey)
	for _, test := range []struct {
		resultType string
		schemaID   string
		version    string
	}{
		{"rag_answer_metadata", "agent.rag-answer-metadata", "v3"},
		{"rag_answer_metadata", "agent.rag-query-plan", "v1"},
		{"unknown", "agent.rag-answer-metadata", "v1"},
	} {
		body := fixtureRequestVersion(t, test.resultType, test.schemaID, test.version, map[string]any{"model_run_ref": "30000000-0000-4000-8000-000000000003"})
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("contract=%+v status=%d body=%s", test, response.Code, response.Body.String())
		}
	}
}

func fixtureRequest(t *testing.T, resultType, schemaID string, input map[string]any) []byte {
	t.Helper()
	version := "v1"
	return fixtureRequestVersion(t, resultType, schemaID, version, input)
}

func fixtureRequestVersion(t *testing.T, resultType, schemaID, version string, input map[string]any) []byte {
	t.Helper()
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"model": "fixture-model", "max_tokens": 1024,
		"messages":        []any{map[string]any{"role": "system", "content": "policy"}, map[string]any{"role": "user", "content": "UNTRUSTED TASK INPUT\n" + string(inputJSON)}},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "fixture", "strict": true, "schema": map[string]any{"properties": map[string]any{"result_type": map[string]any{"const": resultType}, "schema_id": map[string]any{"const": schemaID}, "schema_version": map[string]any{"const": version}}}}},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func callFixture(t *testing.T, handler http.Handler, body []byte) string {
	t.Helper()
	response := callFixtureResponse(t, handler, body)
	return response.Choices[0].Message.Content
}

type fixtureResponse struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func callFixtureResponse(t *testing.T, handler http.Handler, body []byte) fixtureResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var outer fixtureResponse
	if err := json.Unmarshal(response.Body.Bytes(), &outer); err != nil || len(outer.Choices) != 1 {
		t.Fatalf("response=%s err=%v", response.Body.String(), err)
	}
	return outer
}

func fixtureGenerationContext() map[string]any {
	return map[string]any{
		"evidence": []any{map[string]any{"citation": map[string]any{
			"id": "cite-smoke", "workspace_id": "10000000-0000-4000-8000-000000000010", "index_version_id": "10000000-0000-4000-8000-000000000011",
			"chunk_id": "10000000-0000-4000-8000-000000000012", "source_version_id": "10000000-0000-4000-8000-000000000013", "source_span_id": "10000000-0000-4000-8000-000000000014",
		}}},
		"related_topics": map[string]any{"10000000-0000-4000-8000-000000000015": map[string]any{"name": "Smoke topic", "citation_ids": []string{"cite-smoke"}}},
	}
}

func fixtureMetadataV2Input(finalAnswer string) map[string]any {
	return map[string]any{
		"final_answer_markdown": finalAnswer,
		"evidence": []any{
			map[string]any{"ref": "E2", "excerpt": "Approved recovery evidence one."},
			map[string]any{"ref": "E7", "excerpt": "Approved recovery evidence two."},
		},
		"conflicts": []any{
			map[string]any{"ref": "C4", "applicability": map[string]any{"environment": "prod"}, "evidence_refs": []string{"E2"}},
			map[string]any{"ref": "C9", "applicability": map[string]any{"environment": "staging"}, "evidence_refs": []string{"E7"}},
		},
		"related_topics": []any{
			map[string]any{"ref": "T3", "name": "Smoke topic", "evidence_refs": []string{"E2"}},
			map[string]any{"ref": "T8", "name": "Recovery topic", "evidence_refs": []string{"E7"}},
		},
	}
}

func fixtureAgentContext() map[string]any {
	return map[string]any{
		"evidence":  []any{map[string]any{"ref": "E1", "excerpt": "Approved recovery evidence."}},
		"conflicts": []any{},
		"related_topics": []any{map[string]any{
			"ref": "T1", "name": "Smoke topic", "evidence_refs": []string{"E1"},
		}},
	}
}

func fixtureAgentRequest(t *testing.T, context map[string]any, assistant, toolMessage map[string]any) []byte {
	t.Helper()
	contextJSON, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	messages := []any{map[string]any{"role": "system", "content": "policy"}, map[string]any{"role": "user", "content": "Frozen RAG generation context:\n" + string(contextJSON)}}
	if assistant != nil {
		messages = append(messages, assistant)
	}
	if toolMessage != nil {
		messages = append(messages, toolMessage)
	}
	return fixtureRuntimeRequest(t, messages, false, "auto", true)
}

func fixtureStreamRequest(t *testing.T, context map[string]any) []byte {
	t.Helper()
	contextJSON, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	return fixtureRuntimeRequest(t, []any{map[string]any{"role": "system", "content": "policy"}, map[string]any{"role": "user", "content": string(contextJSON)}}, true, "none", false)
}

func fixtureRuntimeRequest(t *testing.T, messages []any, stream bool, toolChoice string, includeTools bool) []byte {
	t.Helper()
	request := map[string]any{
		"model": "fixture-model", "max_tokens": 1024, "messages": messages, "stream": stream, "tool_choice": toolChoice,
	}
	if includeTools {
		request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "ReadSource", "description": "Read approved evidence", "parameters": map[string]any{"type": "object"}}}}
	}
	if stream {
		request["stream_options"] = map[string]any{"include_usage": true}
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
