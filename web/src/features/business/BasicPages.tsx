/* eslint-disable @typescript-eslint/restrict-template-expressions, @typescript-eslint/no-unnecessary-condition */
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FileText, GitBranch, KeyRound, MessageSquare, RefreshCw, Search, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { getSourceVersion, listSourceVersions, type SourceIndexStatus, type SourceIngestionStatus, type WorkflowStatus } from "../../api/business";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { useAuth } from "../../app/auth-context";
import { Button, Card, CardHeader, Badge, EmptyState, Tabs, TabsContent, TabsList, TabsTrigger, UnavailableState } from "../../shared/ui";
import { getWorkspace, scanWorkspace } from "../../api/workspace";
import { createApiToken, listApiTokens, revokeApiToken, type AuthCapability, type ApiTokenCredential } from "../../api/auth";
import { SystemStatusPage } from "../system-status/SystemStatusPage";
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
  const [cursor, setCursor] = useScopedCursor([
    workspaceId,
    securityStatus,
    ingestionStatus,
    workflowStatus,
    indexStatus,
    mimeType,
  ]);
  const query = useQuery({
    queryKey: ["business", workspaceId, "sources", "inbox", securityStatus, ingestionStatus, workflowStatus, indexStatus, mimeType, cursor],
    queryFn: ({ signal }) => listSourceVersions(workspaceId, { limit: 30, ...(cursor ? { cursor } : {}), ...(securityStatus ? { securityStatus } : {}), ...(ingestionStatus ? { ingestionStatus } : {}), ...(workflowStatus ? { workflowStatus } : {}), ...(indexStatus ? { indexStatus } : {}), ...(mimeType ? { mimeType } : {}) }, signal),
    enabled: workspaceId !== "", retry: false,
  });
  const qc = useQueryClient();
  const scan = useMutation({ mutationFn: () => scanWorkspace(workspaceId), onSuccess: () => qc.invalidateQueries({ queryKey: ["business", workspaceId] }) });
  const updateFilter = (updates: Partial<InboxUrlState>) => {
    setCursor("");
    setParams(writeInboxUrlState({ ...urlState, ...updates }));
  };
  if (!workspaceId) return <BusinessWorkspaceGate description="Inbox 只能读取已连接 Workspace 的真实 Source Version；页面不会停在无请求的伪加载状态。" />;
  return <div className="page-stack">
    <div className="page-intro page-intro--split"><div><p className="eyebrow">资料摄取 / Inbox</p><h2>原始资料的版本线。</h2><p>这里展示 Source Version，不把它误称为正式 Document；解析、索引和安全状态来自真实投影。</p></div><Button onClick={() => scan.mutate()} disabled={!workspaceId || scan.isPending} variant="secondary"><RefreshCw size={16} />{scan.isPending ? "扫描中…" : "重新扫描"}</Button></div>
    {scan.isError ? <div className="ui-state ui-state--error" role="alert"><strong>Workspace 扫描失败</strong><p>{scan.error.message}</p></div> : null}
    <Card>
      <div className="filter-bar" aria-label="资料筛选">
        <label>安全状态<select value={securityStatus} onChange={(event) => updateFilter({ securityStatus: event.target.value as InboxUrlState["securityStatus"] })}><option value="">全部</option><option value="pending">待检查</option><option value="passed">通过</option><option value="quarantined">隔离</option></select></label>
        <label>解析状态<select value={ingestionStatus} onChange={(event) => updateFilter({ ingestionStatus: event.target.value as InboxUrlState["ingestionStatus"] })}><option value="">全部</option><option value="validating">安全校验</option><option value="parsing">解析中</option><option value="parsed">已解析</option><option value="chunking">分块中</option><option value="chunked">已分块</option><option value="parse_failed">解析失败</option><option value="cancelled">已取消</option></select></label>
        <label>Workflow 状态<select value={workflowStatus} onChange={(event) => updateFilter({ workflowStatus: event.target.value as InboxUrlState["workflowStatus"] })}><option value="">全部</option><option value="pending">等待</option><option value="running">运行中</option><option value="waiting_for_human">等待人工</option><option value="retry_wait">等待重试</option><option value="paused">已暂停</option><option value="succeeded">成功</option><option value="failed">失败</option><option value="cancelled">已取消</option></select></label>
        <label>索引选择<select value={indexStatus} onChange={(event) => updateFilter({ indexStatus: event.target.value as InboxUrlState["indexStatus"] })}><option value="">全部</option><option value="included">已纳入</option><option value="excluded">已排除</option></select></label>
        <label>资料类型<select value={mimeType} onChange={(event) => updateFilter({ mimeType: event.target.value as InboxUrlState["mimeType"] })}><option value="">全部</option><option value="text/markdown">Markdown</option><option value="text/plain">纯文本</option><option value="text/html">HTML</option><option value="application/pdf">PDF</option></select></label>
      </div>
      <CardHeader eyebrow="Workspace 资料" title={query.isPending ? "读取中…" : `${query.data?.items.length ?? 0} 个版本`} />
      {query.isError ? <UnavailableState title="Source Version 列表不可用" description={query.error.message} /> : query.data?.items.length === 0 ? <EmptyState title="暂无资料版本" description="扫描 Workspace 后，真实 Source Version 会按捕获时间倒序出现。" /> : <div className="table-scroll"><table className="data-table"><thead><tr><th>文件</th><th>类型 / 大小</th><th>捕获时间</th><th>安全</th><th>解析 / 索引</th><th>Workflow</th></tr></thead><tbody>{query.data?.items.map((item) => <tr key={item.id}><td><Link className="table-link" to={`/documents/${item.id}`}><FileText size={15} />{item.path}</Link><small className="mono">{item.contentHash.slice(0, 12)}…</small></td><td>{item.mimeType}<br /><span className="muted">{formatBytes(item.byteSize)}</span></td><td>{new Date(item.capturedAt).toLocaleString("zh-CN")}</td><td><Badge tone={item.securityStatus === "passed" ? "success" : item.securityStatus === "quarantined" ? "danger" : "warning"}>{item.securityStatus}</Badge></td><td>{item.ingestionStatus ?? "未开始"} / {item.indexStatus === "excluded" ? "来源已排除" : item.indexStatus === "included" ? "该版本已纳入" : "未选择"}</td><td>{item.workflowStatus ? <Badge tone={item.workflowStatus === "failed" ? "danger" : item.workflowStatus === "succeeded" ? "success" : item.workflowStatus === "waiting_for_human" ? "warning" : "info"}>{item.workflowStatus}</Badge> : <span className="muted">未绑定</span>}</td></tr>)}</tbody></table></div>}
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
  if (!workspaceId) return <BusinessWorkspaceGate description="资料版本详情必须绑定已连接 Workspace；未连接时不会发出详情请求。" />;
	return <div className="page-stack"><div className="page-intro"><p className="eyebrow">资料版本详情</p><h2>{query.data?.path ?? "读取资料版本"}</h2><p>当前接口只公开不可变元数据；标准化正文预览与正式 Document 聚合尚未交付。</p></div>{query.isError ? <UnavailableState title="资料详情不可用" description={query.error.message} /> : query.isPending ? <Card><p>正在查询 Source Version…</p></Card> : query.data ? <Card><CardHeader eyebrow="不可变事实" title="版本元数据" /><dl className="detail-grid"><div><dt>Source Version</dt><dd className="mono">{query.data.id}</dd></div><div><dt>Content Hash</dt><dd className="mono">{query.data.contentHash}</dd></div><div><dt>Mime / 大小</dt><dd>{query.data.mimeType} · {formatBytes(query.data.byteSize)}</dd></div><div><dt>安全状态</dt><dd><Badge tone={query.data.securityStatus === "passed" ? "success" : "warning"}>{query.data.securityStatus}</Badge></dd></div><div><dt>Parse 状态</dt><dd>{query.data.ingestionStatus ? <Badge tone={ingestionTone(query.data.ingestionStatus)}>{query.data.ingestionStatus}</Badge> : <span className="muted">未开始</span>}</dd></div><div><dt>Workflow 状态</dt><dd>{query.data.workflowStatus ? <Badge tone={workflowTone(query.data.workflowStatus)}>{query.data.workflowStatus}</Badge> : <span className="muted">未绑定</span>}</dd></div><div><dt>Index 状态</dt><dd>{query.data.indexStatus ? <Badge tone={query.data.indexStatus === "included" ? "success" : "warning"}>{sourceIndexLabel(query.data.indexStatus)}</Badge> : <span className="muted">未选择</span>}</dd></div></dl><div className="document-actions"><Button asChild variant="secondary"><Link to={`/search?source_version_id=${encodeURIComponent(query.data.id)}&scope_workspace=${encodeURIComponent(workspaceId)}`}><Search size={15} />在 Search 中限定此版本</Link></Button><Button asChild variant="secondary"><Link to="/chat"><MessageSquare size={15} />进入 Chat</Link></Button><Button asChild variant="secondary"><Link to="/graph"><GitBranch size={15} />进入 Graph</Link></Button></div><UnavailableState title="标准化正文预览尚未交付" description="Search 已可按此 Source Version 检索并打开受控 Evidence；当前仍不在浏览器缓存完整原文。" /></Card> : <EmptyState title="找不到这个版本" description="请回到 Inbox 重新选择真实资料版本。" />}</div>;
};

