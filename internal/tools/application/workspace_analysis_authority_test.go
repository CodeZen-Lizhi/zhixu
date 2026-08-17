package application

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestValidateCitationV3AuthorityNeverSerializesOrLogsPrivateFacts(t *testing.T) {
	authority := ValidateCitationV3Authority{
		WorkspaceID: "authority-workspace-canary", WorkflowRunID: "authority-workflow-canary",
		AnalysisRunID: "authority-analysis-canary", CandidateID: "authority-candidate-canary",
		CandidateHash: "authority-candidate-hash-canary", EvidenceRefs: []string{"authority-evidence-canary"},
		SearchReceipt: domain.ResultReceipt{
			ID: "authority-search-receipt-canary", Output: json.RawMessage(`{"value":"authority-output-canary"}`),
			PrivateBinding: &domain.ResultReceiptPrivateBinding{
				Document: json.RawMessage(`{"value":"authority-binding-canary"}`),
			},
		},
		ReadSourceReceipts: []domain.ResultReceipt{{ID: "authority-read-receipt-canary"}},
	}
	values := []any{authority, &authority}
	canaries := []string{
		"authority-workspace-canary", "authority-workflow-canary", "authority-analysis-canary",
		"authority-candidate-canary", "authority-candidate-hash-canary", "authority-evidence-canary",
		"authority-search-receipt-canary", "authority-output-canary", "authority-binding-canary", "authority-read-receipt-canary",
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != `{}` {
			t.Fatalf("authority JSON must be empty: %s", encoded)
		}
		var logOutput bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
		logger.Info("citation authority", slog.Any("authority", value))
		outputs := []string{
			string(encoded), fmt.Sprintf("%v", value), fmt.Sprintf("%+v", value),
			fmt.Sprintf("%#v", value), logOutput.String(),
		}
		for _, output := range outputs {
			for _, canary := range canaries {
				if strings.Contains(output, canary) {
					t.Fatalf("authority leaked %q through %q", canary, output)
				}
			}
		}
	}
}

func TestWorkspaceAnalysisSynthesisEvidenceAuthorityNeverSerializesOrLogsFacts(t *testing.T) {
	authority := WorkspaceAnalysisSynthesisEvidenceAuthority{
		WorkspaceID: "synthesis-workspace-canary", WorkflowRunID: "synthesis-workflow-canary",
		AnalysisRunID: "synthesis-analysis-canary", EvidenceRefs: []string{"E1"},
		SearchReceipt: domain.ResultReceipt{
			ID: "synthesis-search-canary", Output: json.RawMessage(`{"value":"synthesis-search-output-canary"}`),
		},
		ReadSourceReceipts: []domain.ResultReceipt{{
			ID: "synthesis-read-canary", Output: json.RawMessage(`{"value":"synthesis-read-output-canary"}`),
		}},
		Evidence: []domain.ReadSourceV3ReceiptEvidence{{EvidenceRef: "E1", Excerpt: "synthesis-excerpt-canary"}},
	}
	canaries := []string{
		"synthesis-workspace-canary", "synthesis-workflow-canary", "synthesis-analysis-canary",
		"synthesis-search-canary", "synthesis-search-output-canary", "synthesis-read-canary",
		"synthesis-read-output-canary", "synthesis-excerpt-canary",
	}
	for _, value := range []any{authority, &authority} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != `{}` {
			t.Fatalf("synthesis authority JSON must be empty: %s", encoded)
		}
		var logOutput bytes.Buffer
		slog.New(slog.NewJSONHandler(&logOutput, nil)).Info("synthesis authority", slog.Any("authority", value))
		outputs := []string{
			string(encoded), fmt.Sprintf("%v", value), fmt.Sprintf("%+v", value),
			fmt.Sprintf("%#v", value), logOutput.String(),
		}
		for _, output := range outputs {
			for _, canary := range canaries {
				if strings.Contains(output, canary) {
					t.Fatalf("synthesis authority leaked %q through %q", canary, output)
				}
			}
		}
	}
}

func TestValidateCitationV3PublicationAuthorityNeverSerializesOrLogsFacts(t *testing.T) {
	authority := ValidateCitationV3PublicationAuthority{
		OperationID: "publication-operation-canary",
		Receipt: domain.ResultReceipt{
			ID:             "publication-receipt-canary",
			ToolCallID:     "publication-call-canary",
			OutputHash:     "publication-output-hash-canary",
			Output:         json.RawMessage(`{"value":"publication-output-canary"}`),
			PrivateBinding: &domain.ResultReceiptPrivateBinding{Document: json.RawMessage(`{"value":"publication-binding-canary"}`)},
		},
	}
	canaries := []string{
		"publication-operation-canary", "publication-receipt-canary", "publication-call-canary",
		"publication-output-hash-canary", "publication-output-canary", "publication-binding-canary",
	}
	for _, value := range []any{authority, &authority} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != `{}` {
			t.Fatalf("publication authority JSON must be empty: %s", encoded)
		}
		var logOutput bytes.Buffer
		slog.New(slog.NewJSONHandler(&logOutput, nil)).Info("publication authority", slog.Any("authority", value))
		outputs := []string{
			string(encoded), fmt.Sprintf("%v", value), fmt.Sprintf("%+v", value),
			fmt.Sprintf("%#v", value), logOutput.String(),
		}
		for _, output := range outputs {
			for _, canary := range canaries {
				if strings.Contains(output, canary) {
					t.Fatalf("publication authority leaked %q through %q", canary, output)
				}
			}
		}
	}
}

func TestSearchKnowledgeV2PublicationAuthorityNeverSerializesOrLogsFacts(t *testing.T) {
	authority := SearchKnowledgeV2PublicationAuthority{
		OperationID: "search-publication-operation-canary",
		Receipt: domain.ResultReceipt{
			ID:             "search-publication-receipt-canary",
			ToolCallID:     "search-publication-call-canary",
			OutputHash:     "search-publication-output-hash-canary",
			Output:         json.RawMessage(`{"value":"search-publication-output-canary"}`),
			PrivateBinding: &domain.ResultReceiptPrivateBinding{Document: json.RawMessage(`{"value":"search-publication-binding-canary"}`)},
		},
	}
	canaries := []string{
		"search-publication-operation-canary", "search-publication-receipt-canary", "search-publication-call-canary",
		"search-publication-output-hash-canary", "search-publication-output-canary", "search-publication-binding-canary",
	}
	for _, value := range []any{authority, &authority} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != `{}` {
			t.Fatalf("search publication authority JSON must be empty: %s", encoded)
		}
		var logOutput bytes.Buffer
		slog.New(slog.NewJSONHandler(&logOutput, nil)).Info("search publication authority", slog.Any("authority", value))
		outputs := []string{
			string(encoded), fmt.Sprintf("%v", value), fmt.Sprintf("%+v", value),
			fmt.Sprintf("%#v", value), logOutput.String(),
		}
		for _, output := range outputs {
			for _, canary := range canaries {
				if strings.Contains(output, canary) {
					t.Fatalf("search publication authority leaked %q through %q", canary, output)
				}
			}
		}
	}
}
