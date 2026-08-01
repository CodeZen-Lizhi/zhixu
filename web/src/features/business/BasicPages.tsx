/* eslint-disable @typescript-eslint/restrict-template-expressions, @typescript-eslint/no-unnecessary-condition */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FileText, GitBranch, MessageSquare, RefreshCw, Search } from "lucide-react";
import { useEffect } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";

import { getSourceVersion, listSourceVersions, type SourceIndexStatus, type SourceIngestionStatus, type WorkflowStatus } from "../../api/business";
import { scanWorkspace } from "../../api/workspace";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, Card, CardHeader, EmptyState, PageHeader, UnavailableState } from "../../shared/ui";
import { BusinessWorkspaceGate } from "./BusinessWorkspaceGate";
import { useScopedCursor } from "./pagination";
import { parseInboxUrlState, writeInboxUrlState, type InboxUrlState } from "./url-state";

const formatBytes = (size: number) => size < 1024 ? `${size} B` : size < 1_048_576 ? `${(size / 1024).toFixed(1)} KB` : `${(size / 1_048_576).toFixed(1)} MB`;
const sourceIndexLabel = (status: SourceIndexStatus | undefined) => status === "included" ? "included（该版本已纳入）" : status === "excluded" ? "excluded（来源已排除）" : "未选择";
const ingestionTone = (status: SourceIngestionStatus) => status === "parse_failed" || status === "cancelled" ? "danger" : status === "parsed" || status === "chunked" ? "success" : "info";
const workflowTone = (status: WorkflowStatus) => status === "failed" || status === "cancelled" ? "danger" : status === "succeeded" ? "success" : status === "waiting_for_human" ? "warning" : "info";

export const InboxPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const [params, setParams] = useSearchParams();
  const urlState = parseInboxUrlState(params);
  const canonicalSearch = writeInboxUrlState(urlState).toString();
  useEffect(() => {
    if (params.toString() !== canonicalSearch) setParams(new URLSearchParams(canonicalSearch), { replace: true });
  }, [canonicalSearch, params, setParams]);
  const { securityStatus, ingestionStatus, workflowStatus, indexStatus, mimeType } = urlState;
  const [cursor, setCursor] = useScopedCursor([workspaceId, securityStatus, ingestionStatus, workflowStatus, indexStatus, mimeType]);
  const query = useQuery({
    queryKey: ["business", workspaceId, "sources", "inbox", securityStatus, ingestionStatus, workflowStatus, indexStatus, mimeType, cursor],
    queryFn: ({ signal }) => listSourceVersions(workspaceId, { limit: 30, ...(cursor ? { cursor } : {}), ...(securityStatus ? { securityStatus } : {}), ...(ingestionStatus ? { ingestionStatus } : {}), ...(workflowStatus ? { workflowStatus } : {}), ...(indexStatus ? { indexStatus } : {}), ...(mimeType ? { mimeType } : {}) }, signal),
    enabled: workspaceId !== "",
    retry: false,
  });
  const queryClient = useQueryClient();
  const scan = useMutation({ mutationFn: () => scanWorkspace(workspaceId), onSuccess: () => queryClient.invalidateQueries({ queryKey: ["business", workspaceId] }) });
  const updateFilter = (updates: Partial<InboxUrlState>) => {
    setCursor("");
    setParams(writeInboxUrlState({ ...urlState, ...updates }));
  };

  if (workspaceId === "") return <BusinessWorkspaceGate description="连接工作区后才能读取资料版本。" />;

  return <div className="page-stack">
    <PageHeader title="资料收件箱" description="扫描本地目录并检查资料处理状态。" action={<Button onClick={() => scan.mutate()} disabled={scan.isPending} variant="secondary"><RefreshCw size={16} />{scan.isPending ? "扫描中…" : "重新扫描"}</Button>} />
    {scan.isError ? <div className="ui-state ui-state--error" role="alert"><strong>工作区扫描失败</strong><p>{scan.error.message}</p></div> : null}
    <Card>
      <div className="filter-bar" aria-label="资料筛选">
        <label>安全状态<select value={securityStatus} onChange={(event) => updateFilter({ securityStatus: event.target.value as InboxUrlState["securityStatus"] })}><option value="">全部</option><option value="pending">待检查</option><option value="passed">通过</option><option value="quarantined">隔离</option></select></label>
        <label>解析状态<select value={ingestionStatus} onChange={(event) => updateFilter({ ingestionStatus: event.target.value as InboxUrlState["ingestionStatus"] })}><option value="">全部</option><option value="validating">安全校验</option><option value="parsing">解析中</option><option value="parsed">已解析</option><option value="chunking">分块中</option><option value="chunked">已分块</option><option value="parse_failed">解析失败</option><option value="cancelled">已取消</option></select></label>
        <label>Workflow 状态<select value={workflowStatus} onChange={(event) => updateFilter({ workflowStatus: event.target.value as InboxUrlState["workflowStatus"] })}><option value="">全部</option><option value="pending">等待</option><option value="running">运行中</option><option value="waiting_for_human">等待人工</option><option value="retry_wait">等待重试</option><option value="paused">已暂停</option><option value="succeeded">成功</option><option value="failed">失败</option><option value="cancelled">已取消</option></select></label>
        <label>索引选择<select value={indexStatus} onChange={(event) => updateFilter({ indexStatus: event.target.value as InboxUrlState["indexStatus"] })}><option value="">全部</option><option value="included">已纳入</option><option value="excluded">已排除</option></select></label>
        <label>资料类型<select value={mimeType} onChange={(event) => updateFilter({ mimeType: event.target.value as InboxUrlState["mimeType"] })}><option value="">全部</option><option value="text/markdown">Markdown</option><option value="text/plain">纯文本</option><option value="text/html">HTML</option><option value="application/pdf">PDF</option></select></label>
      </div>
      <CardHeader title={query.isPending ? "读取中…" : `${query.data?.items.length ?? 0} 个版本`} />
      {query.isError ? <UnavailableState title="资料版本列表不可用" description={query.error.message} /> : query.data?.items.length === 0 ? <EmptyState title="暂无资料版本" description="扫描工作区后，资料版本会按捕获时间倒序出现。" /> : <div className="table-scroll"><table className="data-table"><thead><tr><th>文件</th><th>类型 / 大小</th><th>捕获时间</th><th>安全</th><th>解析 / 索引</th><th>Workflow</th></tr></thead><tbody>{query.data?.items.map((item) => <tr key={item.id}><td><Link className="table-link" to={`/documents/${item.id}`}><FileText size={15} />{item.path}</Link><small className="mono">{item.contentHash.slice(0, 12)}…</small></td><td>{item.mimeType}<br /><span className="muted">{formatBytes(item.byteSize)}</span></td><td>{new Date(item.capturedAt).toLocaleString("zh-CN")}</td><td><Badge tone={item.securityStatus === "passed" ? "success" : item.securityStatus === "quarantined" ? "danger" : "warning"}>{item.securityStatus}</Badge></td><td>{item.ingestionStatus ?? "未开始"} / {item.indexStatus === "excluded" ? "来源已排除" : item.indexStatus === "included" ? "该版本已纳入" : "未选择"}</td><td>{item.workflowStatus ? <Badge tone={workflowTone(item.workflowStatus)}>{item.workflowStatus}</Badge> : <span className="muted">未绑定</span>}</td></tr>)}</tbody></table></div>}
      <div className="pagination-row">{cursor ? <Button variant="ghost" onClick={() => setCursor("")}>返回首屏</Button> : <span />}{query.data?.nextCursor ? <Button variant="secondary" onClick={() => setCursor(query.data.nextCursor ?? "")}>下一页</Button> : null}</div>
    </Card>
  </div>;
};

