import { useQuery } from "@tanstack/react-query";
import { Archive, Bot, ChevronDown, FolderCog, ServerCog, ShieldCheck } from "lucide-react";
import { useEffect } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";

import { getWorkspace } from "../../api/workspace";
import { setActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";
import { useAuth } from "../../app/auth-context";
import { runtimeMode } from "../../app/runtime-mode";
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

const WorkspaceSettingsPanel = ({ workspaceId }: { workspaceId: string }) => {
  const navigate = useNavigate();
  const workspace = useQuery({
    queryKey: ["workspace", workspaceId],
    queryFn: ({ signal }) => getWorkspace(workspaceId, signal),
    enabled: workspaceId !== "",
    retry: false,
  });
  const reconnect = (): void => {
    if (runtimeMode === "direct") setActiveWorkspaceId("");
    void navigate("/workspace");
  };

  return <section className="settings-section" aria-labelledby="workspace-settings-title">
    <header className="settings-section__header"><div><h2 id="workspace-settings-title">工作区</h2><p>当前目录与 Git 状态。</p></div>{workspaceId !== "" ? <Button variant="secondary" size="sm" onClick={reconnect}>切换工作区</Button> : null}</header>
    {workspaceId === "" ? <EmptyState title="尚未连接工作区" description="连接本地目录后即可使用知识与创作功能。" action={<Button asChild variant="primary"><Link to="/workspace">连接工作区</Link></Button>} /> : workspace.isPending ? <p role="status">正在读取工作区…</p> : workspace.isError ? <div className="ui-state ui-state--error" role="alert"><strong>工作区不可用</strong><p>{workspace.error.message}</p><Button variant="secondary" size="sm" onClick={reconnect}>重新连接</Button></div> : <>
      <dl className="settings-facts">
        <div><dt>名称</dt><dd>{workspace.data.name}</dd></div>
        <div><dt>宿主机目录</dt><dd className="mono">{workspace.data.rootPath}</dd></div>
        <div><dt>连接状态</dt><dd><Badge tone="success">{workspace.data.status}</Badge></dd></div>
        <div><dt>Workspace ID</dt><dd className="mono">{workspace.data.id}</dd></div>
      </dl>
      <div className="settings-divider" />
      <div className="settings-subsection"><h3>Git</h3><dl className="settings-facts settings-facts--compact"><div><dt>仓库</dt><dd>{workspace.data.git.present ? "已检测" : "未检测到"}</dd></div><div><dt>分支</dt><dd>{workspace.data.git.branch || "无分支"}</dd></div><div><dt>工作树</dt><dd><Badge tone={workspace.data.git.dirty ? "danger" : "success"}>{workspace.data.git.dirty ? "有未提交修改" : "干净"}</Badge></dd></div><div><dt>HEAD</dt><dd className="mono">{workspace.data.git.head || "未提供"}</dd></div></dl></div>
      <div className="settings-section__actions"><Button asChild variant="secondary"><Link to="/inbox">前往资料收件箱</Link></Button></div>
    </>}
  </section>;
};

const WorkspaceAndGitSettings = () => {
  const workspaceId = useActiveWorkspaceId();
  return <><WorkspaceSettingsPanel workspaceId={workspaceId} />{workspaceId === "" ? null : <GitRemoteSettingsPanel />}</>;
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
