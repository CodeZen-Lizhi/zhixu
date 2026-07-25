import { Activity, Archive, FileCheck2, FolderOpen, GitBranch, HeartPulse, Inbox, Layers3, LayoutDashboard, LogOut, Menu, MessageSquare, MoreHorizontal, Search, Settings, ShieldAlert, ShieldCheck, Wifi, WifiOff } from "lucide-react";
import { Suspense, useRef, useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";

import { useEventStore } from "../events/event-store";
import { DropdownMenu, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, Sheet, Tooltip } from "../shared/ui";
import { useActiveWorkspaceId } from "./active-workspace";
import { useAuth } from "./auth-context";

const navigation = [
  ["/dashboard", "Dashboard", LayoutDashboard], ["/inbox", "Inbox", Inbox], ["/documents", "资料版本", Archive],
  ["/search", "Search", Search], ["/proposals", "Proposals", FileCheck2], ["/workflows", "Workflows", Activity], ["/graph", "Graph", GitBranch],
  ["/collections", "Collections", Layers3], ["/health", "知识健康", HeartPulse],
  ["/chat", "Chat", MessageSquare], ["/workspace", "Workspace", FolderOpen], ["/settings", "Settings", Settings],
] as const;

const Navigation = ({ onNavigate }: { onNavigate?: () => void }) => <nav aria-label="主导航">
  {navigation.map(([path, label, Icon]) => <NavLink key={path} to={path} onClick={onNavigate} className={({ isActive }) => isActive ? "rail-link rail-link--active" : "rail-link"}><Icon size={17} strokeWidth={1.8} /><span>{label}</span></NavLink>)}
</nav>;

export const AppShell = () => {
  const workspaceId = useActiveWorkspaceId();
  const event = useEventStore();
  const { state: authState, signOut } = useAuth();
  const location = useLocation();
  const [mobileNavigationOpen, setMobileNavigationOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [signOutError, setSignOutError] = useState<Error | undefined>(undefined);
  const mobileMenuButtonRef = useRef<HTMLButtonElement>(null);
  const current = location.pathname === "/"
    ? navigation.find(([path]) => path === "/workspace")
    : navigation.find(([path]) => location.pathname === path || (path !== "/dashboard" && location.pathname.startsWith(`${path}/`)));
  const isDetail = current !== undefined && location.pathname !== current[0];
  const connectionLabel = event.state === "open" ? "实时同步" : event.state === "recovery_failed" ? "恢复失败" : event.state === "reconnecting" ? "正在重连" : event.state === "connecting" ? "正在连接" : "离线";
  const connectionDescription = event.state === "open" ? "SSE 通道已连接；业务终态仍以 API 查询为准。" : event.state === "recovery_failed" ? "事件游标恢复失败，旧游标已保留，请检查 API 与数据库。" : "事件通道当前不可用或正在恢复，不代表业务操作失败。";
  const authRequired = authState.status === "authenticated" && authState.mode === "required";
  const authLabel = authRequired ? `已认证 · ${authState.session?.userLabel ?? "Owner"}` : "开发模式 · 认证关闭";
  const authDescription = authRequired
    ? "浏览器使用 HttpOnly Cookie Session；状态修改同时校验 Origin 与 CSRF。"
    : "认证已显式关闭，仅允许 loopback 开发模式。";
  const handleSignOut = async (): Promise<void> => {
    setSignOutError(undefined);
    setSigningOut(true);
    try {
      await signOut();
    } catch (error: unknown) {
      setSignOutError(error instanceof Error ? error : new Error("退出登录失败"));
    } finally {
      setSigningOut(false);
    }
  };

  return <div className="workbench">
    <aside className="workbench__rail">
      <div className="mobile-brand-row"><Link to="/dashboard" className="wordmark"><span>知序</span><small>ZHIXU / WORKBENCH</small></Link><button ref={mobileMenuButtonRef} className="mobile-menu-button" aria-label="打开主导航" onClick={() => setMobileNavigationOpen(true)}><Menu size={20} /></button></div>
      <div className="rail-section"><p className="rail-label">工作台</p><Navigation /></div>
      <div className="rail-footer"><Tooltip content={connectionDescription}><div className={`connection-pill connection-pill--${event.state}`} role="status" tabIndex={0}><span className="connection-pill__icon">{event.state === "open" ? <Wifi size={14} /> : <WifiOff size={14} />}</span><span>{connectionLabel}</span></div></Tooltip>{event.state === "recovery_failed" ? <button type="button" className="connection-retry" onClick={event.retryRecovery}>重试事件恢复</button> : null}<p className="rail-footnote">M9 · 业务操作台</p></div>
    </aside>
    <Sheet open={mobileNavigationOpen} onOpenChange={setMobileNavigationOpen} title="工作台导航" restoreFocusRef={mobileMenuButtonRef}><div className="mobile-sheet-brand">知序 <small>ZHIXU / WORKBENCH</small></div><Navigation onNavigate={() => setMobileNavigationOpen(false)} /><div className={`connection-pill connection-pill--${event.state}`} role="status">{connectionLabel}</div>{event.state === "recovery_failed" ? <button type="button" className="connection-retry" onClick={event.retryRecovery}>重试事件恢复</button> : null}{authRequired ? <button type="button" className="connection-retry auth-signout-button" onClick={() => void handleSignOut()} disabled={signingOut}><LogOut size={14} />{signingOut ? "正在退出…" : "退出登录"}</button> : null}</Sheet>
    <main className="workbench__main"><header className="workbench__topbar"><div><nav className="topbar-breadcrumb" aria-label="面包屑"><Link to="/dashboard">工作台</Link>{current ? <><span aria-hidden="true">/</span><Link to={current[0]} aria-current={isDetail ? undefined : "page"}>{current[1]}</Link></> : null}{isDetail ? <><span aria-hidden="true">/</span><span aria-current="page">详情</span></> : null}</nav><h1>{location.pathname === "/dashboard" ? "今日编辑台" : current?.[1] ?? "ZHIXU"}</h1></div><div className="topbar-actions"><Tooltip content={authDescription}><span className={`local-mode-badge ${authRequired ? "local-mode-badge--authenticated" : ""}`}>{authRequired ? <ShieldCheck size={14} /> : <ShieldAlert size={14} />}{authLabel}</span></Tooltip><div className="topbar-context"><span className="context-dot" />{workspaceId ? <span className="mono">{workspaceId.slice(0, 8)}…</span> : <span>未连接 Workspace</span>}</div><DropdownMenu label="工作台快捷入口" trigger={<button type="button" className="topbar-menu-button" aria-label="打开工作台快捷入口"><MoreHorizontal size={19} /></button>}><DropdownMenuLabel>{authLabel}</DropdownMenuLabel><DropdownMenuSeparator /><DropdownMenuItem asChild><Link to="/workspace">连接或切换 Workspace</Link></DropdownMenuItem><DropdownMenuItem asChild><Link to="/settings">查看运行边界</Link></DropdownMenuItem><DropdownMenuItem asChild><Link to="/inbox">重新检查资料流</Link></DropdownMenuItem>{authRequired ? <><DropdownMenuSeparator /><DropdownMenuItem disabled={signingOut} onSelect={() => { void handleSignOut(); }}><LogOut size={15} />{signingOut ? "正在退出…" : "退出登录"}</DropdownMenuItem></> : null}</DropdownMenu></div></header>{signOutError ? <p className="topbar-auth-error" role="alert">退出登录失败：{signOutError.message}。请重试；当前 Session 未被本地假设为已撤销。</p> : null}<div className="workbench__content"><Suspense fallback={<div className="page-loading">正在加载工作台…</div>}><Outlet /></Suspense></div></main>
  </div>;
};
