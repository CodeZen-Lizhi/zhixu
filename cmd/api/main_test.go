package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workflow"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewWorkflowServiceRequiresDatabase(t *testing.T) {
	if service, err := newWorkflowService(nil); err == nil || service != nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
}

func TestNewAPIServerBoundsRequestReads(t *testing.T) {
	server := newAPIServer("127.0.0.1:0", http.NotFoundHandler())
	if server.ReadTimeout != apiReadTimeout || server.ReadTimeout <= 0 {
		t.Fatalf("read timeout=%s", server.ReadTimeout)
	}
	if server.ReadHeaderTimeout != apiReadHeaderTimeout || server.IdleTimeout != apiIdleTimeout {
		t.Fatalf("server timeouts header=%s idle=%s", server.ReadHeaderTimeout, server.IdleTimeout)
	}
}

func TestInitializeAPITelemetryHonorsConfiguredMode(t *testing.T) {
	available := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(available.Close)
	unavailable := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "collector unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(unavailable.Close)
	tests := []struct {
		name          string
		mode          config.TelemetryMode
		endpoint      string
		wantErr       error
		wantDegraded  bool
		wantExporting bool
	}{
		{name: "disabled", mode: config.TelemetryModeDisabled},
		{name: "optional exporter unavailable", mode: config.TelemetryModeOptional, endpoint: unavailable.URL, wantDegraded: true},
		{name: "required exporter unavailable", mode: config.TelemetryModeRequired, endpoint: unavailable.URL, wantErr: observability.ErrTelemetryExporterRequired},
		{name: "optional exporter available", mode: config.TelemetryModeOptional, endpoint: available.URL, wantExporting: true},
		{name: "required exporter available", mode: config.TelemetryModeRequired, endpoint: available.URL, wantExporting: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.TelemetryMode = test.mode
			cfg.TelemetryEndpoint = test.endpoint
			telemetry, err := initializeAPITelemetry(context.Background(), cfg)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("initializeAPITelemetry error=%v, want %v", err, test.wantErr)
			}
			if err != nil {
				return
			}
			defer func() {
				if shutdownErr := telemetry.Shutdown(context.Background()); shutdownErr != nil {
					t.Fatalf("telemetry shutdown: %v", shutdownErr)
				}
			}()
			if telemetry.Tracer() == nil || telemetry.MetricsHandler() == nil || telemetry.Status().Degraded != test.wantDegraded || telemetry.Status().Exporting != test.wantExporting {
				t.Fatalf("telemetry=%#v tracer=%#v", telemetry.Status(), telemetry.Tracer())
			}
		})
	}
}

func TestAPICompositionDoesNotCallConfiguredModelFactories(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	factoryCalls := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && (selector.Sel.Name == "NewConfiguredChatModel" || selector.Sel.Name == "NewConfiguredEmbedder") {
			factoryCalls++
		}
		return true
	})
	if factoryCalls != 0 {
		t.Fatalf("API composition directly called configured model factories %d times", factoryCalls)
	}
}

func TestImpactAuditActorDistinguishesSessionAndAPIToken(t *testing.T) {
	scopes := []capability.Capability{capability.ReadLocal}
	for _, test := range []struct {
		name      string
		principal authdomain.Principal
		found     bool
		wantType  auditdomain.ActorType
		wantRef   string
	}{
		{name: "anonymous", wantType: auditdomain.ActorAnonymous},
		{name: "session", principal: authdomain.Principal{Kind: authdomain.PrincipalSession, ID: foundation.ID("91000000-0000-4000-8000-000000000001"), Scopes: scopes}, found: true, wantType: auditdomain.ActorUser, wantRef: "91000000-0000-4000-8000-000000000001"},
		{name: "api token", principal: authdomain.Principal{Kind: authdomain.PrincipalAPIToken, ID: foundation.ID("91000000-0000-4000-8000-000000000002"), Scopes: scopes}, found: true, wantType: auditdomain.ActorAPIToken, wantRef: "91000000-0000-4000-8000-000000000002"},
		{name: "invalid principal", principal: authdomain.Principal{Kind: authdomain.PrincipalAPIToken, ID: "not-a-uuid", Scopes: scopes}, found: true, wantType: auditdomain.ActorAnonymous},
	} {
		t.Run(test.name, func(t *testing.T) {
			actorType, actorRef := impactAuditActorForPrincipal(test.principal, test.found)
			if actorType != test.wantType || actorRef != test.wantRef {
				t.Fatalf("actor=(%q,%q), want=(%q,%q)", actorType, actorRef, test.wantType, test.wantRef)
			}
		})
	}
}

