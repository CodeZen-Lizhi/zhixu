package postgres

import (
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
)

func TestGenerationRuntimeMatchesKind(t *testing.T) {
	t.Parallel()

	version := organizingworkflow.GenerationRuntimeVersion
	outline := agentdomain.ModelRun{
		Prompt:        agentdomain.PromptRef{ID: organizingworkflow.OutlinePromptID, Version: version},
		Schema:        agentdomain.SchemaRef{ID: organizingworkflow.OutlineSchemaID, Version: version},
		ReducedSchema: agentdomain.SchemaRef{ID: organizingworkflow.OutlineReducedSchemaID, Version: version},
	}
	document := agentdomain.ModelRun{
		Prompt:        agentdomain.PromptRef{ID: organizingworkflow.DocumentPromptID, Version: version},
		Schema:        agentdomain.SchemaRef{ID: organizingworkflow.DocumentSchemaID, Version: version},
		ReducedSchema: agentdomain.SchemaRef{ID: organizingworkflow.DocumentReducedSchemaID, Version: version},
	}

	tests := []struct {
		name string
		run  agentdomain.ModelRun
		kind organizingworkflow.GenerationKind
		want bool
	}{
		{name: "outline", run: outline, kind: organizingworkflow.GenerationOutline, want: true},
		{name: "document", run: document, kind: organizingworkflow.GenerationDocument, want: true},
		{name: "cross kind", run: outline, kind: organizingworkflow.GenerationDocument},
		{name: "unknown kind", run: outline, kind: organizingworkflow.GenerationKind("OTHER")},
		{name: "wrong prompt", run: withGenerationPrompt(outline, organizingworkflow.DocumentPromptID, version), kind: organizingworkflow.GenerationOutline},
		{name: "wrong schema", run: withGenerationSchema(outline, organizingworkflow.DocumentSchemaID, version), kind: organizingworkflow.GenerationOutline},
		{name: "wrong reduced schema", run: withGenerationReducedSchema(outline, organizingworkflow.DocumentReducedSchemaID, version), kind: organizingworkflow.GenerationOutline},
		{name: "wrong version", run: withGenerationSchema(outline, organizingworkflow.OutlineSchemaID, "v2"), kind: organizingworkflow.GenerationOutline},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := generationRuntimeMatches(test.run, test.kind); got != test.want {
				t.Fatalf("generationRuntimeMatches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestGenerationResultMatchesKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		kind       organizingworkflow.GenerationKind
		resultType string
		want       bool
	}{
		{name: "outline", kind: organizingworkflow.GenerationOutline, resultType: agentdomain.ResultTypeOrganizingOutline, want: true},
		{name: "document", kind: organizingworkflow.GenerationDocument, resultType: agentdomain.ResultTypeOrganizingDocument, want: true},
		{name: "outline cannot become document", kind: organizingworkflow.GenerationOutline, resultType: agentdomain.ResultTypeOrganizingDocument},
		{name: "document cannot become outline", kind: organizingworkflow.GenerationDocument, resultType: agentdomain.ResultTypeOrganizingOutline},
		{name: "unknown kind", kind: organizingworkflow.GenerationKind("OTHER"), resultType: agentdomain.ResultTypeOrganizingOutline},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := generationResultMatches(test.kind, test.resultType); got != test.want {
				t.Fatalf("generationResultMatches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestGenerationCallMatchesRuntime(t *testing.T) {
	t.Parallel()

	run := agentdomain.ModelRun{
		Model:         agentdomain.ModelRef{AdapterName: "test", AdapterVersion: "v1", ModelID: "model", ModelVersion: "v1"},
		Profile:       agentdomain.ModelProfileRef{ID: "default", Version: "v1"},
		Prompt:        agentdomain.PromptRef{ID: organizingworkflow.OutlinePromptID, Version: organizingworkflow.GenerationRuntimeVersion},
		Schema:        agentdomain.SchemaRef{ID: organizingworkflow.OutlineSchemaID, Version: organizingworkflow.GenerationRuntimeVersion},
		ReducedSchema: agentdomain.SchemaRef{ID: organizingworkflow.OutlineReducedSchemaID, Version: organizingworkflow.GenerationRuntimeVersion},
	}
	primary := agentdomain.ModelCall{Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema, Phase: agentdomain.ModelCallInitial}
	reduced := primary
	reduced.Phase = agentdomain.ModelCallReduced
	reduced.Schema = run.ReducedSchema

	tests := []struct {
		name string
		call agentdomain.ModelCall
		want bool
	}{
		{name: "initial primary", call: primary, want: true},
		{name: "repair primary", call: withGenerationCallPhase(primary, agentdomain.ModelCallRepair), want: true},
		{name: "reduced fallback", call: reduced, want: true},
		{name: "wrong model", call: withGenerationCallModel(primary, agentdomain.ModelRef{AdapterName: "other", AdapterVersion: "v1", ModelID: "model", ModelVersion: "v1"})},
		{name: "wrong profile", call: withGenerationCallProfile(primary, agentdomain.ModelProfileRef{ID: "other", Version: "v1"})},
		{name: "wrong prompt", call: withGenerationCallPrompt(primary, agentdomain.PromptRef{ID: organizingworkflow.DocumentPromptID, Version: organizingworkflow.GenerationRuntimeVersion})},
		{name: "initial reduced schema", call: withGenerationCallSchema(primary, run.ReducedSchema)},
		{name: "reduced primary schema", call: withGenerationCallSchema(reduced, run.Schema)},
		{name: "plan phase", call: withGenerationCallPhase(primary, agentdomain.ModelCallPlan)},
		{name: "review phase", call: withGenerationCallPhase(primary, agentdomain.ModelCallReview)},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := generationCallMatchesRuntime(test.call, run); got != test.want {
				t.Fatalf("generationCallMatchesRuntime() = %v, want %v", got, test.want)
			}
		})
	}
}

func withGenerationPrompt(run agentdomain.ModelRun, id, version string) agentdomain.ModelRun {
	run.Prompt = agentdomain.PromptRef{ID: id, Version: version}
	return run
}

func withGenerationSchema(run agentdomain.ModelRun, id, version string) agentdomain.ModelRun {
	run.Schema = agentdomain.SchemaRef{ID: id, Version: version}
	return run
}

func withGenerationReducedSchema(run agentdomain.ModelRun, id, version string) agentdomain.ModelRun {
	run.ReducedSchema = agentdomain.SchemaRef{ID: id, Version: version}
	return run
}

func withGenerationCallPhase(call agentdomain.ModelCall, phase agentdomain.ModelCallPhase) agentdomain.ModelCall {
	call.Phase = phase
	return call
}

func withGenerationCallModel(call agentdomain.ModelCall, model agentdomain.ModelRef) agentdomain.ModelCall {
	call.Model = model
	return call
}

func withGenerationCallProfile(call agentdomain.ModelCall, profile agentdomain.ModelProfileRef) agentdomain.ModelCall {
	call.Profile = profile
	return call
}

func withGenerationCallPrompt(call agentdomain.ModelCall, prompt agentdomain.PromptRef) agentdomain.ModelCall {
	call.Prompt = prompt
	return call
}

func withGenerationCallSchema(call agentdomain.ModelCall, schema agentdomain.SchemaRef) agentdomain.ModelCall {
	call.Schema = schema
	return call
}
