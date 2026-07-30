package application

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	renderWorkspaceID  foundation.ID = "11111111-1111-4111-8111-111111111111"
	renderCollectionID foundation.ID = "22222222-2222-4222-8222-222222222222"
	renderItemID       foundation.ID = "33333333-3333-4333-8333-333333333333"
)

func TestRenderNeverLeaksSecretsAbsolutePathsOrFormulaPrefixes(t *testing.T) {
	now := time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC)
	version := int64(3)
	snapshot := CollectionSnapshot{
		WorkspaceID: renderWorkspaceID, CollectionID: pointerFoundationID(renderCollectionID), CollectionVersion: &version,
		QueryHash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 1, Name: "Collection",
		Items: []Item{{
			ObjectType: "CLAIM", ID: renderItemID, Title: "=SUM(1,1)", CreatedAt: now, UpdatedAt: now,
			Sources: []Source{
				{Type: "MARKDOWN", Path: "/Users/private/source.md", Support: "SUPPORTS", CreatedAt: now},
				{Type: "MARKDOWN", Path: "docs/safe.md", Support: "SUPPORTS", CreatedAt: now},
			},
			Applicability: json.RawMessage(`{"nested":{"secret":"Bearer top-secret","path":"/Users/private/app.md","url":"https://example.test/usr/help"}}`),
		}},
	}
	base := domain.Job{WorkspaceID: renderWorkspaceID, Kind: domain.KindMetadataJSON, SchemaVersion: SchemaVersionV1, Scope: domain.Scope{Kind: domain.ScopeCollection, CollectionID: pointerFoundationID(renderCollectionID), CollectionVersion: &version, QueryHash: snapshot.QueryHash}, Fields: []domain.Field{domain.FieldTitle, domain.FieldSource, domain.FieldApplicability}}

	masked := base
	masked.Redaction = domain.RedactionMasked
	payload, extension, err := Render(snapshot, masked)
	if err != nil || extension != "json" {
		t.Fatalf("Render(masked) extension=%q err=%v", extension, err)
	}
	decoded := decodeEnvelope(t, payload)
	item := decoded.Items[0]
	sources := item[string(domain.FieldSource)].([]any)
	if got := sources[0].(map[string]any)["file_path"]; got != "<redacted-path>" {
		t.Fatalf("masked source path=%v", got)
	}
	if got := sources[1].(map[string]any)["file_path"]; got != "docs/safe.md" {
		t.Fatalf("masked relative source path=%v", got)
	}
	if got := item[string(domain.FieldTitle)]; got != "'=SUM(1,1)" {
		t.Fatalf("masked formula title=%v", got)
	}
	value := item[string(domain.FieldApplicability)].(map[string]any)["value"].(map[string]any)["nested"].(map[string]any)
	if value["secret"] != "<redacted>" || value["path"] != "<redacted-path>" || value["url"] != "https://example.test/usr/help" {
		t.Fatalf("masked applicability=%#v", value)
	}

	full := base
	full.Redaction = domain.RedactionFull
	full.IncludeSensitive = true
	payload, _, err = Render(snapshot, full)
	if err != nil {
		t.Fatalf("Render(full) error=%v", err)
	}
	decoded = decodeEnvelope(t, payload)
	item = decoded.Items[0]
	sources = item[string(domain.FieldSource)].([]any)
	if got := sources[0].(map[string]any)["file_path"]; got != "<redacted-path>" {
		t.Fatalf("full source path=%v", got)
	}
	value = item[string(domain.FieldApplicability)].(map[string]any)["value"].(map[string]any)["nested"].(map[string]any)
	if value["secret"] != "<redacted>" || value["path"] != "<redacted-path>" {
		t.Fatalf("full applicability=%#v", value)
	}
	if strings.Contains(string(payload), "/Users/private") || strings.Contains(string(payload), "top-secret") {
		t.Fatalf("full export leaked a canary: %s", payload)
	}
}

func decodeEnvelope(t *testing.T, payload []byte) exportEnvelope {
	t.Helper()
	var envelope exportEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func pointerFoundationID(value foundation.ID) *foundation.ID { return &value }
