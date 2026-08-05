import type { OnMount } from "@monaco-editor/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("monaco-editor", () => ({}));
vi.mock("@monaco-editor/react", () => ({ loader: { config: vi.fn() } }));

import { markdownModelPath, releaseDetachedEditorModel } from "./monaco-runtime";

describe("shared Monaco runtime", () => {
  it("binds Markdown models to the complete Workspace and Draft identity", () => {
    expect(markdownModelPath(
      "e1000000-0000-4000-8000-000000000001",
      "e1000000-0000-4000-8000-000000000002",
    )).toBe("inmemory://zhixu/workspaces/e1000000-0000-4000-8000-000000000001/authoring/working-drafts/e1000000-0000-4000-8000-000000000002.md");
  });

  it("releases an editor model only after editor detachment", async () => {
    const model = {
      dispose: vi.fn(),
      isDisposed: vi.fn(() => false),
      isAttachedToEditor: vi.fn(() => false),
    };
    let notifyDisposed: (() => void) | undefined;
    const editor = {
      getModel: () => model,
      onDidDispose: (listener: () => void) => {
        notifyDisposed = listener;
        return { dispose: vi.fn() };
      },
    } as unknown as Parameters<OnMount>[0];

    releaseDetachedEditorModel(editor, {} as Parameters<OnMount>[1]);
    expect(model.dispose).not.toHaveBeenCalled();

    notifyDisposed?.();
    await new Promise<void>((resolve) => queueMicrotask(resolve));
    expect(model.dispose).toHaveBeenCalledOnce();
  });

  it("keeps a same-URI model that was attached again before cleanup", async () => {
    const model = {
      dispose: vi.fn(),
      isDisposed: vi.fn(() => false),
      isAttachedToEditor: vi.fn(() => true),
    };
    let notifyDisposed: (() => void) | undefined;
    const editor = {
      getModel: () => model,
      onDidDispose: (listener: () => void) => {
        notifyDisposed = listener;
        return { dispose: vi.fn() };
      },
    } as unknown as Parameters<OnMount>[0];

    releaseDetachedEditorModel(editor, {} as Parameters<OnMount>[1]);
    notifyDisposed?.();
    await new Promise<void>((resolve) => queueMicrotask(resolve));
    expect(model.dispose).not.toHaveBeenCalled();
  });
});
