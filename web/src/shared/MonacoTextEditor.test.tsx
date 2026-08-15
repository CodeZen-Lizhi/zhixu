import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const editorRuntime = vi.hoisted(() => ({
  configure: vi.fn(),
  release: vi.fn(),
  props: vi.fn(),
}));

vi.mock("./monaco-runtime", () => ({
  configureLocalMonaco: editorRuntime.configure,
  releaseDetachedEditorModel: editorRuntime.release,
}));

vi.mock("@monaco-editor/react", () => ({
  Editor: (props: { value: string; onChange: (value: string | undefined) => void; loading: unknown }) => {
    editorRuntime.props(props);
    return <textarea aria-label="Monaco mock" value={props.value} onChange={(event) => props.onChange(event.target.value)} />;
  },
}));

import { MonacoTextEditor } from "./MonacoTextEditor";

describe("MonacoTextEditor", () => {
  beforeEach(() => {
    editorRuntime.configure.mockClear();
    editorRuntime.release.mockReset();
    editorRuntime.props.mockClear();
  });

  it("binds the controlled value to one retained route-owned model", () => {
    const onChange = vi.fn();
    const onReady = vi.fn();
    const modelPath = "inmemory://zhixu/workspaces/w/proposals/p/revisions/r/candidate.md";

    render(<MonacoTextEditor
      ariaLabel="待提交 Revision 正文"
      value="# Candidate"
      modelPath={modelPath}
      language="markdown"
      height="440px"
      onChange={onChange}
      onReady={onReady}
    />);

    expect(editorRuntime.configure).toHaveBeenCalledOnce();
    const editorProps = editorRuntime.props.mock.calls[0]?.[0] as unknown as {
      value: string;
      path: string;
      height: string;
      keepCurrentModel: boolean;
      language: string;
      theme: string;
      options: Record<string, unknown>;
      onMount: (editor: object, monaco: object) => void;
    };
    expect(editorProps).toMatchObject({
      value: "# Candidate",
      path: modelPath,
      height: "440px",
      keepCurrentModel: true,
      language: "markdown",
      theme: "vs-light",
    });
    expect(editorProps.options).toMatchObject({
      ariaLabel: "待提交 Revision 正文",
      automaticLayout: true,
      readOnly: false,
      wordWrap: "on",
    });

    const editor = {};
    const monaco = {};
    editorProps.onMount(editor, monaco);
    expect(editorRuntime.release).toHaveBeenCalledWith(editor, monaco);
    expect(onReady).toHaveBeenCalledOnce();

    fireEvent.change(screen.getByRole("textbox", { name: "Monaco mock" }), { target: { value: "# Revised" } });
    expect(onChange).toHaveBeenCalledWith("# Revised");
  });

  it("keeps a stable frame for read-only and reports mount failures", () => {
    const failure = new Error("model URI collision");
    editorRuntime.release.mockImplementationOnce(() => { throw failure; });
    const onError = vi.fn();

    render(<MonacoTextEditor
      ariaLabel="历史 Revision 正文"
      value="historic"
      modelPath="inmemory://zhixu/history.md"
      disabled
      onChange={vi.fn()}
      onError={onError}
    />);
    const props = editorRuntime.props.mock.calls[0]?.[0] as unknown as {
      options: Record<string, unknown>;
      onMount: (editor: object, monaco: object) => void;
    };

    expect(props.options).toMatchObject({ readOnly: true });
    props.onMount({}, {});
    expect(onError).toHaveBeenCalledWith(failure);
  });
});
