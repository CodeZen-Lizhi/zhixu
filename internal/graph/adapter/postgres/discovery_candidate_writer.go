package postgres

import (
	"sort"
	"strings"

	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func semanticLinkRuleGeneration() graphdomain.SemanticLinkCandidateGeneration {
	ruleID := graphapp.SemanticLinkScanRuleID
	return graphdomain.SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: graphapp.SemanticLinkScanRuleVersion}
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
