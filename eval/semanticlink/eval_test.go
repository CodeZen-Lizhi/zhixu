package semanticlinkeval

import (
	"encoding/json"
	"strings"
	"testing"

	graphapplication "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
)

func TestFrozenSemanticLinkDatasetContainsInputsInsteadOfPredictions(t *testing.T) {
	raw, err := datasetFS.ReadFile("testdata/dataset.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if _, exists := document["predictions"]; exists {
		t.Fatal("frozen eval dataset must not embed precomputed predictions")
	}
	if _, exists := document["pairs"]; !exists {
		t.Fatal("frozen eval dataset must provide discovery input pairs")
	}
}

func TestFrozenSemanticLinkDatasetPassesFiveMetricGate(t *testing.T) {
	dataset, err := LoadV2()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Evaluate(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if report.RelationTypeAccuracy != 1 || report.EvidenceSupportRate != 1 || report.CandidatePrecisionAtK != 1 || report.CandidateRecall != 1 || report.IgnoredCandidateReappearanceRate != 0 ||
		report.Versions.ProviderMode != "DETERMINISTIC_FAKE" || report.Versions.Rule.RuleVersion != graphapplication.SemanticLinkScanRuleVersion || report.Versions.Workflow != graphapplication.SemanticLinkScanWorkflowGenerationVersion {
		t.Fatalf("report=%+v", report)
	}
}

func TestSemanticLinkEvalFailsWhenDiscoveryInputNoLongerProducesGoldCandidate(t *testing.T) {
	dataset, err := LoadV2()
	if err != nil {
		t.Fatal(err)
	}
	dataset.Pairs[0].Target.Labels = []string{"different statement"}
	report, err := Evaluate(dataset)
	if err == nil || report.CandidateRecall >= 0.80 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestSemanticLinkEvalScoresIgnoredCandidateReappearance(t *testing.T) {
	dataset, err := LoadV2()
	if err != nil {
		t.Fatal(err)
	}
	report := score(dataset, []prediction{{
		Key: dataset.IgnoredUnchanged[0].Key, RelationType: dataset.Expected[0].RelationType,
		EvidenceSupported: true, Score: 0.95,
	}})
	if report.IgnoredCandidateReappearanceRate == 0 {
		t.Fatalf("report=%+v", report)
	}
}

func TestSemanticLinkEvalDiscoversIgnoredSamplesBeforeFingerprintSuppression(t *testing.T) {
	dataset, err := LoadV2()
	if err != nil {
		t.Fatal(err)
	}
	predictions, err := discoverPredictions(dataset)
	if err != nil {
		t.Fatal(err)
	}
	byKey := make(map[string]prediction, len(predictions))
	for _, item := range predictions {
		byKey[item.Key] = item
	}
	for _, ignored := range dataset.IgnoredUnchanged {
		item, exists := byKey[ignored.Key]
		if !exists {
			t.Fatalf("ignored sample %s did not traverse discovery", ignored.Key)
		}
		if item.Fingerprint != ignored.Fingerprint {
			t.Fatalf("ignored sample %s fingerprint=%s, want %s", ignored.Key, item.Fingerprint, ignored.Fingerprint)
		}
	}
	active, evaluatedIgnored := projectActiveRecommendations(dataset.IgnoredUnchanged, predictions)
	if evaluatedIgnored != len(dataset.IgnoredUnchanged) || score(dataset, active).IgnoredCandidateReappearanceRate != 0 {
		t.Fatalf("evaluated_ignored=%d report=%+v", evaluatedIgnored, score(dataset, active))
	}
}

func TestSemanticLinkEvalFailsWhenIgnoredFingerprintChanges(t *testing.T) {
	dataset, err := LoadV2()
	if err != nil {
		t.Fatal(err)
	}
	dataset.IgnoredUnchanged[0].Fingerprint = strings.Repeat("0", 64)
	report, err := Evaluate(dataset)
	if err == nil || report.IgnoredCandidateReappearanceRate == 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
