package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	// MaxSemanticLinkDiscoveryPairs 限制一次批量发现评估的 pair 数量。
	// Topic scan 每页最多 100 个节点、每节点最多 100 个检索结果时，
	// 由上层将输入切成不超过该值的有界批次。
	MaxSemanticLinkDiscoveryPairs = 10_000
	// MaxSemanticLinkDiscoveryLimit 是一个 100-node scan page 最多返回的匹配数。
	// 单个 anchor 仍受 MaxSemanticLinkDiscoveryPerSource=100 的独立硬限制。
	MaxSemanticLinkDiscoveryLimit = 10_000
	// MaxSemanticLinkDiscoveryPerSource 是单个 anchor 最多返回的匹配数。
	MaxSemanticLinkDiscoveryPerSource = 100
	// MaxSemanticLinkDiscoveryLabels 是一个端点允许参与标题/别名匹配的标签数。
	MaxSemanticLinkDiscoveryLabels = 33
	// MaxSemanticLinkDiscoveryTerms 是一个端点允许参与术语匹配的术语数。
	MaxSemanticLinkDiscoveryTerms = 128
	// MaxSemanticLinkDiscoveryTopics 是一个端点允许携带的共同 Topic 数。
	MaxSemanticLinkDiscoveryTopics = 100
	// MaxSemanticLinkDiscoverySources 是一个端点允许携带的 Source Version 数。
	MaxSemanticLinkDiscoverySources = 100

	semanticLinkDiscoveryFingerprintSchema = "semantic-link-discovery/v1"
	semanticLinkScanFingerprintSchema      = "semantic-link-scan/v1"
)

const (
	// SemanticLinkDiscoverySignalExecuted 表示该信号确实执行过并返回了命中或空结果。
	SemanticLinkDiscoverySignalExecuted = "EXECUTED"
	// SemanticLinkDiscoverySignalUnsupported 表示依赖未配置或能力明确不支持。
	SemanticLinkDiscoverySignalUnsupported = "UNSUPPORTED"
)

// SemanticLinkDiscoverySignalStatus 是一次批量发现对单个信号的事实回执。
// Unsupported 必须携带稳定 reason，调用方不能把它当成零命中成功。
type SemanticLinkDiscoverySignalStatus string

const (
	SemanticLinkDiscoverySignalStatusExecuted    SemanticLinkDiscoverySignalStatus = SemanticLinkDiscoverySignalExecuted
	SemanticLinkDiscoverySignalStatusUnsupported SemanticLinkDiscoverySignalStatus = SemanticLinkDiscoverySignalUnsupported
)

// SemanticLinkDiscoverySignalReport 记录六类信号各自的执行状态和命中数量。
type SemanticLinkDiscoverySignalReport struct {
	Method     SemanticLinkDiscoveryMethod
	Status     SemanticLinkDiscoverySignalStatus
	HitCount   int
	Reason     string
	Generation SemanticLinkCandidateGeneration
}

// SemanticLinkDiscoveryNode 是 discovery 所需的最小 Knowledge 节点快照。
// 文本只用于内存中的匹配；不会被该领域结果直接写入日志或 Workflow payload。
type SemanticLinkDiscoveryNode struct {
	WorkspaceID      foundation.ID
	Endpoint         SemanticLinkCandidateEndpoint
	Lifecycle        knowledge.NodeLifecycle
	Labels           []string
	Terms            []string
	TopicRefs        []knowledge.NodeRef
	SourceVersionIDs []foundation.ID
}

// SemanticLinkDiscoveryPair 是一次已批量加载的端点 pair。
type SemanticLinkDiscoveryPair struct {
	Source SemanticLinkDiscoveryNode
	Target SemanticLinkDiscoveryNode
}

// SemanticLinkDiscoveryPairKey 是不区分方向的稳定 pair 身份。
type SemanticLinkDiscoveryPairKey struct {
	Left  knowledge.NodeRef
	Right knowledge.NodeRef
}

// SemanticLinkDiscoveryExclusion 表示已有正式 Relation 或未终结 Proposal 对 pair 的阻断。
// 排除在分类前发生，避免把已有事实重新推荐成候选。
type SemanticLinkDiscoveryExclusion struct {
	Source knowledge.NodeRef
	Target knowledge.NodeRef
	Reason string
}

// SemanticLinkDiscoveryExternalHit 是 Semantic/RAG provider 返回的批量命中。
// Provider 只能返回稳定端点与 semantic hash，不能返回原始正文。
type SemanticLinkDiscoveryExternalHit struct {
	Source         knowledge.NodeRef
	Target         knowledge.NodeRef
	Score          float64
	EvidenceHashes []string
}

// SemanticLinkDiscoveryExternalResult 是一个可选外部信号的能力回执。
// Supported=false 时必须填写 UnsupportedReason；这表示显式不支持，而非空候选成功。
type SemanticLinkDiscoveryExternalResult struct {
	Method            SemanticLinkDiscoveryMethod
	Supported         bool
	UnsupportedReason string
	Hits              []SemanticLinkDiscoveryExternalHit
	Generation        SemanticLinkCandidateGeneration
}

// SemanticLinkDiscoveryProviderPair 是外部 provider 所需的轻量版本化 pair。
// Provider 通过 Knowledge/Retrieval 的批量读取边界加载正文或 Evidence，Application
// 不复制整页文本进入远程调用合同。
type SemanticLinkDiscoveryProviderPair struct {
	Source        knowledge.NodeRef
	SourceVersion int64
	Target        knowledge.NodeRef
	TargetVersion int64
}

// SemanticLinkDiscoveryProviderRequest 是 Semantic/RAG Adapter 的单次批量输入。
// Provider 必须一次处理整个 pair 页，禁止在 Application 层逐 pair 远程调用。
type SemanticLinkDiscoveryProviderRequest struct {
	WorkspaceID foundation.ID
	Pairs       []SemanticLinkDiscoveryProviderPair
	Limit       int
}

