import { readFileSync, writeFileSync } from "node:fs";

const root = new URL(".", import.meta.url);
const openapiPath = new URL("./openapi.json", root);
const manifestPath = new URL("./operation-tags.json", root);

export const TAG_CATALOG = [
  ["Auth", "Authentication sessions and API token lifecycle."],
  ["Workspaces", "Workspace activation, metadata, and workspace scans."],
  ["System", "Public system capability and runtime status."],
  ["Health", "Liveness, readiness, and knowledge health operations."],
  ["ModelSettings", "Managed model settings and activation operations."],
  ["Captures", "Capture creation, upload, retrieval, and retry operations."],
  ["Authoring", "Working drafts, authoring projections, and publication commands."],
  ["DocumentHistory", "Document history, comparison, and restore operations."],
  ["SourceSpans", "Source versions, ingestion, knowledge profiles, and evidence spans."],
  ["Organizing", "Material organizing drafts, templates, snapshots, and runs."],
  ["Synthesis", "Continuously evolving synthesis notes, exact sources, and durable processing."],
  ["GitSync", "Git remote configuration and synchronization operations."],
  ["Business", "Workflow, proposal, approval, and change-control operations."],
  ["BusinessRevisions", "Proposal current-content and revision-history operations."],
  ["Conversation", "Conversation, question, answer, and feedback operations."],
  ["Timeline", "Knowledge timeline and impact-analysis operations."],
  ["Search", "Knowledge search operations."],
  ["Graph", "Canonical graph query and evidence operations."],
  ["Collections", "Collection definition, validation, and result operations."],
  ["SemanticLinks", "Semantic-link candidate and scan operations."],
  ["Review", "Review decks, cards, sessions, and learning-path operations."],
  ["Interview", "Interview sessions, turns, and interview learning-path operations."],
  ["Memory", "Memory candidate and lifecycle operations."],
  ["Artifacts", "Artifact planning, authoring, generation, and publication operations."],
  ["Exports", "Collection and artifact export job operations."],
  ["AttachmentExports", "Workspace attachment ZIP export operations."],
  ["Events", "Server event stream operations."],
];

const CATALOG_NAMES = new Set(TAG_CATALOG.map(([name]) => name));

