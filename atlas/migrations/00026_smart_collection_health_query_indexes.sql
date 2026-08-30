-- Collection predicates hydrate facts by owner and target; keep these lookups bounded
-- when a workspace contains many issues or formal relations.
CREATE INDEX IF NOT EXISTS idx_ops_health_issue_workspace_target_status_type
    ON ops.health_issue (workspace_id, target_type, target_id, status, type, updated_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_knowledge_relation_workspace_source_status_type
    ON core.relation (workspace_id, source_node_type, source_node_id, status, relation_type, id);

CREATE INDEX IF NOT EXISTS idx_knowledge_relation_workspace_target_status_type
    ON core.relation (workspace_id, target_node_type, target_node_id, status, relation_type, id);
