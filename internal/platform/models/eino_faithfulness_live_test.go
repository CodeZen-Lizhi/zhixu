package models_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

const (
	einoLiveFaithfulnessEnabledEnv         = "ZHIXU_EINO_LIVE_FAITHFULNESS_ENABLED"
	einoLiveFaithfulnessMaxOutputTokensEnv = "ZHIXU_EINO_LIVE_FAITHFULNESS_MAX_OUTPUT_TOKENS"
	einoLiveFaithfulnessProfileTokens      = 1024
	// Production keeps enough headroom for providers that count internal
	// reasoning against max_tokens, even for this small fixed fixture.
	einoLiveFaithfulnessDynamicTokens = 1024
)

// TestEinoOpenAIFaithfulnessReviewLiveSmoke exercises the production Eino
// configured runtime and the application-owned REVIEW gate. It is a protocol
// and domain-contract check, not a natural-language quality benchmark.
func TestEinoOpenAIFaithfulnessReviewLiveSmoke(t *testing.T) {
	if !einoLiveEnabled(t, einoLiveFaithfulnessEnabledEnv) {
		return
	}
	apiKey := requiredEinoLiveEnv(t, einoLiveAPIKeyEnv)
	modelID := requiredEinoLiveEnv(t, einoLiveModelEnv)
	baseURL := requiredEinoLiveEnv(t, einoLiveBaseURLEnv)
	modelVersion := optionalEinoLiveEnv(t, einoLiveModelVersionEnv)
	if modelVersion == "" {
		modelVersion = modelID
	}
	timeout := einoLiveTimeout(t, einoLiveTimeoutEnv)
	requestedOutputTokens := 0
	expectedOutputTokens := einoLiveFaithfulnessDynamicTokens
	if value := optionalEinoLiveEnv(t, einoLiveFaithfulnessMaxOutputTokensEnv); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > agentapplication.MaxOutputTokens {
			t.Fatalf("parse %s: expected a positive integer within the chat output budget", einoLiveFaithfulnessMaxOutputTokensEnv)
		}
		requestedOutputTokens = parsed
		expectedOutputTokens = parsed
		if expectedOutputTokens > einoLiveFaithfulnessProfileTokens {
			expectedOutputTokens = einoLiveFaithfulnessProfileTokens
		}
	}

	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = baseURL
	cfg.ChatAPIKey = apiKey
	cfg.ChatModel = modelID
	cfg.ChatModelVersion = modelVersion
	cfg.ChatTimeout = timeout
	runtime, err := models.NewConfiguredModelRuntime(cfg)
	if err != nil {
		failFaithfulnessLiveError(t, "create configured production model runtime", err, apiKey, baseURL)
	}
	chatCapability := runtime.Chat()
	if chatCapability.State() != models.CapabilityConfigured {
		t.Fatalf("production chat capability state=%s", chatCapability.State())
	}
	model, ok := chatCapability.Model().(*models.EinoOpenAIChatModel)
	if !ok {
		t.Fatalf("production chat adapter=%T, want *models.EinoOpenAIChatModel", chatCapability.Model())
	}

	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{
		Model: model.Contract().Model, Timeout: timeout, MaxOutputTokens: einoLiveFaithfulnessProfileTokens,
	})
	if err != nil {
		failFaithfulnessLiveError(t, "create production runtime catalog", err, apiKey, baseURL)
	}
	promptRef := agentworkflow.FaithfulnessReviewPromptRef()
	schemaRef := agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	profileRef := agentworkflow.DefaultProfileRef()
	snapshot, err := catalog.Snapshot(promptRef, schemaRef, schemaRef, profileRef)
	if err != nil {
		failFaithfulnessLiveError(t, "resolve production faithfulness runtime snapshot", err, apiKey, baseURL)
	}

	answer, evidence := faithfulnessLiveFixture(t)
	captured := &faithfulnessLiveChatCapture{delegate: model}
	budget := agentapplication.DefaultRunBudget()
	budget.Timeout = timeout
	reviewer, err := agentapplication.NewStructuredFaithfulnessReviewer(captured, catalog, budget)
	if err != nil {
		failFaithfulnessLiveError(t, "create StructuredFaithfulnessReviewer", err, apiKey, baseURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := reviewer.Review(ctx, agentapplication.FaithfulnessReviewRequest{
		ProfileRef: profileRef, PromptRef: promptRef, SchemaRef: schemaRef,
		Answer: answer, Evidence: evidence, MaxOutputTokens: requestedOutputTokens,
	})
	if err != nil {
		failFaithfulnessLiveError(t, "run production Eino Faithfulness REVIEW", err, apiKey, baseURL)
	}

	if captured.calls != 1 {
		t.Fatalf("production faithfulness REVIEW calls=%d, want 1", captured.calls)
	}
	request := captured.request
	if request.Phase != agentdomain.ModelCallReview {
		t.Fatalf("production faithfulness phase=%s, want REVIEW", request.Phase)
	}
	if request.ProfileRef != profileRef || request.PromptRef != promptRef || request.SchemaRef != schemaRef || request.Model != model.Contract().Model {
		t.Fatalf("production faithfulness runtime references drifted")
	}
	if request.MaxOutputTokens != expectedOutputTokens {
		t.Fatalf("production faithfulness output cap=%d, want %d", request.MaxOutputTokens, expectedOutputTokens)
	}
	if !bytes.Equal(request.OutputSchema, snapshot.Schema.JSONSchema) {
		t.Fatalf("production faithfulness request did not carry the frozen schema")
	}
	if err := validateStrictFaithfulnessSchema(request.OutputSchema); err != nil {
		t.Fatalf("production faithfulness schema is not strict: %v", err)
	}

	decoded, err := agentdomain.DecodeFaithfulnessReview(captured.response.Content, agentdomain.DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("production faithfulness response failed strict domain decoding: %v", err)
	}
	if decoded.ModelRunRef != answer.ModelRunRef || !decoded.Payload.Passed || len(decoded.Payload.Items) != 2 {
		t.Fatalf("production faithfulness response did not preserve the bounded review envelope")
	}
	if err := result.Review.Validate(); err != nil {
		t.Fatalf("production faithfulness review domain output is invalid: %v", err)
	}
	if err := agentapplication.ValidateFaithfulnessReview(answer, result.Review); err != nil {
		t.Fatalf("production faithfulness review did not cover all publishable targets: %v", err)
	}
	if result.Runtime.Profile != profileRef || result.Runtime.Prompt != promptRef || result.Runtime.Schema != schemaRef ||
		result.Runtime.Model != snapshot.Profile.Model || result.RequestBytes <= 0 || result.ResponseBytes <= 0 || result.Usage.TotalTokens <= 0 {
		t.Fatalf("production faithfulness result runtime or usage is incomplete")
	}
}

type faithfulnessLiveChatCapture struct {
	delegate agentapplication.ChatModel
	request  agentapplication.ChatRequest
	response agentapplication.ChatResponse
	calls    int
}

func (capture *faithfulnessLiveChatCapture) Chat(ctx context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	capture.calls++
	capture.request = request
	capture.request.Messages = append([]agentapplication.ChatMessage(nil), request.Messages...)
	capture.request.OutputSchema = append([]byte(nil), request.OutputSchema...)
	response, err := capture.delegate.Chat(ctx, request)
	capture.response = response
	capture.response.Content = append([]byte(nil), response.Content...)
	return response, err
}

func faithfulnessLiveFixture(t *testing.T) (agentdomain.RAGAnswerResult, []agentdomain.Evidence) {
	t.Helper()
	citation := agentdomain.Citation{
		ID:              "citation-live-faithfulness-1",
		WorkspaceID:     foundation.ID("8e000000-0000-4000-8000-000000000101"),
		IndexVersionID:  foundation.ID("8e000000-0000-4000-8000-000000000102"),
		ChunkID:         foundation.ID("8e000000-0000-4000-8000-000000000103"),
		SourceVersionID: foundation.ID("8e000000-0000-4000-8000-000000000104"),
		SourceSpanID:    foundation.ID("8e000000-0000-4000-8000-000000000105"),
	}
	const statement = "The approved runbook requires a durable replay receipt."
	answer := agentdomain.RAGAnswerResult{
		ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: foundation.ID("8e000000-0000-4000-8000-000000000100"),
		Payload: agentdomain.RAGAnswerPayload{
			Conclusion:        statement,
			Assertions:        []agentdomain.Assertion{{ID: "assertion-live-faithfulness-1", Text: statement, Kind: agentdomain.AssertionFactual, CitationIDs: []string{citation.ID}}},
			Citations:         []agentdomain.Citation{citation},
			ConflictPositions: []agentdomain.ConflictPosition{},
		},
	}
	if err := answer.Validate(); err != nil {
		t.Fatalf("faithfulness live fixture answer is invalid: %v", err)
	}
	evidence := []agentdomain.Evidence{{Citation: citation, Excerpt: statement, Eligibility: knowledgedomain.EvidenceEligible}}
	if err := evidence[0].Validate(); err != nil {
		t.Fatalf("faithfulness live fixture evidence is invalid: %v", err)
	}
	return answer, evidence
}

func validateStrictFaithfulnessSchema(raw []byte) error {
	root, err := strictObjectSchema(raw, "result_type", "schema_id", "schema_version", "model_run_ref", "payload")
	if err != nil {
		return err
	}
	payload, err := strictObjectSchema(root["payload"], "passed", "items", "summary")
	if err != nil {
		return err
	}
	var items struct {
		Type  string          `json:"type"`
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(payload["items"], &items); err != nil || items.Type != "array" || len(items.Items) == 0 {
		return errors.New("faithfulness items schema is invalid")
	}
	_, err = strictObjectSchema(items.Items, "assertion_id", "verdict", "citation_ids", "reason")
	return err
}

func strictObjectSchema(raw []byte, required ...string) (map[string]json.RawMessage, error) {
	var schema struct {
		Type                 string                     `json:"type"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties ||
		len(schema.Required) != len(required) || len(schema.Properties) != len(required) {
		return nil, errors.New("schema is not a closed object with the expected required fields")
	}
	for _, name := range required {
		if _, exists := schema.Properties[name]; !exists {
			return nil, errors.New("schema is missing a required property")
		}
		found := false
		for _, value := range schema.Required {
			if value == name {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("schema property is not required")
		}
	}
	return schema.Properties, nil
}

func failFaithfulnessLiveError(t *testing.T, operation string, err error, forbidden ...string) {
	t.Helper()
	errText := err.Error()
	for _, value := range forbidden {
		if value != "" && strings.Contains(errText, value) {
			t.Fatalf("%s leaked provider configuration", operation)
		}
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		t.Fatalf("%s failed (%s)", operation, classified.Code)
	}
	t.Fatalf("%s failed (unclassified)", operation)
}