// SemanticLinkDiscoveryRequest 是纯领域 discovery engine 的有界输入。
// 四类规则信号由 engine 真正计算；Semantic/RAG 由上层 provider 批量提供结果。
type SemanticLinkDiscoveryRequest struct {
	WorkspaceID    foundation.ID
	Pairs          []SemanticLinkDiscoveryPair
	Exclusions     []SemanticLinkDiscoveryExclusion
	Semantic       SemanticLinkDiscoveryExternalResult
	RAG            SemanticLinkDiscoveryExternalResult
	Limit          int
	PerSourceLimit int
	RuleGeneration SemanticLinkCandidateGeneration
}

// SemanticLinkDiscoveryHit 是一个可审阅的 signal 组合结果。
type SemanticLinkDiscoveryHit struct {
	Source         knowledge.NodeRef
	Target         knowledge.NodeRef
	Methods        []SemanticLinkDiscoveryMethod
	EvidenceHashes []string
	SignalScores   map[SemanticLinkDiscoveryMethod]float64
}

// SemanticLinkRuleClassification 是确定性规则对 discovery hit 的候选分类结果。
// Eligible=false 表示信号不足以形成可审阅 Candidate。
type SemanticLinkRuleClassification struct {
	RelationType knowledge.RelationType
	Confidence   float64
	Reason       string
	Eligible     bool
}

// SemanticLinkDiscoveryResult 是 discovery engine 的确定性输出。
type SemanticLinkDiscoveryResult struct {
	WorkspaceID    foundation.ID
	Items          []SemanticLinkDiscoveryHit
	Signals        []SemanticLinkDiscoverySignalReport
	EvaluatedPairs int
	ExcludedPairs  int
	Truncated      bool
}

// ValidateSemanticLinkDiscoveryRequest 校验 discovery engine 的完整有界输入。
func ValidateSemanticLinkDiscoveryRequest(request SemanticLinkDiscoveryRequest) error {
	return validateSemanticLinkDiscoveryRequest(request)
}

// ValidateSemanticLinkDiscoveryGeneration 校验 discovery/scan 冻结版本合同。
func ValidateSemanticLinkDiscoveryGeneration(value SemanticLinkCandidateGeneration) error {
	return validateDiscoveryGeneration(value)
}

// EqualSemanticLinkCandidateGeneration 比较版本值而不是可选 ID 指针地址。
func EqualSemanticLinkCandidateGeneration(left, right SemanticLinkCandidateGeneration) bool {
	return equalOptionalDiscoveryID(left.IndexVersionID, right.IndexVersionID) &&
		equalOptionalDiscoveryID(left.EmbeddingVersionID, right.EmbeddingVersionID) &&
		equalOptionalDiscoveryID(left.RerankVersionID, right.RerankVersionID) &&
		equalOptionalDiscoveryID(left.RuleID, right.RuleID) &&
		equalOptionalDiscoveryID(left.ModelRunID, right.ModelRunID) &&
		left.ModelVersion == right.ModelVersion &&
		left.ModelProfileVersion == right.ModelProfileVersion &&
		left.PromptVersion == right.PromptVersion &&
		left.SchemaVersion == right.SchemaVersion &&
		left.RuleVersion == right.RuleVersion
}

// ValidateSemanticLinkDiscoveryNode 校验 discovery 节点只携带真实 Topic/Claim 引用。
func ValidateSemanticLinkDiscoveryNode(workspaceID foundation.ID, node SemanticLinkDiscoveryNode) error {
	if !validDiscoveryID(workspaceID) || node.WorkspaceID != workspaceID || !validDiscoveryNodeRef(node.Endpoint.Ref) || node.Endpoint.Version < 1 {
		return discoveryInvalid("discovery node identity or workspace is invalid")
	}
	if node.Endpoint.Ref.Type != knowledge.NodeTypeTopic && node.Endpoint.Ref.Type != knowledge.NodeTypeClaim {
		return discoveryInvalid("discovery node type is unsupported")
	}
	if strings.TrimSpace(node.Endpoint.Summary) == "" {
		return discoveryInvalid("discovery node summary is required")
	}
	if summary, err := knowledge.NormalizeReason(node.Endpoint.Summary, true); err != nil || summary != node.Endpoint.Summary {
		return discoveryInvalid("discovery node summary is not canonical")
	}
	if node.Endpoint.Excerpt != "" {
		if excerpt, err := knowledge.NormalizeReason(node.Endpoint.Excerpt, true); err != nil || excerpt != node.Endpoint.Excerpt {
			return discoveryInvalid("discovery node excerpt is not canonical")
		}
	}
	if !validDiscoveryLifecycle(node.Lifecycle) {
		return discoveryInvalid("discovery node lifecycle is not eligible")
	}
	if len(node.Labels) > MaxSemanticLinkDiscoveryLabels || len(node.Terms) > MaxSemanticLinkDiscoveryTerms || len(node.TopicRefs) > MaxSemanticLinkDiscoveryTopics || len(node.SourceVersionIDs) > MaxSemanticLinkDiscoverySources {
		return discoveryInvalid("discovery node metadata exceeds the bounded limit")
	}
	if err := validateDiscoveryTextSet(node.Labels, MaxSemanticLinkDiscoveryLabels); err != nil {
		return err
	}
	if err := validateDiscoveryTextSet(node.Terms, MaxSemanticLinkDiscoveryTerms); err != nil {
		return err
	}
	if err := validateDiscoveryNodeRefs(node.TopicRefs, knowledge.NodeTypeTopic); err != nil {
		return err
	}
	if err := validateDiscoveryIDs(node.SourceVersionIDs); err != nil {
		return err
	}
	return nil
}

