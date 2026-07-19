package agenteval

import (
	"strings"
	"testing"

	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestEvaluateV1ProducesVersionedMetricsAndExplicitProviderSkip(t *testing.T) {
	dataset, err := LoadV1()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Evaluate(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if report.DatasetVersion != "v1" || report.RealProvider != ProviderSkipped || report.ConflictToDuplicateErrors != 0 ||
		report.CitationPrecision.Value != 1 || report.CitationCoverage.Value != 1 || report.Faithfulness.Value != 1 ||
		report.ConflictDisclosure.Value != 1 || report.AppropriateRefusal.Value != 1 {
		t.Fatalf("report=%#v", report)
	}
	encoded, err := MarshalReport(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"prompt", "source", "raw_response", "authorization"} {
		if strings.Contains(strings.ToLower(string(encoded)), secret) {
			t.Fatalf("report contains forbidden payload marker %q", secret)
		}
	}
}

func TestEvaluateBlocksConflictToDuplicateRegression(t *testing.T) {
	dataset, err := LoadV1()
	if err != nil {
		t.Fatal(err)
	}
	for index := range dataset.RelationCases {
		if dataset.RelationCases[index].Expected == knowledgedomain.AssessmentConflict {
			dataset.RelationCases[index].Predicted = knowledgedomain.AssessmentDuplicate
		}
	}
	report, err := Evaluate(dataset)
	if err == nil || report.ConflictToDuplicateErrors != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestValidateDatasetRejectsInvalidMetricBounds(t *testing.T) {
	dataset, err := LoadV1()
	if err != nil {
		t.Fatal(err)
	}
	dataset.AnswerCases[0].SupportedCitationCount = dataset.AnswerCases[0].CitationCount + 1
	if err := ValidateDataset(dataset); err == nil {
		t.Fatal("invalid metric bounds accepted")
	}
}
