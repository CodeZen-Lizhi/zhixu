package workflow

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolagent "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/agent"
)

func TestRAGMetadataSchemaUsesProviderCompatibleLexicalReferenceBounds(t *testing.T) {
	for _, test := range []struct {
		prefix  string
		valid   []string
		invalid []string
	}{
		{prefix: "E", valid: []string{"E1", "E99", "E999"}, invalid: []string{"E0", "E01", "E1000"}},
		{prefix: "C", valid: []string{"C1", "C999"}, invalid: []string{"C0", "C1000"}},
		{prefix: "T", valid: []string{"T1", "T999"}, invalid: []string{"T0", "T1000"}},
	} {
		pattern := metadataReferenceSchema(test.prefix)["pattern"].(string)
		compiled := regexp.MustCompile(pattern)
		for _, value := range test.valid {
			if !compiled.MatchString(value) {
				t.Fatalf("pattern %s rejected %s", pattern, value)
			}
		}
		for _, value := range test.invalid {
			if compiled.MatchString(value) {
				t.Fatalf("pattern %s accepted %s", pattern, value)
			}
		}
	}
}

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
			ref: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2},
			expectedPayloadFields: []string{
				"assertions", "conflict_positions", "conflict_summary", "related_topic_refs", "follow_up_questions",
			},
		},
		{
			ref: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2},
			expectedPayloadFields: []string{
				"reason_code", "summary", "retrieval_scope", "missing_requirements", "suggested_actions",
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
			prompt := DefaultPromptRef()
			reduced := test.ref
			if test.ref == (agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2}) ||
				test.ref == (agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2}) {
				prompt = RAGAnswerMetadataPromptRef()
				if test.ref.ID == agentdomain.RAGAnswerMetadataSchemaID {
					reduced = agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2}
				}
			}
			snapshot, err := catalog.Snapshot(prompt, test.ref, reduced, DefaultProfileRef())
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
			variants := []map[string]any{payload}
			if rawVariants, ok := payload["anyOf"].([]any); ok {
				variants = make([]map[string]any, len(rawVariants))
				for index, raw := range rawVariants {
					variants[index] = raw.(map[string]any)
				}
			}
			for _, variant := range variants {
				assertStrictPayloadFields(t, test.ref, variant, test.expectedPayloadFields)
			}
			if test.ref == (agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2}) ||
				test.ref == (agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2}) {
				assertStrictMetadataV2Root(t, document)
			}
		})
	}
}

