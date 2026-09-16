import { synthesisItemSources, synthesisSourceIdentity, type SynthesisRevision, type SynthesisSourceRef } from "../../api/synthesis";
import { canonicalUuidPattern } from "../../shared/codec";

const sourceParameters = ["workspace_id", "source_id", "source_version_id", "content_artifact_id", "parse_projection_id", "source_span_id", "content_hash", "excerpt_hash"] as const;

type SourceLink =
  | { kind: "none" }
  | { kind: "invalid" | "missing" }
  | { kind: "resolved"; reference: SynthesisSourceRef };

/** The URL only selects an exact saved reference; it cannot supply a new source or its title. */
export const resolveSynthesisSourceLink = (params: URLSearchParams, revision: SynthesisRevision): SourceLink => {
  if (!sourceParameters.some((key) => params.has(key))) return { kind: "none" };
  if (sourceParameters.some((key) => params.getAll(key).length !== 1)) return { kind: "invalid" };
  const reference = {
    source: {
      workspaceId: params.get("workspace_id") ?? "",
      sourceId: params.get("source_id") ?? "",
      sourceVersionId: params.get("source_version_id") ?? "",
      contentArtifactId: params.get("content_artifact_id") ?? "",
      parseProjectionId: params.get("parse_projection_id") ?? "",
      contentHash: params.get("content_hash") ?? "",
    },
    sourceSpanId: params.get("source_span_id") ?? "",
    excerptHash: params.get("excerpt_hash") ?? "",
  };
  if (reference.source.workspaceId !== revision.workspaceId ||
    ![reference.source.workspaceId, reference.source.sourceId, reference.source.sourceVersionId, reference.source.contentArtifactId, reference.source.parseProjectionId, reference.sourceSpanId].every((value) => canonicalUuidPattern.test(value)) ||
    ![reference.source.contentHash, reference.excerptHash].every((value) => /^[0-9a-f]{64}$/.test(value))) return { kind: "invalid" };
  const identity = synthesisSourceIdentity(reference);
  const saved = [...revision.items.flatMap(synthesisItemSources), ...(revision.display?.historicalSources ?? [])].find((candidate) => synthesisSourceIdentity(candidate) === identity);
  return saved === undefined ? { kind: "missing" } : { kind: "resolved", reference: saved };
};

export const synthesisSourceHref = (noteId: string, revisionId: string, ref: SynthesisSourceRef) => {
  const params = new URLSearchParams({ revision_id: revisionId, workspace_id: ref.source.workspaceId, source_id: ref.source.sourceId,
    source_version_id: ref.source.sourceVersionId, content_artifact_id: ref.source.contentArtifactId, parse_projection_id: ref.source.parseProjectionId,
    source_span_id: ref.sourceSpanId, content_hash: ref.source.contentHash, excerpt_hash: ref.excerptHash });
  return `/authoring/notes/${noteId}?${params.toString()}`;
};
