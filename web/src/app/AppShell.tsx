import {
  Activity,
  BookOpenCheck,
  Brain,
  ChevronDown,
  Compass,
  FileCheck2,
  FileText,
  FolderOpen,
  GitBranch,
  GraduationCap,
  HeartPulse,
  Inbox,
  Layers3,
  LayoutDashboard,
  LogOut,
  Menu,
  MessageSquare,
  Network,
  Plus,
  Search,
  Settings,
  ShieldAlert,
  ShieldCheck,
  SquarePen,
  UserRoundCheck,
  Wifi,
  WifiOff,
  type LucideIcon,
} from "lucide-react";
import { forwardRef, Suspense, useEffect, useId, useRef, useState } from "react";
import { Link, Outlet, useLocation } from "react-router-dom";

import { useEventStore } from "../events/event-store";
import { QuickCaptureDialog } from "../features/capture/QuickCaptureDialog";
import { quickCaptureIntentEvent } from "../features/capture/quick-capture-intent";
import { Sheet, Tooltip } from "../shared/ui";
import { RouteContentBoundary } from "../routes/RouteContentBoundary";
import { getRouteDisplay, routeBelongsToSection, routeDisplayRegistry, type RouteNavigationIcon, type RouteSection } from "../routes/route-display";
import { useActiveWorkspaceId } from "./active-workspace";
import { useAuth } from "./auth-context";

interface NavigationDestination {
  icon: LucideIcon;
  label: string;
  path: string;
}

interface PrimaryNavigationItem extends NavigationDestination {
  section: RouteSection;
}

interface OrderedPrimaryNavigationItem extends PrimaryNavigationItem {
  order: number;
}

interface FooterNavigationItem extends NavigationDestination {
  section: RouteSection;
  order: number;
}

interface KnowledgeNavigationItem extends NavigationDestination {
  groupIcon: LucideIcon;
  groupLabel: string;
  groupOrder: number;
  order: number;
}

interface KnowledgeNavigationGroup {
  icon: LucideIcon;
  label: string;
  order: number;
  items: KnowledgeNavigationItem[];
}

const navigationIcons: Record<RouteNavigationIcon, LucideIcon> = {
  activity: Activity,
  "book-open-check": BookOpenCheck,
  brain: Brain,
  compass: Compass,
  dashboard: LayoutDashboard,
  "file-check": FileCheck2,
  "file-text": FileText,
  "folder-open": FolderOpen,
  "git-branch": GitBranch,
  "graduation-cap": GraduationCap,
  "heart-pulse": HeartPulse,
  inbox: Inbox,
  layers: Layers3,
  "message-square": MessageSquare,
  network: Network,
  search: Search,
  settings: Settings,
  "square-pen": SquarePen,
  "user-round-check": UserRoundCheck,
};

const primaryNavigation = routeDisplayRegistry.flatMap<OrderedPrimaryNavigationItem>((route) => {
  const navigation = route.navigation;
  if (navigation?.placement !== "primary") return [];
  const item: OrderedPrimaryNavigationItem = { path: route.basePath, label: navigation.label, icon: navigationIcons[navigation.icon], section: route.section, order: navigation.order };
  return [item];
}).sort((left, right) => left.order - right.order);

const footerNavigation = routeDisplayRegistry.flatMap<FooterNavigationItem>((route) => {
  const navigation = route.navigation;
  if (navigation?.placement !== "footer") return [];
  return [{ path: route.basePath, label: navigation.label, icon: navigationIcons[navigation.icon], section: route.section, order: navigation.order }];
}).sort((left, right) => left.order - right.order);

const knowledgeNavigationItems = routeDisplayRegistry.flatMap<KnowledgeNavigationItem>((route) => {
  const navigation = route.navigation;
  if (route.section !== "knowledge" || navigation === undefined || !("group" in navigation)) return [];
  return [{
    path: route.basePath,
    label: route.label,
    icon: navigationIcons[navigation.icon],
    groupIcon: navigationIcons[navigation.group.icon],
    groupLabel: navigation.group.label,
    groupOrder: navigation.group.order,
    order: navigation.group.itemOrder,
  }];
});

const knowledgeNavigationGroups = Array.from(knowledgeNavigationItems.reduce((groups, item) => {
  const group = groups.get(item.groupLabel) ?? { icon: item.groupIcon, label: item.groupLabel, order: item.groupOrder, items: [] };
  group.items.push(item);
  groups.set(item.groupLabel, group);
  return groups;
}, new Map<string, KnowledgeNavigationGroup>()).values()).sort((left, right) => left.order - right.order).map((group) => ({ ...group, items: group.items.sort((left, right) => left.order - right.order) }));

