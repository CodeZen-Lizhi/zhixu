import {
  Activity,
  BookOpenCheck,
  Brain,
  ChevronDown,
  FileCheck2,
  FileText,
  GitBranch,
  HeartPulse,
  Inbox,
  Layers3,
  LayoutDashboard,
  LogOut,
  Menu,
  MessageSquare,
  MoreHorizontal,
  Search,
  Settings,
  ShieldAlert,
  ShieldCheck,
  UserRoundCheck,
  Wifi,
  WifiOff,
  type LucideIcon,
} from "lucide-react";
import { forwardRef, Suspense, useId, useRef, useState } from "react";
import { Link, Outlet, useLocation } from "react-router-dom";

import { useEventStore } from "../events/event-store";
import {
  DropdownMenu,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  Sheet,
  Tooltip,
} from "../shared/ui";
import { getRouteDisplay, routeBelongsToSection, routeDisplayRegistry, type RouteNavigationIcon, type RouteSection } from "../routes/route-display";
import { useActiveWorkspaceId } from "./active-workspace";
import { useAuth } from "./auth-context";

interface NavigationDestination {
  icon: LucideIcon;
  label: string;
  path: string;
}

interface NavigationMenuGroup {
  label: string;
  items: readonly NavigationDestination[];
}

interface PrimaryNavigationItem extends NavigationDestination {
  section: RouteSection;
  groups?: readonly NavigationMenuGroup[];
}

interface OrderedNavigationMenuGroup {
  label: string;
  order: number;
  items: (NavigationDestination & { order: number })[];
}

interface OrderedPrimaryNavigationItem extends PrimaryNavigationItem {
  order: number;
}

interface FooterNavigationItem extends NavigationDestination {
  section: RouteSection;
  order: number;
}

const navigationIcons: Record<RouteNavigationIcon, LucideIcon> = {
  activity: Activity,
  "book-open-check": BookOpenCheck,
  brain: Brain,
  dashboard: LayoutDashboard,
  "file-check": FileCheck2,
  "file-text": FileText,
  "git-branch": GitBranch,
  "heart-pulse": HeartPulse,
  inbox: Inbox,
  layers: Layers3,
  "message-square": MessageSquare,
  search: Search,
  settings: Settings,
  "user-round-check": UserRoundCheck,
};

const navigationGroupsFor = (section: RouteSection): readonly NavigationMenuGroup[] => {
  const groups = new Map<string, OrderedNavigationMenuGroup>();

  for (const route of routeDisplayRegistry) {
    const navigation = route.navigation;
    if (navigation === undefined || navigation.placement === "footer") continue;
    const group = navigation.group;
    if (route.section !== section || group === undefined) continue;
    const current = groups.get(group.label) ?? { label: group.label, order: group.order, items: [] };
    current.items.push({ path: route.basePath, label: route.label, icon: navigationIcons[navigation.icon], order: group.itemOrder });
    groups.set(group.label, current);
  }

  return [...groups.values()]
    .sort((left, right) => left.order - right.order)
    .map((group) => ({ label: group.label, items: group.items.sort((left, right) => left.order - right.order) }));
};

const primaryNavigation = routeDisplayRegistry.flatMap<OrderedPrimaryNavigationItem>((route) => {
  const navigation = route.navigation;
  if (navigation?.placement !== "primary") return [];
  const groups = navigationGroupsFor(route.section);
  const item: OrderedPrimaryNavigationItem = { path: route.basePath, label: navigation.label, icon: navigationIcons[navigation.icon], section: route.section, order: navigation.order };
  if (groups.length > 0) item.groups = groups;
  return [item];
}).sort((left, right) => left.order - right.order);

const footerNavigation = routeDisplayRegistry.flatMap<FooterNavigationItem>((route) => {
  const navigation = route.navigation;
  if (navigation?.placement !== "footer") return [];
  return [{ path: route.basePath, label: navigation.label, icon: navigationIcons[navigation.icon], section: route.section, order: navigation.order }];
}).sort((left, right) => left.order - right.order);

const knowledgeHome = routeDisplayRegistry.find((route) => route.section === "knowledge" && route.navigation?.placement === "primary");

interface NavigationProps {
  currentPath: string;
  compact?: boolean;
  mobile?: boolean;
  onNavigate?: () => void;
  workspaceConnected: boolean;
}

const DestinationLink = ({ destination, currentPath, onNavigate }: { destination: NavigationDestination; currentPath: string; onNavigate: (() => void) | undefined }) => {
  const Icon = destination.icon;
  const active = currentPath === destination.path || currentPath.startsWith(`${destination.path}/`);
  return <Link className={active ? "rail-submenu-link rail-submenu-link--active" : "rail-submenu-link"} aria-current={active ? "page" : undefined} to={destination.path} onClick={onNavigate}><Icon size={16} strokeWidth={1.8} /><span>{destination.label}</span></Link>;
};

