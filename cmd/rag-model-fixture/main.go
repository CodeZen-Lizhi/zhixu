// Command rag-model-fixture provides a deterministic OpenAI-compatible model
// endpoint for the explicit Compose RAG smoke profile. It is not a production
// provider and refuses non-loopback binds unless the Compose opt-in is set.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddress      = "127.0.0.1:18080"
	defaultModelVersion = "rag-smoke-v1"
	maxRequestBytes     = 4 << 20
	fixtureFinalAnswer  = "Approved recovery requires durable replay without duplicate provider work."
	defaultBarrierPoll  = 50 * time.Millisecond
	defaultBarrierWait  = 2 * time.Minute
)

type schemaContract struct {
	resultType string
	schemaID   string
	version    string
}

type fixtureContractError struct {
	reason  string
	message string
}

// fixtureBarrier is an opt-in process-local drain witness. It never changes
// the default fixture behavior; a blocked request is released only by an
// explicitly created file, so a smoke cannot claim quiescence by timeout.
type fixtureBarrier struct {
	stage       string
	releasePath string
	maxWait     time.Duration
	poll        time.Duration
}

func (barrier fixtureBarrier) wait(ctx context.Context, stage string) error {
	if ctx == nil {
		return fixtureError("barrier_context_invalid", "fixture barrier context is unavailable")
	}
	if barrier.stage == "" || barrier.releasePath == "" || barrier.stage != stage {
		return nil
	}
	if barrier.maxWait <= 0 {
		barrier.maxWait = defaultBarrierWait
	}
	if barrier.poll <= 0 {
		barrier.poll = defaultBarrierPoll
	}
	log.Printf("rag model fixture barrier waiting stage=%s release=%s", stage, filepath.Base(barrier.releasePath))
	deadline := time.Now().Add(barrier.maxWait)
	for {
		if _, err := os.Stat(barrier.releasePath); err == nil {
			log.Printf("rag model fixture barrier released stage=%s", stage)
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fixtureError("barrier_release_unavailable", "fixture barrier release path is unavailable")
		}
		if time.Now().After(deadline) {
			return fixtureError("barrier_timeout", "fixture barrier release timed out")
		}
		timer := time.NewTimer(barrier.poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (err *fixtureContractError) Error() string { return err.message }

var supportedSchemas = map[schemaContract]struct{}{
	{resultType: "rag_query_plan", schemaID: "agent.rag-query-plan", version: "v1"}:           {},
	{resultType: "rag_answer", schemaID: "agent.rag-answer", version: "v2"}:                   {},
	{resultType: "rag_answer_metadata", schemaID: "agent.rag-answer-metadata", version: "v1"}: {},
	{resultType: "rag_answer_metadata", schemaID: "agent.rag-answer-metadata", version: "v2"}: {},
	{resultType: "refusal", schemaID: "agent.rag-answer-metadata-refusal", version: "v2"}:     {},
	{resultType: "faithfulness_review", schemaID: "agent.faithfulness-review", version: "v1"}: {},
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []message       `json:"messages"`
	MaxTokens      int             `json:"max_tokens"`
	Temperature    *float64        `json:"temperature"`
	Stream         bool            `json:"stream"`
	Tools          []tool          `json:"tools"`
	ToolChoice     json.RawMessage `json:"tool_choice"`
	StreamOptions  *streamOptions  `json:"stream_options"`
	ResponseFormat struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string          `json:"name"`
			Strict bool            `json:"strict"`
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	} `json:"response_format"`
}

type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []toolCall      `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type taskSchema struct {
	Properties struct {
		ResultType struct {
			Const string `json:"const"`
		} `json:"result_type"`
		SchemaID struct {
			Const string `json:"const"`
		} `json:"schema_id"`
		SchemaVersion struct {
			Const string `json:"const"`
		} `json:"schema_version"`
	} `json:"properties"`
}

func main() {
	address := envOr("ZHIXU_RAG_FIXTURE_ADDR", defaultAddress)
	if err := validateAddress(address, os.Getenv("ZHIXU_RAG_FIXTURE_COMPOSE") == "true"); err != nil {
		log.Fatal(err)
	}
	apiKey := os.Getenv("ZHIXU_RAG_FIXTURE_API_KEY")
	if apiKey == "" {
		log.Fatal("ZHIXU_RAG_FIXTURE_API_KEY must be set")
	}
	barrier := fixtureBarrier{stage: os.Getenv("ZHIXU_RAG_FIXTURE_BARRIER_STAGE"), releasePath: os.Getenv("ZHIXU_RAG_FIXTURE_BARRIER_RELEASE_FILE")}
	handler := newHandlerWithBarrier(envOr("ZHIXU_RAG_FIXTURE_MODEL_VERSION", defaultModelVersion), apiKey, barrier)
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("rag model fixture listening on %s", address)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func newHandler(modelVersion, apiKey string) http.Handler {
	return newHandlerWithBarrier(modelVersion, apiKey, fixtureBarrier{})
}

func newHandlerWithBarrier(modelVersion, apiKey string, barrier fixtureBarrier) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		serveChatWithBarrier(w, r, modelVersion, apiKey, barrier)
	})
	return mux
}

func serveChat(w http.ResponseWriter, r *http.Request, modelVersion, apiKey string) {
	serveChatWithBarrier(w, r, modelVersion, apiKey, fixtureBarrier{})
}

func serveChatWithBarrier(w http.ResponseWriter, r *http.Request, modelVersion, apiKey string, barrier fixtureBarrier) {
	if r.Method != http.MethodPost {
		rejectFixture(w, "request", "method_not_allowed", "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !hasBearerCanary(r.Header.Values("Authorization"), apiKey) {
		rejectFixture(w, "request", "authorization_invalid", "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		rejectFixture(w, "request", "content_type_invalid", "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request chatRequest
	if err := decoder.Decode(&request); err != nil {
		rejectFixture(w, "request", "request_decode_invalid", "invalid request", http.StatusBadRequest)
		return
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		rejectFixture(w, "request", "request_trailing_document", "request must contain exactly one JSON document", http.StatusBadRequest)
		return
	}
	if request.Model == "" || request.MaxTokens <= 0 || len(request.Messages) == 0 {
		rejectFixture(w, fixtureRequestStage(request), "request_contract_unsupported", "unsupported request contract", http.StatusBadRequest)
		return
	}
	if err := barrier.wait(r.Context(), fixtureRequestStage(request)); err != nil {
		rejectFixtureContract(w, fixtureRequestStage(request), err, http.StatusGatewayTimeout)
		return
	}
	if request.Stream {
		if err := validateStreamRequest(request); err != nil {
			rejectFixtureContract(w, "answer_stream", err, http.StatusUnprocessableEntity)
			return
		}
		serveFinalAnswerStream(w, modelVersion)
		return
	}
	if request.ResponseFormat.Type == "json_schema" {
		if err := serveStructuredChat(w, request, modelVersion); err != nil {
			rejectFixtureContract(w, fixtureRequestStage(request), err, http.StatusUnprocessableEntity)
		}
		return
	}
	if err := serveAgentChat(w, request, modelVersion); err != nil {
		rejectFixtureContract(w, fixtureRequestStage(request), err, http.StatusUnprocessableEntity)
	}
}

func rejectFixture(w http.ResponseWriter, stage, reason, message string, status int) {
	log.Printf("rag model fixture rejected stage=%s reason=%s status=%d", stage, reason, status)
	http.Error(w, message, status)
}

func rejectFixtureContract(w http.ResponseWriter, stage string, err error, status int) {
	reason := "fixture_contract_internal"
	message := "fixture contract rejected"
	var contractErr *fixtureContractError
	if errors.As(err, &contractErr) {
		reason = contractErr.reason
		message = contractErr.message
	}
	rejectFixture(w, stage, reason, message, status)
}

func fixtureError(reason, message string) error {
	return &fixtureContractError{reason: reason, message: message}
}

func fixtureRequestStage(request chatRequest) string {
	if request.Stream {
		return "answer_stream"
	}
	if request.ResponseFormat.Type == "json_schema" {
		if isQueryPlanProviderSchemaV2(request.ResponseFormat.JSONSchema.Schema) {
			return "structured_plan"
		}
		var document taskSchema
		if json.Unmarshal(request.ResponseFormat.JSONSchema.Schema, &document) == nil {
			switch document.Properties.ResultType.Const {
			case "rag_query_plan":
				return "structured_plan"
			case "rag_answer_metadata":
				return "structured_metadata"
			case "refusal":
				if document.Properties.SchemaID.Const == "agent.rag-answer-metadata-refusal" {
					return "structured_metadata"
				}
			case "faithfulness_review":
				return "structured_review"
			}
		}
		return "structured_unknown"
	}
	if hasToolMessage(request.Messages) {
		return "agent_followup"
	}
	return "agent_initial"
}

func hasBearerCanary(values []string, apiKey string) bool {
	if apiKey == "" || len(values) != 1 {
		return false
	}
	expected := "Bearer " + apiKey
	return subtle.ConstantTimeCompare([]byte(values[0]), []byte(expected)) == 1
}

func serveStructuredChat(w http.ResponseWriter, request chatRequest, modelVersion string) error {
	if len(request.Tools) != 0 || len(request.ToolChoice) != 0 || request.StreamOptions != nil || !request.ResponseFormat.JSONSchema.Strict {
		return fixtureError("structured_contract_unsupported", "unsupported structured request contract")
	}
	if isModelSettingsConnectionSchema(request.ResponseFormat.JSONSchema.Schema) {
		writeCompletion(w, modelVersion, `{"ok":true}`, nil, "stop")
		return nil
	}
	if isQueryPlanProviderSchemaV2(request.ResponseFormat.JSONSchema.Schema) {
		input, err := lastTaskInput(request.Messages)
		if err != nil {
			return fixtureError("structured_input_missing", "missing task input")
		}
		if _, leaked := input["model_run_ref"]; leaked {
			return fixtureError("query_plan_identity_leaked", "query plan provider input contains server identity")
		}
		writeCompletion(w, modelVersion, `{"i":"answer from approved recovery evidence","r":["approved recovery"],"d":"","q":"","s":[]}`, nil, "stop")
		return nil
	}
	var schema taskSchema
	if err := json.Unmarshal(request.ResponseFormat.JSONSchema.Schema, &schema); err != nil {
		return fixtureError("structured_schema_invalid", "invalid task schema")
	}
	contract := schemaContract{
		resultType: schema.Properties.ResultType.Const,
		schemaID:   schema.Properties.SchemaID.Const,
		version:    schema.Properties.SchemaVersion.Const,
	}
	if _, ok := supportedSchemas[contract]; !ok {
		return fixtureError("structured_schema_unsupported", "unsupported task schema")
	}
	input, err := lastTaskInput(request.Messages)
	if err != nil {
		return fixtureError("structured_input_missing", "missing task input")
	}
	content, err := fixtureDocument(schema, input)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return fixtureError("fixture_response_encoding_failed", "fixture encoding failed")
	}
	writeCompletion(w, modelVersion, string(encoded), nil, "stop")
	return nil
}

func isModelSettingsConnectionSchema(raw json.RawMessage) bool {
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil || len(schema) != 4 || schema["type"] != "object" || schema["additionalProperties"] != false {
		return false
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "ok" {
		return false
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 1 {
		return false
	}
	okProperty, ok := properties["ok"].(map[string]any)
	return ok && len(okProperty) == 1 && okProperty["type"] == "boolean"
}

func isQueryPlanProviderSchemaV2(raw json.RawMessage) bool {
	var schema struct {
		Type                 string                     `json:"type"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &schema) != nil || schema.Type != "object" || schema.AdditionalProperties == nil ||
		*schema.AdditionalProperties || len(schema.Required) != 5 || len(schema.Properties) != 5 {
		return false
	}
	required := map[string]struct{}{"i": {}, "r": {}, "d": {}, "q": {}, "s": {}}
	for _, field := range schema.Required {
		if _, ok := required[field]; !ok {
			return false
		}
		delete(required, field)
	}
	if len(required) != 0 {
		return false
	}
	for _, field := range []string{"i", "d", "q"} {
		if !schemaPropertyHasType(schema.Properties[field], "string") {
			return false
		}
	}
	for _, field := range []string{"r", "s"} {
		var property struct {
			Type  string          `json:"type"`
			Items json.RawMessage `json:"items"`
		}
		if json.Unmarshal(schema.Properties[field], &property) != nil || property.Type != "array" || !schemaPropertyHasType(property.Items, "string") {
			return false
		}
	}
	return true
}

