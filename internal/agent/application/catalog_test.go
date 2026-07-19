package application

import (
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRuntimeCatalogFreezesExactVersionsAndCopiesSchema(t *testing.T) {
	catalog := NewRuntimeCatalog()
	prompt := testPrompt()
	schema := testSchema("answer", "v1", false)
	profile := testProfile(time.Second)
	if err := catalog.RegisterPrompt(prompt); err != nil {
		t.Fatalf("RegisterPrompt() error = %v", err)
	}
	if err := catalog.RegisterSchema(schema); err != nil {
		t.Fatalf("RegisterSchema() error = %v", err)
	}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatalf("RegisterProfile() error = %v", err)
	}
	if err := catalog.RegisterPrompt(prompt); errorCode(err) != errorCodePromptDuplicate {
		t.Fatalf("duplicate prompt code = %q, want %q", errorCode(err), errorCodePromptDuplicate)
	}
	if err := catalog.RegisterSchema(schema); errorCode(err) != errorCodeSchemaDuplicate {
		t.Fatalf("duplicate schema code = %q, want %q", errorCode(err), errorCodeSchemaDuplicate)
	}
	if err := catalog.RegisterProfile(profile); errorCode(err) != errorCodeProfileDuplicate {
		t.Fatalf("duplicate profile code = %q, want %q", errorCode(err), errorCodeProfileDuplicate)
	}

	schema.JSONSchema[0] = '['
	if err := catalog.Freeze(); err != nil {
		t.Fatalf("Freeze() error = %v", err)
	}
	snapshot, err := catalog.Snapshot(prompt.Ref, domain.SchemaRef{ID: "answer", Version: "v1"}, domain.SchemaRef{ID: "answer", Version: "v1"}, profile.Ref)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if string(snapshot.Schema.JSONSchema) != `{"type":"object"}` {
		t.Fatalf("snapshot schema = %q", snapshot.Schema.JSONSchema)
	}
	snapshot.Schema.JSONSchema[0] = '['
	again, err := catalog.Snapshot(prompt.Ref, snapshot.ReducedSchema.Ref, snapshot.ReducedSchema.Ref, profile.Ref)
	if err != nil {
		t.Fatalf("second Snapshot() error = %v", err)
	}
	if string(again.Schema.JSONSchema) != `{"type":"object"}` {
		t.Fatalf("catalog schema mutated through snapshot: %q", again.Schema.JSONSchema)
	}
	if err := catalog.RegisterPrompt(PromptDefinition{}); errorCode(err) != errorCodeCatalogFrozen {
		t.Fatalf("invalid frozen registration code = %q", errorCode(err))
	}
}

func TestRuntimeCatalogFailsClosedWhenNotFrozenOrVersionMissing(t *testing.T) {
	catalog := NewRuntimeCatalog()
	prompt := testPrompt()
	profile := testProfile(time.Second)
	schema := testSchema("answer", "v1", false)
	if _, err := catalog.Snapshot(prompt.Ref, schema.Ref, schema.Ref, profile.Ref); errorCode(err) != errorCodeCatalogNotFrozen {
		t.Fatalf("unfrozen snapshot code = %q", errorCode(err))
	}
	if err := catalog.RegisterPrompt(prompt); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(schema); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	missing := domain.SchemaRef{ID: "answer", Version: "v2"}
	if _, err := catalog.Snapshot(prompt.Ref, missing, schema.Ref, profile.Ref); errorCode(err) != errorCodeSchemaNotFound {
		t.Fatalf("missing exact version code = %q", errorCode(err))
	}
	if err := catalog.RegisterPrompt(PromptDefinition{
		Ref:                domain.PromptRef{ID: "other", Version: "v1"},
		System:             "system",
		InitialInstruction: "initial",
		RepairInstruction:  "repair",
		ReducedInstruction: "reduced",
	}); errorCode(err) != errorCodeCatalogFrozen {
		t.Fatalf("frozen registration code = %q, want %q", errorCode(err), errorCodeCatalogFrozen)
	}
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return ""
	}
	return classified.Code
}
