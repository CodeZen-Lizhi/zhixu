//go:build integration

package main

import (
	"io"
	"log/slog"
	"os"
	"testing"

	graphapplication "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
)

func TestWorkerRegistersTopicV1AndSmartCollectionV2SemanticScans(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a PostgreSQL admin database")
	}
	pool := newMigratedWorkerTestPool(t, databaseURL)
	components, err := newWorkerComponents(pool, config.Defaults(), slog.New(slog.NewTextHandler(io.Discard, nil)), observability.NewMemoryMetrics())
	if err != nil {
		t.Fatal(err)
	}
	for _, schemaVersion := range []int{
		graphapplication.SemanticLinkScanInputSchemaVersion,
		graphapplication.SemanticLinkSmartCollectionScanInputSchemaVersion,
	} {
		if _, err := components.executors.Resolve(graphapplication.SemanticLinkScanNodeKind, schemaVersion); err != nil {
			t.Fatalf("semantic scan executor schema %d is unreachable: %v", schemaVersion, err)
		}
	}
	for _, definitionVersion := range []int64{
		graphapplication.SemanticLinkScanWorkflowDefinitionVersion,
		graphapplication.SemanticLinkSmartCollectionScanWorkflowDefinitionVersion,
	} {
		definition, err := components.definitions.Resolve(graphapplication.SemanticLinkScanWorkflowDefinitionKey, definitionVersion)
		if err != nil {
			t.Fatalf("semantic scan definition version %d is unreachable: %v", definitionVersion, err)
		}
		if len(definition.Graph.Nodes) != 1 || definition.Graph.Nodes[0].InputSchemaVersion != int(definitionVersion) {
			t.Fatalf("semantic scan definition=%#v", definition)
		}
	}
}
