import { beforeEach, describe, expect, it, vi } from "vitest";

import { getCsrfToken, setCsrfToken, subscribeAuthInvalidation } from "./auth";
import { decodeSourceSpan, getSourceSpan, SourceSpanApiError, type SourceSpanReference } from "./source-spans";

const reference: SourceSpanReference = {
  workspaceId: "92000000-0000-4000-8000-000000000001",
  sourceVersionId: "92000000-0000-4000-8000-000000000002",
  sourceSpanId: "92000000-0000-4000-8000-000000000003",
};

const payload = {
  source_version: {
    workspace_id: reference.workspaceId,
    source_id: "92000000-0000-4000-8000-000000000004",
    source_version_id: reference.sourceVersionId,
    source_type: "local_file",
    logical_name: "auth.md",
    relative_path: "docs/auth.md",
    content_hash: "a".repeat(64),
    byte_size: 128,
    media_type: "text/markdown",
    security_status: "passed",
    ingestion_status: "parsed",
    workflow_status: "succeeded",
    index_status: "included",
    captured_at: "2026-07-25T08:09:10Z",
  },
  parse_projection_id: "92000000-0000-4000-8000-000000000005",
  span_id: reference.sourceSpanId,
  span_type: "section",
  start_line: 3,
  end_line: 5,
  start_byte: 16,
  end_byte: 64,
  selector: { heading: ["Auth"] },
  excerpt_hash: "b".repeat(64),
  parser_version: "goldmark-1",
  schema_version: "parse-v1",
  excerpt: "Session 必须通过 authFetch 读取。",
  excerpt_truncated: false,
};

const jsonResponse = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("source span API boundary", () => {
  beforeEach(() => {
    window.localStorage.clear();
    vi.stubGlobal("fetch", vi.fn());
  });

  it("严格解码与请求身份一致的不可变片段", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(payload));

    await expect(getSourceSpan(reference)).resolves.toMatchObject({
      sourceVersion: { logicalName: "auth.md", relativePath: "docs/auth.md" },
      startLine: 3,
      endLine: 5,
      excerpt: "Session 必须通过 authFetch 读取。",
    });
    const [path, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    expect(path).toBe(`/api/v1/workspaces/${reference.workspaceId}/source-versions/${reference.sourceVersionId}/spans/${reference.sourceSpanId}`);
    expect(init?.credentials).toBe("include");
    expect(new Headers(init?.headers).get("Accept")).toBe("application/json");
  });

  it("拒绝 Span 身份不一致和未知响应字段", () => {
    expect(() => decodeSourceSpan({ ...payload, span_id: reference.sourceVersionId }, reference)).toThrow(SourceSpanApiError);
    expect(() => decodeSourceSpan({ ...payload, untrusted: true }, reference)).toThrow(SourceSpanApiError);
    expect(() => decodeSourceSpan({ ...payload, source_version: { ...payload.source_version, captured_at: "2026-02-31T08:09:10Z" } }, reference)).toThrow(SourceSpanApiError);
  });

  it("401 经由 authFetch 清理认证状态并通知 AuthProvider", async () => {
    const listener = vi.fn();
    const unsubscribe = subscribeAuthInvalidation(listener);
    setCsrfToken("c".repeat(43));
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ error_code: "AUTH_UNAUTHORIZED", message: "expired", retryable: false }, 401));

    await expect(getSourceSpan(reference)).rejects.toMatchObject({ code: "AUTH_UNAUTHORIZED", status: 401 });

    expect(getCsrfToken()).toBeUndefined();
    expect(listener).toHaveBeenCalledWith({ kind: "unauthorized" });
    unsubscribe();
  });
});
