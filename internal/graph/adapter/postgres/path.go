package postgres

import (
	"errors"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

type pathChain struct {
	nodes []knowledge.NodeRef
	edges []graphdomain.GraphEdge
}

const pathFrontierSQL = `
WITH frontier AS (
    SELECT frontier_type,frontier_id::uuid
    FROM unnest((@p2)::text[],(@p3)::text[]) AS input(frontier_type,frontier_id)
), candidate AS (
    SELECT r.id,'FORWARD'::text AS traversal,r.target_node_type AS neighbor_type,r.target_node_id AS neighbor_id
    FROM frontier f JOIN core.relation r
      ON r.workspace_id=(@p1) AND r.source_node_type=f.frontier_type AND r.source_node_id=f.frontier_id
    WHERE ((@p4) IN ('BOTH','OUTBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
    UNION ALL
    SELECT r.id,'REVERSE'::text,r.source_node_type,r.source_node_id
    FROM frontier f JOIN core.relation r
      ON r.workspace_id=(@p1) AND r.target_node_type=f.frontier_type AND r.target_node_id=f.frontier_id
    WHERE ((@p4) IN ('BOTH','INBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
), eligible AS (
    SELECT DISTINCT ON (r.id) r.id,c.traversal
    FROM candidate c JOIN core.relation r ON r.workspace_id=(@p1) AND r.id=c.id
    WHERE r.status='CONFIRMED'
      AND (cardinality((@p5)::text[])=0 OR r.relation_type=ANY((@p5)::text[]))
      AND (
          (c.neighbor_type='TOPIC' AND EXISTS (
              SELECT 1 FROM core.topic t WHERE t.workspace_id=(@p1) AND t.id=c.neighbor_id AND t.status='ACTIVE'
          )) OR
          (c.neighbor_type='CLAIM' AND EXISTS (
              SELECT 1 FROM core.claim cl WHERE cl.workspace_id=(@p1) AND cl.id=c.neighbor_id AND cl.status IN ('CONFIRMED','DISPUTED')
          ))
      )
    ORDER BY r.id,c.traversal
)
SELECT r.id::text,r.source_node_type,r.source_node_id::text,r.target_node_type,r.target_node_id::text,r.relation_type,e.traversal
FROM eligible e JOIN core.relation r ON r.workspace_id=(@p1) AND r.id=e.id
ORDER BY r.relation_type,r.source_node_type,r.source_node_id,r.target_node_type,r.target_node_id,r.id
LIMIT (@p6)`

const pathEdgeHydrationSQL = `
WITH evidence_summary AS (
    SELECT relation_id,COUNT(*)::int AS evidence_count
    FROM core.relation_evidence
    WHERE workspace_id=(@p1) AND relation_id=ANY((@p2)::uuid[])
    GROUP BY relation_id
)
SELECT r.id::text,r.workspace_id::text,r.source_node_type,r.source_node_id::text,
       r.target_node_type,r.target_node_id::text,r.relation_type,r.status,r.confidence_score,
       r.version,r.evidence_fingerprint,r.updated_at,COALESCE(e.evidence_count,0)
FROM core.relation r
LEFT JOIN evidence_summary e ON e.relation_id=r.id
WHERE r.workspace_id=(@p1) AND r.id=ANY((@p2)::uuid[])`

func scanPathHydratedEdge(row rowScanner) (graphdomain.GraphEdge, error) {
	var edge graphdomain.GraphEdge
	var id, workspaceID, sourceType, sourceID, targetType, targetID, relationType, status string
	var evidenceFingerprint *string
	if err := row.Scan(&id, &workspaceID, &sourceType, &sourceID, &targetType, &targetID, &relationType, &status,
		&edge.Confidence, &edge.Version, &evidenceFingerprint, &edge.UpdatedAt, &edge.EvidenceCount); err != nil {
		return graphdomain.GraphEdge{}, err
	}
	edge.RelationID, edge.WorkspaceID = idValue(id), idValue(workspaceID)
	edge.Source = knowledge.NodeRef{Type: knowledge.NodeType(sourceType), ID: idValue(sourceID)}
	edge.Target = knowledge.NodeRef{Type: knowledge.NodeType(targetType), ID: idValue(targetID)}
	edge.Type, edge.Status = knowledge.RelationType(relationType), knowledge.RelationStatus(status)
	if evidenceFingerprint != nil {
		edge.EvidenceFingerprint = *evidenceFingerprint
	}
	edge.EvidenceHref = relationEvidenceHref(edge.WorkspaceID, edge.RelationID)
	return edge, nil
}

func mergePathLevel(known map[knowledge.NodeRef]pathChain, level map[knowledge.NodeRef]pathChain) []knowledge.NodeRef {
	frontier := make([]knowledge.NodeRef, 0, len(level))
	for ref, chain := range level {
		known[ref] = chain
		frontier = append(frontier, ref)
	}
	sort.Slice(frontier, func(i, j int) bool { return pathChainKey(level[frontier[i]]) < pathChainKey(level[frontier[j]]) })
	return frontier
}

func chooseMeetingPaths(best *pathChain, level, other map[knowledge.NodeRef]pathChain, levelIsForward bool, maxDepth int) *pathChain {
	for ref, chain := range level {
		otherChain, ok := other[ref]
		if !ok {
			continue
		}
		prefix, suffix := chain, otherChain
		if !levelIsForward {
			prefix, suffix = otherChain, chain
		}
		candidate := pathChain{
			nodes: append(append([]knowledge.NodeRef(nil), prefix.nodes...), suffix.nodes[1:]...),
			edges: append(append([]graphdomain.GraphEdge(nil), prefix.edges...), suffix.edges...),
		}
		if len(candidate.edges) > maxDepth {
			continue
		}
		if best == nil || len(candidate.edges) < len(best.edges) || len(candidate.edges) == len(best.edges) && pathChainKey(candidate) < pathChainKey(*best) {
			value := candidate
			best = &value
		}
	}
	return best
}

func pathVisitedCount(forward, backward map[knowledge.NodeRef]pathChain, pending map[knowledge.NodeRef]pathChain) int {
	seen := make(map[knowledge.NodeRef]struct{}, len(forward)+len(backward)+len(pending))
	for ref := range forward {
		seen[ref] = struct{}{}
	}
	for ref := range backward {
		seen[ref] = struct{}{}
	}
	for ref := range pending {
		seen[ref] = struct{}{}
	}
	return len(seen)
}

func pathChainKey(chain pathChain) string {
	var value strings.Builder
	for index, ref := range chain.nodes {
		value.WriteString(string(ref.Type))
		value.WriteByte(0)
		value.WriteString(string(ref.ID))
		value.WriteByte(0)
		if index < len(chain.edges) {
			value.WriteString(string(chain.edges[index].RelationID))
			value.WriteByte(0)
		}
	}
	return value.String()
}

func pathTraversalEndpoints(edge graphdomain.GraphEdge) (knowledge.NodeRef, knowledge.NodeRef) {
	if edge.Traversal == graphdomain.EdgeTraversalReverse {
		return edge.Target, edge.Source
	}
	return edge.Source, edge.Target
}

func reverseDirection(direction graphdomain.TraversalDirection) graphdomain.TraversalDirection {
	if direction == graphdomain.TraversalOutbound {
		return graphdomain.TraversalInbound
	}
	if direction == graphdomain.TraversalInbound {
		return graphdomain.TraversalOutbound
	}
	return graphdomain.TraversalBoth
}

func reverseEdgeTraversal(traversal graphdomain.EdgeTraversal) graphdomain.EdgeTraversal {
	if traversal == graphdomain.EdgeTraversalForward {
		return graphdomain.EdgeTraversalReverse
	}
	return graphdomain.EdgeTraversalForward
}

func pathBudgetExceeded() error {
	return foundation.NewError(foundation.ErrorNonRetryableFailure, graphdomain.ErrorCodeQueryBudgetExceeded, false, errors.New("graph path visit budget exceeded"))
}

const commonTopicSuggestionSQL = `
WITH from_topics AS (
    SELECT (@p3)::uuid AS topic_id WHERE (@p2)='TOPIC'
    UNION
    SELECT r.target_node_id FROM core.relation r
    WHERE (@p2)='CLAIM' AND r.workspace_id=(@p1) AND r.source_node_type='CLAIM' AND r.source_node_id=(@p3)
      AND r.target_node_type='TOPIC' AND r.relation_type='BELONGS_TO' AND r.status='CONFIRMED'
), to_topics AS (
    SELECT (@p5)::uuid AS topic_id WHERE (@p4)='TOPIC'
    UNION
    SELECT r.target_node_id FROM core.relation r
    WHERE (@p4)='CLAIM' AND r.workspace_id=(@p1) AND r.source_node_type='CLAIM' AND r.source_node_id=(@p5)
      AND r.target_node_type='TOPIC' AND r.relation_type='BELONGS_TO' AND r.status='CONFIRMED'
)
SELECT t.id::text,t.workspace_id::text,t.name,t.description,t.status,t.version,t.updated_at
FROM from_topics f JOIN to_topics target USING(topic_id)
JOIN core.topic t ON t.workspace_id=(@p1) AND t.id=f.topic_id AND t.status='ACTIVE'
ORDER BY t.id LIMIT 5`
