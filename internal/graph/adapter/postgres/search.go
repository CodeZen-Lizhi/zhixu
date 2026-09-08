package postgres

import (
	"errors"

	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const topicSearchSQL = `
SELECT t.id::text,t.workspace_id::text,t.name,t.description,t.status,t.version,t.updated_at,
       CASE WHEN t.normalized_name=(@p2) OR EXISTS (
           SELECT 1 FROM core.topic_alias exact_alias
           WHERE exact_alias.workspace_id=t.workspace_id AND exact_alias.topic_id=t.id AND exact_alias.normalized_alias=(@p2)
       ) THEN 0 ELSE 1 END AS match_rank
FROM core.topic t
WHERE t.workspace_id=(@p1) AND t.status='ACTIVE' AND (
    t.normalized_name LIKE (@p2) || '%' OR EXISTS (
        SELECT 1 FROM core.topic_alias prefix_alias
        WHERE prefix_alias.workspace_id=t.workspace_id AND prefix_alias.topic_id=t.id
          AND prefix_alias.normalized_alias LIKE (@p2) || '%'
    )
)
ORDER BY match_rank,t.id
LIMIT (@p3)`

const claimSearchSQL = `
SELECT c.id::text,c.workspace_id::text,c.statement,c.applicability,c.applicability_schema_version,
       c.applicability_hash,c.status,c.confidence_score,c.version,c.updated_at,
       CASE WHEN c.normalized_statement=(@p2) THEN 0 ELSE 1 END AS match_rank
FROM core.claim c
WHERE c.workspace_id=(@p1) AND c.status IN ('CONFIRMED','DISPUTED')
  AND c.normalized_statement LIKE (@p2) || '%'
ORDER BY match_rank,c.id
LIMIT (@p3)`

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
