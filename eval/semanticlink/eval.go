// Package semanticlinkeval 计算 Semantic Link Candidate 的冻结离线质量指标。
package semanticlinkeval

import (
	"bytes"
	"context"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapplication "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	datasetSchemaVersion = "semantic-link-eval/v2"
	datasetVersion       = "semantic-link-gold/v2"
	reportSchemaVersion  = "semantic-link-eval-report/v2"
	deterministicFake    = "DETERMINISTIC_FAKE"
)

var (
	semanticIndexVersion     foundation.ID = "71000000-0000-4000-8000-000000000001"
	semanticEmbeddingVersion foundation.ID = "71000000-0000-4000-8000-000000000002"
	semanticRerankVersion    foundation.ID = "71000000-0000-4000-8000-000000000003"
	ragIndexVersion          foundation.ID = "72000000-0000-4000-8000-000000000001"
	ragEmbeddingVersion      foundation.ID = "72000000-0000-4000-8000-000000000002"
	ragRerankVersion         foundation.ID = "72000000-0000-4000-8000-000000000003"
)

//go:embed testdata/dataset.json
var datasetFS embed.FS

// Versions 冻结一次评测实际执行的规则、Workflow 与 fake provider 版本。
type Versions struct {
	Dataset            string                                      `json:"dataset"`
	Rule               graphdomain.SemanticLinkCandidateGeneration `json:"rule_generation"`
	Workflow           string                                      `json:"workflow"`
	ProviderMode       string                                      `json:"provider_mode"`
	SemanticGeneration graphdomain.SemanticLinkCandidateGeneration `json:"semantic_generation"`
	RAGGeneration      graphdomain.SemanticLinkCandidateGeneration `json:"rag_generation"`
}

// ExpectedCandidate 是 Gold Set 中应被发现的关系。
type ExpectedCandidate struct {
	Key          string                 `json:"key"`
	RelationType knowledge.RelationType `json:"relation_type"`
}

// DiscoveryNode 是 Gold Set 中交给生产 discovery application/domain 的冻结节点输入。
type DiscoveryNode struct {
	Type             knowledge.NodeType `json:"type"`
	ID               foundation.ID      `json:"id"`
	Version          int64              `json:"version"`
	Summary          string             `json:"summary"`
	Excerpt          string             `json:"excerpt"`
	Labels           []string           `json:"labels"`
	Terms            []string           `json:"terms"`
	TopicIDs         []foundation.ID    `json:"topic_ids"`
	SourceVersionIDs []foundation.ID    `json:"source_version_ids"`
}