// This is intentionally an operationId decision table rather than a path-prefix guess.
// New operations must be assigned explicitly and will fail the verifier until reviewed.
const EXACT_TAGS = new Map([
  ["submitQuestionV2", "Conversation"],
  ["listConversationTurnsV2", "Conversation"],
  ["getAnswerV2", "Conversation"],
  ["getWorkspaceAnalysisTimelineV2", "Timeline"],
  ["listInterviewsV2", "Interview"],
  ["startInterviewV2", "Interview"],
  ["getInterviewV2", "Interview"],
  ["submitInterviewTurnV2", "Interview"],
  ["completeInterviewV2", "Interview"],
  ["updateLearningPathStepV2", "Review"],
  ["listSynthesisNotes", "Synthesis"],
  ["getSynthesisNote", "Synthesis"],
  ["listSynthesisRevisions", "Synthesis"],
  ["getSynthesisRevision", "Synthesis"],
  ["openSynthesisSource", "Synthesis"],
  ["listSynthesisProcessing", "Synthesis"],
  ["getSynthesisProcessing", "Synthesis"],
  ["retrySynthesisProcessing", "Synthesis"],
  ["prepareSynthesisNoteInterview", "Interview"],
  ["listSynthesisNoteInterviewPreparations", "Interview"],
  ["getSynthesisNoteInterviewPreparation", "Interview"],
  ["retrySynthesisNoteInterviewPreparation", "Interview"],
  ["getLiveness", "Health"],
  ["getReadiness", "Health"],
  ["getSystemStatus", "System"],
  ["getModelSettings", "ModelSettings"],
  ["updateModelSettings", "ModelSettings"],
  ["startModelSettingsActivation", "ModelSettings"],
  ["testModelSettings", "ModelSettings"],
  ["createWorkspace", "Workspaces"],
  ["getActiveWorkspace", "Workspaces"],
  ["getWorkspace", "Workspaces"],
  ["scanWorkspace", "Workspaces"],
  ["createCapture", "Captures"],
  ["listCaptures", "Captures"],
  ["uploadCapture", "Captures"],
  ["getCapture", "Captures"],
  ["retryCapture", "Captures"],
  ["createWorkingDraft", "Authoring"],
  ["listWorkingDrafts", "Authoring"],
  ["getWorkingDraft", "Authoring"],
  ["updateWorkingDraft", "Authoring"],
  ["freezeWorkingDraft", "Authoring"],
  ["publishArticleRevision", "Authoring"],
  ["getDocumentDraft", "Authoring"],
  ["getAuthoringOverview", "Authoring"],
  ["listDocumentDrafts", "Authoring"],
  ["listDocumentHistory", "DocumentHistory"],
  ["compareDocumentHistory", "DocumentHistory"],
  ["createDocumentRestorePreview", "DocumentHistory"],
  ["createDocumentRestoreProposal", "DocumentHistory"],
  ["getDocumentKnowledgeProfile", "SourceSpans"],
  ["retryDocumentKnowledgeProfile", "SourceSpans"],
  ["processSourceVersion", "SourceSpans"],
  ["getEvidenceSourceVersion", "SourceSpans"],
  ["getEvidenceSourceSpan", "SourceSpans"],
  ["listSourceVersions", "SourceSpans"],
  ["startWorkflow", "Business"],
  ["listWorkflowRuns", "Business"],
  ["getWorkflowRun", "Business"],
  ["pauseWorkflow", "Business"],
  ["resumeWorkflow", "Business"],
  ["cancelWorkflow", "Business"],
  ["submitHumanDecision", "Business"],
  ["createProposal", "Business"],
  ["listProposals", "Business"],
  ["getProposal", "Business"],
  ["decideProposal", "Business"],
  ["checkProposalApplyPreflight", "Business"],
  ["getProposalCurrentContent", "BusinessRevisions"],
  ["previewProposalRevisionMerge", "BusinessRevisions"],
  ["listProposalRevisions", "BusinessRevisions"],
  ["appendProposalRevision", "BusinessRevisions"],
  ["getProposalRevision", "BusinessRevisions"],
  ["createConversation", "Conversation"],
  ["listConversations", "Conversation"],
  ["getConversation", "Conversation"],
  ["submitQuestion", "Conversation"],
  ["listConversationTurns", "Conversation"],
  ["getAnswer", "Conversation"],
  ["submitAnswerFeedback", "Conversation"],
  ["subscribeAnswerDraft", "Conversation"],
  ["getWorkspaceAnalysisTimeline", "Timeline"],
  ["subscribeServerEvents", "Events"],
  ["searchKnowledge", "Search"],
  ["getGraphGlobalPage", "Graph"],
  ["getGraphNeighborhoodPage", "Graph"],
  ["findGraphPath", "Graph"],
  ["searchGraphNodes", "Graph"],
  ["getGraphNodeDetail", "Graph"],
  ["getGraphRelationDetail", "Graph"],
  ["getGraphRelationEvidencePage", "Graph"],
  ["listSemanticLinkCandidates", "SemanticLinks"],
  ["getSemanticLinkCandidate", "SemanticLinks"],
  ["decideSemanticLinkCandidate", "SemanticLinks"],
  ["startSemanticLinkCandidateScan", "SemanticLinks"],
  ["getSemanticLinkCandidateScan", "SemanticLinks"],
  ["listCollections", "Collections"],
  ["createCollection", "Collections"],
  ["validateCollection", "Collections"],
  ["previewCollection", "Collections"],
  ["getCollection", "Collections"],
  ["updateCollection", "Collections"],
  ["archiveCollection", "Collections"],
  ["getCollectionResults", "Collections"],
  ["getHealthSummary", "Health"],
  ["listHealthIssues", "Health"],
  ["getHealthIssue", "Health"],
  ["listHealthIssueObservations", "Health"],
  ["listHealthIssueDecisions", "Health"],
  ["decideHealthIssue", "Health"],
  ["createHealthRepairProposal", "Health"],
  ["startHealthScan", "Health"],
  ["getHealthScan", "Health"],
  ["getHealthSchedule", "Health"],
  ["updateHealthSchedule", "Health"],
  ["exchangeBootstrapForSession", "Auth"],
  ["getCurrentSession", "Auth"],
  ["revokeCurrentSession", "Auth"],
  ["rotateSession", "Auth"],
  ["listApiTokens", "Auth"],
  ["createApiToken", "Auth"],
  ["revokeApiToken", "Auth"],
  ["listKnowledgeTimeline", "Timeline"],
  ["getKnowledgeTimelineEvent", "Timeline"],
  ["analyzeKnowledgeImpact", "Timeline"],
  ["getKnowledgeImpactReport", "Timeline"],
  ["createImpactDownstreamUpdateProposal", "Timeline"],
  ["createExport", "Exports"],
  ["getExport", "Exports"],
  ["downloadExport", "Exports"],
  ["listCollectionExports", "Exports"],
  ["createAttachmentExport", "AttachmentExports"],
  ["listAttachmentExports", "AttachmentExports"],
  ["getAttachmentExport", "AttachmentExports"],
  ["downloadAttachmentExport", "AttachmentExports"],
  ["listReviewDecks", "Review"],
  ["createReviewDeck", "Review"],
  ["getReviewDeck", "Review"],
  ["listReviewCards", "Review"],
  ["createReviewCard", "Review"],
  ["editReviewCard", "Review"],
  ["pauseReviewDeckSchedule", "Review"],
  ["resumeReviewDeckSchedule", "Review"],
  ["resetReviewDeckSchedule", "Review"],
  ["listReviewDueCards", "Review"],
  ["approveReviewCard", "Review"],
  ["rejectReviewCard", "Review"],
  ["invalidateReviewCard", "Review"],
  ["invalidateReviewCards", "Review"],
  ["startReviewSession", "Review"],
  ["completeReviewSession", "Review"],
  ["submitReviewAnswer", "Review"],
  ["getReviewLearningPath", "Review"],
  ["createReviewLearningPath", "Review"],
  ["updateReviewLearningPathStatus", "Review"],
  ["updateReviewLearningPathStep", "Review"],
  ["updateLearningPathStatus", "Review"],
  ["updateLearningPathStep", "Review"],
  ["listMemories", "Memory"],
  ["createMemoryCandidate", "Memory"],
  ["getMemory", "Memory"],
  ["editMemory", "Memory"],
  ["deleteMemory", "Memory"],
  ["confirmMemory", "Memory"],
  ["pauseMemory", "Memory"],
  ["resumeMemory", "Memory"],
  ["listInterviews", "Interview"],
  ["startInterview", "Interview"],
  ["getInterview", "Interview"],
  ["submitInterviewTurn", "Interview"],
  ["completeInterview", "Interview"],
  ["suggestInterviewMemoryCandidate", "Interview"],
  ["listArtifacts", "Artifacts"],
  ["planArtifact", "Artifacts"],
  ["getArtifact", "Artifacts"],
  ["submitArtifactOutline", "Artifacts"],
  ["approveArtifactOutline", "Artifacts"],
  ["startArtifactRevision", "Artifacts"],
  ["recordArtifactSection", "Artifacts"],
  ["generateArtifactSection", "Artifacts"],
  ["listArtifactSectionGenerations", "Artifacts"],
  ["approveArtifactDraft", "Artifacts"],
  ["exportArtifactMarkdown", "Artifacts"],
  ["getArtifactExport", "Exports"],
  ["createArtifactPublishProposal", "Artifacts"],
  ["createOrganizingDraft", "Organizing"],
  ["getOrganizingDraft", "Organizing"],
  ["updateOrganizingDraft", "Organizing"],
  ["suggestOrganizingMaterials", "Organizing"],
  ["addOrganizingMaterial", "Organizing"],
  ["setOrganizingMaterialSelection", "Organizing"],
  ["removeOrganizingMaterial", "Organizing"],
  ["confirmOrganizingDraft", "Organizing"],
  ["searchOrganizingMaterials", "Organizing"],
  ["getOrganizingSnapshot", "Organizing"],
  ["listOrganizingTemplates", "Organizing"],
  ["createOrganizingTemplate", "Organizing"],
  ["getOrganizingTemplate", "Organizing"],
  ["cloneOrganizingTemplate", "Organizing"],
  ["reviseOrganizingTemplate", "Organizing"],
  ["getOrganizingRun", "Organizing"],
  ["getGitRemoteConfig", "GitSync"],
  ["saveGitRemoteConfig", "GitSync"],
  ["removeGitRemoteConfig", "GitSync"],
  ["testGitRemoteConfig", "GitSync"],
  ["getGitSyncStatus", "GitSync"],
  ["listGitSyncRuns", "GitSync"],
  ["createGitSyncRun", "GitSync"],
  ["getGitSyncRun", "GitSync"],
  ["retryGitSyncRun", "GitSync"],
]);

