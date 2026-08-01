import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type SyntheticEvent } from "react";
import { useNavigate } from "react-router-dom";

import { createWorkspace, getWorkspace, WorkspaceApiError } from "../../api/workspace";
import { setActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";
import { Button, PageHeader } from "../../shared/ui";

const ErrorNotice = ({ error }: { error: Error }) => <div className="workspace-alert workspace-alert--error" role="alert"><strong>连接失败</strong><span>{error.message}</span>{error instanceof WorkspaceApiError ? <code>{error.code}</code> : null}</div>;

export const WorkspacePage = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const workspaceId = useActiveWorkspaceId();
  const [mode, setMode] = useState<"create" | "existing">("create");
  const [name, setName] = useState("");
  const [rootPath, setRootPath] = useState("");
  const [existingWorkspaceId, setExistingWorkspaceId] = useState("");
  const [initializeGit, setInitializeGit] = useState(false);

  const workspaceQuery = useQuery({
    queryKey: ["workspace", workspaceId],
    queryFn: ({ signal }) => getWorkspace(workspaceId, signal),
    enabled: workspaceId !== "",
    retry: false,
  });

  useEffect(() => {
    if (workspaceQuery.data !== undefined) void navigate("/settings?section=workspace", { replace: true });
  }, [navigate, workspaceQuery.data]);

  const createMutation = useMutation({
    mutationFn: createWorkspace,
    onSuccess: (workspace) => {
      setActiveWorkspaceId(workspace.id);
      queryClient.setQueryData(["workspace", workspace.id], workspace);
      void navigate("/settings?section=workspace", { replace: true });
    },
  });
  const openMutation = useMutation({
    mutationFn: (id: string) => getWorkspace(id),
    onSuccess: (workspace) => {
      setActiveWorkspaceId(workspace.id);
      queryClient.setQueryData(["workspace", workspace.id], workspace);
      void navigate("/settings?section=workspace", { replace: true });
    },
  });

  const submitCreate = (event: SyntheticEvent<HTMLFormElement>): void => {
    event.preventDefault();
    createMutation.mutate({ name: name.trim(), rootPath: rootPath.trim(), initializeGit });
  };
  const submitOpen = (event: SyntheticEvent<HTMLFormElement>): void => {
    event.preventDefault();
    openMutation.mutate(existingWorkspaceId.trim());
  };

  if (workspaceId !== "" && workspaceQuery.isPending) return <div className="page-stack"><PageHeader title="连接工作区" description="正在恢复当前工作区。" /><p role="status">正在读取工作区…</p></div>;
  if (workspaceId !== "" && workspaceQuery.isError) return <div className="page-stack"><PageHeader title="连接工作区" description="当前工作区无法读取。" /><ErrorNotice error={workspaceQuery.error} /><Button variant="secondary" onClick={() => setActiveWorkspaceId("")}>清除本地引用</Button></div>;

  return <div className="page-stack workspace-page">
    <PageHeader title="连接工作区" description="选择知序可以访问的本地目录。" />
    <section className="workspace-connection" aria-labelledby="workspace-setup-title">
      <header className="workspace-connection__heading"><h2 id="workspace-setup-title">连接方式</h2><p>创建新工作区，或使用已有 Workspace ID。</p></header>
      <div className="workspace-mode-switch" role="group" aria-label="连接方式"><button type="button" aria-pressed={mode === "create"} className={mode === "create" ? "is-active" : ""} onClick={() => setMode("create")}>新建</button><button type="button" aria-pressed={mode === "existing"} className={mode === "existing" ? "is-active" : ""} onClick={() => setMode("existing")}>使用 Workspace ID</button></div>
      {mode === "create" ? <form className="workspace-form" onSubmit={submitCreate}><label><span>名称</span><input value={name} onChange={(event) => setName(event.target.value)} required /></label><label><span>宿主机目录</span><input value={rootPath} onChange={(event) => setRootPath(event.target.value)} required placeholder="/Users/me/knowledge" /></label><label className="checkbox-field"><input type="checkbox" checked={initializeGit} onChange={(event) => setInitializeGit(event.target.checked)} /><span>需要时初始化 Git</span></label><Button type="submit" disabled={createMutation.isPending}>{createMutation.isPending ? "正在创建…" : "创建工作区"}</Button></form> : <form className="workspace-open-form" onSubmit={submitOpen}><label><span>Workspace ID</span><input value={existingWorkspaceId} onChange={(event) => setExistingWorkspaceId(event.target.value)} placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" pattern="[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}" title="请输入规范 UUID" required /></label><Button type="submit" variant="secondary" disabled={openMutation.isPending}>{openMutation.isPending ? "正在打开…" : "打开工作区"}</Button></form>}
      {createMutation.isError ? <ErrorNotice error={createMutation.error} /> : null}
      {openMutation.isError ? <ErrorNotice error={openMutation.error} /> : null}
    </section>
  </div>;
};
