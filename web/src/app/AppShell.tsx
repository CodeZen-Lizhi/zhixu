import { Activity, Archive, BookOpenCheck, Brain, ChevronDown, FileCheck2, FileText, FolderOpen, GitBranch, HeartPulse, History, Inbox, Layers3, LayoutDashboard, LogOut, Menu, MessageSquare, MoreHorizontal, Search, Settings, ShieldAlert, ShieldCheck, UserRoundCheck, Wifi, WifiOff, type LucideIcon } from "lucide-react";
import { forwardRef, Suspense, useRef, useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";

import { useEventStore } from "../events/event-store";
import { DropdownMenu, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, Sheet, Tooltip } from "../shared/ui";
import { useActiveWorkspaceId } from "./active-workspace";
import { useAuth } from "./auth-context";

interface NavigationItem {
  path: string;
  label: string;
  icon: LucideIcon;
}

interface NavigationGroup {
  label: string;
  items: readonly NavigationItem[];
  collapsible?: boolean;
  availableWithoutWorkspace?: boolean;
}

const navigationGroups: readonly NavigationGroup[] = [
  {
    label: "工作台",
    availableWithoutWorkspace: true,
    items: [{ path: "/dashboard", label: "工作台", icon: LayoutDashboard }],
  },
  {
    label: "资料与知识",
    items: [
      { path: "/inbox", label: "资料收件箱", icon: Inbox },
      { path: "/documents", label: "资料版本", icon: Archive },
      { path: "/search", label: "检索", icon: Search },
      { path: "/graph", label: "知识图谱", icon: GitBranch },
      { path: "/collections", label: "集合", icon: Layers3 },
      { path: "/health", label: "知识健康", icon: HeartPulse },
      { path: "/timeline", label: "时间线", icon: History },
    ],
  },
  {
    label: "审阅与产出",
    items: [
      { path: "/proposals", label: "提案", icon: FileCheck2 },
      { path: "/workflows", label: "流程", icon: Activity },
      { path: "/artifacts", label: "产物", icon: FileText },
    ],
  },
  {
    label: "学习与研究",
    collapsible: true,
    items: [
      { path: "/review", label: "复习", icon: BookOpenCheck },
      { path: "/memories", label: "记忆", icon: Brain },
      { path: "/interviews", label: "访谈", icon: UserRoundCheck },
      { path: "/chat", label: "对话", icon: MessageSquare },
    ],
  },
  {
    label: "系统",
    availableWithoutWorkspace: true,
    items: [
      { path: "/workspace", label: "工作区", icon: FolderOpen },
      { path: "/settings", label: "设置", icon: Settings },
    ],
  },
];

const navigation = navigationGroups.flatMap((group) => group.items);

const matchesNavigationItem = (path: string, pathname: string): boolean =>
  pathname === path || (path !== "/dashboard" && pathname.startsWith(`${path}/`));

interface NavigationProps {
  currentPath: string;
  onNavigate?: () => void;
  workspaceConnected: boolean;
}

const Navigation = ({ currentPath, onNavigate, workspaceConnected }: NavigationProps) => {
  const visibleGroups = workspaceConnected
    ? navigationGroups
    : navigationGroups.filter((group) => group.availableWithoutWorkspace);

  return (
    <nav className="rail-navigation" aria-label="主导航">
      {visibleGroups.map((group) => {
        const hasCurrentRoute = group.items.some((item) => matchesNavigationItem(item.path, currentPath));
        const links = (
          <div className="rail-nav-group__links">
            {group.items.map(({ path, label, icon: Icon }) => {
              const content = <><Icon size={17} strokeWidth={1.8} /><span>{label}</span></>;
              if (currentPath === "/" && path === "/workspace") {
                return <Link key={path} to={path} onClick={onNavigate} aria-current="page" className="rail-link rail-link--active">{content}</Link>;
              }
              return <NavLink key={path} to={path} onClick={onNavigate} className={({ isActive }) => isActive ? "rail-link rail-link--active" : "rail-link"}>{content}</NavLink>;
            })}
          </div>
        );

        if (group.collapsible) {
          return (
            <details className="rail-nav-group rail-nav-group--collapsible" key={group.label} open={hasCurrentRoute || undefined}>
              <summary className="rail-nav-group__summary">
                <span>{group.label}</span>
                <ChevronDown size={15} aria-hidden="true" />
              </summary>
              {links}
            </details>
          );
        }

        return (
          <section className="rail-nav-group" aria-labelledby={`navigation-${group.label}`} key={group.label}>
            <p className="rail-nav-group__label" id={`navigation-${group.label}`}>{group.label}</p>
            {links}
          </section>
        );
      })}
    </nav>
  );
};

interface ConnectionPillProps {
  state: string;
  label: string;
  waitingForWorkspace: boolean;
}

const ConnectionPill = forwardRef<HTMLDivElement, ConnectionPillProps>(({ state, label, waitingForWorkspace }, ref) => (
  <div ref={ref} className={`connection-pill connection-pill--${state}`} role="status" tabIndex={0}>
    <span className="connection-pill__icon" aria-hidden="true">
      {waitingForWorkspace ? <FolderOpen size={14} /> : state === "open" ? <Wifi size={14} /> : <WifiOff size={14} />}
    </span>
    <span>{label}</span>
  </div>
));

ConnectionPill.displayName = "ConnectionPill";

export const AppShell = () => {
  const workspaceId = useActiveWorkspaceId();
  const event = useEventStore();
  const { state: authState, signOut } = useAuth();
  const location = useLocation();
  const [mobileNavigationOpen, setMobileNavigationOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [signOutError, setSignOutError] = useState<Error | undefined>(undefined);
  const mobileMenuButtonRef = useRef<HTMLButtonElement>(null);
  const workspaceConnected = workspaceId !== "";
  const isWorkspaceRoot = location.pathname === "/";
  const current = isWorkspaceRoot
    ? navigation.find((item) => item.path === "/workspace")
    : navigation.find((item) => matchesNavigationItem(item.path, location.pathname));
  const isDetail = current !== undefined && !isWorkspaceRoot && location.pathname !== current.path;
  const eventConnectionLabel = event.state === "open" ? "实时同步" : event.state === "recovery_failed" ? "恢复失败" : event.state === "reconnecting" ? "正在重连" : event.state === "connecting" ? "正在连接" : "同步未连接";
  const eventConnectionDescription = event.state === "open" ? "SSE 通道已连接；业务终态仍以 API 查询为准。" : event.state === "recovery_failed" ? "事件游标恢复失败，旧游标已保留，请检查 API 与数据库。" : "事件通道当前不可用或正在恢复，不代表业务操作失败。";
  const connectionLabel = workspaceConnected ? eventConnectionLabel : "等待连接 Workspace";
  const connectionDescription = workspaceConnected
    ? eventConnectionDescription
    : "尚未建立 Workspace 上下文；连接后才会建立该 Workspace 的同步通道。";
  const connectionState = workspaceConnected ? event.state : "waiting";
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
      <div className="rail-section"><Navigation currentPath={location.pathname} workspaceConnected={workspaceConnected} /></div>
      <div className="rail-footer"><Tooltip content={connectionDescription}><ConnectionPill state={connectionState} label={connectionLabel} waitingForWorkspace={!workspaceConnected} /></Tooltip>{workspaceConnected && event.state === "recovery_failed" ? <button type="button" className="connection-retry" onClick={event.retryRecovery}>重试事件恢复</button> : null}<p className="rail-footnote">M9 · 业务操作台</p></div>
    </aside>
    <Sheet open={mobileNavigationOpen} onOpenChange={setMobileNavigationOpen} title="工作台导航" restoreFocusRef={mobileMenuButtonRef}><div className="mobile-sheet-brand">知序 <small>ZHIXU / WORKBENCH</small></div><Navigation currentPath={location.pathname} workspaceConnected={workspaceConnected} onNavigate={() => setMobileNavigationOpen(false)} /><ConnectionPill state={connectionState} label={connectionLabel} waitingForWorkspace={!workspaceConnected} />{workspaceConnected && event.state === "recovery_failed" ? <button type="button" className="connection-retry" onClick={event.retryRecovery}>重试事件恢复</button> : null}{authRequired ? <button type="button" className="connection-retry auth-signout-button" onClick={() => void handleSignOut()} disabled={signingOut}><LogOut size={14} />{signingOut ? "正在退出…" : "退出登录"}</button> : null}</Sheet>
    <main className="workbench__main"><header className="workbench__topbar"><div><nav className="topbar-breadcrumb" aria-label="面包屑"><Link to="/dashboard">工作台</Link>{current ? <><span aria-hidden="true">/</span><Link to={isWorkspaceRoot ? "/" : current.path} aria-current={isDetail ? undefined : "page"}>{current.label}</Link></> : null}{isDetail ? <><span aria-hidden="true">/</span><span aria-current="page">详情</span></> : null}</nav><h1>{location.pathname === "/dashboard" ? "今日编辑台" : current?.label ?? "ZHIXU"}</h1></div><div className="topbar-actions"><Tooltip content={authDescription}><span className={`local-mode-badge ${authRequired ? "local-mode-badge--authenticated" : ""}`}>{authRequired ? <ShieldCheck size={14} /> : <ShieldAlert size={14} />}{authLabel}</span></Tooltip><div className="topbar-context"><span className="context-dot" />{workspaceConnected ? <span className="mono">{workspaceId.slice(0, 8)}…</span> : <span>等待连接 Workspace</span>}</div><DropdownMenu label="工作台快捷入口" trigger={<button type="button" className="topbar-menu-button" aria-label="打开工作台快捷入口"><MoreHorizontal size={19} /></button>}><DropdownMenuLabel>{authLabel}</DropdownMenuLabel><DropdownMenuSeparator /><DropdownMenuItem asChild><Link to="/workspace">连接或切换 Workspace</Link></DropdownMenuItem><DropdownMenuItem asChild><Link to="/settings">查看运行边界</Link></DropdownMenuItem>{workspaceConnected ? <DropdownMenuItem asChild><Link to="/inbox">重新检查资料流</Link></DropdownMenuItem> : null}{authRequired ? <><DropdownMenuSeparator /><DropdownMenuItem disabled={signingOut} onSelect={() => { void handleSignOut(); }}><LogOut size={15} />{signingOut ? "正在退出…" : "退出登录"}</DropdownMenuItem></> : null}</DropdownMenu></div></header>{signOutError ? <p className="topbar-auth-error" role="alert">退出登录失败：{signOutError.message}。请重试；当前 Session 未被本地假设为已撤销。</p> : null}<div className="workbench__content"><Suspense fallback={<div className="page-loading">正在加载工作台…</div>}><Outlet /></Suspense></div></main>
  </div>;
};
