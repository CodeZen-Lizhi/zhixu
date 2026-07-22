package postgres

import "fmt"

type scanScopeTarget uint8

const (
	detectorFindingScopeTarget scanScopeTarget = iota + 1
	issueScopeTarget
)

// scanScopePredicate 返回 detector 读取与 missing-set 共用的对象集合约束。
// alias 与参数位置只来自下方冻结枚举，外部值不会进入 SQL 标识符。
func scanScopePredicate(target scanScopeTarget) string {
	var alias, scopeTypeParameter, scopeRefParameter, topicIDsParameter, claimIDsParameter string
	switch target {
	case detectorFindingScopeTarget:
		alias, scopeTypeParameter, scopeRefParameter, topicIDsParameter, claimIDsParameter = "findings", "$2", "$3", "$6", "$7"
	case issueScopeTarget:
		alias, scopeTypeParameter, scopeRefParameter, topicIDsParameter, claimIDsParameter = "issue", "$4", "$5", "$7", "$8"
	default:
		return "FALSE"
	}
	return fmt.Sprintf(`(%[2]s='WORKSPACE' OR (
  %[2]s='TOPIC' AND (
    (%[1]s.target_type='TOPIC' AND %[1]s.target_id=%[3]s::uuid)
    OR (%[1]s.target_type='CLAIM' AND EXISTS (
      SELECT 1 FROM core.relation scoped_membership
      WHERE scoped_membership.workspace_id=%[1]s.workspace_id
        AND scoped_membership.status='CONFIRMED'
        AND scoped_membership.relation_type='BELONGS_TO'
        AND scoped_membership.source_node_type='CLAIM'
        AND scoped_membership.source_node_id=%[1]s.target_id
        AND scoped_membership.target_node_type='TOPIC'
        AND scoped_membership.target_node_id=%[3]s::uuid
    ))
    OR (%[1]s.target_type='RELATION' AND EXISTS (
      SELECT 1 FROM core.relation scoped_relation
      WHERE scoped_relation.workspace_id=%[1]s.workspace_id
        AND scoped_relation.id=%[1]s.target_id
        AND (
          (scoped_relation.source_node_type='TOPIC' AND scoped_relation.source_node_id=%[3]s::uuid)
          OR (scoped_relation.target_node_type='TOPIC' AND scoped_relation.target_node_id=%[3]s::uuid)
          OR (scoped_relation.source_node_type='CLAIM' AND EXISTS (
            SELECT 1 FROM core.relation source_membership
            WHERE source_membership.workspace_id=scoped_relation.workspace_id
              AND source_membership.status='CONFIRMED'
              AND source_membership.relation_type='BELONGS_TO'
              AND source_membership.source_node_type='CLAIM'
              AND source_membership.source_node_id=scoped_relation.source_node_id
              AND source_membership.target_node_type='TOPIC'
              AND source_membership.target_node_id=%[3]s::uuid
          ))
          OR (scoped_relation.target_node_type='CLAIM' AND EXISTS (
            SELECT 1 FROM core.relation target_membership
            WHERE target_membership.workspace_id=scoped_relation.workspace_id
              AND target_membership.status='CONFIRMED'
              AND target_membership.relation_type='BELONGS_TO'
              AND target_membership.source_node_type='CLAIM'
              AND target_membership.source_node_id=scoped_relation.target_node_id
              AND target_membership.target_node_type='TOPIC'
              AND target_membership.target_node_id=%[3]s::uuid
          ))
        )
    ))
    OR (%[1]s.target_type='CONFLICT' AND EXISTS (
      SELECT 1 FROM core.conflict_member scoped_member
      WHERE scoped_member.workspace_id=%[1]s.workspace_id
        AND scoped_member.conflict_id=%[1]s.target_id
        AND EXISTS (
          SELECT 1 FROM core.relation member_membership
          WHERE member_membership.workspace_id=scoped_member.workspace_id
            AND member_membership.status='CONFIRMED'
            AND member_membership.relation_type='BELONGS_TO'
            AND member_membership.source_node_type='CLAIM'
            AND member_membership.source_node_id=scoped_member.claim_id
            AND member_membership.target_node_type='TOPIC'
            AND member_membership.target_node_id=%[3]s::uuid
        )
    ))
  )
  OR (%[2]s='SMART_COLLECTION' AND (
    (%[1]s.target_type='TOPIC' AND %[1]s.target_id=ANY(%[4]s::uuid[]))
    OR (%[1]s.target_type='CLAIM' AND %[1]s.target_id=ANY(%[5]s::uuid[]))
    OR (%[1]s.target_type='RELATION' AND EXISTS (
      SELECT 1 FROM core.relation collection_relation
      WHERE collection_relation.workspace_id=%[1]s.workspace_id
        AND collection_relation.id=%[1]s.target_id
        AND (
          (collection_relation.source_node_type='TOPIC' AND collection_relation.source_node_id=ANY(%[4]s::uuid[]))
          OR (collection_relation.target_node_type='TOPIC' AND collection_relation.target_node_id=ANY(%[4]s::uuid[]))
          OR (collection_relation.source_node_type='CLAIM' AND collection_relation.source_node_id=ANY(%[5]s::uuid[]))
          OR (collection_relation.target_node_type='CLAIM' AND collection_relation.target_node_id=ANY(%[5]s::uuid[]))
        )
    ))
    OR (%[1]s.target_type='CONFLICT' AND EXISTS (
      SELECT 1 FROM core.conflict_member collection_member
      WHERE collection_member.workspace_id=%[1]s.workspace_id
        AND collection_member.conflict_id=%[1]s.target_id
        AND collection_member.claim_id=ANY(%[5]s::uuid[])
    ))
  ))
))`, alias, scopeTypeParameter, scopeRefParameter, topicIDsParameter, claimIDsParameter)
}