func schemaPropertyHasType(raw json.RawMessage, expected string) bool {
	var property struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &property) == nil && property.Type == expected
}

func serveAgentChat(w http.ResponseWriter, request chatRequest, modelVersion string) error {
	if request.ResponseFormat.Type != "" || request.ResponseFormat.JSONSchema.Strict {
		return fixtureError("agent_response_format_unexpected", "unsupported agent request contract")
	}
	if len(request.Tools) == 0 {
		return fixtureError("agent_tools_missing", "unsupported agent request contract")
	}
	if request.StreamOptions != nil {
		return fixtureError("agent_stream_options_unexpected", "unsupported agent request contract")
	}
	if !hasDefaultAutoToolChoice(request.ToolChoice) {
		return fixtureError("agent_tool_choice_invalid", "unsupported agent request contract")
	}
	if !hasReadSourceTool(request.Tools) {
		return fixtureError("agent_tool_schema_invalid", "unsupported agent request contract")
	}
	if hasToolMessage(request.Messages) {
		expectedArguments, err := readSourceArguments(request.Messages)
		if err != nil {
			return err
		}
		if !hasFixtureReadSourceExchange(request.Messages, expectedArguments) {
			return fixtureError("agent_tool_exchange_unbound", "agent tool result is not bound to the fixture call")
		}
		writeCompletion(w, modelVersion, "Approved evidence was inspected for the final answer.", nil, "stop")
		return nil
	}
	arguments, err := readSourceArguments(request.Messages)
	if err != nil {
		return err
	}
	writeCompletion(w, modelVersion, "", []toolCall{{
		ID: "fixture-read-source-1", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "ReadSource", Arguments: arguments},
	}}, "tool_calls")
	return nil
}

