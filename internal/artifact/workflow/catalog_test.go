package workflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
)

func TestRuntimeCatalogPublishesStrictPrimaryAndGapOnlySchemas(t *testing.T) {
	catalog := newArtifactRuntimeCatalog(t)
	snapshot, err := catalog.Snapshot(PromptRef(), SchemaRef(), ReducedSchemaRef(), artifactTestProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	var primary, reduced map[string]any
	if err := json.Unmarshal(snapshot.Schema.JSONSchema, &primary); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(snapshot.ReducedSchema.JSONSchema, &reduced); err != nil {
		t.Fatal(err)
	}
	primaryPayload := schemaPath(primary, "payload")
	reducedPayload := schemaPath(reduced, "payload")
	if primary["additionalProperties"] != false || reduced["additionalProperties"] != false ||
		len(primaryPayload["oneOf"].([]any)) != 3 || len(reducedPayload["oneOf"].([]any)) != 1 ||
		len(snapshot.ReducedSchema.JSONSchema) >= len(snapshot.Schema.JSONSchema) {
		t.Fatalf("primary = %s\nreduced = %s", snapshot.Schema.JSONSchema, snapshot.ReducedSchema.JSONSchema)
	}
	trusted := snapshot.Prompt.System + "\n" + snapshot.Prompt.InitialInstruction + "\n" + snapshot.Prompt.ReducedInstruction
	for _, required := range []string{"server-provided evidence labels", "Never invent", "Do not use outside knowledge", "safe GAP"} {
		if !strings.Contains(trusted, required) {
			t.Fatalf("trusted prompt lacks %q: %s", required, trusted)
		}
	}
}

func TestDecodeSectionGenerationOutputMapsCoveredPartialAndGap(t *testing.T) {
	tests := []SectionGenerationPayload{
		{CoverageStatus: artifactdomain.CoverageCovered, Content: "supported", CitationLabels: []string{"citation-001"}, Gaps: []GapOutput{}},
		{CoverageStatus: artifactdomain.CoveragePartial, Content: "partly supported", CitationLabels: []string{"citation-001"}, Gaps: []GapOutput{{Code: "missing-a", Description: "missing detail"}}},
		{CoverageStatus: artifactdomain.CoverageGap, Content: "", CitationLabels: []string{}, Gaps: []GapOutput{{Code: "missing-all", Description: "no approved evidence"}}},
	}
	for _, payload := range tests {
		payload := payload
		t.Run(string(payload.CoverageStatus), func(t *testing.T) {
			raw := sectionGenerationDocument(t, payload)
			decoded, err := DecodeSectionGenerationOutput(raw)
			if err != nil || decoded.Payload.CoverageStatus != payload.CoverageStatus {
				t.Fatalf("decoded = %#v, err = %v", decoded, err)
			}
		})
	}
}

func TestSectionGenerationDecoderRejectsCallerControlledFactsAndMalformedJSON(t *testing.T) {
	valid := sectionGenerationDocument(t, SectionGenerationPayload{
		CoverageStatus: artifactdomain.CoverageCovered, Content: "supported",
		CitationLabels: []string{"citation-001"}, Gaps: []GapOutput{},
	})
	tests := map[string][]byte{
		"artifact identity": append(valid[:len(valid)-1], []byte(`,"artifact_id":"90000000-0000-4000-8000-000000000002"}`)...),
		"title":             append(valid[:len(valid)-1], []byte(`,"title":"caller title"}`)...),
		"metadata":          []byte(strings.Replace(string(valid), `"content":"supported"`, `"metadata":{"model_version":"caller"},"content":"supported"`, 1)),
		"citation tuple":    []byte(strings.Replace(string(valid), `"citation_labels":["citation-001"]`, `"citation_labels":["citation-001"],"citations":[{"source_version_id":"x"}]`, 1)),
		"provider":          []byte(strings.Replace(string(valid), `"content":"supported"`, `"provider":"caller","content":"supported"`, 1)),
		"duplicate":         append(valid[:len(valid)-1], []byte(`,"result_type":"artifact_section"}`)...),
		"trailing":          append(append([]byte(nil), valid...), []byte(` {}`)...),
		"null":              []byte(`{"result_type":"artifact_section","schema_id":"artifact.section-generation","schema_version":"v1","payload":null}`),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSectionGenerationOutput(raw); err == nil {
				t.Fatalf("unsafe document accepted: %s", raw)
			}
		})
	}
}

