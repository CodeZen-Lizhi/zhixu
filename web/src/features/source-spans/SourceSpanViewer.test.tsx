import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SourceSpanViewer } from "./SourceSpanViewer";

const reference = {
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

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SourceSpanViewer", () => {
  it("只在用户操作后通过受控请求显示片段，不渲染原始 API 链接", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(payload), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    render(<SourceSpanViewer reference={reference} label="打开证据片段" />);

    expect(fetchMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("link", { name: "打开证据片段" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "打开证据片段" }));

    expect(await screen.findByText("Session 必须通过 authFetch 读取。")).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Source Span 证据" })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith(
      `/api/v1/workspaces/${reference.workspaceId}/source-versions/${reference.sourceVersionId}/spans/${reference.sourceSpanId}`,
      expect.objectContaining({ credentials: "include" }),
    );
  });
});
