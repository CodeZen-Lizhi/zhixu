// Command rag-model-fixture provides a deterministic OpenAI-compatible model
// endpoint for the explicit Compose RAG smoke profile. It is not a production
// provider and refuses non-loopback binds unless the Compose opt-in is set.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultAddress      = "127.0.0.1:18080"
	defaultModelVersion = "rag-smoke-v1"
	maxRequestBytes     = 4 << 20
)

type schemaContract struct {
	resultType string
	schemaID   string
	version    string
}

var supportedSchemas = map[schemaContract]struct{}{
	{resultType: "rag_query_plan", schemaID: "agent.rag-query-plan", version: "v1"}:           {},
	{resultType: "rag_answer", schemaID: "agent.rag-answer", version: "v2"}:                   {},
	{resultType: "faithfulness_review", schemaID: "agent.faithfulness-review", version: "v1"}: {},
}

type chatRequest struct {
	Model          string    `json:"model"`
	Messages       []message `json:"messages"`
	MaxTokens      int       `json:"max_tokens"`
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
	Role    string `json:"role"`
	Content string `json:"content"`
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

type structuredRequestGate struct {
	mu sync.Mutex

	armed   bool
	entered bool
	release chan struct{}
}

type structuredRequestGateState struct {
	Armed   bool `json:"armed"`
	Entered bool `json:"entered"`
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
	handler := newHandler(envOr("ZHIXU_RAG_FIXTURE_MODEL_VERSION", defaultModelVersion), apiKey)
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("rag model fixture listening on %s", address)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func newHandler(modelVersion, apiKey string) http.Handler {
	mux := http.NewServeMux()
	gate := &structuredRequestGate{}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		serveChat(w, r, modelVersion, apiKey, gate)
	})
	mux.HandleFunc("/control/block-next", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeFixtureControl(w, r, apiKey, http.MethodPost) {
			return
		}
		if !gate.arm() {
			http.Error(w, "structured request gate is already armed", http.StatusConflict)
			return
		}
		writeGateState(w, gate.state())
	})
	mux.HandleFunc("/control/state", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeFixtureControl(w, r, apiKey, http.MethodGet) {
			return
		}
		writeGateState(w, gate.state())
	})
	mux.HandleFunc("/control/release", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeFixtureControl(w, r, apiKey, http.MethodPost) {
			return
		}
		if !gate.open() {
			http.Error(w, "structured request gate is not armed", http.StatusConflict)
			return
		}
		writeGateState(w, gate.state())
	})
	return mux
}

func serveChat(w http.ResponseWriter, r *http.Request, modelVersion, apiKey string, gate *structuredRequestGate) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !hasBearerCanary(r.Header.Values("Authorization"), apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request chatRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, "request must contain exactly one JSON document", http.StatusBadRequest)
		return
	}
	if request.Model == "" || len(request.Messages) == 0 {
		http.Error(w, "unsupported request contract", http.StatusBadRequest)
		return
	}
	if request.ResponseFormat.Type == "" {
		if request.MaxTokens != 0 || len(request.Messages) != 1 || request.Messages[0].Role != "user" || request.Messages[0].Content != "test" {
			http.Error(w, "unsupported plain probe contract", http.StatusBadRequest)
			return
		}
		writeChatResponse(w, modelVersion, "ok")
		return
	}
	if request.ResponseFormat.Type != "json_schema" || !request.ResponseFormat.JSONSchema.Strict {
		http.Error(w, "unsupported request contract", http.StatusBadRequest)
		return
	}
	if err := gate.wait(r.Context()); err != nil {
		http.Error(w, "blocked request was cancelled", http.StatusRequestTimeout)
		return
	}
	var schema taskSchema
	if err := json.Unmarshal(request.ResponseFormat.JSONSchema.Schema, &schema); err != nil {
		http.Error(w, "invalid task schema", http.StatusBadRequest)
		return
	}
	contract := schemaContract{
		resultType: schema.Properties.ResultType.Const,
		schemaID:   schema.Properties.SchemaID.Const,
		version:    schema.Properties.SchemaVersion.Const,
	}
	if _, ok := supportedSchemas[contract]; !ok {
		http.Error(w, "unsupported task schema", http.StatusUnprocessableEntity)
		return
	}
	input, err := lastTaskInput(request.Messages)
	if err != nil {
		http.Error(w, "missing task input", http.StatusBadRequest)
		return
	}
	content, err := fixtureDocument(schema, input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		http.Error(w, "fixture encoding failed", http.StatusInternalServerError)
		return
	}
	writeChatResponse(w, modelVersion, string(encoded))
}

func (gate *structuredRequestGate) arm() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.armed {
		return false
	}
	gate.armed = true
	gate.entered = false
	gate.release = make(chan struct{})
	return true
}

func (gate *structuredRequestGate) wait(ctx context.Context) error {
	gate.mu.Lock()
	if !gate.armed || gate.entered {
		gate.mu.Unlock()
		return nil
	}
	gate.entered = true
	release := gate.release
	gate.mu.Unlock()
	select {
	case <-ctx.Done():
		gate.reset(release)
		return ctx.Err()
	case <-release:
		return nil
	}
}

