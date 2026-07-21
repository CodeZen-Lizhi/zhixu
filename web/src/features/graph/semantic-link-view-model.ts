import type {
  RelationType,
  SemanticLinkCandidate,
  SemanticLinkCandidateStatus,
  SemanticLinkDecisionAction,
  SemanticLinkDiscoveryMethod,
} from "../../api/semantic-links";
import { graphNodeRefIdentity, graphRelationTypeCompatible, isSymmetricGraphRelationType } from "../../api/graph";

export const semanticLinkRelationTypes: readonly RelationType[] = [
  "CITES",
  "DERIVED_FROM",
  "BELONGS_TO",
  "SUPPORTS",
  "COMPLEMENTS",
  "DUPLICATES",
  "CONFLICTS_WITH",
  "PREREQUISITE_OF",
  "VERSION_OF",
  "IMPACTS",
];

export const semanticLinkStatusLabels: Record<SemanticLinkCandidateStatus, string> = {
  ACTIVE: "待处理",
  DEFERRED: "稍后处理",
  IGNORED: "已忽略",
  FALSE_POSITIVE: "已标记误报",
  PROPOSAL_CREATED: "Proposal 已创建",
  SUPERSEDED: "已被新评估替代",
};

export const semanticLinkRelationLabels: Record<RelationType, string> = {
  CITES: "引用",
  DERIVED_FROM: "派生自",
  BELONGS_TO: "归属于",
  SUPPORTS: "支持",
  COMPLEMENTS: "互补",
  DUPLICATES: "重复",
  CONFLICTS_WITH: "冲突",
  PREREQUISITE_OF: "前置条件",
  VERSION_OF: "版本关系",
  IMPACTS: "影响",
};

export const semanticLinkDiscoveryLabels: Record<SemanticLinkDiscoveryMethod, string> = {
  TITLE_ALIAS: "标题 / 别名",
  TERM_MATCH: "术语匹配",
  CLAIM_SEMANTIC_SIMILARITY: "Claim 语义相似",
  COMMON_TOPIC: "共同 Topic",
  SHARED_SOURCE: "同一来源",
  RAG_CO_RETRIEVAL: "RAG 共召回",
};

export const semanticLinkActionLabels: Record<SemanticLinkDecisionAction, string> = {
  CONFIRM: "确认建议关系",
  CONFIRM_WITH_RELATION_TYPE: "改类型后确认",
  IGNORE: "忽略",
  FALSE_POSITIVE: "标记误报",
  DEFER: "稍后处理",
  RESUME: "恢复处理",
};

export const compatibleSemanticLinkRelationTypes = (candidate: SemanticLinkCandidate): RelationType[] =>
  semanticLinkRelationTypes.filter((relationType) => {
    if (!graphRelationTypeCompatible(relationType, candidate.source.type, candidate.target.type)) return false;
    if (!isSymmetricGraphRelationType(relationType)) return true;
    return graphNodeRefIdentity(candidate.source) <= graphNodeRefIdentity(candidate.target);
  });

export const semanticLinkCandidateActions = (
  status: SemanticLinkCandidateStatus,
): SemanticLinkDecisionAction[] => {
  if (status === "ACTIVE") {
    return ["CONFIRM", "CONFIRM_WITH_RELATION_TYPE", "DEFER", "IGNORE", "FALSE_POSITIVE"];
  }
  if (status === "DEFERRED") {
    return ["CONFIRM", "CONFIRM_WITH_RELATION_TYPE", "RESUME", "IGNORE", "FALSE_POSITIVE"];
  }
  return [];
};

export interface SemanticLinkCandidateGroup {
  relationType: RelationType;
  items: SemanticLinkCandidate[];
}

export const groupSemanticLinkCandidates = (
  candidates: readonly SemanticLinkCandidate[],
): SemanticLinkCandidateGroup[] => {
  const groups = new Map<RelationType, SemanticLinkCandidate[]>();
  candidates.forEach((candidate) => {
    const items = groups.get(candidate.proposedRelationType) ?? [];
    items.push(candidate);
    groups.set(candidate.proposedRelationType, items);
  });
  return [...groups.entries()]
    .sort(([left], [right]) => semanticLinkRelationTypes.indexOf(left) - semanticLinkRelationTypes.indexOf(right))
    .map(([relationType, items]) => ({
      relationType,
      items: [...items].sort((left, right) => right.confidence - left.confidence ||
        right.updatedAt.localeCompare(left.updatedAt) || left.id.localeCompare(right.id)),
    }));
};
