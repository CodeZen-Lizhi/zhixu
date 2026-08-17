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
	defaultAddress                               = "127.0.0.1:18080"
	defaultModelVersion                          = "rag-smoke-v1"
	maxRequestBytes                              = 4 << 20
	maxWorkspaceAnalysisFixtureExcerptBytes      = 4 << 10
	fixtureFinalAnswer                           = "Approved recovery requires durable replay without duplicate provider work."
	fixtureWorkspaceAnalysisCandidate            = "The supplied workspace evidence supports this bounded fixture result [E1]."
	workspaceAnalysisCandidateResponseSchemaName = "zhixu_workspace_analysis_candidate_v1"
	defaultBarrierPoll                           = 50 * time.Millisecond
	defaultBarrierWait                           = 2 * time.Minute
	fixtureBarrierTokenBytes                     = 16
	maxFixtureAnswerStreamFrameDelay             = 5 * time.Second
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

// fixtureBarrier is an opt-in smoke-local drain witness. It never changes
// the default fixture behavior; a blocked request is released only by an
// explicitly created file, so a smoke cannot claim quiescence by timeout.
type fixtureBarrier struct {
	stage             string
	releasePath       string
	enteredPath       string
	settledPath       string
	armedPath         string
	lockPath          string
	maxWait           time.Duration
	poll              time.Duration
	afterClaimForTest func()
}

type fixtureHandlerConfig struct {
	barrier                fixtureBarrier
	answerStreamFrameDelay time.Duration
}

