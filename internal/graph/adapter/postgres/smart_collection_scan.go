package postgres

import (
	"errors"
	"sort"
	"strings"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func durableScanPairKeys(pairs []collectionapp.DurableScanPair) []collectionapp.DurableScanKey {
	set := make(map[collectionapp.DurableScanKey]struct{}, len(pairs)*2)
	for _, pair := range pairs {
		set[pair.Source] = struct{}{}
		set[pair.Target] = struct{}{}
	}
	keys := make([]collectionapp.DurableScanKey, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		return keys[left].ObjectType < keys[right].ObjectType || (keys[left].ObjectType == keys[right].ObjectType && keys[left].ID < keys[right].ID)
	})
	return keys
}

type smartCollectionDiscoveryNode struct {
	node     graphdomain.SemanticLinkDiscoveryNode
	eligible bool
}

func smartCollectionDiscoveryNodes(workspaceID foundation.ID, values []collectionapp.DurableScanNode) (map[collectionapp.DurableScanKey]smartCollectionDiscoveryNode, error) {
	result := make(map[collectionapp.DurableScanKey]smartCollectionDiscoveryNode, len(values))
	for _, value := range values {
		if _, duplicate := result[value.Key]; duplicate {
			return nil, scanRepositoryConsistency(errors.New("smart collection durable node is duplicated"))
		}
		lifecycle, eligible := smartCollectionNodeLifecycle(value.Key.ObjectType, value.Status)
		entry := smartCollectionDiscoveryNode{eligible: eligible}
		if eligible {
			labels := make([]string, 0, 1+len(value.Aliases))
			labels = append(labels, boundedScanText(value.Title, 512))
			for _, alias := range value.Aliases {
				labels = append(labels, boundedScanText(alias, 512))
			}
			node := graphdomain.SemanticLinkDiscoveryNode{
				WorkspaceID: workspaceID,
				Endpoint: graphdomain.SemanticLinkCandidateEndpoint{
					Ref: knowledge.NodeRef{Type: knowledge.NodeType(value.Key.ObjectType), ID: value.Key.ID}, Version: value.Version,
					Summary: boundedScanText(value.Summary, 4096), Excerpt: boundedScanText(value.Summary, 4096),
				},
				Lifecycle: lifecycle, Labels: labels, Terms: scanTerms(value.Title + " " + value.Summary + " " + strings.Join(value.Aliases, " ")),
			}
			if node.Endpoint.Summary == "" {
				node.Endpoint.Summary = boundedScanText(value.Title, 4096)
				node.Endpoint.Excerpt = node.Endpoint.Summary
			}
			for _, topicID := range value.TopicIDs {
				node.TopicRefs = append(node.TopicRefs, knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: topicID})
			}
			node.SourceVersionIDs = append(node.SourceVersionIDs, value.SourceVersionIDs...)
			if err := graphdomain.ValidateSemanticLinkDiscoveryNode(workspaceID, node); err != nil {
				return nil, err
			}
			entry.node = node
		}
		result[value.Key] = entry
	}
	return result, nil
}

func smartCollectionNodeLifecycle(objectType, status string) (knowledge.NodeLifecycle, bool) {
	if objectType == string(knowledge.NodeTypeTopic) {
		return knowledge.NodeLifecycleActive, status == string(knowledge.TopicStatusActive)
	}
	switch status {
	case string(knowledge.ClaimStatusSuggested):
		return knowledge.NodeLifecycleSuggested, true
	case string(knowledge.ClaimStatusConfirmed):
		return knowledge.NodeLifecycleConfirmed, true
	case string(knowledge.ClaimStatusDisputed):
		return knowledge.NodeLifecycleDisputed, true
	default:
		return "", false
	}
}