func validateStreamRequest(request chatRequest) error {
	if request.ResponseFormat.Type != "" || request.ResponseFormat.JSONSchema.Strict || request.StreamOptions == nil || !request.StreamOptions.IncludeUsage ||
		len(request.Tools) != 0 || !hasExactToolChoice(request.ToolChoice, "none") {
		return fixtureError("answer_stream_contract_unsupported", "unsupported stream request contract")
	}
	return nil
}

func serveFinalAnswerStream(w http.ResponseWriter, modelVersion string) {
	w.Header().Set("Content-Type", "text/event-stream")
	frames := []string{
		`data: {"id":"stream-rag-smoke","object":"chat.completion.chunk","created":1,"model":"` + modelVersion + `","choices":[{"index":0,"delta":{"role":"assistant","content":"Approved recovery requires durable "},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"stream-rag-smoke","object":"chat.completion.chunk","created":1,"model":"` + modelVersion + `","choices":[{"index":0,"delta":{"content":"replay without duplicate provider work."},"finish_reason":"stop"}]}` + "\n\n",
		`data: {"id":"stream-rag-smoke","object":"chat.completion.chunk","created":1,"model":"` + modelVersion + `","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	for _, frame := range frames {
		_, _ = io.WriteString(w, frame)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func writeCompletion(w http.ResponseWriter, modelVersion, content string, toolCalls []toolCall, finishReason string) {
	message := map[string]any{"role": "assistant", "content": content}
	if len(toolCalls) != 0 {
		message["content"] = nil
		message["tool_calls"] = toolCalls
	}
	response := map[string]any{
		"id": "chatcmpl-rag-smoke", "object": "chat.completion", "created": int64(1), "model": modelVersion,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}},
		"usage":   map[string]any{"prompt_tokens": 32, "completion_tokens": 32, "total_tokens": 64},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func lastTaskInput(messages []message) (map[string]any, error) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "user" {
			continue
		}
		var content string
		if json.Unmarshal(messages[index].Content, &content) != nil {
			continue
		}
		start, end := strings.IndexByte(content, '{'), strings.LastIndexByte(content, '}')
		if start < 0 || end < start {
			continue
		}
		var input map[string]any
		if json.Unmarshal([]byte(content[start:end+1]), &input) == nil {
			return input, nil
		}
	}
	return nil, fixtureError("task_input_not_found", "task input not found")
}

func fixtureDocument(schema taskSchema, input map[string]any) (map[string]any, error) {
	resultType, schemaID, version := schema.Properties.ResultType.Const, schema.Properties.SchemaID.Const, schema.Properties.SchemaVersion.Const
	modelRunRef, _ := input["model_run_ref"].(string)
	metadataV2 := resultType == "rag_answer_metadata" && version == "v2"
	metadataRefusalV2 := resultType == "refusal" && schemaID == "agent.rag-answer-metadata-refusal" && version == "v2"
	identityless := metadataV2 || metadataRefusalV2
	if !identityless && modelRunRef == "" {
		return nil, fixtureError("model_run_ref_missing", "model_run_ref is required")
	}
	envelope := map[string]any{"result_type": resultType, "schema_id": schemaID, "schema_version": version}
	if !identityless {
		envelope["model_run_ref"] = modelRunRef
	}
	switch resultType {
	case "rag_query_plan":
		envelope["payload"] = map[string]any{"intent": "answer from approved recovery evidence", "requires_clarification": false, "rewrites": []string{"approved recovery"}, "clarification_reason": "", "clarification_question": "", "suggested_scopes": []string{}}
	case "rag_answer":
		evidence, ok := firstObject(input["evidence"])
		if !ok {
			return nil, fixtureError("answer_evidence_missing", "rag answer input has no evidence")
		}
		citation, ok := evidence["citation"].(map[string]any)
		if !ok {
			return nil, fixtureError("answer_citation_missing", "rag answer evidence has no citation")
		}
		citationID, _ := citation["id"].(string)
		topicID, topicName, ok := firstTopic(input["related_topics"])
		if !ok || citationID == "" {
			return nil, fixtureError("answer_topic_binding_missing", "rag answer input has no bound topic or citation")
		}
		envelope["payload"] = map[string]any{
			"conclusion": fixtureFinalAnswer,
			"assertions": []any{map[string]any{
				"id": "assertion-smoke", "text": fixtureFinalAnswer, "kind": "FACTUAL", "citation_ids": []string{citationID},
			}},
			"citations": []any{citation}, "conflict_positions": []any{}, "conflict_summary": "",
			"related_topics": []any{map[string]any{
				"topic_id": topicID, "name": topicName, "citation_ids": []string{citationID},
			}},
			"follow_up_questions": []string{"Which source supports this smoke result?"},
		}
	case "rag_answer_metadata":
		finalAnswer, _ := input["final_answer_markdown"].(string)
		if finalAnswer != fixtureFinalAnswer {
			return nil, fixtureError("metadata_answer_binding_invalid", "rag metadata input is not bound to the streamed answer")
		}
		if version == "v1" {
			answerSHA256, _ := input["answer_sha256"].(string)
			if !isSHA256(answerSHA256) {
				return nil, fixtureError("metadata_answer_hash_invalid", "rag metadata input has no answer sha256")
			}
			if !matchesSHA256(finalAnswer, answerSHA256) {
				return nil, fixtureError("metadata_answer_binding_invalid", "rag metadata input is not bound to the streamed answer")
			}
			context, ok := input["generation_context"].(map[string]any)
			if !ok {
				return nil, fixtureError("metadata_context_missing", "rag metadata input has no generation context")
			}
			evidence, ok := firstObject(context["evidence"])
			if !ok {
				return nil, fixtureError("metadata_evidence_missing", "rag metadata input has no evidence")
			}
			citation, ok := evidence["citation"].(map[string]any)
			if !ok {
				return nil, fixtureError("metadata_citation_missing", "rag metadata evidence has no citation")
			}
			citationID, _ := citation["id"].(string)
			topicID, topicName, ok := firstTopic(context["related_topics"])
			if !ok || citationID == "" {
				return nil, fixtureError("metadata_topic_binding_missing", "rag metadata input has no bound topic or citation")
			}
			envelope["payload"] = map[string]any{
				"answer_sha256": answerSHA256,
				"assertions":    []any{map[string]any{"id": "assertion-smoke", "text": "Approved recovery requires durable replay without duplicate provider work.", "kind": "FACTUAL", "citation_ids": []string{citationID}}},
				"citations":     []any{citation}, "conflict_positions": []any{}, "conflict_summary": "",
				"related_topics":      []any{map[string]any{"topic_id": topicID, "name": topicName, "citation_ids": []string{citationID}}},
				"follow_up_questions": []string{"Which source supports this smoke result?"},
			}
			break
		}
		evidenceRefs, err := fixtureMetadataRefs(input["evidence"], "E", true)
		if err != nil {
			return nil, fixtureError("metadata_evidence_missing", "rag metadata input has no evidence ref")
		}
		topicRefs, err := fixtureMetadataRefs(input["related_topics"], "T", true)
		if err != nil {
			return nil, fixtureError("metadata_topic_binding_missing", "rag metadata input has no bound topic ref")
		}
		conflictRefs, err := fixtureMetadataRefs(input["conflicts"], "C", false)
		if err != nil || len(conflictRefs) == 1 {
			return nil, fixtureError("metadata_conflict_binding_invalid", "rag metadata input has invalid conflict refs")
		}
		positions := make([]any, 0, len(conflictRefs))
		for _, conflictRef := range conflictRefs {
			positions = append(positions, map[string]any{
				"conflict_ref": conflictRef, "position": "The supplied approved evidence describes this disputed position.",
			})
		}
		conflictSummary := ""
		if len(positions) > 0 {
			conflictSummary = "The approved evidence contains multiple disputed positions."
		}
		envelope["payload"] = map[string]any{
			"assertions": []any{map[string]any{
				"id": "assertion-smoke", "text": "Approved recovery requires durable replay without duplicate provider work.",
				"kind": "FACTUAL", "evidence_refs": evidenceRefs,
			}},
			"conflict_positions": positions, "conflict_summary": conflictSummary,
			"related_topic_refs":  topicRefs,
			"follow_up_questions": []string{"Which approved evidence should be reviewed next?"},
		}
	case "refusal":
		if !metadataRefusalV2 {
			return nil, fixtureError("structured_refusal_schema_unsupported", "unsupported refusal schema")
		}
		envelope["payload"] = map[string]any{
			"reason_code": "VALIDATION_EXHAUSTED", "summary": "Metadata validation could not be completed.",
			"retrieval_scope": "approved workspace evidence", "missing_requirements": []string{"valid bounded metadata"},
			"suggested_actions": []string{"retry with approved evidence"},
		}
	case "faithfulness_review":
		targets, _ := input["review_targets"].([]any)
		items := make([]any, 0, len(targets))
		for _, raw := range targets {
			target, ok := raw.(map[string]any)
			if !ok {
				return nil, fixtureError("review_target_invalid", "invalid review target")
			}
			verdict := "SUPPORTED"
			if target["kind"] == "MODEL_INFERENCE" {
				verdict = "INFERENCE_DISCLOSED"
			}
			items = append(items, map[string]any{"assertion_id": target["id"], "verdict": verdict, "citation_ids": target["citation_ids"], "reason": "deterministic fixture verified the supplied target"})
		}
		if len(items) == 0 {
			return nil, fixtureError("review_targets_missing", "faithfulness input has no targets")
		}
		envelope["payload"] = map[string]any{"passed": true, "items": items, "summary": "all supplied targets are covered"}
	default:
		return nil, fixtureError("structured_result_type_unsupported", "unsupported schema result type")
	}
	return envelope, nil
}

func hasExactToolChoice(raw json.RawMessage, expected string) bool {
	var actual string
	return len(raw) != 0 && json.Unmarshal(raw, &actual) == nil && actual == expected
}

func hasDefaultAutoToolChoice(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || hasExactToolChoice(raw, "auto")
}

func hasReadSourceTool(tools []tool) bool {
	readSource := 0
	seen := make(map[string]struct{}, len(tools))
	for _, candidate := range tools {
		if candidate.Type != "function" || !fixtureAgentToolName(candidate.Function.Name) || !jsonObject(candidate.Function.Parameters) {
			return false
		}
		if _, duplicate := seen[candidate.Function.Name]; duplicate {
			return false
		}
		seen[candidate.Function.Name] = struct{}{}
		if candidate.Function.Name == "ReadSource" {
			readSource++
		}
	}
	return readSource == 1
}

func fixtureAgentToolName(value string) bool {
	return value == "ReadSource" || value == "ValidateCitation"
}

func hasToolMessage(messages []message) bool {
	for _, message := range messages {
		if message.Role == "tool" {
			return true
		}
	}
	return false
}

func hasFixtureReadSourceExchange(messages []message, expectedArguments string) bool {
	seenCall := false
	seenTool := false
	for _, message := range messages {
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				if call.ID == "fixture-read-source-1" && call.Type == "function" && call.Function.Name == "ReadSource" && call.Function.Arguments == expectedArguments {
					seenCall = true
				}
			}
		}
		if message.Role == "tool" && message.ToolCallID == "fixture-read-source-1" &&
			fixtureReadSourceToolResult(message.Content, expectedArguments) {
			seenTool = true
		}
	}
	return seenCall && seenTool
}

func readSourceArguments(messages []message) (string, error) {
	input, err := lastTaskInput(messages)
	if err != nil {
		return "", fixtureError("agent_context_missing", "agent request has no frozen generation context")
	}
	evidence, ok := firstObject(input["evidence"])
	if !ok {
		return "", fixtureError("agent_evidence_missing", "agent request has no approved evidence")
	}
	evidenceRef, _ := evidence["ref"].(string)
	if !fixtureMetadataRef(evidenceRef, "E") {
		return "", fixtureError("agent_evidence_ref_invalid", "agent evidence has no valid short ref")
	}
	arguments, err := json.Marshal(map[string]string{"evidence_ref": evidenceRef})
	if err != nil {
		return "", fixtureError("fixture_response_encoding_failed", "fixture encoding failed")
	}
	return string(arguments), nil
}

func fixtureReadSourceToolResult(raw json.RawMessage, expectedArguments string) bool {
	var expected struct {
		EvidenceRef string `json:"evidence_ref"`
	}
	if json.Unmarshal([]byte(expectedArguments), &expected) != nil || !fixtureMetadataRef(expected.EvidenceRef, "E") {
		return false
	}
	var content string
	if json.Unmarshal(raw, &content) != nil {
		return false
	}
	var result map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &result) != nil || len(result) != 2 {
		return false
	}
	var evidenceRef, excerpt string
	if json.Unmarshal(result["evidence_ref"], &evidenceRef) != nil || json.Unmarshal(result["excerpt"], &excerpt) != nil {
		return false
	}
	return evidenceRef == expected.EvidenceRef && strings.TrimSpace(excerpt) != ""
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func matchesSHA256(value, expected string) bool {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum) == expected
}

func fixtureMetadataRefs(raw any, prefix string, required bool) ([]string, error) {
	items, ok := raw.([]any)
	if !ok || (required && len(items) == 0) {
		return nil, errors.New("metadata references are missing")
	}
	refs := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, errors.New("metadata reference is invalid")
		}
		ref, _ := item["ref"].(string)
		if !fixtureMetadataRef(ref, prefix) {
			return nil, errors.New("metadata reference is invalid")
		}
		if _, duplicate := seen[ref]; duplicate {
			return nil, errors.New("metadata reference is duplicated")
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs, nil
}

func fixtureMetadataRef(ref, prefix string) bool {
	if !strings.HasPrefix(ref, prefix) || len(ref) <= len(prefix) || ref[len(prefix)] == '0' {
		return false
	}
	number, err := strconv.Atoi(ref[len(prefix):])
	return err == nil && number > 0 && ref == prefix+strconv.Itoa(number)
}

func jsonObject(raw []byte) bool {
	trimmed := strings.TrimSpace(string(raw))
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid([]byte(trimmed))
}

func messageContentJSONObject(raw json.RawMessage) bool {
	var content string
	return json.Unmarshal(raw, &content) == nil && jsonObject([]byte(content))
}

func firstObject(raw any) (map[string]any, bool) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	value, ok := items[0].(map[string]any)
	return value, ok
}

func firstTopic(raw any) (string, string, bool) {
	values, ok := raw.(map[string]any)
	if !ok || len(values) == 0 {
		return "", "", false
	}
	var selected string
	for id := range values {
		if selected == "" || id < selected {
			selected = id
		}
	}
	topic, ok := values[selected].(map[string]any)
	if !ok {
		return "", "", false
	}
	name, ok := topic["name"].(string)
	return selected, name, ok && name != ""
}

func validateAddress(address string, compose bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("fixture address must include host and port")
	}
	if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || host == "localhost" {
		return nil
	}
	if compose && (host == "0.0.0.0" || host == "::") {
		return nil
	}
	return errors.New("fixture only permits loopback or explicit Compose wildcard bind")
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
