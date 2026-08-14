package models_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
)

const (
	einoLiveEnabledEnv      = "ZHIXU_EINO_LIVE_ENABLED"
	einoLiveAPIKeyEnv       = "ZHIXU_EINO_LIVE_API_KEY"
	einoLiveModelEnv        = "ZHIXU_EINO_LIVE_MODEL"
	einoLiveModelVersionEnv = "ZHIXU_EINO_LIVE_MODEL_VERSION"
	einoLiveBaseURLEnv      = "ZHIXU_EINO_LIVE_BASE_URL"
	einoLiveTimeoutEnv      = "ZHIXU_EINO_LIVE_TIMEOUT"
	einoLiveQueryPlanEnv    = "ZHIXU_EINO_LIVE_QUERY_PLAN_ENABLED"
	einoLiveQueryPlanTokens = "ZHIXU_EINO_LIVE_QUERY_PLAN_MAX_OUTPUT_TOKENS"
	einoLiveRAGMetadataEnv  = "ZHIXU_EINO_LIVE_RAG_METADATA_ENABLED"
	// Structured metadata and REVIEW exercise larger constrained outputs than
	// the minimal Chat smoke, so the aggregate live gate needs a bounded stage
	// timeout that does not reject a compatible Provider before it can respond.
	einoLiveDefaultTimeout     = 120 * time.Second
	einoLiveMaxOutputTokens    = 512
	einoLiveMetadataTokens     = 2048
	einoLiveMetadataCallTokens = 640
)

// TestEinoOpenAIChatModelLiveSmoke exercises the production adapter only when
// the explicitly opt-in provider environment is complete. It never logs values
// from the provider configuration.
func TestEinoOpenAIChatModelLiveSmoke(t *testing.T) {
	if !einoLiveEnabled(t, einoLiveEnabledEnv) {
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

	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = baseURL
	cfg.ChatAPIKey = apiKey
	cfg.ChatModel = modelID
	cfg.ChatModelVersion = modelVersion
	cfg.ChatTimeout = timeout
	runtime, err := models.NewConfiguredModelRuntime(cfg)
	if err != nil {
		t.Fatalf("create configured production model runtime: %v", err)
	}
	chat := runtime.Chat()
	if chat.State() != models.CapabilityConfigured {
		t.Fatalf("chat capability state=%s", chat.State())
	}
	model, ok := chat.Model().(*models.EinoOpenAIChatModel)
	if !ok {
		t.Fatalf("production chat adapter=%T, want *models.EinoOpenAIChatModel", chat.Model())
	}

	request := validChatRequest(model.Contract().Model)
	// Reasoning-capable providers may spend part of this budget before emitting JSON.
	request.MaxOutputTokens = einoLiveMaxOutputTokens
	request.Messages = []agentapplication.ChatMessage{
		{Role: agentapplication.MessageRoleSystem, Content: "Return one JSON object that follows the response schema."},
		{Role: agentapplication.MessageRoleUser, Content: `Return {"ok":true}.`},
	}
	request.OutputSchema = []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	response, err := model.Chat(ctx, request)
	if err != nil {
		t.Fatalf("production Eino adapter smoke failed: %v", err)
	}
	var payload struct {
		OK bool `json:"ok"`
	}
	if !json.Valid(response.Content) || json.Unmarshal(response.Content, &payload) != nil || !payload.OK {
		t.Fatalf("production Eino adapter returned invalid JSON content")
	}
}

// TestEinoOpenAIQueryPlanLiveSmoke exercises the production Query Plan path.
// It verifies that a complete retrieval request remains actionable and never
// logs model input, output, endpoint, or credentials.
func TestEinoOpenAIQueryPlanLiveSmoke(t *testing.T) {
	if !einoLiveEnabled(t, einoLiveQueryPlanEnv) {
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
	maxOutputTokens := einoLiveMaxOutputTokens
	if value := optionalEinoLiveEnv(t, einoLiveQueryPlanTokens); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > agentapplication.MaxOutputTokens {
			t.Fatalf("parse %s: expected a positive integer within the chat output budget", einoLiveQueryPlanTokens)
		}
		maxOutputTokens = parsed
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
		t.Fatalf("create configured production model runtime: %v", err)
	}
	model, ok := runtime.Chat().Model().(*models.EinoOpenAIChatModel)
	if !ok {
		t.Fatalf("production chat adapter=%T, want *models.EinoOpenAIChatModel", runtime.Chat().Model())
	}
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{
		Model: model.Contract().Model, Timeout: timeout, MaxOutputTokens: maxOutputTokens,
	})
	if err != nil {
		t.Fatalf("create production runtime catalog: %v", err)
	}
	planner, err := agentapplication.NewQueryPlanner(model, catalog)
	if err != nil {
		t.Fatalf("create production query planner: %v", err)
	}

	modelRunRef := foundation.ID("8e000000-0000-4000-8000-000000000001")
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := planner.Plan(ctx, agentapplication.QueryPlanRequest{
		ModelRunRef: modelRunRef,
		ProfileRef:  agentworkflow.DefaultProfileRef(),
		PromptRef:   agentworkflow.QueryPlanProviderPromptRef(),
		SchemaRef:   agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		Input:       []byte(`{"schema_version":2,"untrusted_data":true,"question":"What exact durable recovery token does the approved evidence require? Include durable-rag-query-plan-smoke verbatim and cite the evidence.","history":[],"scope":{"retrieval_mode":"hybrid","source_ids":[],"source_version_ids":[],"path_prefixes":[],"captured_at_from":null,"captured_at_before":null,"allow_original_sources":false,"allow_web":false},"non_evidence_context":{"untrusted_data":true,"user_preferences":[],"task_context":[]},"answer_depth":"standard","output_format":"markdown"}`),
	})
	if err != nil {
		t.Fatalf("production Eino query plan smoke failed: %v", err)
	}
	if result.Plan.ModelRunRef != modelRunRef || result.Plan.Validate() != nil {
		t.Fatal("production Eino query plan returned an invalid planning contract")
	}
	if result.Plan.Payload.RequiresClarification || len(result.Plan.Payload.Rewrites) == 0 {
		t.Fatal("production Eino query plan asked for clarification for a complete retrieval request")
	}
}