func TestRuntimeCatalogPublishesIdentitylessWorkspaceAnalysisPlanSchema(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	ref := agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	snapshot, err := catalog.Snapshot(WorkspaceAnalysisPlanPromptRef(), ref, ref, DefaultProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Prompt.Ref != WorkspaceAnalysisPlanPromptRef() || snapshot.Schema.Ref != ref || snapshot.ReducedSchema.Ref != ref ||
		bytes.Contains(snapshot.Schema.JSONSchema, []byte("model_run_ref")) || bytes.Contains(snapshot.Schema.JSONSchema, []byte("workspace_id")) {
		t.Fatalf("workspace analysis plan snapshot exposes identity: prompt=%+v schema=%s", snapshot.Prompt.Ref, snapshot.Schema.JSONSchema)
	}
	providerDocument := json.RawMessage(`{"i":"inspect workspace","r":["workspace policy"],"d":"","q":"","s":[]}`)
	decoded, err := snapshot.Schema.Decode(providerDocument)
	if err != nil || !bytes.Equal(decoded, providerDocument) {
		t.Fatalf("provider decode=%s err=%v", decoded, err)
	}
	if _, err := snapshot.Schema.Decode(json.RawMessage(`{"model_run_ref":"83000000-0000-4000-8000-000000000001"}`)); err == nil {
		t.Fatal("workspace analysis plan schema accepted a server-owned identity")
	}
}

func TestRuntimeCatalogPublishesIdentitylessWorkspaceAnalysisCandidateSchema(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	ref := agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "1"}
	snapshot, err := catalog.Snapshot(WorkspaceAnalysisSynthesisPromptRef(), ref, ref, DefaultProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Prompt.Ref != WorkspaceAnalysisSynthesisPromptRef() || snapshot.Schema.Ref != ref || snapshot.ReducedSchema.Ref != ref ||
		bytes.Contains(snapshot.Schema.JSONSchema, []byte("model_run_ref")) || bytes.Contains(snapshot.Schema.JSONSchema, []byte("workspace_id")) {
		t.Fatalf("workspace analysis candidate snapshot exposes identity: prompt=%+v schema=%s", snapshot.Prompt.Ref, snapshot.Schema.JSONSchema)
	}
	providerDocument := json.RawMessage(`{"result_type":"workspace_analysis_candidate","schema_id":"agent.workspace-analysis-candidate","schema_version":"1","payload":{"answer_markdown":"Grounded answer [E1].","citation_refs":["E1"],"proposal_suggestion":null}}`)
	decoded, err := snapshot.Schema.Decode(providerDocument)
	if err != nil || !bytes.Equal(decoded, providerDocument) {
		t.Fatalf("provider decode=%s err=%v", decoded, err)
	}
	if _, err := snapshot.Schema.Decode(json.RawMessage(`{"model_run_ref":"83000000-0000-4000-8000-000000000001"}`)); err == nil {
		t.Fatal("workspace analysis candidate schema accepted a server-owned identity")
	}
}

func TestRuntimeCatalogDoesNotExposeLegacyRAGMetadataOrMixedVersions(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	v1Prompt := agentdomain.PromptRef{ID: "rag-answer-metadata", Version: agentdomain.OutputSchemaVersionV1}
	v2Prompt := RAGAnswerMetadataPromptRef()
	v1Schema := agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	v2Schema := agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2}
	for name, refs := range map[string]struct {
		prompt agentdomain.PromptRef
		schema agentdomain.SchemaRef
	}{
		"legacy prompt and schema": {prompt: v1Prompt, schema: v1Schema},
		"v1 prompt with v2 schema": {prompt: v1Prompt, schema: v2Schema},
		"v2 prompt with v1 schema": {prompt: v2Prompt, schema: v1Schema},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := catalog.Snapshot(refs.prompt, refs.schema, refs.schema, DefaultProfileRef()); err == nil {
				t.Fatal("production catalog exposed legacy or mixed metadata refs")
			}
		})
	}
}

func assertStrictMetadataV2Root(t *testing.T, document map[string]any) {
	t.Helper()
	properties := document["properties"].(map[string]any)
	if _, exists := properties["model_run_ref"]; exists {
		t.Fatal("metadata v2 root exposes model_run_ref")
	}
	expected := []string{"payload", "result_type", "schema_id", "schema_version"}
	propertyNames := make([]string, 0, len(properties))
	for name := range properties {
		propertyNames = append(propertyNames, name)
	}
	required := make([]string, 0, len(document["required"].([]any)))
	for _, name := range document["required"].([]any) {
		required = append(required, name.(string))
	}
	slices.Sort(propertyNames)
	slices.Sort(required)
	if !slices.Equal(propertyNames, expected) || !slices.Equal(required, expected) {
		t.Fatalf("metadata v2 root reused generic task schema: %+v", document)
	}
}

