import {
  ArrowLeft,
  ArrowRightLeft,
  CheckCircle2,
  Clock3,
  ExternalLink,
  FileClock,
  GitBranch,
  GitCommitHorizontal,
  History,
  RefreshCw,
  RotateCcw,
  ShieldCheck,
  TriangleAlert,
  UserRound,
} from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";

import {
  DocumentHistoryApiError,
  documentWorktreeRef,
  isDocumentHistoryId,
  type CreateDocumentRestoreProposalInput,
  type DocumentHistoryEntry,
} from "../../api/document-history";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { useRegisterWorkspaceRecovery } from "../../events/event-store";
import { Badge, Button, Dialog, EmptyState, ErrorState, PageHeader } from "../../shared/ui";
import {
  useCreateDocumentRestorePreview,
  useCreateDocumentRestoreProposal,
  useDocumentHistoryRecovery,
  useDocumentHistoryCompare,
  useDocumentHistoryPage,
} from "./queries";
import "./document-history.css";

const MonacoDiffViewer = lazy(() => import("../../shared/MonacoDiffViewer").then((module) => ({ default: module.MonacoDiffViewer })));

interface VersionOption {
  ref: string;
  kind: DocumentHistoryEntry["kind"] | "WORKTREE";
  summary: string;
  committedAt: string | null;
}

const historyLimit = 30;
const shortRef = (value: string): string => value === documentWorktreeRef ? "WORKTREE" : value.slice(0, 10);
const formatTime = (value: string): string => new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
}).format(new Date(value));

const errorText = (error: unknown): string => error instanceof DocumentHistoryApiError
  ? `${error.message}（${error.errorCode}）`
  : error instanceof Error ? error.message : "文档历史请求未完成。";

const hasErrorCode = (error: unknown, code: string): boolean =>
  error instanceof DocumentHistoryApiError && error.errorCode === code;

const hasAnyErrorCode = (error: unknown, codes: readonly string[]): boolean =>
  error instanceof DocumentHistoryApiError && codes.includes(error.errorCode);

const baselineErrorCodes = [
  "DOCUMENT_HISTORY_CURSOR_INVALID",
  "DOCUMENT_HISTORY_CURSOR_STALE",
  "DOCUMENT_HISTORY_DETACHED_HEAD",
  "DOCUMENT_RESTORE_STALE",
] as const;

const restoreBlockedErrorCodes = [
  ...baselineErrorCodes,
  "DOCUMENT_RESTORE_DIRTY_WORKTREE",
] as const;

const entryOption = (entry: DocumentHistoryEntry): VersionOption => ({
  ref: entry.commit ?? documentWorktreeRef,
  kind: entry.kind,
  summary: entry.summary,
  committedAt: entry.committedAt,
});

const optionLabel = (option: VersionOption): string => option.ref === documentWorktreeRef
  ? `当前工作树 · ${option.summary}`
  : `${shortRef(option.ref)} · ${option.summary}`;

const VersionIdentity = ({ label, option }: { label: string; option: VersionOption | undefined }) => <div className="history-version-identity">
  <span>{label}</span>
  <strong>{option === undefined ? "尚未选择" : option.ref === documentWorktreeRef ? "当前工作树" : shortRef(option.ref)}</strong>
  <small>{option?.summary ?? "选择一个可读取版本"}</small>
  {option?.committedAt ? <time dateTime={option.committedAt}>{formatTime(option.committedAt)}</time> : null}
</div>;