func TestAPIHasAllToolContractsWithoutExecutors(t *testing.T) {
	registry, err := newToolContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := toolcatalog.Contracts()
	if err != nil || len(contracts) != 11 {
		t.Fatalf("contracts=%d err=%v", len(contracts), err)
	}
	for _, expected := range contracts {
		resolved, err := registry.ResolveContract(expected.Definition.Ref)
		if err != nil || resolved.Definition.DefinitionHash != expected.Definition.DefinitionHash {
			t.Fatalf("resolve %s: resolved=%+v err=%v", expected.Definition.Ref.Name, resolved.Definition.Ref, err)
		}
		if _, err := registry.ResolveExecutor(expected.Definition.Ref); err == nil {
			t.Fatalf("API contract registry exposed executor for %s", expected.Definition.Ref.Name)
		}
	}
}

func TestAPIWorkflowRegistrationAlwaysExposesModelContractsAndDefinitions(t *testing.T) {
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerAPIWorkflowExecutors(executors); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerAPIWorkflowDefinitions(definitions); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	definition, resolveErr := definitions.Resolve(agentworkflow.RelationAssessmentDefinitionKey, agentworkflow.RelationAssessmentDefinitionVersion)
	if resolveErr != nil || len(definition.Graph.Nodes) != 1 || definition.Graph.Nodes[0].Kind != agentworkflow.RelationAssessmentNodeKind ||
		!executors.SupportsContract(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion) {
		t.Fatalf("definition=%+v err=%v", definition, resolveErr)
	}
	ragDefinition, ragResolveErr := definitions.Resolve(conversationworkflow.DefinitionKey, conversationworkflow.DefinitionVersion)
	if ragResolveErr != nil || len(ragDefinition.Graph.Nodes) != 1 || ragDefinition.Graph.Nodes[0].Kind != conversationworkflow.NodeKind ||
		!executors.SupportsContract(conversationworkflow.NodeKind, conversationworkflow.InputSchemaVersion) {
		t.Fatalf("rag definition=%+v err=%v", ragDefinition, ragResolveErr)
	}
}

