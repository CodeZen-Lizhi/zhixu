package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// SemanticLinkDiscoveryCandidateWriter 将高置信度确定性 hit 写入 Candidate 表。
// 它不创建 Proposal 或正式 Relation；外部模型信号只参与发现报告，首版规则分类不冒充模型判断。
type SemanticLinkDiscoveryCandidateWriter struct {
	db         DB
	repository *Repository
	ids        foundation.IDGenerator
	clock      foundation.Clock
}

var _ graphapp.SemanticLinkDiscoveryCandidateWriter = (*SemanticLinkDiscoveryCandidateWriter)(nil)

// NewSemanticLinkDiscoveryCandidateWriter 创建 rule-only Candidate 持久化 Adapter。
func NewSemanticLinkDiscoveryCandidateWriter(db DB, repository *Repository, ids foundation.IDGenerator, clock foundation.Clock) (*SemanticLinkDiscoveryCandidateWriter, error) {
	if isNilScanDependency(db) || repository == nil || isNilScanDependency(ids) || isNilScanDependency(clock) {
		return nil, scanRepositoryUnavailable(errors.New("semantic link candidate writer dependencies are missing"))
	}
	return &SemanticLinkDiscoveryCandidateWriter{db: db, repository: repository, ids: ids, clock: clock}, nil
}

// PersistDiscoveryCandidates 只持久化规则能够安全分类的 Claim pair；单项缺少 Evidence 计为 failed。
func (writer *SemanticLinkDiscoveryCandidateWriter) PersistDiscoveryCandidates(ctx context.Context, request graphapp.SemanticLinkDiscoveryCandidateWriteRequest) (graphapp.SemanticLinkDiscoveryCandidateWriteResult, error) {
	if writer == nil || isNilScanDependency(writer.db) || writer.repository == nil {
		return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, scanRepositoryUnavailable(errors.New("semantic link candidate writer is unavailable"))
	}
	if request.WorkspaceID == "" || request.Discovery.WorkspaceID != request.WorkspaceID || !graphdomain.EqualSemanticLinkCandidateGeneration(request.Generation.Rule, semanticLinkRuleGeneration()) {
		return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, scanRepositoryInvalid(errors.New("semantic link candidate write request is invalid"))
	}
	nodes, err := discoveryPairNodeIndex(request.Pairs)
	if err != nil {
		return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
	}
	evidence, err := writer.loadClaimEvidence(ctx, request.WorkspaceID, request.Discovery.Items)
	if err != nil {
		return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
	}
	result := graphapp.SemanticLinkDiscoveryCandidateWriteResult{}
	for _, hit := range request.Discovery.Items {
		classification := graphdomain.ClassifySemanticLinkRuleDiscoveryHit(hit)
		if !classification.Eligible {
			continue
		}
		key := discoveryPairKey(hit.Source, hit.Target)
		pair, ok := nodes[key]
		if !ok || pair.Source.Endpoint.Ref.Type != knowledge.NodeTypeClaim || pair.Target.Endpoint.Ref.Type != knowledge.NodeTypeClaim {
			result.Failed++
			continue
		}
		candidateEvidence := mergeCandidateEvidence(evidence[pair.Source.Endpoint.Ref.ID], evidence[pair.Target.Endpoint.Ref.ID])
		if len(candidateEvidence) == 0 {
			result.Failed++
			continue
		}
		candidateID, err := writer.ids.New()
		if err != nil {
			return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
		}
		candidateEvidence, err = writer.reidentifyCandidateEvidence(candidateEvidence)
		if err != nil {
			return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
		}
		now := writer.clock.Now().UTC()
		if now.IsZero() {
			return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, scanRepositoryUnavailable(errors.New("semantic link candidate writer clock returned zero time"))
		}
		candidate := graphdomain.SemanticLinkCandidate{
			ID: candidateID, WorkspaceID: request.WorkspaceID,
			Source: pair.Source.Endpoint, Target: pair.Target.Endpoint,
			SuggestedRelationType: classification.RelationType, Status: graphdomain.SemanticLinkCandidateStatusActive,
			Reason: classification.Reason, Confidence: classification.Confidence,
			DiscoveryMethods: append([]graphdomain.SemanticLinkDiscoveryMethod(nil), hit.Methods...),
			Evidence:         candidateEvidence, Generation: request.Generation.Rule,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		candidate.Fingerprint, err = graphdomain.ComputeSemanticLinkCandidateFingerprint(candidate)
		if err != nil {
			return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
		}
		persisted, err := writer.repository.UpsertSemanticLinkCandidate(ctx, candidate)
		if err != nil {
			return graphapp.SemanticLinkDiscoveryCandidateWriteResult{}, err
		}
		switch {
		case persisted.Suppressed:
			result.Suppressed++
		case persisted.Reopened:
			result.Reopened++
		default:
			// Exact active replay still counts for this scan page. The scan checkpoint
			// is the once-only counter boundary, while Candidate fingerprint is the fact boundary.
			result.Created++
		}
	}
	return result, nil
}

func (writer *SemanticLinkDiscoveryCandidateWriter) reidentifyCandidateEvidence(values []graphdomain.SemanticLinkCandidateEvidence) ([]graphdomain.SemanticLinkCandidateEvidence, error) {
	result := make([]graphdomain.SemanticLinkCandidateEvidence, len(values))
	for index, value := range values {
		id, err := writer.ids.New()
		if err != nil {
			return nil, err
		}
		value.ID = id
		result[index] = value
	}
	return result, nil
}

func semanticLinkRuleGeneration() graphdomain.SemanticLinkCandidateGeneration {
	ruleID := graphapp.SemanticLinkScanRuleID
	return graphdomain.SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: graphapp.SemanticLinkScanRuleVersion}
}