func TestRuntimeCatalogQueryPlanSchemaKeepsProviderShapeFlatAndBounded(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	ref := agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV2}
	snapshot, err := catalog.Snapshot(QueryPlanProviderPromptRef(), ref, ref, DefaultProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(snapshot.Schema.JSONSchema, &document); err != nil {
		t.Fatal(err)
	}
	if _, exists := document["anyOf"]; exists {
		t.Fatal("query plan provider schema must not use anyOf; the strict domain decoder owns branch invariants")
	}
	if _, exists := document["oneOf"]; exists {
		t.Fatal("query plan provider schema must not use oneOf; the strict domain decoder owns branch invariants")
	}
	fields := []string{"i", "r", "d", "q", "s"}
	assertStrictPayloadFields(t, ref, document, fields)
	properties := document["properties"].(map[string]any)
	if properties["r"].(map[string]any)["maxItems"] != float64(3) ||
		properties["s"].(map[string]any)["maxItems"] != float64(10) {
		t.Fatalf("query plan provider bounds drifted: %+v", properties)
	}
	for _, field := range []string{"r", "s"} {
		if _, exists := properties[field].(map[string]any)["uniqueItems"]; exists {
			t.Fatalf("query plan provider schema uses unsupported uniqueItems for %s", field)
		}
	}
	for _, forbidden := range []string{"result_type", "schema_id", "schema_version", "model_run_ref", "payload", "requires_clarification"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("query plan provider schema exposes %s", forbidden)
		}
	}
}

func TestCurrentRAGProviderSchemasDoNotUseUnsupportedUniqueItems(t *testing.T) {
	tests := []struct {
		name   string
		schema func() ([]byte, error)
	}{
		{name: "query plan", schema: queryPlanProviderSchemaV2},
		{name: "metadata", schema: func() ([]byte, error) { return ragMetadataTaskSchemaV2(agentdomain.ResultTypeRAGAnswerMetadata) }},
		{name: "metadata refusal", schema: func() ([]byte, error) { return ragMetadataRefusalTaskSchemaV2(agentdomain.ResultTypeRefusal) }},
		{name: "faithfulness", schema: func() ([]byte, error) {
			return taskSchema(
				agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1},
				agentdomain.ResultTypeFaithfulnessReview,
			)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := test.schema()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(`"uniqueItems"`)) {
				t.Fatalf("provider schema contains unsupported uniqueItems: %s", raw)
			}
		})
	}
}

func assertStrictPayloadFields(t *testing.T, ref agentdomain.SchemaRef, payload map[string]any, expectedFields []string) {
	t.Helper()
	if payload["additionalProperties"] != false {
		t.Fatalf("schema=%s/%s payload is not strict", ref.ID, ref.Version)
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
	expected := append([]string(nil), expectedFields...)
	slices.Sort(propertyNames)
	slices.Sort(required)
	slices.Sort(expected)
	if !slices.Equal(propertyNames, expected) || !slices.Equal(required, expected) {
		t.Fatalf("schema=%s/%s payload=%+v", ref.ID, ref.Version, payload)
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
			name:        "rag-answer-metadata-v2",
			ref:         agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2},
			raw:         validRAGAnswerMetadataV2Document(t),
			resultType:  agentdomain.ResultTypeRAGAnswerMetadata,
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
			prompt := DefaultPromptRef()
			reduced := test.ref
			if test.ref == (agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2}) {
				prompt = RAGAnswerMetadataPromptRef()
				reduced = agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2}
			}
			snapshot, err := catalog.Snapshot(prompt, test.ref, reduced, DefaultProfileRef())
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

func validRAGAnswerMetadataV2Document(t *testing.T) []byte {
	t.Helper()
	document, err := json.Marshal(agentdomain.RAGAnswerMetadataResultV2{
		ResultType: agentdomain.ResultTypeRAGAnswerMetadata, SchemaID: agentdomain.RAGAnswerMetadataSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV2,
		Payload: agentdomain.RAGAnswerMetadataPayloadV2{
			Assertions: []agentdomain.RAGAnswerMetadataAssertionV2{{
				ID: "assertion-1", Text: "Production deploys use a stricter gate.", Kind: agentdomain.AssertionFactual,
				EvidenceRefs: []string{"E1"},
			}},
			ConflictPositions: []agentdomain.RAGAnswerMetadataConflictPositionV2{}, ConflictSummary: "",
			RelatedTopicRefs:  []string{"T1"},
			FollowUpQuestions: []string{"What changed in deployment?"},
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
