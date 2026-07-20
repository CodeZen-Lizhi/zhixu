package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

type pathChain struct {
	nodes []knowledge.NodeRef
	edges []graphdomain.GraphEdge
}

// FindPath 使用有界批量双向 BFS 返回确定性的无权最短路径。
func (repository *Repository) FindPath(ctx context.Context, request graphdomain.PathRequest) (graphdomain.PathResult, error) {
	if repository == nil || repository.db == nil {
		return graphdomain.PathResult{}, unavailable(errors.New("graph repository is unavailable"))
	}
	if err := graphdomain.ValidatePathRequest(request); err != nil {
		return graphdomain.PathResult{}, err
	}
	beginner, ok := repository.db.(interface {
		BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	})
	if !ok {
		return graphdomain.PathResult{}, unavailable(errors.New("graph path repeatable-read snapshot is unavailable"))
	}
	tx, err := beginner.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return graphdomain.PathResult{}, classify(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := (&Repository{db: tx}).findPathInSnapshot(ctx, request)
	if err != nil {
		return graphdomain.PathResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return graphdomain.PathResult{}, classify(err)
	}
	return result, nil
}

func (repository *Repository) findPathInSnapshot(ctx context.Context, request graphdomain.PathRequest) (graphdomain.PathResult, error) {
	for _, endpoint := range []knowledge.NodeRef{request.From, request.To} {
		node, err := repository.NodeDetail(ctx, request.WorkspaceID, endpoint)
		if err != nil {
			return graphdomain.PathResult{}, err
		}
		if !nodeMatchesNeighborhoodFilter(node, graphdomain.GraphFilter{}) {
			return graphdomain.PathResult{}, notFound(graphdomain.ErrorCodeNodeNotFound, errors.New("graph path endpoint is outside the formal projection"))
		}
	}

	forward := map[knowledge.NodeRef]pathChain{request.From: {nodes: []knowledge.NodeRef{request.From}}}
	backward := map[knowledge.NodeRef]pathChain{request.To: {nodes: []knowledge.NodeRef{request.To}}}
	forwardFrontier, backwardFrontier := []knowledge.NodeRef{request.From}, []knowledge.NodeRef{request.To}
	forwardDepth, backwardDepth := 0, 0
	var best *pathChain
	for forwardDepth+backwardDepth < request.MaxDepth {
		if best != nil && forwardDepth+backwardDepth >= len(best.edges) {
			break
		}
		if len(forwardFrontier) == 0 && len(backwardFrontier) == 0 {
			break
		}
		expandForward := len(backwardFrontier) == 0 || len(forwardFrontier) > 0 && len(forwardFrontier) <= len(backwardFrontier)
		if expandForward {
			next, err := repository.expandPathLevel(ctx, request, forwardFrontier, forward, false)
			if err != nil {
				return graphdomain.PathResult{}, err
			}
			if pathVisitedCount(forward, backward, next) > request.MaxVisited {
				return graphdomain.PathResult{}, pathBudgetExceeded()
			}
			forwardDepth++
			forwardFrontier = mergePathLevel(forward, next)
			best = chooseMeetingPaths(best, next, backward, true, request.MaxDepth)
		} else {
			next, err := repository.expandPathLevel(ctx, request, backwardFrontier, backward, true)
			if err != nil {
				return graphdomain.PathResult{}, err
			}
			if pathVisitedCount(forward, backward, next) > request.MaxVisited {
				return graphdomain.PathResult{}, pathBudgetExceeded()
			}
			backwardDepth++
			backwardFrontier = mergePathLevel(backward, next)
			best = chooseMeetingPaths(best, next, forward, false, request.MaxDepth)
		}
	}
	explored := pathVisitedCount(forward, backward, nil)
	if best == nil {
		suggestions, err := repository.commonTopicSuggestions(ctx, request)
		if err != nil {
			return graphdomain.PathResult{}, err
		}
		return graphdomain.PathResult{WorkspaceID: request.WorkspaceID, From: request.From, To: request.To, Status: graphdomain.PathNotFound, ExploredNodes: explored, CommonTopicSuggestions: suggestions}, nil
	}
	nodes, err := repository.hydrateExactNeighborhoodRefs(ctx, request.WorkspaceID, best.nodes)
	if err != nil {
		return graphdomain.PathResult{}, err
	}
	nodeByRef := make(map[knowledge.NodeRef]graphdomain.GraphNode, len(nodes))
	for _, node := range nodes {
		nodeByRef[node.Ref()] = node
	}
	orderedNodes := make([]graphdomain.GraphNode, len(best.nodes))
	for index, ref := range best.nodes {
		node, ok := nodeByRef[ref]
		if !ok {
			return graphdomain.PathResult{}, inconsistent(errors.New("graph path node disappeared during hydration"))
		}
		orderedNodes[index] = node
	}
	edges, err := repository.hydratePathEdges(ctx, request.WorkspaceID, best.edges)
	if err != nil {
		return graphdomain.PathResult{}, err
	}
	return graphdomain.PathResult{
		WorkspaceID: request.WorkspaceID, From: request.From, To: request.To, Status: graphdomain.PathFound,
		Nodes: orderedNodes, Edges: edges, HopCount: len(edges), ExploredNodes: explored,
	}, nil
}

func (repository *Repository) expandPathLevel(ctx context.Context, request graphdomain.PathRequest, frontier []knowledge.NodeRef, known map[knowledge.NodeRef]pathChain, backward bool) (map[knowledge.NodeRef]pathChain, error) {
	direction := request.Direction
	if backward {
		direction = reverseDirection(direction)
	}
	query := graphdomain.NeighborhoodRequest{
		WorkspaceID: request.WorkspaceID, Center: frontier[0], Depth: 1, Limit: 1, Direction: direction,
		Filter: graphdomain.GraphFilter{RelationTypes: request.RelationTypes}, MaxNodes: graphdomain.MaxNodes,
		MaxEdges: graphdomain.MaxEdges, MaxFrontier: graphdomain.MaxNodes,
	}
	edges, err := repository.queryPathFrontier(ctx, query, frontier, graphdomain.MaxEdges+1)
	if err != nil {
		return nil, err
	}
	if len(edges) > graphdomain.MaxEdges {
		return nil, pathBudgetExceeded()
	}
	frontierSet := make(map[knowledge.NodeRef]struct{}, len(frontier))
	for _, ref := range frontier {
		frontierSet[ref] = struct{}{}
	}
	next := make(map[knowledge.NodeRef]pathChain)
	for _, edge := range edges {
		from, to := pathTraversalEndpoints(edge)
		if _, ok := frontierSet[from]; !ok {
			continue
		}
		if _, visited := known[to]; visited {
			continue
		}
		base := known[from]
		candidate := pathChain{}
		if backward {
			forwardEdge := edge
			forwardEdge.Traversal = reverseEdgeTraversal(edge.Traversal)
			candidate.nodes = append([]knowledge.NodeRef{to}, base.nodes...)
			candidate.edges = append([]graphdomain.GraphEdge{forwardEdge}, base.edges...)
		} else {
			candidate.nodes = append(append([]knowledge.NodeRef(nil), base.nodes...), to)
			candidate.edges = append(append([]graphdomain.GraphEdge(nil), base.edges...), edge)
		}
		if current, exists := next[to]; !exists || pathChainKey(candidate) < pathChainKey(current) {
			next[to] = candidate
		}
	}
	return next, nil
}

func (repository *Repository) queryPathFrontier(ctx context.Context, request graphdomain.NeighborhoodRequest, frontier []knowledge.NodeRef, limit int) ([]graphdomain.GraphEdge, error) {
	frontierTypes, frontierIDs := make([]string, len(frontier)), make([]string, len(frontier))
	for index, ref := range frontier {
		frontierTypes[index], frontierIDs[index] = string(ref.Type), string(ref.ID)
	}
	rows, err := repository.db.Query(ctx, pathFrontierSQL,
		string(request.WorkspaceID), frontierTypes, frontierIDs, string(request.Direction), stringsOf(request.Filter.RelationTypes), limit,
	)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	edges := make([]graphdomain.GraphEdge, 0, limit)
	for rows.Next() {
		var edge graphdomain.GraphEdge
		var id, sourceType, sourceID, targetType, targetID, relationType, traversal string
		if err := rows.Scan(&id, &sourceType, &sourceID, &targetType, &targetID, &relationType, &traversal); err != nil {
			return nil, inconsistent(err)
		}
		edge.RelationID = idValue(id)
		edge.Source = knowledge.NodeRef{Type: knowledge.NodeType(sourceType), ID: idValue(sourceID)}
		edge.Target = knowledge.NodeRef{Type: knowledge.NodeType(targetType), ID: idValue(targetID)}
		edge.Type, edge.Traversal = knowledge.RelationType(relationType), graphdomain.EdgeTraversal(traversal)
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return edges, nil
}

const pathFrontierSQL = `
WITH frontier AS (
    SELECT frontier_type,frontier_id::uuid
    FROM unnest($2::text[],$3::text[]) AS input(frontier_type,frontier_id)
), candidate AS (
    SELECT r.id,'FORWARD'::text AS traversal,r.target_node_type AS neighbor_type,r.target_node_id AS neighbor_id
    FROM frontier f JOIN core.relation r
      ON r.workspace_id=$1 AND r.source_node_type=f.frontier_type AND r.source_node_id=f.frontier_id
    WHERE ($4 IN ('BOTH','OUTBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
    UNION ALL
    SELECT r.id,'REVERSE'::text,r.source_node_type,r.source_node_id
    FROM frontier f JOIN core.relation r
      ON r.workspace_id=$1 AND r.target_node_type=f.frontier_type AND r.target_node_id=f.frontier_id
    WHERE ($4 IN ('BOTH','INBOUND') OR r.relation_type IN ('DUPLICATES','CONFLICTS_WITH'))
), eligible AS (
    SELECT DISTINCT ON (r.id) r.id,c.traversal
    FROM candidate c JOIN core.relation r ON r.workspace_id=$1 AND r.id=c.id
    WHERE r.status='CONFIRMED'
      AND (cardinality($5::text[])=0 OR r.relation_type=ANY($5::text[]))
      AND (
          (c.neighbor_type='TOPIC' AND EXISTS (
              SELECT 1 FROM core.topic t WHERE t.workspace_id=$1 AND t.id=c.neighbor_id AND t.status='ACTIVE'
          )) OR
          (c.neighbor_type='CLAIM' AND EXISTS (
              SELECT 1 FROM core.claim cl WHERE cl.workspace_id=$1 AND cl.id=c.neighbor_id AND cl.status IN ('CONFIRMED','DISPUTED')
          ))
      )
    ORDER BY r.id,c.traversal
)
SELECT r.id::text,r.source_node_type,r.source_node_id::text,r.target_node_type,r.target_node_id::text,r.relation_type,e.traversal
FROM eligible e JOIN core.relation r ON r.workspace_id=$1 AND r.id=e.id
ORDER BY r.relation_type,r.source_node_type,r.source_node_id,r.target_node_type,r.target_node_id,r.id
LIMIT $6`

func (repository *Repository) hydratePathEdges(ctx context.Context, workspaceID foundation.ID, pathEdges []graphdomain.GraphEdge) ([]graphdomain.GraphEdge, error) {
	idsToLoad := make([]foundation.ID, len(pathEdges))
	traversalByID := make(map[foundation.ID]graphdomain.EdgeTraversal, len(pathEdges))
	for index, edge := range pathEdges {
		idsToLoad[index], traversalByID[edge.RelationID] = edge.RelationID, edge.Traversal
	}
	rows, err := repository.db.Query(ctx, pathEdgeHydrationSQL, string(workspaceID), ids(idsToLoad))
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	byID := make(map[foundation.ID]graphdomain.GraphEdge, len(pathEdges))
	for rows.Next() {
		edge, scanErr := scanPathHydratedEdge(rows)
		if scanErr != nil {
			return nil, inconsistent(scanErr)
		}
		edge.Traversal = traversalByID[edge.RelationID]
		byID[edge.RelationID] = edge
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	result := make([]graphdomain.GraphEdge, len(pathEdges))
	for index, edge := range pathEdges {
		hydrated, ok := byID[edge.RelationID]
		if !ok {
			return nil, inconsistent(errors.New("graph path relation disappeared during hydration"))
		}
		result[index] = hydrated
	}
	return result, nil
}

const pathEdgeHydrationSQL = `
WITH evidence_summary AS (
    SELECT relation_id,COUNT(*)::int AS evidence_count
    FROM core.relation_evidence
    WHERE workspace_id=$1 AND relation_id=ANY($2::uuid[])
    GROUP BY relation_id
)
SELECT r.id::text,r.workspace_id::text,r.source_node_type,r.source_node_id::text,
       r.target_node_type,r.target_node_id::text,r.relation_type,r.status,r.confidence_score,
       r.version,r.evidence_fingerprint,r.updated_at,COALESCE(e.evidence_count,0)
FROM core.relation r
LEFT JOIN evidence_summary e ON e.relation_id=r.id
WHERE r.workspace_id=$1 AND r.id=ANY($2::uuid[])`

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
	edge.EvidenceHref = nodeEvidenceHref(edge.RelationID)
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

func (repository *Repository) commonTopicSuggestions(ctx context.Context, request graphdomain.PathRequest) ([]graphdomain.TopicNode, error) {
	rows, err := repository.db.Query(ctx, commonTopicSuggestionSQL,
		string(request.WorkspaceID), string(request.From.Type), string(request.From.ID), string(request.To.Type), string(request.To.ID),
	)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	topics := make([]graphdomain.TopicNode, 0, 5)
	for rows.Next() {
		node, scanErr := scanTopicNode(rows)
		if scanErr != nil {
			return nil, inconsistent(scanErr)
		}
		topics = append(topics, node)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return topics, nil
}

const commonTopicSuggestionSQL = `
WITH from_topics AS (
    SELECT $3::uuid AS topic_id WHERE $2='TOPIC'
    UNION
    SELECT r.target_node_id FROM core.relation r
    WHERE $2='CLAIM' AND r.workspace_id=$1 AND r.source_node_type='CLAIM' AND r.source_node_id=$3
      AND r.target_node_type='TOPIC' AND r.relation_type='BELONGS_TO' AND r.status='CONFIRMED'
), to_topics AS (
    SELECT $5::uuid AS topic_id WHERE $4='TOPIC'
    UNION
    SELECT r.target_node_id FROM core.relation r
    WHERE $4='CLAIM' AND r.workspace_id=$1 AND r.source_node_type='CLAIM' AND r.source_node_id=$5
      AND r.target_node_type='TOPIC' AND r.relation_type='BELONGS_TO' AND r.status='CONFIRMED'
)
SELECT t.id::text,t.workspace_id::text,t.name,t.description,t.status,t.version,t.updated_at
FROM from_topics f JOIN to_topics target USING(topic_id)
JOIN core.topic t ON t.workspace_id=$1 AND t.id=f.topic_id AND t.status='ACTIVE'
ORDER BY t.id LIMIT 5`
