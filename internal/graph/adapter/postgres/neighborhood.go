package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// NeighborhoodWindow 读取中心节点的一至三跳有界邻域，并只提交完整展开层。
func (repository *Repository) NeighborhoodWindow(ctx context.Context, request graphdomain.NeighborhoodRequest) (graphdomain.Neighborhood, error) {
	if repository == nil || repository.db == nil {
		return graphdomain.Neighborhood{}, unavailable(errors.New("graph repository is unavailable"))
	}
	if err := graphdomain.ValidateNeighborhoodRequest(request); err != nil {
		return graphdomain.Neighborhood{}, err
	}
	center, err := repository.NodeDetail(ctx, request.WorkspaceID, request.Center)
	if err != nil {
		return graphdomain.Neighborhood{}, err
	}
	if !nodeMatchesNeighborhoodFilter(center, request.Filter) {
		return graphdomain.Neighborhood{}, notFound(graphdomain.ErrorCodeNodeNotFound, errors.New("graph center is outside the requested projection"))
	}
	if request.Depth > 1 {
		return repository.neighborhoodDeep(ctx, request, center)
	}

	relationStatuses := request.Filter.RelationStatuses
	if len(relationStatuses) == 0 {
		relationStatuses = []knowledge.RelationStatus{knowledge.RelationStatusConfirmed}
	}
	claimStatuses := request.Filter.ClaimStatuses
	if len(claimStatuses) == 0 {
		claimStatuses = []knowledge.ClaimStatus{knowledge.ClaimStatusConfirmed, knowledge.ClaimStatusDisputed}
	}
	probeLimit := request.MaxEdges + 1
	if probeLimit > graphapp.MaxResultWindowItems+1 {
		probeLimit = graphapp.MaxResultWindowItems + 1
	}
	rows, err := repository.db.Query(ctx, neighborhoodDepthOneSQL,
		string(request.WorkspaceID), string(request.Center.Type), string(request.Center.ID), string(request.Direction),
		stringsOf(relationStatuses), stringsOf(request.Filter.RelationTypes), request.Filter.RelationMinConfidence,
		request.Filter.UpdatedAfter, stringsOf(request.Filter.NodeTypes), ids(request.Filter.TopicIDs),
		stringsOf(claimStatuses), request.Filter.ClaimMinConfidence, probeLimit,
	)
	if err != nil {
		return graphdomain.Neighborhood{}, classify(err)
	}
	defer rows.Close()
	edges := make([]graphdomain.GraphEdge, 0, probeLimit)
	for rows.Next() {
		edge, scanErr := scanNeighborhoodEdge(rows)
		if scanErr != nil {
			return graphdomain.Neighborhood{}, inconsistent(scanErr)
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return graphdomain.Neighborhood{}, classify(err)
	}
	truncated, reason := false, ""
	windowLimit := request.MaxEdges
	if windowLimit > graphapp.MaxResultWindowItems {
		windowLimit = graphapp.MaxResultWindowItems
	}
	if len(edges) > windowLimit {
		edges = edges[:windowLimit]
		truncated = true
		if windowLimit == graphapp.MaxResultWindowItems {
			reason = "RESULT_WINDOW_LIMIT"
		} else {
			reason = "EDGE_BUDGET"
		}
	}

	refs := neighborhoodRefs(request.Center, edges)
	nodes, boundaries, err := repository.hydrateNeighborhoodNodes(ctx, request, center, refs)
	if err != nil {
		return graphdomain.Neighborhood{}, err
	}
	fingerprint, err := neighborhoodFingerprint(nodes, edges, boundaries)
	if err != nil {
		return graphdomain.Neighborhood{}, inconsistent(err)
	}
	meta := graphdomain.PageMeta{Fingerprint: fingerprint, Complete: !truncated, Truncated: truncated, Reason: reason}
	return graphdomain.Neighborhood{
		WorkspaceID: request.WorkspaceID, Center: request.Center, Nodes: nodes, Edges: edges,
		BoundaryNodes: boundaries, LayerCounts: []int{len(nodes) - 1}, CompletedDepth: 1, Meta: meta,
	}, nil
}

func (repository *Repository) neighborhoodDeep(ctx context.Context, request graphdomain.NeighborhoodRequest, center graphdomain.GraphNode) (graphdomain.Neighborhood, error) {
	result := graphdomain.Neighborhood{
		WorkspaceID: request.WorkspaceID, Center: request.Center,
		Nodes: []graphdomain.GraphNode{center}, Edges: []graphdomain.GraphEdge{}, BoundaryNodes: []knowledge.NodeRef{},
		LayerCounts: []int{},
	}
	visitedNodes := map[knowledge.NodeRef]struct{}{request.Center: {}}
	seenEdges := make(map[foundation.ID]struct{})
	frontier := []knowledge.NodeRef{request.Center}
	for depth := 1; depth <= request.Depth; depth++ {
		if len(frontier) == 0 {
			result.LayerCounts = append(result.LayerCounts, 0)
			result.CompletedDepth = depth
			continue
		}
		remainingEdges := request.MaxEdges - len(result.Edges)
		if remainingEdges < 0 {
			remainingEdges = 0
		}
		candidates, err := repository.queryFrontierEdges(ctx, request, frontier, seenEdges, remainingEdges+1)
		if err != nil {
			return graphdomain.Neighborhood{}, err
		}
		newEdges := make([]graphdomain.GraphEdge, 0, len(candidates))
		newNodeSet := make(map[knowledge.NodeRef]struct{})
		for _, edge := range candidates {
			if _, exists := seenEdges[edge.RelationID]; exists {
				continue
			}
			newEdges = append(newEdges, edge)
			for _, ref := range []knowledge.NodeRef{edge.Source, edge.Target} {
				if _, exists := visitedNodes[ref]; !exists {
					newNodeSet[ref] = struct{}{}
				}
			}
		}
		newRefs := sortedRefs(newNodeSet)
		reason := ""
		switch {
		case len(newEdges) > remainingEdges:
			reason = "EDGE_BUDGET"
		case len(result.Nodes)+len(newRefs) > request.MaxNodes:
			reason = "NODE_BUDGET"
		case len(newRefs) > request.MaxFrontier:
			reason = "FRONTIER_BUDGET"
		}
		if reason != "" {
			fingerprint, fingerprintErr := neighborhoodFingerprint(result.Nodes, result.Edges, nil)
			if fingerprintErr != nil {
				return graphdomain.Neighborhood{}, inconsistent(fingerprintErr)
			}
			result.Meta = graphdomain.PageMeta{Fingerprint: fingerprint, Truncated: true, Reason: reason}
			return result, nil
		}
		layerNodes, err := repository.hydrateExactNeighborhoodRefs(ctx, request.WorkspaceID, newRefs)
		if err != nil {
			return graphdomain.Neighborhood{}, err
		}
		result.Nodes = append(result.Nodes, layerNodes...)
		result.Edges = append(result.Edges, newEdges...)
		sort.Slice(result.Nodes, func(i, j int) bool { return graphNodeLess(result.Nodes[i], result.Nodes[j]) })
		sort.Slice(result.Edges, func(i, j int) bool { return graphEdgeLess(result.Edges[i], result.Edges[j]) })
		for _, ref := range newRefs {
			visitedNodes[ref] = struct{}{}
		}
		for _, edge := range newEdges {
			seenEdges[edge.RelationID] = struct{}{}
		}
		frontier = newRefs
		result.LayerCounts = append(result.LayerCounts, len(newRefs))
		result.CompletedDepth = depth
	}
	fingerprint, err := neighborhoodFingerprint(result.Nodes, result.Edges, nil)
	if err != nil {
		return graphdomain.Neighborhood{}, inconsistent(err)
	}
	result.Meta = graphdomain.PageMeta{Fingerprint: fingerprint, Complete: true}
	return result, nil
}

func (repository *Repository) queryFrontierEdges(ctx context.Context, request graphdomain.NeighborhoodRequest, frontier []knowledge.NodeRef, seen map[foundation.ID]struct{}, limit int) ([]graphdomain.GraphEdge, error) {
	frontierTypes := make([]string, len(frontier))
	frontierIDs := make([]string, len(frontier))
	for index, ref := range frontier {
		frontierTypes[index], frontierIDs[index] = string(ref.Type), string(ref.ID)
	}
	seenIDs := make([]foundation.ID, 0, len(seen))
	for id := range seen {
		seenIDs = append(seenIDs, id)
	}
	relationStatuses := request.Filter.RelationStatuses
	if len(relationStatuses) == 0 {
		relationStatuses = []knowledge.RelationStatus{knowledge.RelationStatusConfirmed}
	}
	claimStatuses := request.Filter.ClaimStatuses
	if len(claimStatuses) == 0 {
		claimStatuses = []knowledge.ClaimStatus{knowledge.ClaimStatusConfirmed, knowledge.ClaimStatusDisputed}
	}
	rows, err := repository.db.Query(ctx, neighborhoodFrontierSQL,
		string(request.WorkspaceID), frontierTypes, frontierIDs, string(request.Direction), ids(seenIDs),
		stringsOf(relationStatuses), stringsOf(request.Filter.RelationTypes), request.Filter.RelationMinConfidence,
		request.Filter.UpdatedAfter, stringsOf(request.Filter.NodeTypes), ids(request.Filter.TopicIDs),
		stringsOf(claimStatuses), request.Filter.ClaimMinConfidence, limit,
	)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	edges := make([]graphdomain.GraphEdge, 0, limit)
	for rows.Next() {
		edge, scanErr := scanNeighborhoodEdge(rows)
		if scanErr != nil {
			return nil, inconsistent(scanErr)
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return edges, nil
}

const neighborhoodFrontierSQL = `
WITH frontier AS (
    SELECT frontier_type,frontier_id::uuid
    FROM unnest($2::text[],$3::text[]) AS input(frontier_type,frontier_id)
), candidate AS (
    SELECT r.id,'FORWARD'::text AS traversal,r.target_node_type AS neighbor_type,r.target_node_id AS neighbor_id
    FROM frontier f
    JOIN core.relation r ON r.workspace_id=$1 AND r.source_node_type=f.frontier_type AND r.source_node_id=f.frontier_id
    WHERE ($4 IN ('BOTH','OUTBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
    UNION ALL
    SELECT r.id,'REVERSE'::text,r.source_node_type,r.source_node_id
    FROM frontier f
    JOIN core.relation r ON r.workspace_id=$1 AND r.target_node_type=f.frontier_type AND r.target_node_id=f.frontier_id
    WHERE ($4 IN ('BOTH','INBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
), eligible AS (
    SELECT DISTINCT ON (r.id) r.id,c.traversal
    FROM candidate c
    JOIN core.relation r ON r.workspace_id=$1 AND r.id=c.id
    WHERE (cardinality($5::uuid[])=0 OR NOT r.id=ANY($5::uuid[]))
      AND r.status=ANY($6::text[])
      AND (cardinality($7::text[])=0 OR r.relation_type=ANY($7::text[]))
      AND ($8::double precision IS NULL OR (r.confidence_score IS NOT NULL AND r.confidence_score >= $8))
      AND ($9::timestamptz IS NULL OR r.updated_at > $9)
      AND (cardinality($10::text[])=0 OR c.neighbor_type=ANY($10::text[]))
      AND (
          (c.neighbor_type='TOPIC' AND EXISTS (
              SELECT 1 FROM core.topic t WHERE t.workspace_id=$1 AND t.id=c.neighbor_id AND t.status='ACTIVE'
                AND (cardinality($11::uuid[])=0 OR t.id=ANY($11::uuid[]))
                AND ($9::timestamptz IS NULL OR t.updated_at > $9)
          )) OR
          (c.neighbor_type='CLAIM' AND EXISTS (
              SELECT 1 FROM core.claim cl WHERE cl.workspace_id=$1 AND cl.id=c.neighbor_id AND cl.status=ANY($12::text[])
                AND ($13::double precision IS NULL OR (cl.confidence_score IS NOT NULL AND cl.confidence_score >= $13))
                AND ($9::timestamptz IS NULL OR cl.updated_at > $9)
          ))
      )
    ORDER BY r.id,c.traversal
)
SELECT r.id::text,r.workspace_id::text,r.source_node_type,r.source_node_id::text,
       r.target_node_type,r.target_node_id::text,r.relation_type,r.status,e.traversal,
       r.confidence_score,r.version,r.evidence_fingerprint,r.updated_at,
       (SELECT COUNT(*)::int FROM core.relation_evidence re WHERE re.workspace_id=r.workspace_id AND re.relation_id=r.id)
FROM eligible e JOIN core.relation r ON r.workspace_id=$1 AND r.id=e.id
ORDER BY r.relation_type,r.source_node_type,r.source_node_id,r.target_node_type,r.target_node_id,r.id
LIMIT $14`

const neighborhoodDepthOneSQL = `
WITH candidate AS (
    SELECT r.id,'FORWARD'::text AS traversal,r.target_node_type AS neighbor_type,r.target_node_id AS neighbor_id
    FROM core.relation r
    WHERE r.workspace_id=$1 AND r.source_node_type=$2 AND r.source_node_id=$3
      AND ($4 IN ('BOTH','OUTBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
    UNION ALL
    SELECT r.id,'REVERSE'::text,r.source_node_type,r.source_node_id
    FROM core.relation r
    WHERE r.workspace_id=$1 AND r.target_node_type=$2 AND r.target_node_id=$3
      AND ($4 IN ('BOTH','INBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
), eligible AS (
    SELECT c.id,c.traversal,c.neighbor_type,c.neighbor_id
    FROM candidate c
    JOIN core.relation r ON r.workspace_id=$1 AND r.id=c.id
    WHERE r.status=ANY($5::text[])
      AND (cardinality($6::text[])=0 OR r.relation_type=ANY($6::text[]))
      AND ($7::double precision IS NULL OR (r.confidence_score IS NOT NULL AND r.confidence_score >= $7))
      AND ($8::timestamptz IS NULL OR r.updated_at > $8)
      AND (cardinality($9::text[])=0 OR c.neighbor_type=ANY($9::text[]))
      AND (
          (c.neighbor_type='TOPIC' AND EXISTS (
              SELECT 1 FROM core.topic t
              WHERE t.workspace_id=$1 AND t.id=c.neighbor_id AND t.status='ACTIVE'
                AND (cardinality($10::uuid[])=0 OR t.id=ANY($10::uuid[]))
                AND ($8::timestamptz IS NULL OR t.updated_at > $8)
          )) OR
          (c.neighbor_type='CLAIM' AND EXISTS (
              SELECT 1 FROM core.claim cl
              WHERE cl.workspace_id=$1 AND cl.id=c.neighbor_id AND cl.status=ANY($11::text[])
                AND ($12::double precision IS NULL OR (cl.confidence_score IS NOT NULL AND cl.confidence_score >= $12))
                AND ($8::timestamptz IS NULL OR cl.updated_at > $8)
          ))
      )
)
SELECT r.id::text,r.workspace_id::text,r.source_node_type,r.source_node_id::text,
       r.target_node_type,r.target_node_id::text,r.relation_type,r.status,e.traversal,
       r.confidence_score,r.version,r.evidence_fingerprint,r.updated_at,
       (SELECT COUNT(*)::int FROM core.relation_evidence re WHERE re.workspace_id=r.workspace_id AND re.relation_id=r.id)
FROM eligible e
JOIN core.relation r ON r.workspace_id=$1 AND r.id=e.id
ORDER BY r.relation_type,r.source_node_type,r.source_node_id,r.target_node_type,r.target_node_id,r.id
LIMIT $13`

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
	edge.EvidenceHref = nodeEvidenceHref(edge.RelationID)
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

func (repository *Repository) hydrateExactNeighborhoodRefs(ctx context.Context, workspaceID foundation.ID, refs []knowledge.NodeRef) ([]graphdomain.GraphNode, error) {
	nodeByRef := make(map[knowledge.NodeRef]graphdomain.GraphNode, len(refs))
	topicIDs, claimIDs := make([]foundation.ID, 0), make([]foundation.ID, 0)
	for _, ref := range refs {
		if ref.Type == knowledge.NodeTypeTopic {
			topicIDs = append(topicIDs, ref.ID)
		} else {
			claimIDs = append(claimIDs, ref.ID)
		}
	}
	if err := repository.hydrateTopics(ctx, workspaceID, topicIDs, nodeByRef); err != nil {
		return nil, err
	}
	if err := repository.hydrateClaims(ctx, workspaceID, claimIDs, nodeByRef); err != nil {
		return nil, err
	}
	nodes := make([]graphdomain.GraphNode, 0, len(refs))
	for _, ref := range refs {
		node, ok := nodeByRef[ref]
		if !ok {
			return nil, inconsistent(errors.New("graph neighborhood node disappeared during hydration"))
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (repository *Repository) hydrateNeighborhoodNodes(ctx context.Context, request graphdomain.NeighborhoodRequest, center graphdomain.GraphNode, refs []knowledge.NodeRef) ([]graphdomain.GraphNode, []knowledge.NodeRef, error) {
	topicIDs, claimIDs := make([]foundation.ID, 0), make([]foundation.ID, 0)
	for _, ref := range refs {
		if ref == request.Center {
			continue
		}
		if ref.Type == knowledge.NodeTypeTopic {
			topicIDs = append(topicIDs, ref.ID)
		} else {
			claimIDs = append(claimIDs, ref.ID)
		}
	}
	nodeByRef := map[knowledge.NodeRef]graphdomain.GraphNode{request.Center: center}
	if err := repository.hydrateTopics(ctx, request.WorkspaceID, topicIDs, nodeByRef); err != nil {
		return nil, nil, err
	}
	if err := repository.hydrateClaims(ctx, request.WorkspaceID, claimIDs, nodeByRef); err != nil {
		return nil, nil, err
	}
	nodes := make([]graphdomain.GraphNode, 0, request.MaxNodes)
	boundaries := make([]knowledge.NodeRef, 0)
	nodes = append(nodes, center)
	for _, ref := range refs {
		if ref == request.Center {
			continue
		}
		node, ok := nodeByRef[ref]
		if !ok {
			return nil, nil, inconsistent(errors.New("graph neighborhood node disappeared during hydration"))
		}
		if len(nodes) < request.MaxNodes {
			nodes = append(nodes, node)
		} else {
			boundaries = append(boundaries, ref)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return graphNodeLess(nodes[i], nodes[j]) })
	return nodes, boundaries, nil
}

func (repository *Repository) hydrateTopics(ctx context.Context, workspaceID foundation.ID, topicIDs []foundation.ID, target map[knowledge.NodeRef]graphdomain.GraphNode) error {
	if len(topicIDs) == 0 {
		return nil
	}
	rows, err := repository.db.Query(ctx, `SELECT id::text,workspace_id::text,name,description,status,version,updated_at FROM core.topic WHERE workspace_id=$1 AND id=ANY($2::uuid[])`, string(workspaceID), ids(topicIDs))
	if err != nil {
		return classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		node, scanErr := scanTopicNode(rows)
		if scanErr != nil {
			return inconsistent(scanErr)
		}
		target[node.Ref] = graphdomain.GraphNode{Topic: &node}
	}
	return classify(rows.Err())
}

func (repository *Repository) hydrateClaims(ctx context.Context, workspaceID foundation.ID, claimIDs []foundation.ID, target map[knowledge.NodeRef]graphdomain.GraphNode) error {
	if len(claimIDs) == 0 {
		return nil
	}
	rows, err := repository.db.Query(ctx, `SELECT id::text,workspace_id::text,statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,version,updated_at FROM core.claim WHERE workspace_id=$1 AND id=ANY($2::uuid[])`, string(workspaceID), ids(claimIDs))
	if err != nil {
		return classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		node, scanErr := scanClaimNode(rows)
		if scanErr != nil {
			return inconsistent(scanErr)
		}
		target[node.Ref] = graphdomain.GraphNode{Claim: &node}
	}
	return classify(rows.Err())
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