const authCapabilities: readonly AuthCapability[] = ["READ_LOCAL", "READ_EXTERNAL", "WRITE_PROPOSAL", "WRITE_KNOWLEDGE", "GIT_WRITE", "INDEX_MAINTENANCE", "EVALUATION_RUN"];

const ApiTokenSettings = () => {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<AuthCapability[]>(["READ_LOCAL"]);
  const [expiresInSeconds, setExpiresInSeconds] = useState("2592000");
  const [created, setCreated] = useState<ApiTokenCredential | undefined>();
  const [createdConfirmed, setCreatedConfirmed] = useState(false);
  const [formError, setFormError] = useState<string | undefined>();
  const tokens = useInfiniteQuery({
    queryKey: ["auth", "api-tokens"],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) => listApiTokens(pageParam || undefined, 30, signal),
    getNextPageParam: (lastPage) => lastPage.nextCursor,
    retry: false,
  });
  const tokenItems = tokens.data?.pages.flatMap((page) => page.items) ?? [];
  const initialTokenListError = tokens.isError && tokenItems.length === 0;
  const create = useMutation({
    mutationFn: () => createApiToken({ name: name.trim(), scopes, expiresInSeconds: Number(expiresInSeconds) }),
    onSuccess: (credential) => {
      setCreated(credential);
      setCreatedConfirmed(false);
      setName("");
      void queryClient.invalidateQueries({ queryKey: ["auth", "api-tokens"] });
    },
    onError: (error: Error) => setFormError(error.message),
  });
  const revoke = useMutation({
    mutationFn: (tokenId: string) => revokeApiToken(tokenId),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["auth", "api-tokens"] }),
  });
  useEffect(() => {
    if (created === undefined || createdConfirmed) return undefined;
    const warnBeforeUnload = (event: BeforeUnloadEvent): void => {
      event.preventDefault();
    };
    const confirmInAppNavigation = (event: MouseEvent): void => {
      if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      const target = event.target instanceof Element ? event.target.closest("a[href], [role='tab']") : null;
      if (target === null || (target instanceof HTMLAnchorElement && (target.target === "_blank" || target.hasAttribute("download")))) return;
      if (window.confirm("该 API Token 尚未确认复制，离开后将无法再次查看。确定放弃它吗？")) return;
      event.preventDefault();
      event.stopPropagation();
    };
    window.addEventListener("beforeunload", warnBeforeUnload);
    document.addEventListener("click", confirmInAppNavigation, true);
    return () => {
      window.removeEventListener("beforeunload", warnBeforeUnload);
      document.removeEventListener("click", confirmInAppNavigation, true);
    };
  }, [created, createdConfirmed]);
  const toggleScope = (scope: AuthCapability) => setScopes((current) => current.includes(scope) ? current.filter((item) => item !== scope) : [...current, scope]);
  return <Card>
    <CardHeader eyebrow="Automation credentials" title="API Token" description="Token 明文只在创建成功后显示一次；确认复制前不能继续创建，且离页时会显式警告。" />
    {created ? <div className="token-reveal" role="alert" id="api-token-pending-copy"><strong>请立即复制 API Token</strong><code>{created.token}</code><p>该值不会再次从服务端返回，也不会写入浏览器存储。确认前刷新、关闭或离开页面都会触发防丢失提示。</p><label className="token-scope"><input type="checkbox" checked={createdConfirmed} onChange={(event) => setCreatedConfirmed(event.target.checked)} />我已复制并安全保存该 Token</label><Button variant="secondary" size="sm" disabled={!createdConfirmed} onClick={() => { setCreated(undefined); setCreatedConfirmed(false); }}>关闭一次性 Token</Button></div> : null}
    <form className="token-form" aria-describedby={created === undefined ? undefined : "api-token-pending-copy"} onSubmit={(event) => { event.preventDefault(); setFormError(undefined); if (created !== undefined) { setFormError("请先确认已复制上一个 API Token。"); return; } if (name.trim() === "" || scopes.length === 0 || !Number.isSafeInteger(Number(expiresInSeconds)) || Number(expiresInSeconds) < 0) { setFormError("请填写名称、至少一个 Scope 和有效期。"); return; } create.mutate(); }}>
      <label>名称<input value={name} maxLength={80} onChange={(event) => setName(event.target.value)} placeholder="例如：CI 只读" /></label>
      <label>有效期（秒）<input value={expiresInSeconds} inputMode="numeric" onChange={(event) => setExpiresInSeconds(event.target.value)} /></label>
      <fieldset><legend>Capabilities</legend>{authCapabilities.map((scope) => <label key={scope} className="token-scope"><input type="checkbox" checked={scopes.includes(scope)} onChange={() => toggleScope(scope)} />{scope}</label>)}</fieldset>
      {formError ? <p className="form-error" role="alert">{formError}</p> : null}
      <Button type="submit" disabled={create.isPending || created !== undefined}><KeyRound size={15} />{create.isPending ? "正在创建…" : "创建 API Token"}</Button>
    </form>
    {initialTokenListError ? <UnavailableState title="API Token 列表不可用" description={tokens.error.message} /> : tokens.isPending ? <p>正在读取 Token 元数据…</p> : tokenItems.length === 0 ? <EmptyState title="尚无 API Token" description="为脚本或自动化任务创建一个限 Scope Token。" /> : <><div className="table-scroll"><table className="data-table"><thead><tr><th>名称</th><th>Scope</th><th>有效期</th><th>最后使用</th><th /></tr></thead><tbody>{tokenItems.map((token) => <tr key={token.id}><td>{token.name}</td><td><span className="mono">{token.scopes.join(", ")}</span></td><td>{new Date(token.expiresAt).toLocaleString("zh-CN")}</td><td>{token.lastUsedAt ? new Date(token.lastUsedAt).toLocaleString("zh-CN") : "未使用"}</td><td><Button variant="danger" size="sm" onClick={() => revoke.mutate(token.id)} disabled={(revoke.isPending && revoke.variables === token.id) || token.revokedAt !== undefined}>{token.revokedAt ? "已撤销" : revoke.isPending && revoke.variables === token.id ? "撤销中…" : "撤销"}</Button></td></tr>)}</tbody></table></div>{revoke.isError && revoke.variables !== undefined ? <div className="ui-state ui-state--error" role="alert"><strong>撤销 API Token 失败</strong><p>{revoke.error.message}</p><Button variant="secondary" size="sm" onClick={() => revoke.mutate(revoke.variables)} disabled={revoke.isPending}>重试撤销 {tokenItems.find((token) => token.id === revoke.variables)?.name ?? "Token"}</Button></div> : null}{tokens.isFetchNextPageError ? <div className="ui-state ui-state--error" role="alert"><strong>加载更多 API Token 失败</strong><p>{tokens.error.message}</p><Button variant="secondary" size="sm" onClick={() => void tokens.fetchNextPage()} disabled={tokens.isFetchingNextPage}>重试加载更多 Token</Button></div> : null}{tokens.hasNextPage && !tokens.isFetchNextPageError ? <div className="pagination-row"><span /> <Button variant="secondary" onClick={() => void tokens.fetchNextPage()} disabled={tokens.isFetchingNextPage}>{tokens.isFetchingNextPage ? "加载中…" : "加载更多 Token"}</Button></div> : null}</>}
  </Card>;
};