const PrimaryItem = ({ item, currentPath, compact, mobile, onNavigate }: { item: PrimaryNavigationItem; currentPath: string; compact: boolean; mobile: boolean; onNavigate: (() => void) | undefined }) => {
  const Icon = item.icon;
  const active = routeBelongsToSection(currentPath, item.section);
  const currentDestination = currentPath === item.path;
  const [expanded, setExpanded] = useState(active);
  const panelId = useId();
  const linkClassName = active ? "rail-link rail-link--active" : "rail-link";

  if (item.groups === undefined) {
    const link = <Link className={linkClassName} aria-current={active ? "page" : undefined} aria-label={compact ? item.label : undefined} to={item.path} onClick={onNavigate}><Icon size={18} strokeWidth={1.8} /><span>{item.label}</span></Link>;
    return compact ? <Tooltip content={item.label}>{link}</Tooltip> : link;
  }

  return <div className={active ? "rail-split-link rail-split-link--active" : "rail-split-link"}>
    <Link className={linkClassName} aria-current={currentDestination ? "page" : undefined} to={item.path} onClick={onNavigate}><Icon size={18} strokeWidth={1.8} /><span>{item.label}</span></Link>
    {mobile ? <button type="button" className="rail-menu-trigger" aria-label={`${expanded ? "收起" : "展开"}${item.label}菜单`} aria-expanded={expanded} aria-controls={panelId} onClick={() => setExpanded((value) => !value)}><ChevronDown size={17} className={expanded ? "is-expanded" : undefined} /></button> : <DropdownMenu label={`${item.label}菜单`} side="right" align="start" trigger={<button type="button" className="rail-menu-trigger" aria-label={`打开${item.label}菜单`}><ChevronDown size={17} /></button>}>
      {item.groups.map((group, index) => <div key={group.label}>{index > 0 ? <DropdownMenuSeparator /> : null}<DropdownMenuLabel>{group.label}</DropdownMenuLabel>{group.items.map((destination) => <DropdownMenuItem key={destination.path} asChild><Link to={destination.path} onClick={onNavigate}><destination.icon size={16} strokeWidth={1.8} /><span>{destination.label}</span></Link></DropdownMenuItem>)}</div>)}
    </DropdownMenu>}
    {mobile && expanded ? <div className="rail-mobile-submenu" id={panelId}>{item.groups.map((group) => <section key={group.label}><p>{group.label}</p>{group.items.map((destination) => <DestinationLink key={destination.path} destination={destination} currentPath={currentPath} onNavigate={onNavigate} />)}</section>)}</div> : null}
  </div>;
};

const Navigation = ({ currentPath, compact = false, mobile = false, onNavigate, workspaceConnected }: NavigationProps) => {
  const visibleNavigation = workspaceConnected ? primaryNavigation : primaryNavigation.filter((item) => item.section === "dashboard");

  return <nav className={mobile ? "rail-navigation rail-navigation--mobile" : compact ? "rail-navigation rail-navigation--compact" : "rail-navigation"} aria-label="主导航">
    <div className="rail-navigation__primary">{visibleNavigation.map((item) => <PrimaryItem key={item.section} item={item} currentPath={currentPath} compact={compact} mobile={mobile} onNavigate={onNavigate} />)}</div>
    <div className="rail-navigation__settings">{footerNavigation.map((item) => {
      const active = routeBelongsToSection(currentPath, item.section);
      const Icon = item.icon;
      const link = <Link key={item.path} className={active ? "rail-link rail-link--active" : "rail-link"} aria-current={active ? "page" : undefined} aria-label={compact ? item.label : undefined} to={item.path} onClick={onNavigate}><Icon size={18} strokeWidth={1.8} /><span>{item.label}</span></Link>;
      return compact ? <Tooltip key={item.path} content={item.label}>{link}</Tooltip> : link;
    })}</div>
  </nav>;
};

interface ConnectionPillProps {
  label: string;
  state: string;
  waitingForWorkspace: boolean;
}

