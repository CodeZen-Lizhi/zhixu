import { FileImage, FileText, Link2, Save, Type, Upload } from "lucide-react";
import {
  type ClipboardEvent,
  type DragEvent,
  type KeyboardEvent,
  type RefObject,
  useEffect,
  useRef,
  useState,
} from "react";

import { CaptureApiError, type Capture, type CaptureCreateInput, type CaptureKind } from "../../api/captures";
import { Button, Dialog, Tabs, TabsContent, TabsList, TabsTrigger } from "../../shared/ui";
import { useCreateCapture } from "./queries";
import "./capture.css";

export interface QuickCaptureDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCaptured: (capture: Capture) => void;
  restoreFocusRef: RefObject<HTMLElement | null>;
  workspaceId: string;
}

const modes: readonly { kind: CaptureKind; label: string; icon: typeof Type }[] = [
  { kind: "TEXT", label: "文字", icon: Type },
  { kind: "URL", label: "链接", icon: Link2 },
  { kind: "FILE", label: "文件", icon: FileText },
  { kind: "IMAGE", label: "图片", icon: FileImage },
];

const imageMimeTypes = new Set(["image/png", "image/jpeg", "image/webp", "image/gif"]);
const documentMimeTypes = new Set(["text/markdown", "text/plain", "text/html", "application/pdf"]);
const imageExtensions = new Set(["png", "jpg", "jpeg", "webp", "gif"]);
const documentExtensions = new Set(["md", "markdown", "txt", "pdf", "html", "htm"]);
const fileAccept = ".md,.markdown,.txt,.pdf,.html,.htm,text/markdown,text/plain,text/html,application/pdf";
const imageAccept = ".png,.jpg,.jpeg,.webp,.gif,image/png,image/jpeg,image/webp,image/gif";
const maximumUploadBytes = 10 * 1024 * 1024;
const maximumTextBytes = 2 * 1024 * 1024 - 1024;
const textEncoder = new TextEncoder();
const controlCharacterPattern = /[\u0000-\u001f\u007f]/;

const captureErrorText = (error: unknown): string => {
  if (error instanceof CaptureApiError) return `${error.message}（${error.errorCode}）`;
  return error instanceof Error ? error.message : "快速记录未完成。";
};

const canRetry = (error: unknown): boolean => error instanceof CaptureApiError && error.retryable;

const fileExtension = (name: string): string => {
  const separator = name.lastIndexOf(".");
  return separator < 0 ? "" : name.slice(separator + 1).toLowerCase();
};

const classifyFile = (file: File): "FILE" | "IMAGE" | undefined => {
  const extension = fileExtension(file.name);
  if (imageMimeTypes.has(file.type) || (file.type === "" && imageExtensions.has(extension))) return "IMAGE";
  if (documentMimeTypes.has(file.type) || documentExtensions.has(extension)) return "FILE";
  return undefined;
};

const validateFile = (file: File): { kind: "FILE" | "IMAGE" } | { error: string } => {
  if (file.size < 1) return { error: "不能记录空文件。" };
  if (file.size > maximumUploadBytes) return { error: "单个文件不能超过 10 MiB。" };
  const kind = classifyFile(file);
  if (kind === undefined) return { error: "仅支持 Markdown、TXT、PDF、HTML、PNG、JPEG、WebP 或 GIF。" };
  return { kind };
};

const validateUpload = (file: File, kind: "FILE" | "IMAGE"): string | undefined => {
  const validation = validateFile(file);
  if ("error" in validation) return validation.error;
  return validation.kind === kind ? undefined : "所选内容与当前记录类型不匹配。";
};

const validateURL = (value: string): string | undefined => {
  const trimmed = value.trim();
  if (trimmed === "") return "请输入原始链接。";
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return "请输入完整的 HTTP 或 HTTPS 链接。";
  }
  if (
    (parsed.protocol !== "http:" && parsed.protocol !== "https:")
    || parsed.hostname === ""
    || parsed.username !== ""
    || parsed.password !== ""
  ) {
    return "请输入不含账号信息的 HTTP 或 HTTPS 链接。";
  }
  return undefined;
};

