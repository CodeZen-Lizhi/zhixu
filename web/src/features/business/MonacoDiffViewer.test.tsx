import { render, screen } from "@testing-library/react";
import type { DiffOnMount } from "@monaco-editor/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const reactMonaco = vi.hoisted(() => ({
  diffEditor: vi.fn(),
  loaderConfig: vi.fn(),
}));

vi.mock("monaco-editor", () => ({ localMonaco: true }));
vi.mock("@monaco-editor/react", () => ({
  DiffEditor: (props: Record<string, unknown>) => {
    reactMonaco.diffEditor(props);
    return <div>Local Monaco Diff</div>;
  },
  loader: { config: reactMonaco.loaderConfig },
}));

import { MonacoDiffViewer, releaseDetachedDiffModels } from "./MonacoDiffViewer";

interface DiffModelMock {
  dispose: ReturnType<typeof vi.fn>;
  isDisposed: ReturnType<typeof vi.fn<() => boolean>>;
  isAttachedToEditor: ReturnType<typeof vi.fn<() => boolean>>;
}

const model = (attached: boolean): DiffModelMock => ({
  dispose: vi.fn(),
  isDisposed: vi.fn(() => false),
  isAttachedToEditor: vi.fn(() => attached),
});

afterEach(() => {
  reactMonaco.diffEditor.mockClear();
});

describe("MonacoDiffViewer", () => {
  it("使用本地 monaco loader 和 Vite worker，并传递安全模型配置", () => {
    const onReady = vi.fn();
    const onError = vi.fn();

    render(<MonacoDiffViewer
      identityKey="workspace:proposal:revision"
      original="old"
      modified="new"
      originalModelPath="inmemory://zhixu/workspaces/w/proposals/p/original.md"
      modifiedModelPath="inmemory://zhixu/workspaces/w/proposals/p/modified.md"
      language="markdown"
      onReady={onReady}
      onError={onError}
    />);

    expect(screen.getByText("Local Monaco Diff")).toBeInTheDocument();
    expect(reactMonaco.loaderConfig).toHaveBeenCalledWith({ monaco: { localMonaco: true } });
    expect(reactMonaco.diffEditor).toHaveBeenCalledWith(expect.objectContaining({
      original: "old",
      modified: "new",
      originalModelPath: "inmemory://zhixu/workspaces/w/proposals/p/original.md",
      modifiedModelPath: "inmemory://zhixu/workspaces/w/proposals/p/modified.md",
      keepCurrentOriginalModel: true,
      keepCurrentModifiedModel: true,
      language: "markdown",
      theme: "vs-light",
    }));
    expect(typeof globalThis.MonacoEnvironment?.getWorker).toBe("function");
  });

  it("内部 DiffEditor 解除绑定后释放 detached models", async () => {
    const original = model(false);
    const modified = model(false);
    let notifyEditorDisposed: (() => void) | undefined;

    const editor = {
      getModel: () => ({ original, modified }),
      getModifiedEditor: () => ({
        onDidDispose: (listener: () => void) => {
          notifyEditorDisposed = listener;
          return { dispose: vi.fn() };
        },
      }),
    } as unknown as Parameters<DiffOnMount>[0];

    releaseDetachedDiffModels(editor, {} as Parameters<DiffOnMount>[1]);

    notifyEditorDisposed?.();
    await new Promise<void>((resolve) => queueMicrotask(resolve));

    expect(original.dispose).toHaveBeenCalledOnce();
    expect(modified.dispose).toHaveBeenCalledOnce();
  });

  it("快速重挂载到同 URI 时不释放仍 attached 的 models", async () => {
    const original = model(true);
    const modified = model(true);
    let notifyEditorDisposed: (() => void) | undefined;

    const editor = {
      getModel: () => ({ original, modified }),
      getModifiedEditor: () => ({
        onDidDispose: (listener: () => void) => {
          notifyEditorDisposed = listener;
          return { dispose: vi.fn() };
        },
      }),
    } as unknown as Parameters<DiffOnMount>[0];

    releaseDetachedDiffModels(editor, {} as Parameters<DiffOnMount>[1]);

    notifyEditorDisposed?.();
    await new Promise<void>((resolve) => queueMicrotask(resolve));

    expect(original.dispose).not.toHaveBeenCalled();
    expect(modified.dispose).not.toHaveBeenCalled();
  });
});
