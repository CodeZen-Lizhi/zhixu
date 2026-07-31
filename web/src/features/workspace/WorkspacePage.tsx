import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type SyntheticEvent, useState } from "react";
import { Link } from "react-router-dom";

import { setActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";
import {
  createWorkspace,
  getWorkspace,
  scanWorkspace,
  type Workspace,
  WorkspaceApiError,
  type WorkspaceScan,
} from "../../api/workspace";
import { SystemStatusPage } from "../system-status/SystemStatusPage";

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
        <p className="eyebrow">安全摄取结果</p>
        <h2 id="scan-title">发现 {scan.count} 个受支持文件</h2>
      </div>
      <p>扫描会把原始字节捕获到受管不可变存储，不修改源文件，也不代表已完成解析或索引。</p>
    </div>
    <div className="table-scroll">
      <table className="scan-table">
        <thead><tr><th>相对路径</th><th>类型</th><th>大小</th><th>版本</th><th>SHA-256</th></tr></thead>
        <tbody>
          {scan.files.map((file) => (
            <tr key={`${file.relativePath}:${file.contentHash}`}>
              <td>{file.relativePath}</td>
              <td>{file.mediaType}</td>
              <td>{formatBytes(file.byteSize)}</td>
              <td>{file.contentArtifactCreated ? "新建不可变版本" : "复用已有版本"}</td>
              <td className="hash-value">{file.contentHash}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
    <section className="workspace-next-step workspace-next-step--scan" aria-labelledby="scan-next-step-title">
      <div>
        <p className="eyebrow">扫描后的下一步</p>
        <h3 id="scan-next-step-title">继续检查资料事实</h3>
        <p>本次扫描已返回 {scan.count} 个文件事实；资料的解析与索引状态仍以对应页面的服务端投影为准。</p>
      </div>
      <div className="workspace-action-row">
        <Link className="workspace-action-link workspace-action-link--primary" to="/documents">查看资料版本</Link>
        <Link className="workspace-action-link" to="/dashboard">进入工作台</Link>
      </div>
    </section>
  </section>
);

export const WorkspacePage = () => {
  const queryClient = useQueryClient();
  const workspaceID = useActiveWorkspaceId();
  const [name, setName] = useState("");
  const [rootPath, setRootPath] = useState("");
  const [existingWorkspaceID, setExistingWorkspaceID] = useState("");
  const [existingWorkspaceError, setExistingWorkspaceError] = useState<Error>();
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
      setActiveWorkspaceId(workspace.id);
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
      <header className="workspace-intro">
        <p className="brand-mark">知序 · ZHIXU</p>
        <h2>连接你的本地知识。</h2>
        <p>创建或打开 Workspace 后，再从真实目录扫描资料；运行状态与文件事实始终分别呈现。</p>
      </header>

      <section className="workspace-connection" aria-labelledby="workspace-setup-title">
        <div className="section-heading workspace-connection__heading">
          <div>
            <p className="eyebrow">Workspace 配置</p>
            <h2 id="workspace-setup-title">{workspaceID === "" ? "连接本地知识目录" : "当前工作区"}</h2>
          </div>
          <p>{workspaceID === "" ? "目录必须存在，并且可由 API 服务访问。" : "Workspace 已连接；同步通道与业务状态仍由各自的事实源负责。"}</p>
        </div>

        <div className="workspace-connection__body">
          <div className="workspace-connection__primary">
            {workspaceID === "" ? (
              <>
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
                <form className="workspace-open-form" onSubmit={(event) => {
                  event.preventDefault();
                  try {
                    setActiveWorkspaceId(existingWorkspaceID.trim());
                    setExistingWorkspaceError(undefined);
                  } catch (error: unknown) {
                    setExistingWorkspaceError(error instanceof Error ? error : new Error("Workspace ID 无效"));
                  }
                }}>
                  <label><span>已有 Workspace ID</span><input value={existingWorkspaceID} onChange={(event) => setExistingWorkspaceID(event.target.value)} placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" pattern="[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}" title="请输入规范 UUID" required /></label>
                  <button type="submit" className="secondary-button">打开已有 Workspace</button>
                </form>
              </>
            ) : null}

            {existingWorkspaceError === undefined ? null : <ErrorNotice error={existingWorkspaceError} />}
            {createMutation.isError ? <ErrorNotice error={createMutation.error} /> : null}
            {workspaceQuery.isPending && workspaceID !== "" ? <p role="status">正在打开上次 Workspace…</p> : null}
            {workspaceQuery.isError ? (
              <>
                <ErrorNotice error={workspaceQuery.error} />
                <button type="button" className="secondary-button" onClick={() => {
                  setActiveWorkspaceId("");
                }}>清除本地引用并重新创建</button>
              </>
            ) : null}

            {workspace === undefined ? null : (
              <>
                <WorkspaceSummary workspace={workspace} />
                <section className="workspace-next-step" aria-labelledby="workspace-next-step-title">
                  <div>
                    <p className="eyebrow">下一步</p>
                    <h3 id="workspace-next-step-title">扫描资料，或进入工作台</h3>
                    <p>扫描只捕获受支持文件的真实版本；不需要扫描时，可以直接查看已有工作状态。</p>
                  </div>
                  <div className="workspace-action-row">
                    <button type="button" onClick={() => scanMutation.mutate(workspace.id)} disabled={scanMutation.isPending}>
                      {scanMutation.isPending ? "正在安全扫描…" : "扫描受支持文件"}
                    </button>
                    <Link className="workspace-action-link" to="/dashboard">进入工作台</Link>
                  </div>
                </section>
              </>
            )}
            {scanMutation.isError ? <ErrorNotice error={scanMutation.error} /> : null}
          </div>

          <SystemStatusPage display="compact" />
        </div>
      </section>

      {scan === undefined ? null : <ScanTable scan={scan} />}
    </div>
  );
};
