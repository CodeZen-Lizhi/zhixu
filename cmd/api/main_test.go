package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewWorkflowServiceRequiresDatabase(t *testing.T) {
	if service, err := newWorkflowService(nil); err == nil || service != nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
}

func TestNewRetrievalHandlerRequiresDependencies(t *testing.T) {
	if handler, err := newRetrievalHandler(nil, config.Defaults(), nil, nil); err == nil || handler != nil {
		t.Fatalf("handler=%#v err=%v", handler, err)
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
