import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const editorRuntime = vi.hoisted(() => ({
  props: vi.fn(),
}));

vi.mock("../../shared/MonacoTextEditor", () => ({
  MonacoTextEditor: (props: Record<string, unknown>) => {
    editorRuntime.props(props);
    return <div>Shared Monaco editor</div>;
  },
}));

import { MonacoMarkdownEditor } from "./MonacoMarkdownEditor";

describe("MonacoMarkdownEditor", () => {
  it("is a Markdown-specific wrapper around the shared editor", () => {
    const onChange = vi.fn();
    const onReady = vi.fn();
    const onError = vi.fn();
    const modelPath = "inmemory://zhixu/workspaces/w/working-drafts/d.md";

    render(<MonacoMarkdownEditor value="# Java AI" modelPath={modelPath} disabled onChange={onChange} onReady={onReady} onError={onError} />);

    const editorProps = editorRuntime.props.mock.calls[0]?.[0] as Record<string, unknown>;
    expect(editorProps).toMatchObject({
      ariaLabel: "Markdown 正文",
      disabled: true,
      height: "100%",
      language: "markdown",
      loadingLabel: "正在加载编辑器...",
      modelPath,
      onChange,
      onError,
      onReady,
      value: "# Java AI",
    });
  });
});