// TestEinoOpenAIRAGMetadataLiveSmoke verifies the current metadata-only v2
// contract through the production Chat adapter and Eino structured Graph.
// The project, rather than the model, owns the exact final Stream bytes.
func TestEinoOpenAIRAGMetadataLiveSmoke(t *testing.T) {
	if !einoLiveEnabled(t, einoLiveRAGMetadataEnv) {
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

	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = baseURL
	cfg.ChatAPIKey = apiKey
	cfg.ChatModel = modelID
	cfg.ChatModelVersion = modelVersion
	cfg.ChatTimeout = timeout
	runtime, err := models.NewConfiguredModelRuntime(cfg)
	if err != nil {
		t.Fatalf("create configured production model runtime: %v", err)
	}
	model, ok := runtime.Chat().Model().(*models.EinoOpenAIChatModel)
	if !ok {
		t.Fatalf("production chat adapter=%T, want *models.EinoOpenAIChatModel", runtime.Chat().Model())
	}
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{
		Model: model.Contract().Model, Timeout: timeout, MaxOutputTokens: einoLiveMetadataTokens,
	})
	if err != nil {
		t.Fatalf("create production runtime catalog: %v", err)
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatalf("create Eino structured scheduler: %v", err)
	}
	budget := agentapplication.DefaultRunBudget()
	budget.Timeout = timeout
	runner, err := agentapplication.NewStructuredRunnerWithScheduler(model, catalog, budget, scheduler)
	if err != nil {
		t.Fatalf("create production structured runner: %v", err)
	}

	modelRunRef := foundation.ID("8e000000-0000-4000-8000-000000000002")
	workspaceID := foundation.ID("8e000000-0000-4000-8000-000000000003")
	indexVersionID := foundation.ID("8e000000-0000-4000-8000-000000000004")
	chunkID1 := foundation.ID("8e000000-0000-4000-8000-000000000005")
	sourceVersionID1 := foundation.ID("8e000000-0000-4000-8000-000000000006")
	sourceSpanID1 := foundation.ID("8e000000-0000-4000-8000-000000000007")
	topicID := foundation.ID("8e000000-0000-4000-8000-000000000008")
	chunkID2 := foundation.ID("8e000000-0000-4000-8000-000000000009")
	sourceVersionID2 := foundation.ID("8e000000-0000-4000-8000-000000000010")
	sourceSpanID2 := foundation.ID("8e000000-0000-4000-8000-000000000011")
	claimID1 := foundation.ID("8e000000-0000-4000-8000-000000000012")
	claimID2 := foundation.ID("8e000000-0000-4000-8000-000000000013")
	updatedAt1 := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	updatedAt2 := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	applicability1 := json.RawMessage(`{"environment":"prod"}`)
	applicability2 := json.RawMessage(`{"environment":"dev"}`)
	const citationID1 = "citation-live-metadata-1"
	const citationID2 = "citation-live-metadata-2"
	const finalAnswer = "Production and development use distinct approved recovery procedures."
	input, err := json.Marshal(map[string]any{
		"final_answer_markdown": finalAnswer,
		"evidence": []any{
			map[string]any{"ref": "E1", "excerpt": "Production uses the approved production recovery procedure."},
			map[string]any{"ref": "E2", "excerpt": "Development uses the approved development recovery procedure."},
		},
		"conflicts": []any{
			map[string]any{"ref": "C1", "applicability": json.RawMessage(`{"environment":"prod"}`), "evidence_refs": []string{"E1"}},
			map[string]any{"ref": "C2", "applicability": json.RawMessage(`{"environment":"dev"}`), "evidence_refs": []string{"E2"}},
		},
		"related_topics": []any{map[string]any{"ref": "T1", "name": "Recovery procedures", "evidence_refs": []string{"E1", "E2"}}},
	})
	if err != nil {
		t.Fatalf("encode metadata input: %v", err)
	}
	if bytes.Contains(input, []byte(`"model_run_ref"`)) {
		t.Fatal("metadata input contains model_run_ref")
	}
	serverIdentities := []foundation.ID{
		modelRunRef, workspaceID, indexVersionID, chunkID1, sourceVersionID1, sourceSpanID1, topicID,
		chunkID2, sourceVersionID2, sourceSpanID2, claimID1, claimID2,
	}
	for _, identity := range serverIdentities {
		if bytes.Contains(input, []byte(identity)) {
			t.Fatalf("metadata input contains server-owned identity %s", identity)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := runner.Run(ctx, agentapplication.StructuredRunRequest{
		ProfileRef: agentworkflow.DefaultProfileRef(), PromptRef: agentworkflow.RAGAnswerMetadataPromptRef(),
		SchemaRef:        agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		ReducedSchemaRef: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		Input:            input,
		MaxOutputTokens:  einoLiveMetadataCallTokens,
	})
	if err != nil {
		t.Fatalf("production Eino RAG metadata smoke failed: %v", err)
	}
	if result.Runtime.Schema != (agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2}) ||
		(result.Phase != agentdomain.ModelCallInitial && result.Phase != agentdomain.ModelCallRepair) {
		t.Fatalf("production Eino RAG metadata degraded to phase=%s schema=%s/%s", result.Phase, result.Runtime.Schema.ID, result.Runtime.Schema.Version)
	}
	metadata, err := agentdomain.DecodeRAGAnswerMetadataV2(result.Output, agentdomain.DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("production Eino RAG metadata failed strict decoding: %v", err)
	}
	conflictRefCounts := make(map[string]int, len(metadata.Payload.ConflictPositions))
	for _, position := range metadata.Payload.ConflictPositions {
		conflictRefCounts[position.ConflictRef]++
	}
	if len(metadata.Payload.ConflictPositions) != 2 || conflictRefCounts["C1"] != 1 || conflictRefCounts["C2"] != 1 ||
		len(conflictRefCounts) != 2 || strings.TrimSpace(metadata.Payload.ConflictSummary) == "" {
		t.Fatalf("production Eino RAG metadata did not cover C1/C2 exactly once with a summary")
	}
	if len(metadata.Payload.RelatedTopicRefs) != 1 || metadata.Payload.RelatedTopicRefs[0] != "T1" {
		t.Fatal("production Eino RAG metadata did not select the supplied T1 topic")
	}
	for _, forbidden := range []string{"model_run_ref", "answer_sha256", "conclusion", "citation_ids", "topic_id"} {
		if bytes.Contains(result.Output, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("production Eino RAG metadata emitted forbidden field %s", forbidden)
		}
	}
	for _, identity := range serverIdentities {
		if bytes.Contains(result.Output, []byte(identity)) {
			t.Fatalf("production Eino RAG metadata emitted server-owned identity %s", identity)
		}
	}
	bindings := agentdomain.RAGAnswerMetadataBindings{
		Evidence: []agentdomain.RAGAnswerMetadataEvidenceBinding{
			{Ref: "E1", Citation: agentdomain.Citation{
				ID: citationID1, WorkspaceID: workspaceID, IndexVersionID: indexVersionID, ChunkID: chunkID1,
				SourceVersionID: sourceVersionID1, SourceSpanID: sourceSpanID1,
			}},
			{Ref: "E2", Citation: agentdomain.Citation{
				ID: citationID2, WorkspaceID: workspaceID, IndexVersionID: indexVersionID, ChunkID: chunkID2,
				SourceVersionID: sourceVersionID2, SourceSpanID: sourceSpanID2,
			}},
		},
		Conflicts: []agentdomain.RAGAnswerMetadataConflictBinding{
			{Ref: "C1", ClaimID: claimID1, Applicability: applicability1, CitationIDs: []string{citationID1}, UpdatedAt: updatedAt1},
			{Ref: "C2", ClaimID: claimID2, Applicability: applicability2, CitationIDs: []string{citationID2}, UpdatedAt: updatedAt2},
		},
		RelatedTopics: []agentdomain.RAGAnswerMetadataTopicBinding{{Ref: "T1", Topic: agentdomain.RelatedTopic{
			TopicID: topicID, Name: "Recovery procedures", CitationIDs: []string{citationID1, citationID2},
		}}},
	}
	answer, err := metadata.ComposeRAGAnswerV2(modelRunRef, finalAnswer, bindings)
	if err != nil || answer.Payload.Conclusion != finalAnswer || answer.ModelRunRef != modelRunRef ||
		len(answer.Payload.Citations) != 2 || answer.Payload.Citations[0].ID != citationID1 || answer.Payload.Citations[1].ID != citationID2 ||
		len(answer.Payload.ConflictPositions) != 2 || answer.Payload.ConflictPositions[0].ClaimID != claimID1 ||
		!bytes.Equal(answer.Payload.ConflictPositions[0].Applicability, applicability1) ||
		!answer.Payload.ConflictPositions[0].UpdatedAt.Equal(updatedAt1) || answer.Payload.ConflictPositions[1].ClaimID != claimID2 ||
		!bytes.Equal(answer.Payload.ConflictPositions[1].Applicability, applicability2) ||
		!answer.Payload.ConflictPositions[1].UpdatedAt.Equal(updatedAt2) ||
		len(answer.Payload.RelatedTopics) != 1 || answer.Payload.RelatedTopics[0].TopicID != topicID {
		t.Fatalf("compose exact final Stream with production metadata: %v", err)
	}
}

func einoLiveEnabled(t *testing.T, name string) bool {
	t.Helper()
	value, present := os.LookupEnv(name)
	if !present || value == "" {
		t.Skipf("production Eino live smoke disabled; set %s=true to enable it", name)
	}
	enabled, valid := canonicalEinoLiveBoolean(value)
	if !valid {
		t.Fatalf("parse %s: expected a boolean", name)
	}
	if !enabled {
		t.Skipf("production Eino live smoke disabled; set %s=true to enable it", name)
	}
	return true
}

func canonicalEinoLiveBoolean(value string) (bool, bool) {
	switch value {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func TestCanonicalEinoLiveBoolean(t *testing.T) {
	for _, test := range []struct {
		value   string
		enabled bool
		valid   bool
	}{
		{value: "true", enabled: true, valid: true},
		{value: "false", enabled: false, valid: true},
		{value: "1"},
		{value: "TRUE"},
		{value: " true"},
	} {
		enabled, valid := canonicalEinoLiveBoolean(test.value)
		if enabled != test.enabled || valid != test.valid {
			t.Fatalf("value=%q enabled=%t valid=%t", test.value, enabled, valid)
		}
	}
}

func requiredEinoLiveEnv(t *testing.T, name string) string {
	t.Helper()
	value, present := os.LookupEnv(name)
	if !present || value == "" {
		t.Fatalf("%s is required when the live smoke is enabled", name)
	}
	if value != strings.TrimSpace(value) {
		t.Fatalf("%s must be canonical", name)
	}
	return value
}

func optionalEinoLiveEnv(t *testing.T, name string) string {
	t.Helper()
	value, present := os.LookupEnv(name)
	if !present {
		return ""
	}
	if value == "" || value != strings.TrimSpace(value) {
		t.Fatalf("%s must be non-empty and canonical when set", name)
	}
	return value
}

func einoLiveTimeout(t *testing.T, name string) time.Duration {
	t.Helper()
	value := optionalEinoLiveEnv(t, name)
	if value == "" {
		return einoLiveDefaultTimeout
	}
	timeout, err := time.ParseDuration(value)
	if err != nil || timeout <= 0 {
		t.Fatalf("parse %s: expected a positive duration", name)
	}
	return timeout
}
