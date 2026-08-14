package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
)

func TestEinoStructuredMetadataReducedRefusalDoesNotRequireModelIdentity(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := eino.NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	model := &metadataRefusalFallbackChat{}
	runner, err := agentapplication.NewStructuredRunnerWithScheduler(model, catalog, agentapplication.DefaultRunBudget(), scheduler)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), agentapplication.StructuredRunRequest{
		ProfileRef:       DefaultProfileRef(),
		PromptRef:        RAGAnswerMetadataPromptRef(),
		SchemaRef:        agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		ReducedSchemaRef: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		Input:            []byte(`{"final_answer_markdown":"answer","evidence":[],"conflicts":[],"related_topics":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != agentdomain.ModelCallReduced || result.Runtime.Schema.ID != agentdomain.RAGAnswerMetadataRefusalSchemaID {
		t.Fatalf("result phase/schema=%s/%s", result.Phase, result.Runtime.Schema.ID)
	}
	if _, err := agentdomain.DecodeRAGAnswerMetadataRefusalV2(result.Output, agentdomain.DefaultDecodeLimits()); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 3 {
		t.Fatalf("calls=%d", len(model.requests))
	}
	for _, request := range model.requests {
		for _, message := range request.Messages[1:] {
			if strings.Contains(message.Content, "model_run_ref") || strings.Contains(message.Content, string(testModelRunID)) {
				t.Fatalf("reduced metadata provider input leaked identity: %+v", request.Messages)
			}
		}
	}
}

type metadataRefusalFallbackChat struct {
	requests []agentapplication.ChatRequest
}

func (model *metadataRefusalFallbackChat) Chat(_ context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	model.requests = append(model.requests, request)
	content := []byte(`{}`)
	if len(model.requests) == 3 {
		content, _ = json.Marshal(agentdomain.RAGAnswerMetadataRefusalResultV2{
			ResultType: agentdomain.ResultTypeRefusal, SchemaID: agentdomain.RAGAnswerMetadataRefusalSchemaID,
			SchemaVersion: agentdomain.OutputSchemaVersionV2,
			Payload: agentdomain.RefusalPayload{
				ReasonCode: agentdomain.RefusalValidationExhausted, Summary: "Metadata validation could not be completed.",
				RetrievalScope: "approved workspace evidence", MissingRequirements: []string{"valid bounded metadata"},
				SuggestedActions: []string{"retry with approved evidence"},
			},
		})
	}
	return agentapplication.ChatResponse{Model: request.Model, Content: content, Usage: agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
}
