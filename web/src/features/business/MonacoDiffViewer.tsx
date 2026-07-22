import { DiffEditor, loader, type DiffOnMount } from "@monaco-editor/react";
import * as monaco from "monaco-editor";
import EditorWorker from "monaco-editor/editor/editor.worker.js?worker";

interface MonacoWorkerEnvironment {
  getWorker: (_workerId: string, _label: string) => Worker;
}

type MonacoGlobal = typeof globalThis & { MonacoEnvironment?: MonacoWorkerEnvironment };

export interface MonacoDiffViewerProps {
  identityKey: string;
  original: string;
  modified: string;
  originalModelPath: string;
  modifiedModelPath: string;
  language: string;
  onReady: () => void;
  onError: (error: Error) => void;
}

let monacoConfigured = false;

export const configureLocalMonaco = (): void => {
  if (monacoConfigured) return;
  loader.config({ monaco });
  (globalThis as MonacoGlobal).MonacoEnvironment = {
    getWorker: () => new EditorWorker(),
  };
  monacoConfigured = true;
};

export const releaseDetachedDiffModels: DiffOnMount = (editor) => {
  const models = editor.getModel();
  if (!models) return;

  editor.getModifiedEditor().onDidDispose(() => {
    queueMicrotask(() => {
      if (!models.original.isDisposed() && !models.original.isAttachedToEditor()) models.original.dispose();
      if (!models.modified.isDisposed() && !models.modified.isAttachedToEditor()) models.modified.dispose();
    });
  });
};

export const MonacoDiffViewer = ({
  identityKey,
  original,
  modified,
  originalModelPath,
  modifiedModelPath,
  language,
  onReady,
  onError,
}: MonacoDiffViewerProps) => {
  configureLocalMonaco();

  const handleMount: DiffOnMount = (editor, monacoInstance) => {
    try {
      releaseDetachedDiffModels(editor, monacoInstance);
      onReady();
    } catch (error: unknown) {
      onError(error instanceof Error ? error : new Error("Diff Viewer 加载失败"));
    }
  };

  return <DiffEditor
    key={identityKey}
    height="480px"
    original={original}
    modified={modified}
    originalModelPath={originalModelPath}
    modifiedModelPath={modifiedModelPath}
    keepCurrentOriginalModel
    keepCurrentModifiedModel
    onMount={handleMount}
    language={language}
    theme="vs-light"
    loading={<p>正在加载 Diff Viewer…</p>}
    options={{ readOnly: true, renderSideBySide: true, minimap: { enabled: false }, wordWrap: "on", originalEditable: false, automaticLayout: true, scrollBeyondLastLine: false }}
  />;
};