/** 持有单个内存草稿，并在成功或确认放弃后清除。 */
export const QuickCaptureDialog = ({
  open,
  onOpenChange,
  onCaptured,
  restoreFocusRef,
  workspaceId,
}: QuickCaptureDialogProps) => {
  const [kind, setKind] = useState<CaptureKind>("TEXT");
  const [displayName, setDisplayName] = useState("");
  const [text, setText] = useState("");
  const [url, setURL] = useState("");
  const [file, setFile] = useState<File>();
  const [localError, setLocalError] = useState<string>();
  const [submitted, setSubmitted] = useState(false);
  const [discardPending, setDiscardPending] = useState(false);
  const draftRevisionRef = useRef(0);
  const attemptRef = useRef<{ revision: number; idempotencyKey: string } | undefined>(undefined);
  const textInputRef = useRef<HTMLTextAreaElement>(null);
  const urlInputRef = useRef<HTMLInputElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const dropzoneRef = useRef<HTMLButtonElement>(null);
  const mutation = useCreateCapture();

  const dirty = displayName !== "" || text !== "" || url !== "" || file !== undefined;
  const displayNameError = displayName.trim() !== "" && (
    textEncoder.encode(displayName.trim()).byteLength > 512 || controlCharacterPattern.test(displayName)
  ) ? "临时名称不能超过 512 字节，也不能包含控制字符。" : undefined;
  const validationError = displayNameError ?? (kind === "TEXT"
    ? text.trim() === ""
      ? "请输入记录内容。"
      : textEncoder.encode(text).byteLength > maximumTextBytes
        ? "文字内容过大，请改用文件记录。"
        : text.includes("\u0000")
          ? "文字内容不能包含空字符。"
          : undefined
    : kind === "URL"
      ? validateURL(url)
      : file === undefined
        ? `请选择一个${kind === "IMAGE" ? "图片" : "文件"}。`
        : validateUpload(file, kind));
  const valid = workspaceId !== "" && validationError === undefined;

  useEffect(() => {
    if (!open) return undefined;
    const timer = window.setTimeout(() => {
      if (kind === "TEXT") textInputRef.current?.focus();
      else if (kind === "URL") urlInputRef.current?.focus();
      else dropzoneRef.current?.focus();
    });
    return () => window.clearTimeout(timer);
  }, [kind, open]);

  const markDraftChanged = (): void => {
    draftRevisionRef.current += 1;
    attemptRef.current = undefined;
    setSubmitted(false);
    setLocalError(undefined);
    setDiscardPending(false);
    mutation.reset();
  };

  const clearDraft = (): void => {
    draftRevisionRef.current += 1;
    attemptRef.current = undefined;
    setKind("TEXT");
    setDisplayName("");
    setText("");
    setURL("");
    setFile(undefined);
    setLocalError(undefined);
    setSubmitted(false);
    setDiscardPending(false);
    mutation.reset();
    if (fileInputRef.current !== null) fileInputRef.current.value = "";
  };

  const closeAndClear = (): void => {
    clearDraft();
    onOpenChange(false);
  };

  const requestOpenChange = (nextOpen: boolean): void => {
    if (nextOpen) {
      onOpenChange(true);
      return;
    }
    if (mutation.isPending) return;
    if (dirty) {
      setDiscardPending(true);
      return;
    }
    closeAndClear();
  };

  const setSelectionError = (message: string): void => {
    markDraftChanged();
    setFile(undefined);
    setLocalError(message);
    if (fileInputRef.current !== null) fileInputRef.current.value = "";
  };

  const selectFile = (nextFile: File): void => {
    const validation = validateFile(nextFile);
    if ("error" in validation) {
      setSelectionError(validation.error);
      return;
    }
    markDraftChanged();
    setFile(nextFile);
    setKind(validation.kind);
  };

  const selectExactlyOneFile = (files: readonly File[]): void => {
    if (files.length !== 1) {
      setSelectionError("每次只能记录一个文件。请重新选择。");
      return;
    }
    const selected = files[0];
    if (selected !== undefined) selectFile(selected);
  };

  const handleDrop = (event: DragEvent<HTMLButtonElement>): void => {
    event.preventDefault();
    selectExactlyOneFile(Array.from(event.dataTransfer.files));
  };

  const handlePaste = (event: ClipboardEvent<HTMLFormElement>): void => {
    const files = Array.from(event.clipboardData.files);
    if (files.length === 0) {
      for (const item of Array.from(event.clipboardData.items)) {
        if (item.kind !== "file") continue;
        const pasted = item.getAsFile();
        if (pasted !== null) files.push(pasted);
      }
    }
    if (files.length === 0) return;
    event.preventDefault();
    selectExactlyOneFile(files);
  };

  const finishCapture = (capture: Capture): void => {
    onCaptured(capture);
    closeAndClear();
  };

  const save = (input: CaptureCreateInput): void => {
    mutation.mutate(input, { onSuccess: ({ capture }) => finishCapture(capture) });
  };

  const submit = (): void => {
    setSubmitted(true);
    setLocalError(undefined);
    if (!valid || mutation.isPending) return;
    const revision = draftRevisionRef.current;
    const currentAttempt = attemptRef.current;
    const idempotencyKey = currentAttempt?.revision === revision
      ? currentAttempt.idempotencyKey
      : `capture-${crypto.randomUUID()}`;
    attemptRef.current = { revision, idempotencyKey };
    const base = {
      workspaceId,
      idempotencyKey,
      ...(displayName.trim() === "" ? {} : { displayName: displayName.trim() }),
    };
    if (kind === "TEXT") save({ ...base, kind, text });
    else if (kind === "URL") save({ ...base, kind, url: url.trim() });
    else if (file !== undefined) save({ ...base, kind, file });
  };

  const changeKind = (value: string): void => {
    const next = modes.find((mode) => mode.kind === value)?.kind;
    if (next === undefined || mutation.isPending) return;
    markDraftChanged();
    setKind(next);
    setFile(undefined);
    if (fileInputRef.current !== null) fileInputRef.current.value = "";
  };

  const handleKeyboardSubmit = (event: KeyboardEvent<HTMLFormElement>): void => {
    if (event.key !== "Enter" || (!event.metaKey && !event.ctrlKey)) return;
    event.preventDefault();
    submit();
  };

  return <Dialog
    open={open}
    onOpenChange={requestOpenChange}
    title="快速记录"
    description="先存入资料收件箱，处理状态随后更新。"
    restoreFocusRef={restoreFocusRef}
  >
    <form
      aria-label="快速记录表单"
      className="quick-capture"
      noValidate
      onKeyDown={handleKeyboardSubmit}
      onPaste={handlePaste}
      onSubmit={(event) => { event.preventDefault(); submit(); }}
    >
      <Tabs value={kind} onValueChange={changeKind}>
        <TabsList className="quick-capture__modes" aria-label="记录类型">
          {modes.map((mode) => <TabsTrigger key={mode.kind} value={mode.kind} disabled={mutation.isPending}><mode.icon size={15} />{mode.label}</TabsTrigger>)}
        </TabsList>

        <label className="quick-capture__field quick-capture__name" htmlFor="quick-capture-name">
          临时名称
          <input
            id="quick-capture-name"
            value={displayName}
            maxLength={512}
            autoComplete="off"
            disabled={mutation.isPending}
            placeholder="可留空"
            onChange={(event) => { markDraftChanged(); setDisplayName(event.target.value); }}
          />
        </label>

        <TabsContent className="quick-capture__panel" value="TEXT">
          <label className="quick-capture__field" htmlFor="quick-capture-text">
            记录内容
            <textarea
              ref={textInputRef}
              id="quick-capture-text"
              value={text}
              rows={8}
              disabled={mutation.isPending}
              placeholder="写下还没来得及整理的想法或材料"
              onChange={(event) => { markDraftChanged(); setText(event.target.value); }}
            />
          </label>
        </TabsContent>
        <TabsContent className="quick-capture__panel" value="URL">
          <label className="quick-capture__field" htmlFor="quick-capture-url">
            原始链接
            <input
              ref={urlInputRef}
              id="quick-capture-url"
              type="url"
              inputMode="url"
              value={url}
              maxLength={8192}
              autoComplete="url"
              disabled={mutation.isPending}
              placeholder="https://"
              onChange={(event) => { markDraftChanged(); setURL(event.target.value); }}
            />
          </label>
        </TabsContent>
        {modes.filter((mode) => mode.kind === "FILE" || mode.kind === "IMAGE").map((mode) => (
          <TabsContent className="quick-capture__panel" key={mode.kind} value={mode.kind}>
            <input
              ref={kind === mode.kind ? fileInputRef : undefined}
              className="quick-capture__file-input"
              type="file"
              tabIndex={-1}
              aria-hidden="true"
              accept={mode.kind === "IMAGE" ? imageAccept : fileAccept}
              disabled={mutation.isPending}
              onChange={(event) => {
                const selectedFiles = event.currentTarget.files;
                if (selectedFiles !== null && selectedFiles.length > 0) {
                  selectExactlyOneFile(Array.from(selectedFiles));
                }
              }}
            />
            <button
              ref={kind === mode.kind ? dropzoneRef : undefined}
              className="quick-capture__dropzone"
              type="button"
              disabled={mutation.isPending}
              onClick={() => fileInputRef.current?.click()}
              onDragOver={(event) => event.preventDefault()}
              onDrop={handleDrop}
            >
              <Upload size={22} />
              <strong>{mode.kind === "IMAGE" ? "选择或粘贴图片" : "选择或拖入文件"}</strong>
              <span>{mode.kind === "IMAGE" ? "PNG、JPEG、WebP 或 GIF" : "Markdown、TXT、PDF 或 HTML"}</span>
            </button>
            {file === undefined ? null : <div className="quick-capture__selected-file">
              <span>{file.name}</span>
              <small>{(file.size / 1024).toFixed(1)} KB · {file.type || "待服务端识别"}</small>
            </div>}
          </TabsContent>
        ))}
      </Tabs>

      {localError !== undefined || (submitted && validationError !== undefined)
        ? <p className="quick-capture__field-error" role="alert">{localError ?? validationError}</p>
        : null}

      {mutation.isPending ? <p className="quick-capture__pending" role="status">正在可靠保存这条记录…</p> : null}

      {mutation.isError ? <div className="quick-capture__error" role="alert">
        <strong>这条记录还没有保存</strong>
        <p>{captureErrorText(mutation.error)}</p>
        {canRetry(mutation.error)
          ? <Button type="button" variant="secondary" onClick={() => save(mutation.variables)}>重试原请求</Button>
          : null}
      </div> : null}

      {discardPending ? <div className="quick-capture__discard" role="alert">
        <div><strong>放弃这条草稿？</strong><p>未保存内容会从本机内存中清除。</p></div>
        <div className="button-row"><Button type="button" variant="ghost" onClick={() => setDiscardPending(false)}>继续编辑</Button><Button type="button" variant="danger" onClick={closeAndClear}>放弃</Button></div>
      </div> : null}

      <div className="quick-capture__actions">
        <Button type="button" variant="ghost" disabled={mutation.isPending} onClick={() => requestOpenChange(false)}>取消</Button>
        <Button type="submit" disabled={!valid || mutation.isPending}><Save size={16} />{mutation.isPending ? "正在保存…" : "存入收件箱"}</Button>
      </div>
    </form>
  </Dialog>;
};