// PairKey 返回不区分方向的 canonical pair key。
func (pair SemanticLinkDiscoveryPair) PairKey() (SemanticLinkDiscoveryPairKey, error) {
	if pair.Source.Endpoint.Ref == pair.Target.Endpoint.Ref {
		return SemanticLinkDiscoveryPairKey{}, discoveryInvalid("discovery pair must not be self-referential")
	}
	left, right := pair.Source.Endpoint.Ref, pair.Target.Endpoint.Ref
	if discoveryNodeRefKey(right) < discoveryNodeRefKey(left) {
		left, right = right, left
	}
	return SemanticLinkDiscoveryPairKey{Left: left, Right: right}, nil
}

// EvaluateSemanticLinkDiscovery 执行四类本地规则并合并两个可选外部 signal。
// 结果只包含真实命中；未配置的外部能力会在 Signals 中显式标记 UNSUPPORTED。
func EvaluateSemanticLinkDiscovery(request SemanticLinkDiscoveryRequest) (SemanticLinkDiscoveryResult, error) {
	if err := validateSemanticLinkDiscoveryRequest(request); err != nil {
		return SemanticLinkDiscoveryResult{}, err
	}
	semanticHits, err := externalHitIndex(request.Semantic)
	if err != nil {
		return SemanticLinkDiscoveryResult{}, err
	}
	ragHits, err := externalHitIndex(request.RAG)
	if err != nil {
		return SemanticLinkDiscoveryResult{}, err
	}
	requestedPairs := make(map[SemanticLinkDiscoveryPairKey]struct{}, len(request.Pairs))
	for _, pair := range request.Pairs {
		key, keyErr := pair.PairKey()
		if keyErr != nil {
			return SemanticLinkDiscoveryResult{}, keyErr
		}
		requestedPairs[key] = struct{}{}
	}
	exclusions, err := exclusionIndex(request.Exclusions, requestedPairs)
	if err != nil {
		return SemanticLinkDiscoveryResult{}, err
	}

	methods := []SemanticLinkDiscoveryMethod{
		SemanticLinkDiscoveryMethodTitleAlias,
		SemanticLinkDiscoveryMethodTermMatch,
		SemanticLinkDiscoveryMethodCommonTopic,
		SemanticLinkDiscoveryMethodSharedSource,
	}
	result := SemanticLinkDiscoveryResult{WorkspaceID: request.WorkspaceID, EvaluatedPairs: len(request.Pairs)}
	hitCounts := make(map[SemanticLinkDiscoveryMethod]int, 6)
	matches := make([]SemanticLinkDiscoveryHit, 0, minInt(len(request.Pairs), request.Limit))
	seen := make(map[SemanticLinkDiscoveryPairKey]struct{}, len(request.Pairs))

	for _, pair := range request.Pairs {
		key, keyErr := pair.PairKey()
		if keyErr != nil {
			return SemanticLinkDiscoveryResult{}, keyErr
		}
		if _, duplicate := seen[key]; duplicate {
			return SemanticLinkDiscoveryResult{}, discoveryInvalid("discovery pair list contains duplicate endpoints")
		}
		seen[key] = struct{}{}
		if _, blocked := exclusions[key]; blocked {
			result.ExcludedPairs++
			continue
		}

		matched := make(map[SemanticLinkDiscoveryMethod]float64, 6)
		evidence := make([]string, 0, 4)
		if values := intersectCanonicalText(pair.Source.Labels, pair.Target.Labels); len(values) > 0 {
			matched[SemanticLinkDiscoveryMethodTitleAlias] = 1
			evidence = append(evidence, signalEvidenceHash(SemanticLinkDiscoveryMethodTitleAlias, key, values...))
		}
		if values := intersectCanonicalText(pair.Source.Terms, pair.Target.Terms); len(values) > 0 {
			matched[SemanticLinkDiscoveryMethodTermMatch] = termMatchStrength(pair.Source.Terms, pair.Target.Terms)
			evidence = append(evidence, signalEvidenceHash(SemanticLinkDiscoveryMethodTermMatch, key, values...))
		}
		if values := intersectNodeRefs(pair.Source.TopicRefs, pair.Target.TopicRefs); len(values) > 0 {
			matched[SemanticLinkDiscoveryMethodCommonTopic] = 1
			parts := make([]string, 0, len(values))
			for _, value := range values {
				parts = append(parts, discoveryNodeRefKey(value))
			}
			evidence = append(evidence, signalEvidenceHash(SemanticLinkDiscoveryMethodCommonTopic, key, parts...))
		}
		if values := intersectIDs(pair.Source.SourceVersionIDs, pair.Target.SourceVersionIDs); len(values) > 0 {
			matched[SemanticLinkDiscoveryMethodSharedSource] = 1
			parts := make([]string, 0, len(values))
			for _, value := range values {
				parts = append(parts, string(value))
			}
			evidence = append(evidence, signalEvidenceHash(SemanticLinkDiscoveryMethodSharedSource, key, parts...))
		}

		if external, ok := semanticHits[key]; ok {
			matched[SemanticLinkDiscoveryMethodClaimSemanticSimilarity] = external.Score
			evidence = append(evidence, external.EvidenceHashes...)
		}
		if external, ok := ragHits[key]; ok {
			matched[SemanticLinkDiscoveryMethodRAGCoRetrieval] = external.Score
			evidence = append(evidence, external.EvidenceHashes...)
		}
		if len(matched) == 0 {
			continue
		}
		methodsForHit := make([]SemanticLinkDiscoveryMethod, 0, len(matched))
		scores := make(map[SemanticLinkDiscoveryMethod]float64, len(matched))
		for method, score := range matched {
			methodsForHit = append(methodsForHit, method)
			scores[method] = score
			hitCounts[method]++
		}
		sort.Slice(methodsForHit, func(i, j int) bool { return methodsForHit[i] < methodsForHit[j] })
		evidence = uniqueSortedHashes(evidence)
		matches = append(matches, SemanticLinkDiscoveryHit{
			// Discovery signals are symmetric observations. Persisting the pair in
			// canonical order makes replay independent of provider/input direction;
			// relation-specific direction is chosen later by the classifier.
			Source: key.Left, Target: key.Right,
			Methods: methodsForHit, EvidenceHashes: evidence, SignalScores: scores,
		})
	}

	// The input order is not a ranking contract. Keep output stable across DB/provider order.
	sort.Slice(matches, func(i, j int) bool {
		if len(matches[i].Methods) != len(matches[j].Methods) {
			return len(matches[i].Methods) > len(matches[j].Methods)
		}
		left := discoveryNodeRefKey(matches[i].Source) + "\x00" + discoveryNodeRefKey(matches[i].Target)
		right := discoveryNodeRefKey(matches[j].Source) + "\x00" + discoveryNodeRefKey(matches[j].Target)
		return left < right
	})
	perSource := make(map[knowledge.NodeRef]int)
	result.Items = make([]SemanticLinkDiscoveryHit, 0, minInt(len(matches), request.Limit))
	for _, match := range matches {
		if perSource[match.Source] >= request.PerSourceLimit || len(result.Items) >= request.Limit {
			result.Truncated = true
			continue
		}
		result.Items = append(result.Items, match)
		perSource[match.Source]++
	}
	for _, method := range methods {
		result.Signals = append(result.Signals, SemanticLinkDiscoverySignalReport{Method: method, Status: SemanticLinkDiscoverySignalStatusExecuted, HitCount: hitCounts[method], Generation: request.RuleGeneration})
	}
	result.Signals = append(result.Signals, externalSignalReport(request.Semantic, SemanticLinkDiscoveryMethodClaimSemanticSimilarity, hitCounts[SemanticLinkDiscoveryMethodClaimSemanticSimilarity]))
	result.Signals = append(result.Signals, externalSignalReport(request.RAG, SemanticLinkDiscoveryMethodRAGCoRetrieval, hitCounts[SemanticLinkDiscoveryMethodRAGCoRetrieval]))
	return result, nil
}