function operationEntries(document) {
  return Object.entries(document.paths ?? {}).flatMap(([path, item]) =>
    Object.entries(item)
      .filter(([method]) => ["get", "put", "post", "delete", "options", "head", "patch", "trace"].includes(method))
      .map(([method, operation]) => ({ path, method, operation })),
  );
}

export function verifyTagManifest(document, manifest, { checkOperationTags = true } = {}) {
  const operations = operationEntries(document);
  const operationIds = new Set(operations.map(({ operation }) => operation.operationId));
  const manifestIds = new Set(Object.keys(manifest.operations ?? {}));
  const missing = [...operationIds].filter((operationId) => !manifestIds.has(operationId));
  const extra = [...manifestIds].filter((operationId) => !operationIds.has(operationId));
  if (missing.length || extra.length || operations.length !== EXACT_TAGS.size || operationIds.size !== operations.length) {
    throw new Error(`operation tag manifest drift: count=${operations.length}, missing=${missing.join(",")}, extra=${extra.join(",")}`);
  }
  for (const [operationId, tag] of Object.entries(manifest.operations)) {
    if (!CATALOG_NAMES.has(tag)) throw new Error(`operation ${operationId} uses unknown tag ${tag}`);
  }
  if (JSON.stringify(manifest.catalog) !== JSON.stringify(TAG_CATALOG.map(([name, description]) => ({ name, description })))) {
    throw new Error("operation tag catalog drifted from tag-manifest.mjs");
  }
  if (checkOperationTags && JSON.stringify(document.tags) !== JSON.stringify(manifest.catalog)) {
    throw new Error("OpenAPI top-level tag catalog drifted from operation-tags.json");
  }
  for (const { operation } of operations) {
    const expected = manifest.operations[operation.operationId];
    if (checkOperationTags && JSON.stringify(operation.tags) !== JSON.stringify([expected])) {
      throw new Error(`operation ${operation.operationId} must have exactly tag ${expected}`);
    }
  }
}

