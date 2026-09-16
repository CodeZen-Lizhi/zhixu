import { useEffect, useRef, useState } from "react";
import { BookOpen, ExternalLink } from "lucide-react";
import { Link } from "react-router-dom";
import { synthesisItemSources, synthesisSourceIdentity, type SynthesisItem, type SynthesisRevision, type SynthesisSourceRef, type SynthesisStatement } from "../../api/synthesis";
import { Badge, Button, Dialog, ErrorState } from "../../shared/ui";
import { useSynthesisSource } from "./queries";
import { MarkdownPreview } from "../authoring/MarkdownPreview";
import { SourceKnowledgePoints } from "./SourceKnowledgePoints";

const errorText = (value: unknown) => value instanceof Error ? value.message : "无法读取原始来源。";

export const NoteContent = ({ revision, initialSource = null, initialItemId = null }: { revision: SynthesisRevision; initialSource?: SynthesisSourceRef | null; initialItemId?: string | null }) => {
  const [source, setSource] = useState<SynthesisSourceRef | null>(initialSource);
  const trigger = useRef<HTMLElement | null>(null);
  const focusedItem = useRef<HTMLElement | null>(null);
  useEffect(() => {
    if (initialItemId !== null) {
      focusedItem.current?.focus();
      focusedItem.current?.scrollIntoView({ block: "center" });
    }
  }, [revision.id, initialItemId]);
  const opened = useSynthesisSource(revision.noteId, revision.id, source);
  const sourceButtons = (sources: SynthesisSourceRef[]) => sources.length > 0 ? <ul className="synthesis-source-links" aria-label="原始来源">
    {[...new Map(sources.map((reference) => [synthesisSourceIdentity(reference), reference])).values()].map((reference) => <li key={synthesisSourceIdentity(reference)}><Button size="sm" variant="ghost" onClick={(event) => { trigger.current = event.currentTarget; setSource(reference); }}>
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
      case "CONFLICT": return <><h3>{value.conflict.subject}</h3><Badge tone="warning">尚未裁决</Badge><div className="synthesis-alternatives">{value.conflict.alternatives.map((alternative, index) => <section key={index} aria-label={`观点 ${String(index + 1)}`}><Badge tone="warning">观点 {index + 1}</Badge>{statement(alternative)}</section>)}</div></>;
      case "GAP": return <><div className="synthesis-item-heading"><h3>{value.gap.question}</h3><Badge tone={value.gap.resolution === null ? "warning" : "success"}>{value.gap.resolution === null ? "待补充" : "已补充"}</Badge></div>
        {value.gap.context !== "" ? <p>{value.gap.context}</p> : null}{sourceButtons(value.gap.sources)}
        {value.gap.resolution === null ? <p className="synthesis-help">现有资料不足以作出结论。</p> : <div className="synthesis-gap-resolution"><strong>补充结论</strong>{statement(value.gap.resolution)}</div>}</>;
    }
  };
  const indexedItem = (value: SynthesisItem) => <>
    {value.kind === "FACT" ? <p className="synthesis-statement">{value.fact.text}</p> : value.kind === "CONFLICT" ? <>
      <h3>{value.conflict.subject}</h3><Badge tone="warning">尚未裁决</Badge>
      <ul>{value.conflict.alternatives.map((alternative, index) => <li key={index}>{alternative.text}</li>)}</ul>
    </> : <><h3>{value.gap.question}</h3>{value.gap.resolution ? <p>{value.gap.resolution.text}</p> : <Badge tone="warning">待补充</Badge>}</>}
    {sourceButtons(synthesisItemSources(value))}
  </>;
  return <>
    <div className="synthesis-note-content" tabIndex={-1} ref={(element) => { trigger.current ??= element; }}>
      {revision.display ? <>
        {revision.display.reviewRequired ? <p role="status"><Badge tone="warning">{revision.display.manualChanges ? "含人工编辑，来源需复核" : "来源需复核"}</Badge></p> : null}
        <MarkdownPreview markdown={revision.display.fullContent} />
        {revision.display.historicalSources.length > 0 ? <details><summary>历史参考来源（仅供复核）</summary>
          <p>这些来源来自编辑前的内容记录，不代表当前正文已获来源验证。</p>
          {sourceButtons(revision.display.historicalSources)}
        </details> : null}
      </> : null}
      {revision.display && revision.items.length > 0 ? <header><h2>全文片段索引</h2><p>以下摘录用于辨认上方全文中的知识点及其来源，不是新增正文。</p></header> : null}
      {([{ kind: "FACT", label: "事实与互补" }, { kind: "CONFLICT", label: "冲突与适用条件" }, { kind: "GAP", label: "缺口与补充" }] as const).map((section) => {
        const items = revision.items.filter((item) => item.kind === section.kind);
        return items.length === 0 ? null : <section key={section.kind} aria-labelledby={`synthesis-${section.kind}`}><h2 id={`synthesis-${section.kind}`}>{section.label}<span>{items.length}</span></h2>
          <div className="synthesis-items">{items.map((value) => <article id={`synthesis-item-${value.id}`} tabIndex={-1} ref={value.id === initialItemId ? focusedItem : undefined} className={`synthesis-item synthesis-item--${value.kind.toLowerCase()}`} key={value.id}>{revision.display ? indexedItem(value) : item(value)}
            {value.bodyReference ? <Button asChild variant="ghost" size="sm"><Link to={`/authoring/notes/${value.bodyReference.noteId}?${new URLSearchParams({ revision_id: value.bodyReference.revisionId, body_item_id: value.bodyReference.itemId, body_workspace_id: value.bodyReference.workspaceId, body_projection_hash: value.bodyReference.projectionHash }).toString()}`}><ExternalLink size={15} aria-hidden="true" />查看引用的主笔记片段<span className="sr-only">，打开原发布版本</span></Link></Button> : null}
          </article>)}</div>
        </section>;
      })}
    </div>
    <Dialog open={source !== null} onOpenChange={(open) => { if (!open) setSource(null); }} title={source?.title ?? "原始来源"} description="此处保留该笔记版本引用的原始片段。" restoreFocusRef={trigger} contentClassName="synthesis-source-dialog">
      {source !== null ? <>
        {opened.isPending ? <p role="status">正在读取原始片段…</p> : null}
        {opened.isError ? <ErrorState description={errorText(opened.error)} onRetry={() => { void opened.refetch(); }} /> : null}
        {opened.data?.availability === "AVAILABLE" ? <pre className="synthesis-source-excerpt">{opened.data.text}</pre> : opened.data?.snapshotText ? <><p className="synthesis-help">已保存的历史原文，已核验片段哈希。</p><pre className="synthesis-source-excerpt">{opened.data.snapshotText}</pre></> : null}
        {opened.data && opened.data.availability !== "AVAILABLE" ? <div role="status"><Badge tone="warning">{opened.data.availability === "STALE" ? "来源已变化" : "来源不可用"}</Badge><p>{opened.data.snapshotText ? "来源当前不可用或已变化，上方展示的是这个笔记版本保存的原文。" : "无法核验这个历史片段。笔记保留原引用，没有替换为其他资料。"}</p></div> : null}
        <Button asChild variant="secondary"><Link to={`/documents/${source.source.sourceVersionId}`}><ExternalLink size={15} aria-hidden="true" />查看资料版本</Link></Button>
        {opened.data?.role === "HISTORICAL_REVIEW" || revision.display?.historicalSources.some((reference) => synthesisSourceIdentity(reference) === synthesisSourceIdentity(source)) ? <>
          <Badge tone="warning">历史参考，当前正文待复核</Badge>
          <p>此版本未记录该历史参考的知识目录快照，原文仍可追溯。</p>
        </> : <SourceKnowledgePoints key={source.sourceSpanId} revision={revision} reference={source} />}
      </> : null}
    </Dialog>
  </>;
};
