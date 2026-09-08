package postgres

import (
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const globalWindowLimit = graphapp.MaxResultWindowItems + 1

const globalClusterSQL = `
WITH eligible_claims AS (
    SELECT c.id,c.updated_at
    FROM core.claim c
    WHERE c.workspace_id=(@p1)
      AND (@p4)
      AND c.status=ANY((@p3)::text[])
      AND ((@p5)::double precision IS NULL OR (c.confidence_score IS NOT NULL AND c.confidence_score >= (@p5)))
), memberships AS (
    SELECT DISTINCT r.target_node_id AS topic_id,r.source_node_id AS claim_id,
           GREATEST(r.updated_at,c.updated_at) AS updated_at
    FROM core.relation r
    JOIN eligible_claims c ON c.id=r.source_node_id
    WHERE r.workspace_id=(@p1) AND r.source_node_type='CLAIM' AND r.target_node_type='TOPIC'
      AND r.relation_type='BELONGS_TO' AND r.status='CONFIRMED'
      AND ((@p9)::timestamptz IS NULL OR GREATEST(r.updated_at,c.updated_at) > (@p9))
), incident AS (
    SELECT DISTINCT x.topic_id,r.id AS relation_id,r.updated_at
    FROM (
        SELECT t.id AS topic_id,t.id AS node_id,'TOPIC'::text AS node_type FROM core.topic t WHERE t.workspace_id=(@p1)
        UNION ALL
        SELECT m.topic_id,m.claim_id,'CLAIM'::text FROM memberships m
    ) x
    JOIN core.relation r ON r.workspace_id=(@p1) AND (
        (r.source_node_type=x.node_type AND r.source_node_id=x.node_id) OR
        (r.target_node_type=x.node_type AND r.target_node_id=x.node_id)
    )
    WHERE r.status=ANY((@p6)::text[])
      AND (cardinality((@p7)::text[])=0 OR r.relation_type=ANY((@p7)::text[]))
      AND ((@p8)::double precision IS NULL OR (r.confidence_score IS NOT NULL AND r.confidence_score >= (@p8)))
      AND ((@p9)::timestamptz IS NULL OR r.updated_at > (@p9))
      AND (r.relation_type<>'BELONGS_TO' OR NOT (@p4) OR EXISTS (
          SELECT 1 FROM eligible_claims eligible_membership_claim
          WHERE eligible_membership_claim.id=r.source_node_id
      ))
)
SELECT t.id::text,t.workspace_id::text,t.name,t.description,t.status,t.version,t.updated_at,
       COUNT(DISTINCT m.claim_id)::int AS direct_claim_count,
       COUNT(DISTINCT i.relation_id)::int AS incident_relation_count,
       (COUNT(DISTINCT m.claim_id)+COUNT(DISTINCT i.relation_id))::int AS cluster_score,
       GREATEST(t.updated_at,COALESCE(MAX(m.updated_at),t.updated_at),COALESCE(MAX(i.updated_at),t.updated_at)) AS cluster_updated_at
FROM core.topic t
LEFT JOIN memberships m ON m.topic_id=t.id
LEFT JOIN incident i ON i.topic_id=t.id
WHERE t.workspace_id=(@p1) AND t.status='ACTIVE'
  AND (cardinality((@p2)::uuid[])=0 OR t.id=ANY((@p2)::uuid[]))
GROUP BY t.id,t.workspace_id,t.name,t.description,t.status,t.version,t.updated_at
HAVING (@p9)::timestamptz IS NULL OR GREATEST(t.updated_at,COALESCE(MAX(m.updated_at),t.updated_at),COALESCE(MAX(i.updated_at),t.updated_at)) > (@p9)
ORDER BY cluster_score DESC,cluster_updated_at DESC,t.id
LIMIT (@p10)`

func containsNodeType(values []knowledge.NodeType, target knowledge.NodeType) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
