package postgres

import (
	"context"
	"errors"
	"sort"

	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

// SearchNodes 搜索未加载的 Active Topic 与正式 Claim 摘要。
func (repository *Repository) SearchNodes(ctx context.Context, request graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error) {
	if repository == nil || repository.db == nil {
		return graphdomain.NodeSearchResult{}, unavailable(errors.New("graph repository is unavailable"))
	}
	if err := graphdomain.ValidateNodeSearchRequest(request); err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	_, topicQuery, err := knowledge.NormalizeTopicText(request.Query)
	if err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	_, claimQuery, err := knowledge.NormalizeStatement(request.Query)
	if err != nil {
		return graphdomain.NodeSearchResult{}, err
	}
	matches := make([]graphdomain.NodeSearchMatch, 0, request.Limit*2)
	topicRows, err := repository.db.Query(ctx, topicSearchSQL, string(request.WorkspaceID), topicQuery, request.Limit)
	if err != nil {
		return graphdomain.NodeSearchResult{}, classify(err)
	}
	for topicRows.Next() {
		var rank int
		node, scanErr := scanTopicNodeWithRank(topicRows, &rank)
		if scanErr != nil {
			topicRows.Close()
			return graphdomain.NodeSearchResult{}, inconsistent(scanErr)
		}
		matches = append(matches, graphdomain.NodeSearchMatch{Kind: matchKind(rank), Node: graphdomain.GraphNode{Topic: &node}})
	}
	if err := topicRows.Err(); err != nil {
		topicRows.Close()
		return graphdomain.NodeSearchResult{}, classify(err)
	}
	topicRows.Close()

	claimRows, err := repository.db.Query(ctx, claimSearchSQL, string(request.WorkspaceID), claimQuery, request.Limit)
	if err != nil {
		return graphdomain.NodeSearchResult{}, classify(err)
	}
	for claimRows.Next() {
		var rank int
		node, scanErr := scanClaimNodeWithRank(claimRows, &rank)
		if scanErr != nil {
			claimRows.Close()
			return graphdomain.NodeSearchResult{}, inconsistent(scanErr)
		}
		matches = append(matches, graphdomain.NodeSearchMatch{Kind: matchKind(rank), Node: graphdomain.GraphNode{Claim: &node}})
	}
	if err := claimRows.Err(); err != nil {
		claimRows.Close()
		return graphdomain.NodeSearchResult{}, classify(err)
	}
	claimRows.Close()

	sort.Slice(matches, func(left, right int) bool {
		leftRank, rightRank := matchRank(matches[left].Kind), matchRank(matches[right].Kind)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftRef, rightRef := matches[left].Node.Ref(), matches[right].Node.Ref()
		if leftRef.Type != rightRef.Type {
			return leftRef.Type < rightRef.Type
		}
		return leftRef.ID < rightRef.ID
	})
	if len(matches) > request.Limit {
		matches = matches[:request.Limit]
	}
	return graphdomain.NodeSearchResult{WorkspaceID: request.WorkspaceID, Matches: matches}, nil
}

const topicSearchSQL = `
SELECT t.id::text,t.workspace_id::text,t.name,t.description,t.status,t.version,t.updated_at,
       CASE WHEN t.normalized_name=$2 OR EXISTS (
           SELECT 1 FROM core.topic_alias exact_alias
           WHERE exact_alias.workspace_id=t.workspace_id AND exact_alias.topic_id=t.id AND exact_alias.normalized_alias=$2
       ) THEN 0 ELSE 1 END AS match_rank
FROM core.topic t
WHERE t.workspace_id=$1 AND t.status='ACTIVE' AND (
    t.normalized_name LIKE $2 || '%' OR EXISTS (
        SELECT 1 FROM core.topic_alias prefix_alias
        WHERE prefix_alias.workspace_id=t.workspace_id AND prefix_alias.topic_id=t.id
          AND prefix_alias.normalized_alias LIKE $2 || '%'
    )
)
ORDER BY match_rank,t.id
LIMIT $3`

const claimSearchSQL = `
SELECT c.id::text,c.workspace_id::text,c.statement,c.applicability,c.applicability_schema_version,
       c.applicability_hash,c.status,c.confidence_score,c.version,c.updated_at,
       CASE WHEN c.normalized_statement=$2 THEN 0 ELSE 1 END AS match_rank
FROM core.claim c
WHERE c.workspace_id=$1 AND c.status IN ('CONFIRMED','DISPUTED')
  AND c.normalized_statement LIKE $2 || '%'
ORDER BY match_rank,c.id
LIMIT $3`

func scanTopicNodeWithRank(row rowScanner, rank *int) (graphdomain.TopicNode, error) {
	var node graphdomain.TopicNode
	var id, workspaceID, status string
	if err := row.Scan(&id, &workspaceID, &node.Name, &node.Description, &status, &node.Version, &node.UpdatedAt, rank); err != nil {
		return graphdomain.TopicNode{}, err
	}
	node.Ref = knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: idValue(id)}
	node.WorkspaceID = idValue(workspaceID)
	node.Status = knowledge.TopicStatus(status)
	return node, nil
}

func scanClaimNodeWithRank(row rowScanner, rank *int) (graphdomain.ClaimNode, error) {
	var node graphdomain.ClaimNode
	var id, workspaceID, status, schemaVersion, applicabilityHash string
	var applicabilityRaw []byte
	if err := row.Scan(&id, &workspaceID, &node.Statement, &applicabilityRaw, &schemaVersion, &applicabilityHash, &status, &node.Confidence, &node.Version, &node.UpdatedAt, rank); err != nil {
		return graphdomain.ClaimNode{}, err
	}
	applicability, err := knowledge.ParseApplicability(applicabilityRaw)
	if err != nil || applicability.SchemaVersion != schemaVersion || applicability.Hash != applicabilityHash {
		return graphdomain.ClaimNode{}, errors.New("claim applicability projection is inconsistent")
	}
	node.Statement = displaySummary(node.Statement, 512)
	node.Ref = knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: idValue(id)}
	node.WorkspaceID = idValue(workspaceID)
	node.Status = knowledge.ClaimStatus(status)
	node.Applicability = applicability
	return node, nil
}

func matchKind(rank int) graphdomain.NodeSearchMatchKind {
	if rank == 0 {
		return graphdomain.NodeSearchExact
	}
	return graphdomain.NodeSearchPrefix
}

func matchRank(kind graphdomain.NodeSearchMatchKind) int {
	if kind == graphdomain.NodeSearchExact {
		return 0
	}
	return 1
}