func TestNewRetrievalHandlerRequiresDependencies(t *testing.T) {
	if handler, err := newRetrievalHandler(nil, nil, nil, nil); err == nil || handler != nil {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestNewGraphHandlerRequiresDatabase(t *testing.T) {
	if handler, err := newGraphHandler(nil, time.Second); err == nil || handler != nil {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestNewGraphHandlerComposesProductionDependencies(t *testing.T) {
	handler, err := newGraphHandler(&pgxpool.Pool{}, time.Second)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestNewConversationHandlersKeepReadFeedbackAndSSEWhenQuestionDispatchUnavailable(t *testing.T) {
	conversationHandler, eventsHandler, err := newConversationHandlers(&pgxpool.Pool{}, nil, false)
	if err != nil || conversationHandler == nil || eventsHandler == nil {
		t.Fatalf("disabled conversation=%#v events=%#v err=%v", conversationHandler, eventsHandler, err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/92000000-0000-4000-8000-000000000002/questions", strings.NewReader(`{"workspace_id":"92000000-0000-4000-8000-000000000001","question":"q"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "disabled-question")
	response := httptest.NewRecorder()
	router := gin.New()
	conversationHandler.Routes(router.Group("/api/v1"))
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "CONVERSATION_QUESTION_SUBMISSION_UNAVAILABLE") {
		t.Fatalf("disabled question status=%d body=%s", response.Code, response.Body.String())
	}

	if handler, stream, enabledErr := newConversationHandlers(&pgxpool.Pool{}, nil, true); enabledErr == nil || handler != nil || stream != nil {
		t.Fatalf("enabled missing runtime conversation=%#v events=%#v err=%v", handler, stream, enabledErr)
	}
}

func TestQuestionDispatchRequiresFullyInitializedRAGDependencies(t *testing.T) {
	for _, test := range []struct {
		name    string
		initErr error
		want    bool
	}{
		{name: "initialized", want: true},
		{name: "initialization failed", initErr: errors.New("dependency failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := questionDispatchEnabled(test.initErr); got != test.want {
				t.Fatalf("questionDispatchEnabled(%v)=%t want=%t", test.initErr, got, test.want)
			}
		})
	}
}

func TestNewRetrievalHandlerComposesDisabledAndEnabledEmbedderWithoutSecretLeak(t *testing.T) {
	pool := &pgxpool.Pool{}
	repository := fakeSourceMaterialRepository{}
	files := fakeFileScanner{}

	handler, err := newRetrievalHandler(pool, nil, repository, files)
	if err != nil || handler == nil {
		t.Fatalf("disabled handler=%#v err=%v", handler, err)
	}

	cfg := config.Defaults()
	cfg.EmbeddingProvider = config.EmbeddingProviderOpenAICompatible
	cfg.EmbeddingBaseURL = "https://models.example.test"
	cfg.EmbeddingAPIKey = "api-composition-secret-canary"
	cfg.EmbeddingModel = "embed-v1"
	cfg.EmbeddingDimensions = 3
	cfg.EmbeddingNormalization = retrievaldomain.NormalizationL2
	cfg.EmbeddingDistanceMetric = retrievaldomain.DistanceCosine
	models := mustAPIModels(t, cfg)
	handler, err = newRetrievalHandler(pool, models.Embedding().Embedder(), repository, files)
	if err != nil || handler == nil {
		t.Fatalf("enabled handler=%#v err=%v", handler, err)
	}

	cfg.EmbeddingProvider = "unsupported"
	_, err = modelsettingsruntime.LoadSettings(context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("invalid model runtime unexpectedly succeeded")
	}
	if strings.Contains(fmt.Sprintf("%v", err), cfg.EmbeddingAPIKey) {
		t.Fatal("retrieval composition error leaked embedding credential")
	}
}

func TestAPIRegistersModelDefinitionContractsWithoutFakeExecutors(t *testing.T) {
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerAPIWorkflowExecutors(executors); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	if !executors.SupportsContract(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion) {
		t.Fatal("api did not register the agent node contract")
	}
	if _, err := executors.Resolve(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion); err == nil {
		t.Fatal("api constructed a fake agent executor")
	}
	if !executors.SupportsContract(conversationworkflow.NodeKind, conversationworkflow.InputSchemaVersion) {
		t.Fatal("api did not register the RAG node contract")
	}
	if _, err := executors.Resolve(conversationworkflow.NodeKind, conversationworkflow.InputSchemaVersion); err == nil {
		t.Fatal("api constructed a fake RAG executor")
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerAPIWorkflowDefinitions(definitions); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	resolved, err := definitions.Resolve(agentworkflow.RelationAssessmentDefinitionKey, agentworkflow.RelationAssessmentDefinitionVersion)
	if err != nil || len(resolved.Graph.Nodes) != 1 {
		t.Fatalf("definition=%+v err=%v", resolved, err)
	}
	rag, err := definitions.Resolve(conversationworkflow.DefinitionKey, conversationworkflow.DefinitionVersion)
	if err != nil || rag.GraphHash != conversationworkflow.RegisteredDefinition().GraphHash {
		t.Fatalf("rag definition=%+v err=%v", rag, err)
	}
	var classified *foundation.Error
	_, resolveErr := executors.Resolve(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion)
	if !errors.As(resolveErr, &classified) || classified.Code != "WORKFLOW_EXECUTOR_NOT_REGISTERED" {
		t.Fatalf("resolve err=%v", resolveErr)
	}
}

func TestAPINeverExposesInternalToolWorkflowThroughGenericStartRegistry(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := config.Defaults()
		if enabled {
			cfg.ToolRuntimeMode = config.ToolModeEnabled
		}
		catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
		if err != nil {
			t.Fatal(err)
		}
		executors, err := workflowapplication.NewExecutorRegistry(catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err := registerAPIWorkflowExecutors(executors); err != nil {
			t.Fatal(err)
		}
		if err := executors.Freeze(); err != nil {
			t.Fatal(err)
		}
		toolContracts, err := newToolContractRegistry()
		if err != nil {
			t.Fatal(err)
		}
		definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors, toolContracts)
		if err != nil {
			t.Fatal(err)
		}
		if err := registerAPIWorkflowDefinitions(definitions); err != nil {
			t.Fatal(err)
		}
		if err := definitions.Freeze(); err != nil {
			t.Fatal(err)
		}
		definition, resolveErr := definitions.Resolve(toolworkflow.DefinitionKey, toolworkflow.DefinitionVersion)
		if resolveErr == nil || executors.SupportsContract(toolworkflow.NodeKind, toolworkflow.InputSchemaVersion) {
			t.Fatalf("generic API exposed internal tool workflow enabled=%t definition=%+v err=%v", enabled, definition, resolveErr)
		}
		if _, err := toolContracts.ResolveContract(toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 1}); err != nil {
			t.Fatalf("API tool contract catalog unavailable enabled=%t err=%v", enabled, err)
		}
	}
}

type fakeSourceMaterialRepository struct{}

func mustAPIModels(t *testing.T, cfg config.Config) *modelsettingsruntime.Models {
	t.Helper()
	loaded, err := modelsettingsruntime.LoadSettings(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return loaded.Models
}

func (fakeSourceMaterialRepository) GetSourceMaterial(context.Context, foundation.ID) (workspacedomain.SourceMaterial, error) {
	return workspacedomain.SourceMaterial{}, nil
}

type fakeFileScanner struct{}

func (fakeFileScanner) CanonicalRoot(path string) (string, error) { return path, nil }

func (fakeFileScanner) Scan(context.Context, string) ([]workspacedomain.ScannedFile, error) {
	return nil, nil
}

func (fakeFileScanner) Capture(context.Context, string, workspacedomain.ScannedFile) (workspacedomain.ContentCapture, error) {
	return workspacedomain.ContentCapture{}, nil
}

func (fakeFileScanner) ReadArtifact(context.Context, string, workspacedomain.ContentArtifact) ([]byte, error) {
	return nil, nil
}