// CandidateEvidence 是 Gold Set 中构造 Candidate 所需的冻结 Evidence 输入。
type CandidateEvidence struct {
	ID              foundation.ID `json:"id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
	SemanticHash    string        `json:"semantic_hash"`
	Reason          string        `json:"reason"`
	Excerpt         string        `json:"excerpt"`
}

// DiscoveryPair 是一组待发现端点及其候选 Evidence 输入。
type DiscoveryPair struct {
	Key      string              `json:"key"`
	Source   DiscoveryNode       `json:"source"`
	Target   DiscoveryNode       `json:"target"`
	Evidence []CandidateEvidence `json:"evidence"`
}

// IgnoredCandidate 是冻结数据集中已被用户忽略且输入未变化的 Candidate 历史事实。
type IgnoredCandidate struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
}

// Dataset 是 Semantic Link 离线门禁的冻结输入和独立 Gold 标签。
// Predictions 必须由生产 Application/Domain 管线生成，不能出现在数据集中。
type Dataset struct {
	SchemaVersion    string              `json:"schema_version"`
	DatasetVersion   string              `json:"dataset_version"`
	WorkspaceID      foundation.ID       `json:"workspace_id"`
	K                int                 `json:"k"`
	Expected         []ExpectedCandidate `json:"expected"`
	IgnoredUnchanged []IgnoredCandidate  `json:"ignored_unchanged"`
	Pairs            []DiscoveryPair     `json:"pairs"`
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

type prediction struct {
	Key               string
	Fingerprint       string
	RelationType      knowledge.RelationType
	EvidenceSupported bool
	Score             float64
}

type deterministicDiscoveryProvider struct {
	method     graphdomain.SemanticLinkDiscoveryMethod
	generation graphdomain.SemanticLinkCandidateGeneration
}

// LoadV2 严格读取仓库内固定 Gold Set 输入。
func LoadV2() (Dataset, error) {
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

// Evaluate 运行生产 discovery/domain 候选管线，再计算五项确定性质量门禁。
func Evaluate(dataset Dataset) (Report, error) {
	if err := ValidateDataset(dataset); err != nil {
		return Report{}, err
	}
	predictions, err := runDeterministicPipeline(dataset)
	if err != nil {
		return Report{}, err
	}
	report := score(dataset, predictions)
	if report.RelationTypeAccuracy < 0.90 || report.EvidenceSupportRate < 1 || report.CandidatePrecisionAtK < 0.80 || report.CandidateRecall < 0.80 || report.IgnoredCandidateReappearanceRate != 0 {
		return report, errors.New("semantic link eval quality gate failed")
	}
	return report, nil
}

// MarshalReport 输出稳定、可审阅的 JSON 报告。
func MarshalReport(report Report) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

// ValidateDataset 校验数据集身份、输入 pair、Gold 标签和忽略样本唯一性。
func ValidateDataset(dataset Dataset) error {
	if dataset.SchemaVersion != datasetSchemaVersion || dataset.DatasetVersion != datasetVersion || !validID(dataset.WorkspaceID) || dataset.K < 1 || len(dataset.Expected) == 0 || len(dataset.IgnoredUnchanged) == 0 || len(dataset.Pairs) == 0 {
		return errors.New("semantic link eval dataset identity or coverage is invalid")
	}
	pairs, byKey, err := buildDiscoveryPairs(dataset)
	if err != nil {
		return err
	}
	if len(pairs) < len(dataset.Expected)+len(dataset.IgnoredUnchanged) {
		return errors.New("semantic link eval dataset does not cover positive and ignored samples")
	}
	seenExpected := make(map[string]struct{}, len(dataset.Expected))
	for _, item := range dataset.Expected {
		pair, exists := byKey[item.Key]
		if strings.TrimSpace(item.Key) != item.Key || !exists || !knowledge.RelationTypeCompatible(item.RelationType, pair.Source.Endpoint.Ref.Type, pair.Target.Endpoint.Ref.Type) {
			return errors.New("semantic link eval expected candidate is invalid")
		}
		if _, duplicate := seenExpected[item.Key]; duplicate {
			return fmt.Errorf("semantic link eval expected key %s is duplicated", item.Key)
		}
		seenExpected[item.Key] = struct{}{}
	}
	seenIgnored := make(map[string]struct{}, len(dataset.IgnoredUnchanged))
	for _, item := range dataset.IgnoredUnchanged {
		if strings.TrimSpace(item.Key) != item.Key || item.Key == "" || byKey[item.Key].Source.Endpoint.Ref.ID == "" || !validSHA256(item.Fingerprint) {
			return errors.New("semantic link eval ignored candidate is invalid")
		}
		if _, expected := seenExpected[item.Key]; expected {
			return errors.New("semantic link eval candidate cannot be expected and ignored")
		}
		if _, duplicate := seenIgnored[item.Key]; duplicate {
			return fmt.Errorf("semantic link eval ignored key %s is duplicated", item.Key)
		}
		seenIgnored[item.Key] = struct{}{}
	}
	versions := evaluationVersions(dataset.DatasetVersion)
	for _, generation := range []graphdomain.SemanticLinkCandidateGeneration{versions.Rule, versions.SemanticGeneration, versions.RAGGeneration} {
		if err := graphdomain.ValidateSemanticLinkDiscoveryGeneration(generation); err != nil {
			return err
		}
	}
	return nil
}

func runDeterministicPipeline(dataset Dataset) ([]prediction, error) {
	predictions, err := discoverPredictions(dataset)
	if err != nil {
		return nil, err
	}
	active, evaluatedIgnored := projectActiveRecommendations(dataset.IgnoredUnchanged, predictions)
	if evaluatedIgnored != len(dataset.IgnoredUnchanged) {
		return nil, errors.New("semantic link eval ignored samples did not traverse candidate discovery")
	}
	return active, nil
}

func discoverPredictions(dataset Dataset) ([]prediction, error) {
	pairs, byKey, err := buildDiscoveryPairs(dataset)
	if err != nil {
		return nil, err
	}
	versions := evaluationVersions(dataset.DatasetVersion)
	service := graphapplication.NewSemanticLinkDiscoveryService(
		deterministicDiscoveryProvider{method: graphdomain.SemanticLinkDiscoveryMethodClaimSemanticSimilarity, generation: versions.SemanticGeneration},
		deterministicDiscoveryProvider{method: graphdomain.SemanticLinkDiscoveryMethodRAGCoRetrieval, generation: versions.RAGGeneration},
	)
	result, err := service.Discover(context.Background(), graphapplication.SemanticLinkDiscoveryCommand{
		WorkspaceID:    dataset.WorkspaceID,
		Pairs:          pairs,
		Limit:          graphdomain.MaxSemanticLinkDiscoveryLimit,
		PerSourceLimit: graphdomain.MaxSemanticLinkDiscoveryPerSource,
		RuleGeneration: versions.Rule,
	})
	if err != nil {
		return nil, err
	}
	if !executedWithGeneration(result.Signals, graphdomain.SemanticLinkDiscoveryMethodClaimSemanticSimilarity, versions.SemanticGeneration) ||
		!executedWithGeneration(result.Signals, graphdomain.SemanticLinkDiscoveryMethodRAGCoRetrieval, versions.RAGGeneration) {
		return nil, errors.New("semantic link eval deterministic providers did not execute through discovery")
	}
	byPair := make(map[graphdomain.SemanticLinkDiscoveryPairKey]DiscoveryPair, len(dataset.Pairs))
	for _, fixture := range dataset.Pairs {
		pair := byKey[fixture.Key]
		key, keyErr := pair.PairKey()
		if keyErr != nil {
			return nil, keyErr
		}
		byPair[key] = fixture
	}
	predictions := make([]prediction, 0, len(result.Items))
	for index, hit := range result.Items {
		classification := graphdomain.ClassifySemanticLinkRuleDiscoveryHit(hit)
		if !classification.Eligible {
			continue
		}
		key, keyErr := (graphdomain.SemanticLinkDiscoveryPair{
			Source: graphdomain.SemanticLinkDiscoveryNode{Endpoint: graphdomain.SemanticLinkCandidateEndpoint{Ref: hit.Source}},
			Target: graphdomain.SemanticLinkDiscoveryNode{Endpoint: graphdomain.SemanticLinkCandidateEndpoint{Ref: hit.Target}},
		}).PairKey()
		if keyErr != nil {
			return nil, keyErr
		}
		fixture, exists := byPair[key]
		if !exists {
			return nil, errors.New("semantic link eval discovery returned an unknown pair")
		}
		candidate, candidateErr := buildCandidate(dataset.WorkspaceID, fixture, hit, classification, versions.Rule, index)
		if candidateErr != nil {
			return nil, candidateErr
		}
		predictions = append(predictions, prediction{
			Key: fixture.Key, Fingerprint: candidate.Fingerprint, RelationType: candidate.SuggestedRelationType,
			EvidenceSupported: len(candidate.Evidence) > 0, Score: candidate.Confidence,
		})
	}
	return predictions, nil
}

func projectActiveRecommendations(ignored []IgnoredCandidate, predictions []prediction) ([]prediction, int) {
	history := make(map[string]string, len(ignored))
	for _, item := range ignored {
		history[item.Key] = item.Fingerprint
	}
	active := make([]prediction, 0, len(predictions))
	evaluatedIgnored := 0
	for _, item := range predictions {
		fingerprint, previouslyIgnored := history[item.Key]
		if !previouslyIgnored {
			active = append(active, item)
			continue
		}
		evaluatedIgnored++
		if item.Fingerprint != fingerprint {
			active = append(active, item)
		}
	}
	return active, evaluatedIgnored
}

func buildCandidate(workspaceID foundation.ID, fixture DiscoveryPair, hit graphdomain.SemanticLinkDiscoveryHit, classification graphdomain.SemanticLinkRuleClassification, generation graphdomain.SemanticLinkCandidateGeneration, index int) (graphdomain.SemanticLinkCandidate, error) {
	source, target := discoveryNode(fixture.Source, workspaceID), discoveryNode(fixture.Target, workspaceID)
	if source.Endpoint.Ref != hit.Source {
		source, target = target, source
	}
	if source.Endpoint.Ref != hit.Source || target.Endpoint.Ref != hit.Target {
		return graphdomain.SemanticLinkCandidate{}, errors.New("semantic link eval hit endpoints do not match the input pair")
	}
	evidence, err := candidateEvidence(workspaceID, fixture.Evidence)
	if err != nil {
		return graphdomain.SemanticLinkCandidate{}, err
	}
	now := time.Date(2026, time.July, 21, 0, 0, index, 0, time.UTC)
	candidate := graphdomain.SemanticLinkCandidate{
		ID: evaluationID(index + 1), WorkspaceID: workspaceID,
		Source: source.Endpoint, Target: target.Endpoint,
		SuggestedRelationType: classification.RelationType,
		Status:                graphdomain.SemanticLinkCandidateStatusActive,
		Reason:                classification.Reason, Confidence: classification.Confidence,
		DiscoveryMethods: append([]graphdomain.SemanticLinkDiscoveryMethod(nil), hit.Methods...),
		Evidence:         evidence, Generation: generation,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	candidate.Fingerprint, err = graphdomain.ComputeSemanticLinkCandidateFingerprint(candidate)
	if err != nil {
		return graphdomain.SemanticLinkCandidate{}, err
	}
	if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil {
		return graphdomain.SemanticLinkCandidate{}, err
	}
	return candidate, nil
}

func score(dataset Dataset, predictions []prediction) Report {
	expected := make(map[string]knowledge.RelationType, len(dataset.Expected))
	for _, item := range dataset.Expected {
		expected[item.Key] = item.RelationType
	}
	values := append([]prediction(nil), predictions...)
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Score != values[j].Score {
			return values[i].Score > values[j].Score
		}
		return values[i].Key < values[j].Key
	})
	matched, typed, supported := 0, 0, 0
	predictedKeys := make(map[string]struct{}, len(values))
	for _, value := range values {
		predictedKeys[value.Key] = struct{}{}
		if relationType, ok := expected[value.Key]; ok {
			matched++
			if relationType == value.RelationType {
				typed++
			}
		}
		if value.EvidenceSupported {
			supported++
		}
	}
	topK := min(dataset.K, len(values))
	relevantAtK := 0
	for _, value := range values[:topK] {
		if _, ok := expected[value.Key]; ok {
			relevantAtK++
		}
	}
	reappeared := 0
	for _, item := range dataset.IgnoredUnchanged {
		if _, ok := predictedKeys[item.Key]; ok {
			reappeared++
		}
	}
	return Report{
		SchemaVersion:                    reportSchemaVersion,
		Versions:                         evaluationVersions(dataset.DatasetVersion),
		RelationTypeAccuracy:             ratio(typed, matched),
		EvidenceSupportRate:              ratio(supported, len(values)),
		CandidatePrecisionAtK:            ratio(relevantAtK, topK),
		CandidateRecall:                  ratio(matched, len(expected)),
		IgnoredCandidateReappearanceRate: ratio(reappeared, len(dataset.IgnoredUnchanged)),
	}
}

func buildDiscoveryPairs(dataset Dataset) ([]graphdomain.SemanticLinkDiscoveryPair, map[string]graphdomain.SemanticLinkDiscoveryPair, error) {
	result := make([]graphdomain.SemanticLinkDiscoveryPair, 0, len(dataset.Pairs))
	byKey := make(map[string]graphdomain.SemanticLinkDiscoveryPair, len(dataset.Pairs))
	seenPairs := make(map[graphdomain.SemanticLinkDiscoveryPairKey]struct{}, len(dataset.Pairs))
	for _, fixture := range dataset.Pairs {
		if strings.TrimSpace(fixture.Key) != fixture.Key || fixture.Key == "" {
			return nil, nil, errors.New("semantic link eval pair key is invalid")
		}
		if _, duplicate := byKey[fixture.Key]; duplicate {
			return nil, nil, fmt.Errorf("semantic link eval pair key %s is duplicated", fixture.Key)
		}
		pair := graphdomain.SemanticLinkDiscoveryPair{
			Source: discoveryNode(fixture.Source, dataset.WorkspaceID),
			Target: discoveryNode(fixture.Target, dataset.WorkspaceID),
		}
		if err := graphdomain.ValidateSemanticLinkDiscoveryNode(dataset.WorkspaceID, pair.Source); err != nil {
			return nil, nil, err
		}
		if err := graphdomain.ValidateSemanticLinkDiscoveryNode(dataset.WorkspaceID, pair.Target); err != nil {
			return nil, nil, err
		}
		pairKey, err := pair.PairKey()
		if err != nil {
			return nil, nil, err
		}
		if _, duplicate := seenPairs[pairKey]; duplicate {
			return nil, nil, errors.New("semantic link eval pair endpoints are duplicated")
		}
		if _, err := candidateEvidence(dataset.WorkspaceID, fixture.Evidence); err != nil {
			return nil, nil, err
		}
		seenPairs[pairKey] = struct{}{}
		byKey[fixture.Key] = pair
		result = append(result, pair)
	}
	return result, byKey, nil
}

func discoveryNode(value DiscoveryNode, workspaceID foundation.ID) graphdomain.SemanticLinkDiscoveryNode {
	topics := make([]knowledge.NodeRef, len(value.TopicIDs))
	for index, id := range value.TopicIDs {
		topics[index] = knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: id}
	}
	return graphdomain.SemanticLinkDiscoveryNode{
		WorkspaceID: workspaceID,
		Endpoint: graphdomain.SemanticLinkCandidateEndpoint{
			Ref: knowledge.NodeRef{Type: value.Type, ID: value.ID}, Version: value.Version,
			Summary: value.Summary, Excerpt: value.Excerpt,
		},
		Lifecycle: knowledge.NodeLifecycleConfirmed,
		Labels:    append([]string(nil), value.Labels...), Terms: append([]string(nil), value.Terms...),
		TopicRefs: topics, SourceVersionIDs: append([]foundation.ID(nil), value.SourceVersionIDs...),
	}
}

func candidateEvidence(workspaceID foundation.ID, values []CandidateEvidence) ([]graphdomain.SemanticLinkCandidateEvidence, error) {
	result := make([]graphdomain.SemanticLinkCandidateEvidence, len(values))
	for index, value := range values {
		result[index] = graphdomain.SemanticLinkCandidateEvidence{
			ID:           value.ID,
			Provenance:   knowledge.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: value.SourceVersionID, SourceSpanID: value.SourceSpanID},
			SemanticHash: value.SemanticHash, Reason: value.Reason, Excerpt: value.Excerpt,
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SemanticHash < result[j].SemanticHash })
	if len(result) == 0 {
		return nil, errors.New("semantic link eval pair evidence is required")
	}
	seenIDs := make(map[foundation.ID]struct{}, len(result))
	seenHashes := make(map[string]struct{}, len(result))
	for _, value := range result {
		if !validID(value.ID) || knowledge.ValidateProvenanceRef(value.Provenance) != nil || value.Provenance.WorkspaceID != workspaceID || !validSHA256(value.SemanticHash) {
			return nil, errors.New("semantic link eval pair evidence identity is invalid")
		}
		if reason, err := knowledge.NormalizeReason(value.Reason, true); err != nil || reason != value.Reason {
			return nil, errors.New("semantic link eval pair evidence reason is invalid")
		}
		if excerpt, err := knowledge.NormalizeReason(value.Excerpt, true); err != nil || excerpt != value.Excerpt {
			return nil, errors.New("semantic link eval pair evidence excerpt is invalid")
		}
		if _, duplicate := seenIDs[value.ID]; duplicate {
			return nil, errors.New("semantic link eval pair evidence id is duplicated")
		}
		if _, duplicate := seenHashes[value.SemanticHash]; duplicate {
			return nil, errors.New("semantic link eval pair evidence hash is duplicated")
		}
		seenIDs[value.ID], seenHashes[value.SemanticHash] = struct{}{}, struct{}{}
	}
	return result, nil
}

func (provider deterministicDiscoveryProvider) Evaluate(ctx context.Context, request graphdomain.SemanticLinkDiscoveryProviderRequest) (graphdomain.SemanticLinkDiscoveryExternalResult, error) {
	if ctx == nil || ctx.Err() != nil || request.WorkspaceID == "" || request.Limit != len(request.Pairs) {
		return graphdomain.SemanticLinkDiscoveryExternalResult{}, errors.New("semantic link eval deterministic provider request is invalid")
	}
	return graphdomain.SemanticLinkDiscoveryExternalResult{Method: provider.method, Supported: true, Generation: provider.generation}, nil
}

func executedWithGeneration(signals []graphdomain.SemanticLinkDiscoverySignalReport, method graphdomain.SemanticLinkDiscoveryMethod, generation graphdomain.SemanticLinkCandidateGeneration) bool {
	for _, signal := range signals {
		if signal.Method == method {
			return signal.Status == graphdomain.SemanticLinkDiscoverySignalStatusExecuted && graphdomain.EqualSemanticLinkCandidateGeneration(signal.Generation, generation)
		}
	}
	return false
}

func evaluationVersions(dataset string) Versions {
	ruleID := graphapplication.SemanticLinkScanRuleID
	return Versions{
		Dataset:      dataset,
		Rule:         graphdomain.SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: graphapplication.SemanticLinkScanRuleVersion},
		Workflow:     graphapplication.SemanticLinkScanWorkflowGenerationVersion,
		ProviderMode: deterministicFake,
		SemanticGeneration: graphdomain.SemanticLinkCandidateGeneration{
			IndexVersionID: &semanticIndexVersion, EmbeddingVersionID: &semanticEmbeddingVersion, RerankVersionID: &semanticRerankVersion,
			ModelVersion: "semantic-link-eval-semantic/v1", ModelProfileVersion: "deterministic/v1", PromptVersion: "semantic-link-eval-semantic-prompt/v1", SchemaVersion: "semantic-link-eval-semantic-schema/v1",
		},
		RAGGeneration: graphdomain.SemanticLinkCandidateGeneration{
			IndexVersionID: &ragIndexVersion, EmbeddingVersionID: &ragEmbeddingVersion, RerankVersionID: &ragRerankVersion,
			ModelVersion: "semantic-link-eval-rag/v1", ModelProfileVersion: "deterministic/v1", PromptVersion: "semantic-link-eval-rag-prompt/v1", SchemaVersion: "semantic-link-eval-rag-schema/v1",
		},
	}
}

func evaluationID(index int) foundation.ID {
	return foundation.ID(fmt.Sprintf("73000000-0000-4000-8000-%012d", index))
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
