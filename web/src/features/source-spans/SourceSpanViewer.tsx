import { FileText, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { getSourceSpan, type SourceSpan, type SourceSpanReference } from "../../api/source-spans";

export interface SourceSpanViewerProps {
  reference: SourceSpanReference;
  label: string;
  className?: string;
}

type LoadState =
  | { status: "pending" }
  | { status: "ready"; span: SourceSpan }
  | { status: "error"; message: string };

const focusableSelector = "button:not([disabled]), [tabindex]:not([tabindex='-1'])";

/** 通过认证 API 边界在当前应用内查看不可变 Source Span。 */
export const SourceSpanViewer = ({ reference, label, className }: SourceSpanViewerProps) => {
  const [open, setOpen] = useState(false);
  const [reload, setReload] = useState(0);
  const [state, setState] = useState<LoadState>({ status: "pending" });
  const triggerRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLElement>(null);
  const { workspaceId, sourceVersionId, sourceSpanId } = reference;

  const close = (): void => {
    setOpen(false);
    requestAnimationFrame(() => triggerRef.current?.focus());
  };

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setState({ status: "pending" });
    void getSourceSpan({ workspaceId, sourceVersionId, sourceSpanId }, controller.signal)
      .then((span) => {
        if (!controller.signal.aborted) setState({ status: "ready", span });
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setState({ status: "error", message: error instanceof Error ? error.message : "无法读取来源片段。" });
      });
    return () => controller.abort();
  }, [open, reload, sourceSpanId, sourceVersionId, workspaceId]);

  useEffect(() => {
    if (open) dialogRef.current?.focus();
  }, [open]);

  const handleKeyDown = (event: React.KeyboardEvent<HTMLElement>): void => {
    if (event.key === "Escape") {
      event.preventDefault();
      close();
      return;
    }
    if (event.key !== "Tab") return;
    const dialog = dialogRef.current;
    if (dialog === null) return;
    const focusable = [...dialog.querySelectorAll<HTMLElement>(focusableSelector)];
    const first = focusable[0];
    const last = focusable.at(-1);
    if (first === undefined || last === undefined) return;
    if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  return <>
    <button ref={triggerRef} type="button" className={className} aria-haspopup="dialog" onClick={() => setOpen(true)}>
      <FileText size={15} />{label}
    </button>
    {!open ? null : <div className="source-span-backdrop">
      <section ref={dialogRef} className="source-span-dialog" tabIndex={-1} role="dialog" aria-modal="true" aria-label="来源片段证据" onKeyDown={handleKeyDown}>
        <header><div><p className="eyebrow">来源片段</p><h2>不可变来源片段</h2></div><button type="button" className="source-span-dialog__close" aria-label="关闭来源片段" title="关闭来源片段" onClick={close}><X size={18} /></button></header>
        {state.status === "pending" ? <p role="status">正在读取来源片段…</p> : null}
        {state.status === "error" ? <div className="ui-state ui-state--error" role="alert"><strong>来源片段读取失败</strong><p>{state.message}</p><button type="button" className="ui-button ui-button--secondary" onClick={() => setReload((current) => current + 1)}>重试</button></div> : null}
        {state.status !== "ready" ? null : <div className="source-span-dialog__content"><dl><div><dt>文件</dt><dd>{state.span.sourceVersion.relativePath}</dd></div><div><dt>范围</dt><dd>第 {String(state.span.startLine)} 至 {String(state.span.endLine)} 行</dd></div><div><dt>类型</dt><dd>{state.span.spanType}</dd></div></dl><pre>{state.span.excerpt}</pre>{state.span.excerptTruncated ? <p className="source-span-dialog__notice">服务端已按安全上限截断该片段。</p> : null}</div>}
      </section>
    </div>}
  </>;
};