export const SettingsPage = () => {
  const id = useActiveWorkspaceId();
  const { state: authState } = useAuth();
  const query = useQuery({ queryKey: ["workspace", id], queryFn: ({ signal }) => getWorkspace(id, signal), enabled: Boolean(id), retry: false });
  const workspaceState = !id
    ? <UnavailableState title="尚未连接 Workspace" description="先进入 Workspace 页面连接或切换真实目录。" />
    : query.isError
      ? <UnavailableState title="Workspace 不可用" description={query.error.message} />
      : query.isPending
        ? <p>正在读取 Workspace…</p>
        : query.data
          ? <dl className="detail-grid"><div><dt>名称</dt><dd>{query.data.name}</dd></div><div><dt>根目录</dt><dd className="mono">{query.data.rootPath}</dd></div><div><dt>状态</dt><dd><Badge tone="success">{query.data.status}</Badge></dd></div><div><dt>连接 ID</dt><dd className="mono">{query.data.id}</dd></div></dl>
          : <UnavailableState title="Workspace 状态缺失" description="API 没有返回可展示的 Workspace 事实。" />;

  return <div className="page-stack"><div className="page-intro"><p className="eyebrow">运行边界 / Settings</p><h2>把能做什么写清楚。</h2><p>Settings 只展示已有契约；密钥、Bootstrap Token 和 Session Cookie 不进入 Browser Storage，也没有假保存按钮。</p></div><Tabs defaultValue="workspace"><TabsList aria-label="设置分组"><TabsTrigger value="workspace">Workspace</TabsTrigger><TabsTrigger value="runtime">运行依赖</TabsTrigger><TabsTrigger value="models">模型 / 检索</TabsTrigger><TabsTrigger value="git">Git</TabsTrigger><TabsTrigger value="auth">API Token</TabsTrigger><TabsTrigger value="maintenance">维护边界</TabsTrigger></TabsList><TabsContent value="workspace"><Card><CardHeader eyebrow="Workspace" title="当前连接" action={<Button asChild variant="secondary" size="sm"><Link to="/workspace">连接 / 切换</Link></Button>} />{workspaceState}</Card></TabsContent><TabsContent value="runtime"><Card><CardHeader eyebrow="Runtime" title="API 与依赖状态" description="状态来自 /api/v1/system/status，不根据浏览器连接状态推断业务健康。" /><SystemStatusPage /></Card></TabsContent><TabsContent value="models"><Card><CardHeader eyebrow="Model / Retrieval" title="模型与检索能力" /><div className="capability-list"><div><ShieldCheck size={17} /><span>RAG / Retrieval 可用性</span><Badge tone="info">见运行依赖实时状态</Badge></div><div><ShieldCheck size={17} /><span>模型名称、密钥与 Provider 配置</span><Badge tone="warning">无公开读取/写入契约</Badge></div><div><ShieldCheck size={17} /><span>索引维护与重建</span><Badge tone="neutral">未交付 Settings 命令</Badge></div></div></Card></TabsContent><TabsContent value="git"><Card><CardHeader eyebrow="Git" title="Workspace 仓库事实" />{query.data ? <dl className="detail-grid"><div><dt>Repository</dt><dd>{query.data.git.present ? "已检测" : "未检测到"}</dd></div><div><dt>Branch</dt><dd>{query.data.git.branch || "无分支"}</dd></div><div><dt>Working Tree</dt><dd><Badge tone={query.data.git.dirty ? "danger" : "success"}>{query.data.git.dirty ? "dirty" : "clean"}</Badge></dd></div><div><dt>Head</dt><dd className="mono">{query.data.git.head || "未提供"}</dd></div></dl> : workspaceState}</Card></TabsContent><TabsContent value="auth">{authState.mode === "required" ? <ApiTokenSettings /> : <UnavailableState title="开发模式未启用 API Token" description="认证显式关闭时服务端不会注册 Session/API Token 管理端点。切换到 required 后再创建自动化凭据。" />}</TabsContent><TabsContent value="maintenance"><Card><CardHeader eyebrow="Maintenance" title="允许与未交付操作" /><div className="capability-list"><div><ShieldCheck size={17} /><span>Workspace 安全扫描</span><Badge tone="info">已有契约</Badge></div><div><ShieldCheck size={17} /><span>Session / CSRF / Capability</span><Badge tone={authState.mode === "required" ? "success" : "warning"}>{authState.mode === "required" ? "已交付 M10-02" : "开发模式关闭"}</Badge></div><div><ShieldCheck size={17} /><span>Artifact / Review / Memory</span><Badge tone="warning">M8</Badge></div><div><ShieldCheck size={17} /><span>导出、危险清理与 Secret 保存</span><Badge tone="neutral">按权限与任务契约开放</Badge></div></div><div className="document-actions"><Button asChild variant="secondary"><Link to="/inbox"><RefreshCw size={15} />进入安全扫描</Link></Button></div></Card></TabsContent></Tabs></div>;
};
