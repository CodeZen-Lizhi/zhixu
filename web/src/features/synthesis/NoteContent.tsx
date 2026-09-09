import { useRef, useState } from "react";
import { BookOpen, ExternalLink } from "lucide-react";
import { Link } from "react-router-dom";
import type { SynthesisItem, SynthesisRevision, SynthesisSourceRef, SynthesisStatement } from "../../api/synthesis";
import { Badge, Button, Dialog, ErrorState } from "../../shared/ui";
import { useSynthesisSource } from "./queries";

const errorText = (value: unknown) => value instanceof Error ? value.message : "无法读取原始来源。";

export const NoteContent = ({ revision, initialSource = null }: { revision: SynthesisRevision; initialSource?: SynthesisSourceRef | null }) => {
  const [source, setSource] = useState<SynthesisSourceRef | null>(initialSource);
  const trigger = useRef<HTMLElement | null>(null);
  const opened = useSynthesisSource(revision.noteId, revision.id, source);
  const sourceButtons = (sources: SynthesisSourceRef[]) => sources.length > 0 ? <ul className="synthesis-source-links" aria-label="原始来源">
    {sources.map((reference) => <li key={reference.sourceSpanId}><Button size="sm" variant="ghost" onClick={(event) => { trigger.current = event.currentTarget; setSource(reference); }}>
      <BookOpen size={15} aria-hidden="true" /><span>{reference.title}</span><span className="sr-only">，打开原始片段</span>
    </Button></li>)}
  </ul> : null;
  const statement = (value: SynthesisStatement) => <>
    <p className="synthesis-statement">{value.text}</p>
    <p className="synthesis-conditions"><span>适用条件</span>{value.applicability || "来源未说明"}</p>
    {sourceButtons(value.sources)}
  </>;
  const item = (value: SynthesisItem) => {
    switch (value.kind) {
      case "FACT": return statement(value.fact);
      case "CONFLICT": return <><h3>{value.conflict.subject}</h3><div className="synthesis-alternatives">{value.conflict.alternatives.map((alternative, index) => <section key={index} aria-label={`观点 ${String(index + 1)}`}><Badge tone="warning">观点 {index + 1}</Badge>{statement(alternative)}</section>)}</div></>;
      case "GAP": return <><div className="synthesis-item-heading"><h3>{value.gap.question}</h3><Badge tone={value.gap.resolution === null ? "warning" : "success"}>{value.gap.resolution === null ? "待补充" : "已补充"}</Badge></div>
        {value.gap.context !== "" ? <p>{value.gap.context}</p> : null}{sourceButtons(value.gap.sources)}
        {value.gap.resolution === null ? <p className="synthesis-help">现有资料不足以作出结论。</p> : <div className="synthesis-gap-resolution"><strong>补充结论</strong>{statement(value.gap.resolution)}</div>}</>;
    }
  };
  return <>
    <div className="synthesis-note-content" tabIndex={-1} ref={(element) => { trigger.current ??= element; }}>
      {([{ kind: "FACT", label: "事实与互补" }, { kind: "CONFLICT", label: "冲突与适用条件" }, { kind: "GAP", label: "缺口与补充" }] as const).map((section) => {
        const items = revision.items.filter((item) => item.kind === section.kind);
        return items.length === 0 ? null : <section key={section.kind} aria-labelledby={`synthesis-${section.kind}`}><h2 id={`synthesis-${section.kind}`}>{section.label}<span>{items.length}</span></h2>
          <div className="synthesis-items">{items.map((value) => <article className={`synthesis-item synthesis-item--${value.kind.toLowerCase()}`} key={value.id}>{item(value)}</article>)}</div>
        </section>;
      })}
    </div>
    <Dialog open={source !== null} onOpenChange={(open) => { if (!open) setSource(null); }} title={source?.title ?? "原始来源"} description="此处保留该笔记版本引用的原始片段。" restoreFocusRef={trigger} contentClassName="synthesis-source-dialog">
      {source !== null ? <>
        {opened.isPending ? <p role="status">正在读取原始片段…</p> : null}
        {opened.isError ? <ErrorState description={errorText(opened.error)} onRetry={() => { void opened.refetch(); }} /> : null}
        {opened.data?.availability === "AVAILABLE" ? <pre className="synthesis-source-excerpt">{opened.data.text}</pre> : null}
        {opened.data && opened.data.availability !== "AVAILABLE" ? <div role="status"><Badge tone="warning">{opened.data.availability === "STALE" ? "来源已变化" : "来源不可用"}</Badge><p>无法核验这个历史片段。笔记保留原引用，没有替换为其他资料。</p></div> : null}
        <Button asChild variant="secondary"><Link to={`/documents/${source.source.sourceVersionId}`}><ExternalLink size={15} aria-hidden="true" />查看资料版本</Link></Button>
      </> : null}
    </Dialog>
  </>;
};