const HistoryEntry = ({
  entry,
  head,
  dirty,
  onRestore,
}: {
  entry: DocumentHistoryEntry;
  head: string;
  dirty: boolean;
  onRestore: (entry: DocumentHistoryEntry, trigger: HTMLButtonElement) => void;
}) => {
  const isHead = entry.commit === head;
  return <article className={`history-entry history-entry--${entry.kind.toLowerCase()}`}>
    <div className="history-entry__rail" aria-hidden="true"><span /></div>
    <div className="history-entry__body">
      <header>
        <div className="history-entry__title">
          {entry.kind === "MANAGED" ? <ShieldCheck size={18} /> : entry.kind === "EXTERNAL" ? <ExternalLink size={18} /> : <FileClock size={18} />}
          <div>
            <strong>{entry.summary}</strong>
            <span className="history-entry__badges">
              <Badge tone={entry.kind === "MANAGED" ? "success" : entry.kind === "EXTERNAL" ? "warning" : "info"}>
                {entry.kind === "MANAGED" ? "受控写回" : entry.kind === "EXTERNAL" ? "外部变更" : "当前未提交改动"}
              </Badge>
              {isHead ? <Badge tone="neutral">HEAD</Badge> : null}
            </span>
          </div>
        </div>
        {entry.commit !== null ? <Button
          variant="secondary"
          size="sm"
          disabled={dirty || isHead}
          title={dirty ? "工作树存在未提交改动，不能创建恢复提案" : isHead ? "当前已经是此版本" : "先预览反向 Diff，再创建恢复提案"}
          onClick={(event) => onRestore(entry, event.currentTarget)}
        ><RotateCcw size={15} />恢复此版本</Button> : null}
      </header>

      <div className="history-entry__commit">
        <GitCommitHorizontal size={15} />
        <code>{entry.commit ?? documentWorktreeRef}</code>
      </div>
      {entry.committedAt !== null ? <div className="history-entry__meta">
        <span><Clock3 size={14} /><time dateTime={entry.committedAt}>{formatTime(entry.committedAt)}</time></span>
        <span><UserRound size={14} />{entry.authorName || "未知作者"}{entry.authorEmail ? ` · ${entry.authorEmail}` : ""}</span>
      </div> : <p className="history-entry__note">这不是历史 Commit，只表示当前 canonical 文件尚未提交的变化。</p>}

      {entry.kind === "MANAGED" ? <dl className="history-entry__bindings">
        <div><dt>Article Revision</dt><dd>{entry.articleRevisionId === null ? "未关联" : <><code>{entry.articleRevisionId}</code>{entry.articleRevisionNo === null ? null : <span> · #{String(entry.articleRevisionNo)}</span>}</>}</dd></div>
        <div><dt>Proposal</dt><dd>{entry.proposalId === null ? "未关联" : <Link to={`/proposals/${entry.proposalId}`}>{entry.proposalType ?? "Proposal"} · {entry.proposalId}</Link>}</dd></div>
        <div><dt>Approval</dt><dd>{entry.approvalId === null ? "未关联" : <><code>{entry.approvalId}</code>{entry.approvalDecidedAt === null ? null : <time dateTime={entry.approvalDecidedAt}> · {formatTime(entry.approvalDecidedAt)}</time>}</>}</dd></div>
        <div><dt>Workflow / Writeback</dt><dd>
          {entry.workflowRunId === null ? <span>Workflow 未关联</span> : <Link to={`/workflows/${entry.workflowRunId}`}>{entry.workflowRunId}</Link>}
          <span> · </span>
          {entry.writebackId === null ? <span>Writeback 未关联</span> : <code>{entry.writebackId}</code>}
        </dd></div>
      </dl> : entry.kind === "EXTERNAL" ? <p className="history-entry__note">未发现知序的 Revision、审批或写回映射；不会补造旧关系。</p> : null}
    </div>
  </article>;
};

const DocumentHistoryPageScope = ({ workspaceId, documentId }: { workspaceId: string; documentId: string }) => {
  const [scopeHead, setScopeHead] = useState("");
  const [scopePath, setScopePath] = useState("");
  const [scopeDocumentVersion, setScopeDocumentVersion] = useState(0);
  const [cursorStack, setCursorStack] = useState([""]);
  const cursor = cursorStack[cursorStack.length - 1] ?? "";
  const historyQuery = useDocumentHistoryPage(documentId, scopeHead, cursor, historyLimit);
  const [knownVersions, setKnownVersions] = useState<VersionOption[]>([]);
  const [leftRef, setLeftRef] = useState("");
  const [rightRef, setRightRef] = useState<string>(documentWorktreeRef);
  const [submittedCompare, setSubmittedCompare] = useState<{ documentId: string; left: string; right: string }>();
  const compareQuery = useDocumentHistoryCompare(submittedCompare, scopeHead, scopePath, scopeDocumentVersion);
  const [compareViewerError, setCompareViewerError] = useState<Error>();
  const [restoreViewerError, setRestoreViewerError] = useState<Error>();
  const [restoreTarget, setRestoreTarget] = useState<DocumentHistoryEntry>();
  const restoreTriggerRef = useRef<HTMLElement | null>(null);
  const preview = useCreateDocumentRestorePreview();
  const proposal = useCreateDocumentRestoreProposal();
  const [proposalCommand, setProposalCommand] = useState<CreateDocumentRestoreProposalInput>();

  useEffect(() => {
    const page = historyQuery.data;
    if (page === undefined) return;
    if (scopeHead === "") {
      setScopeHead(page.head);
      setScopePath(page.path);
      setScopeDocumentVersion(page.documentVersion);
    } else if (page.head !== scopeHead || page.path !== scopePath || page.documentVersion !== scopeDocumentVersion) {
      setScopeHead(page.head);
      setScopePath(page.path);
      setScopeDocumentVersion(page.documentVersion);
      setCursorStack([""]);
      setKnownVersions([]);
      setLeftRef("");
      setRightRef(documentWorktreeRef);
      setSubmittedCompare(undefined);
      setRestoreTarget(undefined);
      setProposalCommand(undefined);
      return;
    }
    const pageOptions = [
      { ref: documentWorktreeRef, kind: "WORKTREE" as const, summary: page.dirty ? "包含未提交改动" : "与当前 HEAD 一致", committedAt: null },
      ...page.items.filter((item) => item.commit !== null).map(entryOption),
    ];
    setKnownVersions((current) => {
      const merged = new Map(current.map((item) => [item.ref, item]));
      for (const item of pageOptions) merged.set(item.ref, item);
      return [...merged.values()];
    });
    if (leftRef === "") {
      const firstCommit = page.items.find((item) => item.commit !== null)?.commit;
      if (firstCommit !== undefined) setLeftRef(firstCommit);
    }
  }, [historyQuery.data, leftRef, scopeDocumentVersion, scopeHead, scopePath]);

  useEffect(() => {
    setCompareViewerError(undefined);
  }, [compareQuery.data?.diffHash]);

  useEffect(() => {
    const value = preview.data;
    if (value === undefined) return;
    setRestoreViewerError(undefined);
    setProposalCommand({
      workspaceId: value.workspaceId,
      documentId: value.documentId,
      targetCommit: value.targetCommit,
      expectedHead: value.expectedHead,
      expectedDocumentVersion: value.expectedDocumentVersion,
      previewHash: value.previewHash,
      idempotencyKey: `restore-document-${crypto.randomUUID()}`,
    });
  }, [preview.data]);

  const versionByRef = useMemo(() => new Map(knownVersions.map((option) => [option.ref, option])), [knownVersions]);
  const compareReady = leftRef !== "" && rightRef !== "" && leftRef !== rightRef && scopeHead !== "";
  const cursorInvalid = hasErrorCode(historyQuery.error, "DOCUMENT_HISTORY_CURSOR_INVALID");
  const cursorStale = hasErrorCode(historyQuery.error, "DOCUMENT_HISTORY_CURSOR_STALE");
  const cursorResetRequired = cursorInvalid || cursorStale;
  const historyDetached = hasErrorCode(historyQuery.error, "DOCUMENT_HISTORY_DETACHED_HEAD");
  const compareBaselineChanged = hasAnyErrorCode(compareQuery.error, baselineErrorCodes);
  const previewBlocked = hasAnyErrorCode(preview.error, restoreBlockedErrorCodes);
  const proposalBlocked = hasAnyErrorCode(proposal.error, [...restoreBlockedErrorCodes, "IDEMPOTENCY_KEY_REUSED"]);
  const page = historyQuery.data;

  const resetHistory = useCallback((): void => {
    setCursorStack([""]);
    setScopeHead("");
    setScopePath("");
    setScopeDocumentVersion(0);
    setKnownVersions([]);
    setLeftRef("");
    setRightRef(documentWorktreeRef);
    setSubmittedCompare(undefined);
  }, []);

  const openRestore = (entry: DocumentHistoryEntry, trigger: HTMLButtonElement): void => {
    if (entry.commit === null || page === undefined || page.dirty) return;
    restoreTriggerRef.current = trigger;
    setRestoreTarget(entry);
    setProposalCommand(undefined);
    setRestoreViewerError(undefined);
    preview.reset();
    proposal.reset();
    preview.mutate({
      workspaceId,
      documentId,
      targetCommit: entry.commit,
      expectedHead: page.head,
      expectedDocumentVersion: page.documentVersion,
    });
  };

  const closeRestore = (): void => {
    setRestoreTarget(undefined);
    setProposalCommand(undefined);
    setRestoreViewerError(undefined);
    preview.reset();
    proposal.reset();
  };

  const recoverLatestBaseline = (): void => {
    closeRestore();
    resetHistory();
  };

  const resetHistoryForRecovery = useCallback((): void => {
    setRestoreTarget(undefined);
    setProposalCommand(undefined);
    setRestoreViewerError(undefined);
    preview.reset();
    proposal.reset();
    resetHistory();
  }, [preview, proposal, resetHistory]);
  const recoverWorkspaceHistory = useDocumentHistoryRecovery(documentId, historyLimit, resetHistoryForRecovery);
  useRegisterWorkspaceRecovery(recoverWorkspaceHistory);

  if (workspaceId === "") return <div className="page-stack document-history-page"><PageHeader title="文档历史" /><EmptyState title="先连接 Workspace" description="连接工作区后才能读取受控文件历史。" action={<Button asChild><Link to="/workspace">连接 Workspace</Link></Button>} /></div>;
  if (!isDocumentHistoryId(documentId)) return <div className="page-stack document-history-page"><PageHeader title="文档历史" /><ErrorState title="文档地址无效" description="当前链接没有包含合法的 Document ID。" /></div>;

  return <div className="page-stack document-history-page">
    <PageHeader
      title="文档历史"
      description="查看当前分支的文件版本，比较任意已读取版本，并通过新 Proposal 恢复。"
      action={<Button asChild variant="ghost"><Link to="/authoring"><ArrowLeft size={16} />返回创作</Link></Button>}
    />

    {historyQuery.isPending ? <div className="history-loading" role="status"><History size={19} />正在读取当前分支历史…</div> : null}
    {historyQuery.isError ? <ErrorState
      title={cursorInvalid ? "历史游标已失效" : cursorStale ? "历史基线已变化" : historyDetached ? "当前不在可读取分支" : hasErrorCode(historyQuery.error, "DOCUMENT_HISTORY_GIT_UNAVAILABLE") ? "Git 历史暂不可用" : "文档历史读取失败"}
      description={cursorInvalid ? "分页凭据已过期或不再有效。请从最新首屏重新开始。" : cursorStale ? "HEAD、分支或文档路径已经变化。请从最新首屏重新开始。" : historyDetached ? "Detached HEAD 下不会读取或恢复文档版本。切回一个分支后再重试。" : errorText(historyQuery.error)}
      onRetry={cursorResetRequired || historyDetached ? resetHistory : () => { void historyQuery.refetch(); }}
    /> : null}

    {page ? <>
      <section className="history-baseline" aria-label="当前文件基线">
        <div className="history-baseline__path"><span>Canonical path</span><strong>{page.path}</strong></div>
        <dl>
          <div><dt><GitBranch size={14} />当前分支</dt><dd>{page.branch}</dd></div>
          <div><dt><GitCommitHorizontal size={14} />读取 HEAD</dt><dd><code>{page.head}</code></dd></div>
          <div><dt>Document version</dt><dd>v{String(page.documentVersion)}</dd></div>
          <div><dt>工作树</dt><dd><Badge tone={page.dirty ? "danger" : "success"}>{page.dirty ? "存在未提交改动" : "干净"}</Badge></dd></div>
        </dl>
        {page.dirty ? <p className="history-baseline__warning"><TriangleAlert size={16} />恢复已暂停，避免覆盖当前未提交内容；比较仍然可用。</p> : null}
      </section>

      <section className="history-compare" aria-labelledby="history-compare-heading">
        <header>
          <div><p className="eyebrow">Compare</p><h2 id="history-compare-heading">版本比较</h2></div>
          <Button variant="secondary" size="sm" title="交换左右版本" aria-label="交换左右版本" disabled={!compareReady} onClick={() => {
            setLeftRef(rightRef);
            setRightRef(leftRef);
            setSubmittedCompare(undefined);
          }}><ArrowRightLeft size={16} /></Button>
        </header>
        <div className="history-compare__selectors">
          <label>左侧版本<select value={leftRef} onChange={(event) => { setLeftRef(event.target.value); setSubmittedCompare(undefined); }}>
            <option value="">选择版本</option>
            {knownVersions.map((option) => <option value={option.ref} key={`left-${option.ref}`}>{optionLabel(option)}</option>)}
          </select></label>
          <label>右侧版本<select value={rightRef} onChange={(event) => { setRightRef(event.target.value); setSubmittedCompare(undefined); }}>
            <option value="">选择版本</option>
            {knownVersions.map((option) => <option value={option.ref} key={`right-${option.ref}`}>{optionLabel(option)}</option>)}
          </select></label>
          <Button disabled={!compareReady} onClick={() => setSubmittedCompare({ documentId, left: leftRef, right: rightRef })}>比较版本</Button>
        </div>
        <div className="history-version-pair" aria-label="比较版本身份">
          <VersionIdentity label="左侧 / 原始" option={versionByRef.get(submittedCompare?.left ?? leftRef)} />
          <VersionIdentity label="右侧 / 目标" option={versionByRef.get(submittedCompare?.right ?? rightRef)} />
        </div>
        {compareQuery.isFetching ? <p className="history-inline-state" role="status">正在生成完整 Diff…</p> : null}
        {compareQuery.isError ? <ErrorState title={compareBaselineChanged ? "比较基线已变化" : "版本比较失败"} description={errorText(compareQuery.error)} onRetry={compareBaselineChanged ? resetHistory : () => { void compareQuery.refetch(); }} /> : null}
        {compareQuery.data ? <div className="history-diff" aria-label="版本比较结果">
          <p className="history-diff__summary">左侧 {shortRef(compareQuery.data.left)}，右侧 {shortRef(compareQuery.data.right)}。Diff hash <code>{compareQuery.data.diffHash}</code>。</p>
          {compareViewerError ? <ErrorState title="Diff Viewer 加载失败" description={compareViewerError.message} onRetry={() => setCompareViewerError(undefined)} /> : <Suspense fallback={<p className="history-inline-state">正在加载 Diff Viewer…</p>}>
            <MonacoDiffViewer
              identityKey={`${workspaceId}:${documentId}:${compareQuery.data.head}:${compareQuery.data.diffHash}`}
              original={compareQuery.data.leftContent}
              modified={compareQuery.data.rightContent}
              originalModelPath={`inmemory://zhixu/workspaces/${workspaceId}/documents/${documentId}/history/${compareQuery.data.diffHash}/left.md`}
              modifiedModelPath={`inmemory://zhixu/workspaces/${workspaceId}/documents/${documentId}/history/${compareQuery.data.diffHash}/right.md`}
              language="markdown"
              height="min(56vh, 560px)"
              onReady={() => setCompareViewerError(undefined)}
              onError={setCompareViewerError}
            />
          </Suspense>}
          <details><summary>屏幕阅读器与纯文本补丁</summary><pre>{compareQuery.data.patch || "两个版本没有文本变化。"}</pre></details>
        </div> : null}
      </section>

      <section className="history-timeline" aria-labelledby="history-timeline-heading">
        <header><div><p className="eyebrow">Current branch</p><h2 id="history-timeline-heading">版本时间线</h2></div><span>{historyQuery.isFetching ? "正在刷新" : `${String(page.items.length)} 项`}</span></header>
        {page.items.length === 0 ? <EmptyState title="当前路径还没有历史版本" description="文件第一次受控写回后会出现在这里。" /> : <div className="history-timeline__list">{page.items.map((entry) => <HistoryEntry key={entry.commit ?? documentWorktreeRef} entry={entry} head={page.head} dirty={page.dirty} onRestore={openRestore} />)}</div>}
        <nav className="history-pagination" aria-label="文档历史分页">
          <Button variant="ghost" disabled={cursorStack.length <= 1 || historyQuery.isFetching} onClick={() => setCursorStack((current) => current.slice(0, -1))}><ArrowLeft size={16} />上一页</Button>
          <span>第 {String(cursorStack.length)} 页</span>
          <Button variant="secondary" disabled={page.nextCursor === undefined || historyQuery.isFetching} onClick={() => {
            if (page.nextCursor !== undefined) setCursorStack((current) => [...current, page.nextCursor ?? ""]);
          }}>下一页</Button>
        </nav>
      </section>
    </> : null}

    <Dialog
      open={restoreTarget !== undefined}
      onOpenChange={(open) => { if (!open) closeRestore(); }}
      title="恢复为历史版本"
      description="恢复不会移动 Git 历史；批准后会通过 Safe Writeback 追加一个新 Commit。"
      restoreFocusRef={restoreTriggerRef}
      contentClassName="history-restore-shell"
    >
      <div className="history-restore-dialog">
        {restoreTarget?.commit ? <div className="history-restore-target"><span>目标 Commit</span><code>{restoreTarget.commit}</code><strong>{restoreTarget.summary}</strong></div> : null}
        {preview.isPending ? <p className="history-inline-state" role="status"><RefreshCw size={16} />正在重验 HEAD、Document version 与目标 Blob…</p> : null}
        {preview.isError ? <ErrorState
          title={hasErrorCode(preview.error, "DOCUMENT_RESTORE_DIRTY_WORKTREE") ? "工作树不干净" : hasErrorCode(preview.error, "DOCUMENT_RESTORE_STALE") ? "恢复基线已变化" : "恢复预览失败"}
          description={errorText(preview.error)}
          {...(previewBlocked
            ? { onRetry: recoverLatestBaseline }
            : restoreTarget?.commit === null || restoreTarget?.commit === undefined || page === undefined
              ? {}
              : { onRetry: () => preview.mutate({ workspaceId, documentId, targetCommit: restoreTarget.commit, expectedHead: page.head, expectedDocumentVersion: page.documentVersion }) })}
        /> : null}
        {preview.data ? <>
          {preview.data.blockedByDirtyWorktree ? <div className="history-restore-blocked" role="alert"><TriangleAlert size={17} /><div><strong>当前存在未提交改动</strong><p>预览可保留，但不能创建恢复 Proposal。</p></div></div> : null}
          <div className="history-version-pair">
            <div className="history-version-identity"><span>左侧 / 当前 HEAD</span><strong>{shortRef(preview.data.expectedHead)}</strong><small>当前受控内容</small></div>
            <div className="history-version-identity"><span>右侧 / 恢复目标</span><strong>{shortRef(preview.data.targetCommit)}</strong><small>{restoreTarget?.summary ?? "历史内容"}</small></div>
          </div>
          <p className="history-diff__summary">这是相对当前版本的反向 Diff。Preview hash <code>{preview.data.previewHash}</code>。</p>
          {restoreViewerError ? <ErrorState title="Diff Viewer 加载失败" description={restoreViewerError.message} onRetry={() => setRestoreViewerError(undefined)} /> : <Suspense fallback={<p className="history-inline-state">正在加载 Diff Viewer…</p>}>
            <div className="history-restore-diff"><MonacoDiffViewer
              identityKey={`${workspaceId}:${documentId}:restore:${preview.data.previewHash}`}
              original={preview.data.currentContent}
              modified={preview.data.targetContent}
              originalModelPath={`inmemory://zhixu/workspaces/${workspaceId}/documents/${documentId}/restore/${preview.data.previewHash}/current.md`}
              modifiedModelPath={`inmemory://zhixu/workspaces/${workspaceId}/documents/${documentId}/restore/${preview.data.previewHash}/target.md`}
              language="markdown"
              height="min(48vh, 480px)"
              onReady={() => setRestoreViewerError(undefined)}
              onError={setRestoreViewerError}
            /></div>
          </Suspense>}
          <details><summary>屏幕阅读器与纯文本补丁</summary><pre>{preview.data.patch || "目标版本与当前文件没有文本变化。"}</pre></details>
          {proposal.isError ? <ErrorState title={hasErrorCode(proposal.error, "DOCUMENT_RESTORE_STALE") ? "恢复预览已过期" : hasErrorCode(proposal.error, "IDEMPOTENCY_KEY_REUSED") ? "恢复意图键已被占用" : "Proposal 创建结果未确认"} description={errorText(proposal.error)} {...(proposalBlocked ? { onRetry: recoverLatestBaseline } : proposalCommand === undefined ? {} : { onRetry: () => proposal.mutate(proposalCommand) })} /> : null}
          {proposal.data ? <div className="history-restore-success" role="status"><CheckCircle2 size={18} /><div><strong>{proposal.data.replayed ? "已恢复原创建结果" : "恢复 Proposal 已创建"}</strong><p>正式文件尚未改变，仍需在提案中审批并执行。</p><Button asChild size="sm"><Link to={`/proposals/${proposal.data.proposalId}`}>打开 Proposal</Link></Button></div></div> : <div className="history-restore-actions">
            <Button variant="ghost" onClick={closeRestore}>取消</Button>
            <Button
              disabled={proposalCommand === undefined || preview.data.blockedByDirtyWorktree || proposal.isPending || proposal.isError || preview.data.expectedHead !== scopeHead || preview.data.expectedDocumentVersion !== page?.documentVersion}
              title={preview.data.blockedByDirtyWorktree ? "先处理当前未提交改动" : "创建受控恢复 Proposal"}
              onClick={() => { if (proposalCommand !== undefined) proposal.mutate(proposalCommand); }}
            >{proposal.isPending ? "正在创建…" : "创建恢复 Proposal"}</Button>
          </div>}
        </> : null}
      </div>
    </Dialog>
  </div>;
};

export const DocumentHistoryPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const { documentId = "" } = useParams<{ documentId: string }>();
  return <DocumentHistoryPageScope
    key={`${workspaceId}:${documentId}`}
    workspaceId={workspaceId}
    documentId={documentId}
  />;
};
