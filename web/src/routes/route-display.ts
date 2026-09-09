export type RouteSection = "dashboard" | "knowledge" | "authoring" | "settings";

export type RouteNavigationIcon =
  | "activity"
  | "book-open-check"
  | "brain"
  | "compass"
  | "dashboard"
  | "file-check"
  | "file-text"
  | "folder-open"
  | "git-branch"
  | "graduation-cap"
  | "heart-pulse"
  | "inbox"
  | "layers"
  | "message-square"
  | "network"
  | "search"
  | "settings"
  | "square-pen"
  | "user-round-check";

interface RouteNavigationGroup {
  icon: RouteNavigationIcon;
  label: string;
  order: number;
  itemOrder: number;
}

export type RouteNavigation =
  | { placement: "primary"; label: string; order: number; icon: RouteNavigationIcon; group?: RouteNavigationGroup }
  | { placement: "submenu"; icon: RouteNavigationIcon; group: RouteNavigationGroup }
  | { placement: "footer"; label: string; order: number; icon: RouteNavigationIcon };

export interface RouteDisplay {
  basePath: string;
  label: string;
  parentLabel?: string;
  section: RouteSection;
  exact?: boolean;
  navigation?: RouteNavigation;
}

export const routeDisplayRegistry: readonly RouteDisplay[] = [
  { basePath: "/", label: "连接工作区", section: "settings", exact: true },
  { basePath: "/dashboard", label: "工作台", section: "dashboard", navigation: { placement: "primary", label: "工作台", order: 0, icon: "dashboard" } },
  { basePath: "/inbox", label: "资料收件箱", section: "knowledge", navigation: { placement: "primary", label: "知识", order: 1, icon: "inbox", group: { label: "资料", icon: "folder-open", order: 0, itemOrder: 0 } } },
  { basePath: "/captures", label: "快速记录", parentLabel: "资料收件箱", section: "knowledge" },
  { basePath: "/documents", label: "资料版本", parentLabel: "资料收件箱", section: "knowledge" },
  { basePath: "/search", label: "检索", section: "knowledge", navigation: { placement: "submenu", icon: "search", group: { label: "探索", icon: "compass", order: 1, itemOrder: 0 } } },
  { basePath: "/chat", label: "对话", section: "knowledge", navigation: { placement: "submenu", icon: "message-square", group: { label: "探索", icon: "compass", order: 1, itemOrder: 1 } } },
  { basePath: "/graph", label: "知识图谱", section: "knowledge", navigation: { placement: "submenu", icon: "git-branch", group: { label: "探索", icon: "compass", order: 1, itemOrder: 2 } } },
  { basePath: "/collections", label: "集合", section: "knowledge", navigation: { placement: "submenu", icon: "layers", group: { label: "组织", icon: "network", order: 2, itemOrder: 0 } } },
  { basePath: "/timeline", label: "时间线", section: "knowledge", navigation: { placement: "submenu", icon: "activity", group: { label: "组织", icon: "network", order: 2, itemOrder: 1 } } },
  { basePath: "/health", label: "知识健康", section: "knowledge", navigation: { placement: "submenu", icon: "heart-pulse", group: { label: "组织", icon: "network", order: 2, itemOrder: 2 } } },
  { basePath: "/review", label: "复习", section: "knowledge", navigation: { placement: "submenu", icon: "book-open-check", group: { label: "学习", icon: "graduation-cap", order: 3, itemOrder: 0 } } },
  { basePath: "/memories", label: "记忆", section: "knowledge", navigation: { placement: "submenu", icon: "brain", group: { label: "学习", icon: "graduation-cap", order: 3, itemOrder: 1 } } },
  { basePath: "/interviews", label: "访谈", section: "knowledge", navigation: { placement: "submenu", icon: "user-round-check", group: { label: "学习", icon: "graduation-cap", order: 3, itemOrder: 2 } } },
  { basePath: "/authoring", label: "创作", section: "authoring", navigation: { placement: "primary", label: "创作", order: 2, icon: "square-pen" } },
  { basePath: "/authoring/new", label: "新建文章", parentLabel: "创作", section: "authoring" },
  { basePath: "/authoring/notes", label: "合成笔记", parentLabel: "创作", section: "authoring" },
  { basePath: "/authoring/documents", label: "文档历史", parentLabel: "创作", section: "authoring" },
  { basePath: "/authoring/organize", label: "整理成文", parentLabel: "创作", section: "authoring" },
  { basePath: "/proposals", label: "提案", section: "authoring" },
  { basePath: "/workflows", label: "流程", section: "authoring" },
  { basePath: "/artifacts", label: "产物", section: "authoring" },
  { basePath: "/workspace", label: "连接工作区", section: "settings" },
  { basePath: "/settings", label: "设置", section: "settings", navigation: { placement: "footer", label: "设置", order: 0, icon: "settings" } },
] as const;

export const getRouteDisplay = (pathname: string): RouteDisplay | undefined => {
  const matches = routeDisplayRegistry.filter((route) => route.exact
    ? pathname === route.basePath
    : pathname === route.basePath || pathname.startsWith(`${route.basePath}/`));

  return matches.sort((left, right) => right.basePath.length - left.basePath.length)[0];
};

export const routeBelongsToSection = (pathname: string, section: RouteSection): boolean =>
  getRouteDisplay(pathname)?.section === section;