func (gate *structuredRequestGate) open() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if !gate.armed || gate.release == nil {
		return false
	}
	close(gate.release)
	gate.armed = false
	gate.entered = false
	gate.release = nil
	return true
}

func (gate *structuredRequestGate) reset(release chan struct{}) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.release != release {
		return
	}
	gate.armed = false
	gate.entered = false
	gate.release = nil
}

func (gate *structuredRequestGate) state() structuredRequestGateState {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return structuredRequestGateState{Armed: gate.armed, Entered: gate.entered}
}

func authorizeFixtureControl(w http.ResponseWriter, r *http.Request, apiKey, method string) bool {
	if r.Method != method {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !hasBearerCanary(r.Header.Values("Authorization"), apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		http.Error(w, "control request body is not allowed", http.StatusBadRequest)
		return false
	}
	return true
}

func writeGateState(w http.ResponseWriter, state structuredRequestGateState) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
}

func writeChatResponse(w http.ResponseWriter, modelVersion, content string) {
	stop := "stop"
	response := map[string]any{
		"id": "chatcmpl-rag-smoke", "object": "chat.completion", "created": int64(1), "model": modelVersion,
		"choices":            []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content, "refusal": nil, "tool_calls": []any{}, "annotations": []any{}}, "finish_reason": stop, "logprobs": nil}},
		"usage":              map[string]any{"prompt_tokens": 32, "completion_tokens": 32, "total_tokens": 64, "prompt_tokens_details": nil, "completion_tokens_details": nil},
		"system_fingerprint": nil, "service_tier": nil,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func hasBearerCanary(values []string, apiKey string) bool {
	if apiKey == "" || len(values) != 1 {
		return false
	}
	expected := "Bearer " + apiKey
	return subtle.ConstantTimeCompare([]byte(values[0]), []byte(expected)) == 1
}

func lastTaskInput(messages []message) (map[string]any, error) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "user" {
			continue
		}
		content := messages[index].Content
		start, end := strings.IndexByte(content, '{'), strings.LastIndexByte(content, '}')
		if start < 0 || end < start {
			continue
		}
		var input map[string]any
		if json.Unmarshal([]byte(content[start:end+1]), &input) == nil {
			return input, nil
		}
	}
	return nil, errors.New("task input not found")
}

func fixtureDocument(schema taskSchema, input map[string]any) (map[string]any, error) {
	resultType, schemaID, version := schema.Properties.ResultType.Const, schema.Properties.SchemaID.Const, schema.Properties.SchemaVersion.Const
	modelRunRef, _ := input["model_run_ref"].(string)
	if modelRunRef == "" {
		return nil, errors.New("model_run_ref is required")
	}
	envelope := map[string]any{"result_type": resultType, "schema_id": schemaID, "schema_version": version, "model_run_ref": modelRunRef}
	switch resultType {
	case "rag_query_plan":
		envelope["payload"] = map[string]any{"intent": "answer from approved recovery evidence", "requires_clarification": false, "rewrites": []string{"approved recovery"}, "clarification_reason": "", "clarification_question": "", "suggested_scopes": []string{}}
	case "rag_answer":
		evidence, ok := firstObject(input["evidence"])
		if !ok {
			return nil, errors.New("rag answer input has no evidence")
		}
		citation, ok := evidence["citation"].(map[string]any)
		if !ok {
			return nil, errors.New("rag answer evidence has no citation")
		}
		citationID, _ := citation["id"].(string)
		topicID, topicName, ok := firstTopic(input["related_topics"])
		if !ok || citationID == "" {
			return nil, errors.New("rag answer input has no bound topic or citation")
		}
		envelope["payload"] = map[string]any{
			"conclusion": "Approved recovery requires durable replay without duplicate provider work.",
			"assertions": []any{map[string]any{"id": "assertion-smoke", "text": "Approved recovery requires durable replay without duplicate provider work.", "kind": "FACTUAL", "citation_ids": []string{citationID}}},
			"citations":  []any{citation}, "conflict_positions": []any{}, "conflict_summary": "",
			"related_topics":      []any{map[string]any{"topic_id": topicID, "name": topicName, "citation_ids": []string{citationID}}},
			"follow_up_questions": []string{"Which source supports this smoke result?"},
		}
	case "faithfulness_review":
		targets, _ := input["review_targets"].([]any)
		items := make([]any, 0, len(targets))
		for _, raw := range targets {
			target, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("invalid review target")
			}
			verdict := "SUPPORTED"
			if target["kind"] == "MODEL_INFERENCE" {
				verdict = "INFERENCE_DISCLOSED"
			}
			items = append(items, map[string]any{"assertion_id": target["id"], "verdict": verdict, "citation_ids": target["citation_ids"], "reason": "deterministic fixture verified the supplied target"})
		}
		if len(items) == 0 {
			return nil, errors.New("faithfulness input has no targets")
		}
		envelope["payload"] = map[string]any{"passed": true, "items": items, "summary": "all supplied targets are covered"}
	default:
		return nil, fmt.Errorf("unsupported schema result_type %q", resultType)
	}
	return envelope, nil
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