const getCurrentKnowledgeNavigationItem = (currentPath: string): KnowledgeNavigationItem | undefined => {
  const currentRoute = getRouteDisplay(currentPath);
  if (currentRoute?.section !== "knowledge") return undefined;
  return knowledgeNavigationItems.find((item) => item.path === currentRoute.basePath)
    ?? knowledgeNavigationItems.find((item) => item.label === currentRoute.parentLabel);
};

interface KnowledgeNavigationState {
  expanded: boolean;
  expandedGroups: ReadonlySet<string>;
  toggleExpanded: () => void;
  toggleGroup: (groupLabel: string) => void;
}

const useKnowledgeNavigationState = (currentPath: string): KnowledgeNavigationState => {
  const currentGroupLabel = getCurrentKnowledgeNavigationItem(currentPath)?.groupLabel;
  const [expanded, setExpanded] = useState(true);
  const [expandedGroups, setExpandedGroups] = useState<ReadonlySet<string>>(() => new Set(currentGroupLabel === undefined ? [] : [currentGroupLabel]));

  useEffect(() => {
    if (!routeBelongsToSection(currentPath, "knowledge")) return;
    setExpanded(true);
    if (currentGroupLabel === undefined) return;
    setExpandedGroups((current) => {
      if (current.has(currentGroupLabel)) return current;
      const next = new Set(current);
      next.add(currentGroupLabel);
      return next;
    });
  }, [currentGroupLabel, currentPath]);

  const toggleGroup = (groupLabel: string): void => {
    setExpandedGroups((current) => {
      const next = new Set(current);
      if (next.has(groupLabel)) next.delete(groupLabel);
      else next.add(groupLabel);
      return next;
    });
  };

  return {
    expanded,
    expandedGroups,
    toggleExpanded: () => setExpanded((current) => !current),
    toggleGroup,
  };
};

interface NavigationProps {
  currentPath: string;
  compact?: boolean;
  knowledgeState: KnowledgeNavigationState;
  mobile?: boolean;
  onNavigate?: () => void;
  workspaceConnected: boolean;
}

const KnowledgeNavigation = ({ item, currentPath, onNavigate, state }: { item: PrimaryNavigationItem; currentPath: string; onNavigate: (() => void) | undefined; state: KnowledgeNavigationState }) => {
  const Icon = item.icon;
  const navigationId = useId();
  const groupsId = `${navigationId}-knowledge-groups`;
  const currentDestination = getCurrentKnowledgeNavigationItem(currentPath);
  const currentGroupLabel = currentDestination?.groupLabel;
  const active = routeBelongsToSection(currentPath, "knowledge");

  return <div className="rail-primary-item rail-knowledge">
    <button
      type="button"
      className={active ? "rail-disclosure rail-disclosure--knowledge rail-disclosure--current-path" : "rail-disclosure rail-disclosure--knowledge"}
      aria-controls={groupsId}
      aria-current={active ? "true" : undefined}
      aria-expanded={state.expanded}
      onClick={state.toggleExpanded}
    >
      <Icon size={18} strokeWidth={1.8} aria-hidden="true" />
      <span className="rail-disclosure__label">{item.label}</span>
      <ChevronDown className="rail-disclosure__chevron" size={16} strokeWidth={1.8} aria-hidden="true" />
    </button>
    <ul id={groupsId} className="rail-knowledge__groups" hidden={!state.expanded}>
      {knowledgeNavigationGroups.map((group) => {
        const GroupIcon = group.icon;
        const groupExpanded = state.expandedGroups.has(group.label);
        const groupActive = group.label === currentGroupLabel;
        const destinationsId = `${navigationId}-knowledge-group-${String(group.order)}`;
        return <li key={group.label} className="rail-knowledge__group">
          <button
            type="button"
            className={groupActive ? "rail-disclosure rail-disclosure--group rail-disclosure--current-path" : "rail-disclosure rail-disclosure--group"}
            aria-controls={destinationsId}
            aria-current={groupActive ? "true" : undefined}
            aria-expanded={groupExpanded}
            onClick={() => state.toggleGroup(group.label)}
          >
            <GroupIcon className="rail-disclosure__group-icon" size={15} strokeWidth={1.7} aria-hidden="true" />
            <span className="rail-disclosure__label">{group.label}</span>
            <ChevronDown className="rail-disclosure__chevron" size={15} strokeWidth={1.8} aria-hidden="true" />
          </button>
          <ul id={destinationsId} className="rail-knowledge__destinations" hidden={!groupExpanded}>
            {group.items.map((destination) => {
              const DestinationIcon = destination.icon;
              const destinationActive = destination.path === currentDestination?.path;
              return <li key={destination.path}>
                <Link
                  className={destinationActive ? "rail-link rail-link--knowledge rail-link--active" : "rail-link rail-link--knowledge"}
                  to={destination.path}
                  aria-current={destinationActive ? "page" : undefined}
                  onClick={onNavigate}
                >
                  <DestinationIcon size={16} strokeWidth={1.8} aria-hidden="true" />
                  <span>{destination.label}</span>
                </Link>
              </li>;
            })}
          </ul>
        </li>;
      })}
    </ul>
  </div>;
};

