package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const queryPlanModelRunID foundation.ID = "61000000-0000-4000-8000-000000000001"

func TestQueryPlannerReturnsClarificationFromOnePlanCall(t *testing.T) {
	catalog, request, profile := queryPlanCatalog(t, time.Second, nil)
	raw := queryPlanDocument(queryPlanModelRunID, true)
	model := NewDeterministicChatModel(DeterministicChatStep{Response: testResponse(profile.Model, raw, 3, 2)})
	planner := newQueryPlanner(t, model, catalog)

	result, err := planner.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !result.Plan.Payload.RequiresClarification || len(result.Plan.Payload.Rewrites) != 0 {
		t.Fatalf("plan = %+v", result.Plan)
	}
	assertSinglePlanCall(t, model, request, profile)
	if result.Usage.TotalTokens != 5 || result.RequestBytes <= 0 || result.ResponseBytes != int64(len(raw)) ||
		result.Runtime != (FrozenRuntimeRefs{Profile: profile.Ref, Prompt: request.PromptRef, Schema: request.SchemaRef, Model: profile.Model}) {
		t.Fatalf("result metadata = %+v", result)
	}
}

func TestQueryPlannerReturnsRetrievalRewritesFromOnePlanCall(t *testing.T) {
	catalog, request, profile := queryPlanCatalog(t, time.Second, nil)
	raw := queryPlanDocument(queryPlanModelRunID, false)
	model := NewDeterministicChatModel(DeterministicChatStep{Response: testResponse(profile.Model, raw, 1, 1)})
	planner := newQueryPlanner(t, model, catalog)

	result, err := planner.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.Plan.Payload.RequiresClarification || len(result.Plan.Payload.Rewrites) != 2 {
		t.Fatalf("plan = %+v", result.Plan)
	}
	assertSinglePlanCall(t, model, request, profile)
}

func TestQueryPlannerRejectsInvalidReferencesAndInputBeforeProvider(t *testing.T) {
	catalog, request, profile := queryPlanCatalog(t, time.Second, nil)
	cases := map[string]func(*QueryPlanRequest){
		"model run": func(value *QueryPlanRequest) { value.ModelRunRef = "not-an-id" },
		"profile":   func(value *QueryPlanRequest) { value.ProfileRef = domain.ModelProfileRef{} },
		"prompt":    func(value *QueryPlanRequest) { value.PromptRef = domain.PromptRef{} },
		"schema id": func(value *QueryPlanRequest) { value.SchemaRef.ID = "other" },
		"schema v":  func(value *QueryPlanRequest) { value.SchemaRef.Version = "v2" },
		"empty":     func(value *QueryPlanRequest) { value.Input = nil },
		"invalid utf8": func(value *QueryPlanRequest) {
			value.Input = []byte{0xff}
		},
		"nul": func(value *QueryPlanRequest) { value.Input = []byte("bad\x00input") },
		"oversized": func(value *QueryPlanRequest) {
			value.Input = make([]byte, MaxStructuredInputBytes+1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := request
			candidate.Input = append([]byte(nil), request.Input...)
			mutate(&candidate)
			model := NewDeterministicChatModel(DeterministicChatStep{Response: testResponse(profile.Model, queryPlanDocument(queryPlanModelRunID, false), 1, 1)})
			planner := newQueryPlanner(t, model, catalog)
			_, err := planner.Plan(context.Background(), candidate)
			if errorCode(err) != errorCodeQueryPlanRequestInvalid || model.CallCount() != 0 {
				t.Fatalf("error=%v code=%q calls=%d", err, errorCode(err), model.CallCount())
			}
		})
	}
}

func TestQueryPlannerRejectsModelRunMismatchAndDecoderTransformation(t *testing.T) {
	t.Run("model run mismatch", func(t *testing.T) {
		catalog, request, profile := queryPlanCatalog(t, time.Second, nil)
		model := NewDeterministicChatModel(DeterministicChatStep{Response: testResponse(profile.Model, queryPlanDocument("61000000-0000-4000-8000-000000000099", false), 1, 1)})
		planner := newQueryPlanner(t, model, catalog)
		_, err := planner.Plan(context.Background(), request)
		if errorCode(err) != errorCodeQueryPlanResponseMismatch || model.CallCount() != 1 {
			t.Fatalf("error=%v code=%q calls=%d", err, errorCode(err), model.CallCount())
		}
	})

	t.Run("decoder transformation", func(t *testing.T) {
		decoder := func([]byte) (json.RawMessage, error) {
			return json.RawMessage(queryPlanDocument(queryPlanModelRunID, true)), nil
		}
		catalog, request, profile := queryPlanCatalog(t, time.Second, decoder)
		model := NewDeterministicChatModel(DeterministicChatStep{Response: testResponse(profile.Model, queryPlanDocument(queryPlanModelRunID, false), 1, 1)})
		planner := newQueryPlanner(t, model, catalog)
		_, err := planner.Plan(context.Background(), request)
		if errorCode(err) != errorCodeDecoderContract || model.CallCount() != 1 {
			t.Fatalf("error=%v code=%q calls=%d", err, errorCode(err), model.CallCount())
		}
	})
}

