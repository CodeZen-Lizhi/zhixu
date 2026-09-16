import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import type { SynthesisSourceSupplement } from "../../api/synthesis";
import { Badge, Button, Dialog, ErrorState } from "../../shared/ui";
import { useSynthesisSupplements, useSynthesisSupplementSource } from "./queries";

/** 后续补充来源单独展示，绝不改变历史快照。 */
export const SupplementalSources = ({ noteId }: { noteId: string }) => {
  const [expanded, setExpanded] = useState(false);
  const [selected, setSelected] = useState<SynthesisSourceSupplement | null>(null);
  const trigger = useRef<HTMLElement | null>(null);
  const ledger = useSynthesisSupplements(noteId, expanded);
  const source = useSynthesisSupplementSource(noteId, selected);
  const items = ledger.data?.pages.flatMap((page) => page.items) ?? [];
  return <section className="synthesis-list-section" aria-label="后补来源">
    <Button variant="secondary" aria-expanded={expanded} aria-controls="synthesis-supplements" onClick={() => { setExpanded(!expanded); }}>后补来源</Button>
    {expanded ? <div id="synthesis-supplements">
      <p className="synthesis-help">这些资料在正文版本生成后补充，用于支持已有知识。各历史版本当时的引用保持不变。</p>
      {ledger.isPending ? <p role="status">正在读取补充来源…</p> : null}
      {ledger.isError ? <ErrorState description={ledger.error.message} onRetry={() => { void ledger.refetch(); }} /> : null}
      {ledger.isSuccess && items.length === 0 ? <p>还没有后补来源。</p> : null}
      <ul className="synthesis-source-links">{items.map((item) => <li key={item.id}>
        <Button variant="ghost" onClick={(event) => { trigger.current = event.currentTarget; setSelected(item); }}>{item.reference.title}，查看补充片段</Button>
        <Link to={`/authoring/notes/${noteId}?revision_id=${item.baseRevisionId}#synthesis-item-${item.itemId}`}>查看所支持的历史知识</Link>
      </li>)}</ul>
      {ledger.hasNextPage ? <Button disabled={ledger.isFetchingNextPage} onClick={() => { void ledger.fetchNextPage(); }}>{ledger.isFetchingNextPage ? "正在读取…" : "加载更多补充来源"}</Button> : null}
    </div> : null}
    <Dialog open={selected !== null} onOpenChange={(open) => { if (!open) setSelected(null); }} title={selected?.reference.title ?? "补充来源"} description="后补证据，独立于历史版本当时的引用。" restoreFocusRef={trigger} contentClassName="synthesis-source-dialog">
      {source.isPending && selected ? <p role="status">正在读取补充片段…</p> : null}
      {source.isError ? <ErrorState description={source.error.message} onRetry={() => { void source.refetch(); }} /> : null}
      {source.data?.availability === "AVAILABLE" ? <pre className="synthesis-source-excerpt">{source.data.text}</pre> : source.data?.snapshotText ? <><p className="synthesis-help">已保存的历史原文，已核验片段哈希。</p><pre className="synthesis-source-excerpt">{source.data.snapshotText}</pre></> : null}
      {source.data && source.data.availability !== "AVAILABLE" ? <div role="status"><Badge tone="warning">{source.data.availability === "STALE" ? "来源已变化" : "来源不可用"}</Badge><p>补充记录仍然保留。来源不可用不代表知识已被证伪。</p></div> : null}
      {selected ? <Link to={`/documents/${selected.reference.source.sourceVersionId}`}>查看资料版本</Link> : null}
    </Dialog>
  </section>;
};
