package workflow

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisPublicationOutputRoundTripsTerminalMatrix(t *testing.T) {
	modelRunID := foundation.ID("96000000-0000-4000-8000-000000000003")
	tests := []WorkspaceAnalysisPublicationOutput{
		validWorkspaceAnalysisPublicationOutput(&modelRunID),
		{
			SchemaVersion: WorkspaceAnalysisOutputSchemaVersion,
			AnswerID:      "96000000-0000-4000-8000-000000000001", PublicationStatus: conversationdomain.AnswerPublicationRefused,
			ResultType: conversationdomain.AnswerResultWorkspaceAnalysisRefusal, ModelRunID: nil,
			ResultHash: strings.Repeat("b", 64), ProofID: "96000000-0000-4000-8000-000000000002",
		},
		{
			SchemaVersion: WorkspaceAnalysisOutputSchemaVersion,
			AnswerID:      "96000000-0000-4000-8000-000000000001", PublicationStatus: conversationdomain.WorkspaceAnalysisPublicationFailed,
			ResultType: conversationdomain.AnswerResultWorkspaceAnalysisTermination, ModelRunID: nil,
			ResultHash: strings.Repeat("c", 64), ProofID: "96000000-0000-4000-8000-000000000002",
		},
	}
	for _, expected := range tests {
		encoded, err := EncodeWorkspaceAnalysisPublicationOutput(expected)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := DecodeWorkspaceAnalysisPublicationOutput(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if string(actual.AnswerID) != string(expected.AnswerID) || actual.PublicationStatus != expected.PublicationStatus ||
			actual.ResultType != expected.ResultType || actual.ResultHash != expected.ResultHash || actual.ProofID != expected.ProofID ||
			(actual.ModelRunID == nil) != (expected.ModelRunID == nil) ||
			(actual.ModelRunID != nil && *actual.ModelRunID != *expected.ModelRunID) {
			t.Fatalf("decoded=%s expected=%s", actual, expected)
		}
	}
}

func TestWorkspaceAnalysisPublicationOutputRejectsUntrustedDocuments(t *testing.T) {
	modelRunID := foundation.ID("96000000-0000-4000-8000-000000000003")
	valid := validWorkspaceAnalysisPublicationOutput(&modelRunID)
	encoded, err := EncodeWorkspaceAnalysisPublicationOutput(valid)
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		`null`,
		string(encoded) + ` {}`,
		`{"schema_version":1,"answer_id":"96000000-0000-4000-8000-000000000001","publication_status":"failed","result_type":"workspace_analysis_termination","result_hash":"` + strings.Repeat("a", 64) + `","proof_id":"96000000-0000-4000-8000-000000000003"}`,
		strings.Replace(string(encoded), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		strings.Replace(string(encoded), `"result_hash":"`+strings.Repeat("a", 64)+`"`, `"result_hash":null`, 1),
		strings.Replace(string(encoded), `"proof_id":"96000000-0000-4000-8000-000000000002"`, `"proof_id":"96000000-0000-4000-8000-000000000001"`, 1),
		strings.Replace(string(encoded), `"model_run_id":"96000000-0000-4000-8000-000000000003"`, `"model_run_id":null`, 1),
		strings.Replace(string(encoded), `"result_type":"workspace_analysis"`, `"result_type":"workspace_analysis_refusal"`, 1),
		strings.Replace(string(encoded), `}`, `,"unknown":true}`, 1),
	}
	for _, raw := range cases {
		if _, err := DecodeWorkspaceAnalysisPublicationOutput([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestWorkspaceAnalysisPublicationOutputFormattingDoesNotLeakIdentities(t *testing.T) {
	modelRunID := foundation.ID("96000000-0000-4000-8000-000000000003")
	output := validWorkspaceAnalysisPublicationOutput(&modelRunID)
	formatted := []string{output.String(), fmt.Sprintf("%#v", output)}
	record := slog.NewRecord(time.Unix(1, 0).UTC(), slog.LevelInfo, "publication", 0)
	record.AddAttrs(slog.Any("output", output))
	record.Attrs(func(attribute slog.Attr) bool {
		formatted = append(formatted, attribute.Value.String())
		return true
	})
	for _, value := range formatted {
		for _, secret := range []string{string(output.AnswerID), string(*output.ModelRunID), string(output.ProofID), output.ResultHash} {
			if strings.Contains(value, secret) {
				t.Fatalf("format leaked identity: %s", value)
			}
		}
	}
	encoded, err := json.Marshal(output)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("wire encoding failed: %v", err)
	}
}

func validWorkspaceAnalysisPublicationOutput(modelRunID *foundation.ID) WorkspaceAnalysisPublicationOutput {
	return WorkspaceAnalysisPublicationOutput{
		SchemaVersion: WorkspaceAnalysisOutputSchemaVersion,
		AnswerID:      "96000000-0000-4000-8000-000000000001", PublicationStatus: conversationdomain.AnswerPublicationCompleted,
		ResultType: conversationdomain.AnswerResultWorkspaceAnalysis, ModelRunID: modelRunID,
		ResultHash: strings.Repeat("a", 64), ProofID: "96000000-0000-4000-8000-000000000002",
	}
}
