import { MonacoTextEditor } from "../../shared/MonacoTextEditor";

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
  return <MonacoTextEditor
    ariaLabel="Markdown 正文"
    disabled={disabled}
    height="100%"
    language="markdown"
    loadingLabel="正在加载编辑器..."
    modelPath={modelPath}
    onChange={onChange}
    value={value}
    {...(onError === undefined ? {} : { onError })}
    {...(onReady === undefined ? {} : { onReady })}
  />;
};
