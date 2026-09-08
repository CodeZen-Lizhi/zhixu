package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const neighborhoodFrontierSQL = `
WITH frontier AS (
    SELECT frontier_type,frontier_id::uuid
    FROM unnest((@p2)::text[],(@p3)::text[]) AS input(frontier_type,frontier_id)
), candidate AS (
    SELECT r.id,'FORWARD'::text AS traversal,r.target_node_type AS neighbor_type,r.target_node_id AS neighbor_id
    FROM frontier f
    JOIN core.relation r ON r.workspace_id=(@p1) AND r.source_node_type=f.frontier_type AND r.source_node_id=f.frontier_id
    WHERE ((@p4) IN ('BOTH','OUTBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
    UNION ALL
    SELECT r.id,'REVERSE'::text,r.source_node_type,r.source_node_id
    FROM frontier f
    JOIN core.relation r ON r.workspace_id=(@p1) AND r.target_node_type=f.frontier_type AND r.target_node_id=f.frontier_id
    WHERE ((@p4) IN ('BOTH','INBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
), eligible AS (
    SELECT DISTINCT ON (r.id) r.id,c.traversal
    FROM candidate c
    JOIN core.relation r ON r.workspace_id=(@p1) AND r.id=c.id
    WHERE (cardinality((@p5)::uuid[])=0 OR NOT r.id=ANY((@p5)::uuid[]))
      AND r.status=ANY((@p6)::text[])
      AND (cardinality((@p7)::text[])=0 OR r.relation_type=ANY((@p7)::text[]))
      AND ((@p8)::double precision IS NULL OR (r.confidence_score IS NOT NULL AND r.confidence_score >= (@p8)))
      AND ((@p9)::timestamptz IS NULL OR r.updated_at > (@p9))
      AND (cardinality((@p10)::text[])=0 OR c.neighbor_type=ANY((@p10)::text[]))
      AND (
          (c.neighbor_type='TOPIC' AND EXISTS (
              SELECT 1 FROM core.topic t WHERE t.workspace_id=(@p1) AND t.id=c.neighbor_id AND t.status='ACTIVE'
                AND (cardinality((@p11)::uuid[])=0 OR t.id=ANY((@p11)::uuid[]))
                AND ((@p9)::timestamptz IS NULL OR t.updated_at > (@p9))
          )) OR
          (c.neighbor_type='CLAIM' AND EXISTS (
              SELECT 1 FROM core.claim cl WHERE cl.workspace_id=(@p1) AND cl.id=c.neighbor_id AND cl.status=ANY((@p12)::text[])
                AND ((@p13)::double precision IS NULL OR (cl.confidence_score IS NOT NULL AND cl.confidence_score >= (@p13)))
                AND ((@p9)::timestamptz IS NULL OR cl.updated_at > (@p9))
          ))
      )
    ORDER BY r.id,c.traversal
)
SELECT r.id::text,r.workspace_id::text,r.source_node_type,r.source_node_id::text,
       r.target_node_type,r.target_node_id::text,r.relation_type,r.status,e.traversal,
       r.confidence_score,r.version,r.evidence_fingerprint,r.updated_at,
       (SELECT COUNT(*)::int FROM core.relation_evidence re WHERE re.workspace_id=r.workspace_id AND re.relation_id=r.id)
FROM eligible e JOIN core.relation r ON r.workspace_id=(@p1) AND r.id=e.id
ORDER BY r.relation_type,r.source_node_type,r.source_node_id,r.target_node_type,r.target_node_id,r.id
LIMIT (@p14)`

const neighborhoodDepthOneSQL = `
WITH candidate AS (
    SELECT r.id,'FORWARD'::text AS traversal,r.target_node_type AS neighbor_type,r.target_node_id AS neighbor_id
    FROM core.relation r
    WHERE r.workspace_id=(@p1) AND r.source_node_type=(@p2) AND r.source_node_id=(@p3)
      AND ((@p4) IN ('BOTH','OUTBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
    UNION ALL
    SELECT r.id,'REVERSE'::text,r.source_node_type,r.source_node_id
    FROM core.relation r
    WHERE r.workspace_id=(@p1) AND r.target_node_type=(@p2) AND r.target_node_id=(@p3)
      AND ((@p4) IN ('BOTH','INBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
), eligible AS (
    SELECT c.id,c.traversal,c.neighbor_type,c.neighbor_id
    FROM candidate c
    JOIN core.relation r ON r.workspace_id=(@p1) AND r.id=c.id
    WHERE r.status=ANY((@p5)::text[])
      AND (cardinality((@p6)::text[])=0 OR r.relation_type=ANY((@p6)::text[]))
      AND ((@p7)::double precision IS NULL OR (r.confidence_score IS NOT NULL AND r.confidence_score >= (@p7)))
      AND ((@p8)::timestamptz IS NULL OR r.updated_at > (@p8))
      AND (cardinality((@p9)::text[])=0 OR c.neighbor_type=ANY((@p9)::text[]))
      AND (
          (c.neighbor_type='TOPIC' AND EXISTS (
              SELECT 1 FROM core.topic t
              WHERE t.workspace_id=(@p1) AND t.id=c.neighbor_id AND t.status='ACTIVE'
                AND (cardinality((@p10)::uuid[])=0 OR t.id=ANY((@p10)::uuid[]))
                AND ((@p8)::timestamptz IS NULL OR t.updated_at > (@p8))
          )) OR
          (c.neighbor_type='CLAIM' AND EXISTS (
              SELECT 1 FROM core.claim cl
              WHERE cl.workspace_id=(@p1) AND cl.id=c.neighbor_id AND cl.status=ANY((@p11)::text[])
                AND ((@p12)::double precision IS NULL OR (cl.confidence_score IS NOT NULL AND cl.confidence_score >= (@p12)))
                AND ((@p8)::timestamptz IS NULL OR cl.updated_at > (@p8))
          ))
      )
)
SELECT r.id::text,r.workspace_id::text,r.source_node_type,r.source_node_id::text,
       r.target_node_type,r.target_node_id::text,r.relation_type,r.status,e.traversal,
       r.confidence_score,r.version,r.evidence_fingerprint,r.updated_at,
       (SELECT COUNT(*)::int FROM core.relation_evidence re WHERE re.workspace_id=r.workspace_id AND re.relation_id=r.id)
FROM eligible e
JOIN core.relation r ON r.workspace_id=(@p1) AND r.id=e.id
ORDER BY r.relation_type,r.source_node_type,r.source_node_id,r.target_node_type,r.target_node_id,r.id
LIMIT (@p13)`