func (barrier fixtureBarrier) wait(ctx context.Context, stage string) (waitErr error) {
	if ctx == nil {
		return fixtureError("barrier_context_invalid", "fixture barrier context is unavailable")
	}
	if barrier.stage == "" || barrier.stage != stage {
		return nil
	}
	if barrier.releasePath == "" || barrier.enteredPath == "" || barrier.settledPath == "" || barrier.armedPath == "" || barrier.lockPath == "" ||
		barrier.releasePath == barrier.enteredPath || barrier.releasePath == barrier.settledPath || barrier.releasePath == barrier.armedPath ||
		barrier.releasePath == barrier.lockPath || barrier.enteredPath == barrier.settledPath || barrier.enteredPath == barrier.armedPath ||
		barrier.enteredPath == barrier.lockPath || barrier.settledPath == barrier.armedPath || barrier.settledPath == barrier.lockPath || barrier.armedPath == barrier.lockPath {
		return fixtureError("barrier_configuration_invalid", "fixture barrier configuration is unavailable")
	}
	if barrier.maxWait <= 0 {
		barrier.maxWait = defaultBarrierWait
	}
	if barrier.poll <= 0 {
		barrier.poll = defaultBarrierPoll
	}
	deadline := time.Now().Add(barrier.maxWait)
	if err := barrier.acquireClaim(ctx, deadline); err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(barrier.lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			waitErr = errors.Join(waitErr, fixtureError("barrier_lock_unavailable", "fixture barrier claim lock could not be released"))
		}
	}()
	token, err := readFixtureBarrierToken(barrier.armedPath)
	if err != nil {
		return err
	}
	if barrier.afterClaimForTest != nil {
		barrier.afterClaimForTest()
	}
	if err := writeFixtureBarrierAcknowledgement(barrier.enteredPath, token, "entered"); err != nil {
		return err
	}
	// This defer runs before the claim-release defer above, so re-arm can only
	// observe a generation after its settled acknowledgement is durable.
	defer func() {
		if err := writeFixtureBarrierAcknowledgement(barrier.settledPath, token, "settled"); err != nil {
			waitErr = errors.Join(waitErr, err)
		}
	}()
	log.Printf("rag model fixture barrier waiting stage=%s release=%s", stage, filepath.Base(barrier.releasePath))
	for {
		releasedToken, found, err := readOptionalFixtureBarrierToken(barrier.releasePath)
		if err != nil {
			return err
		}
		if found && releasedToken == token {
			log.Printf("rag model fixture barrier released stage=%s", stage)
			return nil
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

func (barrier fixtureBarrier) acquireClaim(ctx context.Context, deadline time.Time) error {
	for {
		err := os.Mkdir(barrier.lockPath, 0o700)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fixtureError("barrier_lock_unavailable", "fixture barrier claim lock is unavailable")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fixtureError("barrier_lock_timeout", "fixture barrier claim lock timed out")
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

func readFixtureBarrierToken(path string) (string, error) {
	token, found, err := readOptionalFixtureBarrierToken(path)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fixtureError("barrier_armed_unavailable", "fixture barrier generation is unavailable")
	}
	return token, nil
}

func readOptionalFixtureBarrierToken(path string) (string, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fixtureError("barrier_release_unavailable", "fixture barrier release path is unavailable")
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, fixtureBarrierTokenBytes*2+2))
	if err != nil {
		return "", false, fixtureError("barrier_release_unavailable", "fixture barrier release path is unavailable")
	}
	token := strings.TrimSuffix(string(encoded), "\n")
	if len(encoded) != fixtureBarrierTokenBytes*2+1 || !validFixtureBarrierToken(token) {
		return "", false, nil
	}
	return token, true, nil
}

func validFixtureBarrierToken(token string) bool {
	if len(token) != fixtureBarrierTokenBytes*2 {
		return false
	}
	for _, character := range token {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func writeFixtureBarrierAcknowledgement(path, token, acknowledgement string) error {
	if !validFixtureBarrierToken(token) {
		return fixtureError("barrier_armed_unavailable", "fixture barrier generation is unavailable")
	}
	if acknowledgement != "entered" && acknowledgement != "settled" {
		return fixtureError("barrier_acknowledgement_invalid", "fixture barrier acknowledgement is invalid")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fixtureError("barrier_"+acknowledgement+"_conflict", "fixture barrier "+acknowledgement+" acknowledgement already exists")
		}
		return fixtureError("barrier_"+acknowledgement+"_unavailable", "fixture barrier "+acknowledgement+" acknowledgement is unavailable")
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return fixtureError("barrier_"+acknowledgement+"_unavailable", "fixture barrier "+acknowledgement+" acknowledgement is unavailable")
	}
	if err := file.Close(); err != nil {
		return fixtureError("barrier_"+acknowledgement+"_unavailable", "fixture barrier "+acknowledgement+" acknowledgement is unavailable")
	}
	return nil
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
	barrier := fixtureBarrier{
		stage:       os.Getenv("ZHIXU_RAG_FIXTURE_BARRIER_STAGE"),
		releasePath: os.Getenv("ZHIXU_RAG_FIXTURE_BARRIER_RELEASE_FILE"),
		enteredPath: os.Getenv("ZHIXU_RAG_FIXTURE_BARRIER_ENTERED_FILE"),
		settledPath: os.Getenv("ZHIXU_RAG_FIXTURE_BARRIER_SETTLED_FILE"),
		armedPath:   os.Getenv("ZHIXU_RAG_FIXTURE_BARRIER_ARMED_FILE"),
		lockPath:    os.Getenv("ZHIXU_RAG_FIXTURE_BARRIER_LOCK_DIR"),
	}
	answerStreamFrameDelay, err := parseFixtureAnswerStreamFrameDelay(os.Getenv("ZHIXU_RAG_FIXTURE_ANSWER_STREAM_FRAME_DELAY_MS"))
	if err != nil {
		log.Fatal(err)
	}
	handler := newHandlerWithConfig(envOr("ZHIXU_RAG_FIXTURE_MODEL_VERSION", defaultModelVersion), apiKey, fixtureHandlerConfig{
		barrier: barrier, answerStreamFrameDelay: answerStreamFrameDelay,
	})
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("rag model fixture listening on %s", address)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func newHandler(modelVersion, apiKey string) http.Handler {
	return newHandlerWithConfig(modelVersion, apiKey, fixtureHandlerConfig{})
}

func newHandlerWithBarrier(modelVersion, apiKey string, barrier fixtureBarrier) http.Handler {
	return newHandlerWithConfig(modelVersion, apiKey, fixtureHandlerConfig{barrier: barrier})
}

func newHandlerWithConfig(modelVersion, apiKey string, config fixtureHandlerConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		serveChatWithConfig(w, r, modelVersion, apiKey, config)
	})
	return mux
}

func serveChat(w http.ResponseWriter, r *http.Request, modelVersion, apiKey string) {
	serveChatWithBarrier(w, r, modelVersion, apiKey, fixtureBarrier{})
}

func serveChatWithBarrier(w http.ResponseWriter, r *http.Request, modelVersion, apiKey string, barrier fixtureBarrier) {
	serveChatWithConfig(w, r, modelVersion, apiKey, fixtureHandlerConfig{barrier: barrier})
}

func serveChatWithConfig(w http.ResponseWriter, r *http.Request, modelVersion, apiKey string, config fixtureHandlerConfig) {
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
	stage := fixtureRequestStage(request)
	log.Printf("rag model fixture request stage=%s", stage)
	if request.Stream {
		workspaceCandidate, err := validateStreamRequest(request)
		if err != nil {
			rejectFixtureContract(w, stage, err, http.StatusUnprocessableEntity)
			return
		}
		if workspaceCandidate {
			evidenceRefs, inputErr := workspaceAnalysisCandidateFixtureInput(request.Messages)
			if inputErr != nil {
				rejectFixtureContract(w, stage, inputErr, http.StatusUnprocessableEntity)
				return
			}
			if err := config.barrier.wait(r.Context(), stage); err != nil {
				rejectFixtureContract(w, stage, err, http.StatusGatewayTimeout)
				return
			}
			serveWorkspaceAnalysisCandidateStream(w, modelVersion, evidenceRefs[0])
			return
		}
		if err := config.barrier.wait(r.Context(), stage); err != nil {
			rejectFixtureContract(w, stage, err, http.StatusGatewayTimeout)
			return
		}
		serveFinalAnswerStream(r.Context(), w, modelVersion, config.answerStreamFrameDelay)
		return
	}
	if err := config.barrier.wait(r.Context(), stage); err != nil {
		rejectFixtureContract(w, stage, err, http.StatusGatewayTimeout)
		return
	}
	if request.ResponseFormat.Type == "json_schema" {
		if err := serveStructuredChat(w, request, modelVersion); err != nil {
			rejectFixtureContract(w, stage, err, http.StatusUnprocessableEntity)
		}
		return
	}
	if err := serveAgentChat(w, request, modelVersion); err != nil {
		rejectFixtureContract(w, stage, err, http.StatusUnprocessableEntity)
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
		if request.ResponseFormat.JSONSchema.Name == workspaceAnalysisCandidateResponseSchemaName {
			return "workspace_analysis_candidate_stream"
		}
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
		workspaceAnalysis, err := validateWorkspaceAnalysisPlannerFixtureInput(input)
		if err != nil {
			return err
		}
		content := `{"i":"answer from approved recovery evidence","r":["approved recovery"],"d":"","q":"","s":[]}`
		if workspaceAnalysis {
			content = workspaceAnalysisPlannerFixtureResponse(input)
		}
		writeCompletion(w, modelVersion, content, nil, "stop")
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
	if contract.resultType == "faithfulness_review" && !isFaithfulnessReviewSchema(request.ResponseFormat.JSONSchema.Schema) {
		return fixtureError("faithfulness_review_schema_unsupported", "unsupported faithfulness review schema")
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
	var schema map[string]json.RawMessage
	if json.Unmarshal(raw, &schema) != nil || !hasExactJSONKeys(schema, "type", "additionalProperties", "required", "properties") ||
		!jsonRawEquals(schema["type"], "object") || !jsonRawEquals(schema["additionalProperties"], false) ||
		!hasExactJSONStringSet(schema["required"], "i", "r", "d", "q", "s") {
		return false
	}
	var properties map[string]json.RawMessage
	if json.Unmarshal(schema["properties"], &properties) != nil || !hasExactJSONKeys(properties, "i", "r", "d", "q", "s") ||
		!hasExactStringSchema(properties["i"], 1, 512) || !hasExactStringSchema(properties["d"], 0, 512) ||
		!hasExactStringSchema(properties["q"], 0, 1024) {
		return false
	}
	return hasExactProviderStringArraySchema(properties["r"], 0, 3, 512) && hasExactProviderStringArraySchema(properties["s"], 0, 10, 256)
}

func hasExactProviderStringArraySchema(raw json.RawMessage, minItems, maxItems, maxLength int) bool {
	var property struct {
		Type     string          `json:"type"`
		MinItems int             `json:"minItems"`
		MaxItems int             `json:"maxItems"`
		Items    json.RawMessage `json:"items"`
	}
	return json.Unmarshal(raw, &property) == nil && property.Type == "array" && property.MinItems == minItems &&
		property.MaxItems == maxItems && hasExactStringSchema(property.Items, 1, maxLength)
}

func isWorkspaceAnalysisCandidateProviderSchema(raw json.RawMessage) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || !hasExactJSONKeys(root, "type", "additionalProperties", "required", "properties") ||
		!jsonRawEquals(root["type"], "object") || !jsonRawEquals(root["additionalProperties"], false) ||
		!hasExactJSONStringSet(root["required"], "result_type", "schema_id", "schema_version", "payload") {
		return false
	}
	var properties map[string]json.RawMessage
	if json.Unmarshal(root["properties"], &properties) != nil || !hasExactJSONKeys(properties, "result_type", "schema_id", "schema_version", "payload") ||
		!jsonRawEquals(properties["result_type"], map[string]any{"const": "workspace_analysis_candidate"}) ||
		!jsonRawEquals(properties["schema_id"], map[string]any{"const": "agent.workspace-analysis-candidate"}) ||
		!jsonRawEquals(properties["schema_version"], map[string]any{"const": "1"}) {
		return false
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(properties["payload"], &payload) != nil || !hasExactJSONKeys(payload, "type", "additionalProperties", "required", "properties") ||
		!jsonRawEquals(payload["type"], "object") || !jsonRawEquals(payload["additionalProperties"], false) ||
		!hasExactJSONStringSet(payload["required"], "answer_markdown", "citation_refs", "proposal_suggestion") {
		return false
	}
	var payloadProperties map[string]json.RawMessage
	if json.Unmarshal(payload["properties"], &payloadProperties) != nil || !hasExactJSONKeys(payloadProperties, "answer_markdown", "citation_refs", "proposal_suggestion") ||
		!hasExactStringSchema(payloadProperties["answer_markdown"], 1, 64*1024) ||
		!hasWorkspaceAnalysisCitationRefsSchema(payloadProperties["citation_refs"]) {
		return false
	}
	return hasWorkspaceAnalysisProposalSchema(payloadProperties["proposal_suggestion"])
}

func isFaithfulnessReviewSchema(raw json.RawMessage) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || !hasExactJSONKeys(root, "type", "additionalProperties", "required", "properties") ||
		!jsonRawEquals(root["type"], "object") || !jsonRawEquals(root["additionalProperties"], false) ||
		!hasExactJSONStringSet(root["required"], "result_type", "schema_id", "schema_version", "model_run_ref", "payload") {
		return false
	}
	var properties map[string]json.RawMessage
	if json.Unmarshal(root["properties"], &properties) != nil || !hasExactJSONKeys(properties, "result_type", "schema_id", "schema_version", "model_run_ref", "payload") ||
		!jsonRawEquals(properties["result_type"], map[string]any{"const": "faithfulness_review"}) ||
		!jsonRawEquals(properties["schema_id"], map[string]any{"const": "agent.faithfulness-review"}) ||
		!jsonRawEquals(properties["schema_version"], map[string]any{"const": "v1"}) ||
		!jsonRawEquals(properties["model_run_ref"], map[string]any{"type": "string", "format": "uuid"}) {
		return false
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(properties["payload"], &payload) != nil || !hasExactJSONKeys(payload, "type", "additionalProperties", "required", "properties") ||
		!jsonRawEquals(payload["type"], "object") || !jsonRawEquals(payload["additionalProperties"], false) ||
		!hasExactJSONStringSet(payload["required"], "passed", "items", "summary") {
		return false
	}
	var payloadProperties map[string]json.RawMessage
	if json.Unmarshal(payload["properties"], &payloadProperties) != nil || !hasExactJSONKeys(payloadProperties, "passed", "items", "summary") ||
		!jsonRawEquals(payloadProperties["passed"], map[string]any{"type": "boolean"}) || !hasExactStringSchema(payloadProperties["summary"], 1, 4096) {
		return false
	}
	return hasFaithfulnessReviewItemsSchema(payloadProperties["items"])
}

func hasFaithfulnessReviewItemsSchema(raw json.RawMessage) bool {
	var items struct {
		Type     string          `json:"type"`
		MinItems int             `json:"minItems"`
		MaxItems int             `json:"maxItems"`
		Items    json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &items) != nil || items.Type != "array" || items.MinItems != 1 || items.MaxItems != 500 {
		return false
	}
	var item map[string]json.RawMessage
	if json.Unmarshal(items.Items, &item) != nil || !hasExactJSONKeys(item, "type", "additionalProperties", "required", "properties") ||
		!jsonRawEquals(item["type"], "object") || !jsonRawEquals(item["additionalProperties"], false) ||
		!hasExactJSONStringSet(item["required"], "assertion_id", "verdict", "citation_ids", "reason") {
		return false
	}
	var properties map[string]json.RawMessage
	return json.Unmarshal(item["properties"], &properties) == nil && hasExactJSONKeys(properties, "assertion_id", "verdict", "citation_ids", "reason") &&
		hasExactStringSchema(properties["assertion_id"], 1, 128) &&
		jsonRawEquals(properties["verdict"], map[string]any{"type": "string", "enum": []string{"SUPPORTED", "UNSUPPORTED", "INFERENCE_DISCLOSED"}}) &&
		hasExactProviderStringArraySchema(properties["citation_ids"], 0, 500, 128) && hasExactStringSchema(properties["reason"], 1, 2048)
}

func hasWorkspaceAnalysisProposalSchema(raw json.RawMessage) bool {
	var variants struct {
		AnyOf []json.RawMessage `json:"anyOf"`
	}
	if json.Unmarshal(raw, &variants) != nil || len(variants.AnyOf) != 2 || !jsonRawEquals(variants.AnyOf[0], map[string]any{"type": "null"}) {
		return false
	}
	var proposal map[string]json.RawMessage
	if json.Unmarshal(variants.AnyOf[1], &proposal) != nil || !hasExactJSONKeys(proposal, "type", "additionalProperties", "required", "properties") ||
		!jsonRawEquals(proposal["type"], "object") || !jsonRawEquals(proposal["additionalProperties"], false) ||
		!hasExactJSONStringSet(proposal["required"], "summary", "citation_refs") {
		return false
	}
	var properties map[string]json.RawMessage
	return json.Unmarshal(proposal["properties"], &properties) == nil && hasExactJSONKeys(properties, "summary", "citation_refs") &&
		hasExactStringSchema(properties["summary"], 1, 4*1024) && hasWorkspaceAnalysisCitationRefsSchema(properties["citation_refs"])
}

func hasWorkspaceAnalysisCitationRefsSchema(raw json.RawMessage) bool {
	var refs struct {
		Type     string          `json:"type"`
		MinItems int             `json:"minItems"`
		MaxItems int             `json:"maxItems"`
		Items    json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &refs) != nil || refs.Type != "array" || refs.MinItems != 1 || refs.MaxItems != 3 {
		return false
	}
	var item struct {
		Type string   `json:"type"`
		Enum []string `json:"enum"`
	}
	return json.Unmarshal(refs.Items, &item) == nil && item.Type == "string" && len(item.Enum) == 3 &&
		item.Enum[0] == "E1" && item.Enum[1] == "E2" && item.Enum[2] == "E3"
}

func hasExactStringSchema(raw json.RawMessage, minLength, maxLength int) bool {
	var property struct {
		Type      string `json:"type"`
		MinLength int    `json:"minLength"`
		MaxLength int    `json:"maxLength"`
	}
	return json.Unmarshal(raw, &property) == nil && property.Type == "string" && property.MinLength == minLength && property.MaxLength == maxLength
}

func hasExactJSONKeys(values map[string]json.RawMessage, keys ...string) bool {
	if len(values) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

func hasExactJSONStringSet(raw json.RawMessage, expected ...string) bool {
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) != len(expected) {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	if len(seen) != len(expected) {
		return false
	}
	for _, value := range expected {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

func jsonRawEquals(raw json.RawMessage, expected any) bool {
	encoded, err := json.Marshal(expected)
	return err == nil && bytes.Equal(bytes.TrimSpace(raw), encoded)
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

func validateStreamRequest(request chatRequest) (bool, error) {
	if request.StreamOptions == nil || !request.StreamOptions.IncludeUsage || len(request.Tools) != 0 ||
		!hasExactToolChoice(request.ToolChoice, "none") {
		return false, fixtureError("answer_stream_contract_unsupported", "unsupported stream request contract")
	}
	if request.ResponseFormat.Type == "" && !request.ResponseFormat.JSONSchema.Strict && request.ResponseFormat.JSONSchema.Name == "" &&
		len(request.ResponseFormat.JSONSchema.Schema) == 0 {
		return false, nil
	}
	if request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema.Name != workspaceAnalysisCandidateResponseSchemaName ||
		!request.ResponseFormat.JSONSchema.Strict || !isWorkspaceAnalysisCandidateProviderSchema(request.ResponseFormat.JSONSchema.Schema) {
		return false, fixtureError("workspace_analysis_candidate_stream_contract_unsupported", "unsupported workspace analysis candidate stream contract")
	}
	return true, nil
}

func serveFinalAnswerStream(ctx context.Context, w http.ResponseWriter, modelVersion string, frameDelay time.Duration) {
	w.Header().Set("Content-Type", "text/event-stream")
	frames := []string{
		`data: {"id":"stream-rag-smoke","object":"chat.completion.chunk","created":1,"model":"` + modelVersion + `","choices":[{"index":0,"delta":{"role":"assistant","content":"Approved recovery requires durable "},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"stream-rag-smoke","object":"chat.completion.chunk","created":1,"model":"` + modelVersion + `","choices":[{"index":0,"delta":{"content":"replay without duplicate provider work."},"finish_reason":"stop"}]}` + "\n\n",
		`data: {"id":"stream-rag-smoke","object":"chat.completion.chunk","created":1,"model":"` + modelVersion + `","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	for _, frame := range frames {
		if frameDelay > 0 {
			timer := time.NewTimer(frameDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		_, _ = io.WriteString(w, frame)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func parseFixtureAnswerStreamFrameDelay(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	milliseconds, err := strconv.Atoi(raw)
	if err != nil || milliseconds < 1 || strconv.Itoa(milliseconds) != raw {
		return 0, errors.New("ZHIXU_RAG_FIXTURE_ANSWER_STREAM_FRAME_DELAY_MS must be a canonical positive integer")
	}
	delay := time.Duration(milliseconds) * time.Millisecond
	if delay > maxFixtureAnswerStreamFrameDelay {
		return 0, errors.New("ZHIXU_RAG_FIXTURE_ANSWER_STREAM_FRAME_DELAY_MS exceeds the bounded maximum")
	}
	return delay, nil
}

func serveWorkspaceAnalysisCandidateStream(w http.ResponseWriter, modelVersion, evidenceRef string) {
	w.Header().Set("Content-Type", "text/event-stream")
	candidate := `{"result_type":"workspace_analysis_candidate","schema_id":"agent.workspace-analysis-candidate","schema_version":"1","payload":{"answer_markdown":"` +
		fixtureWorkspaceAnalysisCandidate + `","citation_refs":["` + evidenceRef + `"],"proposal_suggestion":{"summary":"Review this evidence-backed change in the proposal workspace.","citation_refs":["` + evidenceRef + `"]}}}`
	split := len(candidate) / 2
	frames := []string{
		`data: {"id":"stream-workspace-analysis","object":"chat.completion.chunk","created":1,"model":"` + modelVersion + `","choices":[{"index":0,"delta":{"role":"assistant","content":` + mustJSONQuote(candidate[:split]) + `},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"stream-workspace-analysis","object":"chat.completion.chunk","created":1,"model":"` + modelVersion + `","choices":[{"index":0,"delta":{"content":` + mustJSONQuote(candidate[split:]) + `},"finish_reason":"stop"}]}` + "\n\n",
		`data: {"id":"stream-workspace-analysis","object":"chat.completion.chunk","created":1,"model":"` + modelVersion + `","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	for _, frame := range frames {
		_, _ = io.WriteString(w, frame)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func mustJSONQuote(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("fixture candidate stream cannot encode static content")
	}
	return string(encoded)
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
		if isWorkspaceAnalysisReviewFixtureInput(input) {
			return fixtureWorkspaceAnalysisReviewDocument(envelope, input)
		}
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

func fixtureWorkspaceAnalysisReviewDocument(envelope map[string]any, input map[string]any) (map[string]any, error) {
	modelRunRef, refs, err := workspaceAnalysisReviewFixtureInput(input)
	if err != nil {
		return nil, err
	}
	if envelope["model_run_ref"] != modelRunRef {
		return nil, fixtureError("workspace_analysis_review_subject_drift", "workspace analysis review subject drifted")
	}
	envelope["payload"] = map[string]any{
		"passed": true,
		"items": []any{map[string]any{
			"assertion_id": "@answer/conclusion", "verdict": "SUPPORTED", "citation_ids": refs,
			"reason": "deterministic fixture verified the bounded workspace candidate",
		}},
		"summary": "the supplied workspace candidate is covered",
	}
	return envelope, nil
}

func validateWorkspaceAnalysisPlannerFixtureInput(input map[string]any) (bool, error) {
	_, hasWorkspaceGitStatus := input["git_status"]
	if input["schema_version"] != float64(1) && !hasWorkspaceGitStatus {
		return false, nil
	}
	if !hasExactAnyKeys(input, "schema_version", "untrusted_data", "question", "history", "scope", "answer_depth", "output_format", "git_status") ||
		input["schema_version"] != float64(1) || input["untrusted_data"] != true || !fixtureBoundedString(input["question"]) ||
		workspaceAnalysisInputLeaksIdentity(input) {
		return false, fixtureError("workspace_analysis_plan_input_invalid", "workspace analysis planner input is invalid")
	}
	scope, ok := input["scope"].(map[string]any)
	if !ok || !hasExactAnyKeys(scope, "retrieval_mode", "allow_original_sources", "allow_web") || !fixtureBoundedString(scope["retrieval_mode"]) ||
		scope["allow_web"] != false {
		return false, fixtureError("workspace_analysis_plan_input_invalid", "workspace analysis planner input is invalid")
	}
	if _, ok := scope["allow_original_sources"].(bool); !ok || !fixtureBoundedString(input["answer_depth"]) || !fixtureBoundedString(input["output_format"]) {
		return false, fixtureError("workspace_analysis_plan_input_invalid", "workspace analysis planner input is invalid")
	}
	if _, ok := input["history"].([]any); !ok {
		return false, fixtureError("workspace_analysis_plan_input_invalid", "workspace analysis planner input is invalid")
	}
	if _, ok := input["git_status"].(map[string]any); !ok {
		return false, fixtureError("workspace_analysis_plan_input_invalid", "workspace analysis planner input is invalid")
	}
	return true, nil
}

func workspaceAnalysisPlannerFixtureResponse(input map[string]any) string {
	rewrite := "approved recovery"
	question, _ := input["question"].(string)
	for _, field := range strings.Fields(question) {
		candidate := strings.Trim(field, "\"'()[]{}<>,.;:!?")
		if validFixtureEvidenceToken(candidate) {
			rewrite = candidate
			break
		}
	}
	encoded, err := json.Marshal(struct {
		Intent                string   `json:"i"`
		Rewrites              []string `json:"r"`
		ClarificationReason   string   `json:"d"`
		ClarificationQuestion string   `json:"q"`
		SuggestedScopes       []string `json:"s"`
	}{
		Intent: "answer from approved recovery evidence", Rewrites: []string{rewrite},
		SuggestedScopes: []string{},
	})
	if err != nil {
		panic("workspace analysis planner fixture response cannot be encoded")
	}
	return string(encoded)
}

func validFixtureEvidenceToken(value string) bool {
	const prefix = "durable-rag-"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+12 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func workspaceAnalysisCandidateFixtureInput(messages []message) ([]string, error) {
	input, err := lastTaskInput(messages)
	if err != nil {
		return nil, fixtureError("workspace_analysis_candidate_input_missing", "workspace analysis candidate input is missing")
	}
	if !hasExactAnyKeys(input, "schema_version", "untrusted_data", "question", "history", "answer_depth", "output_format", "git_status", "search", "evidence") ||
		input["schema_version"] != float64(1) || input["untrusted_data"] != true || !fixtureBoundedString(input["question"]) ||
		workspaceAnalysisInputLeaksIdentity(input) {
		return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis candidate input is invalid")
	}
	if _, ok := input["history"].([]any); !ok {
		return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis candidate input is invalid")
	}
	if _, ok := input["git_status"].(map[string]any); !ok {
		return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis candidate input is invalid")
	}
	search, ok := input["search"].(map[string]any)
	if !ok || !hasExactAnyKeys(search, "effective_mode", "hit_count", "degradation_codes") || !fixtureBoundedString(search["effective_mode"]) {
		return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis candidate input is invalid")
	}
	if _, ok := search["hit_count"].(float64); !ok {
		return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis candidate input is invalid")
	}
	if _, ok := search["degradation_codes"].([]any); !ok || !fixtureBoundedString(input["answer_depth"]) || !fixtureBoundedString(input["output_format"]) {
		return nil, fixtureError("workspace_analysis_candidate_input_invalid", "workspace analysis candidate input is invalid")
	}
	evidence, ok := input["evidence"].([]any)
	if !ok || len(evidence) < 1 || len(evidence) > 3 {
		return nil, fixtureError("workspace_analysis_candidate_evidence_invalid", "workspace analysis candidate evidence is invalid")
	}
	refs := make([]string, 0, len(evidence))
	for index, raw := range evidence {
		item, itemOK := raw.(map[string]any)
		if !itemOK || !hasExactAnyKeys(item, "evidence_ref", "excerpt", "truncated") || !fixtureBoundedExcerpt(item["excerpt"]) {
			return nil, fixtureError("workspace_analysis_candidate_evidence_invalid", "workspace analysis candidate evidence is invalid")
		}
		ref, refOK := item["evidence_ref"].(string)
		if !refOK || ref != "E"+strconv.Itoa(index+1) {
			return nil, fixtureError("workspace_analysis_candidate_evidence_invalid", "workspace analysis candidate evidence is invalid")
		}
		if _, truncated := item["truncated"].(bool); !truncated {
			return nil, fixtureError("workspace_analysis_candidate_evidence_invalid", "workspace analysis candidate evidence is invalid")
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func isWorkspaceAnalysisReviewFixtureInput(input map[string]any) bool {
	return input["schema_version"] == "agent-workspace-analysis-review-input/v1"
}

func workspaceAnalysisReviewFixtureInput(input map[string]any) (string, []string, error) {
	if !hasExactAnyKeys(input, "schema_version", "model_run_ref", "candidate", "review_targets", "evidence") ||
		input["schema_version"] != "agent-workspace-analysis-review-input/v1" {
		return "", nil, fixtureError("workspace_analysis_review_input_invalid", "workspace analysis review input is invalid")
	}
	modelRunRef, ok := input["model_run_ref"].(string)
	if !ok || !fixtureCanonicalID(modelRunRef) {
		return "", nil, fixtureError("workspace_analysis_review_subject_invalid", "workspace analysis review subject is invalid")
	}
	candidate, ok := input["candidate"].(map[string]any)
	if !ok || !hasExactAnyKeys(candidate, "answer_markdown", "citation_refs") || !fixtureBoundedString(candidate["answer_markdown"]) {
		return "", nil, fixtureError("workspace_analysis_review_candidate_invalid", "workspace analysis review candidate is invalid")
	}
	refs, err := workspaceAnalysisFixtureEvidenceRefs(candidate["citation_refs"])
	if err != nil {
		return "", nil, err
	}
	targets, ok := input["review_targets"].([]any)
	if !ok || len(targets) != 1 {
		return "", nil, fixtureError("workspace_analysis_review_targets_invalid", "workspace analysis review targets are invalid")
	}
	target, ok := targets[0].(map[string]any)
	if !ok || !hasExactAnyKeys(target, "id", "text", "kind", "citation_ids") || target["id"] != "@answer/conclusion" ||
		target["kind"] != "FACTUAL" || target["text"] != candidate["answer_markdown"] {
		return "", nil, fixtureError("workspace_analysis_review_targets_invalid", "workspace analysis review targets are invalid")
	}
	targetRefs, targetErr := workspaceAnalysisFixtureEvidenceRefs(target["citation_ids"])
	if targetErr != nil || !sameFixtureStrings(targetRefs, refs) {
		return "", nil, fixtureError("workspace_analysis_review_targets_invalid", "workspace analysis review targets are invalid")
	}
	evidence, ok := input["evidence"].([]any)
	if !ok || len(evidence) != len(refs) {
		return "", nil, fixtureError("workspace_analysis_review_evidence_invalid", "workspace analysis review evidence is invalid")
	}
	for index, raw := range evidence {
		item, itemOK := raw.(map[string]any)
		if !itemOK || !hasExactAnyKeys(item, "evidence_ref", "excerpt") || item["evidence_ref"] != refs[index] || !fixtureBoundedExcerpt(item["excerpt"]) {
			return "", nil, fixtureError("workspace_analysis_review_evidence_invalid", "workspace analysis review evidence is invalid")
		}
	}
	return modelRunRef, refs, nil
}

func workspaceAnalysisFixtureEvidenceRefs(raw any) ([]string, error) {
	values, ok := raw.([]any)
	if !ok || len(values) < 1 || len(values) > 3 {
		return nil, fixtureError("workspace_analysis_evidence_refs_invalid", "workspace analysis evidence refs are invalid")
	}
	refs := make([]string, len(values))
	for index, rawRef := range values {
		ref, ok := rawRef.(string)
		if !ok || ref != "E"+strconv.Itoa(index+1) {
			return nil, fixtureError("workspace_analysis_evidence_refs_invalid", "workspace analysis evidence refs are invalid")
		}
		refs[index] = ref
	}
	return refs, nil
}

func hasExactAnyKeys(values map[string]any, keys ...string) bool {
	if len(values) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

func workspaceAnalysisInputLeaksIdentity(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			switch strings.ToLower(key) {
			case "workspace_id", "definition_id", "workflow_run_id", "node_run_id", "node_attempt_id", "analysis_run_id",
				"operation_id", "reservation_id", "model_run_id", "model_run_ref", "model_call_id", "index_version_id",
				"embedding_version_id", "definition_hash", "definition_version", "model_settings_revision", "lease_owner", "lease_fence":
				return true
			}
			if workspaceAnalysisInputLeaksIdentity(child) {
				return true
			}
		}
	case []any:
		for _, child := range current {
			if workspaceAnalysisInputLeaksIdentity(child) {
				return true
			}
		}
	}
	return false
}

func fixtureBoundedString(value any) bool {
	text, ok := value.(string)
	return ok && text != "" && strings.TrimSpace(text) == text && len(text) <= 64*1024
}

func fixtureBoundedExcerpt(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != "" && len(text) <= maxWorkspaceAnalysisFixtureExcerptBytes && !strings.ContainsRune(text, 0)
}

func fixtureCanonicalID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func sameFixtureStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