// ClassifySemanticLinkRuleDiscoveryHit 执行生产 Candidate writer 使用的确定性分类策略。
func ClassifySemanticLinkRuleDiscoveryHit(hit SemanticLinkDiscoveryHit) SemanticLinkRuleClassification {
	deterministic := 0
	titleAlias := false
	for _, method := range hit.Methods {
		switch method {
		case SemanticLinkDiscoveryMethodTitleAlias:
			titleAlias = true
			deterministic++
		case SemanticLinkDiscoveryMethodTermMatch,
			SemanticLinkDiscoveryMethodCommonTopic,
			SemanticLinkDiscoveryMethodSharedSource:
			deterministic++
		}
	}
	if titleAlias {
		return SemanticLinkRuleClassification{
			RelationType: knowledge.RelationDuplicates,
			Confidence:   0.95,
			Reason:       "Matching normalized statements suggest a duplicate Claim; review the evidence before approval.",
			Eligible:     true,
		}
	}
	if deterministic < 2 {
		return SemanticLinkRuleClassification{}
	}
	confidence := 0.65 + float64(deterministic-2)*0.05
	if confidence > 0.85 {
		confidence = 0.85
	}
	return SemanticLinkRuleClassification{
		RelationType: knowledge.RelationComplements,
		Confidence:   confidence,
		Reason:       "Multiple deterministic signals suggest complementary Claims; review the evidence before approval.",
		Eligible:     true,
	}
}

func validateSemanticLinkDiscoveryRequest(request SemanticLinkDiscoveryRequest) error {
	if !validDiscoveryID(request.WorkspaceID) || len(request.Pairs) > MaxSemanticLinkDiscoveryPairs || len(request.Exclusions) > MaxSemanticLinkDiscoveryPairs || request.Limit < 1 || request.Limit > MaxSemanticLinkDiscoveryLimit || request.PerSourceLimit < 1 || request.PerSourceLimit > MaxSemanticLinkDiscoveryPerSource {
		return discoveryRequestInvalid("discovery batch is outside the bounded limit")
	}
	if err := validateDiscoveryGeneration(request.RuleGeneration); err != nil {
		return err
	}
	if !pureRuleGeneration(request.RuleGeneration) {
		return discoveryRequestInvalid("deterministic discovery requires a frozen rule generation")
	}
	pairs := make(map[SemanticLinkDiscoveryPairKey]struct{}, len(request.Pairs))
	for _, pair := range request.Pairs {
		if err := ValidateSemanticLinkDiscoveryNode(request.WorkspaceID, pair.Source); err != nil {
			return err
		}
		if err := ValidateSemanticLinkDiscoveryNode(request.WorkspaceID, pair.Target); err != nil {
			return err
		}
		if pair.Source.Endpoint.Ref == pair.Target.Endpoint.Ref {
			return discoveryInvalid("discovery pair must not be self-referential")
		}
		key, err := pair.PairKey()
		if err != nil {
			return err
		}
		if _, duplicate := pairs[key]; duplicate {
			return discoveryInvalid("discovery pair list contains duplicate endpoints")
		}
		pairs[key] = struct{}{}
	}
	if err := validateExternalResult(request.Semantic, SemanticLinkDiscoveryMethodClaimSemanticSimilarity, pairs); err != nil {
		return err
	}
	if err := validateExternalResult(request.RAG, SemanticLinkDiscoveryMethodRAGCoRetrieval, pairs); err != nil {
		return err
	}
	return nil
}

