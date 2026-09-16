import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { listDiscoveryFailures, scanWorkspace } from "../../api/workspace";
import { Link } from "react-router-dom";

import type { ActiveWorkspaceAvailability } from "../../api/active-workspace";
import { useActiveWorkspace } from "../../app/WorkspaceCacheBoundary";
import { Badge, Button, EmptyState, PageHeader } from "../../shared/ui";

const availabilityLabel = (availability: ActiveWorkspaceAvailability): string => {
  switch (availability) {
    case "available":
      return "可用";
    case "unavailable":
      return "暂不可用";
    case "migration_required":
      return "需要迁移";
  }
};

const availabilityTone = (availability: ActiveWorkspaceAvailability): "success" | "warning" | "danger" =>
  availability === "available" ? "success" : availability === "migration_required" ? "warning" : "danger";

const SwitchInstructions = () => <section className="workspace-connection" aria-labelledby="workspace-switch-title">
  <header className="workspace-connection__heading">
    <h2 id="workspace-switch-title">在本机命令行切换</h2>
    <p>网页只读取当前授权目录，不接受宿主机路径或 Workspace ID 输入。</p>
  </header>
  <div className="workspace-command-list">
    <div><p>首次启动时指定目录：</p><code className="workspace-command">./zhixu up --workspace &lt;宿主机绝对目录&gt;</code></div>
    <div><p>之后切换到其他目录：</p><code className="workspace-command">./zhixu workspace switch &lt;宿主机绝对目录&gt;</code></div>
    <p className="muted">命令完成并重新连接后，这里会显示新的当前 Workspace。切换失败时不会覆盖上一次成功选择。</p>
  </div>
</section>;

export const WorkspacePage = () => {
  const { status, workspace, error, refresh } = useActiveWorkspace();

  return <div className="page-stack workspace-page">
    <PageHeader title="当前 Workspace" description="由后端当前 Workspace 契约提供的只读运行上下文。" />
    {status === "loading" ? <p role="status">正在读取当前 Workspace…</p> : null}
    {status === "error" ? <div className="workspace-alert workspace-alert--error" role="alert"><strong>无法读取当前 Workspace</strong><span>{error?.message ?? "当前 Workspace API 暂不可用。"}</span><Button variant="secondary" size="sm" onClick={() => void refresh()}>重新读取</Button></div> : null}
    {status === "unavailable" && workspace ? <div className="workspace-alert workspace-alert--warning" role="alert"><strong>当前目录暂不可用于业务请求</strong><span>请检查本机运行时状态后重新执行切换命令。</span><Button variant="secondary" size="sm" onClick={() => void refresh()}>重新检查</Button></div> : null}
    {workspace ? <section className="workspace-connection" aria-labelledby="workspace-current-title">
      <header className="workspace-connection__heading"><h2 id="workspace-current-title">服务端当前选择</h2><p>该投影不是浏览器本地身份；页面不会提交或修改根目录。</p></header>
      <dl className="settings-facts">
        <div><dt>名称</dt><dd>{workspace.name}</dd></div>
        <div><dt>宿主机目录</dt><dd className="mono">{workspace.rootPath}</dd></div>
        <div><dt>Workspace ID</dt><dd className="mono">{workspace.id}</dd></div>
        <div><dt>状态</dt><dd><Badge tone="success">已连接</Badge></dd></div>
        <div><dt>可用性</dt><dd><Badge tone={availabilityTone(workspace.availability)}>{availabilityLabel(workspace.availability)}</Badge></dd></div>
        <div><dt>版本</dt><dd className="mono">{workspace.version}</dd></div>
      </dl>
      <div className="settings-section__actions"><Button asChild variant="secondary"><Link to="/settings?section=workspace">查看设置中的运行上下文</Link></Button></div>
    </section> : null}
    {workspace === undefined && status !== "loading" && status !== "error" ? <EmptyState title="尚未激活 Workspace" description="先在本机命令行指定一个知识库目录，业务路由会在激活后自动恢复。" /> : null}
    {status === "ready" && workspace ? <DiscoveryStatus key={`${workspace.id}:${String(workspace.version)}`} workspaceId={workspace.id} version={workspace.version} /> : null}
    <SwitchInstructions />
  </div>;
};

const DiscoveryStatus = ({ workspaceId, version }: { workspaceId: string; version: number }) => {
  const client = useQueryClient();
  const key = ["workspace", workspaceId, "discovery-failures", version];
  const query = useInfiniteQuery({ queryKey: key, initialPageParam: "", queryFn: ({ pageParam, signal }) => listDiscoveryFailures(workspaceId, pageParam, signal), getNextPageParam: (page) => page.next_cursor === "" ? undefined : page.next_cursor, refetchInterval: 30000 });
  const scan = useMutation({ mutationFn: () => scanWorkspace(workspaceId), onSettled: () => client.invalidateQueries({ queryKey: ["workspace", workspaceId, "discovery-failures", version] }) });
  const pages = query.data?.pages ?? [];
  const bindingChanged = pages.some((page) => page.workspace_id !== workspaceId || page.binding_version !== pages[0]?.binding_version);
  const items = bindingChanged ? [] : pages.flatMap((page) => page.items);
  const stages = { WALK: "读取目录", OBSERVE: "读取文件", REGISTER: "登记来源" };
  return <section className="workspace-connection" aria-labelledby="discovery-title">
    <header className="workspace-connection__heading"><h2 id="discovery-title">文件扫描记录</h2><p>文件发现失败会保留在这里。修复文件访问问题后，可等待自动重扫或重新扫描；成功登记后显示已恢复。</p></header>
    <div className="settings-section__actions"><Button variant="secondary" disabled={scan.isPending} onClick={() => scan.mutate()}>{scan.isPending ? "正在扫描…" : "重新扫描"}</Button><Button variant="secondary" disabled={query.isFetching} onClick={() => void client.resetQueries({ queryKey: key, exact: true })}>刷新记录</Button></div>
    {bindingChanged ? <p role="alert">目录绑定已变更，请刷新记录后继续查看。</p> : null}
    {query.isPending ? <p role="status">正在读取扫描记录…</p> : null}
    {query.isError ? <p role="alert">扫描记录读取失败：{query.error.message}</p> : null}
    {scan.isError ? <p role="alert">扫描未完成：{scan.error.message}</p> : null}
    {scan.isSuccess ? <p role="status">本次扫描已完成；请查看下方仍需处理的记录。</p> : null}
    {query.isSuccess && !bindingChanged && items.length === 0 ? <p>当前根目录暂无已记录的扫描失败。</p> : null}
    {items.map((item) => <article key={item.path} className="workspace-alert">
      <strong style={{ overflowWrap: "anywhere" }}>{item.path}</strong>
      <span><Badge tone={item.status === "FAILED" ? "warning" : "success"}>{item.status === "FAILED" ? "待处理" : "已恢复"}</Badge> {stages[item.stage]} · 失败 {item.failure_count} 次</span>
      <span>最近失败：{new Date(item.last_failed_at).toLocaleString()} · {item.code}</span>
      {item.recovered_at !== null ? <span>恢复时间：{new Date(item.recovered_at).toLocaleString()}</span> : null}
    </article>)}
    {!bindingChanged && query.hasNextPage ? <Button variant="secondary" disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>更多记录</Button> : null}
  </section>;
};
