// Package agenteval 计算 M6-02 Agent 固定数据集的离线质量指标。
package agenteval

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

//go:embed dataset/*.json
var datasets embed.FS

// ProviderStatus 表示真实模型评测是否实际执行。
type ProviderStatus string

const (
	// ProviderSkipped 表示当前命令只运行离线固定夹具，不把真实模型质量计为通过。
	ProviderSkipped ProviderStatus = "SKIPPED_NOT_CONFIGURED"
)

// RelationCase 是一个五分类期望与观测对。
type RelationCase struct {
	ID        string                             `json:"id"`
	Expected  knowledgedomain.RelationAssessment `json:"expected"`
	Predicted knowledgedomain.RelationAssessment `json:"predicted"`
}

// AnswerCase 保存 Citation、Faithfulness、Conflict 和 Refusal 的聚合事实。
type AnswerCase struct {
	ID                     string `json:"id"`
	AssertionCount         int    `json:"assertion_count"`
	CitedAssertionCount    int    `json:"cited_assertion_count"`
	CitationCount          int    `json:"citation_count"`
	SupportedCitationCount int    `json:"supported_citation_count"`
	ExpectedFaithful       bool   `json:"expected_faithful"`
	ObservedFaithful       bool   `json:"observed_faithful"`
	ConflictExpected       bool   `json:"conflict_expected"`
	ConflictDisclosed      bool   `json:"conflict_disclosed"`
	RefusalExpected        bool   `json:"refusal_expected"`
	Refused                bool   `json:"refused"`
}

// Dataset 是一个版本化离线评测输入。
type Dataset struct {
	DatasetID      string         `json:"dataset_id"`
	DatasetVersion string         `json:"dataset_version"`
	RelationCases  []RelationCase `json:"relation_cases"`
	AnswerCases    []AnswerCase   `json:"answer_cases"`
}

// Score 保存指标的精确分子、分母和值。
type Score struct {
	Numerator   int     `json:"numerator"`
	Denominator int     `json:"denominator"`
	Value       float64 `json:"value"`
}

// ClassMetrics 保存单个 Relation Assessment 的 Precision、Recall 和 F1。
type ClassMetrics struct {
	Precision Score   `json:"precision"`
	Recall    Score   `json:"recall"`
	F1        float64 `json:"f1"`
}

// Report 是可版本化输出的 Offline Agent Eval 报告。
type Report struct {
	DatasetID                 string                    `json:"dataset_id"`
	DatasetVersion            string                    `json:"dataset_version"`
	Mode                      string                    `json:"mode"`
	RealProvider              ProviderStatus            `json:"real_provider"`
	ConfusionMatrix           map[string]map[string]int `json:"relation_confusion_matrix"`
	RelationMetrics           map[string]ClassMetrics   `json:"relation_metrics"`
	ConflictToDuplicateErrors int                       `json:"conflict_to_duplicate_errors"`
	CitationPrecision         Score                     `json:"citation_precision"`
	CitationCoverage          Score                     `json:"citation_coverage"`
	Faithfulness              Score                     `json:"faithfulness"`
	ConflictDisclosure        Score                     `json:"conflict_disclosure"`
	AppropriateRefusal        Score                     `json:"appropriate_refusal"`
}

// LoadV1 严格加载内嵌的 v1 固定数据集。
func LoadV1() (Dataset, error) {
	raw, err := datasets.ReadFile("dataset/v1.json")
	if err != nil {
		return Dataset{}, err
	}
	return agentdomain.DecodeStrict[Dataset](raw, agentdomain.DefaultDecodeLimits(), ValidateDataset)
}