func TestQueryPlannerPreservesProviderErrorAndEnforcesProfileTimeout(t *testing.T) {
	t.Run("provider error", func(t *testing.T) {
		catalog, request, _ := queryPlanCatalog(t, time.Second, nil)
		providerErr := foundation.NewError(foundation.ErrorRetryableFailure, "CHAT_PROVIDER_UNAVAILABLE", true, errors.New("unavailable"))
		model := NewDeterministicChatModel(DeterministicChatStep{Err: providerErr}, DeterministicChatStep{})
		planner := newQueryPlanner(t, model, catalog)
		_, err := planner.Plan(context.Background(), request)
		if !errors.Is(err, providerErr) || model.CallCount() != 1 {
			t.Fatalf("error=%v calls=%d", err, model.CallCount())
		}
	})

	t.Run("timeout", func(t *testing.T) {
		catalog, request, _ := queryPlanCatalog(t, 20*time.Millisecond, nil)
		model := NewDeterministicChatModel(DeterministicChatStep{WaitForCancel: true}, DeterministicChatStep{})
		planner := newQueryPlanner(t, model, catalog)
		_, err := planner.Plan(context.Background(), request)
		if errorCode(err) != ErrorCodeOperationDeadline || !errors.Is(err, context.DeadlineExceeded) || model.CallCount() != 1 {
			t.Fatalf("error=%v code=%q calls=%d", err, errorCode(err), model.CallCount())
		}
	})
}

func queryPlanCatalog(t *testing.T, timeout time.Duration, decoder OutputDecoder) (*RuntimeCatalog, QueryPlanRequest, ModelProfile) {
	t.Helper()
	prompt := PromptDefinition{
		Ref:    domain.PromptRef{ID: "rag-query-plan", Version: "v1"},
		System: "bounded query planner system policy", InitialInstruction: "return one strict plan",
		RepairInstruction: "unused", ReducedInstruction: "unused",
	}
	schema := SchemaDefinition{
		Ref:        domain.SchemaRef{ID: domain.RAGQueryPlanSchemaID, Version: domain.OutputSchemaVersionV1},
		JSONSchema: []byte(`{"type":"object"}`), Decode: decoder,
	}
	if schema.Decode == nil {
		schema.Decode = func(raw []byte) (json.RawMessage, error) {
			if _, err := domain.DecodeRAGQueryPlan(raw, domain.DefaultDecodeLimits()); err != nil {
				return nil, err
			}
			return append(json.RawMessage(nil), raw...), nil
		}
	}
	profile := ModelProfile{
		Ref: domain.ModelProfileRef{ID: "default", Version: "v1"}, Model: testModelRef(),
		Timeout: timeout, MaxOutputTokens: 1024,
	}
	catalog := NewRuntimeCatalog()
	for _, register := range []func() error{
		func() error { return catalog.RegisterPrompt(prompt) },
		func() error { return catalog.RegisterSchema(schema) },
		func() error { return catalog.RegisterProfile(profile) },
		catalog.Freeze,
	} {
		if err := register(); err != nil {
			t.Fatal(err)
		}
	}
	return catalog, QueryPlanRequest{
		ModelRunRef: queryPlanModelRunID, ProfileRef: profile.Ref, PromptRef: prompt.Ref, SchemaRef: schema.Ref,
		Input: []byte(`{"question":"where is the policy?","history":[]}`),
	}, profile
}

func newQueryPlanner(t *testing.T, model ChatModel, catalog *RuntimeCatalog) *QueryPlanner {
	t.Helper()
	planner, err := NewQueryPlanner(model, catalog)
	if err != nil {
		t.Fatal(err)
	}
	return planner
}

func queryPlanDocument(modelRunRef foundation.ID, clarification bool) string {
	payload := `"intent":"find policy","requires_clarification":false,"rewrites":["policy location","policy path"],"clarification_reason":"","clarification_question":"","suggested_scopes":[]`
	if clarification {
		payload = `"intent":"identify policy","requires_clarification":true,"rewrites":[],"clarification_reason":"scope is missing","clarification_question":"Which workspace should be searched?","suggested_scopes":["current workspace"]`
	}
	return `{"result_type":"rag_query_plan","schema_id":"agent.rag-query-plan","schema_version":"v1","model_run_ref":"` + string(modelRunRef) + `","payload":{` + payload + `}}`
}

func assertSinglePlanCall(t *testing.T, model *DeterministicChatModel, request QueryPlanRequest, profile ModelProfile) {
	t.Helper()
	if model.CallCount() != 1 {
		t.Fatalf("calls = %d", model.CallCount())
	}
	call := model.Calls()[0]
	if call.Phase != domain.ModelCallPlan || call.ProfileRef != request.ProfileRef || call.PromptRef != request.PromptRef ||
		call.SchemaRef != request.SchemaRef || call.Model != profile.Model || len(call.Messages) != 3 ||
		call.Messages[0].Role != MessageRoleSystem || call.Messages[1].Content != "return one strict plan" ||
		!strings.Contains(call.Messages[2].Content, "UNTRUSTED TASK INPUT") || !strings.Contains(call.Messages[2].Content, string(request.Input)) {
		t.Fatalf("call = %+v", call)
	}
}
