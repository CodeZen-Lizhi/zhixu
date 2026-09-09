import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import { BookOpen } from "lucide-react";
import type { NoteQuestionSource, SynthesisSourceRef } from "../../api/synthesis";
import { Button, Dialog, ErrorState } from "../../shared/ui";
import { useSynthesisSource } from "./queries";
import "./synthesis.css";

/** Interview 的已答题/报告来源只通过笔记 owner 重新打开原始片段。 */
export const SynthesisEvidence = ({ source }: { source: NoteQuestionSource }) => {
  const [reference, setReference] = useState<SynthesisSourceRef | null>(null);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const opened = useSynthesisSource(source.revision.noteId, source.revision.revisionId, reference);
  const sources = [...new Map(source.sources.map((item) => [item.sourceSpanId, item])).values()];
  return <div className="synthesis-evidence">
    <p><Link to={`/authoring/notes/${source.revision.noteId}?revision_id=${source.revision.revisionId}`}>{source.revision.title} · 版本 {source.revision.revisionNo}</Link></p>
    {sources.length === 0 ? <p className="synthesis-help">这项知识缺口尚无直接来源，需补充资料后再判断。</p> : <ul className="synthesis-source-links">{sources.map((item) => <li key={item.sourceSpanId}><Button variant="ghost" size="sm" onClick={(event) => { trigger.current = event.currentTarget; setReference(item); }}><BookOpen size={15} aria-hidden="true" />{item.title}</Button></li>)}</ul>}
    <Dialog open={reference !== null} onOpenChange={(open) => { if (!open) setReference(null); }} title={reference?.title ?? "原始来源"} description="回看面试冻结版本所引用的原始片段。" restoreFocusRef={trigger} contentClassName="synthesis-source-dialog">
      {reference !== null && opened.isPending ? <p role="status">正在读取原始片段…</p> : null}
      {opened.isError ? <ErrorState description={opened.error.message} onRetry={() => { void opened.refetch(); }} /> : null}
      {opened.data?.availability === "AVAILABLE" ? <pre className="synthesis-source-excerpt">{opened.data.text}</pre> : null}
      {opened.data && opened.data.availability !== "AVAILABLE" ? <p role="status">{opened.data.availability === "STALE" ? "来源已变化" : "来源不可用"}，无法核验这个历史片段。原引用已保留。</p> : null}
    </Dialog>
  </div>;
};
