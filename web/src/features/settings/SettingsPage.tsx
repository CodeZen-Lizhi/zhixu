import { Archive, Bot, ChevronDown, FolderCog, ServerCog, ShieldCheck } from "lucide-react";
import { useEffect } from "react";
import { Link, useSearchParams } from "react-router-dom";

import { useActiveWorkspace } from "../../app/WorkspaceCacheBoundary";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { useAuth } from "../../app/auth-context";
import { Badge, Button, DropdownMenu, DropdownMenuItem, EmptyState, PageHeader, UnavailableState } from "../../shared/ui";
import { SystemStatusPage } from "../system-status/SystemStatusPage";
import { ApiTokenSettings } from "./ApiTokenSettings";
import { AttachmentExportPanel } from "./AttachmentExportPanel";
import { GitRemoteSettingsPanel } from "./GitRemoteSettingsPanel";
import { ModelSettingsPanel } from "./ModelSettingsPanel";

const settingsSections = [
  { id: "workspace", label: "工作区", icon: FolderCog },
  { id: "models", label: "模型与检索", icon: Bot },
  { id: "exports", label: "数据导出", icon: Archive },
  { id: "access", label: "访问权限", icon: ShieldCheck },
  { id: "system", label: "系统状态", icon: ServerCog },
] as const;

type SettingsSection = typeof settingsSections[number]["id"];

const isSettingsSection = (value: string | null): value is SettingsSection =>
  settingsSections.some((section) => section.id === value);

const availabilityLabel = (availability: string): string => availability === "available" ? "可用" : availability === "migration_required" ? "需要迁移" : "暂不可用";
const availabilityTone = (availability: string): "success" | "warning" | "danger" => availability === "available" ? "success" : availability === "migration_required" ? "warning" : "danger";
const workspaceStatusLabel = (status: string): string => status === "active" ? "已连接" : status;

const WorkspaceSettingsPanel = () => {
  const { status, workspace, error, refresh } = useActiveWorkspace();

  return <section className="settings-section" aria-labelledby="workspace-settings-title">
    <header className="settings-section__header"><div><h2 id="workspace-settings-title">工作区</h2><p>当前 Workspace 的只读投影。</p></div><Button variant="secondary" size="sm" onClick={() => void refresh()}>重新读取</Button></header>
    {status === "loading" ? <p role="status">正在读取当前 Workspace…</p> : status === "error" ? <div className="ui-state ui-state--error" role="alert"><strong>工作区信息不可用</strong><p>{error?.message ?? "当前 Workspace API 暂不可用。"}</p></div> : workspace === undefined ? <EmptyState title="尚未激活工作区" description="请在本机命令行执行启动命令指定知识库目录。" action={<Button asChild variant="primary"><Link to="/workspace">查看命令</Link></Button>} /> : <>
      <dl className="settings-facts">
        <div><dt>名称</dt><dd>{workspace.name}</dd></div>
        <div><dt>宿主机目录</dt><dd className="mono">{workspace.rootPath}</dd></div>
        <div><dt>连接状态</dt><dd><Badge tone="success">{workspaceStatusLabel(workspace.status)}</Badge></dd></div>
        <div><dt>可用性</dt><dd><Badge tone={availabilityTone(workspace.availability)}>{availabilityLabel(workspace.availability)}</Badge></dd></div>
        <div><dt>Workspace ID</dt><dd className="mono">{workspace.id}</dd></div>
        <div><dt>版本</dt><dd className="mono">{workspace.version}</dd></div>
      </dl>
      <div className="settings-divider" />
      <div className="settings-subsection"><h3>切换方式</h3><p>请在本机终端执行 <code>./zhixu workspace switch &lt;宿主机绝对目录&gt;</code>。切换完成后刷新本页；网页不会提交根目录。</p></div>
      <div className="settings-section__actions"><Button asChild variant="secondary"><Link to="/inbox">前往资料收件箱</Link></Button></div>
    </>}
  </section>;
};

const WorkspaceAndGitSettings = () => {
  const workspaceId = useActiveWorkspaceId();
  return <><WorkspaceSettingsPanel />{workspaceId === "" ? null : <GitRemoteSettingsPanel />}</>;
};

export const SettingsPage = () => {
  const { state: authState } = useAuth();
  const [params, setParams] = useSearchParams();
  const rawSection = params.get("section");
  const section: SettingsSection = isSettingsSection(rawSection) ? rawSection : "workspace";

  useEffect(() => {
    if (rawSection === null || isSettingsSection(rawSection)) return;
    const next = new URLSearchParams(params);
    next.set("section", "workspace");
    setParams(next, { replace: true });
  }, [params, rawSection, setParams]);

  const hrefFor = (target: SettingsSection): string => {
    const next = new URLSearchParams(params);
    next.set("section", target);
    return `?${next.toString()}`;
  };
  const currentSection = settingsSections.find((item) => item.id === section) ?? settingsSections[0];

  return <div className="page-stack settings-page">
    <PageHeader title="设置" description="管理工作区、Git、模型、导出和访问权限。" />
    <div className="settings-mobile-select"><DropdownMenu label="设置分类" align="start" trigger={<button type="button" className="settings-mobile-trigger" aria-label={`选择设置分类，当前${currentSection.label}`}><currentSection.icon size={17} strokeWidth={1.8} /><span>{currentSection.label}</span><ChevronDown size={17} /></button>}>{settingsSections.map(({ id, label, icon: Icon }) => <DropdownMenuItem key={id} asChild><Link to={hrefFor(id)} aria-current={section === id ? "page" : undefined}><Icon size={17} strokeWidth={1.8} /><span>{label}</span></Link></DropdownMenuItem>)}</DropdownMenu></div>
    <div className="settings-layout">
      <nav className="settings-navigation" aria-label="设置分类">{settingsSections.map(({ id, label, icon: Icon }) => <Link key={id} className={section === id ? "settings-navigation__link settings-navigation__link--active" : "settings-navigation__link"} aria-current={section === id ? "page" : undefined} to={hrefFor(id)}><Icon size={17} strokeWidth={1.8} /><span>{label}</span></Link>)}</nav>
      <div className="settings-content">
        {section === "workspace" ? <WorkspaceAndGitSettings /> : null}
        {section === "models" ? <ModelSettingsPanel /> : null}
        {section === "exports" ? <AttachmentExportPanel /> : null}
        {section === "access" ? authState.mode === "required" ? <ApiTokenSettings /> : <UnavailableState title="开发模式未启用 API Token" description="启用 required 认证后才能管理自动化凭据。" /> : null}
        {section === "system" ? <section className="settings-section" aria-labelledby="system-settings-title"><header className="settings-section__header"><div><h2 id="system-settings-title">系统状态</h2><p>API 与运行依赖的实时状态。</p></div></header><SystemStatusPage display="full" /></section> : null}
      </div>
    </div>
  </div>;
};