const PrimaryItem = ({ item, currentPath, compact, knowledgeState, onNavigate }: { item: PrimaryNavigationItem; currentPath: string; compact: boolean; knowledgeState: KnowledgeNavigationState; onNavigate: (() => void) | undefined }) => {
  if (item.section === "knowledge") return <KnowledgeNavigation item={item} currentPath={currentPath} onNavigate={onNavigate} state={knowledgeState} />;

  const Icon = item.icon;
  const active = routeBelongsToSection(currentPath, item.section);
  const currentDestination = currentPath === item.path;
  const linkClassName = active ? "rail-link rail-link--active" : "rail-link";
  const link = <Link className={linkClassName} aria-current={currentDestination ? "page" : undefined} aria-label={compact ? item.label : undefined} to={item.path} onClick={onNavigate}><Icon size={18} strokeWidth={1.8} /><span>{item.label}</span></Link>;
  return compact ? <Tooltip content={item.label}>{link}</Tooltip> : link;
};

const Navigation = ({ currentPath, compact = false, knowledgeState, mobile = false, onNavigate, workspaceConnected }: NavigationProps) => {
  const visibleNavigation = workspaceConnected ? primaryNavigation : primaryNavigation.filter((item) => item.section === "dashboard");

  return <nav className={mobile ? "rail-navigation rail-navigation--mobile" : compact ? "rail-navigation rail-navigation--compact" : "rail-navigation"} aria-label="主导航">
    <div className="rail-navigation__primary">{visibleNavigation.map((item) => <PrimaryItem key={item.section} item={item} currentPath={currentPath} compact={compact} knowledgeState={knowledgeState} onNavigate={onNavigate} />)}</div>
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
  const knowledgeNavigationState = useKnowledgeNavigationState(location.pathname);
  const [mobileNavigationOpen, setMobileNavigationOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [signOutError, setSignOutError] = useState<Error>();
  const [quickCaptureOpen, setQuickCaptureOpen] = useState(false);
  const [captureNotice, setCaptureNotice] = useState("");
  const mobileMenuButtonRef = useRef<HTMLButtonElement>(null);
  const quickCaptureButtonRef = useRef<HTMLButtonElement>(null);
  const quickCaptureRestoreRef = useRef<HTMLElement | null>(null);
  const workspaceConnected = workspaceId !== "";
  const dashboardEntry = location.pathname === "/dashboard" && !workspaceConnected;
  const connectedDashboard = location.pathname === "/dashboard" && workspaceConnected;
  const workbenchVariant = dashboardEntry ? "workbench--dashboard-entry" : connectedDashboard ? "workbench--dashboard" : "";
  const current = getRouteDisplay(location.pathname);
  const eventConnectionLabel = event.state === "open" ? "实时同步" : event.state === "recovery_failed" ? "恢复失败" : event.state === "reconnecting" ? "正在重连" : event.state === "connecting" ? "正在连接" : "同步未连接";
  const eventConnectionDescription = event.state === "open" ? "同步通道已连接，业务结果仍以 API 查询为准。" : event.state === "recovery_failed" ? "事件游标恢复失败，旧游标已保留。" : "事件通道正在恢复，不代表业务操作失败。";
  const connectionLabel = workspaceConnected ? eventConnectionLabel : "等待连接工作区";
  const connectionDescription = workspaceConnected ? eventConnectionDescription : "连接工作区后才会建立对应的同步通道。";
  const connectionState = workspaceConnected ? event.state : "waiting";
  const authRequired = authState.status === "authenticated" && authState.mode === "required";
  const authLabel = authRequired ? `已认证 · ${authState.session?.userLabel ?? "Owner"}` : "开发模式";
  const authDescription = authRequired ? "浏览器使用 HttpOnly Cookie Session，并校验 Origin 与 CSRF。" : "认证已关闭，仅允许本机开发模式。";

  const openQuickCapture = (restoreTarget?: HTMLElement | null): void => {
    quickCaptureRestoreRef.current = restoreTarget ?? quickCaptureButtonRef.current;
    setCaptureNotice("");
    setQuickCaptureOpen(true);
  };

  useEffect(() => {
    if (!workspaceConnected) return undefined;
    const handleShortcut = (event: KeyboardEvent): void => {
      if (quickCaptureOpen || event.defaultPrevented || event.repeat || event.altKey || !event.shiftKey || (!event.metaKey && !event.ctrlKey) || event.key.toLowerCase() !== "k") return;
      event.preventDefault();
      const active = document.activeElement instanceof HTMLElement ? document.activeElement : quickCaptureButtonRef.current;
      quickCaptureRestoreRef.current = active;
      setCaptureNotice("");
      setQuickCaptureOpen(true);
    };
    window.addEventListener("keydown", handleShortcut);
    return () => window.removeEventListener("keydown", handleShortcut);
  }, [quickCaptureOpen, workspaceConnected]);

  useEffect(() => {
    if (!workspaceConnected) return undefined;
    const handleIntent = (): void => {
      if (quickCaptureOpen) return;
      quickCaptureRestoreRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : quickCaptureButtonRef.current;
      setCaptureNotice("");
      setQuickCaptureOpen(true);
    };
    window.addEventListener(quickCaptureIntentEvent, handleIntent);
    return () => window.removeEventListener(quickCaptureIntentEvent, handleIntent);
  }, [quickCaptureOpen, workspaceConnected]);

  useEffect(() => {
    if (captureNotice === "") return undefined;
    const timer = window.setTimeout(() => setCaptureNotice(""), 5000);
    return () => window.clearTimeout(timer);
  }, [captureNotice]);

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

  return <div className={`workbench${workbenchVariant === "" ? "" : ` ${workbenchVariant}`}`}>
    <aside className="workbench__rail">
      <div className="mobile-brand-row">{dashboardEntry
        ? <Link to="/dashboard" className="entry-mark" aria-label="知序首页">序</Link>
        : <><Link to="/dashboard" className="wordmark"><span>知序</span></Link><button ref={mobileMenuButtonRef} className="mobile-menu-button" aria-label="打开主导航" onClick={() => setMobileNavigationOpen(true)}><Menu size={20} /></button></>}</div>
      <div className="rail-section"><Navigation currentPath={location.pathname} workspaceConnected={workspaceConnected} compact={dashboardEntry} knowledgeState={knowledgeNavigationState} /></div>
    </aside>
    {dashboardEntry ? null : <Sheet open={mobileNavigationOpen} onOpenChange={setMobileNavigationOpen} title="主导航" restoreFocusRef={mobileMenuButtonRef}>
      <div className="mobile-sheet-brand">知序</div>
      <Navigation currentPath={location.pathname} workspaceConnected={workspaceConnected} knowledgeState={knowledgeNavigationState} mobile onNavigate={() => setMobileNavigationOpen(false)} />
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
          {workspaceConnected ? <button
            ref={quickCaptureButtonRef}
            className="ui-button ui-button--primary ui-button--sm quick-capture-trigger"
            type="button"
            onClick={(event) => openQuickCapture(event.currentTarget)}
          ><Plus size={16} /><span>快速记录</span></button> : null}
          {captureNotice === "" ? null : <span className="quick-capture-notice" role="status">{captureNotice}</span>}
          <Tooltip content={connectionDescription}><ConnectionPill state={connectionState} label={connectionLabel} waitingForWorkspace={!workspaceConnected} /></Tooltip>
          <Tooltip content={authDescription}><span className={`local-mode-badge ${authRequired ? "local-mode-badge--authenticated" : ""}`}>{authRequired ? <ShieldCheck size={14} /> : <ShieldAlert size={14} />}{authLabel}</span></Tooltip>
          {authRequired ? <Tooltip content="退出登录"><button type="button" className="topbar-signout-button" aria-label="退出登录" disabled={signingOut} onClick={() => { void handleSignOut(); }}><LogOut size={17} /></button></Tooltip> : null}
          </div>
        </>}
      </header>
      {signOutError ? <p className="topbar-auth-error" role="alert">退出登录失败：{signOutError.message}。请重试。</p> : null}
      <div className="workbench__content">
        <RouteContentBoundary key={workspaceId} resetKey={location.key}>
          <Suspense fallback={<div className="page-loading">正在加载…</div>}><Outlet /></Suspense>
        </RouteContentBoundary>
      </div>
    </main>
    {workspaceConnected ? <QuickCaptureDialog
      key={workspaceId}
      open={quickCaptureOpen}
      onOpenChange={setQuickCaptureOpen}
      onCaptured={(capture) => setCaptureNotice(`已存入收件箱：${capture.displayName}`)}
      restoreFocusRef={quickCaptureRestoreRef}
      workspaceId={workspaceId}
    /> : null}
  </div>;
};
