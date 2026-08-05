import { loader, type DiffOnMount, type OnMount } from "@monaco-editor/react";
import * as monaco from "monaco-editor";
import EditorWorker from "monaco-editor/editor/editor.worker.js?worker";

interface MonacoWorkerEnvironment {
  getWorker: (_workerId: string, _label: string) => Worker;
}

type MonacoGlobal = typeof globalThis & { MonacoEnvironment?: MonacoWorkerEnvironment };

let configured = false;

/** Monaco 只使用随应用打包的 worker，不从外部 CDN 加载运行时代码。 */
export const configureLocalMonaco = (): void => {
  if (configured) return;
  loader.config({ monaco });
  (globalThis as MonacoGlobal).MonacoEnvironment = {
    getWorker: () => new EditorWorker(),
  };
  configured = true;
};

export const releaseDetachedEditorModel: OnMount = (editor) => {
  const model = editor.getModel();
  if (model === null) return;
  editor.onDidDispose(() => {
    queueMicrotask(() => {
      if (!model.isDisposed() && !model.isAttachedToEditor()) model.dispose();
    });
  });
};

export const releaseDetachedDiffModels: DiffOnMount = (editor) => {
  const models = editor.getModel();
  if (models === null) return;
  editor.getModifiedEditor().onDidDispose(() => {
    queueMicrotask(() => {
      if (!models.original.isDisposed() && !models.original.isAttachedToEditor()) models.original.dispose();
      if (!models.modified.isDisposed() && !models.modified.isAttachedToEditor()) models.modified.dispose();
    });
  });
};

export const markdownModelPath = (workspaceId: string, draftId: string): string =>
  `inmemory://zhixu/workspaces/${workspaceId}/authoring/working-drafts/${draftId}.md`;
