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
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewArtifactHandlerRequiresCoreDependencies(t *testing.T) {
	pool := &pgxpool.Pool{}
	workspaces, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		pool       *pgxpool.Pool
		workspaces *workspacepostgres.Repository
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
	pool := &pgxpool.Pool{}
	workspaces, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}

	handler, err := newArtifactHandler(pool, workspaces, fakeFileScanner{}, nil, time.Second)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestArtifactCompositionKeepsCoreAvailableAndGatesGeneration(t *testing.T) {
	pool := &pgxpool.Pool{}
	workspaces, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}

	disabledGeneration := artifactGenerationDependencies(false, artifactGenerationStarterFake{})
	disabled, err := newArtifactHandler(pool, workspaces, fakeFileScanner{}, nil, time.Second, disabledGeneration...)
	if err != nil || disabled == nil || !disabled.Available() {
		t.Fatalf("disabled handler=%#v err=%v", disabled, err)
	}
	disabledResponse := postArtifactGeneration(t, disabled, "disabled-generation")
	if disabledResponse.Code != http.StatusServiceUnavailable || !strings.Contains(disabledResponse.Body.String(), "ARTIFACT_GENERATION_CAPABILITY_UNAVAILABLE") || strings.Contains(disabledResponse.Body.String(), `"retryable":true`) {
		t.Fatalf("disabled generation status=%d body=%s", disabledResponse.Code, disabledResponse.Body.String())
	}

	enabledGeneration := artifactGenerationDependencies(true, artifactGenerationStarterFake{})
	enabled, err := newArtifactHandler(pool, workspaces, fakeFileScanner{}, nil, time.Second, enabledGeneration...)
	if err != nil || enabled == nil || !enabled.Available() {
		t.Fatalf("enabled handler=%#v err=%v", enabled, err)
	}
	enabledResponse := postArtifactGeneration(t, enabled, "enabled-generation")
	if enabledResponse.Code != http.StatusAccepted || !strings.Contains(enabledResponse.Body.String(), `"status_url":"/api/v1/workflows/`) {
		t.Fatalf("enabled generation status=%d body=%s", enabledResponse.Code, enabledResponse.Body.String())
	}
}

func TestAPIArtifactWorkflowCompositionUsesOneAuthoritativeRuntimeWithoutChat(t *testing.T) {
	pool := &pgxpool.Pool{}
	workspaces, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, runtime, generation, err := newAPIArtifactWorkflowComponents(
		pool, config.Defaults(), workspaces, fakeFileScanner{}, nil,
		foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil || service == nil || runtime == nil || generation == nil {
		t.Fatalf("service=%#v runtime=%#v generation=%#v err=%v", service, runtime, generation, err)
	}
}

func postArtifactGeneration(t *testing.T, handler interface{ Routes(chi.Router) }, key string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/artifacts/93000000-0000-4000-8000-000000000002/sections/generate",
		strings.NewReader(`{"workspace_id":"93000000-0000-4000-8000-000000000001","expected_version":3,"section_key":"scope"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	router := chi.NewRouter()
	router.Route("/api/v1", handler.Routes)
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
