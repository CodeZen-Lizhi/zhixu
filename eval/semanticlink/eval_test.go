package semanticlinkeval

import "testing"

func TestFrozenSemanticLinkDatasetPassesFiveMetricGate(t *testing.T) {
	dataset, err := LoadV1()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Evaluate(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if report.RelationTypeAccuracy != 1 || report.EvidenceSupportRate != 1 || report.CandidatePrecisionAtK != 1 || report.CandidateRecall != 1 || report.IgnoredCandidateReappearanceRate != 0 || report.Versions.ProviderMode != "DETERMINISTIC_FAKE" {
		t.Fatalf("report=%+v", report)
	}
}

func TestSemanticLinkEvalRejectsIgnoredCandidateReappearance(t *testing.T) {
	dataset, err := LoadV1()
	if err != nil {
		t.Fatal(err)
	}
	dataset.Predictions[0].Key = dataset.IgnoredUnchanged[0]
	report, err := Evaluate(dataset)
	if err == nil || report.IgnoredCandidateReappearanceRate == 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
