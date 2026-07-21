// Package semanticlinkeval 计算 Semantic Link Candidate 的冻结离线质量指标。
package semanticlinkeval

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

//go:embed testdata/dataset.json
var datasetFS embed.FS

// Versions 冻结一次评测使用的规则、Workflow 与能力模式。
type Versions struct {
	Dataset      string `json:"dataset"`
	Rule         string `json:"rule"`
	Workflow     string `json:"workflow"`
	ProviderMode string `json:"provider_mode"`
}

// ExpectedCandidate 是 Gold Set 中应被发现的关系。
type ExpectedCandidate struct {
	Key          string `json:"key"`
	RelationType string `json:"relation_type"`
}

// Prediction 是按 score 降序参与门禁的确定性候选输出。
type Prediction struct {
	Key               string  `json:"key"`
	RelationType      string  `json:"relation_type"`
	EvidenceSupported bool    `json:"evidence_supported"`
	Score             float64 `json:"score"`
}

// Dataset 是 Semantic Link 离线门禁的冻结输入。
type Dataset struct {
	SchemaVersion    string              `json:"schema_version"`
	Versions         Versions            `json:"versions"`
	K                int                 `json:"k"`
	Expected         []ExpectedCandidate `json:"expected"`
	IgnoredUnchanged []string            `json:"ignored_unchanged"`
	Predictions      []Prediction        `json:"predictions"`
}

// Report 输出五项 PRD 冻结指标，并明确区分确定性 Fake 与真实 Provider 质量。
type Report struct {
	SchemaVersion                    string   `json:"schema_version"`
	Versions                         Versions `json:"versions"`
	RelationTypeAccuracy             float64  `json:"relation_type_accuracy"`
	EvidenceSupportRate              float64  `json:"evidence_support_rate"`
	CandidatePrecisionAtK            float64  `json:"candidate_precision_at_k"`
	CandidateRecall                  float64  `json:"candidate_recall"`
	IgnoredCandidateReappearanceRate float64  `json:"ignored_candidate_reappearance_rate"`
}

// LoadV1 严格读取仓库内固定 Gold Set。
func LoadV1() (Dataset, error) {
	raw, err := datasetFS.ReadFile("testdata/dataset.json")
	if err != nil {
		return Dataset{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var dataset Dataset
	if err := decoder.Decode(&dataset); err != nil {
		return Dataset{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Dataset{}, errors.New("semantic link eval dataset contains trailing JSON")
	}
	if err := ValidateDataset(dataset); err != nil {
		return Dataset{}, err
	}
	return dataset, nil
}

// Evaluate 计算指标并执行确定性质量门禁。
func Evaluate(dataset Dataset) (Report, error) {
	if err := ValidateDataset(dataset); err != nil {
		return Report{}, err
	}
	expected := make(map[string]string, len(dataset.Expected))
	for _, item := range dataset.Expected {
		expected[item.Key] = item.RelationType
	}
	predictions := append([]Prediction(nil), dataset.Predictions...)
	sort.SliceStable(predictions, func(i, j int) bool {
		if predictions[i].Score != predictions[j].Score {
			return predictions[i].Score > predictions[j].Score
		}
		return predictions[i].Key < predictions[j].Key
	})

	matched, typed, supported := 0, 0, 0
	predictedKeys := make(map[string]struct{}, len(predictions))
	for _, prediction := range predictions {
		predictedKeys[prediction.Key] = struct{}{}
		if relationType, ok := expected[prediction.Key]; ok {
			matched++
			if relationType == prediction.RelationType {
				typed++
			}
		}
		if prediction.EvidenceSupported {
			supported++
		}
	}
	topK := min(dataset.K, len(predictions))
	relevantAtK := 0
	for _, prediction := range predictions[:topK] {
		if _, ok := expected[prediction.Key]; ok {
			relevantAtK++
		}
	}
	reappeared := 0
	for _, key := range dataset.IgnoredUnchanged {
		if _, ok := predictedKeys[key]; ok {
			reappeared++
		}
	}
	report := Report{
		SchemaVersion: "semantic-link-eval-report/v1", Versions: dataset.Versions,
		RelationTypeAccuracy:             ratio(typed, matched),
		EvidenceSupportRate:              ratio(supported, len(predictions)),
		CandidatePrecisionAtK:            ratio(relevantAtK, topK),
		CandidateRecall:                  ratio(matched, len(expected)),
		IgnoredCandidateReappearanceRate: ratio(reappeared, len(dataset.IgnoredUnchanged)),
	}
	if report.RelationTypeAccuracy < 0.90 || report.EvidenceSupportRate < 1 || report.CandidatePrecisionAtK < 0.80 || report.CandidateRecall < 0.80 || report.IgnoredCandidateReappearanceRate != 0 {
		return report, errors.New("semantic link eval quality gate failed")
	}
	return report, nil
}

// MarshalReport 输出稳定、可审阅的 JSON 报告。
func MarshalReport(report Report) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

// ValidateDataset 校验数据集身份、版本和样本唯一性。
func ValidateDataset(dataset Dataset) error {
	if dataset.SchemaVersion != "semantic-link-eval/v1" || dataset.Versions.Dataset == "" || dataset.Versions.Rule == "" || dataset.Versions.Workflow == "" || dataset.Versions.ProviderMode != "DETERMINISTIC_FAKE" || dataset.K < 1 || len(dataset.Expected) == 0 || len(dataset.Predictions) == 0 || len(dataset.IgnoredUnchanged) == 0 {
		return errors.New("semantic link eval dataset identity or coverage is invalid")
	}
	seenExpected := make(map[string]struct{}, len(dataset.Expected))
	for _, item := range dataset.Expected {
		if strings.TrimSpace(item.Key) != item.Key || item.Key == "" || item.RelationType == "" {
			return errors.New("semantic link eval expected candidate is invalid")
		}
		if _, exists := seenExpected[item.Key]; exists {
			return fmt.Errorf("semantic link eval expected key %s is duplicated", item.Key)
		}
		seenExpected[item.Key] = struct{}{}
	}
	seenPredictions := make(map[string]struct{}, len(dataset.Predictions))
	for _, item := range dataset.Predictions {
		if strings.TrimSpace(item.Key) != item.Key || item.Key == "" || item.RelationType == "" || item.Score < 0 || item.Score > 1 {
			return errors.New("semantic link eval prediction is invalid")
		}
		if _, exists := seenPredictions[item.Key]; exists {
			return fmt.Errorf("semantic link eval prediction key %s is duplicated", item.Key)
		}
		seenPredictions[item.Key] = struct{}{}
	}
	return nil
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