export const DocumentsPage = () => {
  const { sourceVersionId } = useParams();
  const workspaceId = useActiveWorkspaceId();
  const query = useQuery({
    queryKey: ["business", workspaceId, "sources", "detail", sourceVersionId],
    queryFn: ({ signal }) => getSourceVersion(workspaceId, sourceVersionId ?? "", signal),
    enabled: Boolean(workspaceId && sourceVersionId),
    retry: false,
  });

  if (!sourceVersionId) return <InboxPage />;
  if (workspaceId === "") return <BusinessWorkspaceGate description="连接工作区后才能读取资料版本。" />;

  return <div className="page-stack">
    <PageHeader title={query.data?.path ?? "资料版本"} description="不可变版本元数据。" />
    {query.isError ? <UnavailableState title="资料详情不可用" description={query.error.message} /> : query.isPending ? <Card><p>正在查询资料版本…</p></Card> : query.data ? <Card>
      <CardHeader title="版本元数据" />
      <dl className="detail-grid"><div><dt>Source Version</dt><dd className="mono">{query.data.id}</dd></div><div><dt>Content Hash</dt><dd className="mono">{query.data.contentHash}</dd></div><div><dt>Mime / 大小</dt><dd>{query.data.mimeType} · {formatBytes(query.data.byteSize)}</dd></div><div><dt>安全状态</dt><dd><Badge tone={query.data.securityStatus === "passed" ? "success" : "warning"}>{query.data.securityStatus}</Badge></dd></div><div><dt>Parse 状态</dt><dd>{query.data.ingestionStatus ? <Badge tone={ingestionTone(query.data.ingestionStatus)}>{query.data.ingestionStatus}</Badge> : <span className="muted">未开始</span>}</dd></div><div><dt>Workflow 状态</dt><dd>{query.data.workflowStatus ? <Badge tone={workflowTone(query.data.workflowStatus)}>{query.data.workflowStatus}</Badge> : <span className="muted">未绑定</span>}</dd></div><div><dt>Index 状态</dt><dd>{query.data.indexStatus ? <Badge tone={query.data.indexStatus === "included" ? "success" : "warning"}>{sourceIndexLabel(query.data.indexStatus)}</Badge> : <span className="muted">未选择</span>}</dd></div></dl>
      <div className="document-actions"><Button asChild variant="secondary"><Link to={`/search?source_version_id=${encodeURIComponent(query.data.id)}&scope_workspace=${encodeURIComponent(workspaceId)}`}><Search size={15} />在 Search 中限定此版本</Link></Button><Button asChild variant="secondary"><Link to="/chat"><MessageSquare size={15} />进入 Chat</Link></Button><Button asChild variant="secondary"><Link to="/graph"><GitBranch size={15} />进入 Graph</Link></Button></div>
      <UnavailableState title="正文预览尚未开放" description="可在检索中限定此资料版本并打开受控证据。" />
    </Card> : <EmptyState title="找不到这个版本" description="请回到资料收件箱重新选择。" />}
  </div>;
};
