package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestDecodeToolRequestV1AcceptsOnlyProjectOwnedWire(t *testing.T) {
	raw := []byte(" \n{\"schema_version\":1,\"tool_name\":\"SearchKnowledge\",\"arguments\":{\"query\":\"证据\",\"filters\":{\"limit\":3}},\"reason\":\"需要检索已批准知识\"}\n")
	want := append([]byte(nil), raw...)
	request, err := DecodeToolRequestV1(raw)
	if err != nil {
		t.Fatalf("DecodeToolRequestV1() error = %v", err)
	}
	if request.SchemaVersion != toolsdomain.ToolRequestSchemaVersionV1 || request.ToolName != "SearchKnowledge" ||
		string(request.Arguments) != `{"query":"证据","filters":{"limit":3}}` || request.Reason != "需要检索已批准知识" {
		t.Fatalf("DecodeToolRequestV1() = %#v", request)
	}
	document, err := DecodeDocument(raw)
	if err != nil {
		t.Fatalf("DecodeDocument() error = %v", err)
	}
	if string(document) != string(want) {
		t.Fatalf("DecodeDocument() transformed accepted input:\n got %q\nwant %q", document, want)
	}
	document[0] = 'x'
	request.Arguments[0] = '['
	if string(raw) != string(want) {
		t.Fatal("decoder result aliases or mutates the caller input")
	}
}

func TestDecodeToolRequestV1RejectsInvalidCorpus(t *testing.T) {
	valid := `{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":"需要检索"}`
	tests := map[string][]byte{
		"invalid utf8":       append([]byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":"`), 0xff, '"', '}'),
		"top level array":    []byte(`[]`),
		"duplicate root":     []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","tool_name":"ReadSource","arguments":{},"reason":"x"}`),
		"duplicate argument": []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{"query":"a","query":"b"},"reason":"x"}`),
		"unknown workspace":  []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":"x","workspace_id":"forged"}`),
		"unknown capability": []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":"x","capability":"READ_LOCAL"}`),
		"unknown approval":   []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":"x","approval_id":"forged"}`),
		"unknown credential": []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":"x","credential":"secret"}`),
		"unknown endpoint":   []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":"x","endpoint":"https://example.test"}`),
		"unknown command":    []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":"x","command":"sh"}`),
		"trailing":           []byte(valid + ` {}`),
		"missing version":    []byte(`{"tool_name":"SearchKnowledge","arguments":{},"reason":"x"}`),
		"missing tool name":  []byte(`{"schema_version":1,"arguments":{},"reason":"x"}`),
		"missing arguments":  []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","reason":"x"}`),
		"missing reason":     []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{}}`),
		"null version":       []byte(`{"schema_version":null,"tool_name":"SearchKnowledge","arguments":{},"reason":"x"}`),
		"string version":     []byte(`{"schema_version":"1","tool_name":"SearchKnowledge","arguments":{},"reason":"x"}`),
		"wrong version":      []byte(`{"schema_version":2,"tool_name":"SearchKnowledge","arguments":{},"reason":"x"}`),
		"null tool name":     []byte(`{"schema_version":1,"tool_name":null,"arguments":{},"reason":"x"}`),
		"numeric tool name":  []byte(`{"schema_version":1,"tool_name":7,"arguments":{},"reason":"x"}`),
		"bad tool name":      []byte(`{"schema_version":1,"tool_name":"search_knowledge","arguments":{},"reason":"x"}`),
		"null arguments":     []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":null,"reason":"x"}`),
		"array arguments":    []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":[],"reason":"x"}`),
		"scalar arguments":   []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":"x","reason":"x"}`),
		"null reason":        []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":null}`),
		"numeric reason":     []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":7}`),
		"blank reason":       []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":"  "}`),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeToolRequestV1(raw); errorCode(err) != toolsdomain.ErrorCodeRequestInvalid {
				t.Fatalf("invalid request accepted: %s", raw)
			}
		})
	}
}

func TestDecodeToolRequestV1EnforcesResourceLimits(t *testing.T) {
	deep := `{"value":1}`
	for range maxToolRequestDepth {
		deep = `{"nested":` + deep + `}`
	}
	fields := make([]string, maxToolRequestObjectFields+1)
	for index := range fields {
		fields[index] = fmt.Sprintf(`"f%03d":0`, index)
	}
	arrays := make([]string, maxToolRequestArrayItems+1)
	for index := range arrays {
		arrays[index] = "0"
	}
	tests := map[string][]byte{
		"document bytes": []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{"value":"` + strings.Repeat("x", maxToolRequestDocumentBytes) + `"},"reason":"x"}`),
		"depth":          []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":` + deep + `,"reason":"x"}`),
		"string bytes":   []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{"value":"` + strings.Repeat("x", maxToolRequestStringBytes+1) + `"},"reason":"x"}`),
		"array items":    []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{"items":[` + strings.Join(arrays, ",") + `]},"reason":"x"}`),
		"object fields":  []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{` + strings.Join(fields, ",") + `},"reason":"x"}`),
		"reason bytes":   []byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{},"reason":"` + strings.Repeat("x", toolsdomain.MaxToolReasonBytes+1) + `"}`),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeToolRequestV1(raw)
			if err == nil {
				t.Fatal("resource limit was not enforced")
			}
			if errorCode(err) != toolsdomain.ErrorCodeRequestInvalid {
				t.Fatalf("error=%v code=%q", err, errorCode(err))
			}
		})
	}
}

func errorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func TestSchemaDefinitionMatchesIndependentV1Contract(t *testing.T) {
	definition := SchemaDefinition()
	if definition.Ref != (agentdomain.SchemaRef{ID: agentdomain.ToolRequestSchemaID, Version: agentdomain.OutputSchemaVersionV1}) ||
		ResultType != agentdomain.ResultTypeToolRequest || definition.Decode == nil {
		t.Fatalf("definition=%#v result_type=%q", definition, ResultType)
	}
	var schema struct {
		Type                 string `json:"type"`
		AdditionalProperties bool   `json:"additionalProperties"`
		Required             []string
		Properties           map[string]json.RawMessage
	}
	if err := json.Unmarshal(definition.JSONSchema, &schema); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"schema_version", "tool_name", "arguments", "reason"}
	if schema.Type != "object" || schema.AdditionalProperties || strings.Join(schema.Required, ",") != strings.Join(wantFields, ",") || len(schema.Properties) != len(wantFields) {
		t.Fatalf("schema=%s", definition.JSONSchema)
	}
	for _, field := range wantFields {
		if _, ok := schema.Properties[field]; !ok {
			t.Fatalf("schema omitted %q: %s", field, definition.JSONSchema)
		}
	}
	definition.JSONSchema[0] = '['
	again := SchemaDefinition()
	if again.JSONSchema[0] != '{' {
		t.Fatal("SchemaDefinition returned shared mutable schema bytes")
	}
}
