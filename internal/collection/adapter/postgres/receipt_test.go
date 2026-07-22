package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCollectionCommandReceiptRoundTripAndStrictBinding(t *testing.T) {
	collection := receiptTestCollection()
	key := "receipt-test"
	hash, err := collectionapp.ComputeRequestHash("CREATE", collection.WorkspaceID, "", 0, &collection)
	if err != nil {
		t.Fatal(err)
	}
	raw := commandReceiptJSON(collection, key, "CREATE", hash)
	payload, err := decodeReceiptPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	receipt := commandReceipt{workspaceID: collection.WorkspaceID, idempotencyKey: key, requestHash: hash, commandType: "CREATE", collectionID: collection.ID, collectionVersion: collection.Version}
	if err := validateCommandReceiptPayload(payload, key, receipt); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}

	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "workspace binding", mutate: func(value map[string]any) { value["workspace_id"] = "00000000-0000-4000-8000-000000000000" }},
		{name: "snapshot tamper", mutate: func(value map[string]any) {
			snapshot := value["collection"].(map[string]any)
			snapshot["Name"] = "tampered"
		}},
		{name: "unknown receipt field", mutate: func(value map[string]any) { value["unexpected"] = true }},
		{name: "schema downgrade", mutate: func(value map[string]any) { value["schema_version"] = "collection-command-receipt/v1" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyDocument := make(map[string]any, len(document))
			encodedDocument, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encodedDocument, &copyDocument); err != nil {
				t.Fatal(err)
			}
			test.mutate(copyDocument)
			tampered, err := json.Marshal(copyDocument)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decodeReceiptPayload(tampered)
			if err == nil {
				err = validateCommandReceiptPayload(decoded, key, receipt)
			}
			if err == nil {
				t.Fatal("tampered receipt was accepted")
			}
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != collectionapp.ErrorCodeResultInconsistent {
				t.Fatalf("tampered receipt error=%v", err)
			}
		})
	}
}

func receiptTestCollection() collectionapp.Collection {
	query := domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: "AND", Clauses: []domain.Clause{{Kind: domain.ClauseKindPredicate, Field: "object_type", Operator: "EQ", Value: json.RawMessage(`"TOPIC"`)}}}}
	canonical, _ := domain.CanonicalizeQuery(query)
	workspaceID := foundation.ID("91000000-0000-4000-8000-000000000001")
	collectionID := foundation.ID("91000000-0000-4000-8000-000000000002")
	now := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	return collectionapp.Collection{ID: collectionID, WorkspaceID: workspaceID, Name: "Inbox", NormalizedName: "inbox", Description: "desc", QuerySchemaVersion: domain.QuerySchemaVersionV1, QueryVersion: 1, Query: canonical.Definition, QueryHash: canonical.Hash, ViewType: domain.ViewTypeList, ViewConfig: domain.ViewConfig{Density: "COMFORTABLE"}, Status: collectionapp.CollectionStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
}
