import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const editorRuntime = vi.hoisted(() => ({
  configure: vi.fn(),
  release: vi.fn(),
  props: vi.fn(),
}));

vi.mock("../../shared/monaco-runtime", () => ({
  configureLocalMonaco: editorRuntime.configure,
  releaseDetachedEditorModel: editorRuntime.release,
}));

vi.mock("@monaco-editor/react", () => ({
  Editor: (props: { value: string; onChange: (value: string | undefined) => void }) => {
    editorRuntime.props(props);
    return <textarea aria-label="Monaco mock" value={props.value} onChange={(event) => props.onChange(event.target.value)} />;
  },
}));

import { MonacoMarkdownEditor } from "./MonacoMarkdownEditor";

describe("MonacoMarkdownEditor", () => {
  it("owns one Workspace/Draft model and releases it after detachment", () => {
    const onChange = vi.fn();
    const onReady = vi.fn();
    const modelPath = "inmemory://zhixu/workspaces/w/working-drafts/d.md";

    render(<MonacoMarkdownEditor value="# Java AI" modelPath={modelPath} onChange={onChange} onReady={onReady} />);

    expect(editorRuntime.configure).toHaveBeenCalledOnce();
    const editorProps = editorRuntime.props.mock.calls[0]?.[0] as unknown as {
      value: string;
      path: string;
      keepCurrentModel: boolean;
      language: string;
      theme: string;
      options: Record<string, unknown>;
      onMount: (editor: object, monaco: object) => void;
    };
    expect(editorProps).toMatchObject({
      value: "# Java AI",
      path: modelPath,
      keepCurrentModel: true,
      language: "markdown",
      theme: "vs-light",
    });
    expect(editorProps.options).toMatchObject({ ariaLabel: "Markdown 正文", automaticLayout: true, wordWrap: "on" });

    const editor = {};
    const monaco = {};
    editorProps.onMount(editor, monaco);
    expect(editorRuntime.release).toHaveBeenCalledWith(editor, monaco);
    expect(onReady).toHaveBeenCalledOnce();

    fireEvent.change(screen.getByRole("textbox", { name: "Monaco mock" }), { target: { value: "# CAS" } });
    expect(onChange).toHaveBeenCalledWith("# CAS");
  });

  it("reports mount failures without hiding the original error", () => {
    const failure = new Error("model URI collision");
    editorRuntime.release.mockImplementationOnce(() => { throw failure; });
    const onError = vi.fn();

    render(<MonacoMarkdownEditor value="" modelPath="inmemory://zhixu/draft.md" onChange={vi.fn()} onError={onError} />);
    const props = editorRuntime.props.mock.calls.at(-1)?.[0] as { onMount: (editor: object, monaco: object) => void };
    props.onMount({}, {});

    expect(onError).toHaveBeenCalledWith(failure);
  });
});
