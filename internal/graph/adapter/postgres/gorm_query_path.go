package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"gorm.io/gorm"
)

// FindPath runs endpoint validation, bidirectional BFS, suggestions and final
// hydration inside one repeatable-read snapshot.
func (repository *GORMRepository) FindPath(ctx context.Context, request graphdomain.PathRequest) (graphdomain.PathResult, error) {
	if err := repository.ready(ctx); err != nil {
		return graphdomain.PathResult{}, err
	}
	if err := graphdomain.ValidatePathRequest(request); err != nil {
		return graphdomain.PathResult{}, err
	}
	var result graphdomain.PathResult
	err := repository.gormReadSnapshot(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		path, err := gormFindPathInSnapshot(callbackCtx, transaction, request)
		if err == nil {
			result = path
		}
		return err
	})
	if err != nil {
		return graphdomain.PathResult{}, err
	}
	return result, nil
}

func gormFindPathInSnapshot(ctx context.Context, database *gorm.DB, request graphdomain.PathRequest) (graphdomain.PathResult, error) {
	for _, endpoint := range []knowledge.NodeRef{request.From, request.To} {
		node, err := gormNodeDetail(ctx, database, request.WorkspaceID, endpoint)
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
			next, err := gormExpandPathLevel(ctx, database, request, forwardFrontier, forward, false)
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
			next, err := gormExpandPathLevel(ctx, database, request, backwardFrontier, backward, true)
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
		suggestions, err := gormCommonTopicSuggestions(ctx, database, request)
		if err != nil {
			return graphdomain.PathResult{}, err
		}
		return graphdomain.PathResult{
			WorkspaceID:            request.WorkspaceID,
			From:                   request.From,
			To:                     request.To,
			Status:                 graphdomain.PathNotFound,
			ExploredNodes:          explored,
			CommonTopicSuggestions: suggestions,
		}, nil
	}
	nodes, err := gormHydrateExactNeighborhoodRefs(ctx, database, request.WorkspaceID, best.nodes)
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
	edges, err := gormHydratePathEdges(ctx, database, request.WorkspaceID, best.edges)
	if err != nil {
		return graphdomain.PathResult{}, err
	}
	return graphdomain.PathResult{
		WorkspaceID: request.WorkspaceID, From: request.From, To: request.To, Status: graphdomain.PathFound,
		Nodes: orderedNodes, Edges: edges, HopCount: len(edges), ExploredNodes: explored,
	}, nil
}

func gormExpandPathLevel(ctx context.Context, database *gorm.DB, request graphdomain.PathRequest, frontier []knowledge.NodeRef, known map[knowledge.NodeRef]pathChain, backward bool) (map[knowledge.NodeRef]pathChain, error) {
	direction := request.Direction
	if backward {
		direction = reverseDirection(direction)
	}
	query := graphdomain.NeighborhoodRequest{
		WorkspaceID: request.WorkspaceID, Center: frontier[0], Depth: 1, Limit: 1, Direction: direction,
		Filter: graphdomain.GraphFilter{RelationTypes: request.RelationTypes}, MaxNodes: graphdomain.MaxNodes,
		MaxEdges: graphdomain.MaxEdges, MaxFrontier: graphdomain.MaxNodes,
	}
	edges, err := gormQueryPathFrontier(ctx, database, query, frontier, graphdomain.MaxEdges+1)
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

func gormQueryPathFrontier(ctx context.Context, database *gorm.DB, request graphdomain.NeighborhoodRequest, frontier []knowledge.NodeRef, limit int) ([]graphdomain.GraphEdge, error) {
	frontierTypes, frontierIDs := make([]string, len(frontier)), make([]string, len(frontier))
	for index, ref := range frontier {
		frontierTypes[index], frontierIDs[index] = string(ref.Type), string(ref.ID)
	}
	rows, err := gormGraphRawRows(ctx, database, pathFrontierSQL,
		string(request.WorkspaceID), frontierTypes, frontierIDs, string(request.Direction), stringsOf(request.Filter.RelationTypes), limit,
	)
	if err != nil {
		return nil, classifyGORM(ctx, err)
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
		return nil, classifyGORM(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return nil, classifyGORM(ctx, err)
	}
	return edges, nil
}

func gormHydratePathEdges(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, pathEdges []graphdomain.GraphEdge) ([]graphdomain.GraphEdge, error) {
	idsToLoad := make([]foundation.ID, len(pathEdges))
	traversalByID := make(map[foundation.ID]graphdomain.EdgeTraversal, len(pathEdges))
	for index, edge := range pathEdges {
		idsToLoad[index], traversalByID[edge.RelationID] = edge.RelationID, edge.Traversal
	}
	rows, err := gormGraphRawRows(ctx, database, pathEdgeHydrationSQL, string(workspaceID), ids(idsToLoad))
	if err != nil {
		return nil, classifyGORM(ctx, err)
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
		return nil, classifyGORM(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return nil, classifyGORM(ctx, err)
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

func gormCommonTopicSuggestions(ctx context.Context, database *gorm.DB, request graphdomain.PathRequest) ([]graphdomain.TopicNode, error) {
	rows, err := gormGraphRawRows(ctx, database, commonTopicSuggestionSQL,
		string(request.WorkspaceID), string(request.From.Type), string(request.From.ID), string(request.To.Type), string(request.To.ID),
	)
	if err != nil {
		return nil, classifyGORM(ctx, err)
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
		return nil, classifyGORM(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return nil, classifyGORM(ctx, err)
	}
	return topics, nil
}