func (writer *SemanticLinkDiscoveryCandidateWriter) loadClaimEvidence(ctx context.Context, workspaceID foundation.ID, hits []graphdomain.SemanticLinkDiscoveryHit) (map[foundation.ID][]graphdomain.SemanticLinkCandidateEvidence, error) {
	claimSet := make(map[foundation.ID]struct{})
	for _, hit := range hits {
		if hit.Source.Type == knowledge.NodeTypeClaim {
			claimSet[hit.Source.ID] = struct{}{}
		}
		if hit.Target.Type == knowledge.NodeTypeClaim {
			claimSet[hit.Target.ID] = struct{}{}
		}
	}
	claimIDs := make([]string, 0, len(claimSet))
	for id := range claimSet {
		claimIDs = append(claimIDs, string(id))
	}
	sort.Strings(claimIDs)
	result := make(map[foundation.ID][]graphdomain.SemanticLinkCandidateEvidence, len(claimIDs))
	if len(claimIDs) == 0 {
		return result, nil
	}
	rows, err := writer.db.Query(ctx, `
		SELECT claim_id::text,id::text,source_version_id::text,source_span_id::text,evidence_hash,reason,
			COALESCE((SELECT chunk.content FROM ingestion.canonical_chunk chunk WHERE chunk.workspace_id=$1 AND chunk.source_span_id=claim_source.source_span_id ORDER BY chunk.sequence,chunk.id LIMIT 1),reason)
		FROM core.claim_source
		WHERE workspace_id=$1 AND claim_id=ANY($2::uuid[])
		ORDER BY claim_id,evidence_hash,id`, string(workspaceID), claimIDs)
	if err != nil {
		return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_EVIDENCE_QUERY_FAILED")
	}
	defer rows.Close()
	perClaim := make(map[foundation.ID]int)
	for rows.Next() {
		var claimID, evidenceID, sourceVersionID, sourceSpanID, semanticHash, reason, excerpt string
		if err := rows.Scan(&claimID, &evidenceID, &sourceVersionID, &sourceSpanID, &semanticHash, &reason, &excerpt); err != nil {
			return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_EVIDENCE_QUERY_FAILED")
		}
		owner := foundation.ID(claimID)
		if perClaim[owner] >= 2 {
			continue
		}
		item := graphdomain.SemanticLinkCandidateEvidence{
			ID: foundation.ID(evidenceID),
			Provenance: knowledge.ProvenanceRef{
				WorkspaceID: workspaceID, SourceVersionID: foundation.ID(sourceVersionID), SourceSpanID: foundation.ID(sourceSpanID),
			},
			SemanticHash: semanticHash, Reason: boundedScanText(reason, 4096), Excerpt: boundedScanText(excerpt, 4096),
		}
		result[owner] = append(result[owner], item)
		perClaim[owner]++
	}
	if err := rows.Err(); err != nil {
		return nil, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_EVIDENCE_QUERY_FAILED")
	}
	return result, nil
}

func discoveryPairNodeIndex(pairs []graphdomain.SemanticLinkDiscoveryPair) (map[string]graphdomain.SemanticLinkDiscoveryPair, error) {
	result := make(map[string]graphdomain.SemanticLinkDiscoveryPair, len(pairs))
	for _, pair := range pairs {
		key, err := pair.PairKey()
		if err != nil {
			return nil, err
		}
		value := pair
		if value.Source.Endpoint.Ref != key.Left {
			value.Source, value.Target = value.Target, value.Source
		}
		result[discoveryPairKey(key.Left, key.Right)] = value
	}
	return result, nil
}

func discoveryPairKey(left, right knowledge.NodeRef) string {
	leftKey, rightKey := string(left.Type)+":"+string(left.ID), string(right.Type)+":"+string(right.ID)
	if rightKey < leftKey {
		leftKey, rightKey = rightKey, leftKey
	}
	return leftKey + "\x00" + rightKey
}

func mergeCandidateEvidence(groups ...[]graphdomain.SemanticLinkCandidateEvidence) []graphdomain.SemanticLinkCandidateEvidence {
	byHash := make(map[string]graphdomain.SemanticLinkCandidateEvidence)
	for _, group := range groups {
		for _, item := range group {
			if existing, ok := byHash[item.SemanticHash]; !ok || string(item.ID) < string(existing.ID) {
				byHash[item.SemanticHash] = item
			}
		}
	}
	result := make([]graphdomain.SemanticLinkCandidateEvidence, 0, len(byHash))
	for _, item := range byHash {
		if strings.TrimSpace(item.Reason) != "" && strings.TrimSpace(item.Excerpt) != "" {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SemanticHash < result[j].SemanticHash })
	if len(result) > graphdomain.MaxSemanticLinkCandidateEvidence {
		result = result[:graphdomain.MaxSemanticLinkCandidateEvidence]
	}
	return result
}
