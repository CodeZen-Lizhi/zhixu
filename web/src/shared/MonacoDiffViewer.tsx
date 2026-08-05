import { DiffEditor, type DiffOnMount } from "@monaco-editor/react";

import { configureLocalMonaco, releaseDetachedDiffModels } from "./monaco-runtime";

export interface MonacoDiffViewerProps {
  identityKey: string;
  original: string;
  modified: string;
  originalModelPath: string;
  modifiedModelPath: string;
  language: string;
  onReady: () => void;
  onError: (error: Error) => void;
  height?: string;
}

export const MonacoDiffViewer = ({
  identityKey,
  original,
  modified,
  originalModelPath,
  modifiedModelPath,
  language,
  onReady,
  onError,
  height = "480px",
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
    height={height}
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
    options={{
      readOnly: true,
      renderSideBySide: true,
      minimap: { enabled: false },
      wordWrap: "on",
      originalEditable: false,
      automaticLayout: true,
      scrollBeyondLastLine: false,
    }}
  />;
};
