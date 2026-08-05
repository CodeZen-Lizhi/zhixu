import { Editor, type OnMount } from "@monaco-editor/react";

import { configureLocalMonaco, releaseDetachedEditorModel } from "../../shared/monaco-runtime";

export interface MonacoMarkdownEditorProps {
  value: string;
  modelPath: string;
  disabled?: boolean;
  onChange: (value: string) => void;
  onReady?: () => void;
  onError?: (error: Error) => void;
}

export const MonacoMarkdownEditor = ({
  value,
  modelPath,
  disabled = false,
  onChange,
  onReady,
  onError,
}: MonacoMarkdownEditorProps) => {
  configureLocalMonaco();

  const handleMount: OnMount = (editor, monacoInstance) => {
    try {
      releaseDetachedEditorModel(editor, monacoInstance);
      onReady?.();
    } catch (error: unknown) {
      onError?.(error instanceof Error ? error : new Error("Markdown 编辑器加载失败"));
    }
  };

  return <Editor
    key={modelPath}
    height="100%"
    language="markdown"
    loading={<p className="authoring-editor__loading">正在加载编辑器…</p>}
    onChange={(nextValue) => onChange(nextValue ?? "")}
    onMount={handleMount}
    options={{
      ariaLabel: "Markdown 正文",
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
