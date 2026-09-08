package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	workspaceruntimegrant "github.com/CodeZen-Lizhi/zhixu/internal/workspace/runtimegrant"
	"github.com/gin-gonic/gin"
)

func TestNewArtifactHandlerRequiresCoreDependencies(t *testing.T) {
	pool := apiConstructorPool(t)
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		pool       *platformpostgres.Pool
		workspaces workspaceruntimegrant.GORMRepositoryPort
		files      workspacedomain.FileScanner
	}{
		{name: "database", pool: nil, workspaces: workspaces, files: fakeFileScanner{}},
		{name: "workspace repository", pool: pool, workspaces: nil, files: fakeFileScanner{}},
		{name: "file scanner", pool: pool, workspaces: workspaces},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, err := newArtifactHandler(test.pool, test.workspaces, test.files, nil, time.Second)
			if err == nil || handler != nil {
				t.Fatalf("handler=%#v err=%v", handler, err)
			}
		})
	}
}

func TestNewArtifactHandlerComposesCoreServicesWithoutPublisher(t *testing.T) {
	pool := apiConstructorPool(t)
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}

	handler, err := newArtifactHandler(pool, workspaces, fakeFileScanner{}, nil, time.Second)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestArtifactCompositionKeepsCoreAvailableAndInjectsPersistedGeneration(t *testing.T) {
	pool := apiConstructorPool(t)
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}

	unavailableGeneration := artifactGenerationDependencies(nil)
	unavailable, err := newArtifactHandler(pool, workspaces, fakeFileScanner{}, nil, time.Second, unavailableGeneration...)
	if err != nil || unavailable == nil || !unavailable.Available() {
		t.Fatalf("unavailable handler=%#v err=%v", unavailable, err)
	}
	unavailableResponse := postArtifactGeneration(t, unavailable, "unavailable-generation")
	if unavailableResponse.Code != http.StatusServiceUnavailable || !strings.Contains(unavailableResponse.Body.String(), "ARTIFACT_GENERATION_CAPABILITY_UNAVAILABLE") || strings.Contains(unavailableResponse.Body.String(), `"retryable":true`) {
		t.Fatalf("unavailable generation status=%d body=%s", unavailableResponse.Code, unavailableResponse.Body.String())
	}

	availableGeneration := artifactGenerationDependencies(artifactGenerationStarterFake{})
	available, err := newArtifactHandler(pool, workspaces, fakeFileScanner{}, nil, time.Second, availableGeneration...)
	if err != nil || available == nil || !available.Available() {
		t.Fatalf("available handler=%#v err=%v", available, err)
	}
	availableResponse := postArtifactGeneration(t, available, "available-generation")
	if availableResponse.Code != http.StatusAccepted || !strings.Contains(availableResponse.Body.String(), `"status_url":"/api/v1/workflows/`) {
		t.Fatalf("available generation status=%d body=%s", availableResponse.Code, availableResponse.Body.String())
	}
}

func TestAPIArtifactWorkflowCompositionUsesOneAuthoritativeRuntime(t *testing.T) {
	pool := apiConstructorPool(t)
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, runtime, generation, err := newAPIArtifactWorkflowComponents(
		pool, config.Defaults(), workspaces, fakeFileScanner{}, nil,
		foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, nil,
	)
	if err != nil || service == nil || runtime == nil || generation == nil {
		t.Fatalf("service=%#v runtime=%#v generation=%#v err=%v", service, runtime, generation, err)
	}
}

func postArtifactGeneration(t *testing.T, handler interface{ Routes(gin.IRouter) }, key string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/artifacts/93000000-0000-4000-8000-000000000002/sections/generate",
		strings.NewReader(`{"workspace_id":"93000000-0000-4000-8000-000000000001","expected_version":3,"section_key":"scope"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	router := gin.New()
	handler.Routes(router.Group("/api/v1"))
	router.ServeHTTP(response, request)
	return response
}

type artifactGenerationStarterFake struct{}

func (artifactGenerationStarterFake) StartSectionGeneration(_ context.Context, command artifactapplication.StartSectionGenerationCommand) (artifactapplication.StartSectionGenerationResult, error) {
	requestHash, err := artifactapplication.ComputeSectionGenerationRequestHash(command)
	if err != nil {
		return artifactapplication.StartSectionGenerationResult{}, err
	}
	at := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	return artifactapplication.StartSectionGenerationResult{Generation: artifactapplication.SectionGeneration{
		ID: "93000000-0000-4000-8000-000000000003", WorkspaceID: command.WorkspaceID, ArtifactID: command.ArtifactID,
		SourceRevisionID: "93000000-0000-4000-8000-000000000004", SourceRevisionNo: 3, SourceArtifactVersion: command.ExpectedVersion,
		SectionKey: command.SectionKey, IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash,
		WorkflowRunID: "93000000-0000-4000-8000-000000000005", NodeRunID: "93000000-0000-4000-8000-000000000006",
		Status: artifactapplication.SectionGenerationPending, Version: 1, CreatedAt: at, UpdatedAt: at,
	}}, nil
}

var _ artifactapplication.SectionGenerationStarter = artifactGenerationStarterFake{}