func TestReducedSchemaAcceptsOnlySafeGap(t *testing.T) {
	catalog := newArtifactRuntimeCatalog(t)
	snapshot, err := catalog.Snapshot(PromptRef(), ReducedSchemaRef(), ReducedSchemaRef(), artifactTestProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	gap := sectionGenerationDocument(t, SectionGenerationPayload{
		CoverageStatus: artifactdomain.CoverageGap, Content: "", CitationLabels: []string{},
		Gaps: []GapOutput{{Code: "missing", Description: "no eligible evidence"}},
	})
	if _, err := snapshot.Schema.Decode(gap); err != nil {
		t.Fatalf("safe GAP rejected: %v", err)
	}
	covered := sectionGenerationDocument(t, SectionGenerationPayload{
		CoverageStatus: artifactdomain.CoverageCovered, Content: "unsupported", CitationLabels: []string{"citation-001"}, Gaps: []GapOutput{},
	})
	if _, err := snapshot.Schema.Decode(covered); err == nil {
		t.Fatal("reduced schema accepted COVERED output")
	}
}

func TestProposalUsesOnlyServerTitleCitationAndRuntimeMetadata(t *testing.T) {
	citation := artifactTestCitation("server-citation")
	evidence := []eligibleEvidence{{Label: "citation-001", Citation: citation, Excerpt: "server excerpt"}}
	output := SectionGenerationEnvelope{
		ResultType: agentdomain.ResultTypeArtifactSection, SchemaID: SchemaID, SchemaVersion: RuntimeVersion,
		Payload: SectionGenerationPayload{
			CoverageStatus: artifactdomain.CoverageCovered, Content: "supported",
			CitationLabels: []string{"citation-001"}, Gaps: []GapOutput{},
		},
	}
	runtime := agentapplication.FrozenRuntimeRefs{
		Profile: artifactTestProfileRef(), Prompt: PromptRef(), Schema: SchemaRef(), Model: artifactTestModelRef(),
	}
	proposal, err := proposalFromOutput(artifactdomain.OutlineSection{Key: "first", Title: "Server title"}, evidence, output, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Title != "Server title" || len(proposal.Citations) != 1 || proposal.Citations[0] != citation ||
		proposal.Metadata.ModelVersion != runtime.Model.ModelVersion || proposal.Metadata.PromptVersion != RuntimeVersion ||
		proposal.Metadata.SchemaVersion != RuntimeVersion {
		t.Fatalf("proposal = %#v", proposal)
	}
	output.Payload.CitationLabels = []string{"citation-002"}
	if _, err := proposalFromOutput(artifactdomain.OutlineSection{Key: "first", Title: "Server title"}, evidence, output, runtime); testWorkflowErrorCode(err) != ErrorCodeOutputInvalid {
		t.Fatalf("unknown label err = %v", err)
	}
	output.Payload.CitationLabels = []string{"citation-001"}
	runtime.Schema = agentdomain.SchemaRef{ID: "artifact.unexpected", Version: RuntimeVersion}
	if _, err := proposalFromOutput(artifactdomain.OutlineSection{Key: "first", Title: "Server title"}, evidence, output, runtime); testWorkflowErrorCode(err) != ErrorCodeOutputInvalid {
		t.Fatalf("runtime schema drift err = %v", err)
	}
}

func newArtifactRuntimeCatalog(t *testing.T) *agentapplication.RuntimeCatalog {
	t.Helper()
	catalog := agentapplication.NewRuntimeCatalog()
	if err := RegisterRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterProfile(agentapplication.ModelProfile{
		Ref: artifactTestProfileRef(), Model: artifactTestModelRef(), Timeout: time.Second, MaxOutputTokens: 1024,
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func artifactTestProfileRef() agentdomain.ModelProfileRef {
	return agentdomain.ModelProfileRef{ID: "artifact.test", Version: "v1"}
}

func artifactTestModelRef() agentdomain.ModelRef {
	return agentdomain.ModelRef{AdapterName: "test-chat", AdapterVersion: "v1", ModelID: "artifact-test", ModelVersion: "v1"}
}

func sectionGenerationDocument(t *testing.T, payload SectionGenerationPayload) []byte {
	t.Helper()
	raw, err := json.Marshal(SectionGenerationEnvelope{
		ResultType: agentdomain.ResultTypeArtifactSection, SchemaID: SchemaID, SchemaVersion: RuntimeVersion, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func schemaPath(document map[string]any, path ...string) map[string]any {
	current := document
	for _, component := range path {
		current = current["properties"].(map[string]any)[component].(map[string]any)
	}
	return current
}

func artifactTestCitation(id string) agentdomain.Citation {
	return agentdomain.Citation{
		ID: id, WorkspaceID: artifactWorkflowTestID(1), IndexVersionID: artifactWorkflowTestID(30),
		ChunkID: artifactWorkflowTestID(31), SourceVersionID: artifactWorkflowTestID(32), SourceSpanID: artifactWorkflowTestID(33),
	}
}