func validateExternalResult(result SemanticLinkDiscoveryExternalResult, expected SemanticLinkDiscoveryMethod, pairs map[SemanticLinkDiscoveryPairKey]struct{}) error {
	if result.Method != expected {
		return discoveryInvalid("external discovery signal method is mismatched")
	}
	if !result.Supported {
		reason := strings.TrimSpace(result.UnsupportedReason)
		if reason == "" || reason != result.UnsupportedReason || len(reason) > 128 || strings.ContainsAny(reason, "\r\n") {
			return discoveryInvalid("unsupported discovery signal requires a reason")
		}
		if len(result.Hits) != 0 {
			return discoveryInvalid("unsupported discovery signal must not carry hits")
		}
		if !EqualSemanticLinkCandidateGeneration(result.Generation, SemanticLinkCandidateGeneration{}) {
			return discoveryInvalid("unsupported discovery signal must not claim a generation")
		}
		return nil
	}
	if result.UnsupportedReason != "" {
		return discoveryInvalid("supported discovery signal must not carry an unsupported reason")
	}
	if len(result.Hits) > MaxSemanticLinkDiscoveryPairs {
		return discoveryInvalid("external discovery hits exceed the bounded limit")
	}
	if err := validateDiscoveryGeneration(result.Generation); err != nil {
		return err
	}
	for _, hit := range result.Hits {
		if !validDiscoveryNodeRef(hit.Source) || !validDiscoveryNodeRef(hit.Target) || hit.Source == hit.Target || !validDiscoveryFiniteScore(hit.Score) {
			return discoveryInvalid("external discovery hit is invalid")
		}
		if len(hit.EvidenceHashes) == 0 || len(hit.EvidenceHashes) > MaxSemanticLinkDiscoveryLabels {
			return discoveryInvalid("external discovery evidence exceeds the bounded limit")
		}
		seenHashes := make(map[string]struct{}, len(hit.EvidenceHashes))
		for _, hash := range hit.EvidenceHashes {
			if !canonicalDiscoveryHash(hash) {
				return discoveryInvalid("external discovery evidence hash is invalid")
			}
			if _, duplicate := seenHashes[hash]; duplicate {
				return discoveryInvalid("external discovery evidence contains duplicate hashes")
			}
			seenHashes[hash] = struct{}{}
		}
		left, right := hit.Source, hit.Target
		if discoveryNodeRefKey(right) < discoveryNodeRefKey(left) {
			left, right = right, left
		}
		if _, requested := pairs[SemanticLinkDiscoveryPairKey{Left: left, Right: right}]; !requested {
			return discoveryInvalid("external discovery hit is not bound to the requested pairs")
		}
		if expected == SemanticLinkDiscoveryMethodClaimSemanticSimilarity && (hit.Source.Type != knowledge.NodeTypeClaim || hit.Target.Type != knowledge.NodeTypeClaim) {
			return discoveryInvalid("claim semantic signal only accepts claim pairs")
		}
	}
	return nil
}

func externalHitIndex(result SemanticLinkDiscoveryExternalResult) (map[SemanticLinkDiscoveryPairKey]SemanticLinkDiscoveryExternalHit, error) {
	index := make(map[SemanticLinkDiscoveryPairKey]SemanticLinkDiscoveryExternalHit, len(result.Hits))
	if !result.Supported {
		return index, nil
	}
	for _, hit := range result.Hits {
		left, right := hit.Source, hit.Target
		if discoveryNodeRefKey(right) < discoveryNodeRefKey(left) {
			left, right = right, left
		}
		key := SemanticLinkDiscoveryPairKey{Left: left, Right: right}
		if _, exists := index[key]; exists {
			return nil, discoveryInvalid("external discovery signal contains duplicate pairs")
		}
		if !validDiscoveryNodeRef(left) || !validDiscoveryNodeRef(right) {
			return nil, discoveryInvalid("external discovery signal pair is invalid")
		}
		index[key] = hit
	}
	return index, nil
}

func externalSignalReport(result SemanticLinkDiscoveryExternalResult, method SemanticLinkDiscoveryMethod, hitCount int) SemanticLinkDiscoverySignalReport {
	if !result.Supported {
		return SemanticLinkDiscoverySignalReport{Method: method, Status: SemanticLinkDiscoverySignalStatusUnsupported, HitCount: 0, Reason: result.UnsupportedReason, Generation: result.Generation}
	}
	return SemanticLinkDiscoverySignalReport{Method: method, Status: SemanticLinkDiscoverySignalStatusExecuted, HitCount: hitCount, Generation: result.Generation}
}

func exclusionIndex(values []SemanticLinkDiscoveryExclusion, requestedPairs map[SemanticLinkDiscoveryPairKey]struct{}) (map[SemanticLinkDiscoveryPairKey]string, error) {
	index := make(map[SemanticLinkDiscoveryPairKey]string, len(values))
	for _, value := range values {
		if !validDiscoveryNodeRef(value.Source) || !validDiscoveryNodeRef(value.Target) || value.Source == value.Target {
			return nil, discoveryInvalid("discovery exclusion endpoints are invalid")
		}
		left, right := value.Source, value.Target
		if discoveryNodeRefKey(right) < discoveryNodeRefKey(left) {
			left, right = right, left
		}
		key := SemanticLinkDiscoveryPairKey{Left: left, Right: right}
		if _, requested := requestedPairs[key]; !requested {
			return nil, discoveryInvalid("discovery exclusion is not bound to the requested pairs")
		}
		if _, exists := index[key]; exists {
			return nil, discoveryInvalid("discovery exclusions contain duplicate pairs")
		}
		reason := strings.TrimSpace(value.Reason)
		if strings.ContainsAny(reason, "\r\n") || len(reason) > 128 {
			return nil, discoveryInvalid("discovery exclusion reason is invalid")
		}
		if reason == "" {
			reason = "EXISTING_RELATION_OR_PROPOSAL"
		}
		index[key] = reason
	}
	return index, nil
}

func intersectCanonicalText(left, right []string) []string {
	leftSet := make(map[string]struct{}, len(left))
	for _, value := range left {
		if canonical := canonicalDiscoveryText(value); canonical != "" {
			leftSet[canonical] = struct{}{}
		}
	}
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, value := range right {
		canonical := canonicalDiscoveryText(value)
		if canonical == "" {
			continue
		}
		if _, ok := leftSet[canonical]; ok {
			if _, duplicate := seen[canonical]; !duplicate {
				result = append(result, canonical)
				seen[canonical] = struct{}{}
			}
		}
	}
	sort.Strings(result)
	return result
}

