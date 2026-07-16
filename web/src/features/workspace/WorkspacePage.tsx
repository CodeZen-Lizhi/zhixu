import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type SyntheticEvent, useState } from "react";

import {
  createWorkspace,
  getWorkspace,
  scanWorkspace,
  type Workspace,
  type WorkspaceScan,
  WorkspaceApiError,
} from "../../api/workspace";
import { SystemStatusPage } from "../system-status/SystemStatusPage";

const workspaceStorageKey = "zhixu.active-workspace-id";

const storedWorkspaceID = () => window.localStorage.getItem(workspaceStorageKey) ?? "";

const formatBytes = (bytes: number) => {
  if (bytes < 1024) return `${String(bytes)} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
};

const ErrorNotice = ({ error }: { error: Error }) => (
  <div className="workspace-alert workspace-alert--error" role="alert">
    <strong>操作失败</strong>
    <span>{error.message}</span>
    {error instanceof WorkspaceApiError ? <code>{error.code}</code> : null}
  </div>
);

const WorkspaceSummary = ({ workspace }: { workspace: Workspace }) => (
  <section className="workspace-summary" aria-labelledby="workspace-title">
    <div>
      <p className="eyebrow">活动 Workspace</p>
      <h2 id="workspace-title">{workspace.name}</h2>
      <p className="workspace-path">{workspace.rootPath}</p>
    </div>
    <dl className="workspace-meta">
      <div><dt>Git 分支</dt><dd>{workspace.git.branch || "尚无分支"}</dd></div>
      <div><dt>HEAD</dt><dd className="hash-value">{workspace.git.head || "尚无提交"}</dd></div>
      <div><dt>工作区</dt><dd>{workspace.git.dirty ? "有未提交修改" : "干净"}</dd></div>
      <div><dt>版本</dt><dd>{workspace.version}</dd></div>
    </dl>
    {workspace.git.dirty ? (
      <div className="workspace-alert workspace-alert--warning" role="status">
        <strong>Git Dirty 警告</strong>
        <span>Workspace 已创建，但后续写回必须以当前 HEAD 和文件哈希重新校验。</span>
      </div>
    ) : null}
  </section>
);

const ScanTable = ({ scan }: { scan: WorkspaceScan }) => (
  <section className="scan-section" aria-labelledby="scan-title">
    <div className="section-heading">
      <div>
        <p className="eyebrow">只读扫描结果</p>
        <h2 id="scan-title">发现 {scan.count} 个受支持文件</h2>
      </div>
      <p>扫描会登记不可变版本元数据，但不修改原文件，也不代表已完成解析或索引。</p>
    </div>
    <div className="table-scroll">
      <table className="scan-table">
        <thead><tr><th>相对路径</th><th>类型</th><th>大小</th><th>SHA-256</th></tr></thead>
        <tbody>
          {scan.files.map((file) => (
            <tr key={`${file.relativePath}:${file.contentHash}`}>
              <td>{file.relativePath}</td>
              <td>{file.mediaType}</td>
              <td>{formatBytes(file.byteSize)}</td>
              <td className="hash-value">{file.contentHash}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  </section>
);

export const WorkspacePage = () => {
  const queryClient = useQueryClient();
  const [workspaceID, setWorkspaceID] = useState(storedWorkspaceID);
  const [name, setName] = useState("");
  const [rootPath, setRootPath] = useState("");
  const [initializeGit, setInitializeGit] = useState(false);
  const [scan, setScan] = useState<WorkspaceScan>();

  const workspaceQuery = useQuery({
    queryKey: ["workspace", workspaceID],
    queryFn: ({ signal }) => getWorkspace(workspaceID, signal),
    enabled: workspaceID !== "",
    retry: false,
  });

  const createMutation = useMutation({
    mutationFn: createWorkspace,
    onSuccess: (workspace) => {
      window.localStorage.setItem(workspaceStorageKey, workspace.id);
      setWorkspaceID(workspace.id);
      setScan(undefined);
      queryClient.setQueryData(["workspace", workspace.id], workspace);
    },
  });

  const scanMutation = useMutation({
    mutationFn: scanWorkspace,
    onSuccess: setScan,
  });

  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    createMutation.mutate({ name, rootPath, initializeGit });
  };

  const workspace = workspaceQuery.data;

  return (
    <div className="workspace-page">
      <SystemStatusPage />

      <section className="workspace-section" aria-labelledby="workspace-setup-title">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Workspace 配置</p>
            <h2 id="workspace-setup-title">连接本地知识目录</h2>
          </div>
          <p>目录必须存在且位于 API 服务可访问的本机或服务器文件系统中。</p>
        </div>

        {workspaceID === "" ? (
          <form className="workspace-form" onSubmit={submit}>
            <label>
              <span>Workspace 名称</span>
              <input value={name} onChange={(event) => setName(event.target.value)} required />
            </label>
            <label>
              <span>根目录绝对路径</span>
              <input value={rootPath} onChange={(event) => setRootPath(event.target.value)} required placeholder="/Users/me/knowledge" />
            </label>
            <label className="checkbox-field">
              <input type="checkbox" checked={initializeGit} onChange={(event) => setInitializeGit(event.target.checked)} />
              <span>目录不是 Git 仓库时允许初始化</span>
            </label>
            <button type="submit" disabled={createMutation.isPending}>
              {createMutation.isPending ? "正在校验并创建…" : "创建 Workspace"}
            </button>
          </form>
        ) : null}

        {createMutation.isError ? <ErrorNotice error={createMutation.error} /> : null}
        {workspaceQuery.isPending && workspaceID !== "" ? <p>正在打开上次 Workspace…</p> : null}
        {workspaceQuery.isError ? (
          <>
            <ErrorNotice error={workspaceQuery.error} />
            <button type="button" className="secondary-button" onClick={() => {
              window.localStorage.removeItem(workspaceStorageKey);
              setWorkspaceID("");
            }}>清除本地引用并重新创建</button>
          </>
        ) : null}
        {workspace === undefined ? null : <WorkspaceSummary workspace={workspace} />}

        {workspace === undefined ? null : (
          <div className="workspace-actions">
            <button type="button" onClick={() => scanMutation.mutate(workspace.id)} disabled={scanMutation.isPending}>
              {scanMutation.isPending ? "正在安全扫描…" : "扫描受支持文件"}
            </button>
          </div>
        )}
        {scanMutation.isError ? <ErrorNotice error={scanMutation.error} /> : null}
      </section>

      {scan === undefined ? null : <ScanTable scan={scan} />}
    </div>
  );
};
