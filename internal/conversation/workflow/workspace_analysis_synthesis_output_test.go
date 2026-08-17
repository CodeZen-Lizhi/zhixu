package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisSynthesisOutputCanonicalRoundTrip(t *testing.T) {
	output := workspaceAnalysisSynthesisOutputFixture()
	document, err := EncodeWorkspaceAnalysisSynthesisOutput(output)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := DecodeWorkspaceAnalysisSynthesisOutput(document)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded != output {
		t.Fatalf("decoded = %#v", decoded)
	}
	reencoded, err := EncodeWorkspaceAnalysisSynthesisOutput(decoded)
	if err != nil || string(reencoded) != string(document) {
		t.Fatalf("reencode = %s, %v", reencoded, err)
	}
	for _, text := range []string{output.String(), output.GoString(), output.LogValue().String()} {
		for _, secret := range []string{string(output.CandidateID), output.CandidateHash, string(output.DraftSessionID)} {
			if strings.Contains(text, secret) {
				t.Fatalf("safe projection leaked %q: %s", secret, text)
			}
		}
	}
}

func TestWorkspaceAnalysisSynthesisOutputRejectsMalformedDocuments(t *testing.T) {
	valid, err := EncodeWorkspaceAnalysisSynthesisOutput(workspaceAnalysisSynthesisOutputFixture())
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"unknown":    strings.TrimSuffix(string(valid), "}") + `,"extra":true}`,
		"duplicate":  strings.Replace(string(valid), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"null":       strings.Replace(string(valid), `"candidate_hash":"`+strings.Repeat("a", 64)+`"`, `"candidate_hash":null`, 1),
		"trailing":   string(valid) + `{}`,
		"uppercase":  strings.Replace(string(valid), strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"generation": strings.Replace(string(valid), `"draft_generation":1`, `"draft_generation":0`, 1),
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWorkspaceAnalysisSynthesisOutput(json.RawMessage(document)); err == nil {
				t.Fatalf("Decode accepted %s", document)
			}
		})
	}
}

func TestWorkspaceAnalysisSynthesisOutputRejectsIdentityReuse(t *testing.T) {
	output := workspaceAnalysisSynthesisOutputFixture()
	output.DraftSessionID = output.CandidateID
	if _, err := EncodeWorkspaceAnalysisSynthesisOutput(output); err == nil {
		t.Fatal("Encode accepted a reused identity")
	}
}

func workspaceAnalysisSynthesisOutputFixture() WorkspaceAnalysisSynthesisOutput {
	return WorkspaceAnalysisSynthesisOutput{
		SchemaVersion: WorkspaceAnalysisOutputSchemaVersion,
		CandidateID:   workspaceAnalysisSynthesisOutputID(1), CandidateHash: strings.Repeat("a", 64),
		SynthesisModelRunID:  workspaceAnalysisSynthesisOutputID(2),
		SynthesisModelCallID: workspaceAnalysisSynthesisOutputID(3),
		DraftSessionID:       workspaceAnalysisSynthesisOutputID(4), DraftGeneration: 1,
	}
}

func workspaceAnalysisSynthesisOutputID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("8a000000-0000-4000-8000-%012d", value))
}
