package postgres

const topicDetailSQL = `
SELECT id::text,workspace_id::text,name,description,status,version,updated_at
FROM core.topic WHERE workspace_id=(@p1) AND id=(@p2)`

const claimDetailSQL = `
SELECT id::text,workspace_id::text,statement,applicability,applicability_schema_version,
       applicability_hash,status,confidence_score,version,updated_at
FROM core.claim WHERE workspace_id=(@p1) AND id=(@p2)`

const relationDetailSQL = `
SELECT r.id::text,r.workspace_id::text,r.source_node_type,r.source_node_id::text,
       r.target_node_type,r.target_node_id::text,r.relation_type,r.status,
       r.confirmation_method,r.confirmation_ref,r.confidence_score,r.valid_from,r.valid_to,
       r.fingerprint,r.evidence_fingerprint,r.version,r.created_at,r.updated_at,
       (SELECT COUNT(*)::int FROM core.relation_evidence e WHERE e.workspace_id=r.workspace_id AND e.relation_id=r.id)
FROM core.relation r
WHERE r.workspace_id=(@p1) AND r.id=(@p2) AND r.status IN ('CONFIRMED','STALE')`
