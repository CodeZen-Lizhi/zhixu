export const synthesisId = (value: number): string => `ca000000-0000-4000-8000-${String(value).padStart(12, "0")}`;
export const synthesisWorkspaceId = synthesisId(1);
export const synthesisTime = "2026-09-09T08:00:00Z";
export const synthesisHash = "b".repeat(64);

export const synthesisSourceFixture = (workspaceId = synthesisWorkspaceId) => ({
  source: { workspace_id: workspaceId, source_id: synthesisId(2), source_version_id: synthesisId(3), content_artifact_id: synthesisId(4), parse_projection_id: synthesisId(5), content_hash: synthesisHash },
  source_span_id: synthesisId(6), excerpt_hash: "c".repeat(64), title: "缓存实践原文",
});
export const synthesisRevisionFixture = (workspaceId = synthesisWorkspaceId) => ({
  id: synthesisId(7), workspace_id: workspaceId, note_id: synthesisId(8), document_id: synthesisId(9), article_revision_id: synthesisId(10), revision_no: 1, article_revision_no: 1,
  parent_revision_id: null, title: "缓存失效策略", content_hash: synthesisHash, projection_hash: "d".repeat(64), created_at: synthesisTime,
  items: [
    { id: synthesisId(11), kind: "FACT", fact: { text: "缓存需要明确的过期策略", applicability: "高频查询", sources: [synthesisSourceFixture(workspaceId)] }, conflict: null, gap: null },
    { id: synthesisId(12), kind: "CONFLICT", fact: null, conflict: { subject: "过期时间存在不同建议", alternatives: [
      { text: "使用 30 秒过期时间", applicability: "高时效场景", sources: [synthesisSourceFixture(workspaceId)] },
      { text: "使用 60 秒过期时间", applicability: "低频读取", sources: [synthesisSourceFixture(workspaceId)] },
    ] }, gap: null },
    { id: synthesisId(13), kind: "GAP", fact: null, conflict: null, gap: { question: "缓存容量应如何确定？", context: "资料未提供容量基准", sources: [], resolution: null } },
  ],
});
export const synthesisNoteFixture = (workspaceId = synthesisWorkspaceId) => ({
  id: synthesisId(8), workspace_id: workspaceId, document_id: synthesisId(9), topic_key: "cache expiry", title: "缓存失效策略", aliases: [], current_revision_id: synthesisId(7),
  version: 2, status: "READY", workflow_run_id: synthesisId(14), failure: null, created_at: synthesisTime, updated_at: synthesisTime,
});
export const synthesisProcessingFixture = (workspaceId = synthesisWorkspaceId) => ({
  id: synthesisId(15), workspace_id: workspaceId, source_version_id: synthesisId(3), workflow_run_id: synthesisId(14), status: "FAILED", revision_ids: [],
  failure: { code: "SYNTHESIS_MODEL_CAPABILITY_UNAVAILABLE", retryable: true }, version: 1, created_at: synthesisTime, updated_at: synthesisTime, completed_at: synthesisTime,
});
export const synthesisDetailFixture = (workspaceId = synthesisWorkspaceId) => ({
  workspace_id: workspaceId, note: synthesisNoteFixture(workspaceId), current_revision: synthesisRevisionFixture(workspaceId), published_revision: synthesisRevisionFixture(workspaceId),
  publication: { revision_id: synthesisId(7), article_revision_id: synthesisId(10), proposal_id: synthesisId(16), proposal_revision_id: synthesisId(17), content_hash: synthesisHash }, latest_processing: null,
});
export const synthesisNoteRefFixture = (workspaceId = synthesisWorkspaceId) => ({
  workspace_id: workspaceId, note_id: synthesisId(8), revision_id: synthesisId(7), document_id: synthesisId(9), article_revision_id: synthesisId(10),
  revision_no: 1, article_revision_no: 1, content_hash: synthesisHash, projection_hash: "d".repeat(64), title: "缓存失效策略",
});
export const synthesisPreparationFixture = (workspaceId = synthesisWorkspaceId) => ({
  id: synthesisId(18), workspace_id: workspaceId, note_revision: synthesisNoteRefFixture(workspaceId), status: "QUEUED", workflow_run_id: synthesisId(14), session_id: null, failure: null,
  options: { role: "知识复习", difficulty: "INTERMEDIATE", duration_minutes: 30, question_count: 6, max_follow_ups: 3 }, created_at: synthesisTime, updated_at: synthesisTime,
});
export const synthesisRevisionSummaryFixture = (workspaceId = synthesisWorkspaceId) => {
  const revision = synthesisRevisionFixture(workspaceId);
  return { id: revision.id, revision_no: revision.revision_no, article_revision_id: revision.article_revision_id, article_revision_no: revision.article_revision_no, content_hash: revision.content_hash, created_at: revision.created_at };
};
