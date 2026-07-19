package workflow

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolagent "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/agent"
)

func TestRuntimeCatalogPublishesAdditiveConversationRAGSchemas(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		ref                   agentdomain.SchemaRef
		expectedPayloadFields []string
	}{
		{
			ref: agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1},
			expectedPayloadFields: []string{
				"intent", "requires_clarification", "rewrites", "clarification_reason", "clarification_question", "suggested_scopes",
			},
		},
		{
			ref: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV1},
			expectedPayloadFields: []string{
				"conclusion", "assertions", "citations", "conflict_positions", "conflict_summary",
			},
		},
		{
			ref: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV2},
			expectedPayloadFields: []string{
				"conclusion", "assertions", "citations", "conflict_positions", "conflict_summary", "related_topics", "follow_up_questions",
			},
		},
		{
			ref: agentdomain.SchemaRef{ID: conversationdomain.ClarificationSchemaID, Version: conversationdomain.ClarificationSchemaVersionV1},
			expectedPayloadFields: []string{
				"reason", "question", "suggested_scopes",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.ref.ID+"-"+test.ref.Version, func(t *testing.T) {
			snapshot, err := catalog.Snapshot(DefaultPromptRef(), test.ref, test.ref, DefaultProfileRef())
			if err != nil {
				t.Fatalf("snapshot(%s,%s): %v", test.ref.ID, test.ref.Version, err)
			}
			var document map[string]any
			if err := json.Unmarshal(snapshot.Schema.JSONSchema, &document); err != nil {
				t.Fatal(err)
			}
			if document["additionalProperties"] != false {
				t.Fatalf("schema=%s/%s root is not strict", test.ref.ID, test.ref.Version)
			}
			properties := document["properties"].(map[string]any)
			if got := properties["schema_version"].(map[string]any)["const"]; got != test.ref.Version {
				t.Fatalf("schema=%s/%s schema_version const=%v", test.ref.ID, test.ref.Version, got)
			}
			payload := properties["payload"].(map[string]any)
			if payload["additionalProperties"] != false {
				t.Fatalf("schema=%s/%s payload is not strict", test.ref.ID, test.ref.Version)
			}
			payloadProperties := payload["properties"].(map[string]any)
			propertyNames := make([]string, 0, len(payloadProperties))
			for name := range payloadProperties {
				propertyNames = append(propertyNames, name)
			}
			required := make([]string, 0, len(payload["required"].([]any)))
			for _, name := range payload["required"].([]any) {
				required = append(required, name.(string))
			}
			expected := append([]string(nil), test.expectedPayloadFields...)
			slices.Sort(propertyNames)
			slices.Sort(required)
			slices.Sort(expected)
			if !slices.Equal(propertyNames, expected) || !slices.Equal(required, expected) {
				t.Fatalf("schema=%s/%s payload=%+v", test.ref.ID, test.ref.Version, payload)
			}
		})
	}
}

func TestRuntimeCatalogKeepsIndependentToolRequestSchema(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	ref := toolagent.SchemaRef()
	if _, err := catalog.Snapshot(DefaultPromptRef(), ref, ref, DefaultProfileRef()); err != nil {
		t.Fatalf("tool request snapshot: %v", err)
	}
}

func TestRuntimeCatalogSeparatesRAGAnswerV1AndV2ByExactSnapshot(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	v1Ref := agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	v2Ref := agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV2}
	v1, err := catalog.Snapshot(DefaultPromptRef(), v1Ref, v1Ref, DefaultProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	v2, err := catalog.Snapshot(DefaultPromptRef(), v2Ref, v2Ref, DefaultProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	v1Raw := validRAGAnswerV1Document(t)
	v2Raw := validRAGAnswerV2Document(t)
	if decoded, err := v1.Schema.Decode(v1Raw); err != nil || string(decoded) != string(v1Raw) {
		t.Fatalf("v1 decode err=%v decoded=%s", err, decoded)
	}
	if decoded, err := v2.Schema.Decode(v2Raw); err != nil || string(decoded) != string(v2Raw) {
		t.Fatalf("v2 decode err=%v decoded=%s", err, decoded)
	}
	if _, err := v1.Schema.Decode(v2Raw); err == nil {
		t.Fatal("v1 snapshot accepted v2 answer")
	}
	if _, err := v2.Schema.Decode(v1Raw); err == nil {
		t.Fatal("v2 snapshot accepted v1 answer")
	}
}

func TestRuntimeCatalogNewSchemasUseStrictDecoders(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		ref         agentdomain.SchemaRef
		raw         []byte
		resultType  string
		replaceFrom string
		replaceTo   string
	}{
		{
			name:        "query-plan",
			ref:         agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1},
			raw:         validQueryPlanDocument(t),
			resultType:  agentdomain.ResultTypeRAGQueryPlan,
			replaceFrom: agentdomain.OutputSchemaVersionV1,
			replaceTo:   agentdomain.OutputSchemaVersionV2,
		},
		{
			name:        "rag-answer-v2",
			ref:         agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV2},
			raw:         validRAGAnswerV2Document(t),
			resultType:  agentdomain.ResultTypeRAGAnswer,
			replaceFrom: agentdomain.OutputSchemaVersionV2,
			replaceTo:   agentdomain.OutputSchemaVersionV1,
		},
		{
			name:        "clarification",
			ref:         agentdomain.SchemaRef{ID: conversationdomain.ClarificationSchemaID, Version: conversationdomain.ClarificationSchemaVersionV1},
			raw:         validClarificationDocument(t),
			resultType:  agentdomain.ResultTypeClarification,
			replaceFrom: conversationdomain.ClarificationSchemaVersionV1,
			replaceTo:   agentdomain.OutputSchemaVersionV2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := catalog.Snapshot(DefaultPromptRef(), test.ref, test.ref, DefaultProfileRef())
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := snapshot.Schema.Decode(test.raw)
			if err != nil {
				t.Fatalf("decode valid document: %v", err)
			}
			if string(decoded) != string(test.raw) {
				t.Fatalf("decoder transformed raw document: got=%s want=%s", decoded, test.raw)
			}
			for name, invalid := range map[string][]byte{
				"unknown":       withUnknownRootField(test.raw),
				"duplicate":     withDuplicateResultType(test.raw, test.resultType),
				"trailing":      withTrailingDocument(test.raw),
				"version-drift": withSchemaVersion(test.raw, test.replaceFrom, test.replaceTo),
			} {
				if _, err := snapshot.Schema.Decode(invalid); err == nil {
					t.Fatalf("%s document accepted: %s", name, invalid)
				}
			}
		})
	}
}

