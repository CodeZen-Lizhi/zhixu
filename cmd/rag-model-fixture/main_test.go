package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const fixtureAPIKey = "fixture-secret-canary"

func TestFixtureUsesSchemaAndRequestInsteadOfCallOrder(t *testing.T) {
	handler := newHandler("fixture-model-v1", fixtureAPIKey)
	plan := fixtureRequest(t, "rag_query_plan", "agent.rag-query-plan", map[string]any{"model_run_ref": "10000000-0000-4000-8000-000000000001"})
	first := callFixture(t, handler, plan)
	second := callFixture(t, handler, plan)
	if first != second || !bytes.Contains([]byte(first), []byte(`"result_type":"rag_query_plan"`)) {
		t.Fatalf("deterministic plan responses differ: %s / %s", first, second)
	}

	ragInput := map[string]any{
		"model_run_ref":  "10000000-0000-4000-8000-000000000002",
		"evidence":       []any{map[string]any{"citation": map[string]any{"id": "cite-smoke", "workspace_id": "10000000-0000-4000-8000-000000000010", "index_version_id": "10000000-0000-4000-8000-000000000011", "chunk_id": "10000000-0000-4000-8000-000000000012", "source_version_id": "10000000-0000-4000-8000-000000000013", "source_span_id": "10000000-0000-4000-8000-000000000014"}}},
		"related_topics": map[string]any{"10000000-0000-4000-8000-000000000015": map[string]any{"name": "Smoke topic", "citation_ids": []string{"cite-smoke"}}},
	}
	rag := callFixture(t, handler, fixtureRequest(t, "rag_answer", "agent.rag-answer", ragInput))
	if !bytes.Contains([]byte(rag), []byte(`"citation_ids":["cite-smoke"]`)) || !bytes.Contains([]byte(rag), []byte(`"topic_id":"10000000-0000-4000-8000-000000000015"`)) {
		t.Fatalf("rag response did not derive request identities: %s", rag)
	}
}

func TestFixtureAcceptsOnlyTheManagedRuntimePlainProbe(t *testing.T) {
	handler := newHandler("fixture-model-v1", fixtureAPIKey)
	for _, test := range []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "managed probe", body: `{"model":"fixture-model","messages":[{"role":"user","content":"test"}]}`, wantStatus: http.StatusOK},
		{name: "other content", body: `{"model":"fixture-model","messages":[{"role":"user","content":"private prompt"}]}`, wantStatus: http.StatusBadRequest},
		{name: "multiple messages", body: `{"model":"fixture-model","messages":[{"role":"system","content":"policy"},{"role":"user","content":"test"}]}`, wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.wantStatus == http.StatusOK && !bytes.Contains(response.Body.Bytes(), []byte(`"content":"ok"`)) {
				t.Fatalf("probe response=%s", response.Body.String())
			}
		})
	}
}

func TestFixtureControlBlocksOnlyTheNextStructuredRequest(t *testing.T) {
	handler := newHandler("fixture-model-v1", fixtureAPIKey)
	state := callFixtureControl(t, handler, http.MethodPost, "/control/block-next", fixtureAPIKey)
	if !state.Armed || state.Entered {
		t.Fatalf("armed state=%+v", state)
	}

	body := fixtureRequest(t, "rag_query_plan", "agent.rag-query-plan", map[string]any{"model_run_ref": "10000000-0000-4000-8000-000000000001"})
	blocked := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		blocked <- response
	}()

	deadline := time.Now().Add(time.Second)
	for {
		state = callFixtureControl(t, handler, http.MethodGet, "/control/state", fixtureAPIKey)
		if state.Armed && state.Entered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("structured request did not enter the fixture gate")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case response := <-blocked:
		t.Fatalf("first structured request was not blocked: %d", response.Code)
	default:
	}

	second := callFixture(t, handler, body)
	if !bytes.Contains([]byte(second), []byte(`"result_type":"rag_query_plan"`)) {
		t.Fatalf("second structured response=%s", second)
	}
	state = callFixtureControl(t, handler, http.MethodPost, "/control/release", fixtureAPIKey)
	if state.Armed || state.Entered {
		t.Fatalf("released state=%+v", state)
	}
	select {
	case response := <-blocked:
		if response.Code != http.StatusOK {
			t.Fatalf("released structured request status=%d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("released structured request did not finish")
	}
}

func TestFixtureControlRequiresExactBearerAndNoBody(t *testing.T) {
	handler := newHandler("fixture-model-v1", fixtureAPIKey)
	for _, test := range []struct {
		name   string
		key    string
		body   string
		status int
	}{
		{name: "missing bearer", status: http.StatusUnauthorized},
		{name: "wrong bearer", key: "wrong", status: http.StatusUnauthorized},
		{name: "body", key: fixtureAPIKey, body: `{}`, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/control/block-next", bytes.NewBufferString(test.body))
			if test.key != "" {
				request.Header.Set("Authorization", "Bearer "+test.key)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
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

func callFixtureControl(t *testing.T, handler http.Handler, method, path, apiKey string) structuredRequestGateState {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("control %s %s status=%d body=%s", method, path, response.Code, response.Body.String())
	}
	var state structuredRequestGateState
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
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
		{"rag_answer", "agent.rag-answer", "v1"},
		{"rag_answer", "agent.rag-query-plan", "v2"},
		{"unknown", "agent.rag-answer", "v2"},
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
	if resultType == "rag_answer" {
		version = "v2"
	}
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
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var outer struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &outer); err != nil || len(outer.Choices) != 1 {
		t.Fatalf("response=%s err=%v", response.Body.String(), err)
	}
	return outer.Choices[0].Message.Content
}
