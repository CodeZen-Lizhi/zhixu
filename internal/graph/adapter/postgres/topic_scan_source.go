package postgres

import (
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const semanticLinkTopicClaimPairsSQL = `
	WITH source_ids AS (
		SELECT unnest((@p3)::uuid[]) AS source_id
	)
	SELECT source_ids.source_id::text,target.target_id::text
	FROM source_ids
	CROSS JOIN LATERAL (
		SELECT scoped.source_node_id AS target_id
		FROM core.relation scoped
		WHERE scoped.workspace_id=(@p1)
		  AND scoped.source_node_type='CLAIM'
		  AND scoped.target_node_type='TOPIC' AND scoped.target_node_id=(@p2)
		  AND scoped.relation_type='BELONGS_TO' AND scoped.status='CONFIRMED'
		  AND scoped.source_node_id>source_ids.source_id
		  AND EXISTS (
			SELECT 1
			FROM core.claim claim
			WHERE claim.workspace_id=scoped.workspace_id
			  AND claim.id=scoped.source_node_id
			  AND claim.status IN ('CONFIRMED','DISPUTED')
		  )
		ORDER BY scoped.source_node_id
		LIMIT (@p4)
	) target
	ORDER BY source_ids.source_id,target.target_id`

type topicScanPairRef struct {
	sourceID foundation.ID
	targetID foundation.ID
}

func topicScanDiscoveryNode(workspaceID, claimID foundation.ID, version int64, statement, status string, topicIDs, sourceVersionIDs []string) (graphdomain.SemanticLinkDiscoveryNode, error) {
	summary := boundedScanText(statement, 4096)
	if summary == "" {
		return graphdomain.SemanticLinkDiscoveryNode{}, scanRepositoryConsistency(errors.New("semantic link claim summary is invalid"))
	}
	lifecycle := knowledge.NodeLifecycleConfirmed
	if status == string(knowledge.ClaimStatusDisputed) {
		lifecycle = knowledge.NodeLifecycleDisputed
	}
	node := graphdomain.SemanticLinkDiscoveryNode{
		WorkspaceID: workspaceID,
		Endpoint: graphdomain.SemanticLinkCandidateEndpoint{
			Ref: knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: claimID}, Version: version,
			Summary: summary, Excerpt: summary,
		},
		Lifecycle: lifecycle, Labels: []string{boundedScanText(statement, 512)}, Terms: scanTerms(statement),
	}
	for _, value := range topicIDs {
		node.TopicRefs = append(node.TopicRefs, knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: foundation.ID(value)})
	}
	for _, value := range sourceVersionIDs {
		node.SourceVersionIDs = append(node.SourceVersionIDs, foundation.ID(value))
	}
	if err := graphdomain.ValidateSemanticLinkDiscoveryNode(workspaceID, node); err != nil {
		return graphdomain.SemanticLinkDiscoveryNode{}, err
	}
	return node, nil
}

func scanTerms(value string) []string {
	set := make(map[string]struct{})
	for _, field := range strings.Fields(strings.ToLower(value)) {
		term := strings.TrimFunc(field, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) })
		if term == "" || utf8.RuneCountInString(term) > 64 {
			continue
		}
		set[term] = struct{}{}
		if len(set) == graphdomain.MaxSemanticLinkDiscoveryTerms {
			break
		}
	}
	result := make([]string, 0, len(set))
	for term := range set {
		result = append(result, term)
	}
	sort.Strings(result)
	return result
}

func boundedScanText(value string, maxBytes int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= maxBytes {
		return value
	}
	for len(value) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value)
}