func intersectNodeRefs(left, right []knowledge.NodeRef) []knowledge.NodeRef {
	leftSet := make(map[knowledge.NodeRef]struct{}, len(left))
	for _, value := range left {
		leftSet[value] = struct{}{}
	}
	result := make([]knowledge.NodeRef, 0)
	seen := make(map[knowledge.NodeRef]struct{})
	for _, value := range right {
		if _, ok := leftSet[value]; ok {
			if _, duplicate := seen[value]; !duplicate {
				result = append(result, value)
				seen[value] = struct{}{}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return discoveryNodeRefKey(result[i]) < discoveryNodeRefKey(result[j]) })
	return result
}

func intersectIDs(left, right []foundation.ID) []foundation.ID {
	leftSet := make(map[foundation.ID]struct{}, len(left))
	for _, value := range left {
		leftSet[value] = struct{}{}
	}
	result := make([]foundation.ID, 0)
	seen := make(map[foundation.ID]struct{})
	for _, value := range right {
		if _, ok := leftSet[value]; ok {
			if _, duplicate := seen[value]; !duplicate {
				result = append(result, value)
				seen[value] = struct{}{}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func termMatchStrength(left, right []string) float64 {
	intersection := len(intersectCanonicalText(left, right))
	denominator := len(uniqueCanonicalText(left)) + len(uniqueCanonicalText(right))
	if denominator == 0 {
		return 0
	}
	value := float64(2*intersection) / float64(denominator)
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func uniqueCanonicalText(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		if canonical := canonicalDiscoveryText(value); canonical != "" {
			set[canonical] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func signalEvidenceHash(method SemanticLinkDiscoveryMethod, key SemanticLinkDiscoveryPairKey, values ...string) string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	payload := struct {
		Schema string                      `json:"schema"`
		Method SemanticLinkDiscoveryMethod `json:"method"`
		Left   string                      `json:"left"`
		Right  string                      `json:"right"`
		Values []string                    `json:"values"`
	}{semanticLinkDiscoveryFingerprintSchema, method, discoveryNodeRefKey(key.Left), discoveryNodeRefKey(key.Right), sorted}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func uniqueSortedHashes(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if canonicalDiscoveryHash(value) {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func canonicalDiscoveryText(value string) string {
	value = norm.NFC.String(value)
	var builder strings.Builder
	space := false
	for _, runeValue := range value {
		if unicode.IsSpace(runeValue) {
			if builder.Len() > 0 {
				space = true
			}
			continue
		}
		if unicode.IsControl(runeValue) {
			return ""
		}
		if space {
			builder.WriteByte(' ')
			space = false
		}
		builder.WriteRune(runeValue)
	}
	return cases.Fold().String(builder.String())
}

func validateDiscoveryTextSet(values []string, limit int) error {
	seen := map[string]struct{}{}
	for _, value := range values {
		canonical := canonicalDiscoveryText(value)
		if canonical == "" || len(canonical) > 512 {
			return discoveryInvalid("discovery text metadata is invalid")
		}
		if _, duplicate := seen[canonical]; duplicate {
			return discoveryInvalid("discovery text metadata contains duplicates")
		}
		seen[canonical] = struct{}{}
	}
	if len(seen) > limit {
		return discoveryInvalid("discovery text metadata exceeds the bounded limit")
	}
	return nil
}

func validateDiscoveryNodeRefs(values []knowledge.NodeRef, expected knowledge.NodeType) error {
	seen := map[knowledge.NodeRef]struct{}{}
	for _, value := range values {
		if !validDiscoveryNodeRef(value) || (expected != "" && value.Type != expected) {
			return discoveryInvalid("discovery node reference is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			return discoveryInvalid("discovery node references contain duplicates")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateDiscoveryIDs(values []foundation.ID) error {
	seen := map[foundation.ID]struct{}{}
	for _, value := range values {
		if !validDiscoveryID(value) {
			return discoveryInvalid("discovery source reference is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			return discoveryInvalid("discovery source references contain duplicates")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateDiscoveryGeneration(value SemanticLinkCandidateGeneration) error {
	// Candidate generation validation is intentionally reused so an engine result
	// can be passed into Candidate fingerprinting without inventing a second version contract.
	return validateSemanticLinkCandidateGeneration(value)
}

func pureRuleGeneration(value SemanticLinkCandidateGeneration) bool {
	return value.RuleID != nil && value.RuleVersion != "" && value.IndexVersionID == nil && value.EmbeddingVersionID == nil && value.RerankVersionID == nil && value.ModelRunID == nil && value.ModelVersion == "" && value.ModelProfileVersion == "" && value.PromptVersion == "" && value.SchemaVersion == ""
}

func validDiscoveryLifecycle(value knowledge.NodeLifecycle) bool {
	switch value {
	case knowledge.NodeLifecycleActive, knowledge.NodeLifecycleSuggested, knowledge.NodeLifecycleConfirmed, knowledge.NodeLifecycleDisputed:
		return true
	default:
		return false
	}
}

func validDiscoveryNodeRef(value knowledge.NodeRef) bool {
	return (value.Type == knowledge.NodeTypeTopic || value.Type == knowledge.NodeTypeClaim) && validDiscoveryID(value.ID)
}

func validDiscoveryID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func equalOptionalDiscoveryID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validDiscoveryFiniteScore(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func canonicalDiscoveryHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func discoveryNodeRefKey(value knowledge.NodeRef) string {
	return string(value.Type) + ":" + string(value.ID)
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func discoveryInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkDiscoveryInvalid, false, errors.New(message))
}

func discoveryRequestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkDiscoveryRequestInvalid, false, errors.New(message))
}

// Error codes used by discovery and scan contracts.
const (
	ErrorCodeSemanticLinkDiscoveryInvalid        = "GRAPH_DISCOVERY_INVALID"
	ErrorCodeSemanticLinkDiscoveryRequestInvalid = "GRAPH_DISCOVERY_REQUEST_INVALID"
	ErrorCodeSemanticLinkDiscoveryUnavailable    = "GRAPH_DISCOVERY_DEPENDENCY_UNAVAILABLE"
	ErrorCodeSemanticLinkScanInvalid             = "GRAPH_SEMANTIC_LINK_SCAN_INVALID"
	ErrorCodeSemanticLinkScanTransitionInvalid   = "GRAPH_SEMANTIC_LINK_SCAN_TRANSITION_INVALID"
	ErrorCodeSemanticLinkScanNotFound            = "GRAPH_SEMANTIC_LINK_SCAN_NOT_FOUND"
)

// SemanticLinkScanScopeType 是扫描 scope 的受控 discriminator。
type SemanticLinkScanScopeType string

const (
	SemanticLinkScanScopeNode            SemanticLinkScanScopeType = "NODE"
	SemanticLinkScanScopeTopic           SemanticLinkScanScopeType = "TOPIC"
	SemanticLinkScanScopeDirectory       SemanticLinkScanScopeType = "DIRECTORY"
	SemanticLinkScanScopeSmartCollection SemanticLinkScanScopeType = "SMART_COLLECTION"
)

// SemanticLinkScanStatus 是 durable Topic scan 的终态和运行态。
type SemanticLinkScanStatus string

const (
	SemanticLinkScanStatusPending   SemanticLinkScanStatus = "PENDING"
	SemanticLinkScanStatusRunning   SemanticLinkScanStatus = "RUNNING"
	SemanticLinkScanStatusSucceeded SemanticLinkScanStatus = "SUCCEEDED"
	SemanticLinkScanStatusFailed    SemanticLinkScanStatus = "FAILED"
	SemanticLinkScanStatusCancelled SemanticLinkScanStatus = "CANCELLED"
)

// SemanticLinkScanScope 描述稳定、版本化的扫描对象。
type SemanticLinkScanScope struct {
	Type          SemanticLinkScanScopeType
	Ref           string
	Version       int64
	SchemaVersion string
}

// SemanticLinkScanGeneration 冻结一次 scan 实际参与的规则、外部 signal 与 Workflow 版本。
// nil Semantic/RAG 表示该能力明确 unsupported，不得填入运行时 latest。
type SemanticLinkScanGeneration struct {
	Rule            SemanticLinkCandidateGeneration  `json:"rule"`
	Semantic        *SemanticLinkCandidateGeneration `json:"semantic,omitempty"`
	RAG             *SemanticLinkCandidateGeneration `json:"rag,omitempty"`
	WorkflowVersion string                           `json:"workflow_version"`
}

// SemanticLinkScanCheckpoint 是可恢复的页边界，不保存正文或模型响应。
type SemanticLinkScanCheckpoint struct {
	Cursor        string
	ProcessedPage int64
	LastNode      *knowledge.NodeRef
}

// SemanticLinkScanError 是安全、稳定的最后失败摘要。
type SemanticLinkScanError struct {
	Stage     string
	Code      string
	Retryable bool
}

// SemanticLinkScan 是异步扫描的 durable 事实投影。
type SemanticLinkScan struct {
	ID              foundation.ID
	WorkspaceID     foundation.ID
	Scope           SemanticLinkScanScope
	Fingerprint     string
	IdempotencyKey  string
	RequestHash     string
	WorkflowRunID   foundation.ID
	Status          SemanticLinkScanStatus
	TotalNodes      int64
	ProcessedNodes  int64
	CandidateCount  int64
	SuppressedCount int64
	ReopenedCount   int64
	FailedCount     int64
	Checkpoint      SemanticLinkScanCheckpoint
	LastError       *SemanticLinkScanError
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
}

// SemanticLinkScanCreate 是 state port 创建/幂等重放的输入。
type SemanticLinkScanCreate struct {
	Scan SemanticLinkScan
}

// SemanticLinkScanProgress 是单页处理后的 CAS 更新输入。
type SemanticLinkScanProgress struct {
	ScanID          foundation.ID
	WorkspaceID     foundation.ID
	ExpectedVersion int64
	Checkpoint      SemanticLinkScanCheckpoint
	ProcessedDelta  int64
	CandidateDelta  int64
	SuppressedDelta int64
	ReopenedDelta   int64
	FailedDelta     int64
}

// SemanticLinkScanTerminal 是完成/失败/取消的 CAS 更新输入。
type SemanticLinkScanTerminal struct {
	ScanID          foundation.ID
	WorkspaceID     foundation.ID
	ExpectedVersion int64
	Status          SemanticLinkScanStatus
	Error           *SemanticLinkScanError
	At              time.Time
}

// ValidateSemanticLinkScanScope 校验首版真实支持的 Node/Topic scope。
func ValidateSemanticLinkScanScope(scope SemanticLinkScanScope) error {
	if scope.Type != SemanticLinkScanScopeNode && scope.Type != SemanticLinkScanScopeTopic {
		if scope.Type == SemanticLinkScanScopeDirectory || scope.Type == SemanticLinkScanScopeSmartCollection {
			return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSemanticLinkDiscoveryUnavailable, false, errors.New("scan scope is not implemented in this release"))
		}
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan scope type is invalid"))
	}
	parsed, parseErr := foundation.ParseID(scope.Ref)
	if parseErr != nil || parsed != foundation.ID(scope.Ref) || scope.Version < 1 || strings.TrimSpace(scope.SchemaVersion) == "" || len(scope.SchemaVersion) > 128 || strings.ContainsAny(scope.SchemaVersion, "\r\n") {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan scope identity or version is invalid"))
	}
	return nil
}

// ComputeSemanticLinkScanFingerprint 绑定 Workspace、scope 和 discovery generation。
func ComputeSemanticLinkScanFingerprint(workspaceID foundation.ID, scope SemanticLinkScanScope, generation SemanticLinkScanGeneration) (string, error) {
	if !validDiscoveryID(workspaceID) {
		return "", discoveryRequestInvalid("scan workspace is invalid")
	}
	if err := ValidateSemanticLinkScanScope(scope); err != nil {
		return "", err
	}
	if err := ValidateSemanticLinkScanGeneration(generation); err != nil {
		return "", err
	}
	payload := struct {
		Schema      string                     `json:"schema"`
		WorkspaceID foundation.ID              `json:"workspace_id"`
		Scope       SemanticLinkScanScope      `json:"scope"`
		Generation  SemanticLinkScanGeneration `json:"generation"`
	}{semanticLinkScanFingerprintSchema, workspaceID, scope, generation}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkScanInvalid, false, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ValidateSemanticLinkScanGeneration 校验 scan 的组合版本绑定。
func ValidateSemanticLinkScanGeneration(generation SemanticLinkScanGeneration) error {
	if err := validateDiscoveryGeneration(generation.Rule); err != nil || !pureRuleGeneration(generation.Rule) {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan rule generation is invalid"))
	}
	for _, external := range []*SemanticLinkCandidateGeneration{generation.Semantic, generation.RAG} {
		if external != nil {
			if err := validateDiscoveryGeneration(*external); err != nil {
				return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan external generation is invalid"))
			}
		}
	}
	workflowVersion, err := knowledge.NormalizeReference(generation.WorkflowVersion, true)
	if err != nil || workflowVersion != generation.WorkflowVersion {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan workflow generation is invalid"))
	}
	return nil
}

// ValidateSemanticLinkScan 校验 durable scan 投影和进度不变量。
func ValidateSemanticLinkScan(scan SemanticLinkScan) error {
	if !validDiscoveryID(scan.ID) || !validDiscoveryID(scan.WorkspaceID) || scan.ID == scan.WorkspaceID || !validDiscoveryID(scan.WorkflowRunID) || scan.Version < 1 || scan.CreatedAt.IsZero() || scan.UpdatedAt.Before(scan.CreatedAt) || !validScanStatus(scan.Status) {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan identity or lifecycle is invalid"))
	}
	if err := ValidateSemanticLinkScanScope(scan.Scope); err != nil {
		return err
	}
	// The scan table stores the canonical generation only through the fingerprint
	// and request hash binding. The state port validates the full input at create
	// time; read projections must at least reject malformed identities.
	if !canonicalDiscoveryHash(scan.Fingerprint) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan fingerprint is invalid"))
	}
	if strings.TrimSpace(scan.IdempotencyKey) == "" || len(scan.IdempotencyKey) > 128 || strings.ContainsAny(scan.IdempotencyKey, "\r\n") || !canonicalDiscoveryHash(scan.RequestHash) {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan idempotency or request hash is invalid"))
	}
	if scan.TotalNodes < 0 || scan.ProcessedNodes < 0 || scan.CandidateCount < 0 || scan.SuppressedCount < 0 || scan.ReopenedCount < 0 || scan.FailedCount < 0 || (scan.TotalNodes > 0 && scan.ProcessedNodes > scan.TotalNodes) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan counters are invalid"))
	}
	if scan.Checkpoint.ProcessedPage < 0 || len(scan.Checkpoint.Cursor) > 512 {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan checkpoint is invalid"))
	}
	if scan.Checkpoint.LastNode != nil && !validDiscoveryNodeRef(*scan.Checkpoint.LastNode) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan checkpoint node is invalid"))
	}
	if scan.LastError != nil && (strings.TrimSpace(scan.LastError.Stage) == "" || strings.TrimSpace(scan.LastError.Code) == "" || len(scan.LastError.Stage) > 128 || len(scan.LastError.Code) > 128 || strings.ContainsAny(scan.LastError.Stage+scan.LastError.Code, "\r\n")) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan failure summary is invalid"))
	}
	if scan.Status == SemanticLinkScanStatusFailed && scan.LastError == nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkScanInvalid, false, errors.New("failed scan requires a failure summary"))
	}
	if scan.Status != SemanticLinkScanStatusFailed && scan.LastError != nil {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkScanInvalid, false, errors.New("non-failed scan cannot carry a failure summary"))
	}
	terminal := scan.Status == SemanticLinkScanStatusSucceeded || scan.Status == SemanticLinkScanStatusFailed || scan.Status == SemanticLinkScanStatusCancelled
	if terminal != (scan.CompletedAt != nil) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan completion timestamp does not match status"))
	}
	if scan.CompletedAt != nil && scan.CompletedAt.Before(scan.CreatedAt) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSemanticLinkScanInvalid, false, errors.New("scan completion time is invalid"))
	}
	return nil
}

// ValidateSemanticLinkScanTransition 校验扫描状态只能单向前进。
func ValidateSemanticLinkScanTransition(from, to SemanticLinkScanStatus) error {
	allowed := map[SemanticLinkScanStatus]map[SemanticLinkScanStatus]struct{}{
		SemanticLinkScanStatusPending: {SemanticLinkScanStatusRunning: {}, SemanticLinkScanStatusCancelled: {}, SemanticLinkScanStatusFailed: {}},
		SemanticLinkScanStatusRunning: {SemanticLinkScanStatusRunning: {}, SemanticLinkScanStatusSucceeded: {}, SemanticLinkScanStatusFailed: {}, SemanticLinkScanStatusCancelled: {}},
	}
	if _, ok := allowed[from][to]; ok {
		return nil
	}
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeSemanticLinkScanTransitionInvalid, false, errors.New("scan status transition is not allowed"))
}

func validScanStatus(status SemanticLinkScanStatus) bool {
	switch status {
	case SemanticLinkScanStatusPending, SemanticLinkScanStatusRunning, SemanticLinkScanStatusSucceeded, SemanticLinkScanStatusFailed, SemanticLinkScanStatusCancelled:
		return true
	default:
		return false
	}
}
