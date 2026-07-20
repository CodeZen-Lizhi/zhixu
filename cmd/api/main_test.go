package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolworkflow "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/workflow"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewWorkflowServiceRequiresDatabase(t *testing.T) {
	if service, err := newWorkflowService(nil); err == nil || service != nil {
		t.Fatalf("service=%#v err=%v", service, err)
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

func TestAPIWorkflowRegistrationExposesAgentDefinitionOnlyWhenChatEnabled(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
	}{
		{name: "disabled"},
		{name: "enabled", enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Defaults()
			if test.enabled {
				cfg.ChatProvider = config.ChatProviderOpenAICompatible
			}
			catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
			if err != nil {
				t.Fatal(err)
			}
			executors, err := workflowapplication.NewExecutorRegistry(catalog)
			if err != nil {
				t.Fatal(err)
			}
			if err := registerAPIWorkflowExecutors(cfg, executors); err != nil {
				t.Fatal(err)
			}
			if err := executors.Freeze(); err != nil {
				t.Fatal(err)
			}
			definitions, err := workflowapplication.NewDefinitionRegistry(catalog, executors)
			if err != nil {
				t.Fatal(err)
			}
			if err := registerAPIWorkflowDefinitions(cfg, definitions); err != nil {
				t.Fatal(err)
			}
			if err := definitions.Freeze(); err != nil {
				t.Fatal(err)
			}
			definition, resolveErr := definitions.Resolve(agentworkflow.RelationAssessmentDefinitionKey, agentworkflow.RelationAssessmentDefinitionVersion)
			ragDefinition, ragResolveErr := definitions.Resolve(conversationworkflow.DefinitionKey, conversationworkflow.DefinitionVersion)
			if test.enabled {
				if resolveErr != nil || len(definition.Graph.Nodes) != 1 || definition.Graph.Nodes[0].Kind != agentworkflow.RelationAssessmentNodeKind ||
					!executors.SupportsContract(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion) {
					t.Fatalf("definition=%+v err=%v", definition, resolveErr)
				}
				if ragResolveErr != nil || len(ragDefinition.Graph.Nodes) != 1 || ragDefinition.Graph.Nodes[0].Kind != conversationworkflow.NodeKind ||
					!executors.SupportsContract(conversationworkflow.NodeKind, conversationworkflow.InputSchemaVersion) {
					t.Fatalf("rag definition=%+v err=%v", ragDefinition, ragResolveErr)
				}
			} else if resolveErr == nil || executors.SupportsContract(agentworkflow.RelationAssessmentNodeKind, agentworkflow.RelationAssessmentInputSchemaVersion) {
				t.Fatalf("disabled chat exposed agent definition=%+v err=%v", definition, resolveErr)
			} else if ragResolveErr == nil || executors.SupportsContract(conversationworkflow.NodeKind, conversationworkflow.InputSchemaVersion) {
				t.Fatalf("disabled chat exposed rag definition=%+v err=%v", ragDefinition, ragResolveErr)
			}
		})
	}
}

func TestNewRetrievalHandlerRequiresDependencies(t *testing.T) {
	if handler, err := newRetrievalHandler(nil, config.Defaults(), nil, nil); err == nil || handler != nil {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestNewConversationHandlersKeepReadFeedbackAndSSECompositionWhenChatDisabled(t *testing.T) {
	conversationHandler, eventsHandler, err := newConversationHandlers(&pgxpool.Pool{}, nil, false)
	if err != nil || conversationHandler == nil || eventsHandler == nil {
		t.Fatalf("disabled conversation=%#v events=%#v err=%v", conversationHandler, eventsHandler, err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/92000000-0000-4000-8000-000000000002/questions", strings.NewReader(`{"workspace_id":"92000000-0000-4000-8000-000000000001","question":"q"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "disabled-question")
	response := httptest.NewRecorder()
	router := chi.NewRouter()
	router.Route("/api/v1", conversationHandler.Routes)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "CONVERSATION_QUESTION_SUBMISSION_UNAVAILABLE") {
		t.Fatalf("disabled question status=%d body=%s", response.Code, response.Body.String())
	}

	if handler, stream, enabledErr := newConversationHandlers(&pgxpool.Pool{}, nil, true); enabledErr == nil || handler != nil || stream != nil {
		t.Fatalf("enabled missing runtime conversation=%#v events=%#v err=%v", handler, stream, enabledErr)
	}
}

func TestQuestionDispatchRequiresEnabledAndFullyInitializedRAG(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		initErr error
		want    bool
	}{
		{name: "enabled and initialized", enabled: true, want: true},
		{name: "enabled but initialization failed", enabled: true, initErr: errors.New("dependency failed")},
		{name: "disabled"},
		{name: "disabled with irrelevant error", initErr: errors.New("ignored while disabled")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := questionDispatchEnabled(test.enabled, test.initErr); got != test.want {
				t.Fatalf("questionDispatchEnabled(%t,%v)=%t want=%t", test.enabled, test.initErr, got, test.want)
			}
		})
	}
}

func TestNewRetrievalHandlerComposesDisabledAndEnabledEmbedderWithoutSecretLeak(t *testing.T) {
	pool := &pgxpool.Pool{}
	repository := fakeSourceMaterialRepository{}
	files := fakeFileScanner{}

	handler, err := newRetrievalHandler(pool, config.Defaults(), repository, files)
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
	handler, err = newRetrievalHandler(pool, cfg, repository, files)
	if err != nil || handler == nil {
		t.Fatalf("enabled handler=%#v err=%v", handler, err)
	}

	cfg.EmbeddingProvider = "unsupported"
	handler, err = newRetrievalHandler(pool, cfg, repository, files)
	if err == nil || handler != nil {
		t.Fatalf("invalid handler=%#v err=%v", handler, err)
	}
	if strings.Contains(fmt.Sprintf("%v", err), cfg.EmbeddingAPIKey) {
		t.Fatal("retrieval composition error leaked embedding credential")
	}
}

func TestAPIRegistersAgentDefinitionContractWithoutFakeExecutorWhenChatEnabled(t *testing.T) {
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	if err := registerAPIWorkflowExecutors(cfg, executors); err != nil {
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
	if err := registerAPIWorkflowDefinitions(cfg, definitions); err != nil {
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
		if err := registerAPIWorkflowExecutors(cfg, executors); err != nil {
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
		if err := registerAPIWorkflowDefinitions(cfg, definitions); err != nil {
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