func scanNeighborhoodEdge(row rowScanner) (graphdomain.GraphEdge, error) {
	var edge graphdomain.GraphEdge
	var id, workspaceID, sourceType, sourceID, targetType, targetID, relationType, status, traversal string
	var evidenceFingerprint *string
	if err := row.Scan(&id, &workspaceID, &sourceType, &sourceID, &targetType, &targetID, &relationType, &status, &traversal,
		&edge.Confidence, &edge.Version, &evidenceFingerprint, &edge.UpdatedAt, &edge.EvidenceCount); err != nil {
		return graphdomain.GraphEdge{}, err
	}
	edge.RelationID, edge.WorkspaceID = idValue(id), idValue(workspaceID)
	edge.Source = knowledge.NodeRef{Type: knowledge.NodeType(sourceType), ID: idValue(sourceID)}
	edge.Target = knowledge.NodeRef{Type: knowledge.NodeType(targetType), ID: idValue(targetID)}
	edge.Type, edge.Status, edge.Traversal = knowledge.RelationType(relationType), knowledge.RelationStatus(status), graphdomain.EdgeTraversal(traversal)
	if evidenceFingerprint != nil {
		edge.EvidenceFingerprint = *evidenceFingerprint
	}
	edge.EvidenceHref = relationEvidenceHref(edge.WorkspaceID, edge.RelationID)
	return edge, nil
}

func nodeMatchesNeighborhoodFilter(node graphdomain.GraphNode, filter graphdomain.GraphFilter) bool {
	if len(filter.NodeTypes) > 0 && !containsNodeType(filter.NodeTypes, node.Ref().Type) {
		return false
	}
	if filter.UpdatedAfter != nil {
		updatedAt := node.Topic
		if updatedAt != nil && !updatedAt.UpdatedAt.After(*filter.UpdatedAfter) {
			return false
		}
		if node.Claim != nil && !node.Claim.UpdatedAt.After(*filter.UpdatedAfter) {
			return false
		}
	}
	if node.Topic != nil {
		if node.Topic.Status != knowledge.TopicStatusActive {
			return false
		}
		return len(filter.TopicIDs) == 0 || containsIDValue(filter.TopicIDs, node.Topic.Ref.ID)
	}
	statuses := filter.ClaimStatuses
	if len(statuses) == 0 {
		statuses = []knowledge.ClaimStatus{knowledge.ClaimStatusConfirmed, knowledge.ClaimStatusDisputed}
	}
	if !containsClaimStatusValue(statuses, node.Claim.Status) {
		return false
	}
	return filter.ClaimMinConfidence == nil || node.Claim.Confidence != nil && *node.Claim.Confidence >= *filter.ClaimMinConfidence
}

func neighborhoodRefs(center knowledge.NodeRef, edges []graphdomain.GraphEdge) []knowledge.NodeRef {
	seen := map[knowledge.NodeRef]struct{}{center: {}}
	for _, edge := range edges {
		seen[edge.Source], seen[edge.Target] = struct{}{}, struct{}{}
	}
	return sortedRefs(seen)
}

func sortedRefs(seen map[knowledge.NodeRef]struct{}) []knowledge.NodeRef {
	refs := make([]knowledge.NodeRef, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Type != refs[j].Type {
			return refs[i].Type < refs[j].Type
		}
		return refs[i].ID < refs[j].ID
	})
	return refs
}

func graphNodeLess(left, right graphdomain.GraphNode) bool {
	if left.Ref().Type != right.Ref().Type {
		return left.Ref().Type < right.Ref().Type
	}
	return left.Ref().ID < right.Ref().ID
}

func graphEdgeLess(left, right graphdomain.GraphEdge) bool {
	if left.Type != right.Type {
		return left.Type < right.Type
	}
	for _, pair := range [][2]string{
		{string(left.Source.Type), string(right.Source.Type)},
		{string(left.Source.ID), string(right.Source.ID)},
		{string(left.Target.Type), string(right.Target.Type)},
		{string(left.Target.ID), string(right.Target.ID)},
		{string(left.RelationID), string(right.RelationID)},
	} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	return false
}

func neighborhoodFingerprint(nodes []graphdomain.GraphNode, edges []graphdomain.GraphEdge, boundaries []knowledge.NodeRef) (string, error) {
	payload, err := json.Marshal(struct {
		Nodes      []graphdomain.GraphNode `json:"nodes"`
		Edges      []graphdomain.GraphEdge `json:"edges"`
		Boundaries []knowledge.NodeRef     `json:"boundaries"`
	}{Nodes: nodes, Edges: edges, Boundaries: boundaries})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func containsIDValue(values []foundation.ID, target foundation.ID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsClaimStatusValue(values []knowledge.ClaimStatus, target knowledge.ClaimStatus) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