// Evaluate 计算固定数据集指标，并阻断 Conflict 被误判 Duplicate 的高风险回归。
func Evaluate(dataset Dataset) (Report, error) {
	if err := ValidateDataset(dataset); err != nil {
		return Report{}, err
	}
	classes := relationClasses()
	matrix := make(map[string]map[string]int, len(classes))
	for _, expected := range classes {
		matrix[string(expected)] = make(map[string]int, len(classes))
	}
	highRisk := 0
	for _, item := range dataset.RelationCases {
		matrix[string(item.Expected)][string(item.Predicted)]++
		if item.Expected == knowledgedomain.AssessmentConflict && item.Predicted == knowledgedomain.AssessmentDuplicate {
			highRisk++
		}
	}
	metrics := make(map[string]ClassMetrics, len(classes))
	for _, class := range classes {
		tp, predicted, expected := 0, 0, 0
		for _, item := range dataset.RelationCases {
			if item.Predicted == class {
				predicted++
			}
			if item.Expected == class {
				expected++
			}
			if item.Expected == class && item.Predicted == class {
				tp++
			}
		}
		precision, recall := score(tp, predicted), score(tp, expected)
		f1 := 0.0
		if precision.Value+recall.Value > 0 {
			f1 = 2 * precision.Value * recall.Value / (precision.Value + recall.Value)
		}
		metrics[string(class)] = ClassMetrics{Precision: precision, Recall: recall, F1: f1}
	}
	assertions, cited, citations, supported := 0, 0, 0, 0
	faithful, conflictExpected, conflictDisclosed, appropriate := 0, 0, 0, 0
	for _, item := range dataset.AnswerCases {
		assertions += item.AssertionCount
		cited += item.CitedAssertionCount
		citations += item.CitationCount
		supported += item.SupportedCitationCount
		if item.ExpectedFaithful == item.ObservedFaithful {
			faithful++
		}
		if item.ConflictExpected {
			conflictExpected++
			if item.ConflictDisclosed {
				conflictDisclosed++
			}
		}
		if item.RefusalExpected == item.Refused {
			appropriate++
		}
	}
	report := Report{
		DatasetID: dataset.DatasetID, DatasetVersion: dataset.DatasetVersion, Mode: "offline_fixture",
		RealProvider: ProviderSkipped, ConfusionMatrix: matrix, RelationMetrics: metrics,
		ConflictToDuplicateErrors: highRisk, CitationPrecision: score(supported, citations), CitationCoverage: score(cited, assertions),
		Faithfulness: score(faithful, len(dataset.AnswerCases)), ConflictDisclosure: score(conflictDisclosed, conflictExpected),
		AppropriateRefusal: score(appropriate, len(dataset.AnswerCases)),
	}
	if highRisk != 0 {
		return report, errors.New("agent eval high-risk conflict-to-duplicate regression")
	}
	return report, nil
}

// ValidateDataset 校验数据集版本、枚举、唯一 ID 与指标边界。
func ValidateDataset(dataset Dataset) error {
	if dataset.DatasetID != "agent-core-offline" || dataset.DatasetVersion != "v1" || len(dataset.RelationCases) < 5 || len(dataset.AnswerCases) == 0 {
		return errors.New("agent eval dataset identity or coverage is invalid")
	}
	seen := make(map[string]struct{}, len(dataset.RelationCases)+len(dataset.AnswerCases))
	covered := make(map[knowledgedomain.RelationAssessment]bool, 5)
	for _, item := range dataset.RelationCases {
		if item.ID == "" || !validAssessment(item.Expected) || !validAssessment(item.Predicted) {
			return errors.New("agent eval relation case is invalid")
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return errors.New("agent eval case id is duplicated")
		}
		seen[item.ID], covered[item.Expected] = struct{}{}, true
	}
	for _, class := range relationClasses() {
		if !covered[class] {
			return fmt.Errorf("agent eval relation class %s is missing", class)
		}
	}
	for _, item := range dataset.AnswerCases {
		if item.ID == "" || item.AssertionCount < 0 || item.CitedAssertionCount < 0 || item.CitedAssertionCount > item.AssertionCount ||
			item.CitationCount < 0 || item.SupportedCitationCount < 0 || item.SupportedCitationCount > item.CitationCount ||
			(!item.ConflictExpected && item.ConflictDisclosed) {
			return errors.New("agent eval answer case is invalid")
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return errors.New("agent eval case id is duplicated")
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

// MarshalReport 返回字段稳定且不包含 Prompt、Source 或模型原始响应的 JSON 报告。
func MarshalReport(report Report) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

func score(numerator, denominator int) Score {
	value := 0.0
	if denominator > 0 {
		value = float64(numerator) / float64(denominator)
	}
	return Score{Numerator: numerator, Denominator: denominator, Value: value}
}

func relationClasses() []knowledgedomain.RelationAssessment {
	values := []knowledgedomain.RelationAssessment{
		knowledgedomain.AssessmentNew, knowledgedomain.AssessmentComplementary, knowledgedomain.AssessmentDuplicate,
		knowledgedomain.AssessmentConflict, knowledgedomain.AssessmentLowConfidence,
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	return values
}

func validAssessment(value knowledgedomain.RelationAssessment) bool {
	for _, candidate := range relationClasses() {
		if value == candidate {
			return true
		}
	}
	return false
}