export function buildManifest(document) {
  const operations = operationEntries(document);
  const operationTags = Object.fromEntries(operations.map(({ operation }) => {
    const tag = EXACT_TAGS.get(operation.operationId);
    if (!tag) throw new Error(`operation ${operation.operationId} has no explicit tag assignment`);
    return [operation.operationId, tag];
  }));
  return {
    generatorTagContract: "2026-08-19",
    catalog: TAG_CATALOG.map(([name, description]) => ({ name, description })),
    operations: operationTags,
  };
}

export function renderTaggedSource(source, manifest) {
  const document = JSON.parse(source);
  verifyTagManifest(document, manifest, { checkOperationTags: false });
  if (document.tags !== undefined || operationEntries(document).some(({ operation }) => operation.tags !== undefined)) {
    verifyTagManifest(document, manifest);
    return source;
  }

  const catalog = JSON.stringify(manifest.catalog, null, 2).replaceAll("\n", "\n  ");
  let output = source.replace('  "paths": {', `  "tags": ${catalog},\n  "paths": {`);
  for (const [operationId, tag] of Object.entries(manifest.operations)) {
    const escapedOperationId = operationId.replaceAll(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const pattern = new RegExp(`^(\\s*)"operationId": "${escapedOperationId}",$`, "m");
    let matches = 0;
    output = output.replace(pattern, (line, indentation) => {
      matches += 1;
      return `${line}\n${indentation}"tags": [\n${indentation}  "${tag}"\n${indentation}],`;
    });
    if (matches !== 1) throw new Error(`operationId source marker must occur exactly once: ${operationId}`);
  }
  verifyTagManifest(JSON.parse(output), manifest);
  return output;
}

if (process.argv.includes("--write")) {
  const source = readFileSync(openapiPath, "utf8");
  const document = JSON.parse(source);
  const manifest = buildManifest(document);
  writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  writeFileSync(openapiPath, renderTaggedSource(source, manifest));
  console.log(`tagged ${Object.keys(manifest.operations).length} operations across ${manifest.catalog.length} domains`);
} else {
  const document = JSON.parse(readFileSync(openapiPath, "utf8"));
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  verifyTagManifest(document, manifest);
  console.log(`verified ${Object.keys(manifest.operations).length} operation tags`);
}