const ConnectionPill = forwardRef<HTMLDivElement, ConnectionPillProps>(({ state, label, waitingForWorkspace }, ref) => (
  <div ref={ref} className={`connection-pill connection-pill--${state}`} role="status" tabIndex={0}>
    <span className="connection-pill__icon" aria-hidden="true">{waitingForWorkspace ? <Settings size={14} /> : state === "open" ? <Wifi size={14} /> : <WifiOff size={14} />}</span>
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
  const [signOutError, setSignOutError] = useState<Error>();
  const mobileMenuButtonRef = useRef<HTMLButtonElement>(null);
  const workspaceConnected = workspaceId !== "";
  const dashboardEntry = location.pathname === "/dashboard" && !workspaceConnected;
  const current = getRouteDisplay(location.pathname);
  const eventConnectionLabel = event.state === "open" ? "实时同步" : event.state === "recovery_failed" ? "恢复失败" : event.state === "reconnecting" ? "正在重连" : event.state === "connecting" ? "正在连接" : "同步未连接";
  const eventConnectionDescription = event.state === "open" ? "同步通道已连接，业务结果仍以 API 查询为准。" : event.state === "recovery_failed" ? "事件游标恢复失败，旧游标已保留。" : "事件通道正在恢复，不代表业务操作失败。";
  const connectionLabel = workspaceConnected ? eventConnectionLabel : "等待连接工作区";
  const connectionDescription = workspaceConnected ? eventConnectionDescription : "连接工作区后才会建立对应的同步通道。";
  const connectionState = workspaceConnected ? event.state : "waiting";
  const authRequired = authState.status === "authenticated" && authState.mode === "required";
  const authLabel = authRequired ? `已认证 · ${authState.session?.userLabel ?? "Owner"}` : "开发模式";
  const authDescription = authRequired ? "浏览器使用 HttpOnly Cookie Session，并校验 Origin 与 CSRF。" : "认证已关闭，仅允许本机开发模式。";

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

  return <div className={dashboardEntry ? "workbench workbench--dashboard-entry" : "workbench"}>
    <aside className="workbench__rail">
      <div className="mobile-brand-row">{dashboardEntry
        ? <Link to="/dashboard" className="entry-mark" aria-label="知序首页">序</Link>
        : <><Link to="/dashboard" className="wordmark"><span>知序</span></Link><button ref={mobileMenuButtonRef} className="mobile-menu-button" aria-label="打开主导航" onClick={() => setMobileNavigationOpen(true)}><Menu size={20} /></button></>}</div>
      <div className="rail-section"><Navigation currentPath={location.pathname} workspaceConnected={workspaceConnected} compact={dashboardEntry} /></div>
    </aside>
    {dashboardEntry ? null : <Sheet open={mobileNavigationOpen} onOpenChange={setMobileNavigationOpen} title="主导航" restoreFocusRef={mobileMenuButtonRef}>
      <div className="mobile-sheet-brand">知序</div>
      <Navigation currentPath={location.pathname} workspaceConnected={workspaceConnected} mobile onNavigate={() => setMobileNavigationOpen(false)} />
      <Tooltip content={connectionDescription}><ConnectionPill state={connectionState} label={connectionLabel} waitingForWorkspace={!workspaceConnected} /></Tooltip>
      {workspaceConnected && event.state === "recovery_failed" ? <button type="button" className="connection-retry" onClick={event.retryRecovery}>重试事件恢复</button> : null}
      {authRequired ? <button type="button" className="connection-retry auth-signout-button" onClick={() => void handleSignOut()} disabled={signingOut}><LogOut size={14} />{signingOut ? "正在退出…" : "退出登录"}</button> : null}
    </Sheet>}
    <main className="workbench__main">
      <header className="workbench__topbar">
        {dashboardEntry ? <>
          <span className="entry-topbar-title">知识脉络</span>
          <Link className="entry-topbar-boundary" to="/settings?section=workspace"><ShieldCheck size={14} />本地模式</Link>
        </> : <>
          <nav className="topbar-breadcrumb" aria-label="面包屑"><Link to="/dashboard">工作台</Link>{current?.parentLabel ? <><span aria-hidden="true">/</span><span>{current.parentLabel}</span></> : null}{current?.label && current.basePath !== "/dashboard" ? <><span aria-hidden="true">/</span><span aria-current="page">{current.label}</span></> : null}</nav>
          <div className="topbar-actions">
          <Tooltip content={connectionDescription}><ConnectionPill state={connectionState} label={connectionLabel} waitingForWorkspace={!workspaceConnected} /></Tooltip>
          <Tooltip content={authDescription}><span className={`local-mode-badge ${authRequired ? "local-mode-badge--authenticated" : ""}`}>{authRequired ? <ShieldCheck size={14} /> : <ShieldAlert size={14} />}{authLabel}</span></Tooltip>
          <DropdownMenu label="工作台快捷入口" trigger={<button type="button" className="topbar-menu-button" aria-label="打开工作台快捷入口"><MoreHorizontal size={19} /></button>}>
            <DropdownMenuItem asChild><Link to="/settings?section=workspace">工作区设置</Link></DropdownMenuItem>
            <DropdownMenuItem asChild><Link to="/settings?section=system">系统状态</Link></DropdownMenuItem>
            {workspaceConnected && knowledgeHome ? <DropdownMenuItem asChild><Link to={knowledgeHome.basePath}>{knowledgeHome.label}</Link></DropdownMenuItem> : null}
            {authRequired ? <><DropdownMenuSeparator /><DropdownMenuItem disabled={signingOut} onSelect={() => { void handleSignOut(); }}><LogOut size={15} />{signingOut ? "正在退出…" : "退出登录"}</DropdownMenuItem></> : null}
          </DropdownMenu>
          </div>
        </>}
      </header>
      {signOutError ? <p className="topbar-auth-error" role="alert">退出登录失败：{signOutError.message}。请重试。</p> : null}
      <div className="workbench__content"><Suspense fallback={<div className="page-loading">正在加载…</div>}><Outlet /></Suspense></div>
    </main>
  </div>;
};
