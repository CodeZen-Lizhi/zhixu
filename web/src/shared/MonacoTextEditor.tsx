import { Editor, type OnMount } from "@monaco-editor/react";

import { configureLocalMonaco, releaseDetachedEditorModel } from "./monaco-runtime";

export interface MonacoTextEditorProps {
  ariaLabel: string;
  value: string;
  modelPath: string;
  onChange: (value: string) => void;
  language?: string;
  height?: string;
  disabled?: boolean;
  loadingLabel?: string;
  onReady?: () => void;
  onError?: (error: Error) => void;
}

export const MonacoTextEditor = ({
  ariaLabel,
  value,
  modelPath,
  onChange,
  language = "plaintext",
  height = "100%",
  disabled = false,
  loadingLabel = "正在加载文本编辑器...",
  onReady,
  onError,
}: MonacoTextEditorProps) => {
  configureLocalMonaco();

  const handleMount: OnMount = (editor, monacoInstance) => {
    try {
      releaseDetachedEditorModel(editor, monacoInstance);
      onReady?.();
    } catch (error: unknown) {
      onError?.(error instanceof Error ? error : new Error("文本编辑器加载失败"));
    }
  };

  return <Editor
    key={modelPath}
    height={height}
    language={language}
    loading={<p className="monaco-text-editor__loading" role="status">{loadingLabel}</p>}
    onChange={(nextValue) => onChange(nextValue ?? "")}
    onMount={handleMount}
    options={{
      ariaLabel,
      automaticLayout: true,
      minimap: { enabled: false },
      padding: { top: 18, bottom: 18 },
      readOnly: disabled,
      renderLineHighlight: "line",
      scrollBeyondLastLine: false,
      tabSize: 2,
      wordWrap: "on",
    }}
    path={modelPath}
    keepCurrentModel
    theme="vs-light"
    value={value}
  />;
};
