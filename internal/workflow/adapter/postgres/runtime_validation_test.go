package workflowpostgres

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const humanIdentitySchema = `{"type":"object","additionalProperties":false,"required":["version","processing_id","workflow_run_id","note_ids","result_hash"],"properties":{"version":{"type":"string","enum":["v1"]},"processing_id":{"type":"string","enum":["processing"]},"workflow_run_id":{"type":"string","enum":["run"]},"note_ids":{"type":"array","enum":[["note-a","note-b"]]},"result_hash":{"type":"string","pattern":"^[0-9a-f]{64}$"}}}`

func humanIdentityDecision() map[string]any {
	return map[string]any{"version": "v1", "processing_id": "processing", "workflow_run_id": "run", "note_ids": []string{"note-a", "note-b"}, "result_hash": strings.Repeat("a", 64)}
}

func TestDecisionSchemaIdentityConstraints(t *testing.T) {
	cases := []struct {
		name, field string
		value       any
	}{
		{"valid", "", nil}, {"version", "version", "v2"}, {"processing", "processing_id", "other"}, {"run", "workflow_run_id", "other"},
		{"notes", "note_ids", []string{"note-a", "other"}}, {"order", "note_ids", []string{"note-b", "note-a"}}, {"missing note", "note_ids", []string{"note-a"}},
		{"type", "processing_id", 1}, {"array element type", "note_ids", []any{"note-a", 1}},
		{"hash short", "result_hash", "abc"}, {"hash upper", "result_hash", strings.Repeat("A", 64)}, {"hash nonhex", "result_hash", strings.Repeat("g", 64)},
		{"hash type", "result_hash", true}, {"additional", "unknown", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := humanIdentityDecision()
			if tc.field != "" {
				decision[tc.field] = tc.value
			}
			raw, _ := json.Marshal(decision)
			code := ""
			if tc.field != "" {
				code = "HUMAN_DECISION_SCHEMA_INVALID"
			}
			assertDecisionSchemaCode(t, []byte(humanIdentitySchema), raw, code)
		})
	}
}

func TestDecisionSchemaMalformedConstraintsAndCompatibility(t *testing.T) {
	for _, property := range []string{`{"type":false}`, `{"type":["string"]}`, `{"type":"unknown"}`, `{"enum":[]}`, `{"enum":null}`, `{"enum":"a"}`, `{"pattern":false}`, `{"pattern":null}`, `{"pattern":"["}`} {
		// 可选字段缺省不能掩盖错误的任务 Schema。
		assertDecisionSchemaCode(t, []byte(`{"properties":{"value":`+property+`}}`), []byte(`{}`), "HUMAN_TASK_SCHEMA_UNSUPPORTED")
	}
	schema := []byte(`{"type":"object","required":["approved"],"properties":{"approved":{"type":"boolean"}},"additionalProperties":false}`)
	for _, value := range []string{`{"approved":true}`, `{"approved":false}`} {
		assertDecisionSchemaCode(t, schema, []byte(value), "")
	}
	for _, value := range []string{`{}`, `{"approved":"true"}`, `{"approved":true,"extra":1}`} {
		assertDecisionSchemaCode(t, schema, []byte(value), "HUMAN_DECISION_SCHEMA_INVALID")
	}
	assertDecisionSchemaCode(t, []byte(`{"properties":{"x":{"enum":[{"a":["x","y"],"b":true}]}}}`), []byte(`{"x":{"b":true,"a":["x","y"]}}`), "")
	assertDecisionSchemaCode(t, []byte(`{"properties":{"x":{"enum":[9007199254740992]}}}`), []byte(`{"x":9007199254740993}`), "HUMAN_DECISION_SCHEMA_INVALID")
	assertDecisionSchemaCode(t, []byte(`{"properties":{"x":{"enum":[1]}}}`), []byte(`{"x":1.0}`), "")
	assertDecisionSchemaCode(t, []byte(`{"properties":{"x":{"pattern":"abc"}}}`), []byte(`{"x":"prefix-abc-suffix"}`), "")
}

func assertDecisionSchemaCode(t *testing.T, schema, decision []byte, code string) {
	t.Helper()
	err := validateDecisionSchema(schema, decision)
	if code == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