func validQueryPlanDocument(t *testing.T) []byte {
	t.Helper()
	document, err := json.Marshal(agentdomain.RAGQueryPlanResult{
		ResultType: agentdomain.ResultTypeRAGQueryPlan, SchemaID: agentdomain.RAGQueryPlanSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: testModelRunID,
		Payload: agentdomain.RAGQueryPlanPayload{
			Intent:                "compare deployment behavior",
			RequiresClarification: false,
			Rewrites:              []string{"deployment behavior", "runtime deployment behavior"},
			ClarificationReason:   "",
			ClarificationQuestion: "",
			SuggestedScopes:       []string{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func validRAGAnswerV1Document(t *testing.T) []byte {
	t.Helper()
	document, err := json.Marshal(agentdomain.RAGAnswerResult{
		ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: testModelRunID,
		Payload: validRAGAnswerPayloadBase(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func validRAGAnswerV2Document(t *testing.T) []byte {
	t.Helper()
	document, err := json.Marshal(agentdomain.RAGAnswerResultV2{
		ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV2, ModelRunRef: testModelRunID,
		Payload: agentdomain.RAGAnswerPayloadV2{
			RAGAnswerPayload: validRAGAnswerPayloadBase(),
			RelatedTopics: []agentdomain.RelatedTopic{{
				TopicID: foundation.ID("82000000-0000-4000-8000-000000000099"),
				Name:    "Deployment",
				CitationIDs: []string{
					"citation-1",
				},
			}},
			FollowUpQuestions: []string{"What changed in deployment?", "Which environment is affected?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func validClarificationDocument(t *testing.T) []byte {
	t.Helper()
	document, err := json.Marshal(conversationdomain.ClarificationResult{
		ResultType: agentdomain.ResultTypeClarification, SchemaID: conversationdomain.ClarificationSchemaID,
		SchemaVersion: conversationdomain.ClarificationSchemaVersionV1, ModelRunRef: testModelRunID,
		Payload: conversationdomain.ClarificationPayload{
			Reason: "the environment is ambiguous", Question: "Which environment should be used?",
			SuggestedScopes: []string{"production", "development"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func validRAGAnswerPayloadBase() agentdomain.RAGAnswerPayload {
	return agentdomain.RAGAnswerPayload{
		Conclusion: "Deployment behavior differs by environment.",
		Assertions: []agentdomain.Assertion{{
			ID: "assertion-1", Text: "Production deploys use a stricter gate.", Kind: agentdomain.AssertionFactual, CitationIDs: []string{"citation-1"},
		}},
		Citations: []agentdomain.Citation{{
			ID:              "citation-1",
			WorkspaceID:     testWorkspaceID,
			IndexVersionID:  testIndexID,
			ChunkID:         testChunkID,
			SourceVersionID: testSourceID,
			SourceSpanID:    testSpanID,
		}},
		ConflictPositions: []agentdomain.ConflictPosition{},
		ConflictSummary:   "",
	}
}

func withUnknownRootField(raw []byte) []byte {
	return append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
}

func withDuplicateResultType(raw []byte, resultType string) []byte {
	needle := `"result_type":"` + resultType + `"`
	return []byte(strings.Replace(string(raw), needle, needle+","+needle, 1))
}

func withTrailingDocument(raw []byte) []byte {
	return append(append([]byte(nil), raw...), []byte(" {}")...)
}

func withSchemaVersion(raw []byte, from, to string) []byte {
	return []byte(strings.Replace(string(raw), `"schema_version":"`+from+`"`, `"schema_version":"`+to+`"`, 1))
}
