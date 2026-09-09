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
	"reflect"
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

func TestParseFixtureAnswerStreamFrameDelay(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
		ok   bool
	}{
		{name: "disabled", raw: "", ok: true},
		{name: "bounded", raw: "750", want: 750 * time.Millisecond, ok: true},
		{name: "maximum", raw: "5000", want: 5 * time.Second, ok: true},
		{name: "zero", raw: "0"},
		{name: "negative", raw: "-1"},
		{name: "leading zero", raw: "0750"},
		{name: "whitespace", raw: " 750"},
		{name: "over maximum", raw: "5001"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseFixtureAnswerStreamFrameDelay(test.raw)
			if (err == nil) != test.ok || got != test.want {
				t.Fatalf("parseFixtureAnswerStreamFrameDelay(%q)=(%s,%v), want (%s, ok=%t)", test.raw, got, err, test.want, test.ok)
			}
		})
	}
}

func TestFixtureBarrierRequiresExplicitRelease(t *testing.T) {
	barrierDir := t.TempDir()
	releasePath := barrierDir + "/release"
	enteredPath := barrierDir + "/entered"
	settledPath := barrierDir + "/settled"
	armedPath := barrierDir + "/armed"
	token := "0123456789abcdef0123456789abcdef"
	writeFixtureBarrierToken(t, armedPath, token)
	handler := newHandlerWithBarrier("fixture-model-v2", fixtureAPIKey, fixtureBarrier{
		stage: "structured_plan", releasePath: releasePath, enteredPath: enteredPath, settledPath: settledPath, armedPath: armedPath, lockPath: barrierDir + "/claim",
		maxWait: 2 * time.Second, poll: 5 * time.Millisecond,
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
	waitForFixtureBarrierFile(t, enteredPath)
	select {
	case response := <-result:
		t.Fatalf("barrier returned before release: %#v", response)
	default:
	}
	writeFixtureBarrierToken(t, releasePath, token)
	select {
	case response := <-result:
		if response.status != http.StatusOK || !strings.Contains(response.body, "rag_query_plan") {
			t.Fatalf("released barrier response=%#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("barrier did not release")
	}
	waitForFixtureBarrierFile(t, settledPath)
	if settled := readFixtureBarrierTokenForTest(t, settledPath); settled != token {
		t.Fatalf("settled token=%q, want current generation", settled)
	}
}

func TestFixtureBarrierAcknowledgesExactWorkspaceAnalysisCandidateStream(t *testing.T) {
	for _, version := range []string{"1", "2"} {
		t.Run(version, func(t *testing.T) { assertFixtureWorkspaceAnalysisCandidateStreamBarrier(t, version) })
	}
}

func assertFixtureWorkspaceAnalysisCandidateStreamBarrier(t *testing.T, version string) {
	t.Helper()
	barrierDir := t.TempDir()
	releasePath := barrierDir + "/release"
	enteredPath := barrierDir + "/entered"
	settledPath := barrierDir + "/settled"
	armedPath := barrierDir + "/armed"
	token := "fedcba9876543210fedcba9876543210"
	writeFixtureBarrierToken(t, armedPath, token)
	fixtureServer, _ := newProductionFixtureServer(t)
	_, catalog := newProductionStructuredRuntime(t, fixtureServer)
	prompt := agentworkflow.WorkspaceAnalysisSynthesisPromptRef()
	input := fixtureWorkspaceAnalysisSynthesisInput()
	if version == "2" {
		prompt = agentworkflow.WorkspaceAnalysisSynthesisPromptRefV2()
		input = fixtureWorkspaceAnalysisV2CandidateInput()
	}
	snapshot, err := catalog.Snapshot(
		prompt,
		agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: version},
		agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: version},
		agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandlerWithBarrier("fixture-model-v2", fixtureAPIKey, fixtureBarrier{
		stage: "workspace_analysis_candidate_stream", releasePath: releasePath, enteredPath: enteredPath, settledPath: settledPath, armedPath: armedPath, lockPath: barrierDir + "/claim",
		maxWait: 2 * time.Second, poll: 5 * time.Millisecond,
	})
	body := fixtureWorkspaceAnalysisCandidateStreamRequest(t, snapshot.Schema.JSONSchema, input)
	if version == "2" {
		body = bytes.Replace(body, []byte(workspaceAnalysisCandidateResponseSchemaName), []byte(workspaceAnalysisCandidateResponseSchemaNameV2), 1)
	}
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
	waitForFixtureBarrierFile(t, enteredPath)
	select {
	case response := <-result:
		t.Fatalf("candidate barrier returned before release: %#v", response)
	default:
	}
	writeFixtureBarrierToken(t, releasePath, token)
	select {
	case response := <-result:
		if response.status != http.StatusOK || !strings.Contains(response.body, "stream-workspace-analysis") {
			t.Fatalf("released candidate barrier response=%#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate barrier did not release")
	}
	waitForFixtureBarrierFile(t, settledPath)
	if settled := readFixtureBarrierTokenForTest(t, settledPath); settled != token {
		t.Fatalf("candidate settled token=%q, want current generation", settled)
	}
}

func TestFixtureBarrierRejectsStaleReleaseToken(t *testing.T) {
	barrierDir := t.TempDir()
	releasePath := barrierDir + "/release"
	enteredPath := barrierDir + "/entered"
	settledPath := barrierDir + "/settled"
	armedPath := barrierDir + "/armed"
	currentToken := "00112233445566778899aabbccddeeff"
	writeFixtureBarrierToken(t, armedPath, currentToken)
	writeFixtureBarrierToken(t, releasePath, "ffeeddccbbaa99887766554433221100")
	handler := newHandlerWithBarrier("fixture-model-v2", fixtureAPIKey, fixtureBarrier{
		stage: "structured_plan", releasePath: releasePath, enteredPath: enteredPath, settledPath: settledPath, armedPath: armedPath, lockPath: barrierDir + "/claim",
		maxWait: 2 * time.Second, poll: 5 * time.Millisecond,
	})
	result := make(chan int, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(fixtureRequest(t, "rag_query_plan", "agent.rag-query-plan", map[string]any{"model_run_ref": "10000000-0000-4000-8000-000000000124"})))
		request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		result <- response.Code
	}()
	waitForFixtureBarrierFile(t, enteredPath)
	if entered := readFixtureBarrierTokenForTest(t, enteredPath); entered != currentToken {
		t.Fatalf("entered token=%q, want current generation", entered)
	}
	select {
	case status := <-result:
		t.Fatalf("stale release token unexpectedly passed status=%d", status)
	default:
	}
	writeFixtureBarrierToken(t, releasePath, currentToken)
	select {
	case status := <-result:
		if status != http.StatusOK {
			t.Fatalf("current release token status=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("current release token did not unblock fixture")
	}
	waitForFixtureBarrierFile(t, settledPath)
	if settled := readFixtureBarrierTokenForTest(t, settledPath); settled != currentToken {
		t.Fatalf("settled token=%q, want current generation", settled)
	}
}

func TestFixtureBarrierAcknowledgesSettlementAfterRequestCancellation(t *testing.T) {
	barrierDir := t.TempDir()
	releasePath := barrierDir + "/release"
	enteredPath := barrierDir + "/entered"
	settledPath := barrierDir + "/settled"
	armedPath := barrierDir + "/armed"
	token := "89abcdef0123456789abcdef01234567"
	writeFixtureBarrierToken(t, armedPath, token)
	handler := newHandlerWithBarrier("fixture-model-v2", fixtureAPIKey, fixtureBarrier{
		stage: "structured_plan", releasePath: releasePath, enteredPath: enteredPath, settledPath: settledPath, armedPath: armedPath, lockPath: barrierDir + "/claim",
		maxWait: 2 * time.Second, poll: 5 * time.Millisecond,
	})
	body := fixtureRequest(t, "rag_query_plan", "agent.rag-query-plan", map[string]any{"model_run_ref": "10000000-0000-4000-8000-000000000125"})
	result := make(chan int, 1)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(requestContext)
		request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		result <- response.Code
	}()
	waitForFixtureBarrierFile(t, enteredPath)
	cancelRequest()
	waitForFixtureBarrierFile(t, settledPath)
	if settled := readFixtureBarrierTokenForTest(t, settledPath); settled != token {
		t.Fatalf("cancelled settled token=%q, want current generation", settled)
	}
	select {
	case status := <-result:
		if status != http.StatusGatewayTimeout {
			t.Fatalf("cancelled barrier status=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled barrier handler did not settle")
	}
}

func TestFixtureBarrierClaimPreventsRearmTOCTOU(t *testing.T) {
	barrierDir := t.TempDir()
	releasePath := barrierDir + "/release"
	enteredPath := barrierDir + "/entered"
	settledPath := barrierDir + "/settled"
	armedPath := barrierDir + "/armed"
	lockPath := barrierDir + "/claim"
	oldToken := "89abcdef0123456789abcdef01234567"
	newToken := "76543210fedcba9876543210fedcba98"
	writeFixtureBarrierToken(t, armedPath, oldToken)

	claimObserved := make(chan struct{})
	allowClaim := make(chan struct{})
	var allowClaimOnce sync.Once
	releaseClaim := func() { allowClaimOnce.Do(func() { close(allowClaim) }) }
	defer releaseClaim()
	barrier := fixtureBarrier{
		stage: "structured_plan", releasePath: releasePath, enteredPath: enteredPath, settledPath: settledPath,
		armedPath: armedPath, lockPath: lockPath, maxWait: 2 * time.Second, poll: 5 * time.Millisecond,
		afterClaimForTest: func() {
			close(claimObserved)
			<-allowClaim
		},
	}
	handler := newHandlerWithBarrier("fixture-model-v2", fixtureAPIKey, barrier)
	body := fixtureRequest(t, "rag_query_plan", "agent.rag-query-plan", map[string]any{"model_run_ref": "10000000-0000-4000-8000-000000000126"})
	requestContext, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	result := make(chan int, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(requestContext)
		request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		result <- response.Code
	}()
	select {
	case <-claimObserved:
	case <-time.After(time.Second):
		t.Fatal("fixture did not claim the barrier generation")
	}

	rearmAttempted := make(chan struct{})
	rearmResult := make(chan error, 1)
	go func() {
		close(rearmAttempted)
		rearmResult <- tryRearmFixtureBarrierGeneration(lockPath, armedPath, enteredPath, settledPath, newToken)
	}()
	<-rearmAttempted
	select {
	case err := <-rearmResult:
		t.Fatalf("re-arm crossed the claimed generation before settlement: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	releaseClaim()
	waitForFixtureBarrierFile(t, enteredPath)
	if entered := readFixtureBarrierTokenForTest(t, enteredPath); entered != oldToken {
		t.Fatalf("entered token=%q, want old generation", entered)
	}
	cancelRequest()
	select {
	case status := <-result:
		if status != http.StatusGatewayTimeout {
			t.Fatalf("cancelled barrier status=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("claimed barrier request did not settle")
	}
	select {
	case err := <-rearmResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("re-arm did not acquire the claim after settlement")
	}
	if armed := readFixtureBarrierTokenForTest(t, armedPath); armed != newToken {
		t.Fatalf("armed token=%q, want new generation", armed)
	}
	if _, err := os.Stat(enteredPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old entered acknowledgement remains after re-arm: %v", err)
	}
}

func waitForFixtureBarrierFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("fixture barrier did not acknowledge %q", path)
}

func writeFixtureBarrierToken(t *testing.T, path, token string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFixtureBarrierTokenForTest(t *testing.T, path string) string {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(encoded), "\n")
}

func tryRearmFixtureBarrierGeneration(lockPath, armedPath, enteredPath, settledPath, token string) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("claim lock acquisition timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer os.Remove(lockPath)
	oldToken, err := os.ReadFile(armedPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(enteredPath); err == nil {
		settled, readErr := os.ReadFile(settledPath)
		if readErr != nil {
			return readErr
		}
		if string(settled) != string(oldToken) {
			return errors.New("old barrier generation did not settle before re-arm")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(enteredPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(settledPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := armedPath + ".tmp"
	if err := os.WriteFile(temporary, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, armedPath)
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

func TestFixtureServesWorkspaceAnalysisPlannerWithoutServerIdentity(t *testing.T) {
	server, _ := newProductionFixtureServer(t)
	_, catalog := newProductionStructuredRuntime(t, server)
	// The catalog owns the frozen provider schema; this request must use it unchanged.
	snapshot, err := catalog.Snapshot(
		agentworkflow.WorkspaceAnalysisPlanPromptRef(),
		agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		t.Fatal(err)
	}
	input := fixtureWorkspaceAnalysisPlannerInput()
	response := callFixture(t, newHandler("fixture-model-v1", fixtureAPIKey), fixtureStructuredRequest(t, snapshot.Schema.JSONSchema, input))
	if response != `{"i":"answer from approved recovery evidence","r":["approved recovery"],"d":"","q":"","s":[]}` ||
		strings.Contains(response, "model_run_ref") || strings.Contains(response, "workspace_id") {
		t.Fatalf("workspace planner provider response=%s", response)
	}

	const evidenceToken = "durable-rag-0123abcdef89"
	input["question"] = "Analyze the approved evidence token " + evidenceToken + " with citations."
	response = callFixture(t, newHandler("fixture-model-v1", fixtureAPIKey), fixtureStructuredRequest(t, snapshot.Schema.JSONSchema, input))
	if response != `{"i":"answer from approved recovery evidence","r":["`+evidenceToken+`"],"d":"","q":"","s":[]}` {
		t.Fatalf("workspace planner did not bind the smoke evidence token: %s", response)
	}

	input["workflow_run_id"] = "10000000-0000-4000-8000-000000000001"
	status := fixtureStatus(t, newHandler("fixture-model-v1", fixtureAPIKey), fixtureStructuredRequest(t, snapshot.Schema.JSONSchema, input))
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("planner identity leak status=%d", status)
	}
}

func TestFixtureKeepsHistoricalRAGV2PlannerSeparateFromWorkspaceAnalysis(t *testing.T) {
	server, _ := newProductionFixtureServer(t)
	_, catalog := newProductionStructuredRuntime(t, server)
	snapshot, err := catalog.Snapshot(
		agentworkflow.QueryPlanProviderPromptRef(),
		agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"schema_version": 2, "untrusted_data": true, "question": "Summarize approved recovery evidence.", "history": []any{},
		"scope": map[string]any{
			"retrieval_mode": "keyword", "source_ids": []any{}, "source_version_ids": []any{}, "path_prefixes": []any{},
			"captured_at_from": nil, "captured_at_before": nil, "allow_original_sources": false, "allow_web": false,
		},
		"non_evidence_context": map[string]any{"untrusted_data": true, "user_preferences": []any{}, "task_context": []any{}},
		"answer_depth":         "standard",
		"output_format":        "markdown",
	}
	response := callFixture(t, newHandler("fixture-model-v1", fixtureAPIKey), fixtureStructuredRequest(t, snapshot.Schema.JSONSchema, input))
	if response != `{"i":"answer from approved recovery evidence","r":["approved recovery"],"d":"","q":"","s":[]}` {
		t.Fatalf("historical RAG v2 planner response drifted: %s", response)
	}
}

func TestFixtureStreamsWorkspaceAnalysisCandidateWithStrictWireContract(t *testing.T) {
	server, bodies := newRecordingFixtureServer(t)
	runtimeModel, err := platformmodels.NewEinoRuntimeChatModel(platformmodels.OpenAIChatOptions{
		Client: server.Client(), BaseURL: server.URL, APIKey: fixtureAPIKey,
		Model: "rag-smoke", ModelVersion: "rag-smoke-v1", AdapterVersion: "fixture-v1",
		Timeout: 5 * time.Second, MaxRequestBytes: maxRequestBytes, MaxResponseBytes: maxRequestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{
		Model:   agentdomain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "fixture-v1", ModelID: "rag-smoke", ModelVersion: "rag-smoke-v1"},
		Timeout: 5 * time.Second, MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot(
		agentworkflow.WorkspaceAnalysisSynthesisPromptRef(),
		agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "1"},
		agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "1"},
		agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agenteino.NewWorkspaceAnalysisCandidateStreamRuntime(runtimeModel)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(fixtureWorkspaceAnalysisSynthesisInput())
	if err != nil {
		t.Fatal(err)
	}
	sink := &fixtureCandidateSink{}
	response, err := runtime.Stream(context.Background(), agentapplication.ChatRequest{
		Phase: agentdomain.ModelCallAnswer, ProfileRef: snapshot.Profile.Ref, PromptRef: snapshot.Prompt.Ref,
		SchemaRef: snapshot.Schema.Ref, Model: snapshot.Profile.Model,
		Messages:     []agentapplication.ChatMessage{{Role: agentapplication.MessageRoleSystem, Content: snapshot.Prompt.InitialInstruction}, {Role: agentapplication.MessageRoleUser, Content: string(input)}},
		OutputSchema: snapshot.Schema.JSONSchema, MaxOutputTokens: 1024,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := agentdomain.DecodeWorkspaceAnalysisCandidateProvider(response.Content, agentdomain.DefaultDecodeLimits())
	if err != nil || candidate.Payload.AnswerMarkdown != fixtureWorkspaceAnalysisCandidate ||
		!reflect.DeepEqual(candidate.Payload.CitationRefs, []string{"E1"}) || candidate.Payload.ProposalSuggestion == nil ||
		candidate.Payload.ProposalSuggestion.Summary != "Review this evidence-backed change in the proposal workspace." ||
		!reflect.DeepEqual(candidate.Payload.ProposalSuggestion.CitationRefs, []string{"E1"}) ||
		response.Usage.TotalTokens != 6 || sink.String() != string(response.Content) {
		t.Fatalf("candidate=%#v usage=%#v sink=%q err=%v", candidate, response.Usage, sink.String(), err)
	}
	requests := bodies()
	if len(requests) != 1 {
		t.Fatalf("candidate provider calls=%d", len(requests))
	}
	var wire struct {
		Stream        bool            `json:"stream"`
		ToolChoice    string          `json:"tool_choice"`
		Tools         json.RawMessage `json:"tools"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string          `json:"name"`
				Strict bool            `json:"strict"`
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal(requests[0], &wire); err != nil || !wire.Stream || wire.ToolChoice != "none" || len(wire.Tools) != 0 ||
		!wire.StreamOptions.IncludeUsage || wire.ResponseFormat.Type != "json_schema" ||
		wire.ResponseFormat.JSONSchema.Name != workspaceAnalysisCandidateResponseSchemaName || !wire.ResponseFormat.JSONSchema.Strict ||
		!bytes.Equal(wire.ResponseFormat.JSONSchema.Schema, snapshot.Schema.JSONSchema) {
		t.Fatalf("candidate provider wire=%s err=%v", requests[0], err)
	}
}

func TestFixtureServesWorkspaceAnalysisFaithfulnessReviewSubject(t *testing.T) {
	server, _ := newProductionFixtureServer(t)
	_, catalog := newProductionStructuredRuntime(t, server)
	snapshot, err := catalog.Snapshot(
		agentworkflow.FaithfulnessReviewPromptRef(),
		agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		t.Fatal(err)
	}
	response := callFixture(t, newHandler("fixture-model-v1", fixtureAPIKey), fixtureStructuredRequest(t, snapshot.Schema.JSONSchema, fixtureWorkspaceAnalysisReviewInput()))
	review, err := agentdomain.DecodeFaithfulnessReview([]byte(response), agentdomain.DefaultDecodeLimits())
	if err != nil || review.ModelRunRef != "40000000-0000-4000-8000-000000000001" || !review.Payload.Passed ||
		len(review.Payload.Items) != 1 || review.Payload.Items[0].AssertionID != "@answer/conclusion" ||
		!reflect.DeepEqual(review.Payload.Items[0].CitationIDs, []string{"E1"}) {
		t.Fatalf("workspace analysis review=%#v err=%v", review, err)
	}
}

func TestFixtureRejectsWorkspaceAnalysisCandidateAndReviewContractDrift(t *testing.T) {
	server, _ := newProductionFixtureServer(t)
	_, catalog := newProductionStructuredRuntime(t, server)
	candidateSnapshot, err := catalog.Snapshot(
		agentworkflow.WorkspaceAnalysisSynthesisPromptRef(),
		agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "1"},
		agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "1"},
		agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		t.Fatal(err)
	}
	invalidCandidate := fixtureWorkspaceAnalysisCandidateStreamRequest(t, candidateSnapshot.Schema.JSONSchema, fixtureWorkspaceAnalysisSynthesisInput())
	var candidateRequest map[string]any
	if err := json.Unmarshal(invalidCandidate, &candidateRequest); err != nil {
		t.Fatal(err)
	}
	candidateRequest["response_format"].(map[string]any)["json_schema"].(map[string]any)["name"] = "wrong_name"
	invalidCandidate, err = json.Marshal(candidateRequest)
	if err != nil {
		t.Fatal(err)
	}
	if status := fixtureStatus(t, newHandler("fixture-model-v1", fixtureAPIKey), invalidCandidate); status != http.StatusUnprocessableEntity {
		t.Fatalf("candidate response format drift status=%d", status)
	}
	identityInput := fixtureWorkspaceAnalysisSynthesisInput()
	identityInput["workspace_id"] = "10000000-0000-4000-8000-000000000001"
	if status := fixtureStatus(t, newHandler("fixture-model-v1", fixtureAPIKey), fixtureWorkspaceAnalysisCandidateStreamRequest(t, candidateSnapshot.Schema.JSONSchema, identityInput)); status != http.StatusUnprocessableEntity {
		t.Fatalf("candidate identity leak status=%d", status)
	}

	reviewInput := fixtureWorkspaceAnalysisReviewInput()
	reviewInput["review_targets"].([]any)[0].(map[string]any)["citation_ids"] = []string{"E2"}
	if status := fixtureStatus(t, newHandler("fixture-model-v1", fixtureAPIKey), fixtureRequest(t, "faithfulness_review", "agent.faithfulness-review", reviewInput)); status != http.StatusUnprocessableEntity {
		t.Fatalf("review binding drift status=%d", status)
	}
}

func TestFixtureBuildsFaithfulnessItemsFromReviewTargets(t *testing.T) {
	input := map[string]any{"model_run_ref": "20000000-0000-4000-8000-000000000001", "review_targets": []any{
		map[string]any{"id": "fact", "kind": "FACTUAL", "citation_ids": []string{"cite-1"}},
		map[string]any{"id": "inference", "kind": "MODEL_INFERENCE", "citation_ids": []string{}},
	}}
	response := callFixture(t, newHandler("fixture-model-v1", fixtureAPIKey), fixtureStructuredRequest(t, fixtureFaithfulnessReviewSchema(t), input))
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

type fixtureCandidateSink struct{ strings.Builder }

func (sink *fixtureCandidateSink) Append(_ context.Context, chunk agentapplication.WorkspaceAnalysisCandidateStreamChunk) error {
	_, err := sink.WriteString(chunk.Content)
	return err
}

func fixtureWorkspaceAnalysisPlannerInput() map[string]any {
	return map[string]any{
		"schema_version": 1, "untrusted_data": true, "question": "Summarize the approved workspace evidence.", "history": []any{},
		"scope":        map[string]any{"retrieval_mode": "workspace", "allow_original_sources": false, "allow_web": false},
		"answer_depth": "STANDARD", "output_format": "MARKDOWN", "git_status": map[string]any{"is_clean": true},
	}
}

func fixtureWorkspaceAnalysisSynthesisInput() map[string]any {
	return map[string]any{
		"schema_version": 1, "untrusted_data": true, "question": "Summarize the approved workspace evidence.", "history": []any{},
		"answer_depth": "STANDARD", "output_format": "MARKDOWN", "git_status": map[string]any{"is_clean": true},
		"search":   map[string]any{"effective_mode": "KEYWORD", "hit_count": 1, "degradation_codes": []any{}},
		"evidence": []any{map[string]any{"evidence_ref": "E1", "excerpt": "Approved workspace evidence.\n", "truncated": false}},
	}
}

func fixtureWorkspaceAnalysisReviewInput() map[string]any {
	const modelRunRef = "40000000-0000-4000-8000-000000000001"
	const answer = "The supplied workspace evidence supports this bounded fixture result [E1]."
	return map[string]any{
		"schema_version": "agent-workspace-analysis-review-input/v1", "model_run_ref": modelRunRef,
		"candidate":      map[string]any{"answer_markdown": answer, "citation_refs": []string{"E1"}},
		"review_targets": []any{map[string]any{"id": "@answer/conclusion", "text": answer, "kind": "FACTUAL", "citation_ids": []string{"E1"}}},
		"evidence":       []any{map[string]any{"evidence_ref": "E1", "excerpt": "Approved workspace evidence.\n"}},
	}
}

func fixtureFaithfulnessReviewSchema(t *testing.T) []byte {
	t.Helper()
	server, _ := newProductionFixtureServer(t)
	_, catalog := newProductionStructuredRuntime(t, server)
	snapshot, err := catalog.Snapshot(
		agentworkflow.FaithfulnessReviewPromptRef(),
		agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		agentworkflow.DefaultProfileRef(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), snapshot.Schema.JSONSchema...)
}

func fixtureStructuredRequest(t *testing.T, responseSchema []byte, input map[string]any) []byte {
	t.Helper()
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"model": "fixture-model", "max_tokens": 1024,
		"messages":        []any{map[string]any{"role": "system", "content": "policy"}, map[string]any{"role": "user", "content": "UNTRUSTED TASK INPUT\n" + string(inputJSON)}},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "fixture", "strict": true, "schema": json.RawMessage(responseSchema)}},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func fixtureWorkspaceAnalysisCandidateStreamRequest(t *testing.T, responseSchema []byte, input map[string]any) []byte {
	t.Helper()
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"model": "fixture-model", "max_tokens": 1024, "stream": true, "tool_choice": "none",
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []any{map[string]any{"role": "system", "content": "policy"}, map[string]any{"role": "user", "content": string(inputJSON)}},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name": workspaceAnalysisCandidateResponseSchemaName, "strict": true, "schema": json.RawMessage(responseSchema),
		}},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func fixtureStatus(t *testing.T, handler http.Handler, body []byte) int {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixtureAPIKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response.Code
}
