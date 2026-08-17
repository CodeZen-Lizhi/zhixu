package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkspaceAnalysisCandidateProviderComposeInjectsOnlyServerModelRun(t *testing.T) {
	raw := []byte(`{"result_type":"workspace_analysis_candidate","schema_id":"agent.workspace-analysis-candidate","schema_version":"1","payload":{"answer_markdown":"Grounded answer [E1].","citation_refs":["E1","E2"],"proposal_suggestion":{"summary":"Update the architecture note","citation_refs":["E2"]}}}`)
	provider, err := DecodeWorkspaceAnalysisCandidateProvider(raw, DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("DecodeWorkspaceAnalysisCandidateProvider: %v", err)
	}
	modelRunID := workspaceAnalysisPersistenceID(88)
	composed, err := provider.Compose(modelRunID)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if composed.ModelRunRef != modelRunID || composed.Payload.AnswerMarkdown != provider.Payload.AnswerMarkdown ||
		strings.Contains(provider.String(), "Grounded answer") || strings.Contains(provider.GoString(), "Update the architecture") {
		t.Fatalf("provider=%s composed=%+v", provider, composed)
	}
	document, err := json.Marshal(composed)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeWorkspaceAnalysisCandidate(document, DefaultDecodeLimits())
	if err != nil || decoded.ModelRunRef != modelRunID {
		t.Fatalf("persisted candidate err=%v decoded=%+v", err, decoded)
	}
}

func TestWorkspaceAnalysisCandidateProviderRejectsNonCanonicalDocuments(t *testing.T) {
	valid := `{"result_type":"workspace_analysis_candidate","schema_id":"agent.workspace-analysis-candidate","schema_version":"1","payload":{"answer_markdown":"Grounded answer [E1].","citation_refs":["E1"],"proposal_suggestion":null}}`
	tests := map[string]string{
		"unknown":   strings.Replace(valid, `"payload":`, `"unknown":1,"payload":`, 1),
		"duplicate": strings.Replace(valid, `"result_type":`, `"result_type":"workspace_analysis_candidate","result_type":`, 1),
		"null":      `null`,
		"trailing":  valid + `{}`,
		"identity":  strings.Replace(valid, `"payload":`, `"model_run_ref":"93000000-0000-4000-8000-000000000088","payload":`, 1),
		"bad ref":   strings.Replace(valid, `"E1"`, `"E4"`, 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWorkspaceAnalysisCandidateProvider([]byte(raw), DefaultDecodeLimits()); err == nil {
				t.Fatal("invalid provider document was accepted")
			}
		})
	}
}
